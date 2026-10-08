ALTER TABLE home_agent_apps ADD COLUMN permissions_json JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE home_agent_apps ADD CONSTRAINT home_agent_apps_permissions_object CHECK (jsonb_typeof(permissions_json) = 'object');
