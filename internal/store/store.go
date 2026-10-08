package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/migrations"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")
var ErrUnsupportedMultiHome = errors.New("multiple homes are no longer supported in a single deployment")

type Store struct {
	db              *sql.DB
	databaseURL     string
	vectorAvailable bool
	secretBox       *secretBox
}

type AgentTokenRecord struct {
	Token domain.AgentToken
	Agent domain.Agent
	Home  domain.Home
}

// Each process leaves PostgreSQL capacity available for the operations worker,
// realtime listener, maintenance, and an administrator during bursts.
const maxDatabaseConnections = 20

func openDatabasePool(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open(driverName, databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxDatabaseConnections)
	db.SetMaxIdleConns(5)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

func Open(ctx context.Context, databaseURL string) (*Store, error) {
	return open(ctx, databaseURL, false)
}

func OpenMigrating(ctx context.Context, databaseURL string) (*Store, error) {
	return open(ctx, databaseURL, true)
}

func BaselineExisting(ctx context.Context, databaseURL string) error {
	db, err := openDatabasePool(databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	return migrations.BaselineExisting(ctx, db, 0)
}

func MigrationStatuses(ctx context.Context, databaseURL string) ([]migrations.Status, error) {
	db, err := openDatabasePool(databaseURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return nil, err
	}
	return migrations.AppliedReadOnly(ctx, db)
}

func CheckMigrations(ctx context.Context, databaseURL string) error {
	db, err := openDatabasePool(databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	return migrations.CheckReadOnly(ctx, db)
}

func open(ctx context.Context, databaseURL string, migrate bool) (*Store, error) {
	db, err := openDatabasePool(databaseURL)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db, databaseURL: databaseURL}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if migrate {
		if err := store.migrate(ctx); err != nil {
			_ = db.Close()
			return nil, err
		}
		store.vectorAvailable = store.detectVectorExtension(ctx)
		if !store.vectorAvailable {
			_ = db.Close()
			return nil, errors.New("required pgvector schema is not available")
		}
		return store, nil
	}

	store.vectorAvailable = store.detectVectorExtension(ctx)
	if err := store.validateExisting(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !store.vectorAvailable {
		_ = db.Close()
		return nil, errors.New("required pgvector schema is not available")
	}

	return store, nil
}

func (s *Store) ConfigureSecretEncryption(key string) error {
	box, err := newSecretBox(key)
	if err != nil {
		return err
	}
	s.secretBox = box
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) MigrationStatuses(ctx context.Context) ([]migrations.Status, error) {
	return migrations.AppliedReadOnly(ctx, s.db)
}

func (s *Store) CheckMigrations(ctx context.Context) error {
	return migrations.CheckReadOnly(ctx, s.db)
}

func (s *Store) RequiredExtensionHealth(ctx context.Context) (migrations.ExtensionHealth, error) {
	return migrations.RequiredExtensionHealth(ctx, s.db)
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) VectorAvailable() bool {
	return s != nil && s.vectorAvailable
}

func (s *Store) detectVectorExtension(ctx context.Context) bool {
	var ready bool
	if err := s.db.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')
		AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'assistant_chunks' AND column_name = 'embedding')
		AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'assistant_file_index' AND column_name = 'embedding')`).Scan(&ready); err != nil {
		return false
	}
	return ready
}

func (s *Store) validateExisting(ctx context.Context) error {
	if err := migrations.CheckReadOnly(ctx, s.db); err != nil {
		return fmt.Errorf("check schema migrations: %w", err)
	}
	if err := migrations.CheckRequiredExtensions(ctx, s.db); err != nil {
		return fmt.Errorf("check required Postgres extensions: %w", err)
	}
	if err := s.validateSingletonHome(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if err := migrations.ApplyPending(ctx, s.db); err != nil {
		return fmt.Errorf("apply schema migrations: %w", err)
	}
	if err := migrations.CheckRequiredExtensions(ctx, s.db); err != nil {
		return fmt.Errorf("check required Postgres extensions: %w", err)
	}

	if err := s.validateSingletonHome(ctx); err != nil {
		return err
	}

	if err := s.seedDefaultHomePermissions(ctx); err != nil {
		return fmt.Errorf("seed default home permissions: %w", err)
	}

	return nil
}

func (s *Store) CreateUser(ctx context.Context, user domain.User) error {
	// Existing callers create password-capable users. SSO-only users are created
	// through the explicit identity path and disabled after their insert.
	user.PasswordLoginEnabled = true
	_, err := s.exec(
		ctx,
		`INSERT INTO users (
				id, email, display_name, password_hash, password_login_enabled, password_change_required, password_changed_at, password_reset_at, password_reset_by, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.ID,
		user.Email,
		user.DisplayName,
		user.PasswordHash,
		user.PasswordLoginEnabled,
		user.PasswordChangeRequired,
		user.PasswordChangedAt,
		user.PasswordResetAt,
		user.PasswordResetBy,
		user.CreatedAt,
		user.UpdatedAt,
	)
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	row := s.queryRow(ctx, `SELECT id, email, display_name, password_hash, password_login_enabled, password_change_required, password_changed_at, password_reset_at, password_reset_by, created_at, updated_at FROM users WHERE email = ?`, email)
	return scanUser(row)
}

func (s *Store) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	row := s.queryRow(ctx, `SELECT id, email, display_name, password_hash, password_login_enabled, password_change_required, password_changed_at, password_reset_at, password_reset_by, created_at, updated_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) UpdateUserDisplayName(ctx context.Context, userID string, displayName string) error {
	result, err := s.exec(ctx, `UPDATE users SET display_name = ?, updated_at = ? WHERE id = ?`, displayName, time.Now().UTC(), userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateUserPassword(ctx context.Context, userID string, passwordHash string, passwordChangeRequired bool, resetBy string, revokeSessions bool, keepSessionID string) error {
	now := time.Now().UTC()
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `UPDATE users
		SET password_hash = ?, password_login_enabled = TRUE,
			password_change_required = ?,
			password_changed_at = CASE WHEN ? THEN password_changed_at ELSE ? END,
			password_reset_at = CASE WHEN ? THEN ? ELSE password_reset_at END,
			password_reset_by = CASE WHEN ? THEN ? ELSE password_reset_by END,
			updated_at = ?
		WHERE id = ?`,
		passwordHash,
		passwordChangeRequired,
		resetBy != "",
		now,
		resetBy != "",
		now,
		resetBy != "",
		resetBy,
		now,
		userID,
	)
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
	if revokeSessions {
		if keepSessionID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE app_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL AND id <> ?`, now, userID, keepSessionID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM apns_devices WHERE user_id = ? AND session_id <> ?`, userID, keepSessionID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE app_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM apns_devices WHERE user_id = ?`, userID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) DisablePasswordLogin(ctx context.Context, userID string) error {
	_, err := s.exec(ctx, `UPDATE users SET password_login_enabled = FALSE, updated_at = ? WHERE id = ?`, time.Now().UTC(), userID)
	return err
}

func (s *Store) CreateHome(ctx context.Context, home domain.Home) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO homes (id, user_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		home.ID,
		home.UserID,
		home.Name,
		home.CreatedAt,
		home.UpdatedAt,
	); err != nil {
		return err
	}

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		home.ID,
		home.UserID,
		domain.HomeRoleAdmin,
		home.CreatedAt,
		home.UpdatedAt,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) GetHomeByID(ctx context.Context, id string) (domain.Home, error) {
	row := s.queryRow(ctx, `SELECT id, user_id, name, agent_enrollment_policy, created_at, updated_at FROM homes WHERE id = ?`, id)
	return scanHome(row)
}

