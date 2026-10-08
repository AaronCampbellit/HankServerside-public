# Hank assistant M0 contracts and capability inventory

Scope update (2026-09-20): Calendar execution and native-client receipt work
are deferred by user request. Calendar entries below are reserved future
contracts, not requirements for the current implementation. Existing calendar
behavior remains available.


Status: execution-v2 specification with opt-in read admission; writes remain
staged. Implements the contract/inventory deliverables of [the execution plan](hank-assistant-execution.md).
The existing v1 implementation remains authoritative until the relevant gates pass.

## Compatibility and admission

Use `X-Hank-Assistant-Execution: 2` on message submission only after status
advertises `execution_versions: [1, 2]`. Absence means v1. An unsupported explicit
version returns HTTP 409 `execution_version_unavailable`; it never executes v1
instead. New clients must tolerate a missing capability field on older servers
and use the v1 interface. A v2 session is permanently pinned to engine version 2 after adoption.
A new conversation can use v1. The dashboard opt-in requires advertised support.

V2 POST `/v1/home/assistant/sessions/{session_id}/messages` takes the existing
content/attachments/device fields plus `submission_id` (client UUID), returns
202 with `task_id` and authoritative task state, and persists before returning.
Reusing a submission ID with different content is a 409 conflict. The legacy
POST keeps its 201 response and run semantics. GET messages gains an additive
`active_task` (latest task, including terminal state) and `execution_version`;
existing `active_run` describes legacy runs only.

New task routes under `/v1/home/assistant/tasks/{task_id}`:

| Method and suffix | Contract |
| --- | --- |
| GET | Owned task snapshot, version, progress, calls and safe outcomes. |
| GET `events?after_sequence=N&limit=N` | Ordered safe events; maximum 100 per page, next cursor and snapshot-required flag if retention passed the cursor. |
| POST `stop` | Idempotent durable stop request; response distinguishes stopping from stopped. |
| POST `followups` | Submission ID, expected task revision and text; serialize at the next checkpoint. Stale revisions return conflict. |
| POST `approvals/{approval_id}` | Expected revision, action digest and approve/reject; immutable approval ID deduplicates identical decisions; atomic consumption. |
| POST `client-calls/{call_id}/claim` | Authenticated device identity and receipt capability; exclusive expiring claim. |
| POST `client-calls/{call_id}/results` | Claim ID, operation ID, action digest and structured result; same result replay is idempotent, changed result conflicts. |

Every route requires the exact session owner in the authenticated Home, existing
token scope checks, and CSRF for cookie writes. All identifiers are opaque and
untrusted until resolved server-side. WebSocket events only signal a new cursor;
the HTTP snapshot/event store is canonical. Capability names:
`assistant.execution.v2`, `assistant.receipts.v1`, `assistant.staging.v1`.
Clients lacking a capability cannot claim dependent mutations. Default provider
selection and per-user source settings remain in the existing settings model.

## Compatibility matrix

| Participant | M0 behavior | Requirement before v2 use |
| --- | --- | --- |
| Existing dashboard/native client | V1 requests and `active_run` unchanged | Advertised version 2 plus task/event/approval support |
| New dashboard against old server | Missing versions means v1 only | Never send v2 optimistically |
| New dashboard against M0 server | Status advertises `[1]`; explicit v2 receives 409 | M3 opt-in and M4 write gates |
| Existing Hank Agent | Existing envelopes unchanged | Additive receipt capability before replayable new writes |
| Calendar-owning native client | Existing legacy handoffs unchanged | Exact call claim, operation receipt and readback; separate repo dependency |
| Ollama | Existing text/planner adapter; live synthetic baseline uses configured models | M3 tested tool-call transport and per-model support |
| OpenAI API-key provider | Existing text adapter unchanged; no new credentials | M3 tool-call contract tests; live validation only with authorized existing setup |
| Linked ChatGPT | Existing subscription-backed text adapter unchanged | Independently verify endpoint support; never assume API-key parity |
| Installed app | Explicit slash commands and access checks unchanged | Versioned schema and conservative mutation recovery policy |

## Model/tool and durable record contract

The reserved JSON schema is `schemas/assistant/execution-v2.schema.json`.
The schema version and record kind are mandatory. Server-owned identity is not
part of model call arguments. Model calls contain provider call ID, tool name,
tool version, and argument object; each tool's independent strict schema
validates that object before authorization or execution. Calls/results associate
by persisted call ID, never by model text or list position alone.

A model turn contains either calls or final text, finish reason, measured input
and output usage, and private opaque continuation data. Usage unknown is null,
not zero. The execution worker, not the provider, decides task completion.
No model-selected URL, user ID, Home ID, access token, or approval flag may
substitute for server-derived authorization.

Task states and operation outcomes are separate. Atomic transitions check task
revision and worker fencing generation. An action digest uses SHA-256 over the
server's canonical action serialization: tool version, normalized arguments,
exact target IDs, resource preconditions, approval policy, task/call identity.
Never hash arbitrary provider JSON as an authorization shortcut. Exact canonical
serialization and vectors must land with M2 before approvals become executable.

Error codes: `invalid_arguments`, `permission_denied`, `not_found`,
`target_ambiguous`, `revision_conflict`, `agent_offline`, `client_unavailable`,
`approval_expired`, `approval_required`, `rate_limited`, `timeout`,
`outcome_unknown`, `budget_exhausted`, `capability_unavailable`, `internal_error`.
User-facing messages are bounded and do not echo secrets or raw provider errors.
Private source evidence is returned only to the owning user/model context.

