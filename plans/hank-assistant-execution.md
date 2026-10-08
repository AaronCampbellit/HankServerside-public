# Hank assistant execution plan

Status: M0–M5 implementation, local validation, and demo deployment complete.
User acceptance testing is next. M6 follows interactive testing.

Scope update (2026-09-20): Calendar upgrades are deferred at the user’s request.
Existing calendar behavior and read adapters remain; calendar execution, client
receipt contracts, and calendar acceptance scenarios are outside this plan’s
remaining completion gates. Other milestones continue in order using Hank’s
existing setup. Historical calendar references below do not expand this scope.
Investigated 2026-09-19 against `9a3cd73` on `main`.

## Outcome and ownership

Replace phrase-driven chat orchestration with a bounded model/tool execution
loop. Hank chooses tools, observes structured results, requests clarification
or approval, and continues until the user's task is complete or explicitly
blocked. PostgreSQL owns durable execution state. HankServerside owns tools,
authorization, approvals, context, scheduling, and the dashboard. Hank Agents
execute authorized local operations over their existing outbound connections.

Keep explicit slash commands, including installed app commands. They select an
explicit capability but use the same execution, authorization, and result
machinery. Do not make an optional app the core assistant engine. Keep JSON
over HTTPS/WebSocket, one Home per deployment, primary-agent default routing,
and explicitly targeted worker operations.

This plan covers all ten requested updates. It does not authorize changes in
other client repositories, production operations, or publication. Calendar
device execution requires a separately coordinated client implementation;
server contracts and simulated-client tests belong here.

## Evidence from current code

| Area | Existing implementation | Gap to address |
| --- | --- | --- |
| Routing | `internal/cloud/assistant_tools.go`: `assistantTool` has `Match` and `Execute`; ordered matchers choose an intent. `generateAssistantResponseForSession` in `assistant.go` invokes the local planner only for general fallback. | The planner chooses one existing intent/query, not successive schema-validated calls based on tool results. |
| Providers | `internal/cloud/assistant_provider.go`: messages contain role/content; chat adapters return text. | Preserve typed model tool calls, call IDs, usage, stop reasons, and tool-result turns. Validate capability per provider/model. |
| Persistence | `internal/store/assistant.go` stores sessions, messages, runs, and a pending-action JSON blob. `GetPendingAssistantRun` finds the latest waiting run; migration 39 indexes unfinished runs. | No per-step execution journal, conditional claim/lease, operation receipt, or general recovery dispatcher in these paths. |
| Run lifecycle | `assistant.go` processes message POSTs using the request context. Current schema restricts runs to completed, waiting-client-tool, and waiting-confirmation states. | No durable queued/running/failed/cancelled lifecycle; disconnect and crash recovery need an independent worker. |
| Confirmations | `handleAssistantConfirm`, `executeConfirmedAssistantAction`, and pending action summaries already prepare and execute exact actions. Client results finalize a run. | Approval and results must advance a step and resume the original task. Current store updates are unconditional; concurrent approval/result handling needs atomic transitions. |
| Context | `resolvePreviousCardFollowup`, media selection, calendar follow-up tests, and private conversation indexing exist. | Follow-ups depend on specialized parsing rather than a shared durable set of selected objects and result references. |
| Retrieval | `assistant_indexing.go`, `assistant_index_queue.go`, `internal/store/assistant_index.go`, and `filterAssistantContexts` supply indexed, filtered context. | Expose search/read/refine as tools; reauthorize original resources before reading, and preserve evidence references. |
| Dashboard | `HankAIPage.tsx`, `api/hankAI.ts`, and `hankAIClientTools.ts` restore `active_run`, render approvals/cards, and commit browser attachments. | Add replayable activity, server-side stop, follow-up delivery, and complete outcome states. Attachment bytes currently live in the browser's submitted-file map. |
| Local execution | `internal/agent/client.go`, `host.go`, file/notes adapters, and protocol command envelopes supply local capabilities and request correlation. | A request ID alone is not a durable deduplication receipt. Mutation adapters need recovery contracts before automatic replay. |
| Evaluation | `tools/hankaieval/main.go` already provisions fixtures and records latency, intent/tool selection, cards, and confirmation expectations. | Add multi-turn drivers, observable final-state assertions, failure injection, recovery checks, usage/cost, and repeated model runs. |
| Background work | `internal/store/assistant_index_jobs.go` has transactional job claiming; notification service has durable source events and coalescing. | Reuse patterns, not the index queue's unconditional requeue semantics, for side-effecting tasks. Add schedules only after interactive recovery passes. |

The August 25 commit `41f686c` added substantial workflow support; this design
extends that foundation rather than assuming nothing exists. Historical intent
plans are superseded and are not acceptance criteria for this work.

Two additional findings affect implementation:

- Trace calls currently supply `prompt`, `query`, and `raw_answer`; the trace
  sanitizer primarily redacts credential-like keys. Replace content-bearing
  diagnostic fields with allowlisted metadata as part of the first milestone.
  Private execution records are separate from operator logs and user activity.
- `loadAssistantProjectDocs` recursively reads Markdown under `docs/` without
  a planning-directory exclusion. This plan lives under top-level `plans/`,
  outside that traversal. Do not place proposed behavior under indexed current
  docs. A catalog link describes it as proposed, without copying its contents.

## Execution contract

### Model loop

One worker claims a runnable task, loads its checkpoint, assembles permitted
context and available tool definitions, and requests the next model turn.
Persist the returned call before execution. Validate the tool name, schema,
authorization, resource references, and budget. Execute a read or prepare a
write; suspend for clarification, approval, or a device as required. Persist
the result and verification evidence, then give that result back to the model.
Only finish when the requested outcome is supported by recorded results.

Keep the model transport behind a provider-neutral Go interface. A model turn
returns text, typed calls, usage, and a finish reason. Preserve provider-required
opaque continuation items privately when necessary; never surface hidden
reasoning in logs or activity. Do not adopt a second hosted task store or move
platform execution into a provider SDK just to implement the loop.

Start with sequential calls, including when a model emits multiple calls.
Persist their order; never speculatively execute writes. Later parallel reads
must prove independence and preserve deterministic result association.
Unrecognized/malformed calls receive bounded structured errors; after two
repair attempts, stop with an actionable failure. Unsupported tool-capable
model combinations report capability unavailable, with an explicitly selectable
answer-only mode. They must not silently fall back to keyword actions.

Proposed initial per-task limits: 12 model turns, 24 tool calls, 3 attempts per
retryable call, 5 minutes active execution, and 32,000 aggregate model tokens.
Waiting for approval/device does not consume active wall time, but has an
explicit expiry. Store limits and consumption durably across restarts. Tune
these defaults from evaluations before release. Monetary limits require a
configured price table; unknown price is unknown, not zero cost.

### Tools and structured results