func (s *Store) GetSingletonHome(ctx context.Context) (domain.Home, error) {
	row := s.queryRow(ctx, `SELECT id, user_id, name, agent_enrollment_policy, created_at, updated_at FROM homes ORDER BY created_at ASC LIMIT 1`)
	return scanHome(row)
}

func (s *Store) GetSingletonHomeForUser(ctx context.Context, userID string) (domain.Home, error) {
	row := s.queryRow(ctx, `SELECT h.id, h.user_id, h.name, h.agent_enrollment_policy, h.created_at, h.updated_at
		FROM homes h
		JOIN home_memberships hm ON hm.home_id = h.id
		WHERE hm.user_id = ?
		ORDER BY h.created_at ASC
		LIMIT 1`, userID)
	return scanHome(row)
}

func (s *Store) ListHomesByUser(ctx context.Context, userID string) ([]domain.Home, error) {
	home, err := s.GetSingletonHomeForUser(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []domain.Home{home}, nil
}

func (s *Store) GetHomeForUser(ctx context.Context, homeID string, userID string) (domain.Home, error) {
	home, err := s.GetSingletonHomeForUser(ctx, userID)
	if err != nil {
		return domain.Home{}, err
	}
	if home.ID != homeID {
		return domain.Home{}, ErrNotFound
	}
	return home, nil
}

func (s *Store) BootstrapSingletonHome(ctx context.Context, user domain.User, name string) (domain.Home, bool, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return domain.Home{}, false, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id, user_id, name, agent_enrollment_policy, created_at, updated_at FROM homes ORDER BY created_at ASC LIMIT 2`)
	if err != nil {
		return domain.Home{}, false, err
	}
	defer rows.Close()

	var homes []domain.Home
	for rows.Next() {
		var home domain.Home
		if err := rows.Scan(&home.ID, &home.UserID, &home.Name, &home.AgentEnrollmentPolicy, &home.CreatedAt, &home.UpdatedAt); err != nil {
			return domain.Home{}, false, err
		}
		homes = append(homes, home)
	}
	if err := rows.Err(); err != nil {
		return domain.Home{}, false, err
	}
	if len(homes) > 1 {
		return domain.Home{}, false, ErrUnsupportedMultiHome
	}
	if len(homes) == 1 {
		return homes[0], false, tx.Commit()
	}

	now := time.Now().UTC()
	home := domain.Home{
		ID:        newSingletonHomeID(),
		UserID:    user.ID,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO homes (id, user_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		home.ID,
		home.UserID,
		home.Name,
		home.CreatedAt,
		home.UpdatedAt,
	); err != nil {
		return domain.Home{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		home.ID,
		user.ID,
		domain.HomeRoleAdmin,
		now,
		now,
	); err != nil {
		return domain.Home{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO home_permissions (home_id, homeassistant_enabled, files_enabled, notes_enabled, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?)`,
		home.ID,
		true,
		true,
		true,
		now,
		user.ID,
	); err != nil {
		return domain.Home{}, false, err
	}
	return home, true, tx.Commit()
}

