# Hank Security Model

## Trust Model

HankServerside is the public security boundary for a self-hosted Home. Clients
authenticate to HankServerside; Hank Agents authenticate as exact enrolled
machines and connect outbound. Local resources, private protocols, and
machine-local credentials remain behind the agent boundary.

The deployment assumes the host, PostgreSQL, reverse proxy, and configured
secret source are operated as trusted infrastructure. It does not assume that
every signed-in user is an administrator or that every agent has every
capability.

## Authentication And Authorization

Browser sessions use secure server-side session state. Supported sign-in paths
include Hank credentials and configured Microsoft Entra SSO. Invitations,
roles, Home membership, per-member permissions, API tokens, MCP grants, agent
tokens, and remote-desktop identities have separate scopes.

Every protected handler must authorize the current user, Home, role, resource,
agent, or token on the server. IDs from a request body are never sufficient
authorization. Foreign or inaccessible resources normally return `404` so the
API does not disclose their existence.

Rate limiting and per-account backoff protect login flows. Password reset does
not reset unrelated cryptographic trust such as remote-desktop identities.

## Browser Write Protection

Cookie-authenticated `POST`, `PUT`, and `DELETE` requests require the dashboard
CSRF token in `X-Hank-CSRF-Token`. Bearer-token APIs and WebSocket upgrades use
their own authentication and do not rely on browser cookies for authorization.

Sessions and browser credentials use Secure, HttpOnly, and restrictive
SameSite/path settings where their flow permits. Reverse proxies must preserve
the public HTTPS origin and required headers.

## Agent Connectivity And Credentials

Hank Agents connect outbound to `/ws/agent` with
`Authorization: Bearer <agent-token>` and `X-Hank-Agent-ID`. Credentials never
belong in query strings. The server matches the presented credential, stored
agent, Home, claimed agent type, live connection, command target, and capability
before routing work. Command replies and file-transfer frames must match the
Home, agent, and exact authenticated socket selected when that command or
transfer attempt was dispatched. A reconnect cannot complete an older socket's
requests; the older socket can still finish its own in-flight work. Resumed
transfers bind each new attempt to its selected socket. Optional envelope
identity fields may be omitted, but conflicting claims are rejected.

Setup and enrollment tokens are scoped, revocable, hashed at rest where only
verification is required, and displayed only at creation. Worker enrollment,
credential rotation, revocation, release rollout, and status events remain
bound to the authenticated agent and Home.

## Local Resource Boundary

Home Assistant tokens, SMB credentials, local folder configuration, app
secrets, and privileged machine authority belong on the Hank Agent that uses
them. The cloud relays settings through authenticated agent commands but does
not expose raw local protocols publicly.

Agent commands must enforce configured capabilities and local policy. Shell,
package, process, service, filesystem, and remote-desktop operations remain
disabled or restricted unless the exact agent configuration and server-side
authorization allow them.

Assistant project-document reads are confined to the configured directory using
root-relative filesystem handles, including symlink resolution. Reads are
bounded before document content is allocated; nonregular files and escaping
links are excluded.

Assistant operator traces use predefined event summaries and allowlisted,
validated metadata. They exclude private prompt/query/source content and raw
provider/agent errors. Execution-version negotiation never silently runs a
different assistant engine than explicitly requested by a client.

## Secret Storage


`HANK_SECRET_ENCRYPTION_KEY` is required for normal startup. The explicit
`HANK_ALLOW_PLAINTEXT_SECRETS=true` opt-out is for throwaway local
development only.

The encryption key protects supported stored OAuth credentials, APNs tokens,
Web Push subscription endpoints/key material, profile-vault data, and other
values using the existing secret helpers. Token verifiers store hashes when the
plaintext is not needed again. `.env.cloud` and `.env.agent` must be mode `0600`
and excluded from source control, logs, support bundles, and documentation.

Older deployments must pass:

```bash
docker compose --env-file .env.cloud run -T --rm \
  --entrypoint /usr/local/bin/hank-server cloud secrets status --strict
```

Use the documented remediation command only with the correct encryption key.
Losing or changing the key without a migration makes existing encrypted values
unreadable. See [Token and secret rotation](runbooks/token-rotation.md).

## Files And Attachments

Every local-file and attachment path is cleaned, symlink-resolved, and checked
for containment inside its configured root before read, write, stat, rename,
move, delete, upload, or download work. Package extraction and runtime paths use
the same clean-relative-path principle and reject traversal, absolute paths,
symlinks, duplicate entries, and paths outside the install root.