Every definition has a stable versioned name, description, strict input and
output JSON schemas, size limits, capability requirements, permission policy,
read/write classification, approval policy, timeout, retry classification,
idempotency strategy, and verification strategy. Reject unknown fields and
out-of-range values. The model supplies resource arguments, never the trusted
user, Home, role, token scope, or effective authorization context.

Use shared domain services underneath assistant, HTTP, and MCP adapters where
practical. Do not call public HTTP handlers internally or copy their permission
checks into an unmaintainable second implementation.

| Family | Initial tool surface | Exact identifiers and safeguards |
| --- | --- | --- |
| Notes | search, get, create, append/update, attach | Note ID plus personal/Home scope; revision precondition; preserve existing note formatting. |
| Files | search, list, stat, bounded read, create folder, commit staged upload; later move/copy/rename/delete | Agent ID, source ID, contained path, version/checksum where available; symlink resolution at operation time; quotas and transfer IDs. |
| Home Assistant | search entities, get state, call an allowlisted service | Entity ID, service/domain, validated data, primary-agent identity; explicit approval and state verification. |
| Calendar | search snapshots, get event, prepare/create/update/delete through capable device | Device/calendar/event IDs, revision/freshness, IANA timezone and explicit timestamps; client receipt and readback. |
| Machines | list agents, status, bounded service/process inspection; selected service lifecycle operations after approval | Explicit agent ID, admin scope where required, supported capability, exact service/process identity. No unrestricted shell exposed to the model in this rollout. |
| Evidence | search sources, read result, search own conversation memory | Authorized source type/ID, result-set reference, pagination, freshness, canonical link; no implicit arbitrary file-content crawling. |
| Apps/slash commands | explicit installed command dispatch | Installed app/command version, schema, user access, target agent; opaque mutations require conservative recovery policy. |

Machine writes start with an allowlist of individually reviewed service
operations. Package changes, process termination, reboot, and broader RMM
mutations need their own schema, approval, receipt, and verification coverage
before becoming tools; existing admin HTTP access is not automatic AI access.

Each result carries `call_id`, `operation_id` when applicable, `outcome`, typed
`data`, exact `resource_refs`, `evidence_refs`, and verification status. Errors
carry a stable code, safe explanation, retryability, and next required action.
Examples: `permission_denied`, `target_ambiguous`, `revision_conflict`,
`agent_offline`, `client_unavailable`, `approval_expired`, `outcome_unknown`.
Model-visible content and errors are bounded and treated as untrusted data.
Fetched notes, documents, filenames, and app results cannot grant permissions
or override the user's task, approval policy, or instruction hierarchy.

### Durable state and recovery

Keep sessions/messages as the user-visible conversation. Add a task layer with
an additive reference on existing runs; do not overload display messages with
tool protocol records. Proposed new records:

| Record | Durable contents and integrity |
| --- | --- |
| Tasks | Home/user/session, original request, engine/schema version, status, revision, budget, cancellation flag, timestamps, lease owner/expiry/fencing generation. Unique user submission key prevents double-submit. |
| Steps/tool calls | Task sequence, provider call ID, tool/schema version, validated arguments, target references, status, attempt count, operation key, bounded result and evidence. Unique task/sequence and call identity. |
| Approvals | Step, canonical action digest, target/revision, approving user, expiry, decision, consumption state. Atomic single consumption; approval cannot authorize changed arguments. |
| Operation receipts | Scoped idempotency key, action digest, dispatch state, destination receipt, outcome, verification timestamps. Key reuse with a different digest is rejected. |
| Task events | Monotonic per-task sequence, safe event type and display data. Transactional publication with state changes; reconnect uses a cursor. |
| Context checkpoints | Versioned summary, goal/constraints, ordered result sets, selected object references, unresolved questions, verified facts and provenance. |
| Schedules/occurrences | Added in final milestone; unique schedule/revision/occurrence identity and task reference. |

Separate task states (`queued`, `running`, `waiting_approval`, `waiting_input`,
`waiting_client`, `waiting_retry`, `reconciling`, `completed`, `failed`,
`cancelled`) from operation outcomes (`not_started`, `accepted`, `confirmed`,
`failed`, `unknown`). An unresolved write remains reconciling or is handed back
with an explicit unknown outcome; it must not become a successful task merely
because a provider produced final text.

Use short PostgreSQL transactions for conditional transitions, step/event
writes, approval consumption, and worker claims. Never hold a transaction
across model or agent I/O. Lease expiry allows recovery, but stale workers must
fail their fencing checks. Destination deduplication remains necessary even
with leases. Default to one active task per session; server-side admission and
ordered follow-up events prevent two tabs from producing conflicting tasks.

Recovery rules:

1. Before dispatch, commit the validated step and operation key.
2. For PostgreSQL-local writes, commit the domain mutation and operation receipt
   atomically where possible; deterministic created IDs alone are insufficient
   for append/update operations.
3. Extend agent commands additively with operation identity and capability
   negotiation. Store a local durable receipt journal using existing local
   persistence conventions. Persist receipt identity before execution and
   result after execution; enforce authenticated Home/agent binding.
4. A local receipt journal cannot close the crash window around an external
   SMB/Home Assistant side effect. Reconcile using transfer IDs, exact target
   versions, checksums, or downstream receipts. If attribution is uncertain,
   report unknown and require resolution; do not blindly redispatch.
5. Clients claim an exact pending call with a bounded lease. Submit results by
   task/step/call/device/operation identity; duplicate identical receipts return
   the canonical result, conflicting or late results cannot advance another
   step. Old clients without receipts cannot execute replayable new writes.
6. Restart recovery reclaims safe reads and undispatched writes; dispatched
   writes enter reconciliation first. Test crashes before dispatch, after the
   side effect, before receipt commit, and after commit/before response.

Durability must include attachment bytes. Introduce private, quota-limited,
expiring staging through existing storage/transfer services before advertising
resumable uploads. Store staged object handles, byte counts, checksum, expiry,
and per-file commit receipts. Resume partial batches without creating “copy”
duplicates. If bytes were never durably staged, pause for reattachment and
verify identity; metadata alone cannot recover the upload.

### Approval, verification, and cancellation

Prepare an immutable action with exact before/after values, resource IDs,
target device, expected revision, effect/risk, and expiry. The UI approval
submits the action ID and digest; the server atomically records the decision.
Reauthorize immediately before execution, including permissions revoked while
waiting. Changed arguments, a new target, or stale resource versions invalidate
approval and produce a fresh proposal. Model text such as “approved” is never
an approval credential. Preserve existing destructive/admin action-token and
audit requirements without trying to persist raw action tokens.

On approval, execute, verify, append a tool result, and resume the original
goal. On rejection, do not retry the rejected action via another tool; let the
task report the declined step or ask about a materially different approach.
Initial mutation policy preserves existing confirmations and requires explicit
approval for newly exposed write tools. Any later low-risk auto-run policy is
a separately specified product change, not a model decision.

