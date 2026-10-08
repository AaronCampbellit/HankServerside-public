# Runbook: Agent Offline

Use this when client requests fail with `agent_offline` or HankServerside shows
the required primary or worker Hank Agent as offline.

## Check

1. Verify `GET /readyz` still reports storage as ready.
2. Check `/metrics` for the current `hank_online_agents` value.
3. Confirm the Hank Agent process is running on the owning machine.
4. Confirm the agent can reach the cloud `wss://.../ws/agent` URL outbound.

## Common Causes

- invalid or revoked agent token
- expired agent token
- cloud URL mismatch
- local network outage
- agent process crash
- agent binary predates the header-auth migration (see below)

## Agents Older Than The Header-Auth Migration

Agent auth moved from URL query credentials to `Authorization: Bearer` plus
`X-Hank-Agent-ID` headers, and the query fallback was removed. An agent built
before that migration is rejected on every connect and shows as permanently
offline after a cloud upgrade.

Upgrade path:

1. Create a new setup token in the dashboard (Settings > Home) or via
   `POST /v1/home/agent/tokens`.
2. Regenerate the agent env file with `scripts/install-agent-env.sh` (or paste
   the new setup block into `.env.agent` and run `chmod 600 .env.agent`).
3. Restart the agent with the current image/binary:
   `docker compose --env-file .env.cloud --profile agent up -d agent`.
4. Confirm the dashboard shows the agent online.
5. Revoke the old token.

## Recovery

1. Restart the agent process.
2. If auth is failing, issue a replacement token from `POST /v1/home/agent/tokens`.
3. Update the agent env file with the replacement token.
4. Restart the agent and verify the cloud reports it online.
5. Revoke the old token once the new token is confirmed working.

## Verify

- `/metrics` shows `hank_online_agents 1` for the active test environment
- the app can route `system.ping`
- Home Assistant or file commands succeed again