## Complete current intent mapping

Current names come from `assistantIntentKind` and `assistantToolRegistry`.
The adapters below are targets for M1, not claims that v2 tools already exist.

| Current intent(s) | V2 disposition | Permission/execution owner |
| --- | --- | --- |
| `general`, `read_only.synthesis` | Model loop using evidence tools; no catch-all action executor | Enabled sources plus per-resource authorization |
| `notes.list`, `notes.search`, `notes.summarize` | `notes.search`, `notes.get`; model summarizes returned evidence | Notes source setting, feature permission, personal ownership/Home visibility |
| `notes.append`, `notes.create` | `notes.append`, `notes.create` prepared writes | Same scope plus revision and policy; cloud note services |
| `files.search`, `files.list_folder` | `files.search`, `files.list`, `files.stat` | Files setting/feature, exact source and target agent; existing files service |
| `files.create_folder` | `files.create_folder` prepared write | Same scope, path/symlink containment, approval; agent files adapter |
| `calendar.search` | `calendar.search`, `calendar.get` | User-owned snapshots, source setting and authorized device |
| `calendar.create_event`, `calendar.update_event`, `calendar.delete_event` | Same names, schema-based prepared client calls | Exact device/calendar/event, expiry, approval, client receipt |
| `homeassistant.query` | `homeassistant.search`, `homeassistant.get` | HA setting/feature; primary-agent HA adapter |
| `homeassistant.control` | `homeassistant.call_service` prepared allowlisted write | Same scope, exact entity/service approval, readback |
| `project_docs` | `evidence.search`, `evidence.read` with project-doc source | Project-doc setting, approved document root |
| `assistant.memory_search` | `evidence.search`, `evidence.read` with conversation source | Current user only; conversations setting |
| `assistant.status` | `assistant.status` read | Owning user settings and source statistics |
| `agent.status` | `machines.list`, `machines.status` reads | Home membership, exact agent; machine administration restrictions remain |
| `sync.status` | `notes.sync_status` read | Notes access, Home sync state; redact downstream errors |
| `backup.status` | `storage.status` read | Home admin; storage service |
| `media.search`, `media.selection` | `media.search`, `media.prepare_download`, `media.download` | Files permission and media policy; primary-agent media adapter |
| `hermes.chat`, `ydownload.command`, `gramaton.command`, `installed_app.command` | Explicit installed app/command adapter, versioned schema | Installed/enabled app, command-specific user access, sandbox capability |
| `ha.command`, `files.command`, `notes.command`, `append.command`, `calendar.command`, `docs.command`, `status.command` | Preserve `/ha`, `/files`, `/notes`, `/append`, `/calendar`, `/docs`, `/status` as explicit selectors into the same tools | Identical tool authorization; slash syntax grants no extra access |

Special flows outside the registry also require migration:

- Attachment planning/commit (`note_attachment`, `smb`) uses staged handles,
  per-file operation keys and existing note/file transfer services. Browser
  uploads currently cannot recover bytes after refresh.
- `planCalendarTool`, previous-card/calendar/media selection and attachment
  clarification become structured checkpoint references, not phrase overrides.
- Media cancel planning uses exact job ownership and approved cancellation.
- Newly exposed Notes updates, file move/copy/rename/delete and machine writes
  remain unavailable until M4 receipts, approvals and verification exist.
- Arbitrary shell, unsupported HA services, remote desktop input, installation,
  and unreviewed app mutations remain explicitly unavailable to model selection.

Existing read/write handlers to extract or wrap: `assistant.go` (notes, HA,
files, attachments, calendar finalization, app invocation),
`assistant_intents.go` (note creation/summary, calendar mutation planning,
folder listing and card follow-ups), `assistant_media.go` and
`assistant_media_commands.go`, `assistant_status_intents.go`,
`assistant_indexing.go`/`assistant_index_queue.go`, `internal/agent/files`,
`internal/agent/notes`, `internal/agent/host.go`, and existing routed command
authorization in `agent_requests.go`/app invocation. MCP/HTTP authorization
must remain equivalent after shared-service extraction.

## Removal checklist for M5

- [ ] Replace every mapped registry intent and all out-of-registry special flows.
- [ ] Remove `Match`, `resolveAssistantTool`, `classifyAssistantIntent`, the
  fallback one-shot planner, slot extractors and previous-card phrase matching
  only after their callers move to the loop. Preserve slash parsing.
- [ ] Migrate planner-enabled/model/prompt settings and dashboard controls with
  explicit compatibility for older clients; do not silently change saved prefs.
- [ ] Preserve v1 pending-run readers until drained; retire writers only after
  supported-client cutover and the legacy-run recovery audit.
- [ ] Convert phrase-selection assertions in `assistant_workflow_test.go` and
  `tools/hankaieval` to outcome assertions while keeping original prompts.
- [ ] Update provider tests, API/UI fixtures, source settings docs, deployment
  configuration, schema/contract docs, evaluation instructions and trace consumers.
- [ ] Search the entire repository for removed symbols/settings/tool names and
  document every deliberately retained compatibility reference.

## M0 evidence

Evidence is recorded in the parent plan after validation. Live baseline data is
synthetic and uses an isolated PostgreSQL database and Hank's existing Ollama
models. This baseline must distinguish routing assertions from task completion;
the current harness does not prove end-to-end execution of confirmed writes.
