DROP INDEX IF EXISTS user_notifications_audit_event_idx;

ALTER TABLE user_notifications
    DROP COLUMN IF EXISTS audit_event_id;
