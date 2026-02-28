# CLAUDE.md

## What is this

Universal control plane for managing LXC-based projects on Proxmox compute nodes. It provides a REST API for registering Proxmox nodes, defining project templates (e.g. STUDIO 23), and provisioning tenant instances. Built with Go, PostgreSQL, and Docker.

## How to run

```bash
docker compose up -d
```

The API starts on port 8080 (bound to 127.0.0.1 by default). PostgreSQL runs on 5432 with automatic migrations.

For first-time setup, set `SETUP_TOKEN` env var — required for initial WebAuthn passkey registration.

## How to run tests

```bash
go test ./...
```

## Project structure

```
controlplane/
  cmd/server/main.go          # Entry point: config, DB connect, migrations, HTTP server, graceful shutdown
  internal/
    config/config.go           # Environment-based configuration (DATABASE_URL, LISTEN_ADDR, LOG_LEVEL, SETUP_TOKEN)
    database/database.go       # PostgreSQL connection pool (pgxpool)
    database/migrate.go        # Embedded SQL migrations via golang-migrate
    database/migrations/       # SQL migration files
    server/server.go           # chi router setup, security headers (HSTS, CSP strict, X-Frame-Options)
    response/response.go       # JSON response helpers (JSON, Error, Decode)
    node/                      # Node CRUD (model, store, handler) + Proxmox client cache invalidation
    project/                   # Project CRUD (model, store, handler)
    tenant/                    # Tenant CRUD (model, store, handler, handler_test)
    provisioner/               # Async LXC provisioning/deprovisioning (bounded concurrency, state machine)
    proxmox/                   # Proxmox VE API client (LXC lifecycle, node status, task polling)
    crypto/                    # AES-256-GCM encryption for API tokens
    health/                    # Health check endpoint with DB ping
    admin/                     # Admin UI — WebAuthn auth, HTMX dashboard, node/project/tenant management
    admin/static/              # Alpine.js, HTMX, CSS, webauthn.js
    admin/templates/           # Go html/template files
    audit/                     # Audit logging (fire-and-forget DB inserts)
  docker-compose.yml           # PostgreSQL + controlplane services
  Dockerfile                   # Multi-stage build (golang:1.24-alpine -> alpine:3.21)
```

## API endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Health check with DB ping |
| GET | `/api/v1/nodes` | List all nodes |
| POST | `/api/v1/nodes` | Register a node |
| GET | `/api/v1/nodes/{nodeID}` | Get a single node |
| DELETE | `/api/v1/nodes/{nodeID}` | Remove a node |
| GET | `/api/v1/projects` | List project types |
| POST | `/api/v1/projects` | Create a project type |
| GET | `/api/v1/tenants` | List tenants |
| POST | `/api/v1/tenants` | Create a tenant (returns 202, provisions async) |
| GET | `/api/v1/tenants/{tenantID}` | Get a single tenant |
| DELETE | `/api/v1/tenants/{tenantID}` | Deprovision and delete a tenant |

## Dependencies

- `github.com/go-chi/chi/v5` — HTTP router
- `github.com/go-chi/httprate` — IP-based rate limiting
- `github.com/jackc/pgx/v5` — PostgreSQL driver (pure Go, no CGO)
- `github.com/golang-migrate/migrate/v4` — Database migrations with embedded SQL
- `github.com/go-webauthn/webauthn` — WebAuthn/passkey authentication
- `github.com/justinas/nosurf` — CSRF protection

## Security

- WebAuthn passkey auth for admin UI (SETUP_TOKEN required for first registration)
- Bearer token auth for API (constant-time compare)
- CSRF protection (nosurf) with SameSite=Strict cookies
- CSP: `script-src 'self'` (no unsafe-inline), HSTS, X-Frame-Options DENY
- Rate limiting: 10 req/min on admin login, 100 req/min on API
- ReadHeaderTimeout 5s (Slowloris protection)
- 1MB body limit on all endpoints
- Proxmox client cache invalidated on token rotation
- DB-first state transitions with rollback for suspend/resume
- Provisioner shutdown with 10s timeout on SIGTERM
- Docker: runs as root (NET_ADMIN required for WireGuard), network_mode: host, bound to Tailscale IP

## Notes

- No ORM, raw SQL via pgx
- Structured logging with `log/slog` (JSON output)
- Graceful shutdown on SIGINT/SIGTERM with provisioner drain
- Migrations auto-run on startup
- API token field excluded from JSON responses (`json:"-"`)
- Proxmox API client uses InsecureSkipVerify (self-signed certs over Tailscale)
- POST to Proxmox uses `application/x-www-form-urlencoded` (not JSON)
- Async Proxmox operations return Task with Wait() for polling
- Tenant provisioning: async goroutine with 10-max concurrency semaphore
- Tenant state machine: provisioning → active/error, active/error → deleting → deleted
- State transitions enforced at SQL level (WHERE status = expected) for CAS safety
- Atomic RAM reservation on nodes (`allocated_ram_mb + N <= total_ram_mb`)
- Error messages in tenant records are sanitized (no internal details)