Verification uses note revision/content readback, file stat/checksum and
transfer receipts, Home Assistant observed state, calendar device readback,
or machine service/status queries. Record expected versus observed outcome,
observation time, and limits of the evidence. For inherently non-observable
effects such as a button/script invocation, report command accepted, without
claiming a physical effect. Readback proves observed state, not necessarily
that this operation caused it.

Retry reads and provably idempotent operations with bounded backoff/jitter.
Do not retry authorization/schema failures or ambiguous writes automatically.
Keep the same operation key across attempts; changed intent creates a new
action and approval. Cancellation is durable: block new dispatch, cancel
provider work where possible, and reconcile already dispatched operations.
“Stopped” never means rollback; show changes that already occurred. A follow-up
that changes an executing action applies after a safe checkpoint, not by
mutating an in-flight call.

### Context, retrieval, and user controls

Store ordered result sets with stable IDs so “the second folder” references
the displayed order, even after later searches. Keep selected folder/event,
timezone, task goal, constraints, approvals, and unresolved actions outside
lossy summaries. Summary generation is versioned and atomic; it cannot erase
operation history or convert a pending action into a completed fact. Recheck
resource existence, revisions, and access before using saved references.
“Continue” resumes the named/latest unambiguous eligible task; otherwise ask.

Search returns bounded snippets, IDs, canonical links, source versions,
freshness and cursors. The model can read a selected result, refine a query,
and gather additional evidence within budget. Reuse vector/lexical retrieval
and current source settings. Source permissions also apply to selected context,
summaries, memory, and citations after revocation. Cite actual evidence IDs;
the server resolves display links rather than trusting invented model URLs.
File-content reads must be explicit, bounded, and use the owning authorized
file API; the existing metadata index is not a content grant.

Add task detail and cursor-based event retrieval, stop, follow-up, approval,
and client-result operations to the assistant API. Proposed v2 execution is
capability-negotiated on the current session/message surface; existing clients
retain v1 semantics during rollout. Do not send asynchronous states to clients
that assume the POST response is final. Document final route/schema choices
in `docs/api.md` and versioned schemas during milestone 0.

The dashboard renders safe activity labels (“Searching notes”, “Waiting for
your approval”, “Checking the result”), per-step outcomes, source cards,
pending device needs, and stop/follow-up controls. Use existing authenticated
WebSocket infrastructure for notification of new events, with cursor-based
HTTPS catch-up as authority. Never put bearer tokens in event URLs. Refresh,
two tabs, dropped events, and server restart must converge on the same state.
Keep the existing online-only PWA boundary. Make activity/status accessible,
keyboard-operable, and screen-reader friendly without announcing every token.

### Scheduling after interactive reliability

Schedules enqueue ordinary tasks in the same engine. Store owner/Home,
versioned prompt/template, explicit resource/tool grants, timezone, recurrence,
next occurrence, per-run and daily budgets, enabled state, concurrency policy,
and notification preferences. Effective authority is the intersection of the
saved grant and the owner's current permissions/settings; deletion, revoked
membership, disabled sources, and expired provider credentials block execution.

Start with read-only digests/checks and notification follow-ups. Scheduled
writes still pause for normal approval; schedule creation is not blanket
approval. Use unique occurrence keys, one active occurrence per schedule, a
documented skip/coalesce misfire policy, and tested daylight-saving behavior.
Default to coalescing missed read-only checks into one current run rather than
bursting through a backlog. Bound waiting approvals and disable/review repeated
failures. Schedule edits affect future occurrences; show an explicit choice
when stopping an already running occurrence.

Expose preview, create/edit, pause/resume/delete, next run, run history, and
manual run in the dashboard. Notification policies include failures, approvals,
changes-only or every digest, quiet hours, and deduplication by occurrence.
Send private detail only through authorized Hank views; push text is generic.
Reuse the notification outbox/coalescing path; the engine must not send duplicate
notifications when recovering a completed occurrence.

## Implementation milestones and exit gates

Milestone checkboxes track validated completion. Each includes code, tests, API/schema updates,
and owning documentation. A passing slice is not completion of the full plan.

### M0 — Contracts, baseline, and privacy prerequisites

- [x] Inventory every current intent, slash command, service adapter, write,
  permission check, and client capability. Map each to a replacement tool or
  explicit unsupported result; retain a removal checklist for old matchers.
- [x] Define versioned model-turn, tool, task, approval, event, receipt, and
  client capability contracts; settle v1/v2 request negotiation and UI rollout.
- [x] Establish synthetic fixtures and record current completion/latency
  baseline with `tools/hankaieval`; preserve current regression phrases.
- [x] Replace raw prompt/query/planner trace fields with safe metadata; test
  that private data and secrets do not enter logs/events/evaluation artifacts.

Exit: reviewed schemas, compatibility matrix, baseline report, and privacy
tests. No new model-directed writes enabled.

### M1 — Typed capability adapters and deterministic evaluation driver

- [x] Build the schema registry and common result/error envelope; extract
  reusable authorized services from existing assistant handlers as needed.
- [x] Implement read adapters for Notes/files/Home Assistant/calendar/machines
  and evidence; implement prepared write adapters behind disabled execution.
- [x] Route slash commands through the registry and preserve app access rules.
- [x] Add a scripted model, fake agent transport, and client snapshot fixtures
  to drive read/prepare tasks deterministically with state assertions. Durable
  recovery is M2; the simulated client write/receipt transport is M4.

Exit: schema, scope, ID, path-containment, malformed input, unavailable target,
and denied-operation tests pass without a live model.

### M2 — Durable task engine and recovery

- [x] Add versioned migrations, store APIs, constraints/indexes, task worker,
  claims/fencing, operation journal, checkpoints, budgets, and event outbox.
- [x] Add atomic approval transitions, deduplicated submission/results, stop,
  follow-up queue, and authenticated task/event APIs.
- [x] Implement local DB write receipts and additive agent/client operation
  contracts, reconciliation strategies, and durable attachment staging.
- [x] Exercise the scripted loop through process restarts and injected crashes.

Exit: duplicate approvals, concurrent tabs/workers, lost responses, agent
disconnects, expired leases, partial uploads, and restart recovery produce no
duplicate writes. Uncertain outcomes remain explicit. Use real PostgreSQL.

### M3 — Model-directed reads, context, and active retrieval

- [x] Extend provider adapters to return tool calls/results/usage with bounded
  validation and provider-specific continuation handling. Validate current
  provider documentation and actual configured endpoints during implementation;
  linked ChatGPT support must not be inferred from API-key support.
- [x] Run the iterative loop with reads only, structured references, resumable
  context checkpoints, summaries, citations, and budget enforcement.
- [x] Add dashboard activity/catch-up, stop/follow-up, pending and failure states.
- [x] Evaluate paraphrases, cross-source investigation, ambiguous references,
  prompt injection in retrieved content, and compaction/restart follow-ups.

