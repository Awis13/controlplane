FROM golang:1.25-alpine AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /controlplane ./cmd/server

FROM alpine:3.21

RUN addgroup -S app && adduser -S app -G app

COPY --from=builder /controlplane /usr/local/bin/controlplane

USER app

EXPOSE 8080

ENTRYPOINT ["controlplane"]
