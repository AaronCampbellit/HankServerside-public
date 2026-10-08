# Hank Release Checklist

Use this checklist for every tagged release. A release is not stable until every
gate below is green and the evidence locations are recorded in the release notes.

## 1. Version And Tag

- Pick the version (`vMAJOR.MINOR.PATCH`, e.g. `v1.0.0-rc1`).
- Build images with the version stamped so `/dashboard` bootstrap and
  `cloud_runtime.version` report it:

```bash
export HANK_BUILD_VERSION="$(git describe --tags --always --dirty)"
export HANK_SOURCE_COMMIT="$(git rev-parse HEAD)"
docker compose --env-file .env.cloud build cloud
```

The image build stamps the same full commit into the bundled Hank Agent. Any
separately packaged Linux agent release must retain Go VCS metadata or stamp
`main.buildSourceCommit` as `hank-source-commit:<full-commit>`; unsigned or
unstamped artifacts fail release verification.

- `scripts/bootstrap-first-run.sh` exports both variables automatically when it
  runs inside a git checkout.
- Tag only after the full gate passes: `git tag vX.Y.Z && git push origin vX.Y.Z`.
- When a release changes any MCP tool name, schema, annotation, resource, discovery capability, or
  supported transport behavior,
  bump `mcpServerVersion` in `internal/cloud/mcp_server.go` in the same change. Record the old and
  new `hank-mcp` identities in the release notes.

## 2. Local Gate

```bash
make fmt
make tidy
go build ./...
go vet ./...
go test ./...
npm --prefix web/dashboard run check
make monitoring-test
```

Note: PostgreSQL-backed tests skip unless `HANK_TEST_DATABASE_URL` is
set. Run the full suite against a real Postgres (locally or on the demo server)
before release; a run where the store/cloud DB tests skipped does not count.

### Assistant execution gate