Exit: supported provider/model combinations complete read tasks against the
same fixtures; unsupported models fail clearly. No phrase-based main router
for opted-in v2 sessions.

### M4 — Approved writes, verification, and continued conversations

- [x] Enable Notes and folder/upload writes first, then allowlisted Home
  Assistant and machine writes, each with receipts and verification coverage.
- [x] Wire exact proposals, edit/reject/approve, stale-target detection, and
  continuation of the original task after success or actionable tool failure.
- Calendar execution and native-client contracts are deferred by user request.
- [x] Test “find note → propose append → approve → update → read back →
  report”, “use the second folder”, and stop during an in-flight action.

Exit: observed outcomes match user intent; no success claim without qualifying
evidence, no approval bypass, and no duplicate effects under crash injection.
Unavailable device capabilities remain durable, visible pending work.

### M5 — Evaluation release gate and routing cutover

Local implementation and validation are complete. The actual canary rollout
and deployed health checks remain deployment operations.

- [x] Extend `tools/hankaieval` with multi-turn/task-state drivers, exact
  approvals, external-state assertions and failure classification. Pair live
  synthetic Notes scenarios with deterministic server/agent failure and
  recovery simulations for other capabilities.
- [x] Run the existing configured Ollama model repeatedly with paraphrases.
  Record completed outcomes, incorrect effects, latency, tool/turn counts,
  tokens and cost provenance. Keep OpenAI mock coverage distinct from live
  model validation; no unconfigured provider is claimed to pass.
- [x] Prepare the single-Home canary switch and default new supported dashboard
  conversations to the loop. Disabling admission preserves recovery. Actual
  canary activation awaits deployment; no shadow mutations are performed.
- [x] Route new supported dashboard conversations through the agent loop and
  evaluate paraphrases by final outcomes. Preserve explicit commands. Retain
  v1 phrase/planner compatibility for existing conversations, external clients,
  and deferred calendar execution; deleting those paths requires coordinated
  client migration and is not part of this non-calendar deployment gate.

Proposed release thresholds: 100% deterministic safety/recovery cases; zero
unauthorized, unapproved, wrong-target, duplicate writes or false-success
claims in release evaluations; at least 95% end-to-end completion on supported,
solvable live cases over three runs per supported model. Publish sample counts,
blocked cases, and confidence limits; zero observed errors is not proof of
zero risk. Fix active-latency and token/cost budgets after M0 measurement and
before cutover; compare equivalent tasks and exclude human waiting separately.
Do not average an unsafe provider into a passing global score.

Exit: these gates pass, latency/cost budgets are recorded and met, supported
clients have compatibility evidence, and rollback/recovery procedures work.

### M6 — Scheduled tasks

- [ ] Add schedule/occurrence migrations and dispatcher on the same engine.
- [ ] Implement scoped grants, expiry/revocation, recurrence/timezones,
  overlap/misfire policy, run/daily budgets, history, and notification rules.
- [ ] Add dashboard schedule management and notification preferences.
- [ ] Test restart at the due boundary, duplicate dispatch, DST, downtime,
  owner removal, source revocation, unavailable devices/providers, exhausted
  budgets, approval expiry, and duplicate notification delivery.

Exit: recurring digests/checks complete with exactly one recorded occurrence,
no expanded permissions or duplicate side effects, and inspectable run history.
Interactive M5 gates are a prerequisite, not a parallel release assumption.

## Database, compatibility, and rollback requirements

Schema changes belong only in `internal/migrations/sql`; allocate versions at
implementation time. Define non-null ownership/status/version fields, explicit
defaults, state constraints, foreign keys, scoped unique idempotency keys, and
indexes for runnable tasks, pending calls, event cursors, and due schedules.
Use JSONB only for versioned, application-validated variable payloads; keep
ownership and important integrity fields relational. Update domain/store
models, migration status, and drift snapshots together.

Add schema first, deploy compatible readers, then enable v2 writes. Existing
completed runs remain readable without invented step histories. Existing
pending runs stay on a marked legacy path until completed, safely cancelled,
or explicitly reconciled; never guess whether an old action ran. Backfill only
deterministic metadata and report unresolved cases. Enforce new constraints
after backfill/validation, including state/flag consistency.

Once v2 task writes exist, rolling back to binaries that cannot understand them
is unsafe. Rollback disables new admission/schedules and retains a compatible
recovery worker; do not downgrade/drop populated tables or blindly replay
pending legacy actions. Back up before migration and test restore against
documented release boundaries. Receipts must outlive retry/replay windows;
retain non-replay tombstones when deleting private content. Define quotas,
retention, task/session deletion and attachment cleanup without deleting
in-flight recovery evidence. Use existing encryption/hashing helpers for
secrets; never store bearer/session tokens in task or schedule records.

## Validation and documentation

Security acceptance covers authentication and cookie CSRF, exact user/Home/
role/agent/token scopes, permission revocation during waits, untrusted tool
content, arbitrary ID injection, stale approvals, path/symlink containment,
late/forged client results, attachment access, and admin machine boundaries.
Audit actor/action/outcome and correlation IDs without private content. Durable
tool data is user-private application state, not a general admin trace feed.

Required scenario suite includes paraphrases; multi-step read/write requests;
ambiguous names and ordinals; timezone changes; deleted or moved resources;
permissions revoked after retrieval/approval; failed tools; unsupported models;
malformed calls; repeated calls; budget exhaustion; user rejection; lost event
streams; two tabs; missing upload bytes; server/agent/client restart; and writes
whose outcome cannot be established. Use synthetic isolated resources, never
production personal data or live household physical actions for routine evals.

For implementation, run targeted checks while developing, then the repository
gates: `make fmt`, `go test ./...`, `make build`; dashboard changes also require
`make frontend-test`, `make frontend-check`, and `make frontend-build`.
Database milestones require real PostgreSQL tests with
`HANK_TEST_DATABASE_URL`, `make migrate-status`, and `make schema-drift-check`.
Skipped DB tests are not database validation. Deployment changes require
`scripts/doctor.sh` and the applicable `RELEASE.md` gate in an authorized test
environment. Live model evaluations are a separate gate from deterministic CI.

Update owning docs as behavior lands: `docs/hankai.md`, `docs/api.md`,
`docs/architecture.md`, `docs/security.md`, `docs/deployment.md`, `docs/pwa.md`,
`docs/demo-validation.md`, `docs/app-platform.md` when app contracts change,
and `RELEASE.md` when release gates change. Publish matching protocol/schema
contracts for external clients. Remove this plan after all milestones land
and durable guidance is folded into those documents; Git remains the archive.

## Planning validation

This change is documentation-only. Investigation used source, migrations,
tests, and Git history; it did not execute providers, agents, or production
services. Check referenced paths, catalog links, discovery placement, canonical
terminology, Makefile commands, and `git diff --check` before handing off.
Runtime tests, builds, database checks, live evaluations, and deployment gates
are deferred to implementation; no runtime security or migration change is
made by this plan.

