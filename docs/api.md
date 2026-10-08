# Hank Platform API

This document maps the stable interface families owned by HankServerside. It is
not a generated OpenAPI specification. Exact handler registration lives in
`internal/cloud/server.go`; cloud/agent messages live in `internal/protocol`;
focused contracts are linked below.

## Versioning And Compatibility

HTTP APIs use the `/v1` prefix. Cloud/agent WebSocket messages use a versioned
envelope with request IDs, command/event types, structured payloads, and stable
error codes. Native remote desktop and `.hankapp` packages have their own
versioned schemas.

Breaking client, agent, MCP, remote-desktop, or app-runtime changes require a
new version, a compatibility adapter, or a documented migration. Internal
handler refactors do not change the public contract by themselves.

## Authentication Modes

- Browser: server session cookie; unsafe writes also require
  `X-Hank-CSRF-Token`.
- API clients: scoped bearer token where the owning endpoint supports it.
- Client WebSocket: a short-lived ticket from `POST /v1/ws/app-ticket`.
- Hank Agent WebSocket: `Authorization: Bearer <agent-token>` plus
  `X-Hank-Agent-ID`.
- MCP: OAuth registration/authorization/token flow with approved tool scopes.
- Remote desktop: authenticated administrative HTTP setup plus independently
  scoped, single-use browser and agent join credentials.

Authentication never replaces resource authorization. Handlers enforce user,
Home, role, permission, agent, session, and token scope server-side.

## Public And Operational Routes

- `GET /healthz`: public process health.
- `GET /readyz`: public dependency readiness without sensitive detail.
- `GET /metrics`: administrator session or configured scrape-token access.
- `/`, `/join`, `/password-change`, `/dashboard`, and dashboard subroutes:
  browser entry points.
- `/manifest.webmanifest`, `/sw.js`, `/offline.html`, and `/assets/*`: PWA and
  static assets with their documented cache policy.
- `/install/linux/*` and `/install/linux-release/*`: tightly scoped managed
  Linux enrollment/release resources.

## Identity And Profile APIs

`/v1/auth/*` owns registration, login, logout, password change, invitations,
and configured Entra flows. `/v1/me` and `/v1/ui/bootstrap` return the current
identity and browser bootstrap state.

User-scoped families include:

- `/v1/me/profile`, `/profile-secret-vault`, and `/profile-backup`
- `/v1/me/notes/*`
- `/v1/me/notification-settings` and `/notifications/*`
- `/v1/me/web-push/*` and `/devices/*`
- `/v1/me/mcp/*`
- `/v1/oauth/openai/*`

Profile Notes, private settings, device registrations, subscriptions, and
notifications are always bound to the authenticated user.

## Home And Administration APIs

`/v1/home` represents the deployment's singleton Home. `/v1/home/*` owns:

- setup status and Home settings
- invitations, members, roles, and permissions
- quick links, audit events, logs, and query telemetry
- agent inventory, setup tokens, enrollments, credentials, releases, and
  lifecycle actions
- service profiles and agent-managed configuration
- shared Notes and synchronization
- file transfers, sources, jobs, and previews
- assistant sessions, runs, confirmations, settings, models, logs, media, and
  attachments
- storage status, backup configuration, backup, restore test, and primary
  restore
- installed app import, configuration, access, and invocation
- native remote-desktop trust and Home/session administration

Administrative writes require the appropriate Home role/permission and browser
CSRF when cookie-authenticated. High-impact actions add exact confirmation and
short-lived action-token requirements.

