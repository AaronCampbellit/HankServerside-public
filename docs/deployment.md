# Deployment And Setup

Use this as the current setup and deployment guide for HankServerside and Hank
Agents.

The supported deployment is one Docker Compose stack on one server:

- `postgres`: the live Hank database
- `db-ops`: backup, checksum, and restore worker
- `cloud`: the dashboard, app API, and WebSocket relay
- `agent`: the primary Hank Agent, started after the first admin creates a setup token
- `postgres-restore`: restore-test database, started only by the restore flow

PostgreSQL 18 is the minimum and default supported database major version for
the primary, restore-test, and database-operations services. `scripts/doctor.sh`
rejects any other running major version. Moving to a later major version
requires an explicit upgrade that validates the image, extensions, pgBackRest,
logical restore, application behavior, and restore-test path together.

Each server-store pool allows at most 20 open connections, retains at most five
idle connections, retires idle connections after five minutes, and recycles
connections after 30 minutes. Excess queries wait with their request context;
this leaves capacity for the dedicated realtime listener, database operations,
and administrator access. Authenticated metrics expose the pool limit, usage,
wait count, and cumulative wait duration so sustained saturation is visible.

Existing installations made before the canonical Hank naming change must run
the [hard naming migration](naming-migration.md) before starting this version.
The migration renames the installation path, environment keys, database and
role, and server-side executables without leaving runtime aliases.

## PWA Delivery Requirements

The dashboard is an installable PWA with local-first offline Notes. Serve the public Hank origin over HTTPS so browsers can register the service worker; localhost is the browser's development-only exception. The reverse proxy or tunnel must pass these root routes to the cloud unchanged:

- `/manifest.webmanifest`
- `/sw.js`
- `/offline.html`

The cloud returns these control files with `Cache-Control: no-cache, max-age=0, must-revalidate` so clients check for updates. Fingerprinted Vite JavaScript and CSS under `/assets/` are served as immutable for one year. Do not override those policies with a proxy-wide cache rule, and do not rewrite `/sw.js` to the login or dashboard document. Authenticated `/v1` API calls and `/ws` connections are not service-worker cached.

Offline Notes data is stored by the browser in user-partitioned IndexedDB, not CacheStorage. Eligibility expires 30 days after the last successful authentication, and explicit logout purges the active user's workspace. Cached attachment Blobs have a 100 MiB per-user soft cap with least-recently-used eviction. Operators should treat browser profiles on shared machines as sensitive local data and clear site data when retiring a device or browser profile. No new server environment variable or PostgreSQL migration is required for this feature.

## 1. Server Folder

Use this folder on the server:

```bash
/srv/hank/HankServerside
```

For a fresh server:

```bash
sudo mkdir -p /srv/hank
sudo chown "$USER":"$USER" /srv/hank
cd /srv/hank
git clone https://github.com/AaronCampbellit/HankServerside-public.git HankServerside
cd /srv/hank/HankServerside
```

Do not create `data/postgres`, `data/files`, or `data/notes` by hand. Docker creates the named volumes.

## 2. Env Files

There are two private env files in the repo root:

- `/srv/hank/HankServerside/.env.cloud`
- `/srv/hank/HankServerside/.env.agent`

They are ignored by git. Keep real passwords, tokens, and backup encryption passphrases only in those private files or in your server secret manager.

Docker Compose uses those repo-root env files directly. The only file left under `configs/` is `configs/pgbackrest.conf`, which is a real pgBackRest config asset, not an env template.

## 3. Recommended Bootstrap

On a fresh server, use the bootstrap script. It creates `.env.cloud`, generates a stable Web Push VAPID identity, builds the first-boot images, starts Postgres, runs migrations, starts `cloud` and `db-ops`, and checks `/healthz` plus `/readyz`. The interactive flow asks for the public Hank HTTPS URL and uses it as the VAPID operator contact.

```bash
cd /srv/hank/HankServerside
scripts/bootstrap-first-run.sh
```

For an unattended install, set values before running the script:

```bash
cd /srv/hank/HankServerside
HANK_BOOTSTRAP_NONINTERACTIVE=true \
HANK_BOOTSTRAP_HOST_BIND=127.0.0.1 \
HANK_BOOTSTRAP_HOST_PORT=18080 \
HANK_BOOTSTRAP_PUBLIC_BASE_URL=https://your-host \
scripts/bootstrap-first-run.sh
```

The bootstrap default bind is `127.0.0.1`, which is the safe setting for Cloudflare Tunnel or a local reverse proxy on the same server. Use `HANK_BOOTSTRAP_HOST_BIND=0.0.0.0` only when the cloud HTTP port must be reachable directly on the server network. Noninteractive setup requires `HANK_BOOTSTRAP_PUBLIC_BASE_URL` to be a public HTTPS URL.

Bootstrap generates VAPID keys only when all three Web Push fields are empty or absent. It atomically replaces blank placeholders, preserves a complete existing identity, and rejects partial configuration before changing services. This makes reruns safe and prevents browser subscriptions from being invalidated by an accidental key rotation.

Run the doctor after bootstrap and after later updates:

```bash
cd /srv/hank/HankServerside
scripts/doctor.sh
```

## 4. Manual `.env.cloud` Reference

Use this only when you are not using `scripts/bootstrap-first-run.sh`. Create this file before first boot:

```bash
cd /srv/hank/HankServerside
nano .env.cloud
chmod 600 .env.cloud
```

Use this shape:

```env
HANK_CLOUD_ADDR=:8080
HANK_CLOUD_HOST_BIND=127.0.0.1
HANK_CLOUD_HOST_PORT=18080

POSTGRES_DB=hank
POSTGRES_USER=hank
POSTGRES_PASSWORD=replace-with-real-db-password
HANK_CLOUD_DATABASE_URL=postgres://hank:replace-with-real-db-password@postgres:5432/hank?sslmode=disable

HANK_SESSION_TTL_SECONDS=604800
HANK_REQUEST_TIMEOUT_SECONDS=120
HANK_MAINTENANCE_INTERVAL_SECONDS=3600
HANK_MAINTENANCE_RETENTION_DAYS=30
HANK_SECRET_ENCRYPTION_KEY=replace-with-stable-random-secret-encryption-key

# Optional first-boot fallback for Microsoft Entra browser SSO. Configure a
# single-tenant Web app with https://your-host/v1/auth/entra/callback and
# require Entra assignment. After a Home exists, admins can configure or rotate
# this in Settings > Home & Agent Setup; the GUI stores the secret encrypted.
HANK_ENTRA_ENABLED=false
HANK_ENTRA_TENANT_ID=
HANK_ENTRA_CLIENT_ID=
HANK_ENTRA_CLIENT_SECRET=
HANK_PUBLIC_BASE_URL=https://your-host

HANK_AI_PROVIDER=auto
HANK_ASSISTANT_EXECUTION_ENABLED=false
HANK_OLLAMA_BASE_URL=
HANK_OLLAMA_CHAT_MODEL=llama3.1
HANK_OLLAMA_EMBEDDING_MODEL=nomic-embed-text
# Embedding dimension is fixed at 768 by the production schema.
HANK_PROJECT_DOCS_DIR=/app

# Optional authenticated MCP endpoint. Interactive Kanban is included when enabled.
HANK_MCP_ENABLED=false
HANK_MCP_DOCS_DIR=/app

# Privileged Linux RMM release feed. The host directory contains the complete,
# signed HankAgent Linux dist set; the cloud receives only the public key.
HANK_LINUX_AGENT_RELEASE_HOST_DIR=./data/linux-agent-release
HANK_LINUX_AGENT_RELEASE_DIR=/var/lib/hank/linux-agent-release
HANK_LINUX_AGENT_RELEASE_PUBLIC_KEY=replace-with-base64-ed25519-public-key

# The Kanban flag is additive and still requires MCP_ENABLED. Until an admin
# saves Settings > AI & MCP, this environment value is the effective default.
# A saved dashboard value is persisted and overrides the environment on later
# restarts; dashboard changes apply immediately.

# Optional OpenAI fallback/provider.
HANK_OPENAI_API_KEY=
HANK_OPENAI_CHAT_MODEL=gpt-4o-mini
HANK_OPENAI_EMBEDDING_MODEL=text-embedding-3-small

# Experimental ChatGPT/Codex link for subscription-backed HankAI chat.
# Keep disabled unless HANK_AI_PROVIDER is chatgpt_codex or you want auto mode to use linked ChatGPT.
HANK_CHATGPT_OAUTH_ENABLED=false
HANK_CHATGPT_AUTH_ISSUER=https://auth.openai.com
HANK_CHATGPT_BACKEND_BASE_URL=https://chatgpt.com/backend-api/codex
HANK_CHATGPT_CLIENT_ID=app_EMoamEEZ73f0CkXaXp7hrann
HANK_CHATGPT_CHAT_MODEL=gpt-5.4-mini

# Optional APNs push notifications for native Apple Hank clients.
# Leave blank locally; device registration routes still work with a no-op sender.
HANK_APNS_TEAM_ID=
HANK_APNS_KEY_ID=
HANK_APNS_PRIVATE_KEY=
HANK_APNS_TOPIC=com.dropfile.Hank
HANK_APNS_ENVIRONMENT=sandbox

# Standards-based Web Push for browsers and installed PWAs. The recommended
# bootstrap fills these automatically; manual installs can use the command below.
HANK_WEB_PUSH_VAPID_PUBLIC_KEY=
HANK_WEB_PUSH_VAPID_PRIVATE_KEY=
HANK_WEB_PUSH_VAPID_SUBJECT=https://your-host

HANK_DB_OPS_STATE_DIR=/var/lib/hank/db-ops/state
HANK_DB_OPS_LOG_DIR=/var/log/hank/db-ops
HANK_DB_OPS_INTENT_SECRET=replace-with-real-db-ops-secret
HANK_DB_OPS_REPO_CIPHER_PASS=replace-with-real-backup-encryption-passphrase
HANK_DB_OPS_STANZA=hank
HANK_DB_OPS_PGDATA=/var/lib/postgresql/data
HANK_DB_OPS_RESTORE_PGDATA=/var/lib/postgresql/restore
HANK_DB_OPS_RESTORE_DATABASE_URL=postgres://hank:replace-with-real-db-password@postgres-restore:5432/hank?sslmode=disable
HANK_DB_OPS_COMPOSE_FILE=/workspace/docker-compose.yml
# Host Docker socket group id. On Linux: stat -c '%g' /var/run/docker.sock
HANK_DB_OPS_DOCKER_GID=
```

Leave `HANK_OLLAMA_BASE_URL` blank until Ollama is installed and reachable from
the cloud container. For Ollama running on the same Docker host, use
`http://host.docker.internal:11434`; for a separate host, use its reachable
LAN URL. This Compose stack has no service named `ollama`. Verify the selected
URL from the cloud container before enabling Ollama in AI Settings.

`HANK_DB_OPS_COMPOSE_FILE` accepts an OS path-list of Compose files. When an
installation uses a volume or deployment override, list the base file first
and the override second (for example,
`/workspace/docker-compose.yml:/workspace/docker-compose.site.yml`) so restore
orchestration attaches the same isolated restore volume as operator commands.

Use the same database password in `POSTGRES_PASSWORD`, `HANK_CLOUD_DATABASE_URL`, and `HANK_DB_OPS_RESTORE_DATABASE_URL`.
Do not wrap either database URL in `< >`; keep the query string exactly as `?sslmode=disable` for the Compose stack.

Keep `HANK_DB_OPS_REPO_CIPHER_PASS`. Both encrypted pgBackRest backups and encrypted attachment archives require this existing key. Losing it prevents recovery; changing it requires a separate coordinated key migration.

Set `HANK_DB_OPS_DOCKER_GID` to the numeric group owner of `/var/run/docker.sock` before starting `db-ops`. The db-ops container runs as a non-root user and needs that supplementary group for restore-test orchestration.

`HANK_MAINTENANCE_INTERVAL_SECONDS` controls how often cloud lifecycle cleanup runs. `HANK_MAINTENANCE_RETENTION_DAYS` controls the shared retention cutoff for expired operational rows, old transfer history, login backoff rows, selected assistant attachment metadata, deleted note attachment rows, safe stale note attachment files, completed notification-source events, and terminal Web Push delivery rows. Durable user notifications have a fixed 30-day policy measured from their latest coalesced occurrence.

Keep `HANK_SECRET_ENCRYPTION_KEY` stable after first use. HankServerside
requires it for normal cloud startup and uses it to encrypt stored OAuth tokens,
APNs device tokens, Web Push subscription endpoints and browser key material,
profile secret vault data, and monitoring delivery credentials at rest. If this key is lost, already encrypted
application secrets cannot be read and browser devices must be re-enrolled. The
only supported no-key mode is an explicit local-development opt-out with
`HANK_ALLOW_PLAINTEXT_SECRETS=true`; do not use that setting for shared
or production-like installs.