## Implementation evidence — M0

Contracts and the complete intent/disposition inventory are in
[hank-assistant-m0-contracts.md](hank-assistant-m0-contracts.md); the reserved
schema is `schemas/assistant/execution-v2.schema.json`. M0 adds version-1
advertising and rejects unsupported explicit versions before any message is
stored. No v2 engine is enabled.

Trace events now use an allowlist of static summaries and typed metadata, with
private fields removed at assistant call sites. Raw provider/indexing warning
errors and private response/query fields in evaluation artifacts are excluded.
Focused tests exercise private-data canaries, malformed metadata, reserved
schema validation, unsupported-version rejection and foreign-session hiding.

Live baseline on 2026-09-19 used a disposable local PostgreSQL 18 database,
a temporary local HTTP API, synthetic fixtures, and the existing configured
Ollama `qwen3:14b` / `nomic-embed-text:latest` models. No deployed Hank data,
credentials, configuration, or services were changed.

- Initial run: 13/15 assertions passed; both document requests reached the
  45-second HTTP deadline. Preserve this cold-model latency limitation.
- Warm run: 15/15 assertions passed; 11 completed responses and 3 waiting
  confirmations (the provider-status case has no run). Request latency p50 was
  379 ms and p95 was 11,979 ms. Reports remain under ignored
  `data/hankai-evals/`; they contain fixture identifiers and safe outcomes only.
- The suite records routing/confirmation assertions and response states;
  verified task-completion rate and monetary cost are explicitly unknown.
  Files/physical device execution are not included in this live baseline.
- `make tidy`, `make fmt`, `make build`, final `go build ./...`, focused
  assistant/schema/evaluation tests, and `git diff --check` passed.
- The broad PostgreSQL-backed run passed all non-cloud packages. Its first
  cloud compile raced the frontend embed rebuild. The subsequent full cloud
  run completed with one existing storage-realtime setup timeout; that exact
  test then passed in isolation (1.582 seconds). All assistant tests passed.
  This is not a claim of a single clean full-suite invocation. No runtime
  storage change or test expectation was weakened to suppress the timeout.
- Live tests are opt-in and ran separately as recorded above. Deployment,
  native-client/device acceptance and migration/drift commands are not M0
  validation: M0 does not deploy, change native clients, or migrate a schema.

No schema migration is introduced by M0. This evidence does not mark later
milestones or production rollout complete.


## Implementation evidence — M1

The staged `internal/assistant` registry validates closed input/output schemas,
rejects duplicate JSON keys and excessive nesting, bounds time/output, and
copies definitions so callers cannot mutate registered validation. Definitions
and model turns use the reserved v2 contract. The synthetic evaluation driver
passes structured observations back to a scripted model, limits turns/calls,
rejects repeated call IDs, and stops at proposals. It is not a production task
worker.

Initial adapters cover Notes search/get/create/append; file list/search/stat/
bounded text reads/folder proposals; Home Assistant search/get/service proposals;
calendar snapshot search/get/create/update/delete proposals; machine inventory/status;
project documentation and owned conversation evidence; and explicit installed
app proposals. Membership, settings, source access, app access, exact IDs,
revisions, and advertised capabilities are rechecked per invocation. No write
adapter dispatches a mutation. Attachment/staged-upload execution and native
calendar dispatch remain M4 work, where operation receipts and client target
contracts can be enforced.

Explicit slash directives select registry tools without natural-language phrase
matching. Installed app directives bind app/command/version; execution rechecks
current access. These directives remain staged for v2 activation in M3/M5;
existing v1 slash behavior stays active in the meantime.

Tests include real PostgreSQL scope/revocation checks, schema conformance,
scripted multi-step decisions, connection-bound fake agent replies, source/path
filtering, second-result folder selection, exact calendar snapshots, stale
revisions, disabled apps, and proof that preparation does not mutate Notes or
send a folder/Home Assistant write. Project-document reads now use contained
filesystem handles and bounded reads, with an escaping-symlink test.

Validation: `make fmt`, `make build` (including the frontend build), focused
registry/model/schema tests, and the final PostgreSQL-backed adapter tests pass.
The broad `go test -p 2 -parallel 4 ./...` run passed all assistant tests and
non-cloud packages but failed two existing collaboration tests at their
15-second deadlines during database checkpoint load. Both exact failures and
the final M1 tests then passed together with `-parallel 1` (18.119 seconds).
One overlapping focused run was cancelled to reduce database contention; its
coverage was rerun in that passing final batch. No test expectations or timeout
values were weakened. This does not claim a single clean broad-suite run.

`git diff --check` passes. No database migration or dashboard source change is
introduced, so migration/drift and separate frontend interaction tests were not
run for M1. No deployment, native client execution, or live device mutation was
performed. The staged adapters remain unavailable through the public v1 API.
M1 is complete; durable execution and public v2 activation are later gates.

## M2 completion evidence

Migrations 41–42 add scoped tasks, steps, approvals, ordered events, input queues,
operation receipts and immutable staged-attachment bindings. Task claims use
`SKIP LOCKED`, leases and fencing; checkpoints and events commit together.
Model/tool budgets are reserved before invocation. Session deletion fences
workers, invalidates approvals, redacts task content and revokes staged handles.
The worker and operation transport remain staged until subsequent activation
and write-adapter milestones; no production worker or new provider is enabled.

Authenticated task controls support snapshots, paged events, stop, follow-ups
and digest-bound decisions. Expected revisions are checked under the task lock;
identical retries are idempotent. The immutable approval ID is the decision
identity, so a second client-generated decision ID is unnecessary. Cookie CSRF,
bearer authentication, foreign-owner rejection and private-checkpoint omission
have direct HTTP tests. Client claim/result transport remains M4, as specified
by the native-client compatibility gate.

The destination journal stores an uncertain receipt before dispatch and never
replays an uncertain operation. Local domain effects and completion receipts
share a transaction. Tests cover lost-result replay, rollback, stale leases,
concurrent claims/submissions, approval expiry, follow-up cancellation, pool
reopen and server-object restart. A subprocess exits after a synthetic effect
before receipt completion; reopening the journal refuses replay. Race checks
pass for the registry, journal and staging packages. Digest/receipt tests retain
exact large numeric values instead of rounding distinct actions together. Actual Notes/agent/calendar mutation adapters
remain gated by M4. Durable journal locking is supported on Unix; other
platforms fail closed until native locking/durability support is implemented.

Staged bytes use the existing attachment volume, verified checksums, fsync and
atomic promotion. The owner/session binding becomes readable only after bytes
are durable. Tests cover partial upload, restart, identical/changed retry,
corruption, cross-user access, session deletion and escaping symlinks. Recovery
restarts an interrupted upload from byte zero. No automatic production cleanup
is introduced; expired/unreferenced stage files require reviewed maintenance.

