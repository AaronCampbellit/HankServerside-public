CREATE TABLE IF NOT EXISTS mcp_settings (
    home_id TEXT PRIMARY KEY REFERENCES homes(id) ON DELETE CASCADE,
    kanban_app_enabled BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    updated_by TEXT NOT NULL REFERENCES users(id)
);
