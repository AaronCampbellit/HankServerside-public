ALTER TABLE user_notifications
    ADD COLUMN IF NOT EXISTS audit_event_id TEXT REFERENCES audit_events(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS user_notifications_audit_event_idx
    ON user_notifications(audit_event_id)
    WHERE audit_event_id IS NOT NULL;
