CREATE TABLE monitoring_delivery_settings (
    home_id TEXT PRIMARY KEY REFERENCES homes(id) ON DELETE CASCADE,
    inbox_enabled BOOLEAN NOT NULL DEFAULT TRUE,
    inbox_audience TEXT NOT NULL DEFAULT 'admins' CHECK (inbox_audience IN ('admins', 'members')),
    email_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    smtp_host TEXT NOT NULL DEFAULT '',
    smtp_port INTEGER NOT NULL DEFAULT 587 CHECK (smtp_port BETWEEN 1 AND 65535),
    smtp_username TEXT NOT NULL DEFAULT '',
    email_from TEXT NOT NULL DEFAULT '',
    email_to TEXT NOT NULL DEFAULT '',
    encrypted_credentials TEXT NOT NULL CHECK (encrypted_credentials LIKE 'hankenc:v1:%'),
    updated_at TIMESTAMPTZ NOT NULL,
    updated_by TEXT REFERENCES users(id) ON DELETE SET NULL,
    CHECK (NOT email_enabled OR (smtp_host <> '' AND email_from <> '' AND email_to <> ''))
);
