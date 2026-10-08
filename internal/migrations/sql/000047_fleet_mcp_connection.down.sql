DROP TABLE mcp_connector_settings;
-- Revoke hosted grants before removing their connection binding.
UPDATE fleet_grants SET state='revoked' WHERE mcp_token_id IS NOT NULL;
ALTER TABLE fleet_grants DROP CONSTRAINT fleet_mcp_owner_fk;
DROP INDEX fleet_grants_mcp_token;
ALTER TABLE fleet_grants DROP COLUMN mcp_token_id;
ALTER TABLE mcp_oauth_tokens DROP CONSTRAINT mcp_tokens_id_user_unique;
