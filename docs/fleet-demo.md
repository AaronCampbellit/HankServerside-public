# Linux fleet demo

HankServerside manages the Home fleet. Agents connect outbound; CLI and MCP
clients use verified HTTPS. There are no public agent listeners, VPN requirements,
or peer credentials. Deployments use the normal release gates and require
explicit operator authorization.

## GUI setup and hosted MCP

In **Settings → AI & MCP**, enable the Hank MCP connector and connect your MCP
app to the displayed URL. In **Agents → Account device access**, choose an access
duration and review the account-wide approval. Members request access; a Home
administrator approves it. Every MCP app authenticated as that account shares the
approval, including a newly connected app or a reconnect. There is no connection,
device, or permission picker. Access covers all current and future registered
Home devices and all five fleet operations. Commands run with the selected
agent account's full permissions, including root for system agents.

Hosted fleet tools use Hank's existing MCP endpoint. No local stdio bridge or
fleet token file is required. `fleet_agents` returns the account's approved grants
and available devices; subsequent calls use the returned `grant_id`. Account
access requires an approved fleet grant as well as a live authenticated MCP token.
Grant revocation, expiry, membership loss and account state still deny access.
Disconnecting an app invalidates that app's credentials but does not revoke the
account approval or cancel account-owned work. Revoke account device access to
stop access across apps and request cancellation of running jobs.

Migration 52 adds opt-in account-wide access without expanding existing approvals.
Earlier per-connection approvals retain their original devices, permissions and
connection binding until revoked or expired. Enable a new account approval and
revoke the earlier approval when ready. Its workspace/job IDs remain owned by the
original grant; a new grant cannot take over them. CLI bearer grants retain their
existing scoped contract. Rollback refuses to remove account access metadata until
an operator explicitly resolves account grants; it never converts them into bearer
credentials.

In **Agents → Install Linux agent**, leave short-code pairing selected and create
a code. On an installed Linux agent (0.3.3 or later), run
`sudo hankagent --system setup`, open its private localhost link, and enter the
server address and code. `setup --terminal` provides guided headless setup.
Hostnames and IP addresses default to HTTPS and must have a trusted certificate.
The 12-character code is single-use, expires in 15 minutes, is stored only as a
hash, and is sent in an authorization header. Pairing is rate limited. The GUI
also retains the one-time installer option for machines without Hank Agent.
Both methods enroll a system agent with unrestricted root management.

The local setup page binds only to loopback, requires the private launch token
and same-origin requests, and expires after 15 minutes. Pairing starts the
system service. Close setup with Ctrl+C afterward; the service continues.

## Visual fleet map

The dashboard Agents page shows registered devices connected through a
HankServerside hub. Solid branches indicate online devices; dashed branches
indicate offline registrations, not active connections. Select a node with a
mouse or keyboard to inspect platform, last seen, CPU load, memory, and disk
usage. The inspector follows the existing 15-second health refresh and links
to the device workspace. Offline metrics are hidden rather than shown as live.
This is a Home control-plane view, not physical LAN or peer-to-peer discovery.
Selection reads existing authorized device data and issues no device commands.

## Authority and grant approval

CLI fleet credentials belong to a user and contain an immutable list of exact
agents and allowed operations. Hosted account approvals cover all Home devices
and operations, including devices registered after approval. A Home member requests access; a Home administrator
reviews the target IDs, operations, requester, expiry and execution authority,
then confirms that specific grant with a single-use five-minute action token.
GUI grant lifetimes are 1 month, 3 months, 6 months, 1 year, or infinite (until revoked). Calendar durations clamp to the last day of shorter months and start when requested. The API accepts `duration`: `1_month`, `3_months`, `6_months`, `1_year`, or `infinite`. Infinite grants return `expires_at: null`. Legacy clients may still supply `hours` (1–168), exclusively of `duration`. Existing grants retain their expiry; revoke and recreate to change duration. Revocation, Home membership, device scope, and MCP connection authorization still apply to non-expiring grants. Credentials are shown to the requesting client
once, stored as hashes on the server, and saved to a private local token file.
Pending, revoked and expired grants cannot operate. Home membership is checked
on every request. An enrolled machine credential never authenticates a fleet
client, and a fleet grant cannot authenticate the ordinary app WebSocket.