Focused store, worker, journal, staging and task HTTP checks pass against the
isolated PostgreSQL container. `make fmt` and `make build` pass. Migration up,
`make migrate-status`, `make schema-drift-check`, and a fresh migrated database
schema comparison pass through version 42. Production data/configuration are
unchanged. Separate dashboard interaction tests are not applicable yet because
no dashboard source has changed; the frontend production build passes.

An earlier broad run passed all new assistant checks and non-cloud packages but
hit two existing realtime tests whose five-second deadline included database
creation/migrations. Their deadline now starts after database setup; assertions
and the five-second behavior deadline are unchanged. Both pass in focused runs.
The next sequential-package run passed cloud (525.133 seconds) and storageops
(60.815 seconds), then the store build saw a changed import after Go had loaded
its dependency graph. A standalone build and the exact unit checks passed.
The subsequent frozen-source full run passed: cloud 515.666 seconds,
storageops 68.886 seconds, and store 175.427 seconds; all other packages passed
or had no tests. Command: `HANK_TEST_DATABASE_URL=… go test -p 1 -parallel 4
-timeout 30m ./...`. This includes staging cleanup/expiry, wire records, exact
numeric identities and uncertain-stop coverage. `go build ./...` and
`git diff --check` pass on the final files. M2 is complete.

## M3 completion evidence

Native Ollama and OpenAI API-key transports preserve typed calls, observations,
usage, and provider continuation. Read-only admission uses existing settings
behind `HANK_ASSISTANT_EXECUTION_ENABLED`; linked ChatGPT/local providers remain
unsupported for v2. Completion and clarification are explicit control tools.
A live ambiguity scenario exposed plain-text clarification being classified as
completion; bounded durable protocol repair and a finish tool resolve that
without phrase matching. Initial live runs also showed omitted inline links;
answers retain links through the server's observed-source list.

The final synthetic live matrix on the existing Ollama `qwen3:14b` passed all
four scenarios: paraphrase (2 turns, 1,441 tokens, 3,951 ms), cross-source search
(3 turns, 2,444 tokens, 6,855 ms), ambiguous selection with compacted/reloaded
context (4 turns, 3,488 tokens, 9,299 ms), and untrusted retrieved instructions
(3 turns, 2,405 tokens, 4,846 ms). These are isolated synthetic observations,
not household data or physical actions. Monetary cost is unknown. OpenAI
transport tests use a fake endpoint; no new API credential was requested.

Migration 43, strict status, and drift checks passed against the disposable
PostgreSQL instance; an independent freshly migrated schema matches. Source
ownership/revocation, preserved citation origins, concurrent admission rejection,
and cached-evidence removal before provider invocation have focused tests.
Browser QA at 1440×1000 and 390×844 exercised refresh recovery, revision-bound
follow-up, source links, and stop. A mobile composer clipping issue was fixed;
the final checks include composer/navigation bounds, no horizontal overflow,
no runtime console errors, and screenshot inspection. Playwright used synthetic
API fixtures against the actual local dashboard; no production data changed.

The final frontend gate passed 100 files / 608 tests plus both production builds.
`make build` and shell syntax checks passed; 44 local links in changed docs
resolve. `scripts/doctor.sh` could not validate a deployment because this
checkout has no `.env.cloud` or `.env.agent`; no environment was invented or
production service changed. Native calendar/agent write acceptance remains M4.
The standalone live transport smoke also passes with the explicit finish
protocol and server-generated source link (2 turns, 25,065 ms). Its old inline-
citation-only assertion was corrected to test the delivered answer contract.
The first full Go run passed the new assistant checks and all non-cloud
packages, but `TestProfileScopedCollaborationDoesNotRequireHome` exhausted its
five-second deadline during database fixture creation/migrations. As with the
M2 fixture corrections, its unchanged behavior deadline now starts after setup.
Final link review added file preview parameters, decoded note identifiers,
distinct calendar identities with owned observation links, and machine-detail
links. Focused checks passed (4.968 seconds), as did the final build and full Go
rerun: cloud 552.040 seconds, all other packages passed or had no tests (cached
where unchanged). Command: `HANK_TEST_DATABASE_URL=… go test -p 1 -parallel 4
-timeout 30m ./...`. Runtime sources stayed unchanged during this final run.
`git diff --check` passes. M3 is complete; no deployment was performed.

### M4 progress — Notes and exclusive folders (in progress)

Notes create/append now consume exact revision/digest-bound approvals, recheck
permissions and target revision, and commit their existing Notes collaboration
write, readback, and execution receipt in one PostgreSQL transaction. Recovery
continues from the receipt without repeating the write. Rejection becomes a
structured observation and resumes the original request. Stop before a local
atomic write records no effect; remote uncertainty remains visible on refresh.
The dashboard reuses its action card for exact content and target previews and
sends immutable approval identity, digest, and task revision. Desktop/mobile
Playwright checks pass for approval/rejection, refresh, and composer visibility.

Folder execution uses the existing file adapters with exclusive single-folder
creation, no implicit parents, and contained local `os.Root` operations. The
agent persists intent before creation, reads the directory back before confirming,
and never replays an uncertain receipt. A persistent journal epoch binds proposals
to one receipt store, preventing replacement/lost storage from authorizing replay.
The core journal has a dedicated Compose volume and fails closed if locking or
metadata validation fails. Raw public WebSocket relay rejects internal assistant
operations even for administrators. Server recovery queries the exact destination
receipt before any dispatch and validates identity, source, path, and readback.

Focused Notes/approval/stop/revocation and store tests pass (cloud 15.304s, store
3.056s); focused dashboard tests pass (21 tests), TypeScript passes, browser
artifacts have no console errors or horizontal overflow. The complete agent
and configuration suites pass. PostgreSQL-backed folder reconciliation and
prepare tests pass (9.540s), covering absent, confirmed, and uncertain receipts;
relay-boundary tests pass. No new database migration in this M4 increment.
M4 was not complete at this checkpoint: uploads, HA/machine writes,
explicit apps, broader recovery cases, and full milestone validation remain.
Native providers still advertise read tools only. No deployment or live file,
calendar, machine, or physical-device mutation has been performed.

M4 staged uploads now use the existing server attachment volume and agent file
adapters. Owned conversation stages are checksum/size-bound, can be listed as
structured tool results, and transfer as bounded resumable chunks into private
agent staging before an exclusive destination write. The approval names exact
agent/source/path, byte count, and SHA-256. Agent readback hashes the complete
file; missing stages, corrupt chunks, duplicate acknowledgements, existing
files, and replay are covered. HTTP authentication/CSRF, owner/session scope,
wire schemas, approval and chunk execution tests pass (16.016s); agent suites
pass. All 609 frontend tests pass, and dashboard/MCP builds pass after correcting
an unsupported test-query option. The fake WebSocket fixture now uses the same
message limit as production; the first large-chunk test had exposed its smaller
default. The UI attachment button remains gated with model write advertisement
until the full M4 acceptance gate.

