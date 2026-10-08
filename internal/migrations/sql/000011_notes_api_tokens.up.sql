CREATE TABLE IF NOT EXISTS notes_api_tokens (
	id TEXT PRIMARY KEY,
	home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	token_hash TEXT NOT NULL UNIQUE,
	scopes JSONB NOT NULL DEFAULT '[]',
	allow_home_notes BOOLEAN NOT NULL DEFAULT FALSE,
	expires_at TIMESTAMP NULL,
	revoked_at TIMESTAMP NULL,
	last_used_at TIMESTAMP NULL,
	last_used_route TEXT NOT NULL DEFAULT '',
	last_used_ip_hash TEXT NOT NULL DEFAULT '',
	last_used_user_agent_hash TEXT NOT NULL DEFAULT '',
	request_count BIGINT NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL,
	created_by TEXT NOT NULL REFERENCES users(id),
	updated_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_notes_api_tokens_home_created ON notes_api_tokens(home_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notes_api_tokens_user_created ON notes_api_tokens(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notes_api_tokens_active ON notes_api_tokens(home_id, revoked_at, expires_at);