Operations are `workspace.read`, `workspace.write`, `job.run`, `job.read`, and
`job.cancel`. Hosted account access includes all five; CLI grants can choose a
limited scope. Workspaces and jobs belong to the exact
grant and target agent; even another grant for the same user cannot access them.
An approval is delegation of the selected operations, not a request for approval
on every subsequent call. `job.run` permits unrestricted shell commands as the
agent account, including root in system mode. Workspace containment applies to
file APIs; it is not a shell sandbox.

Build the standalone Linux worktree with `make build`. Request a grant using a
user `session_token` from the normal `POST /v1/auth/login` flow. Supply it privately:

```bash
export HANK_FLEET_SERVER=https://your-demo-host.example.com
read -rs -p 'Hank user session: ' HANK_FLEET_SESSION_TOKEN
export HANK_FLEET_SESSION_TOKEN
./dist/hankagent --user fleet grant-request \
  --agents EXACT_AGENT_A,EXACT_AGENT_B \
  --operations workspace.read,workspace.write,job.run,job.read,job.cancel \
  --hours 24 --token-file ./fleet-token
unset HANK_FLEET_SESSION_TOKEN
```

An administrator uses their own session with `fleet grants` and
`fleet grant-approve --grant GRANT_ID`. The CLI displays the immutable request
and prompts for the exact confirmation phrase. It does not print the action
credential. Grant management is deliberately absent from MCP tools.
The requester or an administrator can use `fleet grant-revoke --grant GRANT_ID`
with the same review/confirmation flow. Revocation immediately denies new access
and records cancellation intent for active jobs. Cancellation on an offline
machine is pending until the agent reconnects; it is not reported as complete.

## Workspaces and file transfer

```bash
export HANK_FLEET_TOKEN_FILE="$PWD/fleet-token"
./dist/hankagent --user fleet agents
./dist/hankagent --user fleet workspace-create --agent EXACT_AGENT_A
./dist/hankagent --user fleet file-write --agent EXACT_AGENT_A \
  --workspace WORKSPACE_ID --path src/check.sh < ./check.sh
./dist/hankagent --user fleet file-read --agent EXACT_AGENT_A \
  --workspace WORKSPACE_ID --path src/check.sh
```

`workspace-create` prints its stable ID before submitting the request. Reuse
`--workspace ID` after a lost response to provision that same workspace.
Files use the existing inline base64 transfer representation, capped at 512 KiB
per file. Reads return base64 bytes and a SHA-256 revision. The empty write
revision means create-only; overwrites require `--revision CURRENT_SHA256`.
A stale revision is rejected. Writes are refused while that workspace has an
active job. Large trees can be supplied as individual files or fetched by an
explicitly authorized job; there is no implicit directory synchronization.

Agent files live under `StateDir/fleet/GRANT_ID/workspaces/WORKSPACE_ID`.
Root-relative filesystem handles contain symlink resolution; traversal, escaping
symlinks, devices, FIFOs, and nonregular transfers are rejected. Writes use a
private temporary file and atomic rename. Shell processes can change their own
filesystem outside these APIs; revision checks do not sandbox those processes.

## Durable jobs and output

```bash
./dist/hankagent --user fleet job-start --agent EXACT_AGENT_A \
  --workspace WORKSPACE_ID --job job_build_001 --timeout 300 <<'SCRIPT'
set -eu
sh src/check.sh
SCRIPT
./dist/hankagent --user fleet job-watch --job job_build_001
./dist/hankagent --user fleet job-read --job job_build_001 --after 0
./dist/hankagent --user fleet job-cancel --job job_build_001
./dist/hankagent --user fleet jobs
```

IDs contain 8–96 letters, digits, underscores or hyphens. Every new execution
requires a new stable job ID. A repeated identical submission returns the
existing record; a changed request with the same ID conflicts. The server records
admission before dispatch and never redispatches a start automatically. Inspect
that ID after a transport failure; do not create a new ID to retry uncertain work.

The target starts the command in the workspace and returns immediately. The
execution deadline is 0.001–300 seconds; zero selects 60 seconds. Scripts are
limited to 64 KiB. Server and agent each bound active execution to four jobs per
target. Admission remains conservative until active jobs are reconciled.

Output is a cursor-ordered sequence of stdout/stderr text chunks, persisted
locally in private append files. Combined output is capped at 1 MiB per job and
records whether truncation occurred. Reads return up to 16 chunks. Continue
polling the returned cursor until the state is terminal **and output is empty**,
so a fast-finished job's tail is not lost. `job-watch` emits these JSON records
and exits nonzero for failure, cancellation, timeout, or unknown outcome.

