-- Rolling back removes the server's display cache only. Agent grants remain
-- bound to package and target fingerprints and must not be interpreted by older agents.
ALTER TABLE home_agent_apps DROP CONSTRAINT home_agent_apps_permissions_object;
ALTER TABLE home_agent_apps DROP COLUMN permissions_json;
