package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

var credentialHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	agentCredentialRotationInterval = 30 * 24 * time.Hour
	agentCredentialOverlap          = 24 * time.Hour
)

type ConsumeAgentEnrollmentInput struct {
	TokenHash, AgentID, Name, CredentialID, CredentialHash string
	Platform, InstallationID, EnrolledByUserID             string
	Now                                                    time.Time
}

func (s *Store) CreateAgentEnrollment(ctx context.Context, value domain.AgentEnrollment) error {
	if value.ID == "" || value.HomeID == "" || (value.Platform != "linux" && value.Platform != "macos" && value.Platform != "windows") || !credentialHashPattern.MatchString(value.TokenHash) || value.CreatedByUserID == "" || value.CreatedAt.IsZero() || !value.ExpiresAt.After(value.CreatedAt) {
		return errors.New("invalid agent enrollment")
	}
	labels, err := json.Marshal(value.Labels)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO agent_enrollments
		(id, home_id, platform, token_hash, created_by_user_id, name_hint, labels, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.HomeID, value.Platform, value.TokenHash, value.CreatedByUserID, value.NameHint, labels, value.CreatedAt, value.ExpiresAt)
	return err
}

func (s *Store) ListAgentEnrollments(ctx context.Context, homeID string) ([]domain.AgentEnrollment, error) {
	rows, err := s.query(ctx, `SELECT id, home_id, platform, token_hash, created_by_user_id, COALESCE(name_hint,''), labels,
		created_at, expires_at, downloaded_at, consumed_at, revoked_at, COALESCE(agent_id,''), download_count, failed_attempt_count
		FROM agent_enrollments WHERE home_id = ? ORDER BY created_at DESC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []domain.AgentEnrollment
	for rows.Next() {
		value, err := scanAgentEnrollment(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) GetActiveAgentEnrollmentByHash(ctx context.Context, tokenHash string, now time.Time) (domain.AgentEnrollment, error) {
	row := s.queryRow(ctx, `SELECT id, home_id, platform, token_hash, created_by_user_id, COALESCE(name_hint,''), labels,
		created_at, expires_at, downloaded_at, consumed_at, revoked_at, COALESCE(agent_id,''), download_count, failed_attempt_count
		FROM agent_enrollments WHERE token_hash = ? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?`, tokenHash, now)
	return scanAgentEnrollment(row)
}

func (s *Store) MarkAgentEnrollmentDownloaded(ctx context.Context, tokenHash string, now time.Time) error {
	result, err := s.exec(ctx, `UPDATE agent_enrollments SET downloaded_at = COALESCE(downloaded_at, ?), download_count = download_count + 1
		WHERE token_hash = ? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?`, now, tokenHash, now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RevokeAgentEnrollment(ctx context.Context, homeID, enrollmentID string, now time.Time) error {
	result, err := s.exec(ctx, `UPDATE agent_enrollments SET revoked_at = ? WHERE id = ? AND home_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, now, enrollmentID, homeID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ConsumeAgentEnrollment(ctx context.Context, input ConsumeAgentEnrollmentInput) (domain.AgentEnrollment, error) {
	if !credentialHashPattern.MatchString(input.TokenHash) || !credentialHashPattern.MatchString(input.CredentialHash) || input.AgentID == "" || input.Name == "" || input.CredentialID == "" || input.Now.IsZero() {
		return domain.AgentEnrollment{}, errors.New("invalid agent enrollment consumption")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.AgentEnrollment{}, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT id, home_id, platform, token_hash, created_by_user_id, COALESCE(name_hint,''), labels,
		created_at, expires_at, downloaded_at, consumed_at, revoked_at, COALESCE(agent_id,''), download_count, failed_attempt_count
		FROM agent_enrollments WHERE token_hash = ? FOR UPDATE`, input.TokenHash)
	enrollment, err := scanAgentEnrollment(row)
	if err != nil {
		return domain.AgentEnrollment{}, mapAgentEnrollmentConsumeError(err)
	}
	if (input.Platform != "" && enrollment.Platform != input.Platform) || enrollment.ConsumedAt != nil || enrollment.RevokedAt != nil || !enrollment.ExpiresAt.After(input.Now) {
		return domain.AgentEnrollment{}, ErrNotFound
	}
	installationID := strings.TrimSpace(input.InstallationID)
	if installationID == "" {
		installationID = input.AgentID
	}
	enrolledBy := strings.TrimSpace(input.EnrolledByUserID)
	if enrolledBy == "" {
		enrolledBy = enrollment.CreatedByUserID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agents (id, home_id, name, status, agent_type, installation_id, enrolled_by_user_id, created_at, updated_at)
		VALUES (?, ?, ?, 'offline', 'worker', ?, ?, ?, ?)`, input.AgentID, enrollment.HomeID, input.Name, installationID, enrolledBy, input.Now, input.Now)
	if err != nil {
		return domain.AgentEnrollment{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_tokens (id, home_id, agent_id, token_hash, generation, activated_at, confirmed_at, created_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?)`, input.CredentialID, enrollment.HomeID, input.AgentID, input.CredentialHash, input.Now, input.Now, input.Now)
	if err != nil {
		return domain.AgentEnrollment{}, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_enrollments SET consumed_at = ?, agent_id = ? WHERE id = ?`, input.Now, input.AgentID, enrollment.ID)
	if err != nil {
		return domain.AgentEnrollment{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentEnrollment{}, mapAgentEnrollmentConsumeError(err)
	}
	enrollment.ConsumedAt, enrollment.AgentID = &input.Now, input.AgentID
	return enrollment, nil
}

func mapAgentEnrollmentConsumeError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "40001" {
		return ErrNotFound
	}
	return err
}

type BeginAgentCredentialRotationInput struct {
	HomeID, AgentID, CurrentTokenID, ReplacementTokenID, ReplacementHash string
	Now                                                                  time.Time
}

func (s *Store) BeginAgentCredentialRotation(ctx context.Context, input BeginAgentCredentialRotationInput) (domain.AgentCredentialState, error) {
	if input.HomeID == "" || input.AgentID == "" || input.CurrentTokenID == "" || input.ReplacementTokenID == "" || !credentialHashPattern.MatchString(input.ReplacementHash) || input.Now.IsZero() {
		return domain.AgentCredentialState{}, errors.New("invalid credential rotation")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.AgentCredentialState{}, err
	}
	defer tx.Rollback()

	currentRow := tx.QueryRowContext(ctx, `SELECT id, agent_id, generation, COALESCE(replaces_token_id,''), COALESCE(activated_at, created_at), confirmed_at, expires_at, rotation_requested_at
		FROM agent_tokens WHERE id = ? AND home_id = ? AND agent_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?) FOR UPDATE`, input.CurrentTokenID, input.HomeID, input.AgentID, input.Now)
	current, err := scanAgentCredentialState(currentRow, input.Now)
	if err != nil {
		return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
	}
	if current.ConfirmBy != nil && !current.ConfirmBy.After(input.Now) {
		return domain.AgentCredentialState{}, ErrNotFound
	}

	pendingRow := tx.QueryRowContext(ctx, `SELECT id, agent_id, generation, COALESCE(replaces_token_id,''), COALESCE(activated_at, created_at), confirmed_at, expires_at, rotation_requested_at, token_hash
		FROM agent_tokens WHERE home_id = ? AND agent_id = ? AND replaces_token_id = ? AND confirmed_at IS NULL AND revoked_at IS NULL`, input.HomeID, input.AgentID, current.CredentialID)
	var pending domain.AgentCredentialState
	var pendingConfirmed, pendingExpires, pendingRequested sql.NullTime
	var pendingHash string
	err = pendingRow.Scan(&pending.CredentialID, &pending.AgentID, &pending.Generation, &pending.ReplacesCredentialID, &pending.ActivatedAt, &pendingConfirmed, &pendingExpires, &pendingRequested, &pendingHash)
	if err == nil {
		if pendingHash != input.ReplacementHash {
			return domain.AgentCredentialState{}, ErrConflict
		}
		populateAgentCredentialState(&pending, pendingConfirmed, pendingExpires, pendingRequested, input.Now)
		return pending, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
	}

	confirmBy := input.Now.Add(agentCredentialOverlap)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET expires_at = CASE WHEN expires_at IS NULL OR expires_at > ? THEN ? ELSE expires_at END, rotation_requested_at = NULL WHERE id = ?`, confirmBy, confirmBy, current.CredentialID); err != nil {
		return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tokens
		(id, home_id, agent_id, token_hash, generation, replaces_token_id, activated_at, confirmed_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?)`, input.ReplacementTokenID, input.HomeID, input.AgentID, input.ReplacementHash, current.Generation+1, current.CredentialID, input.Now, input.Now); err != nil {
		return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
	}
	rotationDueAt := input.Now.Add(agentCredentialRotationInterval)
	return domain.AgentCredentialState{CredentialID: input.ReplacementTokenID, AgentID: input.AgentID, Generation: current.Generation + 1, ReplacesCredentialID: current.CredentialID, ActivatedAt: input.Now, ConfirmBy: &confirmBy, RotationDueAt: rotationDueAt}, nil
}

func (s *Store) ConfirmAgentCredentialRotation(ctx context.Context, homeID, agentID, replacementTokenID, authenticatedTokenID string, now time.Time) (domain.AgentCredentialState, error) {
	if homeID == "" || agentID == "" || replacementTokenID == "" || authenticatedTokenID != replacementTokenID || now.IsZero() {
		return domain.AgentCredentialState{}, ErrNotFound
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.AgentCredentialState{}, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `SELECT id, agent_id, generation, COALESCE(replaces_token_id,''), COALESCE(activated_at, created_at), confirmed_at, expires_at, rotation_requested_at
		FROM agent_tokens WHERE id = ? AND home_id = ? AND agent_id = ? AND replaces_token_id IS NOT NULL AND revoked_at IS NULL FOR UPDATE`, replacementTokenID, homeID, agentID)
	state, err := scanAgentCredentialState(row, now)
	if err != nil {
		return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
	}
	if state.ConfirmBy != nil && !state.ConfirmBy.After(now) {
		return domain.AgentCredentialState{}, ErrNotFound
	}
	if state.ConfirmedAt == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET confirmed_at = ?, expires_at = NULL WHERE id = ?`, now, replacementTokenID); err != nil {
			return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE home_id = ? AND agent_id = ? AND id <> ? AND revoked_at IS NULL`, now, homeID, agentID, replacementTokenID); err != nil {
			return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
		}
		state.ConfirmedAt = &now
		state.ConfirmBy = nil
	}
	if err := tx.Commit(); err != nil {
		return domain.AgentCredentialState{}, mapAgentEnrollmentConsumeError(err)
	}
	return state, nil
}

