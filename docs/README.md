# Hank Documentation

This directory contains current documentation for HankServerside. Start with
the product and architecture documents, then follow the owning reference for
the system you are changing or operating.

## Start Here

- [Repository overview](../README.md)
- [Portfolio screenshot provenance](screenshots/README.md)
- [Product identity and scope](product.md)
- [Architecture](architecture.md)
- [Deployment and setup](deployment.md)
- [Hard naming migration](naming-migration.md)
- [Release checklist](../RELEASE.md)
- [Active audit remediation](audit-remediation.md)

## Active Plans


- [Hank assistant execution plan](../plans/hank-assistant-execution.md) — active implementation
  milestones for the model/tool loop, durable tasks, approvals, context,
  evaluations, and scheduled work; canonical feature docs describe enabled behavior.

## Platform And Development

- [Linux fleet demo](fleet-demo.md)
- [API and protocol overview](api.md)
- [Security model](security.md)
- [Agent change rules](../AGENTS.md)
- [Demo and staging validation](demo-validation.md)

## Contracts And Features

- [Installable app platform contract](app-platform.md)
- [MCP integration](mcp.md)
- [Notes and Kanban API](notes.md)
- [Hank and file search](search.md)
- [Dashboard PWA and offline boundary](pwa.md)
- [HankAI providers, retrieval, and evaluation](hankai.md)
- [Remote desktop V1 acceptance](remote-desktop/v1-acceptance.md)
- [Remote desktop operations](remote-desktop/v1-operations.md)

## Operations

- [Agent offline](runbooks/agent-offline.md)
- [Authentication failures](runbooks/auth-failures.md)
- [File-transfer failures](runbooks/file-transfer-failures.md)
- [Home Assistant failures](runbooks/home-assistant-failures.md)
- [PostgreSQL 17-to-18 upgrade](runbooks/postgres-17-to-18.md)
- [Single-host Compose](runbooks/single-host-compose.md)
- [Storage failures](runbooks/storage-failures.md)
- [Token and secret rotation](runbooks/token-rotation.md)

## Release Evidence

The release gate links current evidence contracts and commands. Evidence
documents state what must be proven; generated reports and private/native
artifacts remain untracked.

- [Release checklist](../RELEASE.md)
- [Demo validation](demo-validation.md)
- [Remote desktop V1 acceptance](remote-desktop/v1-acceptance.md)

## Documentation Policy

- Active documentation describes current behavior and supported operations.
- One document owns each subject; other documents summarize and link.
- Every behavior claim must be traceable to current code, schema, configuration,
  script, test, or release requirement.
- Product prose and current runtime instructions use Hank, HankServerside, Hank
  Agent, and the canonical identifiers in the hard naming migration guide.
  Retired identifiers appear only where that guide explains an upgrade.
- Completed plans, superseded checklists, audits, and implementation diaries are
  removed after durable guidance is incorporated. Git history is the archive.
- Temporary planning directories are excluded from HankAI project-document
  discovery and are removed after execution.
- Changes to routes, configuration, schemas, compatibility contracts,
  deployment, security boundaries, or product ownership update their owning
  document in the same change.