### Browser and PWA notifications

The recommended bootstrap generates and stores one VAPID key pair automatically, using `HANK_PUBLIC_BASE_URL` as its HTTPS contact subject. It preserves existing credentials on later runs. For a manual installation, generate the pair on an administrative host and copy the two output lines into `.env.cloud`:

```bash
docker compose --env-file .env.cloud run -T --rm --no-deps --entrypoint /usr/local/bin/hank-server cloud web-push-keys
```

Set `HANK_WEB_PUSH_VAPID_SUBJECT` to a monitored `mailto:` address or an HTTPS URL controlled by the operator. The public key, private key, and subject are all-or-none; omitting all three disables browser delivery without disabling the durable inbox. Never publish the private key. Run `scripts/doctor.sh` to catch incomplete configuration.

Web Push requires a production HTTPS origin and browser permission granted from Hank's explicit Enable action. On iPhone and iPad, the user must first add Hank to the Home Screen and open that installed web app; ordinary Safari tabs do not receive this delivery. Category choices are account-wide and cover Hank Agent health, quick links, storage, shared notes, Home Assistant, and server monitoring. Turning a category off suppresses only push delivery—its durable inbox entries remain available.

The cloud makes delivery calls with a dedicated no-proxy transport, blocks redirects and non-public resolved addresses, and logs only delivery IDs/status outcomes rather than endpoints or key material. Expired or rejected subscriptions are invalidated automatically. Logout and session revocation remove session-owned subscription records; the dashboard also asks the browser to unsubscribe on explicit logout. A restored database includes encrypted subscription and pending-delivery state, so the same `HANK_SECRET_ENCRYPTION_KEY` must accompany backup recovery.

Rotating the VAPID pair invalidates existing browser enrollment. After rotation, users must open Notification Settings and enable delivery again; browsers with a subscription bound to the previous application-server key are unsubscribed and re-enrolled after permission is confirmed. If delivery stops, verify HTTPS, all three VAPID values, session validity, browser permission, installed-PWA state on iOS, and the cloud's redacted Web Push outcome logs.

When Entra SSO is enabled, Hank validates browser identity tokens by immutable tenant and object IDs. The client secret remains only in `.env.cloud`; Hank stores no Entra access or refresh tokens. Existing Hank passwords and agent setup tokens remain valid. Entra does not authenticate `/ws/agent`: agents continue to use `Authorization: Bearer <agent-token>` and `X-Hank-Agent-ID` unchanged.

Settings > Home & Agent Setup lets a Home administrator enable or disable Entra, set the tenant ID, application client ID, public Hank URL, and rotate the client secret. The secret is write-only in the dashboard and encrypted using `HANK_SECRET_ENCRYPTION_KEY`. A saved GUI configuration overrides the environment fallback on later cloud restarts.

After adding the key to an older deployment that may have previously stored plaintext secrets, run:

```bash
docker compose --env-file .env.cloud run -T --rm --entrypoint /usr/local/bin/hank-server cloud secrets status --strict
docker compose --env-file .env.cloud run -T --rm --entrypoint /usr/local/bin/hank-server cloud secrets reencrypt
```

`secrets status` prints only counts for plaintext OpenAI OAuth tokens, APNs device tokens, Web Push subscription envelopes, and profile secret vault rows. `secrets reencrypt` rewrites those known plaintext rows using the configured `HANK_SECRET_ENCRYPTION_KEY`.

`HANK_AI_PROVIDER=openai` still means the supported OpenAI API-key path using `HANK_OPENAI_API_KEY`. `HANK_AI_PROVIDER=chatgpt_codex` uses the experimental ChatGPT/Codex device-code link for chat only. Browser-redirect OpenAI OAuth is not supported. Embeddings continue to use Ollama, the OpenAI API key, or HankServerside's local fallback; ChatGPT subscription OAuth is not used as an embeddings credential.
Production requires pgvector through the bundled Postgres image. The production vector schema is fixed at 768 dimensions, and HankServerside refuses startup when the pgvector schema is unavailable. Do not set `HANK_AI_EMBEDDING_DIMENSION` in production unless a future migration explicitly changes the vector dimension.

After signing in to the dashboard, open `AI Settings` to manage the HankAI harness. Those settings are stored in the database and apply immediately to the next HankAI message:
- which Hank sources can be sent to the active provider
- whether HankAI can use your private past conversations as memory
- which configured AI provider HankAI should use for this Home/user, including Local Ollama, linked ChatGPT/Codex, OpenAI API key, or the configured default
- which Ollama base URL to use for local chat and embedding model discovery
- which chat and embedding model overrides HankAI should use for testing local and external providers
- which prompt profile is active: the stricter ChatGPT/Codex profile, the local-model planner profile, or a custom prompt
- the chat model override for the active chat provider, including ChatGPT/Codex subscription-backed chat
- the system prompt HankAI uses

The dashboard model controls do not replace server secrets. Keep OpenAI API keys and ChatGPT/Codex device-code settings in `.env.cloud`; use the GUI to switch between providers, set the Ollama base URL, choose models, choose embedding models, set the server-owned maximum context window used for provider requests, and swap prompt profiles.

`Project docs` is one of those sources. By default the Docker image makes
`README.md`, `AGENTS.md`, current files under `docs/`, `schemas/`, and the
sanitized `code-reference/` snapshot available from `/app`. Temporary planning
directories are excluded. For local runs,
`HANK_PROJECT_DOCS_DIR=.` points HankAI at the checkout root.
The Hank dashboard and AI Settings page show the current index counts, embedding counts, and pgvector retrieval status.

If the server should only be reached by a local Cloudflare Tunnel or local reverse proxy, use:

```env
HANK_CLOUD_HOST_BIND=127.0.0.1
```

If port `18080` is already in use, change:

```env
HANK_CLOUD_HOST_PORT=18081
```

Important: run Compose with `--env-file .env.cloud`. The stack requires `.env.cloud`, and Compose needs that flag for host bind and port interpolation.

## 5. Manual First Boot

If you created `.env.cloud` manually, build and start only the first-boot services:

```bash
cd /srv/hank/HankServerside
docker compose --env-file .env.cloud build postgres cloud db-ops
docker compose --env-file .env.cloud up -d postgres
docker compose --env-file .env.cloud run --rm cloud /usr/local/bin/hank-server migrate up
docker compose --env-file .env.cloud run --rm cloud /usr/local/bin/hank-server migrate status --strict
docker compose --env-file .env.cloud up -d cloud db-ops
docker compose --env-file .env.cloud ps
```

