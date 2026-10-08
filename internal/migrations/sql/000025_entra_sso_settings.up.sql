CREATE TABLE IF NOT EXISTS entra_sso_settings (
    home_id TEXT PRIMARY KEY REFERENCES homes(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    tenant_id TEXT NOT NULL DEFAULT '',
    client_id TEXT NOT NULL DEFAULT '',
    client_secret TEXT NOT NULL DEFAULT '',
    public_base_url TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    updated_by TEXT NOT NULL REFERENCES users(id)
);