func (s *Store) RequestAgentCredentialRotation(ctx context.Context, homeID, agentID string, now time.Time) (domain.AgentCredentialState, error) {
	if homeID == "" || agentID == "" || now.IsZero() {
		return domain.AgentCredentialState{}, errors.New("invalid credential rotation request")
	}
	var tokenID string
	err := s.queryRow(ctx, `SELECT id FROM agent_tokens WHERE home_id = ? AND agent_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY generation DESC, created_at DESC LIMIT 1`, homeID, agentID, now).Scan(&tokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentCredentialState{}, ErrNotFound
	}
	if err != nil {
		return domain.AgentCredentialState{}, err
	}
	if _, err := s.exec(ctx, `UPDATE agent_tokens SET rotation_requested_at = ? WHERE id = ?`, now, tokenID); err != nil {
		return domain.AgentCredentialState{}, err
	}
	return s.GetAgentCredentialState(ctx, homeID, agentID, tokenID, now)
}

func (s *Store) GetAgentCredentialState(ctx context.Context, homeID, agentID, tokenID string, now time.Time) (domain.AgentCredentialState, error) {
	row := s.queryRow(ctx, `SELECT id, agent_id, generation, COALESCE(replaces_token_id,''), COALESCE(activated_at, created_at), confirmed_at, expires_at, rotation_requested_at
		FROM agent_tokens WHERE id = ? AND home_id = ? AND agent_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`, tokenID, homeID, agentID, now)
	return scanAgentCredentialState(row, now)
}