Expected running services:

- `postgres`
- `db-ops`
- `cloud`

The `agent` should not be running yet.

Check the server:

```bash
curl http://127.0.0.1:18080/healthz
curl http://127.0.0.1:18080/readyz
```

Use your custom port if you changed `HANK_CLOUD_HOST_PORT`.

`/metrics` requires authentication: either a signed-in admin session, or the
dedicated scrape token when `HANK_METRICS_SCRAPE_TOKEN` is set in
`.env.cloud`. The scrape token exists so Prometheus can scrape without an
expiring admin session; see the monitoring section of
`docs/runbooks/single-host-compose.md` for the full Prometheus + Alertmanager
setup.

```bash
curl -H "Authorization: Bearer $HANK_ADMIN_SESSION_TOKEN" \
  http://127.0.0.1:18080/metrics | head
```

Home administrators configure alert email and inbox destinations in
**Settings → Notifications → Server alert delivery**. Settings include channel
enablement, inbox audience, SMTP host and STARTTLS port, username, write-only
password, sender and one recipient address. Password omission preserves the
existing value; removal is explicit. Changes persist in PostgreSQL and apply
without a restart. The test action queues an alert through Alertmanager; verify
actual receipt at both enabled destinations.

The home-page setup checklist and Notifications screen check live metric collection,
fresh rule evaluation, required disk/agent metrics, and a configured alert
destination. Only Home administrators can inspect these checks. Passing service
checks does not confirm recipient delivery: send a test and check its arrival.
`HANK_PROMETHEUS_URL` defaults to `http://prometheus:9090`; this is an
operator-only internal address, not a browser-selectable destination. Initial
service installation and metrics-token provisioning use the deployment runbook;
ongoing inbox/email configuration and test alerts are available in the GUI.

Compose configures `HANK_MONITORING_CONFIG_DIR=/var/lib/hank/monitoring`
and `HANK_ALERTMANAGER_URL=http://alertmanager:9093`, with a private shared
volume mounted read-only by Alertmanager. The cloud periodically writes the
generated configuration and calls the fixed internal reload endpoint. Failed
application remains pending and retries every 30 seconds; the last applied
email configuration keeps working independently during a cloud outage.

SMTP requires verified STARTTLS (usually port 587); implicit TLS on port 465
and private/local relay addresses are not supported by this GUI integration.
Cloud validates and pins a public SMTP IP while retaining the configured
hostname for TLS verification, preventing DNS rebinding into internal services.
User input cannot choose the Alertmanager API URL or configuration path.

Managed inbox delivery uses an automatically generated, encrypted, Home-scoped
credential for bearer-only `POST /v1/integrations/alertmanager`. Cookie and query
credentials cannot authorize it. Outside managed mode, the legacy explicit
`HANK_ALERTMANAGER_WEBHOOK_TOKEN` remains available for standalone
integrations; it must differ from the scrape token. The example
`ops/alertmanager/alertmanager-email-inbox.example.yml` and helper `--inbox` mode
support that standalone arrangement, not the standard managed Compose volume.
`python3 scripts/check-monitoring.py` verifies collection, rules and configured
integrations; it does not establish mailbox delivery.

HTTP metric labels contain the registered route pattern, a bounded HTTP method,
and status code. Unknown paths use `unmatched`, and extension methods use
`OTHER`; resource IDs, installer credentials, filenames, and query strings are
not recorded in these labels. `hank_primary_agent_online` measures the
primary routing connection independently of worker connectivity.

Do not expose unauthenticated `/metrics` to the public internet. `/healthz` and `/readyz` stay unauthenticated for deployment checks.

## 6. Public URL

Point Cloudflare Tunnel or your reverse proxy at the Hank server:

```text
http://127.0.0.1:18080
```

or, if your proxy runs from another machine:

```text
http://<server-ip>:18080
```

The proxy must allow WebSocket upgrades for:

- `/ws/app`
- `/ws/agent`

The agent authenticates to `/ws/agent` with `Authorization: Bearer <agent-token>` and `X-Hank-Agent-ID`. Query-string agent credentials are not supported.

Postgres is not published to the host. It stays on private Docker networks.

## 7. First Admin

Open the public Hank URL.

On a fresh database:

1. Register the first account.
2. That account becomes the deployment admin.
3. The singleton Home is created automatically.
4. The dashboard opens for that admin.

Registration is disabled after this first setup. Additional users should be added from the dashboard invitation flow.

## 8. Create `.env.agent`

In the dashboard, create an agent setup token from the Primary Hank Agent section. The token is shown once as a full `.env.agent` block.

Copy that block, then install it on the server. From your Mac, with the copied block in your clipboard:

```bash
pbpaste | ssh <server-user>@<server-host> 'cd /srv/hank/HankServerside && scripts/install-agent-env.sh'
```

The helper writes `.env.agent` with mode `0600` and starts the Compose `agent` profile.

Or paste it manually inside an SSH session:

```bash
cd /srv/hank/HankServerside
nano .env.agent
chmod 600 .env.agent
```

`.env.agent` contains the Hank Agent token and may later contain Settings-managed Home Assistant, SMB, and media-service credentials. Treat it as a secret file and keep mode `0600`.

The Compose agent container is allowed to update the bind-mounted `.env.agent` file so Settings > Connections can persist Home Assistant and SMB credentials after first start. SMB shares are stored only in `HANK_SMB_SHARES_JSON`. Keep the host file `0600`; only set `HANK_AGENT_CONTAINER_USER` if you also manage `.env.agent` and agent volume ownership for that custom container user.

The generated file should look like this:

```env
HANK_AGENT_CLOUD_URL=ws://cloud:8080/ws/agent
HANK_AGENT_ID=home-main
HANK_AGENT_TOKEN=replace-with-issued-token
HANK_AGENT_HOME_NAME=Home
HANK_AGENT_CONFIG_PATH=/app/.env.agent

HANK_AGENT_FILES_ROOT=/srv/hank/files
HANK_AGENT_SHELL_ENABLED=false
HANK_AGENT_NOTES_ROOT=/srv/hank/notes
```

`HANK_AGENT_SHELL_ENABLED` is the single shell permission switch for the primary Hank Agent. Setting it to `true` enables both compatibility shell commands and resumable live terminal sessions; changing it back to `false` prevents new sessions. Leave it disabled unless remote administration is intentionally required.

