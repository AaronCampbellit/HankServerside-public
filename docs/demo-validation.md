# Hank Demo Validation

This document keeps demo and staging validation separate from product runtime
behavior. The validation harness is committed because it proves release
readiness, but secrets, live host details, and generated reports stay private
and untracked.

## Committed Validation Files

These files are safe to keep in the repository with the platform code:

- `tools/livevalidation/main.go`: end-to-end live app, agent, Home Assistant, and file-flow validation.
- `tools/adminvalidation/main.go`: admin UI/API validation that protected browser routes serve the React shell, protected React bootstrap returns required objects/arrays, and current Settings > Backups audit/query telemetry, Apps, File Server file jobs, and query telemetry APIs keep their dashboard contracts.
- `tools/loadtest/loadtest_test.go`: single-home target load scenarios and JSON report output.
- `scripts/restart-validation.sh`: restart-recovery test wrapper.
- `scripts/file-safety-validation.sh`: file policy and managed-job safety wrapper.
- `scripts/migration-baseline-validation.sh`: fresh DB plus baseline validation.
- `scripts/schema-drift-check.sh`: live schema drift comparison.
- `scripts/scale-validation.sh`: synthetic 1M file-index, 100k note, and attachment-size fixture.
- `scripts/production-load-report.sh`: load test plus resource report.
- `scripts/backup-during-traffic.sh`: backup and restore-test while load is running.
- `scripts/restore-proof.sh`: restore proof report against `postgres-restore`.
- `scripts/query-telemetry-report.sh`: top query report from `pg_stat_statements`.
- `scripts/metrics-assert.sh`: authenticated metrics coverage assertion.
- `scripts/bootstrap-first-run.sh`: fresh-server first boot for demo and production-like installs.
- `scripts/doctor.sh`: post-bootstrap and post-update health check.

## Demo-Only Private Inputs

Do not commit these:

- `.env.cloud`
- `.env.agent`
- any Home Assistant token
- any SMB username/password or share-specific secret
- any Cloudflare tunnel token or one-off tunnel command
- any demo host SSH key, known-host file, or password
- any live session token used by validation tools
- generated `data/` report artifacts

The repository `.gitignore` keeps `.env.*` and `data/` out of source control. Keep any demo-specific credentials in the operator's private password manager or private server notes.

## Demo Environment Variables

Use environment variables to bind the generic validation tools to a specific demo server. Do not hard-code demo hostnames, LAN IPs, share names, or tokens into source files.

Common variables:

```bash
export HANK_LIVE_BASE_URL="https://your-demo-host.example.com"
export HANK_LIVE_SESSION_TOKEN="<private session token>"
export HANK_LIVE_SOURCE_ONE="replace-with-first-demo-source-id"
export HANK_LIVE_SOURCE_TWO="replace-with-second-demo-source-id"

export HANK_LOADTEST_BASE_URL="$HANK_LIVE_BASE_URL"
export HANK_LOADTEST_SESSION_TOKEN="$HANK_LIVE_SESSION_TOKEN"
export HANK_LOADTEST_FILE_SOURCE="$HANK_LIVE_SOURCE_ONE"
export HANK_LOADTEST_FILE_SOURCE_TWO="$HANK_LIVE_SOURCE_TWO"
```

For local compose validation, also set:

```bash
export COMPOSE_PROJECT_NAME="hank_validation"
export HANK_CLOUD_ENV_FILE=".env.cloud"
```

## Durable Assistant Tasks

