# Hank Platform Agent Guide

## Project Authority

`HankServerside` is the canonical backend and platform for Hank. It owns the
server behavior, shared protocol, persistence, dashboard/PWA, core services,
managed-agent control plane, and installable-app runtime.

Other Hank repositories—including native, desktop, and mobile clients,
standalone agents, and installable apps—consume or extend this platform. When a
client conflicts with an established platform contract, fix the client in its
owning repository unless product direction explicitly requires a versioned
platform change.

## Product and Architecture Model

- Hank is the product; a Hank Agent runs on a managed machine and connects
  outbound, owning its local credentials and local-network access.
- The dashboard/PWA is a first-party Hank interface served by HankServerside.
- Clients and MCP integrations consume stable platform contracts.
- `.hankapp` packages are optional extensions and do not replace core services.
- One self-hosted deployment represents one Home, which may have multiple users
  and Hank Agents.
- One primary agent owns default home routing; worker agents provide explicitly
  targeted machine capabilities.
- Public clients use HankServerside HTTPS/WebSocket APIs.
- PostgreSQL is the durable server store. Schema changes use embedded versioned
  migrations, status, and drift checks.
- JSON over HTTPS/WebSocket remains the shared protocol unless measured
  requirements justify a versioned alternative.

## Repository Scope

Build and operate the cloud services, Hank Agents, dashboard/PWA, persistence,
shared protocols, storage operations, and installable-app runtime. Define stable
client, integration, and agent contracts; provide the core capabilities
described in `docs/product.md`; and add focused tests, telemetry, runbooks, and
release evidence.

Do not:

- implement interface-specific behavior owned by another client repository
- weaken a stable platform contract to preserve a client workaround
- expose SMB, Home Assistant, local filesystems, or other raw local protocols
  to the public internet
- design around a VPN requirement
- introduce multi-home SaaS or multi-node cloud assumptions without explicit
  product direction

## Working Safely

Before editing:

- inspect the branch, worktree status, and relevant diff
- preserve unrelated and uncommitted user work
- identify the owning layer before changing a shared contract
- inspect current code before trusting prose in an old document
- never hide schema mutation in startup or handler code
- do not modify another Hank repository unless explicitly included
- do not commit, stage, push, tag, publish, or deploy unless explicitly asked

Complete every task in the approved plan or active milestone referenced by the
current task before handing off. An intermediate passing batch is progress, not
completion. Stop early only for a blocker that requires human review, new
authority, unavailable access, or an external state change after safe
alternatives are exhausted.

## Security Gate

For auth, routing, files, storage, settings, agent commands, tokens, remote
desktop, or external calls, verify:

- routes authenticate unless intentionally public
- server-side authorization enforces exact user, Home, role, agent, and token
  scope
- cookie-authenticated writes require CSRF; bearer and WebSocket flows never
  put secrets in query strings
- tokens, passwords, OAuth credentials, push credentials, private file
  contents, screen/input/clipboard content, and ciphertext are not logged
- stored secrets use existing encryption or hashing helpers
- file paths are cleaned, symlink-resolved, and contained inside configured
  roots before every operation
- destructive actions use existing confirmation/action-token/audit patterns
- agent and release events match the authenticated connection, Home, agent,
  rollout, and assignment
- `/healthz` and `/readyz` may be public; sensitive metrics remain
  authenticated or internal

See `docs/security.md` for the full security guidance.

## Database Gate

- Put schema changes only in `internal/migrations/sql` through the repository
  migration workflow.
- Define defaults/nullability, indexes/constraints, backfill, compatibility,
  and rollback boundaries.
- Prefer database constraints for important integrity rules.
- Update store reads/writes, APIs, tests, migration status, schema drift, and
  docs together.
- Avoid destructive migrations without a staged compatibility and backup plan.
- PostgreSQL-backed tests that skip without `HANK_TEST_DATABASE_URL` do not
  count as full database validation.

## Cleanup Gate

- Identify every reader, writer, deployment reference, test, script, and link
  before removing a compatibility path.
- Remove stale behavior coherently across code, tests, docs, scripts, and
  operator instructions.
- Do not auto-clean production data or schemas.
- Do not delete material merely because it is old; prove it is superseded or
  consolidate any current guidance first.

## Configuration

Runtime environment files are `.env.cloud` and `.env.agent`. Treat them as
sensitive: never commit, print, or copy their values into tests, logs, docs, or
handoffs. `docs/deployment.md` owns supported configuration and must stay aligned
with `internal/config`, Compose, and setup scripts.

## Repository Map and References

- `cmd/`: cloud, Hank Agent, and database-operations entry points
- `internal/cloud`: auth, APIs, routing, relay, dashboard, MCP, app runtime, and
  remote desktop; start at `internal/cloud/server.go` and follow the owning
  handler
- `internal/agent`: outbound connection and local capability adapters; start at
  `internal/agent/client.go`
- `internal/protocol`: versioned cloud/agent wire contract in
  `internal/protocol/messages.go`
- `internal/store` and `internal/migrations`: PostgreSQL persistence and
  versioned schema workflow
- `internal/storageops`, `internal/maintenance`, and
  `internal/observability`: lifecycle and operations
- `web/dashboard`: React/Vite/TypeScript dashboard and PWA; follow the
  corresponding server API for backend behavior
- `schemas`: versioned external compatibility schemas
- `scripts`, `tools`, and `ops`: setup, validation, administration, release,
  managed-agent, and monitoring tooling

Current guidance and ownership:

- Product direction and current technical identifiers: `docs/product.md`
- Architecture: `docs/architecture.md`
- Active documentation catalog: `docs/README.md`
- Deployment and configuration: `docs/deployment.md`
- Security: `docs/security.md`
- Release work: `RELEASE.md` and `docs/demo-validation.md`
- App runtime, MCP, Notes/Kanban, and PWA/offline: `docs/app-platform.md`,
  `docs/mcp.md`, `docs/notes.md`, and `docs/pwa.md`
- One-time operator naming upgrade: `docs/naming-migration.md`

## Development and Validation

Use the smallest relevant check while developing, then broaden in proportion to
risk.

- Always use the demo server when available for live integration, end-to-end,
  and release validation, alongside required local checks. Follow
  `docs/demo-validation.md` for procedures.
- Every authorized production deployment includes updating demo to the same
  release. Follow `RELEASE.md` for deployment and verification procedures.

- Go/protocol changes: `make fmt`, targeted tests, `go test ./...`, and
  `make build`; also run `make tidy` for whole-platform work.
- Dashboard changes: targeted tests, `make frontend-test`,
  `make frontend-check`, and `make frontend-build`.
- Database changes: store tests, `make migrate-status`, and
  `make schema-drift-check`.
- Auth/routing/files/storage/agent/secret changes: focused security and failure
  coverage.
- Deployment/release changes: `scripts/doctor.sh` and the applicable release
  gate when access exists.
- Documentation-only changes: verify commands, paths, links, terminology,
  project-doc discovery, and `git diff --check`.

## Documentation Policy

- Current-state docs describe behavior, not implementation history.
- Remove completed plans and checklists once durable guidance is folded into
  canonical docs; Git history is the archive.
- Product prose uses Hank, HankServerside, and Hank Agent.
- Current documentation and runtime code use canonical identifiers. Use
  `docs/product.md` for current identifiers and `docs/naming-migration.md` only
  for the one-time operator upgrade; retired names there explain that upgrade.
- One document owns each subject; avoid copying long inventories.
- Route, configuration, schema, deployment, security, and ownership changes
  update the owning doc in the same change.

Before reporting completion, state security impact, database/migration impact,
validation performed, and every skipped check.
