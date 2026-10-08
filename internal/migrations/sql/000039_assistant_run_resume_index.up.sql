CREATE INDEX IF NOT EXISTS idx_assistant_runs_session_pending
ON assistant_runs(session_id, created_at DESC)
WHERE completed_at IS NULL;
