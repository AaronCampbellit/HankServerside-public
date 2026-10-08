# Hank Product

## Product Identity

Hank is a self-hosted platform that gives people one secure place to work with
their home services, files, notes, managed machines, automation, and assistant.
HankServerside is the canonical backend and platform repository. It owns shared
behavior, durable state, security boundaries, compatibility contracts, the web
experience, and the runtime used by every Hank client and extension.

Hank is not an iOS app with a supporting server. Native and mobile clients may
provide excellent Hank experiences, but they consume the platform rather than
define it.

## Product Surfaces

- The Hank dashboard is the first-party browser interface served by
  HankServerside.
- The installable PWA uses the same dashboard and authentication model, with a
  deliberately bounded offline Notes and Kanban workspace.
- Native, desktop, mobile, CLI, and future clients use stable HankServerside
  HTTPS and WebSocket contracts.
- MCP integrations expose explicitly scoped Hank tools and context.
- Hank Agents connect managed machines and private-network resources.
- Optional Hank apps add independently installable workflows.

## Core Platform Capabilities

HankServerside owns identity, permissions, homes, agents, routing, persistence,
Home Assistant, files, notes, Kanban, assistant state, notifications, storage
operations, machine management, remote desktop, observability, and operator
workflows. These services remain part of the platform even when optional apps
or clients are unavailable.

PostgreSQL is the durable source of truth for server state. Versioned migrations
own schema changes. Managed file storage holds bounded artifacts such as note
attachments and operational evidence where the corresponding metadata and
authorization remain server-owned.

## Hank Agents

A Hank Agent is installed on a machine and connects outbound to HankServerside.
Agents hold machine-local credentials, enforce local capability boundaries, and
translate stable platform commands into local operations.

Each Home has one primary agent for default home services. Additional worker
agents represent other managed machines. Commands can target an exact worker;
untargeted home commands retain the primary-agent behavior.

The isolated Linux fleet demo lets an approved user client provision workspaces,
transfer files and coordinate bounded jobs across explicitly selected machines.
CLI and MCP access use revocable user grants; enrollment alone grants no access
to other machines. See [fleet demo](fleet-demo.md).


## Clients And Integrations

Clients authenticate to HankServerside and use versioned platform contracts.
They do not reimplement server persistence, authorization, local-network
protocols, or agent routing. When a client conflicts with an established
contract, the client changes unless product direction explicitly requires a
versioned platform migration.

## Installable Hank Apps

`.hankapp` packages are optional extensions loaded through the platform's
versioned app runtime. They may add focused workflows, settings, and assistant
commands. They do not replace core services or bypass platform authorization,
file-source, secret, or package-containment rules.

The package format, manifest schema, settings renderer, access modes, and
`apps.*` command family are compatibility surfaces.

## Deployment Scope

The production target is one self-hosted deployment for one Home. A deployment
may manage multiple users and multiple Hank Agents, but it is not a multi-home
SaaS control plane or a clustered cloud service.

Hank Agents connect outbound. Public clients reach HankServerside over HTTPS and
authenticated WebSockets. Raw SMB, Home Assistant, local filesystems, privileged
machine APIs, and other private protocols are never exposed directly to the
internet. A VPN is not required by the architecture.

## Product Boundaries

- HankServerside owns shared contracts and platform behavior.
- Hank Agents own access to local resources and credentials.
- Clients own interface-specific presentation and operating-system integration.
- Optional apps own removable workflows above stable platform capabilities.
- Breaking API, protocol, or `.hankapp` changes require a new version or a
  documented compatibility path.
- Local credentials and private data remain within the narrowest owning
  boundary.

## Current Priorities

1. Keep HankServerside as the source of truth for shared Hank behavior.
2. Strengthen the dashboard/PWA as a complete first-party Hank experience.
3. Make Hank Agent installation, enrollment, health, and managed-machine
   operations reliable.
4. Preserve authentication, authorization, secret handling, file containment,
   backup safety, and auditability ahead of convenience.
5. Protect stable client, agent, MCP, remote-desktop, and `.hankapp` contracts.
6. Complete release-readiness evidence and keep operator workflows repeatable.
7. Keep current documentation aligned with the code that actually ships.

## Canonical technical identifiers

Server-side binaries, environment variables, database defaults, metrics,
cookies, recovery exports, notification events, and deployment paths use the
canonical Hank naming scheme. Retired names are not runtime aliases. Existing
operators use the one-time [hard naming migration](naming-migration.md), which
fails on conflicting paths or database identifiers instead of merging data.

The signed standalone HankAgent product, its `hankagent.service`, artifacts,
and update paths retain their established names. Compose service names remain
`cloud`, `agent`, and `db-ops`; environment files remain `.env.cloud` and
`.env.agent`; protocol identifiers such as `cloud.command`, `cloud.response`,
and `X-Hank-Agent-ID` are unchanged. MCP's connector terminology also remains
part of its integration model.
