CREATE TABLE IF NOT EXISTS agent_enrollments (
    id TEXT PRIMARY KEY,
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    platform TEXT NOT NULL CHECK (platform = 'linux'),
    token_hash TEXT NOT NULL UNIQUE,
    created_by_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name_hint TEXT,
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    downloaded_at TIMESTAMPTZ,
    consumed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    agent_id TEXT REFERENCES agents(id) ON DELETE SET NULL,
    download_count INTEGER NOT NULL DEFAULT 0 CHECK (download_count >= 0),
    failed_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (failed_attempt_count >= 0),
    CHECK (expires_at > created_at),
    CHECK (NOT (consumed_at IS NOT NULL AND revoked_at IS NOT NULL)),
    CHECK (consumed_at IS NULL OR agent_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS agent_enrollments_home_created_idx ON agent_enrollments(home_id, created_at DESC);
CREATE INDEX IF NOT EXISTS agent_enrollments_active_idx ON agent_enrollments(expires_at) WHERE consumed_at IS NULL AND revoked_at IS NULL;

ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS generation INTEGER NOT NULL DEFAULT 1 CHECK (generation > 0);
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS replaces_token_id TEXT REFERENCES agent_tokens(id) ON DELETE SET NULL;
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS activated_at TIMESTAMPTZ;
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS confirmed_at TIMESTAMPTZ;
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS rotation_requested_at TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS agent_tokens_one_pending_replacement_idx
    ON agent_tokens(agent_id) WHERE replaces_token_id IS NOT NULL AND confirmed_at IS NULL AND revoked_at IS NULL;
