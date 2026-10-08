-- Historical retries could change agents without durable attribution. Do not
-- infer ownership from a source name, the current primary, or old relay rows.
ALTER TABLE file_operation_jobs ADD COLUMN agent_id TEXT NULL;
ALTER TABLE file_operation_jobs ADD CONSTRAINT file_jobs_agent_home_fk
    FOREIGN KEY (home_id, agent_id) REFERENCES agents(home_id, id)
    ON DELETE SET NULL (agent_id);
CREATE INDEX file_jobs_agent_home_idx ON file_operation_jobs(home_id, agent_id);
