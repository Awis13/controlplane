# TODO

## In Progress

## Up Next
- [ ] Extract `internal/validation/` package — deduplicate nameRegexp, subdomainRegexp, reservedSubdomains, IP validation between admin and API handlers. (medium)
- [ ] Split admin handler by domain — 1420 lines, extract dashboard/nodes/projects/tenants/settings into sub-handlers or separate files. (large)
- [ ] Unify tenant lifecycle — single service layer for create/delete/suspend/resume used by both admin and API handlers. (large)
- [ ] Admin tenant list pagination — loads all tenants in memory + filters in Go. Use ListPaginated like API handler. (small)
- [ ] Audit auth events — log WebAuthn login/register/credential-delete to audit_log. (small)
- [ ] Proxmox client cache TTL — add 15min expiry instead of caching forever. (small)

## Backlog
- [ ] Server-side session store — replace cookie-only sessions with DB-backed sessions for proper revocation. (medium)
- [ ] Proxmox TLS cert pinning — replace InsecureSkipVerify with CA cert pinning per node. (medium)
- [ ] Integration tests with real Postgres — current tests use mocks only. (medium)
- [ ] CI/CD pipeline with GitHub Actions — run tests + lint on PR. (medium)
- [ ] Status string constants — extract "active"/"suspended"/"error" to shared const.go. (small)
- [ ] CSP nonce for inline scripts — move inline JS to separate files. (small)

## Known Issues
- [ ] TOCTOU between GetByID and SetDeleting in delete handler — mitigated by CAS in SQL. (low)
- [ ] HTTP 286 in tenantRow — non-standard status code, HTMX-specific hack. (low)

## Done
- [x] Security hardening — CSRF (nosurf), rate limiting (httprate), security headers, session TTL 8h, body size limits, UUID validation, provisioner panic recovery (2026-02-27)
- [x] Phase 2 Admin UI — layout, dashboard, detail pages, filters, audit/settings pages, toast system (2026-02-27)
- [x] Phase 5.2 Audit wiring — all CRUD handlers log to audit_log (2026-02-27)
- [x] Phase 5.1 Audit log package — internal/audit/ (2026-02-27)
- [x] Phase 1 Backend — shutdown wiring, node/project/tenant update, suspend/resume, pagination, deletion guards, migration 000004 (2026-02-26)
- [x] Tenant provisioning — async provision, sync deprovision, state machine, 86 tests (2026-02-24, PR #3)
- [x] Proxmox VE API client — LXC lifecycle, task polling (2026-02-23, PR #2)
- [x] Project scaffold — Go 1.24, chi router, pgx, Docker + Postgres 17 (2026-02-23, PR #1)
