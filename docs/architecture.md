# Hank Architecture

## System Context

```text
Hank clients, operators, and integrations
                  |
                  | HTTPS / authenticated WebSocket
                  v
             HankServerside
        +---------+----------+
        |         |          |
        |         |          +-- dashboard and PWA
        |         +------------- PostgreSQL and managed storage
        +----------------------- APIs, relay, jobs, MCP, app runtime
                  |
                  | outbound authenticated WebSockets
                  v
             Hank Agents
        +---------+----------+
        |         |          |
 Home Assistant  files    managed machines
                 / SMB    and remote desktop
```

HankServerside is the public platform boundary. Clients do not connect directly
to Home Assistant, SMB, local filesystems, or privileged machine services.

## Deployment Model

The supported production model is one self-hosted deployment for one Home. The
deployment may contain multiple users and multiple agents. It is not a
multi-home SaaS control plane and does not assume multiple cloud nodes.

HankServerside, PostgreSQL, and the database-operations service normally run on
one Compose host behind an HTTPS reverse proxy or tunnel. Hank Agents run on
machines that own local resources and connect outbound to HankServerside.

## HankServerside

The cloud side owns:

- authentication, sessions, invitations, roles, permissions, and CSRF
- singleton-Home management and agent enrollment
- agent routing, relay, realtime events, and durable jobs
- browser, client, MCP, agent, and remote-desktop APIs
- the dashboard/PWA and authenticated feature surfaces
- server-side authorization for every home-, user-, agent-, and token-scoped
  operation
- PostgreSQL persistence, migrations, retention, backup/restore coordination,
  readiness, metrics, logs, and audit events
- the generic `.hankapp` package/runtime contract

## Assistant Execution Storage

The reserved v2 engine uses PostgreSQL tasks, steps, approvals, operation
receipts, follow-up inputs, and ordered events. Task ownership is tied to the
session's Home/user scope by a composite foreign key. Submission keys deduplicate
requests. Worker leases use fencing generations and revision checks; checkpoints
and events commit in the same transaction. Restart recovery retains pending
calls, observations, approvals, and consumed budgets.

Approvals bind an exact action digest and expire. Local domain writes can commit
with their completion receipt in one transaction. Remote dispatch intent is
recorded before delivery; an existing accepted/unknown receipt requires
reconciliation rather than another write. The destination journal uses private
files, exclusive process ownership, file/directory sync, and atomic replacement.
It never permits a second first-attempt dispatch after reopen.

A runtime worker recovers durable tasks independently of HTTP requests. New v2
admission is opt-in and uses the existing Ollama/OpenAI configuration. The read
loop persists native tool calls before invoking adapters, reauthorizes retained
observations, and publishes messages atomically with terminal/waiting state.
Completion and clarification use explicit model control tools; unclassified
prose gets at most two durable repair turns before failing. These decisions
are not inferred from words in the answer.
Session adoption prevents mixed v1/v2 writes and permits one active task per
session. Stop and follow-up fence the old worker and cancel its provider I/O.
Write adapters and agent operation capabilities remain gated. See
[HankAI](hankai.md) for client behavior and provider limitations.

## Hank Agents

Agents authenticate with scoped credentials and maintain outbound WebSockets.
One primary agent owns default home capability routing. Worker agents represent
additional managed machines and receive only explicitly targeted or
capability-routed commands.

The demo fleet API delegates explicit machine operations through approved user
grants. Agent enrollment credentials remain separate. Workspaces and durable job
metadata bind the grant and exact agent; output stays on the agent and reconnects
reconcile state without repeating execution. The local MCP bridge consumes these
same HTTPS contracts. See [fleet demo](fleet-demo.md) for limits and authority.


Agents translate platform commands into local Home Assistant, filesystem, SMB,
notes, media, configuration, telemetry, package, process, service, shell, and
remote-desktop operations. They hold local credentials and must enforce path,
capability, permission, and process boundaries before touching local resources.

