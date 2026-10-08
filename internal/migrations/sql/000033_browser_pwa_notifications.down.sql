DROP TABLE IF EXISTS web_push_deliveries;
DROP TABLE IF EXISTS web_push_subscriptions;
DROP TABLE IF EXISTS notification_source_events;
DROP TABLE IF EXISTS notification_source_states;
DROP TABLE IF EXISTS notification_event_receipts;
DROP TABLE IF EXISTS user_notifications;
ALTER TABLE notification_settings DROP COLUMN IF EXISTS quick_links_enabled;
ALTER TABLE notification_settings DROP COLUMN IF EXISTS connector_enabled;
