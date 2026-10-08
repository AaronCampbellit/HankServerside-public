CREATE TABLE fleet_grant_dismissals (
    grant_id TEXT NOT NULL REFERENCES fleet_grants(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    dismissed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (grant_id, user_id)
);