## Clients And Interfaces

The React dashboard is the first-party Hank interface and is embedded into the
cloud binary at build time. Its installable PWA uses the same origin, sessions,
authorization, routes, and APIs. Offline support is confined to the documented
local-first Notes and Kanban workspace.

Native, mobile, desktop, command-line, and future clients use the same stable
HTTPS/WebSocket surface. The optional MCP endpoint provides a scoped tool
interface for supported AI clients and project-context integrations.

## Data And Persistence

PostgreSQL is authoritative for users, homes, members, agents, sessions,
permissions, notes, assistant state, apps, tokens, jobs, notifications, remote
desktop metadata, audit events, and operational state. Schema changes use the
embedded versioned migration workflow.

Managed server storage holds artifacts whose bytes do not belong in PostgreSQL,
including note attachments, safe previews, staging data, backup state, and
bounded validation evidence. Database rows retain ownership, authorization,
integrity, and lifecycle metadata.

Local resource bytes and credentials stay with their owning Hank Agent unless a
specific authenticated transfer or managed storage workflow moves them.

## Protocol And Routing

Clients use JSON over HTTPS and authenticated WebSockets. Agents use the
versioned JSON envelope in `internal/protocol`, with request IDs, explicit
commands/events, structured errors, and correlation for responses and
long-running work.

Untargeted home commands route to the primary agent. Machine-scoped operations
may target a worker agent by ID. The router validates the authenticated
connection, Home, stored agent type, target, and capability before delivery.
Registration, replacement, heartbeat, and disconnect serialize presence updates.
Only the current registered connection may refresh runtime health; teardown of
an older or unregistered socket cannot mark its replacement offline. Outbound
WebSocket writers honor context cancellation while waiting for another writer.

File transfers, managed jobs, notifications, and remote-desktop sessions use
durable server state so reconnects and process restarts do not silently lose
their lifecycle.

## Core Services

Core platform services include Home Assistant, files, Notes and Kanban, media,
assistant sessions and tools, notifications, members and permissions, agent
management, storage operations, backup/restore, remote desktop, audit, logs,
readiness, and metrics. Optional apps may consume stable platform capabilities
but cannot replace these services.

## Installable App Runtime

HankServerside imports and validates `.hankapp` packages, stores installation
and configuration state, renders manifest-driven settings, enforces access
modes, and routes app invocation to the owning agent runtime. Package archive
containment, runtime paths, schemas, secrets, file-source bindings, command IDs,
and access modes are compatibility-sensitive.

## Trust Boundaries

- Public traffic terminates at HankServerside over HTTPS.
- Browser writes require authenticated sessions and CSRF protection.
- Bearer tokens are hashed or encrypted as appropriate and never belong in URL
  query strings.
- Agent credentials authenticate one exact agent and Home.
- Local credentials remain on agents; server-side saved secrets use the
  configured encryption helpers.
- File and attachment paths are cleaned, symlink-resolved, and contained.
- High-impact administrative actions use explicit confirmation and the existing
  short-lived action-token pattern.
- Remote-desktop content is end-to-end encrypted; the server relays opaque data
  while retaining only required lifecycle and routing metadata.
- Logs and audit records exclude tokens, passwords, private file contents,
  screen/input/clipboard content, and raw encrypted payloads.

## Availability And Operations

`/healthz` reports process health and `/readyz` reports dependency readiness.
Sensitive metrics require administrator authentication or a dedicated scrape
token. PostgreSQL migrations, schema drift, secret-storage status, backup
freshness, restore proof, agent health, and release evidence have explicit
operator checks documented in the deployment guide, runbooks, and release gate.

## Deliberate Non-Goals

- exposing raw SMB, Home Assistant, local filesystems, or machine protocols to
  the public internet
- requiring a VPN for normal product use
- client-specific networking or persistence implementations in this repository
- weakening stable contracts to preserve client workarounds
- multi-home SaaS or multi-node cloud clustering
- moving core services into optional apps
