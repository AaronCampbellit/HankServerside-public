CREATE TABLE IF NOT EXISTS home_agent_apps (
	home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
	app_id TEXT NOT NULL,
	name TEXT NOT NULL,
	version TEXT NOT NULL,
	enabled BOOLEAN NOT NULL DEFAULT FALSE,
	public_config_json TEXT NOT NULL DEFAULT '{}',
	secret_fields_set_json TEXT NOT NULL DEFAULT '{}',
	status TEXT NOT NULL DEFAULT 'pending',
	last_error TEXT NOT NULL DEFAULT '',
	updated_at TIMESTAMP NOT NULL,
	updated_by TEXT NOT NULL,
	PRIMARY KEY (home_id, app_id)
);
