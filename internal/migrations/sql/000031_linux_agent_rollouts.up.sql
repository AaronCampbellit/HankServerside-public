ALTER TABLE agents ADD COLUMN IF NOT EXISTS platform TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS architecture TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS app_version TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS installation_mode TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS capabilities JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS linux_agent_releases (
    id TEXT PRIMARY KEY,
    version TEXT NOT NULL UNIQUE CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+$'),
    manifest_sha256 TEXT NOT NULL UNIQUE CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
    source_commit TEXT NOT NULL CHECK (source_commit ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    manifest_url TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('hosted', 'active', 'superseded')),
    created_at TIMESTAMPTZ NOT NULL,
    activated_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS linux_agent_releases_one_active_idx ON linux_agent_releases(state) WHERE state = 'active';

CREATE TABLE IF NOT EXISTS linux_agent_rollouts (
    id TEXT PRIMARY KEY,
    release_id TEXT NOT NULL REFERENCES linux_agent_releases(id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('active', 'paused', 'completed', 'cancelled')),
    scope TEXT NOT NULL CHECK (scope = 'all-linux'),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS linux_agent_rollouts_one_open_idx ON linux_agent_rollouts((1)) WHERE state IN ('active', 'paused');

CREATE TABLE IF NOT EXISTS linux_agent_version_pins (
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    agent_id TEXT PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    version TEXT NOT NULL CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+$'),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS linux_agent_update_assignments (
    id TEXT PRIMARY KEY,
    rollout_id TEXT NOT NULL REFERENCES linux_agent_rollouts(id) ON DELETE CASCADE,
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    from_version TEXT,
    to_version TEXT NOT NULL CHECK (to_version ~ '^[0-9]+\.[0-9]+\.[0-9]+$'),
    state TEXT NOT NULL CHECK (state IN ('pending', 'delayed', 'waiting_online', 'downloading', 'installing', 'reconnecting', 'healthy', 'failed', 'rolled_back', 'recovery_failed', 'manual_update_required', 'pinned', 'cancelled')),
    error_code TEXT NOT NULL DEFAULT '',
    not_before TIMESTAMPTZ NOT NULL,
    attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (rollout_id, agent_id)
);
CREATE INDEX IF NOT EXISTS linux_agent_assignments_dispatch_idx ON linux_agent_update_assignments(state, not_before);
CREATE INDEX IF NOT EXISTS linux_agent_assignments_home_idx ON linux_agent_update_assignments(home_id, rollout_id, created_at);