Keep this value unchanged for the single-server Compose deployment:

```env
HANK_AGENT_CLOUD_URL=ws://cloud:8080/ws/agent
```

After the agent is online, use Settings > Connections to save Home Assistant and SMB credentials; the agent persists those values back into `.env.agent`.

When no file sources are configured, the agent uses the Docker-managed `hank_agent_files` volume for file operations. The File Server supports two source types, both managed from Settings > Connections:

- **Network (SMB) shares** connect to a NAS or another machine on the network; the agent persists the share list in `HANK_SMB_SHARES_JSON`.
- **Host folders** serve a directory on the primary Hank Agent itself. The directory may live anywhere the agent can reach on the host and is created on save when "Create folder if it does not exist" is checked. The agent persists host folders in `HANK_AGENT_FILES_ROOTS_JSON` (a JSON array of `{id, name, root, policy}` objects); the legacy single-root `HANK_AGENT_FILES_ROOT` is still read at startup and is folded into that list the first time host folders are saved. Access stays confined to each folder's tree, but the folder itself is not sandboxed to a base path, so only point it at directories you intend to expose.

For optional HankAI app workflows such as Hermes, Gramaton, or other trusted first-party command apps, build the app package with the matching `scripts/package-*-app.sh` helper, then open Settings > Apps and import the generated `.hankapp` archive. When using Codex for app work, use the local `hank-create-app` / "Hank App Builder" skill and require it to produce a non-empty `dist/<id>.hankapp` before treating the app as install-ready. Settings > Apps can also accept a selected app folder; the folder must contain `app.json` at its top level, plus the `runtime.command` file and any referenced schema files. Configure the installed app from its Apps-page Configure action; the fields come from the package `config.settings_schema`. Secrets stay agent-side and are shown in the dashboard only as set/unset metadata. HankAI slash commands such as `/Hermes` and `/gramaton` appear from installed enabled app metadata, not from built-in dashboard command lists. Each installed app also has an access setting: `admins_only` keeps all commands in that app admin-only, while `home_members` makes every command in that app available to regular home members who can use HankAI. Home admins can use the confirmed Uninstall action while the primary Hank Agent is online; it removes the agent-side package, settings, and secrets before deleting the server-side app record. If a new app needs a server-side primitive HankServerside does not expose yet, add that primitive to the cloud/agent protocol before relying on the package.

If you have an older `.env.agent` with legacy single-share SMB keys, convert it before updating the agent:

```bash
cd /srv/hank/HankServerside
scripts/migrate-agent-smb-env.sh .env.agent
```

## 9. Privileged Linux RMM Agents

Build and sign a HankAgent Linux release in its standalone repository, then copy the complete `dist` contents into `HANK_LINUX_AGENT_RELEASE_HOST_DIR`. Keep the signing private key off the Hank server. The cloud container receives the directory read-only and validates the Ed25519 signature, artifact sizes, and SHA-256 values before listening; invalid configured release state stops cloud startup.

After restarting cloud, an administrator opens the Agents page and creates a Linux installer link. Hank displays the secret-bearing command once and keeps it only in page memory:

```bash
curl -fsSL "https://your-host/install/linux/ONE_TIME_TOKEN" | sudo bash
```

The 256-bit link expires after 15 minutes and is atomically single-use. It installs an outbound-only `hankagent.service` running as root. The device generates its own 256-bit runtime credential, sends only the SHA-256 hash, rotates every 30 days with at most 24 hours of overlap, and supports administrator-requested rotation or immediate revocation.

This is an unrestricted RMM authority. A Hank administrator can use root shell, root files, process signals, package changes, and systemd lifecycle operations on the enrolled machine. Protect administrator accounts, the deployment database, TLS endpoint, release public-key configuration, and release signing workflow accordingly.

The Agents page shows safe enrollment history without bootstrap token material. Revoking an unused link prevents enrollment. Revoking all credentials immediately rejects new connections and disconnects the active agent; re-enrollment then requires a new link.

## 10. Start The Primary Hank Agent

After `.env.agent` exists:

```bash
cd /srv/hank/HankServerside
docker compose --env-file .env.cloud --profile agent up -d agent
docker compose --env-file .env.cloud --profile agent ps
```

Check the agent logs:

```bash
docker compose --env-file .env.cloud --profile agent logs -f agent
```

The dashboard should show the Primary Hank Agent as online.

Admins can also restart an online primary Hank Agent from Settings > Home. The
button sends a controlled `system.restart` command to the agent, and the agent
exits after acknowledging the request. The host supervisor must restart it; the
Compose deployment does this through the agent service restart policy.

## 11. Storage And Backups

Open `/dashboard/settings/backups` as an admin.

After first boot:

1. Confirm checksum status is enabled.
2. Run a manual backup.
3. Run a restore test after the first backup exists.
4. Confirm storage status shows no new failures.

Primary restore still requires the typed confirmation phrase and now also uses a short-lived admin action token issued immediately before the restore request. The dashboard handles this automatically.

Default schedule:

- full backup: Sunday at 02:00
- differential backup: Monday through Saturday at 02:00
- checksum status: every 15 minutes
- `pg_amcheck`: Sunday at 03:30
- restore verification: Sunday at 04:00

Back up these volumes and files:

- `hank_pg18_pgbackrest_repo`
- `hank_note_attachments`
- `hank_db_ops_state`
- `hank_agent_files`
- `hank_agent_notes`
- `/srv/hank/HankServerside/.env.cloud`
- `/srv/hank/HankServerside/.env.agent`

`hank_pg18_postgres_data` is the live PostgreSQL 18 database volume. It mounts at
`/var/lib/postgresql` while `PGDATA` remains explicit at
`/var/lib/postgresql/data`; this avoids the anonymous parent volume otherwise
created by the PostgreSQL 18 image. Once pgBackRest is running,
`hank_pg18_pgbackrest_repo` is the encrypted database restore source, and
`hank_pg18_postgres_restore_data` is the separate restore-test volume.
`hank_note_attachments` stores note attachment files outside Postgres and must be retained with the database backups.
New attachment snapshots are streamed as `hank-attachments/<backup-label>.tar.gz.age`
inside the backup target, using authenticated age passphrase encryption with the
existing repository key. No plaintext intermediate archive is written; completed
archives have mode `600`. Restore verification authenticates the complete archive
in a private staging directory, then checks every live attachment's size and
SHA-256 against the restored database before publishing verified files to the
separate restore-test volume. Failed authentication or metadata validation leaves
previous verified files intact; publication across a mounted volume is not an
atomic whole-directory replacement.

