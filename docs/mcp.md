# Hank MCP Integration

HankServerside can expose a [Model Context Protocol](https://modelcontextprotocol.io)
endpoint so approved AI clients can use scoped Hank tools and context over an
authenticated connection.

It is **off by default**. Administrators can enable it in **Settings → AI & MCP**. The saved setting overrides the initial `HANK_MCP_ENABLED` default and survives restarts. When disabled, the public MCP and OAuth routes
below return 404 and nothing else changes.

The endpoint provides **project docs and a read-only source snapshot**, optional
**live project context**, **profile notes**, **Kanban**, and explicitly approved
**fleet device control**. Fleet device access is approved once for the account, covering all Home
devices and operations across its connected MCP apps. Home Assistant, calendars, the secret vault, and shared/home notes
are not exposed through these tools. Fleet commands run with the selected
agent account’s full permissions, including filesystem and network access.

The `code-reference/` source snapshot is a build-time copy of the Go source (`cmd/`, `internal/`,
`go.mod`) shipped in the image by `Dockerfile.server` — it is **not** the running process's source,
and `.env*`, `.git`, `data/`, and `*.db` are excluded by `.dockerignore` so nothing sensitive is
copied. It shares the `docs:read` scope and the same path-containment + extension allowlist as the
docs tools.
The image build verifies that the normal `hank` runtime user can read the
packaged documentation and source directories, including snapshots copied from
checkouts with restrictive directory permissions.

## Enabling it

| Env var | Default | Purpose |
|---------|---------|---------|
| `HANK_MCP_ENABLED` | `false` | Initial endpoint/OAuth default; the GUI connector setting overrides it. |
| `HANK_PUBLIC_BASE_URL` | _(request host)_ | Public HTTPS origin (e.g. `https://hank.example.com`). Used as the OAuth issuer and in discovery metadata. **Required for ChatGPT**, whose servers reach the endpoint from outside your network. |
| `HANK_MCP_DOCS_DIR` | falls back to `HANK_PROJECT_DOCS_DIR`, then `.` | Directory the docs tools read from. In the Docker image this is `/app`, where `README.md`, `AGENTS.md`, current `docs/`, `schemas/`, and a `code-reference/` source snapshot are shipped. Temporary plan directories are excluded. |

## Routes

| Route | Auth | Purpose |
|-------|------|---------|
| `GET /.well-known/oauth-protected-resource` | public | RFC 9728 resource metadata (points at the authorization server). |
| `GET /.well-known/oauth-authorization-server` | public | RFC 8414 authorization-server metadata. |
| `POST /v1/oauth/mcp/register` | public | RFC 7591 Dynamic Client Registration (rate-limited). |
| `GET/POST /v1/oauth/mcp/authorize` | dashboard session | Consent screen; POST issues a short-lived auth code. |
| `POST /v1/oauth/mcp/token` | PKCE | Exchanges the code (and refresh tokens) for access tokens. |
| `GET/POST/DELETE /v1/mcp` | Bearer access token | MCP `2026-07-28` JSON-RPC/subscriptions plus initialize-era Streamable HTTP compatibility. |

## Protocol compatibility

`/v1/mcp` exposes the current stateless MCP `2026-07-28` surface and an initialize-era compatibility
transport backed by the official Go MCP SDK. `server/discover` advertises only `2026-07-28`;
initialize-era clients negotiate their dated protocol from `initialize.params.protocolVersion` and
receive a stateful session. Hank advertises the server identity `hank-mcp` version `0.7.0`.

An `initialize` request does not require `MCP-Protocol-Version`, and any pre-negotiation value in
that header is ignored. Hank returns the negotiated version and `Mcp-Session-Id`. Every later POST,
GET SSE, or DELETE for that session must carry both the session ID and an
`MCP-Protocol-Version` value exactly equal to the negotiated version. Sessions are bound to the
exact OAuth access-token grant, reauthenticated on every request, and cannot be reused by another
token for the same user. GET supplies the optional standalone SSE stream and DELETE terminates the
session.

Modern requests are independent and do not create an MCP session. Each POST must contain one
JSON-RPC request, `Content-Type: application/json`, an `Accept` value containing both
`application/json` and `text/event-stream`, and these routing fields:

- `MCP-Protocol-Version: 2026-07-28`, matching
  `params._meta["io.modelcontextprotocol/protocolVersion"]`.
- `Mcp-Method`, matching the JSON-RPC method.
- `Mcp-Name` on `tools/call`, matching `params.name` (using the MCP Base64 sentinel encoding when
  the name is not safe as a plain ASCII header value).
- `params._meta["io.modelcontextprotocol/clientCapabilities"]`; client information is optional,
  but when supplied it must include a name and version.

`server/discover` advertises only `2026-07-28`, the current server identity, zero-TTL private
discovery, and `tools.listChanged: true`. `tools/list` and `tools/call` responses include
`resultType: "complete"`. Every JSON-RPC response uses `Cache-Control: no-store, max-age=0`,
`Pragma: no-cache`, and `Expires: 0`; intermediaries must not cache discovery. Batches,
header/body disagreement and unsupported modern versions are rejected. The modern transport does
not create sessions or offer tasks, replay, or multi-round-trip input requests; compatibility
sessions exist only for initialize-era clients.

Every advertised tool includes a human-readable title and explicit `readOnlyHint`,
`destructiveHint`, and `openWorldHint` booleans. These fields are required by the ChatGPT app
scanner even though the base MCP conformance suite permits them to be omitted. Hank tools are
scoped to the authenticated Hank account. Notes and read-only context tools
advertise `openWorldHint: false`; note/card deletion advertises
`destructiveHint: true`. Fleet mutations also advertise `destructiveHint: true`,
and unrestricted command execution advertises `openWorldHint: true` because a
command can access the agent's filesystem and network.

Clients request change events with `POST subscriptions/listen`. An accepted request becomes an SSE
response whose first frame is `notifications/subscriptions/acknowledged`. Hank then sends
`notifications/tools/list_changed` when an in-process tool surface changes; the notification is
level-triggered and tells clients to refetch `tools/list`, not a descriptor diff. Pending changes
are coalesced per slow subscriber and are not replayed after reconnect. A client may open at most
four listens per OAuth token, and one cloud process accepts at most 128. Heartbeat comments are
sent every 15 seconds.

Token expiry, refresh rotation, dashboard revocation, out-of-band invalidation, and server shutdown
end affected listens. Deliberate healthy closure sends the final empty `subscriptions/listen`
result before closing. Deployment shutdown therefore causes compatible clients to reconnect,
rediscover, re-listen, and refetch `tools/list` without connector recreation.

## OAuth model

- OAuth 2.1, **public clients with PKCE** (S256) — ChatGPT and Claude self-register via DCR and
  authenticate with PKCE, so there is no shared client secret.
- Authorization and authorization-code exchange require the exact canonical resource URL
  (`https://your-host/v1/mcp`). Refresh accepts that same value and, for compatibility with
  existing connectors, may omit it; in either case the stored grant must already be bound to the
  canonical resource. Tokens are rejected at the MCP endpoint if their stored resource does not
  match, preventing cross-resource token use.
- Authorization redirects include RFC 9207 `iss`, and authorization-server metadata advertises
  issuer-response support. New DCR registrations accept HTTPS redirects or HTTP loopback
  redirects only. Existing registrations are not rewritten or revoked. DCR remains available for
  current clients; metadata declares Client ID Metadata Documents unsupported so clients can fall
  back explicitly.
- `GET /authorize` reuses the existing **dashboard session** to identify the user, then renders a
  consent screen. The consent POST is CSRF-protected (double-submit token). If the browser is not
  signed into the Hank dashboard, the page asks the user to sign in first.
- Access tokens (1h) and refresh tokens (30d, rotating) are stored **hashed**; raw values are
  never persisted or logged. Codes are single-use with a 90s TTL.
- Tables: `mcp_oauth_clients`, `mcp_oauth_auth_codes`, `mcp_oauth_tokens` (migration `000016`).

## Tools and scopes

| Tool | Scope required |
|------|----------------|
| `list_docs`, `search_docs`, `read_doc` | `docs:read` |
| `list_notes`, `search_notes`, `list_note_tags`, `get_note` | `notes:read` |
| `create_note`, `update_note` | `notes:write` |
| `append_note` | `notes:append` or `notes:write` |
| `delete_note` | `notes:delete` |
| `list_note_attachments`, `read_note_attachment` | `notes:read` |
| `start_note_attachment_upload`, `get_note_attachment_upload`, `upload_note_attachment_chunk`, `finish_note_attachment_upload`, `abort_note_attachment_upload` | `notes:write` |
| `list_context_sources`, `list_context_files`, `search_context`, `read_context_file` | `docs:read` |
| `list_kanban_boards`, `list_kanban_cards`, `get_kanban_card` | `notes:read` |
| `create_kanban_card`, `update_kanban_card`, `append_kanban_worklog`, `move_kanban_card` | `notes:write` |
| `delete_kanban_card` (private Kanban app only) | `notes:delete` |

The user picks which scopes to grant on the consent screen; `notes:delete` is unchecked by
default. Note access is always scoped to the **authenticated user's own profile notes** and is
audited (`mcp.tool_called`, `mcp_oauth.*`).

### Kanban cards

Kanban tools operate on the existing profile Notes Kanban boards; they do not create a second
task database. In the Kanban UI, use **Set as default board**, choose an **intake** column, and
optionally assign unique semantic roles: Planning, Active Work, Rework, Needs Human, Review, and
Complete. When a create call omits a destination, the server uses the configured default board,
then its intake column, then its first ordered column. A missing or stale default returns the
visible board choices instead of guessing.

Read tools expose stable board, column, and card IDs. Writes require those exact IDs and never
select a card from a human-readable title. `list_kanban_cards` hides columns with the Complete role
by default, supports text/tag/due-date filters, returns at most 100 cards, and never exposes raw
`board_json`. `get_kanban_card` returns the complete Markdown description (including ordinary
links) plus metadata and authenticated download URLs for only the attachment references used by
that card. `create_kanban_card` should be called only after the user explicitly asks to capture
a task. The feature-gated private Kanban app can delete an exact card after in-widget and host
confirmation with `notes:delete`; MCP still cannot create, delete, rename, or reorder boards and
columns. Card deletion leaves referenced attachment records intact.

Card writes patch the loaded board through the existing Notes revision check. A save conflict is
retried at most twice after the initial attempt only when the target card and required columns are
unchanged; otherwise the tool returns the latest targeted state for review. Work-log entries append
server-dated UTC Progress, Verification, Blocker, or Outcome sections without replacing earlier
brainstorming or requirements. Successful writes audit identifiers only, never card titles,
Markdown details, tags, or work-log content.

### Note and card attachments

MCP can transfer PNG, JPEG, GIF, WebP, and HTML attachments referenced by an exact MCP-visible
text note or Kanban card. A target is either `{kind: "note", note_id}` or
`{kind: "kanban_card", board_id, card_id}`. Notebook pages, shared/home notes, excluded notes, and
attachments not referenced by that exact target are returned as not found. Hank never fetches an
agent-supplied URL; upload bytes must arrive through the chunk tools.

Uploads use this resumable sequence:

1. Call `start_note_attachment_upload` with filename, declared media type, the target's current
   revision, and optional total size, SHA-256, or `replace_attachment_id`.
2. Send strict-offset chunks with `upload_note_attachment_chunk`. Binary data uses `data_base64`;
   HTML may instead use `text`. A decoded chunk is at most 4 MiB and an original is at most
   100 MiB.
3. Resume after interruption with `get_note_attachment_upload`, using its `next_offset`.
4. Call `finish_note_attachment_upload`. Hank validates the complete size/hash and detected media,
   decodes images with a 40-megapixel ceiling, validates HTML as UTF-8, and atomically updates only
   the selected note or card. A revision conflict leaves the fully staged upload retryable.
5. Use `abort_note_attachment_upload` to discard an unfinished transfer.

New attachments add a canonical `hank-note-attachment://` Markdown reference. Replacement retains
the attachment ID, so every existing reference continues to resolve; replacing an attachment used
in more than one card/location requires `confirm_shared_replacement: true`. Open upload sessions
expire after 24 hours. Limits and expiry are fixed platform contracts and add no deployment
environment variables.

`read_note_attachment` defaults to a native MCP image preview for images and inert UTF-8 source for
HTML. `image_preview` is bounded to a generated PNG no larger than 2048 pixels per side and 5 MiB;
`html_source` returns at most 256 KiB per call; `original_chunk` returns at most 4 MiB of base64 and
supports offsets. HTML is never executed in an MCP result.

In Hank Notes, images remain viewable inline. HTML has separate **Open** and **Download** actions.
Open renders a freshly sanitized copy in a new tab under a CSP sandbox with scripts, forms, frames,
external resources, event handlers, and unsafe URLs removed. Download streams the unchanged
original with attachment disposition. Both routes require the same authenticated note access as
the attachment metadata.

Migration `000032_mcp_note_attachment_transfers` adds resumable upload rows and optional preview
metadata. Apply it before deploying the new cloud image. Hourly maintenance expires sessions,
removes terminal staging bytes, prunes aged rows/orphaned immutable objects, and preserves open
staging plus live originals/previews. Prometheus exposes fixed-cardinality
`hank_mcp_note_attachment_*` session, byte, failure-code, and cleanup metrics; filenames and file
content are never metric labels or log fields.

After deployment, compatible clients receive the changed descriptors through tool-list change
subscriptions or reconnect-and-rediscover behavior. To roll back the application, restore the previous image; migration 32 is additive and
may remain applied. Existing attachment originals remain valid, while unfinished MCP staging data
can be allowed to expire or removed through normal maintenance.

When an active card needs a human handoff, append a Blocker work-log entry that preserves the
decision, approval, or review needed. Move unfinished work to the column with the **Needs Human**
role and completed work awaiting validation to **Review**; if the preferred role is not configured,
use the other configured handoff role. Then continue with the first ordered unblocked card in the
configured intake column instead of waiting for the human response. If neither handoff role exists,
report the board-configuration issue, leave the blocked card in place, skip it, and continue with the
next intake card. Role resolution uses semantic column metadata and never guesses from column titles.

ChatGPT typed conversations and Codex can use these tools. ChatGPT Voice cannot currently invoke
apps, so Voice use remains deferred until OpenAI enables apps in Voice.

### Note and notebook exclusions

The lock icon in Hank Notes marks a note or notebook as excluded from MCP. This is an AI privacy
marker, not encryption or a user-facing security lock. An excluded notebook also excludes every
note currently inside it. Moving an otherwise unlocked note out makes it visible to MCP again.
MCP list, search, tag, fetch, append, update, and delete operations treat excluded records as
nonexistent; normal Hank Notes and HankAI continue to see them.

### Live project context sources

Settings > AI & MCP > MCP Context Sources grants MCP read-only access to a project folder on an
existing File Server share. Choose a display name, share, and share-relative folder, then use
**Test** to verify live access. The source is temporarily unavailable while the agent or share is
offline.

The owning Hank Agent rejects traversal, hidden and dependency/build trees, `.env*`, binaries,
oversized files, and symlink escapes. MCP receives source-relative paths and has no context write,
rename, upload, or delete tool. Search returns at most 50 matches and is bounded to 10,000 files
and 20 MB of inspected text; individual reads are limited to 400 KB.

## Connecting a client

1. Set `HANK_MCP_ENABLED=true` and `HANK_PUBLIC_BASE_URL=https://your-host`, restart
   the cloud service.
2. Sign into the Hank dashboard in your browser.
3. **ChatGPT**: Settings → Apps & Connectors → Advanced settings, enable Developer mode, then
   create a private app/connector pointing at `https://your-host/v1/mcp`. Complete the
   OAuth/consent prompt.
4. **Claude** (web/desktop): Settings → Connectors → add a custom connector with the same URL.

### Testing the private ChatGPT Kanban app

Set the MCP feature flag and public origin in the deployment environment. Interactive Kanban and
all text Kanban tools are included whenever MCP is enabled:

```dotenv
HANK_MCP_ENABLED=true
HANK_PUBLIC_BASE_URL=https://your-host
```

Restart the cloud service after changing them. In MCP Inspector, connect to the deployed HTTPS
`/v1/mcp` endpoint through its OAuth flow and verify:

- `tools/list` contains `open_kanban` and `delete_kanban_card`, including input/output schemas,
  annotations, and the `ui://hank/kanban/v1` app resource metadata;
- `resources/list` advertises that URI with `text/html;profile=mcp-app`;
- `resources/read` returns a self-contained HTML document with an empty connect/resource/frame
  CSP; and
- `open_kanban` returns structured board data for an MCP-visible board.

After changing tool, resource, schema, or widget metadata, bump `mcpServerVersion`, deploy with
graceful shutdown, and confirm the client reconnects and refetches the list. Manual connector
recreation is recovery for a non-compliant host, not the normal rollout path. Then try these typed prompts:

1. “Open my Hank Kanban.”
2. Switch boards and search for a card.
3. Create a card, edit only one field, and add a Verification work log.
4. Move a card with the explicit Move control. On a wide layout, also drag it to an exact position;
   narrow layouts intentionally use the Move control instead of drag-and-drop.
5. Delete a disposable card. The widget first asks locally, then ChatGPT should present its host
   confirmation before the destructive tool runs.

The widget displays attachment filename, MIME type, and size only. It does not fetch, upload, or
delete attachments. ChatGPT Voice cannot invoke the app. This is a private-use integration only;
do not submit it to the public ChatGPT app directory.

Interactive Kanban is a required part of Hank MCP and cannot be disabled separately. To roll back
the MCP surface, disable it in **Settings → AI & MCP**; this disables the endpoint and all
of its tools. If a write fails or reports a revision conflict, the widget preserves the draft and
requires a manual reload/retry; it never silently repeats the write.

## Rollout and rollback

Canary both transports with the production ChatGPT, Claude, and Codex connector versions:

1. Complete a fresh OAuth connection and a refresh-token rotation with each client.
2. Confirm discovery advertises only `2026-07-28`, `hank-mcp` `0.7.0`, zero TTL, and
   `tools.listChanged: true`; then exercise one harmless docs read, profile-note read,
   Kanban read, and configured live-context read.
3. From Codex, complete a headerless initialize negotiation, confirm later requests carry the
   negotiated version, and call `list_docs`. Open `subscriptions/listen` from a 2026 client and
   verify the acknowledgment is the first SSE frame. Restart cloud
   and confirm the client reconnects and rediscovers without connector recreation.
4. Watch `mcp endpoint request` logs by `mcp_protocol`, fixed-cardinality
   `hank_mcp_subscription*` metrics, and reverse-proxy HTTP status counts. Do not add token,
   subscription ID, tool-argument, note/card content, query-result, or file-content fields to
   logs or metrics.

Rollback restores the previous cloud application image and requires no subscription-data
conversion because subscriptions are process-local and ephemeral. OAuth clients, codes, and tokens
are not rewritten. If the image also includes attachment transfer support, its additive migration
32 may remain applied as described above.

For a localhost-only wire-schema and tool-list check using the pinned official conformance runner,
run:

```bash
HANK_MCP_CONFORMANCE=1 go test ./internal/cloud -run TestMCP20260728Conformance -count=1
```

The test harness bypasses OAuth with a fixed in-process identity and is compiled only into tests;
it never opens the production MCP route without bearer authentication. Hank's focused Go tests
cover stateless discovery, subscriptions, no-store policy, Origin checks, and header validation.
The alpha conformance scenarios for some of those areas assume synthetic tools or unadvertised
prompt/resource capabilities that Hank intentionally does not expose.

## Privacy

When used from a cloud assistant, the **content of the docs, the `code-reference/` source
snapshot, and the notes or Kanban cards opened in the widget is sent to that assistant's provider**
(e.g. OpenAI for ChatGPT).
Notes can be personal even though other personal-data surfaces are excluded, and the snapshot
exposes your application source. Grant a read-only set of scopes if you only want to pull context
in, and prefer Claude if you do not want note or source content reaching OpenAI.

## Security checklist (for changes here)

- Every route is authenticated or intentionally public (per the table above).
- Consent writes are CSRF-protected; tokens/codes are hashed at rest and never logged.
- OAuth codes, refresh grants, and bearer tokens are bound to the canonical MCP resource; issuer
  responses prevent authorization-server mix-up. PKCE and client bindings are validated before a
  code is consumed, and refresh-token rotation revokes the old grant and creates the replacement
  atomically.
- Tool calls enforce OAuth scopes and account ownership server-side; device control
  also requires approved fleet access for the account.
- Streamable HTTP validates Origin and modern protocol/routing headers before executing a tool.
  Initialize-era sessions negotiate from request parameters, require the negotiated version only
  after initialization, and bind the session to the exact revalidated OAuth token grant.
- Subscription state contains token and user IDs, never raw bearer tokens; limits and fixed label
  vocabularies prevent unbounded memory and metrics cardinality. Reverse proxies must not buffer SSE.
- Schema changes go through `internal/migrations` (no startup mutations).

## Fleet control

`hank-mcp` version **0.7.0** uses account-wide device access. In
**Agents → Account device access**, choose a duration and obtain administrator
approval once for the account. All its authenticated MCP apps share full access
to all current and future Home devices. No app, device or operation selection is
required. OAuth Notes/document scopes remain separate, and a revoked or expired
MCP token cannot make calls. Disconnecting one app leaves the account approval
and jobs intact; revoke account access to deny every app and request cancellation.
See [fleet setup](fleet-demo.md#gui-setup-and-hosted-mcp) for legacy approvals,
permissions, pairing, tool arguments and revocation. Machine commands run as the
selected agent account, including root for system agents. Files and command output
are untrusted data; scripts and output are not stored in server audits.

Apply migration 52 before deploying this version. Existing scoped approvals are
preserved and need a new review to become account-wide. Its down migration refuses
to remove account grants without an explicit operator resolution.

`GET /v1/me/mcp` remains available to authenticated users while the connector is
disabled so its settings can be managed. `PATCH /v1/me/mcp` with `{"enabled":true}`
or `false` requires Home administrator access and CSRF for cookie authentication.
Disabling closes active subscription streams and denies subsequent MCP/OAuth
requests; it does not revoke existing connections or cancel already running jobs.
Use fleet revocation when cancellation is required.
