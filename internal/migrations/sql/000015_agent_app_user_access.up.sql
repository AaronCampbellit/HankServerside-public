ALTER TABLE home_agent_apps
	ADD COLUMN IF NOT EXISTS user_access TEXT NOT NULL DEFAULT 'admins_only';

ALTER TABLE home_agent_apps
	DROP CONSTRAINT IF EXISTS home_agent_apps_user_access_check;

ALTER TABLE home_agent_apps
	ADD CONSTRAINT home_agent_apps_user_access_check
	CHECK (user_access IN ('admins_only', 'home_members'));
