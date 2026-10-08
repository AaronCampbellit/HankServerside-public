ALTER TABLE user_notifications
    DROP CONSTRAINT IF EXISTS user_notifications_category_check;

UPDATE user_notifications
SET category = 'connector'
WHERE category = 'agent_health';

UPDATE user_notifications
SET event_kind = CASE event_kind
    WHEN 'agent.offline' THEN 'connector.offline'
    WHEN 'agent.recovered' THEN 'connector.recovered'
    ELSE event_kind
END
WHERE event_kind IN ('agent.offline', 'agent.recovered');

UPDATE notification_source_events
SET event_kind = CASE event_kind
    WHEN 'agent.offline' THEN 'connector.offline'
    WHEN 'agent.recovered' THEN 'connector.recovered'
    ELSE event_kind
END
WHERE event_kind IN ('agent.offline', 'agent.recovered');

UPDATE audit_events
SET event_type = CASE event_type
    WHEN 'agent.offline' THEN 'connector.offline'
    WHEN 'agent.recovered' THEN 'connector.recovered'
    ELSE event_type
END
WHERE event_type IN ('agent.offline', 'agent.recovered');

UPDATE apns_devices AS device
SET enabled_categories = (
    SELECT COALESCE(
        jsonb_agg(
            CASE WHEN item.value = 'agent_health' THEN 'connector' ELSE item.value END
            ORDER BY item.ordinality
        ),
        '[]'::jsonb
    )
    FROM jsonb_array_elements_text(device.enabled_categories)
        WITH ORDINALITY AS item(value, ordinality)
)
WHERE device.enabled_categories ? 'agent_health';

ALTER TABLE user_notifications
    ADD CONSTRAINT user_notifications_category_check
    CHECK (category IN ('connector', 'quick_links', 'storage', 'notes', 'dashboard_entities'));

ALTER TABLE notification_settings
    RENAME COLUMN agent_health_enabled TO connector_enabled;