- Run the durable-task evaluation described in [HankAI](docs/hankai.md#automated-evaluation)
  against an isolated synthetic Home and the exact provider/model being enabled.
  Record sample counts, verified outcomes, active/total latency, tokens, and
  cost provenance; unknown cost stays unknown.
- Require passing deterministic safety/recovery tests and no observed
  unauthorized, unapproved, wrong-target, duplicate, or falsely verified action.
  Live supported scenarios must reach at least 95% completion across three
  repetitions. Do not infer provider parity from transport mocks.
- Initially enable `HANK_ASSISTANT_EXECUTION_ENABLED` only on the test deployment.
  New supported dashboard chats default to the agent loop; existing legacy
  conversations and clients retain their versioned behavior.
- Preserve the Hank Agent operation-receipt volume and existing database and
  attachment volumes. Confirm agent receipt identity before testing writes.
- Disabling new-task admission must leave recovery of existing tasks running.
  Once v2 tasks exist, rollback requires a compatible recovery worker; do not
  downgrade to an image that cannot interpret them or delete populated tables.
- Perform the database/attachment backup and restore-proof gates below before
  deployment, then check approval, rejection, refresh recovery, stop, follow-up,
  source links, and verified versus uncertain outcomes with synthetic targets.

## 3. Database Gate

```bash
scripts/production-naming-preflight.sh
make migrate-status
make schema-drift-check
```

- Fresh-install proof: `scripts/migration-baseline-validation.sh`.
- No schema change ships outside `internal/migrations/sql`.
- If migration 35 is the assistant resume index, use the exact-history reconciliation in `docs/naming-migration.md` during the maintenance window before the naming migration. A checksum mismatch is never permission to bypass migration validation.
- Apply pending migrations through 50; preserve monitoring history, encrypted delivery settings, exact file-job ownership, the assistant index, file-search catalog, and app permission metadata when planning rollback. Migration 35 must match the production naming migration checksum. Migration 48 rollback refuses infinite grants; migration 49 rollback refuses non-Linux enrollments. Migration 50 rollback removes only per-user dismissal preferences.
- Verify a new `.tar.gz.age` attachment backup with the existing repository key, wrong-key/corruption rejection, and recovery of an older archive. Confirm file sizes and SHA-256 against the restored database. Existing plaintext archives require a separately approved retention/migration decision.
- Prove primary restore publishes the selected database and matching attachment bytes together, preserves recoverable originals, and restores the old pair after an interrupted swap. Confirm non-root cloud read/write access after both success and rollback.
- The production naming preflight must resolve the effective Compose topology
  and match every running database, restore, pgBackRest, and attachment mount.
- A manual backup must pass the live attachment integrity preflight before
  pgBackRest begins, then a restore proof must match its database rows and
  attachment files.

## 4. Secret Storage Gate

```bash
docker compose --env-file .env.cloud run --rm cloud \
  /usr/local/bin/hank-server secrets status --strict
```

- Must exit clean. If legacy plaintext rows are reported, run
  `secrets reencrypt` with `HANK_SECRET_ENCRYPTION_KEY` set, then re-run
  the strict status check.
- `scripts/doctor.sh` runs this check automatically when the stack is up.

## 5. Live Validation Gate (demo or staging server)

Run the sequence from `docs/demo-validation.md`:

```bash
scripts/doctor.sh
promtool check rules ops/prometheus/alerts.yml
promtool test rules ops/prometheus/alerts.test.yml
scripts/restart-validation.sh
scripts/file-safety-validation.sh
scripts/schema-drift-check.sh
scripts/migration-baseline-validation.sh
go run ./tools/livevalidation
go run ./tools/adminvalidation
scripts/restore-proof.sh
scripts/metrics-assert.sh
```

Record report paths (under `data/`) in the release notes; the artifacts stay
untracked. Restore proof must be newer than 7 days at release time.

For MCP tool-surface or transport releases, also preserve focused real HTTP test evidence for
modern discovery, initialize-era negotiation and post-init header enforcement, no-store headers,
acknowledgment-first `subscriptions/listen`, controlled tool-list notification delivery, token
lifecycle closure, and graceful shutdown. The deployed canary must prove automatic reconnect and
rediscovery without connector recreation.

## 6. Operator Upgrade Notes

Include in every release's notes:

- **Migrations**: run `make migrate-up` (or let bootstrap do it) before starting
  the new cloud image; verify with `make migrate-status`.
- **Agents older than the header-auth migration**: agents that still authenticate
  with URL query tokens are rejected. Upgrade path: create a new setup token in
  the dashboard, regenerate `.env.agent` (`scripts/install-agent-env.sh`),
  restart the agent, confirm it shows online, then revoke the old token. See
  `docs/runbooks/agent-offline.md`.
- **Secret encryption**: deployments that ever ran without
  `HANK_SECRET_ENCRYPTION_KEY` must pass the strict secrets status check
  (section 4) before the release is considered applied.
- **Env file permissions**: `.env.cloud` and `.env.agent` must be `chmod 600`.

## 7. Monitoring Gate

- Prometheus and Alertmanager are up (`docker compose --profile monitoring ps`)
  and `ops/prometheus/alerts.yml` loads without errors.
- `python3 scripts/check-monitoring.py` passes against the deployed monitoring stack.
- Configure the scrape credential and verify the managed configuration volume;
  run `make monitoring-test`. Verify a real test alert and its recovery notice
  in both the configured inbox audience and the selected mailbox using Settings → Notifications. Verify later edits, password retention/removal, administrator access, CSRF, restart persistence, and pending-application retry.
- `scripts/metrics-assert.sh` passes against the deployed instance.
- When Web Push is enabled, `scripts/doctor.sh` reports complete VAPID
  configuration and a staging browser proves enrollment, background delivery,
  same-origin click-through, inbox read state, category suppression, and logout
  cleanup. Prove installed-PWA delivery on iOS when iOS is in release scope.

## 8. Native Remote Desktop V1 Gate

- `scripts/remote-desktop-load-validation.sh --contract-only` passes.
- `scripts/remote-desktop-acceptance.sh --contract-only` executes every explicit portable mapping and records integration/physical-only rows as not run. It emits a complete JSONL contract receipt, not native-device evidence.
- Require metadata-only native-device evidence proving the eight-hour longevity, eleven-point native matrix, compatibility, signed package upgrade/rollback/uninstall, permission/elevation, and resource-baseline requirements in `docs/remote-desktop/v1-acceptance.md`.
- First privileged activation proves mutually authenticated legacy-credential transfer or explicit re-enrollment, privileged-authority readiness, and retirement of the old reusable credential only after the exact server and agent are bound by the committed receipt.
- Windows MSI and macOS pkg are signed as one compatible GUI/authority/host unit; macOS notarization is accepted and stapled. Unsigned-test artifacts do not satisfy this gate.
- No database, signing, packaging, or physical-device skip may be treated as release readiness.

## 9. Privileged Linux RMM Gate

- Migration 30 is applied and strict migration/schema-drift checks pass.
- The configured Linux release manifest has a valid Ed25519 signature and both served architecture binaries match their signed sizes and SHA-256 values.
- A fresh one-machine installer link succeeds once and replay is rejected.
- The demo root service is active/enabled and proves metrics, synthetic root-file operations, root shell/PTY, process signal, package install/remove, and synthetic systemd lifecycle/logs.
- Forced credential rotation succeeds, the former credential is rejected after confirmation, and immediate revocation disconnects the agent when exercised.
- Exact bootstrap and device secrets are absent from application/proxy logs, journald, process arguments, audit payloads, retained evidence, and database text columns.
- No privileged Linux deployment is promoted from demo until this gate is fully green.

Before replacing a live image, compare its OCI `org.opencontainers.image.revision`
and `org.opencontainers.image.version` labels with the approved source. Older
images may lack these labels; inspect their bundled code-reference provenance
and preserve the running image ID as rollback evidence. A dirty build version
is review evidence, not a reproducible release identifier.

## Restricted App Gate

Run `make app-sandbox-test` on a Linux Docker host. It builds disposable images,
executes the real containment and broker probes inside an outer resource-limited
container, and removes its own images. The test’s permissive outer namespace
setup is strictly for local acceptance, never a production profile.

- Choose the native-agent or dedicated-container deployment approach before enabling optional apps. Verify namespace, seccomp and delegated cgroup v2 support using the prerequisites in `docs/deployment.md`; never weaken the production container to an unconfined or privileged profile.
- Prove denied-by-default network and files access, exact target grants, separate read/write permissions, package/target change invalidation and cancellation after revocation.
- Prove host credentials and sockets are absent, detached descendants stop, and memory/process limits hold in a separately resource-limited disposable environment. Unsupported environments must refuse execution.
- Migrate separately owned packages to the broker in their owning repositories. An incompatible package must not prevent other valid packages from loading.

## Third-party materials

Build source distributions with `make distribution` and ship its accompanying
notices and MPL-covered source alongside the binaries. The Go application
Dockerfiles carry those files under `/app/`. Dashboard build output includes
`assets/THIRD_PARTY_NOTICES.txt`; preserve it and the standalone MCP HTML's
embedded notices. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for the
reviewed scope, source access, and the bounded SMB dependency update policy.
Audit actual container/base-image and external agent installer contents before
publishing those releases; application notices do not clear those extra packages.
