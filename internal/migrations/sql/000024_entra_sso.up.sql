ALTER TABLE users
    ADD COLUMN IF NOT EXISTS password_login_enabled BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE IF NOT EXISTS external_identities (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider TEXT NOT NULL CHECK (provider IN ('entra')),
    tenant_id TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE(provider, tenant_id, subject_id),
    UNIQUE(user_id, provider, tenant_id)
);
