package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
)

const homeAgentAppColumns = `home_id, app_id, name, version, enabled, public_config_json, secret_fields_set_json, settings_schema_json, capabilities_json, slash_commands_json, commands_json, permissions_json, user_access, status, last_error, updated_at, updated_by`

func (s *Store) UpsertHomeApp(ctx context.Context, app domain.HomeAgentApp) error {
	if strings.TrimSpace(app.PermissionsJSON) == "" {
		app.PermissionsJSON = "{}"
	}
	_, err := s.exec(ctx, `INSERT INTO home_agent_apps (
			home_id, app_id, name, version, enabled, public_config_json, secret_fields_set_json, settings_schema_json, capabilities_json, slash_commands_json, commands_json, permissions_json, user_access, status, last_error, updated_at, updated_by
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id, app_id) DO UPDATE SET
			name = excluded.name,
			version = excluded.version,
			enabled = excluded.enabled,
			public_config_json = excluded.public_config_json,
			secret_fields_set_json = excluded.secret_fields_set_json,
			settings_schema_json = excluded.settings_schema_json,
			capabilities_json = excluded.capabilities_json,
			slash_commands_json = excluded.slash_commands_json,
			commands_json = excluded.commands_json,
 permissions_json = excluded.permissions_json,
			user_access = excluded.user_access,
			status = excluded.status,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by`,
		app.HomeID,
		app.AppID,
		app.Name,
		app.Version,
		app.Enabled,
		app.PublicConfigJSON,
		app.SecretFieldsSetJSON,
		app.SettingsSchemaJSON,
		app.CapabilitiesJSON,
		app.SlashCommandsJSON,
		app.CommandsJSON,
		app.PermissionsJSON,
		normalizeHomeAgentAppUserAccess(app.UserAccess),
		app.Status,
		app.LastError,
		app.UpdatedAt,
		app.UpdatedBy,
	)
	return err
}

func (s *Store) GetHomeApp(ctx context.Context, homeID string, appID string) (domain.HomeAgentApp, error) {
	row := s.queryRow(ctx, `SELECT `+homeAgentAppColumns+`
		FROM home_agent_apps
		WHERE home_id = ? AND app_id = ?`, homeID, appID)
	return scanHomeAgentApp(row)
}

func (s *Store) ListHomeApps(ctx context.Context, homeID string) ([]domain.HomeAgentApp, error) {
	rows, err := s.query(ctx, `SELECT `+homeAgentAppColumns+`
		FROM home_agent_apps
		WHERE home_id = ?
		ORDER BY name ASC, app_id ASC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []domain.HomeAgentApp
	for rows.Next() {
		app, err := scanHomeAgentApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (s *Store) DeleteHomeApp(ctx context.Context, homeID string, appID string) error {
	result, err := s.exec(ctx, `DELETE FROM home_agent_apps WHERE home_id = ? AND app_id = ?`, homeID, appID)
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

func scanHomeAgentApp(scanner interface{ Scan(dest ...any) error }) (domain.HomeAgentApp, error) {
	var app domain.HomeAgentApp
	err := scanner.Scan(
		&app.HomeID,
		&app.AppID,
		&app.Name,
		&app.Version,
		&app.Enabled,
		&app.PublicConfigJSON,
		&app.SecretFieldsSetJSON,
		&app.SettingsSchemaJSON,
		&app.CapabilitiesJSON,
		&app.SlashCommandsJSON,
		&app.CommandsJSON,
		&app.PermissionsJSON,
		&app.UserAccess,
		&app.Status,
		&app.LastError,
		&app.UpdatedAt,
		&app.UpdatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomeAgentApp{}, ErrNotFound
	}
	app.UserAccess = normalizeHomeAgentAppUserAccess(app.UserAccess)
	return app, err
}

func normalizeHomeAgentAppUserAccess(value string) string {
	switch strings.TrimSpace(value) {
	case domain.HomeAgentAppUserAccessHomeMembers:
		return domain.HomeAgentAppUserAccessHomeMembers
	default:
		return domain.HomeAgentAppUserAccessAdminsOnly
	}
}