Go agent local-file mutations also use an open `os.Root` directory handle for
parent creation, file opens, and rename. This enforces containment at the write
itself, including dangling links and symlink replacement after path validation.
Existing internal aliases and configured symlink roots are resolved to contained
relative targets; a missing target's parent must exist when accessed through a
terminal alias. SMB uses the separately authenticated share adapter.

File transfers and managed jobs bind ownership, Home, agent, source, path, and
lifecycle state. Move event writes enforce Home, durable agent ownership, and
operation together with the terminal-state guard. Command response job IDs must
match the dispatched job. Recovery entry points share the same owner and file
policy checks. Historical ownership is not inferred from the current primary
or incomplete relay history. Note and Kanban attachment access binds the exact user-visible
note/card target, attachment ID, revision, and permitted representation.
Authenticated HTML previews are sanitized and sandboxed; downloads preserve the
original bytes.

Search metadata is scoped to the authenticated Home and Files feature. Results
from a connected agent are filtered by the current Home and source read
policies before returning paths or names. The catalog contains metadata only;
opening a result rechecks the normal agent/source/path authorization. See
[Search](search.md) for indexing and freshness behavior.

## Backup And Restore

Database operations run through the dedicated intent/worker flow and pgBackRest
repository. Primary restore requires an administrator, the exact confirmation
phrase, and a short-lived administrative action token. Backup and restore
events are audited without secret values.

Release readiness requires migration status, schema drift, backup freshness,
and recent restore proof. Repository cipher-passphrase rotation is a repository
migration, not a routine string replacement.

## MCP And Installable Apps

MCP uses scoped OAuth grants and tool-level authorization. A token receives only
the approved Notes, Kanban, attachment, live-context, or project-document
scopes. Project-context roots and project-document reads enforce containment,
extension, symlink, and size limits. User settings can exclude private Notes and
other source families.

`.hankapp` packages are untrusted archives until validated. The runtime enforces
manifest schema, package containment, runtime command location, reserved
command names, access mode, file-source permissions, secret preservation, and
bounded invocation. App stdout/stderr must not expose secrets.

## Remote Desktop

Native remote desktop uses a Home-specific P-256 trust root with separately
certified operator-device and endpoint identities. HankServerside stores public
trust material, encrypted recovery envelopes, credential/challenge hashes,
authorization metadata, lifecycle events, and aggregate byte counts. It does
not store private keys, the offline recovery secret, plaintext join credentials,
screen frames, input, clipboard content, or ciphertext samples.

Browser and agent joins are independently authenticated and bound to one Home,
agent, session, side, and key epoch. The data plane is end-to-end encrypted and
the relay forwards opaque bounded frames. Revocation, expiry, authorization
loss, invalid sequence/epoch, backpressure, or termination closes both sides.

Privileged Windows and macOS components keep reusable agent credentials,
endpoint keys, desktop authorization, and traffic keys outside user-session
hosts. Native code signing, operating-system permission, visible-indicator,
secure-desktop, active-console, IPC authentication, packaging, and physical
acceptance requirements are part of the V1 release gate.

The web viewer has an explicit limit: end-to-end encryption cannot protect a
user if a compromised server replaces the viewer JavaScript before execution.

## Logging, Audit, And Metrics

Logs and audit records may contain bounded actor, Home, agent, resource,
lifecycle, timing, result, and aggregate-size metadata. They must not contain
passwords, raw tokens, OAuth credentials, push keys/endpoints, private keys,
recovery material, private file or note contents, command output, screen/input/
clipboard data, request bodies containing secrets, or raw ciphertext.

`/healthz` and `/readyz` are public deployment probes. `/metrics` requires an
administrator session or the configured dedicated scrape token. Monitoring
services should remain bound to private or loopback interfaces unless an
authenticated network boundary protects them.

HTTP metrics use registered route patterns and a fixed fallback for unmatched
paths, with a bounded method set. Request paths, query strings, resource IDs,
and installer credentials must not become metric labels. Monitoring credential
directories are excluded from Git and Docker build contexts.

