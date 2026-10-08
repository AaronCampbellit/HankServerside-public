-- Removing populated ownership would make later recovery ambiguous.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM file_operation_jobs WHERE agent_id IS NOT NULL) THEN
        RAISE EXCEPTION 'file job ownership exists; preserve history before rollback';
    END IF;
END $$;
ALTER TABLE file_operation_jobs DROP CONSTRAINT file_jobs_agent_home_fk;
DROP INDEX file_jobs_agent_home_idx;
ALTER TABLE file_operation_jobs DROP COLUMN agent_id;
