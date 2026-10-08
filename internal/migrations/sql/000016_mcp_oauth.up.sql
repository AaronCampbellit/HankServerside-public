CREATE TABLE IF NOT EXISTS mcp_oauth_clients (
	id TEXT PRIMARY KEY,
	client_secret_hash TEXT NOT NULL DEFAULT '',
	redirect_uris JSONB NOT NULL DEFAULT '[]',
	client_name TEXT NOT NULL DEFAULT '',
	token_endpoint_auth_method TEXT NOT NULL DEFAULT 'none',
	grant_types JSONB NOT NULL DEFAULT '[]',
	scope TEXT NOT NULL DEFAULT '',
	created_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS mcp_oauth_auth_codes (
	code_hash TEXT PRIMARY KEY,
	client_id TEXT NOT NULL REFERENCES mcp_oauth_clients(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	redirect_uri TEXT NOT NULL,
	code_challenge TEXT NOT NULL,
	code_challenge_method TEXT NOT NULL DEFAULT 'S256',
	scopes JSONB NOT NULL DEFAULT '[]',
	resource TEXT NOT NULL DEFAULT '',
	expires_at TIMESTAMP NOT NULL,
	consumed_at TIMESTAMP NULL,
	created_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mcp_oauth_auth_codes_user ON mcp_oauth_auth_codes(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_oauth_auth_codes_expiry ON mcp_oauth_auth_codes(expires_at);

CREATE TABLE IF NOT EXISTS mcp_oauth_tokens (
	id TEXT PRIMARY KEY,
	client_id TEXT NOT NULL REFERENCES mcp_oauth_clients(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	access_token_hash TEXT NOT NULL UNIQUE,
	refresh_token_hash TEXT NULL UNIQUE,
	scopes JSONB NOT NULL DEFAULT '[]',
	resource TEXT NOT NULL DEFAULT '',
	access_expires_at TIMESTAMP NOT NULL,
	refresh_expires_at TIMESTAMP NULL,
	revoked_at TIMESTAMP NULL,
	last_used_at TIMESTAMP NULL,
	last_used_route TEXT NOT NULL DEFAULT '',
	last_used_ip_hash TEXT NOT NULL DEFAULT '',
	request_count BIGINT NOT NULL DEFAULT 0,
	created_at TIMESTAMP NOT NULL,
	updated_at TIMESTAMP NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mcp_oauth_tokens_user ON mcp_oauth_tokens(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_mcp_oauth_tokens_active ON mcp_oauth_tokens(user_id, revoked_at, access_expires_at);
