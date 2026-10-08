# Active Audit Remediation

This worklist tracks the code, production, and interface audit being addressed
on `main`. “Verified locally” does not mean deployed. Production changes,
retention cleanup, and destructive recovery remain separate operator actions.
Remove this worklist when the audit is closed and retain durable guidance in
the owning documentation.

| Area | Finding / required outcome | Status |
| --- | --- | --- |
| Monitoring | Restore scrape configuration, alert evaluation, service health, bounded metric labels, readable runtime project docs | Implemented and verified locally |
| Alert delivery | Hank notification inbox plus email; administrator GUI configuration; encrypted credentials and retryable reload | Implemented and verified locally |
| Backups | Encrypt attachment archives with the existing backup key, private permissions, authenticated safe extraction | Implemented and verified locally |
| Agent replies | Bind commands and transfer frames to authenticated Home, agent, and dispatched socket | Implemented; focused, race, and review checks passed |
| Transfers | Prevent slow/abandoned transfer queues from blocking the shared agent socket | Negotiated byte window, cancellation, disconnect cleanup and replay revocation verified; focused and race tests pass |
| Agent lifecycle | Replacement/unregistered socket cleanup must not mark a current connection offline | Implemented; replacement, heartbeat, cancellation, and concurrent lifecycle tests pass |
| Realtime UI | Recover subscriptions and refresh authoritative state after reconnect without replaying writes | Implemented; unit and browser recovery checks pass |
| Database | Bound connection pools and verify under concurrent load | Pool limits, cancellation under saturation, and metrics verified against PostgreSQL |
| File jobs | Persist exact agent ownership; reject forged events/replies and misrouted recovery | Migration 38 and exact-owner routing verified. Administrator attribution with a paginated historical queue verified through API, real PostgreSQL and desktop/mobile UI |
| Migrations | Preserve statements preceded by SQL comments | Parser corrected; fresh and applied schemas agree |
| Restore | Restore database and matching attachment bytes coherently | Journaled paired restore implemented; crash/failure review regressions, real container backup/restore, cloud file permissions, API and mobile confirmation verified |
| Installable apps | Define and enforce extension trust, environment, and process isolation | Restricted namespaces, seccomp, explicit broker grants and per-invocation cgroups implemented; independent review findings corrected and real sandbox tests pass; integrated Go, frontend, PostgreSQL, race and sandbox checks pass; deployment choice pending |
| Test integrity | Resolve seven existing PostgreSQL-backed failures without weakening platform contracts | Corrected invalid/stale fixtures; full PostgreSQL-backed Go suite passes |
| Production storage | Disk pressure, obsolete backup archives, retention alignment, and long-running rollback jobs | Read-only diagnosis completed; cleanup/recovery needs explicit operator authorization |
| Architecture and UI | Reduce coupling and correct demonstrated interface failures | Transfer handlers isolated from server composition; HA desktop spacing and permission messaging corrected; desktop/mobile browser checks pass |
| Release verification | Full Go/dashboard/database/operations checks; reconcile production source divergence; production verification after authorized deployment | Local Go, dashboard, migration and schema checks pass. Naming reconciliation and local revalidation completed; production rollout and external delivery acceptance pending |

Previously reproduced baseline failures: desktop enrollment public trust
fixture, expired browser identity session foreign key, MCP default-board
fallback, notes token attachment deletion, inherited MCP notebook exclusion,
desktop history state transition fixture, and migration baseline fixture.

## Approved Work In Progress

The owner approved restricted apps with explicit administrator grants; paired
database and attachment restore; negotiated download flow control; administrator
confirmation of historical job owners; and production naming reconciliation on
main. These choices do not authorize production deployment or data cleanup.

Naming reconciliation is implemented locally. Production migration 35 is
preserved byte-for-byte; the newer main assistant index is migration 39. Full
post-reconciliation Go, dashboard, database and monitoring checks pass. Download flow control, transport cancellation and historical owner administration
are implemented and verified. Paired restore passed a real disposable PostgreSQL
and cloud-container restore with matching attachment bytes and non-root file
access. Restricted app isolation passes real namespace, broker, revocation, detached-process
and resource-limit tests. Final recovery acceptance restored a migration-39 backup using current code,
waited for writable promotion, applied migration 40, recovered matching attachment
bytes, removed post-backup data and verified non-root cloud writes and readiness.
The baseline script now tests the original legacy schema.

