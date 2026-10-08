# Hard Naming Migration

This release makes a clean break from the retired Hank Remote server-side
identifiers. Fresh installations use only the canonical names. Existing
installations must run `scripts/migrate-hank-naming.sh --apply` once before the
new server is started; normal startup does not accept aliases.

## Naming Contract

| Retired identifier | Canonical identifier | Owner |
| --- | --- | --- |
| `/srv/hank-remote` | `/srv/hank` | host installation root |
| `hank-remote-cloud` | `hank-server` | server executable |
| `hank-remote-agent` | `hank-agent` | bundled Compose agent executable |
| `HANK_REMOTE_*` | `HANK_*` | cloud and agent environment keys |
| PostgreSQL database `hankremote` | `hank` | live and restore database defaults |
| PostgreSQL role `hankremote` | `hank` | database owner/login default |
| Go module `github.com/dropfile/hankremote` | `github.com/dropfile/HankServerside` | source imports |
| metric prefix `hank_remote_` | `hank_` | Prometheus producers, rules, and queries |
| session/CSRF cookie prefix `hank_remote_` | `hank_` | browser authentication cookies |
| notification category `connector` | `agent_health` | API and persisted notification settings |
| events `connector.offline` / `connector.recovered` | `agent.offline` / `agent.recovered` | notification and audit events |
| recovery product `hank-remote`, schema 1 | product `hank`, schema 2 | recovery export/import |
| user-facing `Hank Remote Desktop` | `Remote Desktop` | dashboard and operator copy |
| user-facing `Home & Connector` | `Home & Agent Setup` | dashboard navigation |
| user-facing `Cloud service` | `Hank server` | dashboard and operator copy |

Hank Agent remains the product term. Signed HankAgent artifacts,
`hankagent.service`, and their update paths are unchanged. Compose services
remain `cloud`, `agent`, and `db-ops`; private environment filenames remain
`.env.cloud` and `.env.agent`; `cloud.command`, `cloud.response`,
`X-Hank-Agent-ID`, `hank-mcp`, and MCP connector terminology are unchanged.

## Before Applying

1. Update the checkout at `/srv/hank-remote/HankServerside` to the release that
   contains this script, but do not start the new containers.
2. Stop external automation that could restart the Compose application during
   the maintenance window.
3. Run the read-only detector:

   ```bash
   cd /srv/hank-remote/HankServerside
   scripts/migrate-hank-naming.sh --check
   ```

4. Confirm that `/srv/hank` does not exist. If both installation roots exist,
   the script stops without merging or deleting either one.
5. Confirm that the PostgreSQL cluster contains the old database and role and
   not the new database or role. Partial or colliding database state is a hard
   failure.
6. Confirm that the running PostgreSQL container was created from Compose files
   inside this checkout. The script refuses split-checkout or missing temporary
   Compose configuration because reconciling that state could attach the wrong
   database volume.
7. Run the storage topology preflight. It resolves every effective Compose file,
   including `COMPOSE_FILE` overrides, and fails if the live database, restore
   database, pgBackRest repository, or attachment volumes disagree between
   their readers and writers, or if a running container is attached to a
   different named volume:

   ```bash
   scripts/production-naming-preflight.sh
   ```

A new database backup is recommended before any maintenance operation but is
not a script prerequisite. The script makes mode-`0600`, timestamped backups of
both environment files. It does not print environment values or secrets.

## Apply

Run from the existing checkout:

```bash
cd /srv/hank-remote/HankServerside
scripts/migrate-hank-naming.sh --apply
```

The script rewrites environment keys and known path, binary, database, and role
values; stops application services; renames the PostgreSQL database and role;
and finally moves the complete installation root to `/srv/hank`. It never
creates a compatibility symlink. A failure after changes begin triggers a
best-effort full rollback of the database name, role, environment files, and
installation path. If rollback itself reports an error, keep the application
stopped and inspect both naming states before retrying.

An installation rooted at another parent but still named
`hank-remote/HankServerside` is detected automatically. For example,
`/Dockerapps/hank-remote/HankServerside` moves as a unit to
`/Dockerapps/hank/HankServerside`; sibling rollback or upgrade evidence under
that installation root moves with it. The canonical sibling root must not
already exist.

For an installation whose repository is not under the standard
`/srv/hank-remote` root, explicitly request a repository-only move. This moves
the checkout and its private environment files without moving its parent
directory:

```bash
cd /path/to/HankServerside
HANK_NAMING_MOVE_REPOSITORY=true scripts/migrate-hank-naming.sh --apply
```

The same `/srv/hank` collision rule applies. The script creates
`/srv/hank/HankServerside` and leaves siblings of the old checkout untouched.

After success, continue only from the canonical path:

```bash
cd /srv/hank/HankServerside
scripts/bootstrap-first-run.sh
scripts/doctor.sh
```

