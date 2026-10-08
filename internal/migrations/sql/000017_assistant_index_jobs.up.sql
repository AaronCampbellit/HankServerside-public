CREATE TABLE IF NOT EXISTS assistant_index_jobs (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL,
	user_id TEXT NOT NULL,
	source_type TEXT NOT NULL,
	source_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	attempts INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	run_after TIMESTAMPTZ NOT NULL,
	started_at TIMESTAMPTZ NULL,
	completed_at TIMESTAMPTZ NULL,
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	FOREIGN KEY(home_id) REFERENCES homes(id),
	FOREIGN KEY(user_id) REFERENCES users(id),
	UNIQUE(home_id, user_id, source_type, source_id),
	CHECK (status IN ('queued', 'running', 'completed', 'failed'))
);

CREATE INDEX IF NOT EXISTS idx_assistant_index_jobs_claim
	ON assistant_index_jobs(status, run_after, updated_at);

CREATE INDEX IF NOT EXISTS idx_assistant_index_jobs_home_user
	ON assistant_index_jobs(home_id, user_id, status);
