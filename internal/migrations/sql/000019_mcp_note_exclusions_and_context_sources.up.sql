ALTER TABLE user_notes ADD COLUMN IF NOT EXISTS mcp_excluded BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS mcp_context_sources (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	home_id TEXT NOT NULL,
	name TEXT NOT NULL,
	source_id TEXT NOT NULL,
	root_path TEXT NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT TRUE,
	last_tested_at TIMESTAMPTZ NULL,
	last_test_error TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMPTZ NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL,
	FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
	FOREIGN KEY(home_id) REFERENCES homes(id) ON DELETE CASCADE,
	CONSTRAINT mcp_context_sources_name_nonempty CHECK (btrim(name) <> ''),
	CONSTRAINT mcp_context_sources_source_id_nonempty CHECK (btrim(source_id) <> ''),
	CONSTRAINT mcp_context_sources_root_path_nonempty CHECK (btrim(root_path) <> '')
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_mcp_context_sources_user_name
	ON mcp_context_sources (user_id, name);

CREATE INDEX IF NOT EXISTS idx_mcp_context_sources_owner_enabled
	ON mcp_context_sources (user_id, enabled);

CREATE INDEX IF NOT EXISTS idx_mcp_context_sources_home_user
	ON mcp_context_sources (home_id, user_id);
