ALTER TABLE notification_settings
    ADD COLUMN IF NOT EXISTS connector_enabled BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE notification_settings
    ADD COLUMN IF NOT EXISTS quick_links_enabled BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE IF NOT EXISTS user_notifications (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    category TEXT NOT NULL CHECK (category IN ('connector', 'quick_links', 'storage', 'notes', 'dashboard_entities')),
    event_kind TEXT NOT NULL,
    severity TEXT NOT NULL DEFAULT 'info',
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    target_path TEXT NOT NULL,
    collapse_key TEXT NOT NULL,
    outcome TEXT NOT NULL,
    occurrence_count INTEGER NOT NULL DEFAULT 1 CHECK (occurrence_count > 0),
    first_occurred_at TIMESTAMPTZ NOT NULL,
    last_occurred_at TIMESTAMPTZ NOT NULL,
    read_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (char_length(event_kind) BETWEEN 1 AND 80),
    CHECK (char_length(title) BETWEEN 1 AND 160),
    CHECK (char_length(body) <= 512),
    CHECK (char_length(target_path) BETWEEN 1 AND 512 AND left(target_path, 1) = '/'),
    CHECK (char_length(collapse_key) BETWEEN 1 AND 512),
    CHECK (char_length(outcome) BETWEEN 1 AND 80),
    CHECK (last_occurred_at >= first_occurred_at)
);

CREATE INDEX IF NOT EXISTS user_notifications_user_timeline_idx
    ON user_notifications(user_id, last_occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS user_notifications_user_unread_idx
    ON user_notifications(user_id, last_occurred_at DESC, id DESC)
    WHERE read_at IS NULL;
CREATE INDEX IF NOT EXISTS user_notifications_coalesce_idx
    ON user_notifications(user_id, category, event_kind, collapse_key, outcome, last_occurred_at DESC)
    WHERE read_at IS NULL;
CREATE INDEX IF NOT EXISTS user_notifications_retention_idx
    ON user_notifications(last_occurred_at);

CREATE TABLE IF NOT EXISTS notification_event_receipts (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_key TEXT NOT NULL,
    notification_id TEXT NOT NULL REFERENCES user_notifications(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, event_key),
    CHECK (char_length(event_key) BETWEEN 1 AND 768)
);
CREATE INDEX IF NOT EXISTS notification_event_receipts_notification_idx
    ON notification_event_receipts(notification_id);

CREATE TABLE IF NOT EXISTS notification_source_states (
    source_key TEXT PRIMARY KEY,
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL,
    observed_state TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (char_length(source_key) BETWEEN 1 AND 512),
    CHECK (char_length(resource_id) BETWEEN 1 AND 256),
    CHECK (char_length(observed_state) BETWEEN 1 AND 80)
);
CREATE INDEX IF NOT EXISTS notification_source_states_home_idx
    ON notification_source_states(home_id, source_key);

CREATE TABLE IF NOT EXISTS notification_source_events (
    id TEXT PRIMARY KEY,
    event_key TEXT NOT NULL UNIQUE,
    source_key TEXT NOT NULL REFERENCES notification_source_states(source_key) ON DELETE CASCADE,
    home_id TEXT NOT NULL REFERENCES homes(id) ON DELETE CASCADE,
    resource_id TEXT NOT NULL,
    event_kind TEXT NOT NULL,
    outcome TEXT NOT NULL,
    severity TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL,
    emitted_at TIMESTAMPTZ,
    CHECK (char_length(event_key) BETWEEN 1 AND 768),
    CHECK (char_length(resource_id) BETWEEN 1 AND 256),
    CHECK (char_length(event_kind) BETWEEN 1 AND 80),
    CHECK (char_length(outcome) BETWEEN 1 AND 80),
    CHECK (char_length(severity) BETWEEN 1 AND 80),
    CHECK (char_length(display_name) <= 160)
);
CREATE INDEX IF NOT EXISTS notification_source_events_pending_idx
    ON notification_source_events(occurred_at, id)
    WHERE emitted_at IS NULL;

CREATE TABLE IF NOT EXISTS web_push_subscriptions (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id TEXT NOT NULL REFERENCES app_sessions(id) ON DELETE CASCADE,
    endpoint_fingerprint TEXT NOT NULL UNIQUE,
    encrypted_subscription TEXT NOT NULL,
    browser_label TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    refreshed_at TIMESTAMPTZ NOT NULL,
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    last_failure_code TEXT NOT NULL DEFAULT '',
    CHECK (char_length(endpoint_fingerprint) = 64),
    CHECK (char_length(browser_label) <= 120),
    CHECK (char_length(last_failure_code) <= 80)
);
CREATE INDEX IF NOT EXISTS web_push_subscriptions_user_idx
    ON web_push_subscriptions(user_id, refreshed_at DESC);
CREATE INDEX IF NOT EXISTS web_push_subscriptions_session_idx
    ON web_push_subscriptions(session_id);

CREATE TABLE IF NOT EXISTS web_push_deliveries (
    id TEXT PRIMARY KEY,
    notification_id TEXT NOT NULL REFERENCES user_notifications(id) ON DELETE CASCADE,
    subscription_id TEXT NOT NULL REFERENCES web_push_subscriptions(id) ON DELETE CASCADE,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'claimed', 'delivered', 'cancelled', 'terminal')),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL,
    claim_owner TEXT NOT NULL DEFAULT '',
    claim_expires_at TIMESTAMPTZ,
    last_outcome_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    delivered_at TIMESTAMPTZ,
    UNIQUE (notification_id, subscription_id),
    CHECK (char_length(claim_owner) <= 120),
    CHECK (char_length(last_outcome_code) <= 80)
);
CREATE INDEX IF NOT EXISTS web_push_deliveries_due_idx
    ON web_push_deliveries(state, next_attempt_at)
    WHERE state IN ('pending', 'claimed');