## Remaining Boundaries

Restricted app execution requires Linux namespace support and delegated cgroup v2
controllers. Production uses Docker’s default confinement profile; enabling apps
requires the owner’s native-agent versus dedicated-container deployment choice.
Unsupported runtime environments fail closed while core agent functions remain
available. Legacy packages using direct network or filesystem access must migrate
to the broker in their owning repositories before being enabled.

The large assistant implementation, Agents page, and shared stylesheet remain
maintenance risks. The changes separate the transfer subsystem and connection
lifecycle and correct demonstrated UI behavior; they do not claim a complete
rewrite or a screen-by-screen accessibility certification. Further extraction
should follow service boundaries and measured regressions, preserving contracts.

Local checks use disposable PostgreSQL with real migrations. Production doctor
and release acceptance need the actual deployment environment, source
reconciliation, and deployment authorization. SMTP provider/mailbox acceptance
and real device push delivery remain external integration checks. Committing and pushing this remediation does not deploy it or authorize
production data cleanup.

## Local Validation Evidence (2026-09-05)

- Full `go test ./...` with disposable PostgreSQL and no skipped database suite; `make fmt`, `make tidy`, `make build` and `go vet ./...`.
- `make frontend-check`: 604 tests, TypeScript, dashboard/PWA and MCP builds, bundle budgets. Desktop/mobile browser checks cover permissions, historical owners and paired-restore confirmation.
- App authorization/grant/transfer race checks; real `make app-sandbox-test` proves default denial, explicit broker access, revocation of a running invocation, host isolation, detached descendant cleanup, resource exhaustion containment and the example SDK.
- Migration 40 applied; strict status, deep schema drift comparison and corrected fresh-install/legacy-baseline acceptance pass on disposable PostgreSQL.
- `make monitoring-test`, naming migration fixtures and Compose storage preflight fixtures pass. The local server image builds; Linux arm64 and macOS arm64 agents cross-compile. The real sandbox ran on Linux amd64. Linux arm64 runtime acceptance and macOS runtime acceptance were not performed; macOS app execution intentionally fails closed.
- Doctor was attempted: deployment checks are unavailable in this checkout because `.env.cloud` and `.env.agent` are absent. Production deployment, production doctor/release acceptance, actual SMTP mailbox delivery and physical-device push delivery are not claimed.

## Production Actions Still Requiring Authorization

The read-only production snapshot showed 84% disk usage with about 31 GiB free,
large Docker image/build caches, 30 legacy plaintext attachment archives (about
46 MiB), 16 archive candidates older than retained pgBackRest labels, and two
historical rollback-required file jobs. Counts must be refreshed immediately
before any approved action. Docker cache/image estimates overlap and must not be
summed as guaranteed reclaimable space.

The proposed rollout is to retain a verified database/attachment backup and
rollback image, run naming/topology preflight, apply versioned migrations, deploy
the reconciled cloud/agent/db-ops and monitoring changes, then execute the release
acceptance checks. Optional apps remain blocked until the owner chooses a native
agent or a dedicated container confinement profile and the affected app packages
are migrated. Historical job attribution remains an explicit administrator action.

The proposed storage cleanup first inventories exact unused build-cache/image
objects while retaining running and rollback images. Attachment archives can be
considered only against retained database backup labels after encrypted backup
and paired recovery acceptance. No production volumes, backups, file jobs or
archives have been deleted or altered by this work.

## Assistant-Index Upgrade Compatibility

A deployment that already applied the assistant index as migration 35 requires
explicit history reconciliation before naming migration 35 can run. The
maintenance workflow is owned by [naming migration](naming-migration.md). Its
check is read-only; its apply operation preserves the verified existing index
and moves only the matching ledger record to 39 in one transaction. Normal
startup and migration checksum checks remain strict. This follow-up passes full PostgreSQL-backed Go tests, focused race tests, build
and vet checks, plus the packaged maintenance command with a legacy environment
file. The reconciled upgrade schema matches a fresh migration-40 schema.
Production reconciliation and rollout have not been performed; the target host
for the reported conflict has not yet been confirmed.