Alertmanager inbox delivery requires its own bearer credential with only
monitoring-inbox write authority. Requests are bounded to 1 MiB and 256 alerts;
recipient Home and administrator membership are resolved server-side. Raw labels
and generator links are not persisted, and navigation targets are fixed. Alert
summaries are bounded display text. Duplicate incident deliveries use durable
receipts. SMTP requires verified STARTTLS. Administrator-only Home settings use the existing
authentication and cookie-CSRF boundary; credential fields are write-only and
stored using the existing encryption helper. SMTP DNS results must be public and
are pinned into the generated configuration with separate TLS hostname
verification. The private runtime configuration contains the secrets needed by
Alertmanager and must be protected as secret material. Save/test audits contain
only bounded channel choices; remote errors and configuration bodies are not
returned. GUI test sends are rate-limited. The Alertmanager URL and output root
are deployment configuration, never user-supplied API fields.

New attachment backups use authenticated streaming age encryption with the
existing backup repository passphrase. Restore verification consumes the full
ciphertext and verifies file hashes before publishing staged files. Legacy
plaintext archives remain a compatibility input and must retain protected storage;
see [deployment recovery guidance](deployment.md) for limits.

## Operator Responsibilities

- terminate public traffic with HTTPS and preserve origin/security headers
- protect `.env.cloud`, `.env.agent`, backups, database volumes, and signing keys
- configure and test monitoring receivers
- rotate scoped tokens through staged replacement rather than simultaneous
  revocation
- keep agents and privileged packages current and signed
- run secret status, migration, schema-drift, backup, restore, and release gates
- review audit/log access and retention
- never paste secrets or private content into issues, docs, or validation
  evidence

## Reporting And Reviewing Security Changes

Security changes should state the protected asset, attacker position, trust
boundary, authorization scope, failure behavior, logging/content boundary, and
rollback plan. Add focused success, denial, cross-scope, malformed-input, and
secret-leak coverage. Use `AGENTS.md` for the repository change gate and
`RELEASE.md` for release evidence.


Installable apps execute through the mandatory [restricted runtime](app-platform.md).
Administrator grants bind package contents to exact URL/file-source scopes; legacy
agents cannot receive app execution/configuration requests. Unsupported sandbox
hosts fail closed. Both ordinary commands and settings validation share the same
boundary, and app-provided events cannot impersonate core platform events.

Assistant execution-v2 persistence is scoped to the owning session, Home, and
user. It fences stale workers and binds approval to a proposal digest, actor,
expiry, and single consumption. Stop/follow-up invalidates an old worker and
unconsumed proposals. Conversation deletion removes task conversation content
and follow-up text, fences execution, and retains receipt identity/digest/
outcome needed to prevent duplicate actions. A dispatched remote action may
have happened even when a task is cancelled; an uncertain receipt cannot be
reported as verified success or blindly retried. V2 admission is configurable; supported writes require exact approval.

Server-owned assistant attachment stages are scoped to the exact Home, user,
and session, with immutable size/checksum metadata and 24-hour handle expiry.
Byte access uses an open directory handle under the configured attachment root,
rejects escaping symlinks, and verifies content before execution. Partial uploads
are not addressable; a lost upload response can safely repeat the same bytes.
Conversation deletion cascades the stage binding, immediately removing API
access. Expiry/deletion does not claim secure erasure of retained volume bytes.

Native tool output is untrusted evidence, not an instruction source. Cached
observations are reauthorized before provider turns and again before answer
publication. Revocation discards old assistant/tool evidence; pending calls
with revoked context fail closed. Source observation downloads enforce task
ownership and current source authorization and use plain text, no-store, and
nosniff responses. Session execution adoption and a unique active-task index
prevent mixed legacy dispatch and concurrent task admission. Activity exposes
safe states and tool names, not arguments, provider reasoning, or retrieved text.

Approved Notes writes use the existing collaboration transaction and atomically
persist their readback receipt. Revoked visibility or changed revision prevents
mutation. Folder/file adapters journal intent before effects, create exclusively
inside configured sources, and verify directory metadata or complete file bytes
before confirming. Agent receipt epochs survive restarts and reject operations
prepared against replacement stores. Receipt lookup is identity-bound; absence
is actionable only within that same store. Internal execution/staging commands
cannot be sent through the public app relay. Upload transfer stages are private
agent state, not a public file source, and do not authorize destination effects.

Home Assistant execution uses a narrow domain/service allowlist on both server
and agent, binds the prior entity state to approval, and submits the service once.
Only matching readback is a confirmed change. A successful command without a
verifiable matching state remains accepted, with verification unavailable; it is
not retried to obtain a more convenient success result.

