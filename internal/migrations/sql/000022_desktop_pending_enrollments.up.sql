CREATE TABLE IF NOT EXISTS desktop_pending_enrollments (
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL,
    request_json JSONB NOT NULL,
    fingerprint TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (home_id, agent_id),
    FOREIGN KEY (home_id, agent_id) REFERENCES agents(home_id, id) ON DELETE CASCADE,
    CHECK (expires_at > created_at)
);
