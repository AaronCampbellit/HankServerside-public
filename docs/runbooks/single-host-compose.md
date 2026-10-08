# Single-Host Docker Compose Runbook

Use this runbook for the supported live deployment shape:

- one server
- one Docker Compose stack
- one singleton Home
- first registered user becomes admin
- Cloudflare Tunnel or a reverse proxy in front of `cloud`

The complete current setup flow is:

- `docs/deployment.md`

## Files

Private server files in the repo root:

- `.env.cloud`
- `.env.agent`

Protect them after every edit:

```bash
chmod 600 .env.cloud .env.agent
```

The agent service can update `.env.agent` from inside the container so dashboard connection changes persist. If you override `HANK_AGENT_CONTAINER_USER`, make sure that custom user can write `.env.agent` and the agent volumes.

Compose file:

- `docker-compose.yml`

Use `docker compose --env-file .env.cloud ...` for deployment commands so Compose sees the host bind and port values from `.env.cloud`.

## First Boot

```bash
cd /srv/hank/HankServerside
scripts/bootstrap-first-run.sh
docker compose --env-file .env.cloud ps
scripts/doctor.sh
```

Expected services:

- `postgres`
- `db-ops`
- `cloud`

The `agent` is profile-gated and should stay stopped until `.env.agent` exists.

The bootstrap asks for the public Hank HTTPS URL and automatically creates the stable VAPID identity used for browser and installed-PWA notifications. A rerun preserves a complete identity; it does not rotate subscribed browsers.

Manual first boot without the bootstrap script must run migrations before starting `cloud` normally:

```bash
docker compose --env-file .env.cloud build postgres cloud db-ops
docker compose --env-file .env.cloud up -d postgres
docker compose --env-file .env.cloud run --rm cloud /usr/local/bin/hank-server migrate up
docker compose --env-file .env.cloud run --rm cloud /usr/local/bin/hank-server migrate status --strict
docker compose --env-file .env.cloud up -d cloud db-ops
```

## Bootstrap

1. Open the public Hank URL.
2. Register the first account.
3. Let the first account become the admin automatically.
4. Create the agent setup token in the dashboard.
5. Copy the generated `.env.agent` block.
6. Install it from your Mac clipboard:

```bash
pbpaste | ssh <server-user>@<server-host> 'cd /srv/hank/HankServerside && scripts/install-agent-env.sh'
```

If you are already in an SSH session, paste the block into `.env.agent`, run `chmod 600 .env.agent`, then run `docker compose --env-file .env.cloud --profile agent up -d agent`.

Public registration is disabled after the first Home exists. Add more users through dashboard invitations.

## Verify

```bash
curl http://127.0.0.1:18080/healthz
curl http://127.0.0.1:18080/readyz
curl -H "Authorization: Bearer $HANK_ADMIN_SESSION_TOKEN" http://127.0.0.1:18080/metrics | head
docker compose --env-file .env.cloud --profile agent ps
scripts/doctor.sh
```

Use the configured `HANK_CLOUD_HOST_PORT` if it is not `18080`.

## Monitoring And Alerts

The compose file ships a `monitoring` profile with Prometheus (rule evaluation
against `ops/prometheus/alerts.yml`) and Alertmanager. Both bind to
`127.0.0.1` only; access them over SSH port forwarding, never expose them
publicly.

1. Keep a dedicated `HANK_METRICS_SCRAPE_TOKEN` in `.env.cloud`, protect
   that file with mode `600`, and recreate cloud after configuring it. Preserve
   existing tokens. Apply migrations 36 and 37 before starting the updated cloud.
2. Run `scripts/configure-monitoring.sh` to synchronize the running cloud's
   scrape token into the private Prometheus credential file without printing or
   rotating it. Docker access is required. Missing or failed input preserves the
   existing file. The helper makes shipped Prometheus configuration readable
   by container gid 65534 while preserving the checkout owner.
3. Start or recreate monitoring:

```bash
docker compose --env-file .env.cloud --profile monitoring up -d --force-recreate prometheus alertmanager node-exporter
python3 scripts/check-monitoring.py
```

Prometheus requires the credential file to exist before it can start; Compose
will not silently create a directory in its place. Recreate Prometheus after
credential synchronization because the script replaces the file atomically.

Open **Settings → Notifications → Server alert delivery** as a Home
administrator. Enable or disable inbox and email delivery, choose administrators
or all Home members, and enter the SMTP hostname/STARTTLS port, account, sender,
and recipient. A blank password retains the saved value; use the explicit
removal control to clear it. Save, then choose **Send test alert** and verify the
message in each enabled destination. A queued test confirms submission to the
monitoring service, not mailbox receipt. Test requests have a 30-second cooldown.

Settings and credentials persist in PostgreSQL; credentials use the existing
application secret-encryption key. Cloud generates an independent webhook token
and writes an atomic private configuration in `hank_monitoring_config`, then
reloads Alertmanager. No SMTP environment edit or restart is needed for later
changes. If the service is unavailable, the GUI reports pending application;
cloud retries every 30 seconds. That same cycle refreshes the validated public
SMTP address. No settings or credentials are returned to non-administrators.