Older `.tar.gz` archives remain readable only when the corresponding encrypted
archive is absent. An unreadable or corrupt `.age` archive never falls back to
plaintext. Existing plaintext archives are neither rewritten nor deleted by
this change. Legacy regular, sparse and safe hard-linked files remain readable;
hard links become independent files inside the restore directory. Symlinks,
special files and escaping paths are rejected. CLI recovery can use
`age --decrypt --output /private/path/attachments.tar.gz <archive>.age` with the
key entered at its passphrase prompt; protect and remove the resulting plaintext
using the operator's recovery procedure. Never put the passphrase in command
arguments.

Primary restore replaces the database and matching attachments as one operation.
It requires an explicit backup label and an administrator confirmation token
bound to that Home and label. The worker restores an isolated PostgreSQL instance,
waits until recovery promotes to a writable primary, applies embedded versioned
migrations there, authenticates the matching archive,
and verifies original and preview sizes and SHA-256 hashes against that restored
database. Historical rows are not compared with today's live rows. An empty
attachment set publishes an empty live set. Missing or corrupt required bytes
fail before stopping the live services.

The worker then copies the prepared database and files onto their destination
filesystems, stops cloud and PostgreSQL, and replaces both datasets under a
synced recovery journal in the shared db-ops state directory. Cloud startup
checks this journal before opening its store or repairing files. An interrupted
replacement blocks startup until db-ops recovers the original pair; a worker
restart recovers before scheduling work and archives the interrupted intent to
prevent replay. PostgreSQL readiness, live attachment hashes, and cloud health
are checked before reporting success. Rollback failure leaves cloud stopped.

Original datasets remain in private `.hank-recovery/<recovery-id>` directories
beside PGDATA and inside the attachment volume. Attachment backups, permission
repair and orphan pruning exclude this reserved directory; it is never a valid
attachment key. Plan free space for the isolated restore, prepared copy and
retained originals. Retained recovery datasets require deliberate operator
cleanup after acceptance; they are not automatically deleted.

The db-ops attachment mount is writable for paired replacement. Cloud and
`postgres` share the configured attachment group (`HANK_DB_OPS_SHARED_GID`,
which must match the cloud group). Live directories use setgid `2770` and files
use `660`; private recovery directories remain `700`. The db-ops entrypoint
prepares group access while preserving file owners. Restored files are readable
and writable by cloud through the shared group, without world access. Compose
also supplies this supplementary group to one-off db-ops checks that bypass
the entrypoint. Native
installations must provision equivalent groups, writable restore/state roots,
and PostgreSQL/Compose orchestration. Restore roots must be separate real
directories; external tablespace/WAL symlinks require a separate recovery plan.

The same volume contains immutable MCP-uploaded originals, generated image previews, and a
`.mcp-staging` subtree for resumable uploads. Do not exclude that subtree from a live backup: the
database and filesystem should be captured together so open transfers remain resumable. Open MCP
upload sessions expire after 24 hours; hourly maintenance removes terminal staging files and
retention-aged orphaned objects while preserving live originals and previews.

Before pgBackRest starts any full or differential backup, `db-ops` now compares
every active attachment row (and generated preview) with the live filesystem.
The backup fails closed on a missing or unreadable file, size mismatch,
duplicate or unsafe storage key, non-regular file, or symlink in any path
component. This prevents a successful database backup from being paired with
an incomplete or escaped attachment archive. The storage event reports only
counts and attachment identifiers, never filenames, paths, or file contents.

MCP attachment limits are fixed platform contracts: 100 MiB originals, 4 MiB decoded transfer
chunks, 256 KiB HTML-source reads, 40 megapixels per decoded image, and 2048-pixel/5 MiB generated
image previews. No new environment variable is required. Before deploying a build with this
feature, run the normal migration workflow and confirm migration
`000032_mcp_note_attachment_transfers` is applied:

```bash
make migrate-status
make schema-drift-check
```

Reverse proxies in front of `/v1/mcp` must pass `text/event-stream` responses without buffering
and use an idle timeout longer than Hank's 15-second heartbeat. Deployment shutdown gracefully
completes subscriptions; compatible clients reconnect, rediscover, re-listen, and refetch the
current tool list. Manual connector recreation is recovery for a non-compliant host, not the
normal rollout path. Operators can watch the fixed-cardinality
`hank_mcp_note_attachment_*` metrics; these contain statuses, byte totals, stable error codes, and
cleanup counts, never filenames or content. The `hank_mcp_subscription*` metrics contain only
aggregate counts and fixed reason/kind labels. An application rollback may leave migration 32 in
place because it is additive; subscription state is in-memory and requires no conversion.

## 12. Normal Updates

Migration 37 adds one Home-owned delivery-settings row with encrypted SMTP and
webhook credentials. Existing Homes receive inbox-on/admin-only and email-off
settings when the managed runtime initializes; SMTP credentials are never
imported from external files automatically. The primary-key/Home foreign key and
setting constraints enforce integrity; no bulk backfill or additional indexes
are required. Its down migration refuses to discard any configured row. Preserve
settings and the application encryption key for coordinated rollback/recovery.


Migration 36 adds the monitoring category and a default-on `monitoring_enabled`
preference, including existing settings rows. It preserves other preferences
and uses existing inbox/receipt indexes. Older clients may omit the new setting
in updates. The down migration refuses to remove the category while monitoring
history exists; coordinate application/schema rollback and preserve that history
rather than deleting it automatically.

After the agent is active, update the stack with the agent profile:

```bash
cd /srv/hank/HankServerside
git pull
docker compose --env-file .env.cloud --profile agent up --build -d
```

Check status:

```bash
docker compose --env-file .env.cloud --profile agent ps
curl http://127.0.0.1:18080/readyz
scripts/doctor.sh
```

Use your custom port if you changed `HANK_CLOUD_HOST_PORT`.

## Demo-only automatic Linux release promotion

Tagged HankAgent Linux releases may be promoted automatically only to `Hankdemoserver`, serving `https://hankdemo.campbellservers.com`. The restricted deploy account accepts one command, `promote MAJOR.MINOR.PATCH`, and no interactive shell. Its SSH key is separate from the signing key; the private signing key never reaches the server or deployment job.

On the demo host, run the installer from the reviewed HankServerside checkout as root with the dedicated deploy public key:

```bash
sudo env HANK_LINUX_RELEASE_DEPLOY_PUBLIC_KEY='ssh-ed25519 AAAA... hank-linux-demo-release' \
  scripts/install-linux-release-deployer.sh
```

The installer creates `/srv/hank/linux-agent-releases`, configures `.env.cloud` to mount its atomic `current` link, installs the forced command, and grants the deploy account sudo access only to that promoter. Promotion rejects other hostnames and public URLs, malformed or duplicate versions, paths, links, oversized files, incomplete checksums, invalid signatures, and binaries built from different source commits.

After verification, promotion atomically switches `current`, recreates only the cloud container, compares the live manifest byte-for-byte, updates the installed root HankAgent through its signed update command, waits for its authenticated reconnect, and runs a privileged local health probe. Any failure restores the prior release link, cloud container, and `.previous` agent binary.

Only after those demo checks pass does the promoter register the immutable release and activate an `all-linux` rollout. Eligible system workers receive a cryptographically randomized `not_before` time within 15 minutes; offline workers remain queued until their next authenticated connection. Native agents use `system.update.apply`. The one-time `0.2.x` compatibility hop installs the same rollback guard and pending-assignment state through the already-authorized root shell before replacing the binary. User-mode workers are reported as requiring manual updates.

The Agents dashboard shows the target and per-device state. Administrators can pause or resume dispatch, retry a failed/rolled-back device, or pin a device to an already registered version. Pausing does not interrupt an installation already in progress. A healthy reconnect is accepted only when the authenticated agent identity, assignment ID, and reported target version all match.

The active release, three previous versions, pinned versions, nonterminal assignments, and rollback references are retained. Other immutable version directories are pruned only after activation succeeds; symlinks and non-version paths are refused.

## Verification Checklist

- `.env.cloud` exists in `/srv/hank/HankServerside`
- `.env.cloud` has real database, db-ops, and backup encryption secrets
- `.env.cloud` has a stable `HANK_SECRET_ENCRYPTION_KEY`
- `.env.agent` does not exist until after the first admin creates an agent token
- first boot starts `postgres`, `db-ops`, and `cloud`
- first admin registration works once on a fresh database
- later public registration returns `403`
- the agent starts only after `.env.agent` exists
- `/dashboard/settings/backups` loads for admins
- Primary Hank Agent status becomes online
- the Hank app can sign in through the public URL

### File Job Ownership Migration

Migration 38 adds a nullable agent owner to file jobs, an index, and a composite
Home/agent foreign key. Existing jobs remain unowned because old retries could
change machines without durable attribution. Deleting an agent clears only its
job ownership and preserves history. There is no automatic historical backfill.
The down migration refuses to discard populated ownership. Apply the migration
before starting the updated cloud; old agents retain the existing wire format.

## Restricted installable-app runtime

The primary Hank Agent requires bubblewrap and a dedicated runtime root for optional
`.hankapp` execution. `HANK_AGENT_APP_RUNTIME_DIR` defaults to
`/var/lib/hank/app-runtime/v1`. Build the root using the `app-runtime` target of
`Dockerfile.server`; export that image's filesystem to a private operator-managed
directory and set this variable when using another location. Never point it at
`/`, host `/usr`, or a directory containing agent credentials. The normal server
image includes bubblewrap and this runtime for its bundled agent binary.

The deployment must permit creation of unprivileged Linux namespaces and loading
seccomp filters. It also requires Linux cgroup v2 with an empty, writable delegated
subtree at `HANK_AGENT_APP_CGROUP_ROOT` (default `/sys/fs/cgroup/hank-apps`).
The operator must enable `cpu`, `memory` and `pids` in that subtree’s
`cgroup.subtree_control`; the agent itself must run outside that subtree.
`cgroup.kill` and atomic `CLONE_INTO_CGROUP` placement are mandatory. Hank creates
and removes only its own invocation children, enforcing 512 MiB memory, zero swap,
64 processes/threads and one CPU core per invocation. A systemd native-agent
deployment needs controller delegation and a separately prepared empty child
subtree; simply setting the environment variable does not grant delegation.
The default Docker/AppArmor configuration can block nested
namespaces and supplies a read-only cgroup mount; optional apps then fail closed with `app_sandbox_unavailable`, while
core agent capabilities continue to work. A native agent or a separately reviewed
container confinement profile is required before enabling restricted apps in such
an environment. Do not add a privileged container, host-wide SYS_ADMIN access,
or unconfined production security profiles as a workaround.

The Compose `hank_agent_app_permissions` volume persists grants separately from
packages. Native installs must preserve the private `apps-permissions` directory
beside `HANK_AGENT_APPS_DIR`. Follow [app migration and grants](app-platform.md)
before enabling older packages; package updates and target changes require review.


### Assistant execution storage

Migration `000041_assistant_execution` adds the staged v2 task engine tables and
a session scope constraint. It leaves v1 runs and client behavior intact. Run
the normal migration/status/drift workflow before starting the updated server;
handlers and worker startup do not create tables. No provider or credential
change is required.

Do not roll back to a server that lacks task fencing/deletion handling while
v2 work or uncertain operations remain. Stop workers, resolve or explicitly
record uncertain outcomes, and preserve a reviewed backup including receipts.
The down migration refuses to remove nonempty task/receipt stores; it is not a
production cleanup command. Destination receipt files also need to survive an
agent restart or upgrade. The operation capability is reserved and is not yet
advertised by agents.

Migration `000042_assistant_staging` adds immutable owner/session attachment
bindings with 24-hour expiry and bounded size/checksum metadata. Verified bytes
use `.assistant-staging` below the existing note attachment storage volume.
Back up that volume and PostgreSQL together. Expired or deleted-session handles
are unreadable; stage files may remain until reviewed storage maintenance. No
background production cleanup or staging upload route is enabled in this phase.
The down migration refuses nonempty bindings. Interrupted uploads can restart
from byte zero using the same attachment ID and checksum; changed metadata
requires a new ID.

Migration `000043_assistant_session_execution` records conversation execution
versions, backfills sessions with existing tasks to v2, and adds the one-active-
task-per-session constraint. Existing duplicate active tasks must be reviewed
before migration; the migration does not cancel them. Its down migration
refuses populated v2 conversations. Do not downgrade an adopted conversation.