func (s *Store) RenameSingletonHome(ctx context.Context, homeID string, name string) (domain.Home, error) {
	now := time.Now().UTC()
	result, err := s.exec(ctx, `UPDATE homes SET name = ?, updated_at = ? WHERE id = ?`, name, now, homeID)
	if err != nil {
		return domain.Home{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return domain.Home{}, err
	}
	if rows == 0 {
		return domain.Home{}, ErrNotFound
	}
	return s.GetHomeByID(ctx, homeID)
}

func (s *Store) UpdateAgentEnrollmentPolicy(ctx context.Context, homeID, policy string) (domain.Home, error) {
	if policy != "admins_only" && policy != "all_users" {
		return domain.Home{}, errors.New("invalid agent enrollment policy")
	}
	result, err := s.exec(ctx, `UPDATE homes SET agent_enrollment_policy = ?, updated_at = ? WHERE id = ?`, policy, time.Now().UTC(), homeID)
	if err != nil {
		return domain.Home{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return domain.Home{}, err
	}
	if count == 0 {
		return domain.Home{}, ErrNotFound
	}
	return s.GetHomeByID(ctx, homeID)
}

func (s *Store) UpsertAgent(ctx context.Context, agent domain.Agent) error {
	capabilities, err := json.Marshal(agent.Capabilities)
	if err != nil {
		return err
	}
	_, err = s.exec(
		ctx,
		`INSERT INTO agents (id, home_id, name, status, agent_type, installation_id, enrolled_by_user_id, platform, architecture, app_version, installation_mode, capabilities, last_seen_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, COALESCE(NULLIF(?, ''), 'primary'), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		 home_id = excluded.home_id,
		 name = excluded.name,
		 status = excluded.status,
		 agent_type = CASE WHEN ? = '' THEN agents.agent_type ELSE excluded.agent_type END,
		 installation_id = COALESCE(excluded.installation_id, agents.installation_id),
		 enrolled_by_user_id = COALESCE(excluded.enrolled_by_user_id, agents.enrolled_by_user_id),
		 platform = COALESCE(excluded.platform, agents.platform), architecture = COALESCE(excluded.architecture, agents.architecture),
		 app_version = COALESCE(excluded.app_version, agents.app_version), installation_mode = COALESCE(excluded.installation_mode, agents.installation_mode),
		 capabilities = CASE WHEN excluded.capabilities = '[]'::jsonb THEN agents.capabilities ELSE excluded.capabilities END,
		 last_seen_at = excluded.last_seen_at,
		 updated_at = excluded.updated_at`,
		agent.ID,
		agent.HomeID,
		agent.Name,
		agent.Status,
		agent.AgentType,
		agent.InstallationID,
		agent.EnrolledByUserID,
		agent.Platform, agent.Architecture, agent.AppVersion, agent.InstallationMode, capabilities,
		agent.LastSeenAt,
		agent.CreatedAt,
		agent.UpdatedAt,
		agent.AgentType,
	)
	return err
}

func (s *Store) GetAgentByID(ctx context.Context, agentID string) (domain.Agent, error) {
	row := s.queryRow(ctx, `SELECT id, home_id, name, status, agent_type, installation_id, enrolled_by_user_id, platform, architecture, app_version, installation_mode, capabilities, last_seen_at, created_at, updated_at FROM agents WHERE id = ?`, agentID)
	return scanAgent(row)
}

func (s *Store) GetAgentByInstallationID(ctx context.Context, homeID, installationID string) (domain.Agent, error) {
	row := s.queryRow(ctx, `SELECT id, home_id, name, status, agent_type, installation_id, enrolled_by_user_id, platform, architecture, app_version, installation_mode, capabilities, last_seen_at, created_at, updated_at
		FROM agents WHERE home_id = ? AND installation_id = ?`, homeID, installationID)
	return scanAgent(row)
}

func (s *Store) GetAgentByHomeID(ctx context.Context, homeID string) (domain.Agent, error) {
	row := s.queryRow(ctx, `SELECT id, home_id, name, status, agent_type, installation_id, enrolled_by_user_id, platform, architecture, app_version, installation_mode, capabilities, last_seen_at, created_at, updated_at FROM agents WHERE home_id = ?
		 ORDER BY CASE WHEN agent_type = '' OR agent_type = 'primary' THEN 0 ELSE 1 END, created_at ASC LIMIT 1`, homeID)
	return scanAgent(row)
}

func (s *Store) ListAgentsByHome(ctx context.Context, homeID string) ([]domain.Agent, error) {
	rows, err := s.query(ctx, `SELECT id, home_id, name, status, agent_type, installation_id, enrolled_by_user_id, platform, architecture, app_version, installation_mode, capabilities, last_seen_at, created_at, updated_at FROM agents WHERE home_id = ?
		 ORDER BY CASE WHEN agent_type = '' OR agent_type = 'primary' THEN 0 ELSE 1 END, created_at ASC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := make([]domain.Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, agent)
	}
	return agents, rows.Err()
}

func (s *Store) SetAgentStatus(ctx context.Context, agentID string, status string, lastSeenAt *time.Time) error {
	_, err := s.exec(
		ctx,
		`UPDATE agents SET status = ?, last_seen_at = ?, updated_at = ? WHERE id = ?`,
		status,
		lastSeenAt,
		time.Now().UTC(),
		agentID,
	)
	return err
}

// DeleteAgentForHome permanently removes an agent and all credentials that
// could authenticate it. Desktop records and live connection records use
// foreign-key cascades; audit history retains its event but loses the agent
// reference through ON DELETE SET NULL.
func (s *Store) DeleteAgentForHome(ctx context.Context, homeID, agentID string) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// agent_tokens predates the cascading agent foreign key, so delete it
	// explicitly before removing the authoritative agent row.
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_tokens WHERE home_id = ? AND agent_id = ?`, homeID, agentID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE home_id = ? AND id = ?`, homeID, agentID)
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
	return tx.Commit()
}

func (s *Store) CreateAgentToken(ctx context.Context, token domain.AgentToken) error {
	if token.Generation <= 0 {
		token.Generation = 1
	}
	if token.ActivatedAt == nil {
		activatedAt := token.CreatedAt
		token.ActivatedAt = &activatedAt
	}
	if token.ConfirmedAt == nil && token.ReplacesTokenID == "" {
		confirmedAt := token.CreatedAt
		token.ConfirmedAt = &confirmedAt
	}
	_, err := s.exec(
		ctx,
		`INSERT INTO agent_tokens (id, home_id, agent_id, token_hash, generation, replaces_token_id, activated_at, confirmed_at, rotation_requested_at, revoked_at, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token.ID,
		token.HomeID,
		token.AgentID,
		token.TokenHash,
		token.Generation,
		nilIfEmptyString(token.ReplacesTokenID),
		token.ActivatedAt,
		token.ConfirmedAt,
		token.RotationRequestedAt,
		token.RevokedAt,
		token.ExpiresAt,
		token.CreatedAt,
	)
	return err
}

func (s *Store) RevokeAgentToken(ctx context.Context, tokenID string) error {
	_, err := s.exec(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE id = ?`, time.Now().UTC(), tokenID)
	return err
}

func (s *Store) RevokeAgentTokenForHome(ctx context.Context, homeID string, tokenID string) error {
	result, err := s.exec(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE id = ? AND home_id = ?`, time.Now().UTC(), tokenID, homeID)
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

func (s *Store) RevokeAgentTokensForAgent(ctx context.Context, homeID, agentID string, revokedAt time.Time) error {
	_, err := s.exec(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE home_id = ? AND agent_id = ? AND revoked_at IS NULL`, revokedAt, homeID, agentID)
	return err
}

func (s *Store) DeleteAgentTokenForHome(ctx context.Context, homeID string, tokenID string) error {
	result, err := s.exec(ctx, `DELETE FROM agent_tokens WHERE id = ? AND home_id = ?`, tokenID, homeID)
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

func (s *Store) ListAgentTokensByHome(ctx context.Context, homeID string) ([]domain.AgentToken, error) {
	rows, err := s.query(
		ctx,
		`SELECT id, home_id, agent_id, token_hash, generation, COALESCE(replaces_token_id,''), activated_at, confirmed_at, rotation_requested_at, revoked_at, expires_at, created_at
		FROM agent_tokens
		WHERE home_id = ?
		ORDER BY created_at DESC`,
		homeID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []domain.AgentToken
	for rows.Next() {
		token, err := scanAgentToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Store) ValidateAgentToken(ctx context.Context, tokenHash string) (AgentTokenRecord, error) {
	row := s.queryRow(
		ctx,
		`SELECT
			at.id, at.home_id, at.agent_id, at.token_hash, at.generation, COALESCE(at.replaces_token_id,''), at.activated_at, at.confirmed_at, at.rotation_requested_at, at.revoked_at, at.expires_at, at.created_at,
			a.id, a.home_id, a.name, a.status, a.agent_type, a.installation_id, a.enrolled_by_user_id, a.platform, a.architecture, a.app_version, a.installation_mode, a.capabilities, a.last_seen_at, a.created_at, a.updated_at,
			h.id, h.user_id, h.name, h.agent_enrollment_policy, h.created_at, h.updated_at
		FROM agent_tokens at
		JOIN agents a ON a.id = at.agent_id
		JOIN homes h ON h.id = at.home_id
		WHERE at.token_hash = ?`,
		tokenHash,
	)

	record, err := scanAgentTokenRecord(row)
	if err != nil {
		return AgentTokenRecord{}, err
	}

	now := time.Now().UTC()
	if record.Token.RevokedAt != nil {
		return AgentTokenRecord{}, ErrNotFound
	}
	if record.Token.ExpiresAt != nil && record.Token.ExpiresAt.Before(now) {
		return AgentTokenRecord{}, ErrNotFound
	}

	return record, nil
}

func (s *Store) CreateSession(ctx context.Context, session domain.AppSession) error {
	_, err := s.exec(
		ctx,
		`INSERT INTO app_sessions (id, user_id, token_hash, expires_at, revoked_at, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		session.ID,
		session.UserID,
		session.TokenHash,
		session.ExpiresAt,
		session.RevokedAt,
		session.CreatedAt,
	)
	return err
}

func (s *Store) GetSessionByHash(ctx context.Context, tokenHash string) (domain.AppSession, error) {
	row := s.queryRow(ctx, `SELECT id, user_id, token_hash, expires_at, revoked_at, created_at FROM app_sessions WHERE token_hash = ?`, tokenHash)
	session, err := scanSession(row)
	if err != nil {
		return domain.AppSession{}, err
	}

	now := time.Now().UTC()
	if session.RevokedAt != nil || session.ExpiresAt.Before(now) {
		return domain.AppSession{}, ErrNotFound
	}

	return session, nil
}

func (s *Store) GetSessionByID(ctx context.Context, sessionID string) (domain.AppSession, error) {
	row := s.queryRow(ctx, `SELECT id, user_id, token_hash, expires_at, revoked_at, created_at FROM app_sessions WHERE id = ?`, sessionID)
	session, err := scanSession(row)
	if err != nil {
		return domain.AppSession{}, err
	}

	now := time.Now().UTC()
	if session.RevokedAt != nil || session.ExpiresAt.Before(now) {
		return domain.AppSession{}, ErrNotFound
	}

	return session, nil
}

func (s *Store) RevokeSession(ctx context.Context, sessionID string) error {
	_, err := s.exec(ctx, `UPDATE app_sessions SET revoked_at = ? WHERE id = ?`, time.Now().UTC(), sessionID)
	return err
}

func (s *Store) RevokeSessionsForUser(ctx context.Context, userID string, keepSessionID string) error {
	now := time.Now().UTC()
	if keepSessionID != "" {
		_, err := s.exec(ctx, `UPDATE app_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL AND id <> ?`, now, userID, keepSessionID)
		return err
	}
	_, err := s.exec(ctx, `UPDATE app_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, userID)
	return err
}

func scanUser(scanner interface{ Scan(dest ...any) error }) (domain.User, error) {
	var user domain.User
	err := scanner.Scan(
		&user.ID,
		&user.Email,
		&user.DisplayName,
		&user.PasswordHash,
		&user.PasswordLoginEnabled,
		&user.PasswordChangeRequired,
		&user.PasswordChangedAt,
		&user.PasswordResetAt,
		&user.PasswordResetBy,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, ErrNotFound
	}
	return user, err
}

func scanHome(scanner interface{ Scan(dest ...any) error }) (domain.Home, error) {
	var home domain.Home
	err := scanner.Scan(&home.ID, &home.UserID, &home.Name, &home.AgentEnrollmentPolicy, &home.CreatedAt, &home.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Home{}, ErrNotFound
	}
	return home, err
}

func scanAgent(scanner interface{ Scan(dest ...any) error }) (domain.Agent, error) {
	var agent domain.Agent
	var lastSeen sql.NullTime
	var installationID, enrolledByUserID, platform, architecture, appVersion, installationMode sql.NullString
	var capabilities []byte
	err := scanner.Scan(&agent.ID, &agent.HomeID, &agent.Name, &agent.Status, &agent.AgentType, &installationID, &enrolledByUserID, &platform, &architecture, &appVersion, &installationMode, &capabilities, &lastSeen, &agent.CreatedAt, &agent.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Agent{}, ErrNotFound
	}
	if err != nil {
		return domain.Agent{}, err
	}
	if lastSeen.Valid {
		agent.LastSeenAt = &lastSeen.Time
	}
	agent.InstallationID = installationID.String
	agent.EnrolledByUserID = enrolledByUserID.String
	agent.Platform, agent.Architecture, agent.AppVersion, agent.InstallationMode = platform.String, architecture.String, appVersion.String, installationMode.String
	if err := json.Unmarshal(capabilities, &agent.Capabilities); err != nil {
		return domain.Agent{}, err
	}
	return agent, nil
}

func scanSession(scanner interface{ Scan(dest ...any) error }) (domain.AppSession, error) {
	var session domain.AppSession
	var revokedAt sql.NullTime
	err := scanner.Scan(&session.ID, &session.UserID, &session.TokenHash, &session.ExpiresAt, &revokedAt, &session.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AppSession{}, ErrNotFound
	}
	if err != nil {
		return domain.AppSession{}, err
	}
	if revokedAt.Valid {
		session.RevokedAt = &revokedAt.Time
	}
	return session, nil
}

func scanAgentTokenRecord(scanner interface{ Scan(dest ...any) error }) (AgentTokenRecord, error) {
	var record AgentTokenRecord
	var tokenActivatedAt sql.NullTime
	var tokenConfirmedAt sql.NullTime
	var tokenRotationRequestedAt sql.NullTime
	var tokenRevokedAt sql.NullTime
	var tokenExpiresAt sql.NullTime
	var agentLastSeenAt sql.NullTime
	var installationID, enrolledByUserID, platform, architecture, appVersion, installationMode sql.NullString
	var capabilities []byte

	err := scanner.Scan(
		&record.Token.ID,
		&record.Token.HomeID,
		&record.Token.AgentID,
		&record.Token.TokenHash,
		&record.Token.Generation,
		&record.Token.ReplacesTokenID,
		&tokenActivatedAt,
		&tokenConfirmedAt,
		&tokenRotationRequestedAt,
		&tokenRevokedAt,
		&tokenExpiresAt,
		&record.Token.CreatedAt,
		&record.Agent.ID,
		&record.Agent.HomeID,
		&record.Agent.Name,
		&record.Agent.Status,
		&record.Agent.AgentType,
		&installationID,
		&enrolledByUserID,
		&platform,
		&architecture,
		&appVersion,
		&installationMode,
		&capabilities,
		&agentLastSeenAt,
		&record.Agent.CreatedAt,
		&record.Agent.UpdatedAt,
		&record.Home.ID,
		&record.Home.UserID,
		&record.Home.Name,
		&record.Home.AgentEnrollmentPolicy,
		&record.Home.CreatedAt,
		&record.Home.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTokenRecord{}, ErrNotFound
	}
	if err != nil {
		return AgentTokenRecord{}, err
	}
	if tokenRevokedAt.Valid {
		record.Token.RevokedAt = &tokenRevokedAt.Time
	}
	if tokenActivatedAt.Valid {
		record.Token.ActivatedAt = &tokenActivatedAt.Time
	}
	if tokenConfirmedAt.Valid {
		record.Token.ConfirmedAt = &tokenConfirmedAt.Time
	}
	if tokenRotationRequestedAt.Valid {
		record.Token.RotationRequestedAt = &tokenRotationRequestedAt.Time
	}
	if tokenExpiresAt.Valid {
		record.Token.ExpiresAt = &tokenExpiresAt.Time
	}
	if agentLastSeenAt.Valid {
		record.Agent.LastSeenAt = &agentLastSeenAt.Time
	}
	record.Agent.InstallationID = installationID.String
	record.Agent.EnrolledByUserID = enrolledByUserID.String
	record.Agent.Platform, record.Agent.Architecture, record.Agent.AppVersion, record.Agent.InstallationMode = platform.String, architecture.String, appVersion.String, installationMode.String
	if err := json.Unmarshal(capabilities, &record.Agent.Capabilities); err != nil {
		return AgentTokenRecord{}, err
	}
	return record, nil
}

func scanAgentToken(scanner interface{ Scan(dest ...any) error }) (domain.AgentToken, error) {
	var token domain.AgentToken
	var activatedAt sql.NullTime
	var confirmedAt sql.NullTime
	var rotationRequestedAt sql.NullTime
	var revokedAt sql.NullTime
	var expiresAt sql.NullTime
	err := scanner.Scan(&token.ID, &token.HomeID, &token.AgentID, &token.TokenHash, &token.Generation, &token.ReplacesTokenID, &activatedAt, &confirmedAt, &rotationRequestedAt, &revokedAt, &expiresAt, &token.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentToken{}, ErrNotFound
	}
	if err != nil {
		return domain.AgentToken{}, err
	}
	if revokedAt.Valid {
		token.RevokedAt = &revokedAt.Time
	}
	if activatedAt.Valid {
		token.ActivatedAt = &activatedAt.Time
	}
	if confirmedAt.Valid {
		token.ConfirmedAt = &confirmedAt.Time
	}
	if rotationRequestedAt.Valid {
		token.RotationRequestedAt = &rotationRequestedAt.Time
	}
	if expiresAt.Valid {
		token.ExpiresAt = &expiresAt.Time
	}
	return token, nil
}

func (s *Store) validateSingletonHome(ctx context.Context) error {
	rows, err := s.query(ctx, `SELECT id FROM homes ORDER BY created_at ASC LIMIT 2`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count > 1 {
		return fmt.Errorf("%w: found more than one home in homes table; consolidate to a single deployment home before starting this version", ErrUnsupportedMultiHome)
	}
	return nil
}

func (s *Store) seedDefaultHomePermissions(ctx context.Context) error {
	_, err := s.exec(ctx, `INSERT INTO home_permissions (home_id, homeassistant_enabled, files_enabled, notes_enabled, updated_at, updated_by)
		SELECT h.id, TRUE, TRUE, TRUE, h.updated_at, hm.user_id
		FROM homes h
		JOIN home_memberships hm ON hm.home_id = h.id
		WHERE hm.role = ?
			AND NOT EXISTS (
				SELECT 1 FROM home_permissions hp WHERE hp.home_id = h.id
			)
		ORDER BY h.created_at ASC
		LIMIT 1`, domain.HomeRoleAdmin)
	return err
}

func newSingletonHomeID() string {
	return "home_default"
}
