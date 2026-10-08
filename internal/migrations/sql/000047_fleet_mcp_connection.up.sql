-- Existing CLI grants retain their behavior. Hosted grants bind to one user-owned connection.
ALTER TABLE mcp_oauth_tokens ADD CONSTRAINT mcp_tokens_id_user_unique UNIQUE(id,user_id);
ALTER TABLE fleet_grants ADD COLUMN mcp_token_id TEXT;
ALTER TABLE fleet_grants ADD CONSTRAINT fleet_mcp_owner_fk FOREIGN KEY(mcp_token_id,user_id) REFERENCES mcp_oauth_tokens(id,user_id);
CREATE INDEX fleet_grants_mcp_token ON fleet_grants(mcp_token_id) WHERE mcp_token_id IS NOT NULL;
CREATE TABLE mcp_connector_settings (
 singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK(singleton),
 enabled BOOLEAN NOT NULL,
 updated_by TEXT NOT NULL REFERENCES users(id),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