Home Assistant adapters now bind exact primary agent, entity, observed state,
updated timestamp, service, and receipt epoch. The agent enforces a narrow
service allowlist, checks the approved prior state, sends one service POST, and
performs at most three readbacks. Observed target state is `confirmed`; successful
commands without matching readback are `accepted` with verification unavailable.
Server validation rejects wrong entities/states and preserves this distinction.
Focused agent tests and PostgreSQL-backed HA tests pass (14.992s). No live Home
Assistant command was sent. Machine service adapters and the remaining M4
app/recovery acceptance work remained outstanding at this checkpoint.

M4 machine service controls now default to disabled, with an explicit per-unit,
per-operation agent allowlist. Home administrator authorization is rechecked
for reads, preparation, dispatch and retained context. Fixed-argv systemctl
operations use the common journal, reject changed prior state, and require a new
invocation ID before confirming a restart. Server tests reject a claimed restart
with unchanged invocation and prevent a demoted administrator from dispatching.
Agent/config suites pass; PostgreSQL-backed service/schema tests pass (11.297s).
Tests inject a command runner; no host service was started, stopped or restarted.
Explicit apps, broader recovery, live write fixtures,
and the full M4 gate remain outstanding.

### Calendar scope deferral — 2026-09-20

Calendar execution work is deferred. Removed the unfinished v2 device enrollment,
client claim/result transport, calendar write dispatch and migration 44. That
migration had not been applied to the development base database or a deployed
database; no deployed schema was changed. Existing calendar routes, snapshot
read/preparation adapters and source links remain. M4 continues with its remaining
non-calendar gates; M5 and M6 have not started.

### M4 interactive write integration — validation in progress

Native model tool advertisement now includes implemented Notes, exclusive folder
and upload, allowlisted HA, and machine service adapters. Calendar mutations and
model-selected opaque app calls remain excluded. Explicit installed-app slash
commands prepare an exact app/version/agent/request approval, record dispatch
intent before execution, and never replay an interrupted opaque effect. App
results are accepted/unverified; current command permission is checked before
retaining their evidence for continuation. Unverified operation receipts cannot
be promoted into verified success by the final model response.

Agent transport errors now persist bounded `waiting_retry` receipt reconciliation.
Only a same-epoch explicit absence can authorize dispatch; unavailable or foreign
receipt identities never justify a write. Staged upload storage is bounded at
4 GiB including orphans; existing source roots and configured attachment volume
remain authoritative. The chat enables attachments, approvals and clear retry and
unverified-result states. Calendar execution remains deferred.

Focused app/permission/version/replay tests pass. The existing qwen3:14b model
completed real worker tasks against a disposable PostgreSQL Home: exact approved
note creation (one effect, two turns, 4,356 tokens, 5,159 ms active) and rejected
creation with follow-up (zero effects, three turns, 6,127 tokens, 7,367 ms active).
Desktop/mobile approval/rejection browser flows pass with no errors or overflow.
Full platform validation remains in progress; M5 has not started.

### Redeployment preparation — read-only observations, 2026-09-20

The registered `hankdemoserver` is reachable through verified fleet SSH identity.
Its running cloud and agent share image
`sha256:98c09a2db721d4a3326c26bcaf15e6ecfaa01423b66f301e7e0f52923fdd21b6`.
Keep that image as rollback evidence. The running application is at migration 35,
whose checksum matches this checkout; the actual pending upgrade is 36–43, not
just the assistant-specific 41–43. The deployment directory is
`/srv/hank/HankServerside` and is not a Git checkout.

Preserve both `docker-compose.yml` and its existing
`docker-compose.demo-volumes.yml` override. The latter binds the actual database
at `/var/lib/postgresql/data` to `hankserverside_hank_postgres_data_pg18`; do not
replace it with an empty volume during the upgrade. Existing file, Notes, app,
attachment, backup, and monitoring volumes must remain bound. The new agent
receipt volume is additive and must persist across subsequent upgrades.

pgBackRest reports healthy stanza `hank` and a successful differential backup
ending 2026-09-19 02:00:57 UTC. This is backup metadata, not a restore proof.
The newest discovered restore-proof report is dated 2026-08-23, outside the
release freshness window. A current database-plus-attachment backup/restore
proof remains a deployment gate. No remote source, service, configuration,
database, or volume was changed during these observations.

M4 final checks: the first full PostgreSQL-backed run passed all packages except
cloud, where two unrelated 10-second tests expired during fixture migrations
before their first domain write. Their behavior deadlines now start after fixture
creation. The focused rerun, including exact app admission, receipt continuation,
permission revocation and reconciliation, passed (21.578 seconds; the newly added
HTTP admission case is queued for the final targeted pass). The 609-test frontend
gate and production build passed; an additional attachment submission-order test
brings the focused UI/API gate to 22 passing tests. Pinned monitoring checks pass.
Fresh migration schema comparison reports no drift. Local doctor is blocked by
missing runtime environment files, not counted as a deployment-health pass.

M4 complete: final full PostgreSQL-backed `go test -p 1 -parallel 4 -timeout 30m
./...` passed (cloud 583.959s; remaining packages passed/cached). Final explicit
app HTTP admission/continuation/privacy checks passed (18.159s), including the
small attributed-response change after the broad run. Builds, vet, frontend,
browser and schema evidence above pass. Calendar remains deferred; no deployment
or real household/device/file action was performed. M5 is now active.


M5 evaluation baseline and cutover budgets (2026-09-20): the first real HTTP/
worker/CLI run against the existing qwen3:14b completed 12/12 synthetic tasks
(create, reject, append, source-linked read, three repetitions). No unexpected
proposal or incorrect/duplicate effect was observed. Active p50/p95 were
14.363/27.676 seconds; total p50/p95 were 16.857/30.105 seconds, including
synthetic automated approval waits. Tokens were 4,527–10,452 per task. This is
a small synthetic sample, not a measured household-wide reliability rate.

Before default cutover, the repeated paraphrase run must meet active p95 ≤90s
and automated total p95 ≤180s, within the existing 12-turn/24-call/32,000-token
and five-minute active budgets. Human approval time is excluded from active
latency. No monetary threshold is asserted: provider price is unknown in the
report; local model hardware and energy are unmeasured. OpenAI transport has
deterministic parity coverage only and needs its own live model gate before
claiming production parity. No existing provider settings or credentials change.

M5 holdouts exposed two blocked proposals: one altered punctuation in an exact
note body, and another added a separator newline that the append adapter already
inserts. Neither was approved and neither caused a write. The tool descriptions
now state literal text preservation, automatic newline insertion, and the exact
result ID/revision fields to use. The driver reports fixed mismatch classes
without copying proposed content. The original holdout prompts and exact-state
assertions are retained for the corrected-model run; failed trials are not
counted as passes.

