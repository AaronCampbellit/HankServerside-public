package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) GetNotificationSettings(ctx context.Context, userID string) (domain.NotificationSettings, error) {
	row := s.queryRow(ctx, `SELECT user_id, agent_health_enabled, quick_links_enabled, storage_enabled, notes_enabled, dashboard_entities_enabled, monitoring_enabled, updated_at
		FROM notification_settings
		WHERE user_id = ?`, userID)
	return scanNotificationSettings(row)
}

func (s *Store) SaveNotificationSettings(ctx context.Context, settings domain.NotificationSettings) (domain.NotificationSettings, error) {
	settings.UserID = strings.TrimSpace(settings.UserID)
	settings.UpdatedAt = time.Now().UTC()
	_, err := s.exec(ctx, `INSERT INTO notification_settings (
			user_id, agent_health_enabled, quick_links_enabled, storage_enabled, notes_enabled, dashboard_entities_enabled, monitoring_enabled, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			agent_health_enabled = excluded.agent_health_enabled,
			quick_links_enabled = excluded.quick_links_enabled,
			storage_enabled = excluded.storage_enabled,
			notes_enabled = excluded.notes_enabled,
			dashboard_entities_enabled = excluded.dashboard_entities_enabled,
			monitoring_enabled = excluded.monitoring_enabled,
			updated_at = excluded.updated_at`,
		settings.UserID,
		settings.AgentHealthEnabled,
		settings.QuickLinksEnabled,
		settings.StorageEnabled,
		settings.NotesEnabled,
		settings.DashboardEntitiesEnabled,
		settings.MonitoringEnabled,
		settings.UpdatedAt,
	)
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	return settings, nil
}

func (s *Store) UpsertAPNSDevice(ctx context.Context, device domain.APNSDevice) (domain.APNSDevice, error) {
	now := time.Now().UTC()
	if device.CreatedAt.IsZero() {
		device.CreatedAt = now
	}
	device.UpdatedAt = now
	device.LastRegisteredAt = now
	if len(device.EnabledCategories) == 0 || !json.Valid(device.EnabledCategories) {
		device.EnabledCategories = json.RawMessage(`[]`)
	}
	storedToken, err := s.encryptSecret(device.Token)
	if err != nil {
		return domain.APNSDevice{}, err
	}
	_, err = s.exec(ctx, `INSERT INTO apns_devices (
			user_id, session_id, device_id, token, environment, bundle_id, enabled_categories,
			created_at, updated_at, last_registered_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, device_id) DO UPDATE SET
			session_id = excluded.session_id,
			token = excluded.token,
			environment = excluded.environment,
			bundle_id = excluded.bundle_id,
			enabled_categories = excluded.enabled_categories,
			updated_at = excluded.updated_at,
			last_registered_at = excluded.last_registered_at`,
		device.UserID,
		device.SessionID,
		device.DeviceID,
		storedToken,
		device.Environment,
		device.BundleID,
		string(device.EnabledCategories),
		device.CreatedAt,
		device.UpdatedAt,
		device.LastRegisteredAt,
	)
	if err != nil {
		return domain.APNSDevice{}, err
	}
	return device, nil
}