The configuration volume has owner `hank`, group 65534, directory mode `750`,
and files mode `640`; cloud belongs to that group and Alertmanager mounts it
read-only. Alertmanager retains its configuration while cloud is unavailable,
so email delivery remains independent. Protect this volume as runtime secret
material; it is regenerated from PostgreSQL after recovery. Do not include its
contents in logs or source archives. Standard Compose uses this managed volume;
standalone installations must provide equivalent private storage and set
`HANK_MONITORING_CONFIG_DIR` and `HANK_ALERTMANAGER_URL`.

Firing and resolved alerts use independent inbox/email routes. Inbox repeats are
deduplicated; active email alerts repeat every four hours. Recipient and enable
changes affect future deliveries, preserving existing inbox history. The
personal Server monitoring preference suppresses push only. `make monitoring-test`
checks generated configuration and synthetic SMTP/webhook delivery, including
an inbox outage; verify your real provider with the GUI test before relying on it.

The monitoring profile collects Hank, Prometheus, Alertmanager, and Linux host
filesystem metrics. Node Exporter runs with only its filesystem collector,
without published ports, with a read-only host filesystem mount and dropped
capabilities. The host mount is required to measure host filesystems rather
than the container's writable layer. This profile targets a Linux Docker host.

The read-only checker requires Python 3 (standard library only). It rejects
failed or missing scrapes, missing root filesystem or primary-agent metrics,
missing/unhealthy required rules, and zero active Alertmanager integrations.
`scripts/doctor.sh` runs it when Prometheus is running. A healthy container alone
does not establish working monitoring.

Rules distinguish the primary agent from online workers, alert on jobs that
remain `rollback_required` for 15 minutes, and detect absent monitoring data.
File-job row counts are gauges; they are not cumulative failure counters.

After the checker passes, send a test alert and verify receipt at the intended
destination. Configuration checks cannot prove actual delivery. If Prometheus
or Alertmanager itself is down, local rules cannot guarantee delivery; use an
independent uptime/dead-man check if that failure must page an operator.

For capacity pressure, inspect `df -h` and `docker system df`. Image and build
cache sizes share layers and must not be summed. Review retained releases,
rollback requirements, and other workloads before any cleanup; do not use a
blanket prune on a shared production host.

Dashboard checks:

- Primary Hank Agent shows online
- sync status loads
- storage status loads for admins
- first manual backup succeeds
- restore test succeeds after the first backup exists

## Common Operations

View logs:

```bash
docker compose --env-file .env.cloud logs -f cloud postgres db-ops
docker compose --env-file .env.cloud --profile agent logs -f agent
```

Restart everything after the agent is active:

```bash
docker compose --env-file .env.cloud --profile agent restart
```

Rebuild after pulling changes:

```bash
cd /srv/hank/HankServerside
git pull
docker compose --env-file .env.cloud --profile agent up --build -d
scripts/doctor.sh
```

## Storage Notes

Postgres is private to Docker networks. It is not published to the host.

Back up:

- `hank_pg18_pgbackrest_repo`
- `hank_note_attachments`
- `hank_db_ops_state`
- `hank_agent_files`
- `hank_agent_notes`
- `.env.cloud`
- `.env.agent`

Keep `HANK_DB_OPS_REPO_CIPHER_PASS`; encrypted pgBackRest backups cannot be restored without it.
Keep `hank_note_attachments` with the pgBackRest repository because note attachment files live outside Postgres.

PostgreSQL 18 uses `PGDATA=/var/lib/postgresql/data`, but the primary volume
mounts at `/var/lib/postgresql`. Keep that parent mount: mounting only the
`data` child causes the upstream PostgreSQL 18 image to create an additional
anonymous parent volume. The restore service uses the separate
`hank_pg18_postgres_restore_data` volume and receives the repository cipher
passphrase through `PGBACKREST_REPO1_CIPHER_PASS`.

If an existing database has checksums disabled, schedule downtime and run:

```bash
cd /srv/hank/HankServerside
scripts/enable-pg-checksums.sh
```

### Retention and Disk Pressure Review

Check host free space, Docker's image/build-cache inventory, and the active
pgBackRest labels separately. Docker's reported reclaimable totals overlap and
must not be added together. Build cache is host-wide and may belong to other
projects; a broad image, volume, or system prune is not a Hank maintenance step.

Compare each attachment archive's backup label with the current pgBackRest
inventory before proposing cleanup. Preserve every archive matching a retained
database backup. An orphan candidate is not automatically safe to delete:
confirm external recovery requirements and obtain explicit approval for the
exact list. Small or missing archives need restore validation, not deletion
based on size. A retention mismatch is separate from host build-cache pressure.

For rollback-required move jobs, verify the original machine and current source
and destination state before any retry or deletion. Never use a new primary as
evidence of historical ownership. Production cleanup, ownership assignment,
and destructive recovery must remain separately confirmed operator actions.
