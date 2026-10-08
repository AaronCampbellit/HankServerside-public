-- Rollback requires explicit conversion/removal of non-expiring grants first.
-- Refuse to silently change authorized access or invent an expiry.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM fleet_grants WHERE expires_at IS NULL) THEN
  RAISE EXCEPTION 'Resolve non-expiring fleet grants before rolling back migration 48';
 END IF;
END $$;
ALTER TABLE fleet_grants ALTER COLUMN expires_at SET NOT NULL;