States are `dispatching`, `running`, `succeeded`, `failed`, `cancelled`,
`timed_out`, and `unknown`. Metadata and cancellation intent persist in PostgreSQL;
command text, file bytes and output do not. Agent reconnects and heartbeats
reconcile status and cancellation without resubmitting commands. Job output
survives client and cloud restarts. An interrupted agent marks a formerly running
job `unknown`; a graceful shutdown cancels its process groups. Cancellation is
confirmed only when the agent reports completion. Unrestricted commands may
escape a process group; this is not a containment guarantee for hostile programs.

The agent retains at most 128 job records per grant. There is no automatic
workspace/output deletion, production data cleanup, or retention purge in this
demo. An operator can archive/remove private agent state after revoking the grant
and verifying that its work has stopped. Permanently removing an agent through
existing administration also removes that target's fleet metadata and scope;
it does not delete its local files or prove its processes stopped.

## Multi-device workflows

`fleet workflow --file workflow.json` runs up to 16 explicit steps in order and
stops on the first unsuccessful or uncertain outcome. All steps are validated
before execution. The grant must cover every target and required operation.
Each workspace must already be provisioned and populated. For example:

```json
[
  {"agent_id":"AGENT_A","workspace_id":"WORKSPACE_A","job_id":"job_build_001","command":"make build","timeout_seconds":300},
  {"agent_id":"AGENT_B","workspace_id":"WORKSPACE_B","job_id":"job_test_001","command":"make test","timeout_seconds":300}
]
```

Artifacts are not silently copied between machines. Fetch a file from A and
write it into B with an explicit revision, or explicitly run a trusted fetch in
a job. Model clients can coordinate the same operations through MCP.

## MCP bridge

Run `hankagent --user fleet mcp` as a local stdio MCP server with
`HANK_FLEET_SERVER` and `HANK_FLEET_TOKEN_FILE` in its environment. The token file
must be a regular private file with no group/other permissions. The bridge
implements [MCP initialization](https://modelcontextprotocol.io/specification/2025-11-25/basic/lifecycle)
and [tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)
over newline-delimited stdio, negotiating `2025-11-25`.

Tools are `fleet_agents`, `fleet_workspace_create`, `fleet_file_read`,
`fleet_file_write`, `fleet_job_start`, `fleet_job_read`, `fleet_job_cancel`, and
`fleet_jobs`. Discovery filters tools by the current grant; server authorization
checks every call independently. Mutating tools carry destructive annotations;
remote files/output are untrusted data. No model API key or existing Notes/Kanban
OAuth scope is involved. The bridge can be registered by a local MCP-capable
coding client without granting that client a full Hank administrator session.

## Transport decision and measured limits

Retain HTTPS/WebSocket relay for this demo. A local acceptance run with TLS,
PostgreSQL, two real Linux agent processes and fsynced workspace writes measured
five transfers per size: 4 KiB averaged 46 ms, 64 KiB 53 ms, and 512 KiB 103 ms
(about 4.9 MiB/s useful payload for the largest size). A race-instrumented run was
slower, as expected. These are single-host synthetic observations, not WAN,
concurrency, large-repository or physical-device benchmarks.

No demonstrated requirement here justifies direct transport's discovery, trust,
NAT traversal and recovery complexity. Revisit it when measured real workloads
exceed agreed relay latency or throughput targets. Direct transport, if adopted,
must retain the same grants, target identity and audit contracts.

## Legacy session commands

The earlier `fleet list` and `fleet exec --agent ID` remain administrator-session
clients for the existing inventory and one-shot `shell.exec` APIs. They do not
accept fleet grants or provide durable job IDs. New workflows should use the
scoped commands above. See [API contracts](api.md#demo-fleet-shell-execution),
[security](security.md#demo-fleet-access), and [validation](demo-validation.md#linux-fleet-worktree-validation).

## Grant duration migration

Migration 48 makes `fleet_grants.expires_at` nullable; NULL means no expiration.
Existing rows, defaults, indexes, and the finite-expiry constraint are preserved.
Deploy the migration with the server update before creating infinite grants.
Older server binaries cannot read NULL grant expiries. Rollback requires an
explicit operator decision to remove or convert every non-expiring grant before
running the down migration; the down migration refuses to alter access silently.
