CREATE UNIQUE INDEX IF NOT EXISTS agents_home_id_id_idx ON agents(home_id, id);

CREATE TABLE IF NOT EXISTS desktop_trust_roots (
    home_id TEXT PRIMARY KEY REFERENCES homes(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    algorithm TEXT NOT NULL CHECK (algorithm = 'ECDSA_P256_SHA256'),
    public_key_spki BYTEA NOT NULL,
    fingerprint TEXT NOT NULL,
    recovery_envelope BYTEA,
    recovery_challenge_hash BYTEA,
    recovery_challenge_expires_at TIMESTAMPTZ,
    recovery_challenge_consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    rotated_at TIMESTAMPTZ,
    UNIQUE (home_id, generation),
    UNIQUE (home_id, fingerprint),
    CHECK ((recovery_challenge_hash IS NULL AND recovery_challenge_expires_at IS NULL)
        OR (recovery_challenge_hash IS NOT NULL AND recovery_challenge_expires_at IS NOT NULL)),
    CHECK (recovery_challenge_consumed_at IS NULL OR recovery_challenge_hash IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS desktop_identities (
    id TEXT PRIMARY KEY,
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    identity_type TEXT NOT NULL CHECK (identity_type IN ('operator_device', 'endpoint')),
    user_id TEXT,
    device_id TEXT,
    agent_id TEXT,
    public_key_spki BYTEA NOT NULL,
    certificate BYTEA NOT NULL,
    fingerprint TEXT NOT NULL,
    capabilities TEXT[] NOT NULL DEFAULT '{}',
    trust_root_generation INTEGER NOT NULL CHECK (trust_root_generation > 0),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    revocation_reason TEXT,
    CHECK ((identity_type = 'operator_device' AND user_id IS NOT NULL AND device_id IS NOT NULL AND agent_id IS NULL)
        OR (identity_type = 'endpoint' AND user_id IS NULL AND device_id IS NULL AND agent_id IS NOT NULL)),
    CHECK (expires_at > created_at),
    CHECK ((revoked_at IS NULL AND revocation_reason IS NULL)
        OR (revoked_at IS NOT NULL AND revocation_reason IS NOT NULL AND btrim(revocation_reason) <> '')),
    UNIQUE (home_id, fingerprint),
    UNIQUE (home_id, user_id, id),
    FOREIGN KEY (home_id, user_id) REFERENCES home_memberships(home_id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (home_id, agent_id) REFERENCES agents(home_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS desktop_operator_device_identity_idx
    ON desktop_identities(home_id, device_id)
    WHERE identity_type = 'operator_device' AND revoked_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS desktop_endpoint_identity_idx
    ON desktop_identities(home_id, agent_id)
    WHERE identity_type = 'endpoint' AND revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS desktop_sessions (
    id TEXT PRIMARY KEY,
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    agent_id TEXT NOT NULL,
    operator_user_id TEXT NOT NULL,
    operator_device_identity_id TEXT NOT NULL,
    requested_permissions TEXT[] NOT NULL,
    effective_permissions TEXT[] NOT NULL,
    state TEXT NOT NULL,
    key_epoch INTEGER NOT NULL CHECK (key_epoch > 0),
    requested_at TIMESTAMPTZ NOT NULL,
    join_expires_at TIMESTAMPTZ NOT NULL,
    active_at TIMESTAMPTZ,
    reconnect_expires_at TIMESTAMPTZ,
    hard_expires_at TIMESTAMPTZ NOT NULL,
    terminated_at TIMESTAMPTZ,
    termination_reason TEXT,
    source_ip_hash TEXT NOT NULL DEFAULT '',
    source_user_agent_hash TEXT NOT NULL DEFAULT '',
    browser_to_agent_bytes BIGINT NOT NULL DEFAULT 0 CHECK (browser_to_agent_bytes >= 0),
    agent_to_browser_bytes BIGINT NOT NULL DEFAULT 0 CHECK (agent_to_browser_bytes >= 0),
    CHECK (state IN ('requested', 'offered', 'agent_ready', 'joining', 'active', 'reconnecting', 'denied', 'failed', 'expired', 'terminated')),
    CHECK (requested_permissions <@ ARRAY['desktop.view','desktop.control','desktop.clipboard.read','desktop.clipboard.write','desktop.elevate','desktop.secure_desktop','desktop.unattended']::TEXT[]),
    CHECK (effective_permissions <@ requested_permissions),
    CHECK ('desktop.view' = ANY(requested_permissions) AND 'desktop.view' = ANY(effective_permissions)),
    CHECK (join_expires_at > requested_at AND join_expires_at <= requested_at + INTERVAL '60 seconds'),
    CHECK (hard_expires_at > requested_at AND hard_expires_at <= requested_at + INTERVAL '8 hours'),
    CHECK (reconnect_expires_at IS NULL OR (reconnect_expires_at > requested_at AND reconnect_expires_at <= hard_expires_at)),
    CHECK ((state IN ('denied', 'failed', 'expired', 'terminated') AND terminated_at IS NOT NULL AND termination_reason IS NOT NULL)
        OR (state NOT IN ('denied', 'failed', 'expired', 'terminated') AND terminated_at IS NULL AND termination_reason IS NULL)),
    FOREIGN KEY (home_id, agent_id) REFERENCES agents(home_id, id) ON DELETE CASCADE,
    FOREIGN KEY (home_id, operator_user_id) REFERENCES home_memberships(home_id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (home_id, operator_user_id, operator_device_identity_id)
        REFERENCES desktop_identities(home_id, user_id, id)
);

CREATE UNIQUE INDEX IF NOT EXISTS desktop_sessions_one_live_operator_idx
    ON desktop_sessions(agent_id)
    WHERE state IN ('requested','offered','agent_ready','joining','active','reconnecting');

CREATE TABLE IF NOT EXISTS desktop_join_credentials (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES desktop_sessions(id) ON DELETE CASCADE,
    side TEXT NOT NULL CHECK (side IN ('browser', 'agent')),
    credential_hash BYTEA NOT NULL UNIQUE,
    key_epoch INTEGER NOT NULL CHECK (key_epoch > 0),
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at),
    UNIQUE (session_id, side, key_epoch)
);

CREATE TABLE IF NOT EXISTS desktop_session_events (
    session_id TEXT NOT NULL REFERENCES desktop_sessions(id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL CHECK (btrim(event_type) <> ''),
    actor_type TEXT NOT NULL CHECK (actor_type IN ('user', 'agent', 'server', 'browser')),
    actor_id TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'security')),
    reason_code TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    PRIMARY KEY (session_id, sequence)
);

CREATE INDEX IF NOT EXISTS desktop_session_events_occurred_idx
    ON desktop_session_events(occurred_at DESC);

CREATE INDEX IF NOT EXISTS desktop_sessions_home_requested_idx
    ON desktop_sessions(home_id, requested_at DESC);
