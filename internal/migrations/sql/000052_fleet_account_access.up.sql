-- Account-wide MCP access is opt-in. Existing approvals keep their exact scope.
ALTER TABLE fleet_grants ADD COLUMN mcp_account BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE fleet_grants ADD CONSTRAINT fleet_account_scope CHECK (
 NOT mcp_account OR (mcp_token_id IS NULL AND jsonb_array_length(operations)=5
 AND operations @> '["workspace.read","workspace.write","job.run","job.read","job.cancel"]'::jsonb)
);
CREATE INDEX fleet_grants_mcp_account ON fleet_grants(home_id,user_id) WHERE mcp_account;