`HANK_ASSISTANT_EXECUTION_ENABLED` defaults to false and gates new
agent tasks. Enabling it uses the existing user/provider configuration; no new
credentials are required. Ollama native tool models and OpenAI API-key chat
models are supported transports; linked ChatGPT is not. The recovery worker
runs regardless of admission so disabling the flag does not strand queued work.
Compose passes this setting through the existing cloud environment file.

### Agent operation receipts

`HANK_AGENT_OPERATION_DIR` defaults to `/var/lib/hank/assistant-operations`.
Compose persists this core state in `hank_agent_operations`, independently of
optional apps. Keep the directory private and preserve it across agent upgrades.
The agent advertises durable assistant operations only when it can exclusively
lock and synchronize this store. Failure leaves existing read capabilities
available but disables durable assistant operations. Platforms without the
required journal locking fail closed.

The journal records operation identities, action digests, outcomes, and bounded
verification results, not command arguments or credentials. Its persistent epoch
binds operations to this particular receipt store. Replacing the volume changes
the epoch and cannot authorize replay of an operation from the old store. Missing
or corrupt epoch metadata alongside receipts requires operator recovery; Hank
does not discard receipts or initialize over them. An uncertain receipt is never
permission to repeat a write. Back up this directory with agent state; do not
manually clear it to retry a failed task.

Verified agent upload staging lives under the same operation directory and
uses bounded chunks; it is not a configured file source. Preserve it with the
receipt journal while operations are pending. Staged transport retries compare
existing bytes and verify the complete SHA-256 before making bytes executable.

### Assistant machine service allowlist

Machine service tools are disabled by default. On a Linux agent with systemd,
`HANK_AGENT_ASSISTANT_SERVICE_OPERATIONS_JSON` can explicitly grant individual
unit operations, for example:

```json
[{"unit":"example-worker.service","operations":["restart"]}]
```

Use actual loaded `.service` unit names, not aliases or paths. Each grant lists
only reviewed `start`, `stop`, or `restart` operations; at most 32 units are
accepted. Invalid grants prevent agent startup. The agent invokes a fixed
`/usr/bin/systemctl` path with separate arguments, bounded time/output, and no
shell. Existing service-manager permissions still apply. A configured binary
without a working system manager does not make service actions available.
The server requires Home administrator scope on every inspection/preparation
and before dispatch. Approval binds the exact agent, unit, operation, observed
state, and invocation identity. Restart is confirmed only when a new invocation
is observed; an unchanged invocation is accepted but unverified. Reboot, package
changes, process termination, and unrestricted shell are not assistant tools.

Assistant staging admission is serialized by the single cloud writer and capped
at 4 GiB of stored bytes, including interrupted/orphan files, with 100 MiB per
attachment. Expiry removes access, not bytes; capacity exhaustion refuses new
uploads. Retention cleanup is an explicit operator action, not automatic data
deletion. Keep at least one attachment-sized margin for verified retry writes.

## Isolated Linux fleet demo

The [fleet demo](fleet-demo.md) uses separate server/Linux worktrees. Apply embedded
migration `000046_fleet_demo` through the normal migration workflow before
starting the new server. It adds grant, target, workspace and job metadata tables
and an agent/Home identity index, with no backfill or changes to existing rows.
The schema constrains target Home identity, grant/workspace/job ownership, valid
states and nonnegative output cursors. Old agents remain usable for their existing
capabilities; only new agents advertise `fleet.v1`.

The Linux worker owns `StateDir/fleet` (system mode:
`/var/lib/hankagent/fleet`; user mode: its existing XDG state directory). The
existing local shell setting gates fleet capability. Files/directories are private;
include this state in agent-local backups if retained job output/workspaces matter.
No extra network listener is configured. State recovery never reruns interrupted
commands. There is no automatic state purge.

`HANK_FLEET_SERVER`, `HANK_FLEET_TOKEN_FILE`, and the grant-management-only
`HANK_FLEET_SESSION_TOKEN` are CLI inputs, not server or enrollment configuration.
Never place credentials in arguments, URLs, manifests, repository files or logs.
The CLI token file must be mode 0600 and must not be a symlink.

Rollback: stop/revoke fleet access and reconcile/cancel active work first. An older
server binary can run with the additive tables left in place. The down migration
removes fleet metadata and therefore invalidates grants and loses cloud job
history; back up first and apply only through explicit operator database work.
It does not remove private agent files. Do not automatically downgrade schema or
clean production data at startup. No release, signing, installation or deployment
is implied by building these worktrees.

### Linux short-code pairing and hosted fleet settings

Linux agent 0.3.3 adds `sudo hankagent --system setup` with a localhost browser
form, or `setup --terminal` on headless hosts. Create its one-time code in the
Agents page. Server names/IPs are normalized to HTTPS and require trusted TLS;
pairing does not disable certificate checks. See [fleet setup](fleet-demo.md#gui-setup-and-hosted-mcp).
MCP enablement is now saved in PostgreSQL from AI & MCP settings; environment
`HANK_MCP_ENABLED` is the fallback only when no saved setting exists.

Migration 47 adds optional MCP connection binding to fleet grants and the
singleton connector setting. Existing CLI grants and enrollment records remain
compatible. Hosted grants are transferred atomically during OAuth token refresh.
The down migration revokes hosted grants before dropping their binding, and
removes the saved connector override. Back up before rollback; downgrade only
through the versioned migration workflow, after cancelling active hosted jobs.

## Device pairing

Agents > Add device creates a one-time code for Linux, Mac, or Windows. Codes
expire after 15 minutes and work only for the selected platform. Enter the code
in Hank Agent settings on Mac or Windows, using the displayed server address.
Linux uses `sudo hankagent --system setup`; its installer link remains available
in the same panel. Existing enrolled devices keep their credentials.

Mac and Windows generate their runtime credential locally and store it in their
existing OS credential store; the server receives only its hash. Pairing grants
worker connectivity and does not bypass remote-desktop identity approval or OS
privacy permissions. Account sign-in remains separate from worker pairing.
Manual token creation and credential history remain under Advanced.

Migration 49 expands the enrollment platform constraint without changing existing
rows. Rolling it back requires resolving non-Linux enrollment records first;
the rollback deliberately refuses rather than deleting them. Migration 50 stores
per-user fleet-access dismissals. Its rollback loses dismissal preferences only,
not access grants or their history.

Chat attachment staging uses the same private attachment group as note files:
setgid directories are mode 2770 and staged files are mode 0660. The encrypted
backup worker must be able to traverse `.assistant-staging`; do not exclude it
from backups because durable tasks retain references to those bytes. Startup
permission repair also covers staging created by older releases.
