ALTER TABLE assistant_sessions ADD COLUMN execution_version INTEGER NOT NULL DEFAULT 1 CHECK(execution_version IN (1,2));
UPDATE assistant_sessions SET execution_version=2 WHERE EXISTS(SELECT 1 FROM assistant_tasks t WHERE t.session_id=assistant_sessions.id);
-- Fail safely if staged data contains concurrent active work. Operators must
-- reconcile it explicitly; this migration never cancels or discards a task.
CREATE UNIQUE INDEX assistant_tasks_one_active_session ON assistant_tasks(session_id) WHERE session_id IS NOT NULL AND state NOT IN ('completed','failed','cancelled');