Opaque installed-app calls enter v2 only through explicit slash admission. The
server records intent before dispatch, checks current command permission and app
version, and never automatically retries an uncertain app effect. The restricted
app runtime remains mandatory. An app response is attributed and unverified;
revoked app results cannot survive as conversation evidence. Completion reporting
checks durable uncertain receipts even when old conversation evidence is removed.

Machine service execution requires Home administrator access and an agent-local
allowlist of exact systemd units and operations. It uses fixed argv, bounded
output/time, prior-state checks and the same durable receipt protocol. A restart
requires a new invocation ID to count as observed; command acceptance alone does
not confirm a restart. No arbitrary shell tool is exposed to the model.

## Demo fleet access

CLI fleet grants bind immutable exact agent IDs and operations to a current Home
member. Hosted account grants bind full access to all current and future Home
devices to the approved account. Tokens are hashed at rest, delivered once, and inactive until a Home
administrator reviews and approves the grant with an exact confirmation phrase
and single-use, user/action-bound token. Runtime access rechecks grant state,
expiry when set, membership and account state. An explicitly approved infinite
grant has no time limit, but remains subject to revocation and the same live
Home, target, operation, and calling-credential checks. Agent credentials cannot authenticate the
client API. Generic app relay rejects fleet commands, preventing caller-stamped
ownership bypasses. The local MCP bridge uses the same grant API and exposes no
grant-management tools; existing MCP OAuth scopes gain no machine authority.

Agent-local workspaces use root-relative filesystem handles, regular-file checks,
revision preconditions, private temporary files and atomic replacement. Jobs and
files bind to a grant and target. Job execution remains unrestricted shell
execution as the agent account, including root in system mode; file containment
and operation scopes do not sandbox a granted shell. Four active jobs per agent,
300-second deadlines and bounded output limit ordinary resource consumption.

Revocation denies new requests immediately and persists cancellation intent for
active jobs. Expiry and membership removal trigger cancellation on reconciliation.
Offline work is uncertain until reconnect, not falsely reported cancelled.
Cancellation controls the ordinary process group, not deliberately detached or
hostile descendants. Agent interruption records unknown outcome and never
silently restarts the script.

PostgreSQL stores grant hashes and job/workspace metadata, never scripts or output.
Private job output is retained locally in mode-0600 files. Audit events contain
actor, target, operation and IDs; tool responses carry content only to the
explicitly authorized caller. Legacy `shell.exec` relay records retain empty
request/response payloads and a constant failure message. Historical records are
not automatically erased. See [fleet demo](fleet-demo.md).

### Hosted fleet and pairing controls

Hosted account grants share all five operations and all Home devices across that
account's MCP apps. Each call checks the grant owner, current Home membership,
approval, expiry and the calling token's owner, expiry and revocation. Another
account cannot use the grant, including a Home administrator acting through their
own MCP token. A newly enrolled device in the same Home is included automatically;
workspace foreign keys still enforce grant and Home ownership. OAuth refresh and
new connections do not require another account approval. Disconnecting one app
does not revoke account authority or cancel its jobs; account revocation does.

Migration 52 defaults account access to false and preserves existing approvals.
Earlier per-connection grants retain their binding, scope and disconnect
cancellation behavior. New hosted requests use `mcp_account:true` and cannot
specify a connection, devices or operations. Database constraints require full
operations and no connection binding for an account grant. Neither hosted grant
type can authenticate the bearer fleet API. Notes scopes alone confer no machine
authority. GUI approval uses the single-use action-token review; all GUI writes
require authenticated membership and cookie CSRF. Grant credentials are not issued
for hosted access. Connector enablement remains admin-only and audited.

Linux pairing codes contain 60 random bits, are hashed at rest, expire in 15
minutes and are consumed once under a transaction. Codes never appear in query
strings. The consume endpoint enforces IP and deployment rate limits. Local
Linux setup binds to loopback, verifies Host and Origin, requires a random launch
secret in a request header, and uses HTTPS with normal certificate validation
for enrollment. System enrollment still grants unrestricted root management.

Device pairing codes are platform-bound, short-lived, single-use enrollment
credentials. Consumption rechecks the issuer's current Home administrator role
and password-change status. Pairing uses an authorization header, rate limits,
and device-generated runtime credential hashes. Codes and credentials must not
be logged. Desktop trust approval and local OS permissions remain independent.
Fleet grant dismissal is a per-user display preference limited to expired or
revoked grants; it does not change authorization or remove audit history.