func (s *Store) DeleteAPNSDevice(ctx context.Context, userID string, deviceID string) error {
	result, err := s.exec(ctx, `DELETE FROM apns_devices WHERE user_id = ? AND device_id = ?`, userID, deviceID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAPNSDevicesForSession(ctx context.Context, sessionID string) error {
	_, err := s.exec(ctx, `DELETE FROM apns_devices WHERE session_id = ?`, sessionID)
	return err
}

func (s *Store) ListActiveAPNSDevicesForUsers(ctx context.Context, userIDs []string) ([]domain.APNSDevice, error) {
	userIDs = cleanUniqueStrings(userIDs)
	if len(userIDs) == 0 {
		return nil, nil
	}
	query := `SELECT d.user_id, d.session_id, d.device_id, d.token, d.environment, d.bundle_id,
			d.enabled_categories, d.created_at, d.updated_at, d.last_registered_at
		FROM apns_devices d
		JOIN app_sessions s ON s.id = d.session_id
		WHERE d.user_id IN (` + placeholders(len(userIDs)) + `)
			AND s.revoked_at IS NULL
			AND s.expires_at > ?
		ORDER BY d.updated_at DESC`
	args := make([]any, 0, len(userIDs)+1)
	for _, userID := range userIDs {
		args = append(args, userID)
	}
	args = append(args, time.Now().UTC())
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devices []domain.APNSDevice
	for rows.Next() {
		device, err := scanAPNSDevice(rows)
		if err != nil {
			return nil, err
		}
		device.Token, err = s.decryptSecret(device.Token)
		if err != nil {
			return nil, err
		}
		devices = append(devices, device)
	}
	return devices, rows.Err()
}

func (s *Store) ListNotificationSettingsForUsers(ctx context.Context, userIDs []string) (map[string]domain.NotificationSettings, error) {
	userIDs = cleanUniqueStrings(userIDs)
	result := map[string]domain.NotificationSettings{}
	if len(userIDs) == 0 {
		return result, nil
	}
	query := `SELECT user_id, agent_health_enabled, quick_links_enabled, storage_enabled, notes_enabled, dashboard_entities_enabled, monitoring_enabled, updated_at
		FROM notification_settings
		WHERE user_id IN (` + placeholders(len(userIDs)) + `)`
	args := make([]any, 0, len(userIDs))
	for _, userID := range userIDs {
		args = append(args, userID)
	}
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		settings, err := scanNotificationSettings(rows)
		if err != nil {
			return nil, err
		}
		result[settings.UserID] = settings
	}
	return result, rows.Err()
}

func (s *Store) ListStorageNotificationUserIDs(ctx context.Context, homeID string) ([]string, error) {
	rows, err := s.query(ctx, `SELECT user_id
		FROM home_memberships
		WHERE home_id = ? AND role = ?
		ORDER BY created_at ASC`, homeID, domain.HomeRoleAdmin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func (s *Store) ListNoteNotificationUserIDs(ctx context.Context, noteInternalID string, actorUserID string) ([]string, error) {
	noteInternalID = strings.TrimSpace(noteInternalID)
	if noteInternalID == "" {
		return nil, nil
	}
	rows, err := s.query(ctx, `WITH recipients AS (
			SELECT owner_user_id AS user_id
			FROM user_notes
			WHERE id = ?
			UNION
			SELECT target_user_id AS user_id
			FROM note_shares
			WHERE note_id = ?
		)
		SELECT DISTINCT user_id
		FROM recipients
		WHERE user_id <> ?
		ORDER BY user_id ASC`, noteInternalID, noteInternalID, actorUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func (s *Store) ListDashboardEntityNotificationUserIDs(ctx context.Context, homeID string, entityID string) ([]string, error) {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, nil
	}
	rows, err := s.query(ctx, `SELECT DISTINCT ups.user_id
		FROM user_profile_settings ups
		JOIN home_memberships hm ON hm.user_id = ups.user_id
		CROSS JOIN LATERAL jsonb_array_elements(COALESCE(ups.settings_json->'dashboard_tiles', '[]'::jsonb)) AS tile
		WHERE hm.home_id = ?
			AND tile->>'entity_id' = ?
			AND COALESCE(tile->>'is_enabled', 'true') <> 'false'
		ORDER BY ups.user_id ASC`, homeID, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStringRows(rows)
}

func scanNotificationSettings(scanner interface{ Scan(dest ...any) error }) (domain.NotificationSettings, error) {
	var settings domain.NotificationSettings
	err := scanner.Scan(
		&settings.UserID,
		&settings.AgentHealthEnabled,
		&settings.QuickLinksEnabled,
		&settings.StorageEnabled,
		&settings.NotesEnabled,
		&settings.DashboardEntitiesEnabled,
		&settings.MonitoringEnabled,
		&settings.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return domain.NotificationSettings{}, ErrNotFound
	}
	if err != nil {
		return domain.NotificationSettings{}, err
	}
	return settings, nil
}

func scanAPNSDevice(scanner interface{ Scan(dest ...any) error }) (domain.APNSDevice, error) {
	var device domain.APNSDevice
	var categories string
	err := scanner.Scan(
		&device.UserID,
		&device.SessionID,
		&device.DeviceID,
		&device.Token,
		&device.Environment,
		&device.BundleID,
		&categories,
		&device.CreatedAt,
		&device.UpdatedAt,
		&device.LastRegisteredAt,
	)
	if err == sql.ErrNoRows {
		return domain.APNSDevice{}, ErrNotFound
	}
	if err != nil {
		return domain.APNSDevice{}, err
	}
	device.EnabledCategories = json.RawMessage(categories)
	return device, nil
}

func scanStringRows(rows *sql.Rows) ([]string, error) {
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, value)
		}
	}
	return values, rows.Err()
}

func placeholders(count int) string {
	parts := make([]string, count)
	for index := range parts {
		parts[index] = "?"
	}
	return strings.Join(parts, ",")
}

func cleanUniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	var cleaned []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	return cleaned
}
