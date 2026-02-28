FROM golang:1.24-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /controlplane ./cmd/server

FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata wireguard-tools

COPY --from=builder /controlplane /usr/local/bin/controlplane

EXPOSE 8080

ENTRYPOINT ["controlplane"]
