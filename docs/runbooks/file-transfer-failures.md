# Runbook: File Transfer Failure

Use this when `POST /v1/home/files/uploads`, `POST /v1/home/files/downloads`, `GET /v1/file-transfers/{id}`, or `PUT /v1/file-transfers/{id}` fail.

For managed file-browser jobs, also use this when `GET /v1/home/file-jobs/{jobID}` reports `failed`, `cancelled`, or `rollback_required`.

## Check

1. verify the targeted Hank Agent is online
2. verify the configured file root exists on the agent host
3. confirm the transfer URL still has a valid transfer token
4. if resuming, confirm the client is using the correct `offset`
5. for `upload_too_large`, compare `attempted_upload_bytes` with
   `max_upload_bytes` in the `413` response

## Common Causes

- target path is outside the configured file root
- transfer expired before retry
- agent disconnected mid-transfer
- client resumed with the wrong offset

## Recovery

1. retry the transfer if the transfer session is still valid
2. if resume fails repeatedly, create a fresh upload or download session
3. if the agent disconnected, restore the agent connection first
4. for persistent path failures, verify the local file root and permissions on the agent machine

## Managed File Jobs

Managed file jobs are created by dashboard file-browser actions such as cross-source move. The current recovery actions are:

- `POST /v1/home/file-jobs/{jobID}/cancel` for queued or running jobs.
- `POST /v1/home/file-jobs/{jobID}/retry` for `failed` or `cancelled` jobs.
- `POST /v1/home/file-jobs/{jobID}/rollback` for `rollback_required` move jobs.
- `DELETE /v1/home/file-jobs` clears completed, failed, cancelled, and
  rolled-back history, plus old upload/download jobs marked `rollback_required`
  by earlier servers. Queued, running, and rollback-required move jobs remain.

After a server restart, interrupted uploads and downloads are marked failed and
can be cleared or started again. Interrupted moves retain `rollback_required`
because the destination may need recovery.

The File Server dashboard offers Cancel for active uploads and downloads. Confirm
the cancellation, then use **Clear finished** to remove the finished job from history.

Use rollback when the destination copy was verified but source deletion failed or was interrupted. Rollback asks the agent to delete the copied destination path and then marks the job `rolled_back`. The action is idempotent: if the copied destination is already gone, rollback still succeeds.

Do not retry a `rollback_required` move until you have either rolled it back or manually confirmed that keeping the destination copy is intentional.

### History that will not clear

Moves marked **Needs review** are not active transfers. A cloud restart marks
unfinished moves `rollback_required` because it cannot establish whether a
partial copy or source deletion occurred. **Clear finished** deliberately leaves
these records intact; unknown agent ownership also blocks automatic recovery.

Check the source and destination before deciding whether to recover the move or
keep the files as they are. An administrator can use **Remove from history** and
confirm the warning to discard an obsolete recovery record. This does not move,
delete, cancel, or roll back files, and partial copies remain. It removes the
record's retry/rollback information. The [API contract](../api.md) describes the
scoped confirmation token and audit behavior. Other connected dashboards refresh
after history changes; Refresh also reloads the current list.

## Verify

- the transfer completes with `200`
- resumed transfers continue from the requested byte offset
- the resulting file content matches the expected source
- managed file jobs end in `completed`, `cancelled`, or `rolled_back`
