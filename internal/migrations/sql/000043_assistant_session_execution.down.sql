DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM assistant_sessions WHERE execution_version=2) THEN
  RAISE EXCEPTION 'Execution-v2 conversations require offline review before rollback';
 END IF;
END $$;
DROP INDEX assistant_tasks_one_active_session;
ALTER TABLE assistant_sessions DROP COLUMN execution_version;
