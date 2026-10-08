ALTER TABLE homes
    ADD COLUMN IF NOT EXISTS agent_enrollment_policy TEXT NOT NULL DEFAULT 'admins_only';
ALTER TABLE homes DROP CONSTRAINT IF EXISTS homes_agent_enrollment_policy_check;
ALTER TABLE homes ADD CONSTRAINT homes_agent_enrollment_policy_check
    CHECK (agent_enrollment_policy IN ('admins_only', 'all_users'));

ALTER TABLE agents ADD COLUMN IF NOT EXISTS installation_id TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS enrolled_by_user_id TEXT REFERENCES users(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX IF NOT EXISTS agents_home_installation_id_idx
    ON agents(home_id, installation_id) WHERE installation_id IS NOT NULL;

ALTER TABLE desktop_trust_roots
    ADD COLUMN IF NOT EXISTS authority_mode TEXT NOT NULL DEFAULT 'client_managed';
ALTER TABLE desktop_trust_roots
    ADD COLUMN IF NOT EXISTS encrypted_private_key TEXT;
ALTER TABLE desktop_trust_roots DROP CONSTRAINT IF EXISTS desktop_trust_roots_authority_mode_check;
ALTER TABLE desktop_trust_roots ADD CONSTRAINT desktop_trust_roots_authority_mode_check
    CHECK (authority_mode IN ('client_managed', 'server_managed'));

ALTER TABLE desktop_identities
    ADD COLUMN IF NOT EXISTS auth_session_id TEXT REFERENCES app_sessions(id) ON DELETE CASCADE;

CREATE TABLE IF NOT EXISTS desktop_enrollment_challenges (
    id TEXT PRIMARY KEY,
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES app_sessions(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('browser_operator', 'mac_agent')),
    installation_id TEXT NOT NULL,
    challenge_hash BYTEA NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (expires_at > created_at)
);
CREATE INDEX IF NOT EXISTS desktop_enrollment_challenges_active_idx
    ON desktop_enrollment_challenges(home_id, user_id, purpose, expires_at)
    WHERE consumed_at IS NULL;
