ALTER TABLE notification_settings ADD COLUMN monitoring_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE user_notifications DROP CONSTRAINT user_notifications_category_check;
ALTER TABLE user_notifications ADD CONSTRAINT user_notifications_category_check
    CHECK (category IN ('agent_health', 'quick_links', 'storage', 'notes', 'dashboard_entities', 'monitoring'));
