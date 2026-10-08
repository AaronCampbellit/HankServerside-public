-- Fail rather than discard monitoring inbox history on rollback. An older
-- application can run with migration 36 only if its schema gate permits it;
-- normally restore a coordinated pre-upgrade backup instead.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM user_notifications WHERE category = 'monitoring') THEN
        RAISE EXCEPTION 'monitoring notifications exist; preserve their history before rolling back migration 36';
    END IF;
END $$;
ALTER TABLE user_notifications DROP CONSTRAINT user_notifications_category_check;
ALTER TABLE user_notifications ADD CONSTRAINT user_notifications_category_check
    CHECK (category IN ('agent_health', 'quick_links', 'storage', 'notes', 'dashboard_entities'));
ALTER TABLE notification_settings DROP COLUMN monitoring_enabled;
