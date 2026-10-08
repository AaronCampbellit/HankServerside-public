ALTER TABLE home_agent_apps
	ADD COLUMN IF NOT EXISTS settings_schema_json TEXT NOT NULL DEFAULT '{}';