func (s *Store) RevokeAgentCredentials(ctx context.Context, homeID, agentID string, now time.Time) error {
	result, err := s.exec(ctx, `UPDATE agent_tokens SET revoked_at = ? WHERE home_id = ? AND agent_id = ? AND revoked_at IS NULL`, now, homeID, agentID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func scanAgentCredentialState(row rowScanner, now time.Time) (domain.AgentCredentialState, error) {
	var state domain.AgentCredentialState
	var confirmed, expires, requested sql.NullTime
	err := row.Scan(&state.CredentialID, &state.AgentID, &state.Generation, &state.ReplacesCredentialID, &state.ActivatedAt, &confirmed, &expires, &requested)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentCredentialState{}, ErrNotFound
	}
	if err != nil {
		return domain.AgentCredentialState{}, err
	}
	populateAgentCredentialState(&state, confirmed, expires, requested, now)
	return state, nil
}

func populateAgentCredentialState(state *domain.AgentCredentialState, confirmed, expires, requested sql.NullTime, now time.Time) {
	if confirmed.Valid {
		state.ConfirmedAt = &confirmed.Time
	}
	if expires.Valid && state.ConfirmedAt == nil {
		state.ConfirmBy = &expires.Time
	}
	if requested.Valid {
		state.RotationRequestedAt = &requested.Time
		state.RotationRequested = true
	}
	state.RotationDueAt = state.ActivatedAt.Add(agentCredentialRotationInterval)
	state.RotationDue = !state.RotationDueAt.After(now)
}

type rowScanner interface{ Scan(...any) error }

func scanAgentEnrollment(row rowScanner) (domain.AgentEnrollment, error) {
	var value domain.AgentEnrollment
	var labels []byte
	err := row.Scan(&value.ID, &value.HomeID, &value.Platform, &value.TokenHash, &value.CreatedByUserID, &value.NameHint, &labels,
		&value.CreatedAt, &value.ExpiresAt, &value.DownloadedAt, &value.ConsumedAt, &value.RevokedAt, &value.AgentID, &value.DownloadCount, &value.FailedAttemptCount)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentEnrollment{}, ErrNotFound
	}
	if err != nil {
		return domain.AgentEnrollment{}, err
	}
	if len(labels) > 0 {
		if err := json.Unmarshal(labels, &value.Labels); err != nil {
			return domain.AgentEnrollment{}, err
		}
	}
	return value, nil
}