Bootstrap applies embedded migration 35, which rewrites existing notification
settings and event rows to `agent_health` and `agent.*`. No notification schema
mutation is hidden in ordinary handler or startup code.

## Expected User And Operator Impact

- Existing browser sessions are invalid because the cookie names changed. Users
  sign in again; passwords, roles, and account records are unchanged.
- Prometheus series begin under `hank_*`. The bundled scrape configuration and
  alert rules change with the release; external dashboards and long-range
  queries must be updated separately.
- Recovery imports accept product `hank`, schema 2. Schema-1 exports are not
  accepted by the new runtime, so retain the prior release if an old export must
  be restored.
- Environment aliases are intentionally absent. Any private automation or
  service definition that names old variables, binaries, or paths must be
  updated before it starts the new release.
- Database data is preserved in place by PostgreSQL rename operations. The
  embedded schema migration changes notification naming only; it does not drop
  user data.

## Validation And Rollback Boundary

Before serving users, require all of the following:

```bash
docker compose --env-file .env.cloud config --quiet
docker compose --env-file .env.cloud run --rm cloud /usr/local/bin/hank-server migrate status --strict
scripts/schema-drift-check.sh
scripts/doctor.sh
```

Also verify `/healthz`, `/readyz`, Hank Agent reconnect, administrator login,
notification settings, one notification delivery, Remote Desktop readiness,
backup status, and Prometheus target/rule health. If validation fails, stop the
new application. Code rollback across this boundary requires restoring the old
release and reversing the naming migration; database rollback across later
application migrations must follow the repository migration and backup policy.

Build the release from a clean immutable commit with `HANK_BUILD_VERSION` and
`HANK_SOURCE_COMMIT` set. A production `/readyz` response that reports `dev` is
not acceptable release provenance.

## Migration History Compatibility

The production agent-health naming migration remains version 35 with its
original checksum. The newer main assistant resume index is version 39; audit
monitoring and file ownership migrations occupy versions 36–38. A database that
previously applied the main index as version 35 has a different history and fails
normal checksum validation. Existing production naming migrations are not rerun.

### Reconcile The Known Assistant-Index Conflict

Do not hand-edit the ledger, replace a checksum, or skip migration 35. Use the
explicit maintenance command below only for the known assistant-index history.
It does not rename databases, change environment files, restart services, or run
pending application migrations.

1. Preserve a verified database backup and its matching attachment archive, plus
   the old runtime image. Record the pgBackRest label. The command requires that
   label as operator confirmation; it does not independently verify backup bytes.
2. Build a separate maintenance image from the revised checkout, without running
   bootstrap or replacing/restarting any production container:

   ```bash
   docker build -f Dockerfile.server -t hank-migration-reconcile:local .
   ```

3. Identify the running Compose `postgres` container. Run this check with its
   exact name and the existing private environment file:

   ```bash
   scripts/reconcile-assistant-index-migration.sh --check \
     --image hank-migration-reconcile:local \
     --postgres-container <running-postgres-container> --env-file .env.cloud
   ```

   The wrapper accepts the old database URL variable only for this one-time
   operation. It joins the database container's network namespace, has no volume
   mounts, and never prints the environment values. Confirm that the configured
   database URL targets this deployment before running it.

4. Require `status: eligible`. The command checks every recorded migration
   through 34, the exact original migration-35 name/checksum, the valid index
   definition, unchanged notification column naming, and the absence of later
   or conflicting ledger entries. Unsupported histories remain blocked.
5. During an approved maintenance window, stop cloud, agent and db-ops plus any
   automation that could restart them; leave PostgreSQL running. This matters
   because the old binary will reject the reconciled ledger if restarted.
6. Apply with the verified retained backup label:

   ```bash
   scripts/reconcile-assistant-index-migration.sh --apply \
     --image hank-migration-reconcile:local \
     --postgres-container <running-postgres-container> --env-file .env.cloud \
     --backup-label <verified-backup-label>
   ```

The command rechecks under short database locks and updates exactly one ledger
row in a transaction: assistant-index version 35 becomes 39 with the version-39
checksum. Its original name, application timestamp and duration are preserved;
the existing index and application data are untouched. An interruption before
commit rolls back the entire change. Repeating the command reports
`already_reconciled` for the recognized destination history.

Migration 35 remains pending. Continue with the naming migration in this guide,
then apply the normal migrations through 40. Require strict migration status,
deep schema drift checks and the release acceptance checks before serving users.
If the operation refuses the history, keep the deployment unchanged and review
the reported mismatch. Do not add another checksum exception.

Before applying subsequent migrations, rollback is to the retained backup and
old runtime, not a blind ledger edit. After the naming or later migrations,
follow the full database/attachment and naming rollback procedure. Never start
the old binary against a reconciled or upgraded ledger.
