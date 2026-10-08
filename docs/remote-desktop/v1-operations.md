# Native Remote Desktop V1 Operations

Remote Desktop is end-to-end encrypted between the approved browser operator and the signed endpoint host. The cloud relays opaque binary records and retains metadata only.

Use this runbook for current V1 readiness, trust, session, package, permission,
and incident operations. It does not authorize installation on a physical
machine or relax the evidence requirements in
[V1 acceptance](v1-acceptance.md).

## Readiness and sessions

Before starting, confirm endpoint identity trust, platform service/daemon and signed host compatibility, local indicator availability, capture/control permissions, and that no operator is active. A session joins within 60 seconds, may reconnect for at most 90 seconds, and ends at authorization plus eight hours. End Session revokes relay access immediately.

## Metrics and alerts

Use the fixed-cardinality `hank_desktop_*` metrics for state, join/reconnect outcomes, termination reasons, opaque relay byte counts/backpressure, and platform readiness. Never add home, user, device, agent, session, address, user-agent, display, input, clipboard, ciphertext, or free-form reason labels. Investigate join-auth alerts as possible credential replay, backpressure as a quality/network issue, readiness loss at the endpoint, and capacity alerts before authorizing new work.

## Trust operations

- Approval requires comparing the displayed fingerprint with the local endpoint/device value.
- Revocation ends affected live sessions. Unexpected identity changes stay blocked until explicitly compared and approved.
- Recovery uses the offline code locally to decrypt the recovery envelope and enroll a new non-exportable operator. Password reset is not cryptographic recovery.
- Rotation requires old-root proof, creates a new offline code, revokes the old generation, and requires endpoint re-enrollment.
- Reset requires typing `reset desktop trust`; it revokes all identities and sessions. Lost recovery code plus no active root-authorized operator requires reset.

## Endpoint failures

On macOS, Screen Recording gates viewing and Accessibility gates control. Permission loss pauses and clears held input; restore locally and reconnect with fresh keys. On Windows, UAC/secure-desktop capability requires the installed LocalSystem service and authenticated signed host. A daemon/service/host mismatch or repeated helper failure is fail-closed; use the signed package rollback procedure.

## Backpressure, upgrades, and rollback

Quality downgrades before the relay terminates a consumer that remains blocked for ten seconds. Check RTT, decoder/sender queues, dropped frames, and fixed backpressure counters; never inspect frame contents. Packages keep GUI, authority, host, IPC major, and protocol version as one unit. Stop active sessions for upgrade, verify identity/credential continuity and readiness, and restore the prior signed package if atomic installation fails.

## Incident evidence

Collect timestamps, platform/package/protocol/IPC versions, fixed readiness keys, state transitions, actor type, stable reason code, epochs, aggregate bytes, metrics, and redacted service logs. Do not collect screenshots, video, raw relay frames/ciphertext, clipboard, key/pointer input, credentials, private keys, recovery codes, or file contents.
