# Hank Installable App Platform

Date: 2026-06-18

## Decision

Treat HankServerside as the canonical Hank platform. Treat Hank apps as
installable extensions that run on top of that platform. Keep `.hankapp`
compatibility strict so optional workflow development can move independently
without destabilizing core services.

## Runtime Boundary

`HankServerside` owns the platform responsibilities:

- account auth, sessions, roles, permissions, and CSRF behavior
- the single-home cloud-and-agent model
- cloud/agent routing, WebSocket relay, and protocol envelopes
- Home Assistant, files, notes, media, backup, restore, storage operations, and dashboard infrastructure
- database migrations, schema drift checks, observability, audit, and recovery workflows
- the generic app import, configuration, permission, invocation, and Settings > Apps runtime

Hank apps own optional workflows:

- slash-command workflows
- local-home integrations that can be enabled, disabled, upgraded, or removed independently
- focused tools such as Hermes, Gramaton, or YDownload-style packages
- behavior that can fail without breaking auth, routing, files, notes, Home Assistant, backup, or the assistant shell

Do not move core Hank responsibilities into apps just to make feature development easier. If an app needs new stable platform capability, add that capability to the runtime contract first, then let apps consume it.

## Compatibility Contract

The `.hankapp` package format is a compatibility surface, not an internal implementation detail. Changes to the app runtime must preserve existing valid packages unless there is an explicit migration plan.

Compatibility-sensitive surfaces include:

- `schema_version` values such as `hank.app.v1`
- `schemas/hank-app-v1.schema.json`, which external app repos should use for
  local manifest validation
- manifest fields in `app.json`
- the app folder / `.hankapp` archive layout, including root-level `app.json`
- package archive validation rules
- command IDs, slash-command declarations, and reserved built-in command names
- `apps.list`, `apps.package_preview`, `apps.package_activate`, `apps.uninstall`, `apps.config_status`, `apps.config_apply`, and `apps.invoke`
- Settings > Apps schema rendering
- `config.settings_schema`, `secret_fields`, and secret-preservation behavior
- file-source bindings such as `source: "file_sources"`, path-field `source_field` dependencies, and matching `permissions.files` entries
- access mode behavior for `admins_only` and `home_members`

Breaking changes require all of the following:

- a new schema version or compatibility adapter
- package/runtime tests for the old and new behavior
- docs updates for package authors and operators
- a migration or repackage path for existing first-party apps

## Development Rules

Use app packages for optional or experimental workflows when the core runtime already exposes the needed stable capability.

Use core runtime changes when work affects auth, routing, database shape, cloud/agent protocol, file safety, Home Assistant access, notes, backup/restore, assistant sessions, operator setup, or shared dashboard infrastructure.

Keep app-specific settings inside Settings > Apps through manifest-driven schema rendering. Do not add bespoke Settings panes for individual apps unless the platform contract is intentionally being expanded.

Keep first-party apps aligned with the contract. When the contract changes, update the runtime, manifest validation, package docs, package examples, and existing first-party packages together.

## Package Layout

Settings > Apps accepts either a prebuilt `.hankapp` archive or a selected app
folder. The app folder is packaged in the browser before upload. The selected
folder name is stripped while packaging, so `my-app/app.json` becomes
`app.json` at the archive root.

The folder or archive root must contain `app.json`:

```text
my-app/
  app.json
  bin/my-app
  schemas/config.schema.json
  schemas/run.input.schema.json
  schemas/run.output.schema.json
  README.md
```

`app.json` must use `schema_version: "hank.app.v1"`. `runtime.command` must be
a clean relative path to a file in the package, such as `bin/my-app`. Every
schema path referenced by `config.schema`, `commands[].input_schema`, or
`commands[].output_schema` must exist in the package and be a valid JSON object.

Package paths must be clean relative paths. Do not use absolute paths, `../`,
`.` path segments, double slashes, backslashes, Windows drive paths, symlinks,
or duplicate archive paths. Settings > Apps skips `.DS_Store` and `__MACOSX`
entries when packaging a folder.

The current `hank.app.v1` stdio runtime supports `runtime.type` and
`runtime.command`. It does not support `runtime.args`; package authors should
wrap arguments in the app executable or add runtime support before depending on
manifest-provided args.

## Authoring Validation

External app repositories should validate `app.json` against
`schemas/hank-app-v1.schema.json` before building a `.hankapp` archive. Passing
that JSON Schema is a package-author check, not a replacement for HankServerside
runtime validation. The runtime validator remains authoritative because it also
checks cross-field rules such as slash-command command references, reserved
commands, package path containment, default/option compatibility, and supported
permission semantics.

A `path` settings field may declare `source_field` to reference a
`select` field whose source is `file_sources`. Settings > Apps then renders an
existing-directory picker scoped to that selected primary-agent file source.
The picker supports the source root and arbitrary nested directories; it does
not create directories. Changing the referenced source clears the dependent
path. Path fields without `source_field` retain their text-input behavior.

An app is install-ready only when its own tests, build, package step, manifest
schema validation, and HankServerside package validation succeed and produce a
non-empty `dist/<id>.hankapp` artifact. Settings > Apps imports that archive;
source directories and repository-specific build tooling are not part of the
runtime compatibility contract.

