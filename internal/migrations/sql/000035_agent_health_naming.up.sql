ALTER TABLE notification_settings
    RENAME COLUMN connector_enabled TO agent_health_enabled;

ALTER TABLE user_notifications
    DROP CONSTRAINT IF EXISTS user_notifications_category_check;

UPDATE user_notifications
SET category = 'agent_health'
WHERE category = 'connector';

UPDATE user_notifications
SET event_kind = CASE event_kind
    WHEN 'connector.offline' THEN 'agent.offline'
    WHEN 'connector.recovered' THEN 'agent.recovered'
    ELSE event_kind
END
WHERE event_kind IN ('connector.offline', 'connector.recovered');

UPDATE notification_source_events
SET event_kind = CASE event_kind
    WHEN 'connector.offline' THEN 'agent.offline'
    WHEN 'connector.recovered' THEN 'agent.recovered'
    ELSE event_kind
END
WHERE event_kind IN ('connector.offline', 'connector.recovered');

UPDATE audit_events
SET event_type = CASE event_type
    WHEN 'connector.offline' THEN 'agent.offline'
    WHEN 'connector.recovered' THEN 'agent.recovered'
    ELSE event_type
END
WHERE event_type IN ('connector.offline', 'connector.recovered');

UPDATE apns_devices AS device
SET enabled_categories = (
    SELECT COALESCE(
        jsonb_agg(
            CASE WHEN item.value = 'connector' THEN 'agent_health' ELSE item.value END
            ORDER BY item.ordinality
        ),
        '[]'::jsonb
    )
    FROM jsonb_array_elements_text(device.enabled_categories)
        WITH ORDINALITY AS item(value, ordinality)
)
WHERE device.enabled_categories ? 'connector';

ALTER TABLE user_notifications
    ADD CONSTRAINT user_notifications_category_check
    CHECK (category IN ('agent_health', 'quick_links', 'storage', 'notes', 'dashboard_entities'));
