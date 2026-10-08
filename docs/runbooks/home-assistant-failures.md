# Runbook: Home Assistant Failure

Use this when `homeassistant.*` commands fail or return upstream errors.

## Check

1. confirm the primary Hank Agent is online
2. confirm `HANK_HA_BASE_URL` is correct on the agent host
3. confirm `HANK_HA_TOKEN` is still valid
4. test local Home Assistant reachability from the agent machine

## Common Causes

- Home Assistant unavailable on the LAN
- invalid or expired Home Assistant token
- malformed upstream response
- local timeout between the agent and Home Assistant

## Recovery

1. fix the local Home Assistant availability issue
2. replace the Home Assistant token in the agent env file if needed
3. restart the agent to reload configuration
4. retry `homeassistant.health`

## Verify

- `homeassistant.health` succeeds
- `homeassistant.fetch_states` returns the expected dashboard state
- service calls complete without upstream errors