Settings > Apps also supports confirmed uninstall for home admins. Hank first
removes the package directory, local configuration, and stored secrets from the
online primary Hank Agent through `apps.uninstall`; only after the agent returns
the matching app ID does Hank remove the Home's persisted app metadata. A failed
or offline-agent uninstall leaves the cloud record intact so the operation can
be retried safely.

## Invocation Lifecycle

Restricted execution is mandatory for both ordinary commands and `settings_apply`.
The agent advertises `apps.sandbox.v1`; the cloud refuses execution and configuration
on older agents. App management is administrator-only through both HTTP and
WebSocket entry points. A shared app's `commands[].admin_only` remains enforced
in the dashboard and assistant.

In assistant execution v2, an installed-app slash command prepares an exact
app/version/command/request review before invoking this same runtime. Persisted
dispatch intent prevents automatic replay after interruption. App responses are
attributed to the app; opaque effects remain unverified. The model cannot invoke
an installed app without the user's explicit slash selection. Ordinary app API
and legacy conversation contracts retain their existing behavior.

On Linux amd64 and arm64, bubblewrap runs an immutable package copy over a dedicated
runtime filesystem in separate user, mount, PID, IPC, network and UTS namespaces.
The process runs as UID/GID 65534 with no capabilities. Host environment variables,
agent state, host `/proc`, raw file sources and host Unix sockets are absent.
The package and runtime are read-only; private temporary filesystems are bounded.
Seccomp forbids creating more namespaces, direct internet sockets, host-process
inspection and alternate kernel I/O APIs. Commands have bounded output and deadlines;
at most eight invocations run concurrently. Each invocation enters a dedicated
cgroup before its first instruction: 512 MiB memory, no swap, 64 processes/threads
and one CPU core of sustained time. Ending an invocation kills its PID namespace
and cgroup, including detached descendants. Unsupported operating systems, missing
runtime files, unavailable bubblewrap, undelegated resource controllers or blocked
namespaces fail closed. There is
no unrestricted fallback.

An incompatible installed manifest is excluded individually and reported at load;
other valid installed packages continue to load. Its files are preserved for
administrator recovery.

## Resource Grants

Settings > Apps displays each configured URL and file source requested by the
manifest. Grants start empty and are independent of the Home-member access setting.
An administrator explicitly allows network access, file reads, or file creation
and updates. File deletion is not part of this broker contract. Save target changes
with the app disabled, then reopen configuration to review the resolved targets
and enable it. Unresolved targets cannot receive a grant.

Each grant binds the app ID, package contents, permission field, exact target and
resolved scope. Network grants include resolved IP addresses; requests use those
addresses and cannot follow redirects or leave the configured origin/path. New
DNS answers require another review. File grants include a captured source and its
policy, so changing a source ID's configuration cannot redirect an approved
operation. Existing file containment and access policies still apply. The agent
owns SMB credentials; they are never exposed to the app. An app receives only its
own configured secrets through the established stdio contract.

Package updates clear grants. Target or policy changes invalidate the corresponding
grants. Revocation skips app-controlled settings validation, cancels running
invocations and prevents reuse of cached broker scopes. Agent grants are private
files beside the app directory in `apps-permissions`, outside every package mount.
Migration 40 adds an object-constrained `permissions_json` cache for cloud display;
it does not grant authority by itself. Rolling it back removes that cache, not the
agent's restrictions. Do not downgrade the agent to restore unrestricted execution.

Apps cannot emit core platform events. The current manifest contract does not
support event grants; responses containing events are refused. Poll app command
results where needed until a versioned, namespaced event contract exists.

## Broker Adapter and Package Migration

Computation-only `hank.app.v1` packages keep the existing `hank.app.stdio.v1` input
and output contract. Packages that previously opened network connections or host
paths must be repackaged to use `hank.app.broker.v1`. The descriptor is supplied as
`HANK_APP_BROKER_FD`; it carries sequential newline-delimited JSON requests with
`version`, `id`, `permission` and `operation`. Responses echo `id` and contain
`ok` with either `output` or a bounded generic `error`. Requests and responses are
limited to 1 MiB, with at most 1024 operations per invocation. Binary `data` uses
base64. App request/response bodies and arbitrary app errors are excluded from
relay history.

Supported operations:

| Permission ID | Operations | Limits |
| --- | --- | --- |
| `network:<field>` | `http.request` with an absolute `url`, HTTP `method`, optional `headers` and `data` | Exact reviewed origin/path and pinned IPs; no redirects, proxies or Host override; 256 KiB request body, 512 KiB response body, 20 seconds |
| `files_read:<field>` | `files.list`, `files.stat`, `files.read` with source-relative `path` and optional byte `offset` | Existing source read policy; reads return at most 256 KiB and total file size |
| `files_write:<field>` | `files.write` with `path`, `offset`, base64 `data`; `files.mkdir` | Existing source write policy and upload limit; at most 256 KiB per write; no delete operation |

The [Python adapter](../examples/restricted-app/bin/hank_broker.py) and
[example package](../examples/restricted-app/app.json) provide a migration starting
point. Copy the adapter into a package's executable directory and replace direct
HTTP/file calls with the corresponding broker operations. Bundle application
Python dependencies in the package; the dedicated runtime includes Python 3,
BusyBox and public CA certificates, not the agent's host tools or Python packages.
Validate and repackage each separately owned first-party app in its owning
repository before enabling it under the restricted runtime. Production runtime
prerequisites are documented in [deployment](deployment.md).