Assistant execution version negotiation is specified in
[HankAI](hankai.md#status-and-diagnostics). Status advertises version 2 only
when opt-in admission and the configured provider support it; explicit
unsupported versions fail before message execution.

## Hank Agent APIs And Routing


Agents enroll or rotate credentials through scoped HTTP flows and maintain
`GET /ws/agent`. One primary agent owns untargeted Home commands. Worker agents
receive commands explicitly targeted by agent ID or machine-scoped workflows.

The shared command families include:

- `system.*` and configuration/status operations
- `homeassistant.*`
- `files.*`
- `notes.*`
- `media.*`
- `apps.*`
- machine telemetry, shell, process, service, package, update, and
  remote-desktop control families

The server verifies the authenticated connection, stored agent type, Home,
target, and capability. Long-running work reports durable jobs/events rather
than holding a single HTTP request open.

## Files And Notes

File operations use agent/source/path scope and managed transfer/job records.
`files.list` returns one folder. `GET /v1/home/file-search?q=...&source_id=...&agent_id=...`
searches the selected agent and source through the platform file catalog. It
returns up to 100 file/folder matches plus `status` (`ready`, `indexing`,
`partial`, or `offline`) and per-source progress. An omitted agent selects the
primary; an omitted source selects that agent's default. `GET /v1/home/search`
searches permitted platform content and indexed files across connected agents,
and returns `file_index_status` when the file catalog is incomplete. Search
requires Home membership and file feature permission; every returned file path
also passes the current Home and source policies. See [Search](search.md).
Uploads and downloads use bounded transfer tokens, explicit offsets, and
server-side ownership. Cross-source work supports cancellation, retry, and
rollback where the owning job contract allows it. Job snapshots include
`agent_id`: new jobs record the agent selected at dispatch. Move progress must
come from that agent, and HTTP/WebSocket recovery uses the recorded agent and
current file access policy. A changed primary never redirects recovery.
Historical move jobs with an empty owner require administrator review before
recovery. Administrators can use the Transfers panel to select the exact machine
for failed, cancelled, or rollback-required moves. Offline registered machines
may be attributed because saving ownership runs no recovery command. Known
owners cannot be overwritten, and active or completed jobs cannot be attributed.
`GET /v1/home/file-jobs?owner=unknown` provides an administrator-only queue of
50 unresolved moves, with `next_cursor` passed as `after` for the next page.
It remains available when file browsing is offline.
`POST /v1/home/file-jobs/{id}/owner` first issues a five-minute action token with
`agent_id`, the displayed `expected_updated_at`, and `request_action_token: true`.
Saving requires those same fields, `admin_action_token`, and
`confirmation: "CONFIRM OWNER"`. The single-use token binds the administrator,
Home, job, selected agent, status, and reviewed timestamp. A concurrent change
requires fresh review. Assignment is audited without logging file paths.
An offline owner produces an error without falling back to another
machine during recovery. Cancellation requires the move agent's positive acknowledgement.
Completed move jobs ignore delayed progress and timeout failures; a cancelled
move may still report that a verified copy requires rollback.
Streaming downloads and previews negotiate `flow_control: "ack.v1"` and a
262144-byte `window_bytes` in `file.transfer.open` and `file.transfer.ready`.
Agents send contiguous chunks of 32768 bytes (the final chunk may be shorter)
and wait when unacknowledged bytes fill the window. `file.transfer.ack` carries
the absolute consumed `offset`; the server grants credit only after HTTP writes
succeed. Credit applies to one authenticated transfer attempt and socket.
Malformed chunks, premature completion, or excess credit use terminate that
attempt without blocking the shared agent reader. Agents without this negotiated
mode receive `agent_update_required`; update agents before enabling downloads on
an upgraded server. Upload framing is unchanged.

Cancelling a transfer job expires its durable bearer lease, interrupts its active
HTTP stream, and sends cancellation to the originally assigned agent connection.
Cancellation also closes agent upload handles. Late progress cannot revive a
cancelled job or lease. A disconnected download retains its consumed offset for
resumption while its original lease remains valid; explicit cancellation requires
creating a new transfer.

Upload size failures return `413` with the stable `upload_too_large` code plus
`max_upload_bytes` and `attempted_upload_bytes` so clients can explain the
configured limit. `DELETE /v1/home/file-jobs` clears completed, failed,
cancelled, and rolled-back history for the authenticated Home, including legacy
upload/download jobs marked `rollback_required`. It preserves queued, running,
and rollback-required move jobs.

`POST /v1/home/file-jobs/{id}/dismiss` lets a Home administrator explicitly remove
one `rollback_required` move record without executing file operations. Review
sends `expected_updated_at` and `request_action_token: true`; confirmation sends
the same timestamp, the returned `admin_action_token`, and
`confirmation: "REMOVE HISTORY"`. The five-minute, single-use token binds the
administrator, Home, job, owner, status, and timestamp. Changed or active jobs
are rejected, and removal is serialized with rollback. Removing history forfeits
that record's retry/rollback information and is audited without file paths.
Cookie requests require the normal CSRF header. Ordinary members cannot review
or remove these records. No schema migration is required.

Clearing or removing history emits `files.history_changed` with `home_id` on the
Home-scoped `files.jobs` subscription. Clients reload their job list on this event
and ignore responses from requests superseded by a successful clear.

Notes and Kanban expose user and shared-Home scopes, optimistic revisions,
conditional mutation, collaboration, attachments, search, tags, notebooks,
boards, and offline reconciliation. See [Notes and Kanban](notes.md).

## Notifications

The notification APIs expose a durable user inbox, read/delete operations,
category preferences, Web Push configuration, and session-owned subscriptions.
Inbox persistence remains enabled when a user disables a push category.
Equivalent unread events may coalesce within the server window; records expire
through lifecycle maintenance.

Subscription endpoints and browser key material are validated, encrypted, and
never returned after storage. See [PWA and notifications](pwa.md).

## Assistant And MCP

Assistant APIs own settings, provider/model discovery, sessions, messages,
attachments, runs, confirmations, client-tool results, media jobs, logs, and
index status. Providers receive only context allowed by the current user's
settings and permissions.

The optional MCP surface uses:

- OAuth metadata under `/.well-known/*`
- client registration and authorization under `/v1/oauth/mcp/*`
- the stable OAuth resource at `/v1/mcp`, supporting MCP `2026-07-28` only
- independent JSON-RPC POST methods including `server/discover`, `tools/list`, `tools/call`,
  `resources/list`, `resources/read`, and SSE-backed `subscriptions/listen`
- user grant management under `/v1/me/mcp/*`

Tool access is scope-gated and independent of ordinary browser permissions. See
[MCP integration](mcp.md) and [HankAI](hankai.md).

## Native Remote Desktop

Remote desktop V1 separates administrative trust/session APIs from the opaque
encrypted data plane:

- `/v1/home/desktop-trust/*`: Home trust administration
- `/v1/agents/{agentID}/desktop-*`: readiness and session creation
- `/v1/desktop-sessions/{sessionID}/*`: lifecycle, reconnect, and termination
- `/ws/desktop/browser/{sessionID}`: browser side of one session
- `/ws/desktop/agent/{sessionID}`: exact Hank Agent side

Trust and session writes require a Home administrator, CSRF, strict public-key
and certificate validation, and exact confirmations where destructive. Join
credentials are single-use and side-specific. The relay accepts only bounded
binary outer frames and cannot decrypt the content. See
[Remote desktop operations](remote-desktop/v1-operations.md) and
[V1 acceptance](remote-desktop/v1-acceptance.md).

## WebSocket Roles

- `/ws/app`: authenticated client command/response and realtime events.
- `/ws/agent`: authenticated outbound Hank Agent control connection.
- `/ws/desktop/browser/*`: one authorized browser side of an encrypted desktop
  session.
- `/ws/desktop/agent/*`: the exact authorized endpoint side.

WebSocket connections enforce origin or bearer authentication as appropriate,
bounded messages, routing scope, lifecycle timeouts, and disconnect cleanup.

## Errors And Concurrency

HTTP handlers use conventional status codes and bounded JSON errors where the
API family defines them. Inaccessible scoped resources generally return `404`.
Revision conflicts use `409` and the owning stable error code. Rate limits use
`429` without disclosing account existence.

Cloud/agent responses use the protocol error envelope. Clients must use stable
error codes and documented fields rather than parsing human message text.
Conditional writes must re-fetch and reconcile after a conflict instead of
blindly overwriting newer state.

### Primary backup restore

`POST /v1/home/storage/restore-primary` requires an administrator and the usual
CSRF protection for cookie-authenticated requests. Review with
`{"backup_label":"20260905-020000F","request_action_token":true}`, then submit
that exact label with `admin_action_token` and the configured `confirmation`
phrase. The single-use action token is bound to the user, Home and backup label;
a token reviewed for a different backup is rejected. The response queues a
signed intent; it does not mean restoration has completed. The worker validates
an isolated database and matching attachment archive before replacing either
live dataset. See [deployment](deployment.md) for recovery and retained originals.

### Staged assistant task controls

Existing authenticated Home routing also serves owned v2 task controls under
`/v1/home/assistant/tasks/{task_id}`: GET snapshot, GET `events` with
`after_sequence` and `limit` (1–100), POST `stop`, POST `followups`, and POST
`approvals/{approval_id}`. Cookie writes retain the existing CSRF requirement.
Admission remains unavailable while status advertises only execution version 1.

Follow-ups take `submission_id`, `expected_revision`, and `text`; decisions take
`expected_revision`, `action_digest`, and `approved`. Identical retries are
idempotent; changed retries or stale new actions return 409. Approval IDs bind
one immutable action. Snapshots omit model checkpoints and retrieved content.
Unsupported slash commands in follow-ups return 400
`unsupported_followup_command` before changing task state or cancelling an
approval. `/file` aliases `/files`; bare file queries are read-only. See
[HankAI](hankai.md#typed-tools-and-safety) for explicit file-write subcommands.
`effects_uncertain` distinguishes a stopped task from confirmation that an
already dispatched action had no effect. Events contain safe types and call IDs,
with `next_sequence`, `has_more`, and `snapshot_required` for catch-up.

V2 admission uses the existing POST `/v1/home/assistant/sessions/{id}/messages`
with `X-Hank-Assistant-Execution: 2`, `content`, and a stable `submission_id`;
it returns 202 with the task snapshot. Identical retries return the same task.
A second active task or mixed v1 write returns 409. GET session messages adds
`execution_version` and `active_task` (the latest task, including terminal
state), and assistant messages may contain `sources` with title/URI/href.
V2 attachments use the staging endpoint below before message submission.
Supported writes pause for an exact approval and resume with a structured result.

GET `/v1/home/assistant/tasks/{task_id}/source?call_id={id}&item={index}` returns
the stored source observation as plain text only for the task owner and after
current source authorization. Deleted/revoked sources return 404. Snapshots
include a safe `error_code`; private provider errors and reasoning are omitted.

### Assistant staged attachments and destination receipts

With execution admission enabled, `POST
/v1/home/assistant/sessions/{session_id}/staging` accepts a raw attachment body.
It uses ordinary Home authentication and cookie-write CSRF checks. Required
headers are `X-Hank-Attachment-ID` (stable client retry key),
`X-Hank-Filename` (percent-encoded UTF-8), `X-Hank-Content-Type`,
`X-Hank-Size-Bytes`, and `X-Hank-Checksum-SHA256` (lowercase hex).
Bodies must match their declared size and checksum and be 1 byte–100 MiB. The 201
response contains an owned stage ID and immutable metadata, never a storage path.
Repeating the same client attachment and bytes returns the same binding; changed
metadata cannot replace it. `attachments.list` exposes only stages in the task's
owned conversation. This does not itself upload to a file source.

Internal `assistant.operation_status`, `assistant.operation_stage`, and
`assistant.operation_execute` agent commands are reserved for the durable task
worker. Public app WebSocket forwarding rejects the entire `assistant.*` family.
Operation identity includes exact Home, actor, agent, action digest, and receipt
store epoch. Staging uses bounded chunks with exact offsets and SHA-256
verification. Destination creation is exclusive; an existing file/folder is not
replaced or counted as newly created. Confirmed receipts include destination
readback; uncertain receipts never authorize automatic replay. Transport failures
enter bounded durable receipt reconciliation before becoming an unknown outcome.
Opaque app commands require explicit slash selection and exact approval; their
dispatch intent is persisted before sending and never replayed after uncertainty.
An app response means accepted, not independently verified. Calendar mutations
are not exposed to the model.

### Monitoring collection checks

`GET /v1/home/monitoring-settings/health` is Home-admin-only and returns
`ready`, `checked_at`, and `checks` (`id`, `label`, `ready`). It checks fixed
operator-configured monitoring endpoints with a three-second total deadline;
unavailable, stale, or missing data is not ready. It does not expose credentials,
raw monitoring responses, or accept a URL/query from the caller. These service
checks do not prove email or inbox receipt. Existing monitoring settings and
test-alert endpoints own delivery configuration.

## Demo fleet shell execution

The Linux fleet demo consumes `GET /v1/home/agents` and the existing `shell.exec`
app WebSocket command. `agent_id` selects the exact Home agent. Shell commands
require a Home administrator and the target connection's `shell.exec` capability.
The body has `command` (nonblank, at most 65536 bytes, no NUL) and optional
`timeout_seconds` (zero for 60 seconds, otherwise 0.001 through 300). The relay
waits for that duration plus 10 seconds. Rejected payloads return
`invalid_command_payload`; unavailable shell capability returns `shell_disabled`.
The Linux worker enforces the same payload bounds independently. See
[fleet demo operation](fleet-demo.md) for result semantics and limitations.

### Scoped fleet v1

All `/v1/fleet/` responses are `Cache-Control: no-store`. Grant management uses
normal user sessions (and CSRF for cookie writes); runtime operations require a
fleet bearer credential. Query-string credentials are rejected.

| Method/path | Authorization / body |
| --- | --- |
| GET/POST `/v1/fleet/grants` | User session; request `agents`, `operations`, `duration` (or legacy `hours`) |
| POST `/v1/fleet/grants/{id}/approve` | Home administrator; preview then `confirmation`, `action_token` |
| POST `/v1/fleet/grants/{id}/revoke` | Requester or administrator; same confirmation flow |
| GET `/v1/fleet/access` | Current approved grant metadata, no credential |
| GET `/v1/fleet/agents` | Online targets within the grant |
| POST `/v1/fleet/workspaces` | `workspace.write`; `agent_id`, optional stable `workspace_id` |
| GET `/v1/fleet/workspaces/{id}/file` | `workspace.read`; query `agent_id`, `path` |
| POST `/v1/fleet/workspaces/{id}/file` | `workspace.write`; `agent_id`, `path`, `content_base64`, `revision` |
| GET/POST `/v1/fleet/jobs` | `job.read` for metadata listing; `job.run` for creation |
| GET `/v1/fleet/jobs/{id}` | `job.read`; optional numeric `after` cursor |
| POST `/v1/fleet/jobs/{id}/cancel` | `job.cancel`; persistent cancellation request |

A new job body contains `agent_id`, `workspace_id`, `job_id`, `command`, and
optional `timeout_seconds`. IDs are globally unique; workspace/job ownership
binds grant and target. A duplicate identical job submission never dispatches
again; changed content conflicts. `grant_id` is stamped server-side. Unsupported
or offline targets return 503; operation/transport failures return 502 and are
not evidence that work did not run. Cancellation intent survives those failures.

The canonical command family in `internal/protocol/fleet.go` is guarded by
`fleet.v1`. Generic app WebSocket relay rejects `fleet.*` even for administrators.
New server binaries can coexist with old agents; those agents cannot use fleet
v1 until updated. Output cursors describe delivered chunks, not necessarily the
end of retained output. See the [fleet guide](fleet-demo.md) for limits and state
semantics. File payloads reuse the existing inline base64 representation.

### GUI fleet and Linux pairing additions

Hosted MCP requests use `POST /v1/fleet/grants` with
`{"mcp_account":true,"duration":"1_month"}` (or another supported duration).
Omit `agents`, `operations` and `mcp_token_id`: selecting any of them for account
access is rejected with 400. Account approvals include every fleet operation and
all current/future devices in the requester's Home. An account request returns
`token:""`, `mcp_account:true` and the complete operation list; administrator
review and approval are still required. Grant reads include `requester_name` and
the current Home device IDs for account grants.

Any live MCP token for the approved account may use its grant. Each call checks
the credential owner/revocation/expiry, grant owner/state/expiry, Home membership,
operation, target Home and workspace/job ownership. Another account's token cannot
use it. App disconnect invalidates that credential without revoking account
access or cancelling account jobs. Account revocation denies all apps and requests
cancellation. Existing per-connection approvals retain their original scope and
binding; new `mcp_token_id` requests are rejected. CLI scoped bearer requests retain
their existing contract. Migration 52 is required before this server version.

`POST /v1/home/agent-enrollments/linux` accepts `pairing: true` and returns a
one-time `pairing_code`, `server_url`, ID and expiry instead of `install_command`.
The existing Linux consume route accepts that code in `Authorization:
Hank-Enrollment CODE`, ignoring case and hyphens. Codes use 60 random bits,
expire after 15 minutes, and share the existing atomic single-use enrollment
transaction. Pairing attempts are limited to 10/minute per client IP and
100/minute per deployment. The default request still creates an installer link.

### Cross-platform device pairing

`POST /v1/home/agent-enrollments/{linux|macos|windows}` is Home-admin-only and
requires CSRF for cookie authentication. Mac and Windows always return a
`pairing_code`, `server_url`, and `expires_at`; Linux does so with `pairing:true`.
Codes are shown once, expire after 15 minutes, and are never included in history.

`POST /v1/agent/enrollments/consume` accepts `Authorization: Hank-Enrollment CODE`
and JSON `device_id`, `name`, `agent_type:"worker"`, `platform`, `architecture`,
and `credential_hash` (SHA-256 of the device-generated credential). Platform is
`linux`, `macos`, or `windows`; IDs begin `linux_`, `mac_`, or `win_` respectively.
Architecture is `amd64` or `arm64`. The code is consumed atomically and bound to
its platform and Home; the issuer must still be an eligible Home administrator.
The Linux-specific consume route remains compatible and Linux-only.

`POST /v1/fleet/grants/{id}/dismiss` hides an expired or revoked grant for the
current viewer. Home admins can dismiss visible Home grants; members can dismiss
their own. It never revokes, deletes, or extends access. Cookie writes require
CSRF. Renewal creates and reviews a fresh grant for a connected MCP app through
the existing grant creation and approval endpoints.

`GET /v1/home/agent-enrollments` returns combined enrollment history to Home administrators; the platform-specific GET routes return only that platform.
