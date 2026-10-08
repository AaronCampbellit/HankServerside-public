# Runbook: Auth Failures

Use this when Hank client login, client WebSocket auth, or Hank Agent WebSocket
auth is failing.

## Client Authentication

Check:

1. HTTP requests include `Authorization: Bearer <session_token>`
2. `/ws/app` is opened with a fresh `app_ticket` from `POST /v1/ws/app-ticket`
3. the session has not expired
4. the session was not revoked by logout
5. the account is not blocked by `password_change_required`

Recovery:

1. log in again with `POST /v1/auth/login`
2. verify `GET /v1/me` works with the new session token
3. if `GET /v1/me` reports `password_change_required`, call `POST /v1/auth/change-password`
4. request a fresh app ticket and reconnect `/ws/app`

## Password Reset

Dashboard recovery:

1. sign in as a home admin
2. open Settings > People
3. use Reset Password for the target member
4. leave Require password change on next login enabled unless there is a specific reason not to
5. share the temporary password manually; it is not stored raw and existing sessions are revoked

CLI break-glass recovery:

```bash
hank-server users reset-password --email user@example.com --force-change
```

To provide the temporary password without putting it in shell history:

```bash
printf '%s' "$TEMP_PASSWORD" | hank-server users reset-password --email user@example.com --stdin --force-change
```

For scripts that should only recover admins:

```bash
hank-server users reset-password --email admin@example.com --force-change --admin-only
```

## Agent Auth

Check:

1. `agent_id` matches the token’s agent record
2. the token has not been revoked
3. the token has not expired
4. the agent is connecting to the correct cloud environment

Recovery:

1. issue a replacement token from `POST /v1/home/agent/tokens`
2. update the agent env file
3. restart the agent
4. revoke the old token after the replacement is live

## Verify

- `/metrics` auth-failure counters stop increasing for the failing path
- the next app or agent connection succeeds
