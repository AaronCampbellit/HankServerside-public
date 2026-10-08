DROP TABLE IF EXISTS linux_agent_update_assignments;
DROP TABLE IF EXISTS linux_agent_version_pins;
DROP TABLE IF EXISTS linux_agent_rollouts;
DROP TABLE IF EXISTS linux_agent_releases;
ALTER TABLE agents DROP COLUMN IF EXISTS capabilities;
ALTER TABLE agents DROP COLUMN IF EXISTS installation_mode;
ALTER TABLE agents DROP COLUMN IF EXISTS app_version;
ALTER TABLE agents DROP COLUMN IF EXISTS architecture;
ALTER TABLE agents DROP COLUMN IF EXISTS platform;
