-- Offline rollback only, after backup and verification that no v2 task or
-- operation is active/unknown. This removes execution history and receipts.
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM assistant_tasks) OR EXISTS (SELECT 1 FROM assistant_operation_receipts) THEN
  RAISE EXCEPTION 'assistant execution rollback requires empty task and receipt stores after reviewed backup';
 END IF;
END $$;
DROP TABLE assistant_task_inputs;
DROP TABLE assistant_task_events;
DROP TABLE assistant_operation_receipts;
DROP TABLE assistant_task_approvals;
DROP TABLE assistant_task_steps;
DROP TABLE assistant_tasks;
ALTER TABLE assistant_sessions DROP CONSTRAINT assistant_sessions_scope_key;