The newline correction passed, but punctuation still failed with descriptions,
a stronger instruction, lower sampling temperature, and a separate reasoning
experiment. Reasoning was restored to the prior non-thinking transport behavior.
The effective correction supplies bounded, verbatim quoted user literals as
copy context beside the unchanged request. It does not select tools, rewrite
arguments, or bypass exact approval. A focused literal-boundary regression now
passes three native model calls; the complete unchanged holdout suite is rerunning.
Literal hints preserve whitespace/Unicode/backslashes, ignore unmatched quotes,
and are limited to 16 values and 4,096 source bytes. They are never derived from
retrieved tool data. Ollama tool turns use temperature zero for reproducible
argument selection. Failed trials remain recorded separately from final gates.

Repeated cross-source reads also caught answers that linked both sources but
omitted the requested day and room. The completion contract now asks for each
requested fact with its supporting URI, rather than a source inventory. The
unchanged cross-source case passed three consecutive native-model trials after
that correction. Final combined outcome/read and full PostgreSQL gates follow.

Final M5 native outcome gate (qwen3:14b, existing Ollama): 12/12 end-to-end tasks
passed, covering three phrasings each for creation, rejection, append, and
source-linked retrieval. Exact-content proposal regression also passed three
calls. Unexpected proposals and incorrect effects were both zero in this final
batch. Active p50/p95: 14.597/29.234s; automated total p50/p95: 16.623/30.838s;
5,201–11,049 budget-charged tokens per task. All recorded limits were met. Cost
remains unknown. For scale only, 12/12 has a Wilson 95% lower bound of about
75.8% under independent Bernoulli assumptions; fixed synthetic cases are not a
random household sample, so this is not a population reliability claim.

Security impact: all native writes retain server-owned exact approval,
authorization rechecks and durable receipts; the cutover changes default routing
for new supported dashboard chats, not those enforcement boundaries. Quoted
copy hints are user-level input only and do not elevate retrieved content.
Database impact: no M5 migration; the full feature requires migrations 41–43,
with all pending earlier migrations also applied on an older deployment.

The final native read matrix also passed 12/12 trials: paraphrases,
cross-source fact synthesis, ordinal selection after checkpoint compression/
reload, and untrusted retrieved instructions, each repeated three times. These
use synthetic tool observations; actual HTTP/worker/Notes persistence is covered
by the separate 12-task outcome batch. Full backend validation is now running.

The real restricted-app acceptance gate (`make app-sandbox-test`) passed in its
disposable Linux Docker environment, including containment, broker grants and
revocation, detached descendants, resource limits, and the example SDK. Those
opt-in tests are skipped by ordinary `go test` but are covered by this separate
run. No production container privileges or configuration were changed.

Predeployment checks not claimed as live deployment validation: local
`scripts/doctor.sh` cannot run its runtime checks without `.env.cloud`/`.env.agent`;
no deployed health check or real household file/device/service mutation has been
performed. OpenAI has mock transport coverage, not a live release evaluation.
The legacy live baseline and optional MCP 2026-07-28 conformance suite were not
rerun for this assistant gate. The pinned monitoring renderer was covered by
the earlier passing `make monitoring-test`; the restricted-app opt-ins ran
separately as recorded above. Native provider opt-ins run separately from the
ordinary PostgreSQL suite. Fresh server backup/restore proof, actual migration
application, deployment, and post-deployment smoke checks remain operational
steps; no production state was changed by this work.


M5 local gate complete: the final full PostgreSQL-backed command passed with
1,278 cases including subtests and no failures (cloud 514.524s). Its 21 opt-in
skips comprise five sandbox cases and five native-provider cases covered by
separate runs, the pinned monitoring renderer covered earlier, and ten unrun
checks: legacy live baseline, MCP conformance, and eight deployed load probes
(health, session validation, app WebSocket relay/reconnect, concurrent file
transfers, cross-source move jobs, Notes listing, assistant requests).

The final native create/reject worker check passed (one exact write after
approval, zero writes after rejection). The older native smoke test assumed
one search round and initially failed. It now uses the production structured
note result and handles bounded search refinement while still rejecting writes
and requiring the exact day and source. Five fixed consecutive trials passed;
three needed a third model turn. No runtime code changed after the successful
full Go gate; this last change was confined to the opt-in test driver.

Final frontend gate: 100 files, 611 tests and both production bundles passed.
Desktop/mobile browser approval/rejection checks, new-chat default, legacy-chat
compatibility, console and overflow checks passed. Go build/vet, formatting,
dependency tidy, local link checks and `git diff --check` passed. Migrations
through 43, checksum status and fresh-schema drift comparison passed as recorded
in M4; M5 adds no schema changes. No commit, push or deployment was performed.

Deployment checkpoint (2026-09-20): deployed the working-tree artifact
`assistant-20260920-bb39941b6554` to the existing demo cloud and primary agent.
Migrations 36–43 applied with strict checksums and no schema drift. New-task
admission is enabled for the existing Ollama `qwen3:14b` setup. A fresh encrypted
backup and isolated restore matched all 10 attachment records and bytes. The
live synthetic checks passed approval, refresh, server restart recovery, exact
single-write verification, retrieval/source identity, rejection/follow-up, and
stop. The primary agent receipt identity survived its restart. Two existing
worker agents retain their older capabilities; no worker update was attempted.
The initial smoke was interrupted by the database restart performed by the
operational check; its pending synthetic task was explicitly cancelled. The
complete rerun passed. No household device, file, or service write was tested.

Release evidence is retained privately under `data/release-reports/` locally
and `/srv/hank/release-assistant-20260920-bb39941b6554/` on the demo. Existing
storage overrides, encrypted credentials, and operation receipts are preserved.
Once v2 tasks exist, disable new admission and retain a compatible recovery
worker for rollback; do not downgrade to the archived pre-v2 image.

Next checkpoint is user acceptance in a fresh Hank chat. Calendar remains
deferred. Scheduling remains M6 after interactive validation.


Production deployment checkpoint (2026-09-21): promoted the validated artifact
`assistant-20260920-bb39941b6554` to Campbellservers / UbuntuDocker, not demo.
Official legacy-ledger reconciliation and naming migration completed, followed
by embedded migrations through 43. Strict status and deep drift passed. Both
pre-upgrade and post-upgrade encrypted backups passed isolated paired restore
with 32 attachment records and 33 files. Public readiness and dashboard login
respond successfully. Live approval/refresh/cloud-restart/exact-write/retrieval/
rejection/follow-up/stop checks passed. Primary agent receipt identity survived
restart. Doctor has zero failures and one existing monitoring-profile warning.
Offline workers were not updated. Optional app startup reports gramaton and
ydownload unavailable; their repair is outside this assistant deployment.
Evidence: `data/release-reports/hank-assistant-production-20260921/README.md`.
Next: user acceptance in a fresh production chat; calendar deferred, M6 pending.
