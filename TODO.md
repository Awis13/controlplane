# TODO

## In Progress

## Up Next
- [ ] Wire Provisioner.Shutdown() to main.go SIGTERM handler — graceful shutdown on process exit. Reviewer noted this isn't wired yet. (small)
- [ ] Auth middleware — API key or JWT for all endpoints. Currently no authentication. (medium)
- [ ] Tenant CRUD for suspended state — admin suspend/resume endpoints + Proxmox stop/start. (medium)

## Backlog
- [ ] Stale Proxmox client cache eviction — clients map grows unbounded if nodes are removed. Add TTL or invalidation. (small)
- [ ] Node deletion guard — prevent deleting nodes that have active tenants. (small)
- [ ] Integration tests with real Postgres — current tests use mocks only. (medium)
- [ ] CI/CD pipeline with GitHub Actions — run tests + lint on PR. (medium)

## Known Issues
- [ ] TOCTOU between GetByID and SetDeleting in delete handler — mitigated by CAS in SQL but not eliminated. Low risk. (low)
- [ ] updated_at trigger depends on migration being applied — no trigger defined in current migrations. (low)

## Done
- [x] Tenant provisioning logic — async provision, sync deprovision, state machine, bounded concurrency, 86 tests (2026-02-24, PR #3)
- [x] Proxmox VE API client — LXC lifecycle, task polling, TLS over Tailscale (2026-02-23, PR #2)
- [x] Project scaffold — Go 1.24, chi router, pgx, Docker + Postgres 17, migrations (2026-02-23, PR #1)
