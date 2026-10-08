-- Older binaries interpret unbound grants as bearer grants. Resolve account grants
-- explicitly before rollback; never silently convert account-wide authority.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM fleet_grants WHERE mcp_account) THEN
  RAISE EXCEPTION 'Resolve account-wide fleet grants before rollback';
 END IF;
END $$;
DROP INDEX fleet_grants_mcp_account;
ALTER TABLE fleet_grants DROP CONSTRAINT fleet_account_scope;
ALTER TABLE fleet_grants DROP COLUMN mcp_account;
