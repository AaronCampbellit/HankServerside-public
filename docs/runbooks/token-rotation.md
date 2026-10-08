# Runbook: Token And Secret Rotation

Rotate one credential family at a time. Keep the previous value available until
the replacement is verified, except when responding to an active compromise.
Never print secret values in command output or place them in shell history,
documentation, evidence, tickets, or logs.

## Agent Credentials

1. In **Settings → Agents**, create or rotate the credential for the exact Hank
   Agent.
2. Install the generated configuration with `scripts/install-agent-env.sh` or
   the managed credential-rotation flow.
3. Restart only that agent.
4. Verify the same agent ID reconnects, reports expected type/capabilities, and
   completes `system.ping` plus one safe owning-service operation.
5. Revoke the previous token only after the new connection is healthy.

Rollback: restore the previous agent configuration only while its token remains
valid. If it was revoked, issue another replacement; do not re-enable a known
compromised credential.

## Database-Operations Intent Secret

`HANK_DB_OPS_INTENT_SECRET` authenticates requests between the cloud and
database-operations worker.

1. Stop `cloud` and `db-ops` so the two processes cannot disagree.
2. Replace the value in `.env.cloud` without printing it.
3. Start both services.
4. Run `scripts/doctor.sh` and request one manual backup.
5. Verify the intent is accepted once and a terminal backup event is recorded.

Rollback: stop both services and restore the previous value. Never run mixed
old/new processes.

## pgBackRest Repository Cipher Passphrase

`HANK_DB_OPS_REPO_CIPHER_PASS` encrypts repository contents and cannot be
changed in place like an API token.

1. Plan a new repository and retain the old repository/passphrase read-only.
2. Configure the new passphrase and initialize the new repository.
3. Take a new full backup.
4. Complete `scripts/restore-proof.sh` from the new repository.
5. Retain the old repository through the approved recovery window before
   securely retiring it.

Rollback: restore the old repository configuration and passphrase. Do not delete
the old repository until the new restore proof passes.

## Server Secret-Encryption Key

`HANK_SECRET_ENCRYPTION_KEY` encrypts stored application secrets. There
is no routine in-place rotation workflow.

Before any future rotation, implement a migration that can decrypt every known
row with the old key, re-encrypt atomically with the new key, verify all rows,
and roll back safely. Until that migration exists, keep the key stable and
backed up through the deployment's protected secret-management process.

If the key is believed compromised, treat rotation as an incident: restrict
access, preserve the database and key for controlled migration, rotate each
upstream credential after re-encryption, and verify with:

```bash
docker compose --env-file .env.cloud run -T --rm \
  --entrypoint /usr/local/bin/hank-server cloud secrets status --strict
```

## OpenAI And ChatGPT Credentials

- OpenAI API key: replace `HANK_OPENAI_API_KEY`, restart `cloud`, select
  the provider in AI Settings, send a non-sensitive test message, then revoke
  the old provider key.
- Linked ChatGPT/Codex account: unlink/relink through the supported device-code
  flow, verify `/v1/oauth/openai/status`, and send a test message. Revocation at
  the provider is the rollback/containment path.

Verify logs contain provider/status metadata only, never the token or prompt
content.

## Microsoft Entra Client Secret

1. Create a replacement secret in Entra without deleting the old one.
2. Save it through **Settings → Home & Agent Setup** or replace
   `HANK_ENTRA_CLIENT_SECRET` when the environment is the active source.
3. Restart `cloud` only when changing the environment fallback.
4. Complete a new private-browser Entra sign-in and verify the immutable tenant
   and object-ID mapping reaches the correct Hank user.
5. Delete the former Entra secret after verification.

Rollback: restore the previous secret while it remains valid.

## APNs Credentials

1. Replace the APNs key ID/private key and confirm the configured topic and
   environment.
2. Restart `cloud`.
3. Re-register a test native device and trigger a redacted notification.
4. Verify delivery outcome logs contain no token or payload secrets.
5. Revoke the former APNs key after successful delivery.

Rollback: restore the former key ID/private key while the provider still
accepts it.

## Web Push VAPID Keys

Rotating the VAPID key pair invalidates existing subscriptions.

1. Generate a matching P-256 VAPID public/private pair.
2. Replace `HANK_WEB_PUSH_VAPID_PUBLIC_KEY`,
   `HANK_WEB_PUSH_VAPID_PRIVATE_KEY`, and
   `HANK_WEB_PUSH_VAPID_SUBJECT` together.
3. Restart `cloud` and run `scripts/doctor.sh`.
4. Re-enroll a test browser from Notification Settings and prove foreground,
   background, click-through, read-state, category suppression, and logout
   cleanup.
5. Tell users to re-enable browser delivery; old subscriptions cannot be
   migrated to the new application-server key.

Rollback: restore the complete former key pair and subject; browsers enrolled
against the temporary replacement may need another enrollment.

## Service-Profile Secrets

Use the owning dashboard settings flow for Home Assistant, SMB/file sources,
media, and other agent-managed integrations.

1. Save the replacement secret for the exact service/profile.
2. Verify the intended primary or worker agent applies the new configuration
   version.
3. Run a safe health/read/list operation through HankServerside.
4. Revoke the old upstream credential.

Rollback: restore the former upstream credential through the same settings flow
while it remains valid.

## Metrics Scrape Token

1. Generate a new high-entropy `HANK_METRICS_SCRAPE_TOKEN`.
2. Update `.env.cloud` and the Prometheus scrape secret as one maintenance
   change.
3. Restart `cloud` and reload/restart Prometheus.
4. Verify an unauthenticated `/metrics` request is rejected and the configured
   scraper succeeds.
5. Confirm alert evaluation resumes without gaps.

Rollback: restore the former token on both sides and restart/reload both
consumers.

## Finish Checks

- `scripts/doctor.sh` passes for the deployed scope.
- Agent, provider, service, or scraper health proves the replacement value.
- The previous credential is revoked or its retained rollback window is
  documented.
- Logs, audit events, process arguments, and evidence contain no secret values.
