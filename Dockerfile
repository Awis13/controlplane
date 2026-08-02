# Both bases are pinned by digest so a rebuild produces the same image. The tag
# beside each one is what the digest resolved to when it was taken; update both
# together. Digests came from docker pull followed by docker image inspect, so
# they are the multi-arch index digests and work on any architecture.

# golang:1.25-alpine — go1.25.12, matching the go 1.25.0 directive in go.mod.
FROM golang@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /controlplane ./cmd/server

# alpine:3.24 — 3.24.1, the current stable series. Was 3.21.
FROM alpine@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

RUN apk --no-cache add ca-certificates tzdata wireguard-tools

COPY --from=builder /controlplane /usr/local/bin/controlplane

EXPOSE 8080

# This container runs as root, which is not free, so here is why and what
# changing it would take.
#
# The service shells out to wg to program WireGuard peers, which needs
# CAP_NET_ADMIN, and docker-compose grants it with cap_add. A capability
# granted that way lands in the container's bounding set: the kernel hands it
# to a root process automatically but to no other user, so adding a USER line
# here on its own would leave wg with no capabilities at all. Measured, not
# assumed: as root, wg reports "No such device" for a missing interface; as a
# non-root user without file capabilities, "Operation not permitted".
#
# Running unprivileged is achievable and was tested. Installing libcap and
# running setcap cap_net_admin+ep on /usr/bin/wg restores exactly that one
# capability to that one binary, after which an unprivileged user gets "No such
# device" again. That is three lines in this file.
#
# What stops it is outside this file. SSH_KEY_PATH defaults to
# /root/.ssh/id_ed25519 and operators mount their key there read-only. The
# directory is 0700 and a private key is 0600, both owned by root, so a
# non-root process can read neither. The SSH client now parses its key when it
# is constructed, so an existing deployment would come up with LXC
# provisioning silently disabled. Dropping root therefore means changing the
# default key path and having every operator re-mount and re-permission their
# key: a migration, not a Dockerfile change.
ENTRYPOINT ["controlplane"]
