# CLAUDE.md

## What is this

Universal control plane for managing LXC-based projects on Proxmox compute nodes. It provides a REST API for registering Proxmox nodes, defining project templates (e.g. STUDIO 23), and provisioning tenant instances. Built with Go, PostgreSQL, and Docker.

## How to run

```bash
docker compose up -d
```

The API starts on port 8080. PostgreSQL runs on 5432 with automatic migrations.

## How to run tests

```bash
go test ./...
```

## Project structure

```
controlplane/
  cmd/server/main.go          # Entry point: config, DB connect, migrations, HTTP server, graceful shutdown
  internal/
    config/config.go           # Environment-based configuration (DATABASE_URL, LISTEN_ADDR, LOG_LEVEL)
    database/database.go       # PostgreSQL connection pool (pgxpool)
    database/migrate.go        # Embedded SQL migrations via golang-migrate
    database/migrations/       # SQL migration files
    server/server.go           # chi router setup with all routes
    response/response.go       # JSON response helpers (JSON, Error, Decode)
    node/                      # Node CRUD (model, store, handler)
    project/                   # Project CRUD (model, store, handler)
    tenant/                    # Tenant CRUD (model, store, handler, handler_test)
    provisioner/               # Async LXC provisioning/deprovisioning (bounded concurrency, state machine)
    proxmox/                   # Proxmox VE API client (LXC lifecycle, node status, task polling)
    crypto/                    # AES-256-GCM encryption for API tokens
    health/                    # Health check endpoint with DB ping
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
- `github.com/jackc/pgx/v5` — PostgreSQL driver (pure Go, no CGO)
- `github.com/golang-migrate/migrate/v4` — Database migrations with embedded SQL

## Notes

- No ORM, raw SQL via pgx
- Structured logging with `log/slog` (JSON output)
- Graceful shutdown on SIGINT/SIGTERM (TODO: wire Provisioner.Shutdown())
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