Use the [HankAI outcome evaluation](hankai.md#automated-evaluation) for the
execution-v2 release gate. Run it only against an isolated synthetic Home.
The opt-in cloud test provides a disposable database and exercises the actual
HTTP API and worker with the configured model. Deterministic fake-agent tests
cover device/file/service failures and recovery without affecting real targets.
Keep reports private. Follow [RELEASE.md](../RELEASE.md#assistant-execution-gate)
for rollout, backup/restore proof, and recovery-compatible rollback.

## HankAI Local Model Eval

For the current demo setup, the Hank server can test against the LAN Ollama
instance at:

```bash
export HANK_OLLAMA_BASE_URL="http://192.168.86.158:11434"
export HANK_OLLAMA_CHAT_MODEL="gemma4:12b"
export HANK_OLLAMA_EMBEDDING_MODEL="nomic-embed-text"
```

The current Ollama instance must list `gemma4:12b` and
`nomic-embed-text:latest`. The generic deployment default remains `llama3.1`,
so this demo must explicitly select `gemma4:12b` in `.env.cloud` or AI Settings;
an installed model is not selected automatically.

Before running the eval harness, verify the demo host and the running cloud
container can reach Ollama:

```bash
curl -fsS http://192.168.86.158:11434/api/tags
docker compose --env-file .env.cloud exec cloud sh -lc 'wget -qO- http://192.168.86.158:11434/api/tags'
```

Run the live HankAI eval harness with:

```bash
HANK_LIVE_BASE_URL="https://hankdemo.campbellservers.com" \
HANK_LIVE_SESSION_TOKEN="$HANK_LIVE_SESSION_TOKEN" \
HANK_HANKAI_EXPECT_PROVIDER="ollama" \
HANK_HANKAI_EXPECT_MODEL="gemma4:12b" \
HANK_HANKAI_EXPECT_OLLAMA_URL="http://192.168.86.158:11434" \
go run ./tools/hankaieval
```

Reports are generated under `data/hankai-evals/` and must remain untracked.

## Private ChatGPT Kanban canary

Use a disposable MCP-visible board and synthetic cards. Enable `HANK_MCP_ENABLED=true`,
set the deployed HTTPS origin in `HANK_PUBLIC_BASE_URL`, and restart cloud. Interactive
Kanban is included whenever MCP is enabled; there is no separate Kanban setting. Do not record
OAuth tokens, card contents, or screenshots containing private notes.

Validate in this order:

1. Preserve passing real HTTP integration-test evidence that proves `subscriptions/listen`
   acknowledges first and delivers a controlled `notifications/tools/list_changed` event. Do not
   add a production debug endpoint to mutate the compiled tool list.
2. MCP Inspector discovery advertises only `2026-07-28`, `hank-mcp` `0.3.0`, `ttlMs: 0`, and
   `tools.listChanged: true`; it lists the Kanban tools/resource and reads
   `ui://hank/kanban/v1` with its strict CSP metadata.
3. Open `subscriptions/listen` and observe `notifications/subscriptions/acknowledged` as the first
   SSE frame.
4. In a typed ChatGPT conversation, ask “Open my Hank Kanban,” switch boards, and verify both wide
   and narrow layouts.
5. On synthetic cards, create, edit one field, append a work log, use the explicit Move control,
   and test wide-layout drag/reorder.
6. Delete a disposable card and verify both the widget confirmation and ChatGPT host confirmation.
7. Cause a harmless stale-revision failure from a second session and confirm the widget retains
   the draft and requires manual reload/retry.
8. Restart cloud and confirm automatic reconnect, rediscovery, and re-listen without connector
   recreation; verify the Kanban tools/resource remain available without a separate setting.
9. Inspect logs and `hank_mcp_subscription*` metrics for expected aggregate lifecycle events and
   confirm they contain no bearer token, subscription ID, card/note content, or tool arguments.

This is private-use evidence only, not a public ChatGPT app-submission gate. Card content opened
in the widget is sent to ChatGPT/OpenAI. ChatGPT Voice is out of scope because it cannot invoke
apps.

## Future Demo Run Order

Run this sequence after the demo stack is up, the agent is online, Home Assistant is reachable, and two file sources are configured with synthetic test data only:

```bash
HANK_BOOTSTRAP_NONINTERACTIVE=true \
HANK_BOOTSTRAP_HOST_BIND=127.0.0.1 \
HANK_BOOTSTRAP_HOST_PORT=18080 \
HANK_BOOTSTRAP_PUBLIC_BASE_URL=https://your-demo-host.example.com \
scripts/bootstrap-first-run.sh
scripts/doctor.sh
```

```bash
make fmt
make tidy
make build
go test -count=1 ./...
```

```bash
promtool check rules ops/prometheus/alerts.yml
scripts/restart-validation.sh
scripts/file-safety-validation.sh
scripts/schema-drift-check.sh
scripts/migration-baseline-validation.sh
go run ./tools/livevalidation
go run ./tools/hankaieval
go run ./tools/adminvalidation
scripts/scale-validation.sh
scripts/production-load-report.sh
scripts/backup-during-traffic.sh
scripts/restore-proof.sh
scripts/query-telemetry-report.sh
scripts/metrics-assert.sh
```

`tools/adminvalidation` follows current canonical dashboard routes. If a route
split removes or renames an operator page, update this tool in the same change
so demo validation does not silently test stale UI paths.

If `promtool` is unavailable on the demo host, install Prometheus tooling on that host rather than editing the alert rules around the missing binary.

## Generated Artifacts

## Remote Desktop V1

Run `scripts/remote-desktop-load-validation.sh --contract-only` and `scripts/remote-desktop-acceptance.sh --contract-only` in every portable gate. Both execute exact scenario-to-test mappings, explicitly mark integration/physical-only rows as not run, and emit local, Git-ignored JSONL receipts under `.codex/remote-desktop-evidence/`; `native_evidence:false` means they do not satisfy physical acceptance. Packaged physical-device acceptance requires separate authorization and a metadata-only driver; run `scripts/remote-desktop-acceptance.sh` without `--contract-only` only on disposable Windows/macOS devices. Native evidence belongs under the driver-configured untracked evidence directory and must never contain display, input, clipboard, credential, private-key, recovery-code, or ciphertext content. See `docs/remote-desktop/v1-acceptance.md` and `docs/remote-desktop/v1-operations.md`.

Validation output is intentionally generated under `data/` and should remain untracked:

- `data/restart-validation/`
- `data/file-safety-validation/`
- `data/schema-drift/`
- `data/migration-baseline/`
- `data/scale-validation/`
- `data/load-reports/`
- `data/backup-traffic/`
- `data/restore-reports/`
- `data/query-telemetry/`

Keep important demo evidence by copying report paths into the release notes or operator notes, not by committing generated reports.

## Synthetic Data Rule

Demo validation must use synthetic files, notes, attachments, and assistant-index rows. Do not run destructive file-operation tests against real user data. The validation tools create paths under `_hank_validation/` and `_hank_load/`; if a run fails midway, clean those prefixes from the configured demo file sources before the next run.

## Linux fleet worktree validation

The `codex/linux-fleet-demo` worktrees implement the full demo described in
[fleet operation](fleet-demo.md), including scoped grants, workspace transfer,
durable jobs, multi-target workflows, MCP and the relay transport evaluation.

Validated locally with synthetic credentials and disposable PostgreSQL:

- Server: `make fmt`, `make tidy`, `make build`, and the full `go test ./...`
  suite with `HANK_TEST_DATABASE_URL` configured (bounded package/test parallelism).
- Linux: full Go tests, full race suite, vet, build, and cross-repository protocol
  checks, including exact fleet v1 and one-shot shell contract equality.
- Focused server/store race checks: pending/member/expired-scope boundaries,
  one-use approval, cookie CSRF, grant revocation and membership removal,
  cross-Home target constraints, raw-relay bypass denial, concurrent job admission,
  idempotency, metadata-only storage, and agent deletion scope cleanup.
- Real-process acceptance: two standalone Linux agent binaries plus the real CLI,
  a TLS server and PostgreSQL; grant approval, discovery, workspace upload, a
  two-device test workflow, MCP discovery, server replacement/reconnection, worker
  restart/output replay, and offline cancellation delivered after reconnect.
- Migration 46: normal up workflow, strict migration status, direct-mode schema
  check, full `pg_dump --schema-only` comparison against a separately migrated
  fresh database, and transactional down-DDL/rollback validation.
- Filesystem/process tests: revision conflicts, traversal/escaping symlinks,
  local shell gating, timeout/cancellation, duplicate-job prevention, output cap,
  cursor replay, and interrupted-job recovery without reexecution.

To repeat acceptance after building the Linux worktree:

```bash
HANK_TEST_DATABASE_URL="$DISPOSABLE_TEST_DATABASE_URL" \
HANK_FLEET_AGENT_BINARY="$LINUX_WORKTREE/dist/hankagent" \
go test -race ./internal/cloud -run TestFleetTwoLinuxAgentsAcceptance -count=1 -v
```

The disposable database used relaxed WAL durability for test speed; these checks
are not physical disk-loss or crash-durability evidence. The fixture creates
private temporary agent state and cleans up its processes. Relay timing reports
contain only synthetic payload sizes and timings; see the fleet guide for the
measurement and the decision to retain server relay.

Not performed: live shared demo/physical-device acceptance (worktrees are not
deployed and live validation credentials are not configured), dashboard test
suites (no dashboard source changes; build/typechecking did run), packaging,
signing, systemd installation, production/release gates, WAN/concurrent transport
benchmarks, and unrelated provider/platform-specific opt-in integrations. The
Compose wrapper for deep schema comparison was not used; equivalent schema dump
comparison ran directly against the disposable PostgreSQL container. No changes
were installed, published or deployed. Before release, repeat against synthetic
workspaces on physically separate Linux devices and exercise operator rollout and
backup/restore procedures.
