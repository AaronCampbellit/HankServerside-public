ALTER TABLE home_agent_apps
	ADD COLUMN IF NOT EXISTS capabilities_json TEXT NOT NULL DEFAULT '[]';

ALTER TABLE home_agent_apps
	ADD COLUMN IF NOT EXISTS slash_commands_json TEXT NOT NULL DEFAULT '[]';

ALTER TABLE home_agent_apps
	ADD COLUMN IF NOT EXISTS commands_json TEXT NOT NULL DEFAULT '[]';
