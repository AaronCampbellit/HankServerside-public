# Native Remote Desktop V1 Acceptance

This is the current release-evidence contract for native remote desktop V1.
Portable implementation and contract checks are summarized below; release
readiness remains blocked until every explicitly listed physical-device,
release-credential, packaging, and duration gate has current evidence.
Contract-only scripts execute explicit mappings and write machine-readable
receipts marked `native_evidence:false`; they never claim physical acceptance.

## Portable evidence

- Adaptive trace: browser, Swift, and .NET implement the same canonical levels, downgrade cooldown, healthy hysteresis, reconnect reset, generation/keyframe, and manual cap. Authenticated relay pressure reaches the endpoint over the agent control channel, immediately lowers the encoder before slow-consumer close grace, and is reported as resettable per-statistics-window metadata rather than a lifetime-unhealthy counter.
- Relay: process/home/agent budgets, frame/rate/queue/time limits, slow-consumer handling, fixed-cardinality metrics/alerts, isolation tests, and transactional 24h/180d/365d retention.
- Trust and audit: recovery code/checksum, encrypted recovery envelope, non-exportable identities, approval/revoke/recover/rotate/reset journeys, metadata-only readiness/history.
- Database: all migrations through `000021`, strict migration status, and direct schema drift passed against Hank demo's isolated disposable PostgreSQL database. A separate isolated PostgreSQL run proved that 1,000 late terminal replays do not grow persisted history while relay revocation is still applied; each database and tunnel was removed afterward. Deep local Compose/doctor validation remains unavailable because Docker and Docker Compose are not installed.
- Windows: all 152 Release .NET tests pass with zero skips, including installed-manifest/WiX/service/host contracts. MSI bind remains Windows-only (`wix.exe` rejects macOS), so the disposable-VM artifact and upgrade gate remains open.
- macOS: Release Swift build and all HankKit, daemon, and packaging selftests pass; the ad-hoc signed unsigned-test pkg embeds the daemon, exact identifiers, rendered narrow Keychain entitlements, designated requirement, payload inventory, hashes, compatibility metadata, and registration UI. The daemon derives its credential access group only from its signed `keychain-access-groups` entitlement; the GUI has no matching group and runtime Team-ID/environment fallbacks are rejected by packaging tests.

## Required External Evidence Before V1 Release

- [x] Implement first-transfer/re-enrollment coordinators on both platforms. A signed privileged bootstrap works before machine configuration exists, atomically creates provisional configuration and the machine credential, installs the exact approved endpoint certificate, validates an exact server/agent/fingerprint readiness receipt, and creates the configured marker only at commit. Failure after any stage stops the provisional authority, removes credential/readiness/configuration markers, and immediately restores the legacy GUI worker for retry. Installed-device evidence remains required below.
- [x] Native receipt ingestion structurally parses one JSON object, rejects duplicate/contradictory reserved fields and non-boolean evidence flags, and canonicalizes accepted metadata before it reaches the JSONL evidence file.
- [x] The macOS rollback harness builds a dedicated component package containing a verified failing `postinstall`; the portable package build expands and validates both that script and the replacement app payload before any disposable-VM install.
- [ ] Packaged Windows disposable VM: N to N+1, forced failure rollback to N, uninstall/retention flags, LocalSystem service recovery, DPAPI/CNG continuity, signed host validation.
- [ ] Signed/notarized macOS disposable VM: N to N+1, forced failure rollback, uninstall, SMAppService registration, root ownership, Keychain continuity, permissions/readiness continuity.
- [ ] Eight-hour native longevity on both platforms with three reconnects, display changes, lock/unlock, degradation/recovery, local input, hard expiry, and resource baseline recovery.
- [ ] Eleven-point physical-console matrix: exact console; concurrent local/remote input; view-only; clipboard both directions; fresh-key reconnect; local indicator/termination; three termination actors; relay opacity; content-free audit; Windows elevated/UAC; macOS permission loss/recovery.
- [ ] Compatibility matrix: current/current, supported prior `desktop.v1`, incompatible IPC rejection, upgrade/rollback, service/daemon and server restart, identity rotation, trust reset/re-enrollment.
- [ ] Release signing/notarization credentials and separate authorization to submit/install/publish.

Run `scripts/remote-desktop-acceptance.sh --contract-only` for mapped portable contract evidence. On authorized physical test devices, configure the metadata-only native driver and run the script without `--contract-only`; store generated evidence in the configured untracked evidence directory.

## Evidence Content Boundary

Evidence may contain timestamps, platform and package versions, protocol/IPC
versions, fixed readiness keys, stable state/reason codes, actor types, epochs,
durations, aggregate bytes, test case IDs, and pass/fail/skip results. It must
not contain screenshots, video, raw frames or ciphertext, window titles,
process names, clipboard contents, key/pointer input, credentials, private keys,
recovery codes, request bodies, or private file/note contents.

## Release Decision

V1 is not release-ready while any unchecked external row remains. Portable
success cannot waive physical-console, signing/notarization, packaged upgrade,
rollback, uninstall, permission/elevation, longevity, compatibility, or
resource-baseline evidence. `RELEASE.md` is the go/no-go gate.
