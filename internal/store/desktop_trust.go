package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

const desktopIdentityColumns = `id, home_id, identity_type, user_id, device_id, agent_id,
	public_key_spki, certificate, fingerprint, to_json(capabilities)::text, trust_root_generation,
	created_at, expires_at, revoked_at, revocation_reason, auth_session_id`

func (s *Store) UpsertDesktopPendingEnrollment(ctx context.Context, value domain.DesktopPendingEnrollment) error {
	if value.HomeID == "" || value.AgentID == "" || len(value.RequestJSON) == 0 || value.Fingerprint == "" || value.CreatedAt.IsZero() || !value.ExpiresAt.After(value.CreatedAt) {
		return errors.New("invalid desktop pending enrollment")
	}
	_, err := s.exec(ctx, `INSERT INTO desktop_pending_enrollments (home_id, agent_id, request_json, fingerprint, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (home_id, agent_id) DO UPDATE SET request_json=EXCLUDED.request_json, fingerprint=EXCLUDED.fingerprint, created_at=EXCLUDED.created_at, expires_at=EXCLUDED.expires_at`, value.HomeID, value.AgentID, value.RequestJSON, value.Fingerprint, value.CreatedAt, value.ExpiresAt)
	return mapDesktopStoreError(err)
}

func (s *Store) GetDesktopPendingEnrollment(ctx context.Context, homeID, agentID string, now time.Time) (domain.DesktopPendingEnrollment, error) {
	row := s.queryRow(ctx, `SELECT home_id, agent_id, request_json, fingerprint, created_at, expires_at FROM desktop_pending_enrollments WHERE home_id=? AND agent_id=? AND expires_at>?`, homeID, agentID, now)
	var value domain.DesktopPendingEnrollment
	err := row.Scan(&value.HomeID, &value.AgentID, &value.RequestJSON, &value.Fingerprint, &value.CreatedAt, &value.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DesktopPendingEnrollment{}, ErrNotFound
	}
	return value, err
}

func (s *Store) DeleteDesktopPendingEnrollment(ctx context.Context, homeID, agentID string) error {
	_, err := s.exec(ctx, `DELETE FROM desktop_pending_enrollments WHERE home_id=? AND agent_id=?`, homeID, agentID)
	return mapDesktopStoreError(err)
}

func (s *Store) DeleteDesktopPendingEnrollmentsForHome(ctx context.Context, homeID string) error {
	if strings.TrimSpace(homeID) == "" {
		return errors.New("invalid desktop pending enrollment home")
	}
	_, err := s.exec(ctx, `DELETE FROM desktop_pending_enrollments WHERE home_id=?`, homeID)
	return mapDesktopStoreError(err)
}

func (s *Store) BootstrapDesktopTrust(ctx context.Context, root domain.DesktopTrustRoot, firstOperator domain.DesktopIdentity) error {
	if err := validateDesktopTrustRoot(root); err != nil {
		return err
	}
	if root.Generation != 1 {
		return errors.New("desktop trust bootstrap requires generation 1")
	}
	if err := validateDesktopIdentity(firstOperator); err != nil {
		return err
	}
	if firstOperator.IdentityType != domain.DesktopIdentityOperatorDevice ||
		firstOperator.HomeID != root.HomeID ||
		firstOperator.TrustRootGeneration != root.Generation {
		return errors.New("first desktop operator does not match the trust root")
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertDesktopTrustRoot(ctx, tx, root); err != nil {
		return mapDesktopStoreError(err)
	}
	if err := insertDesktopIdentity(ctx, tx, firstOperator); err != nil {
		return mapDesktopStoreError(err)
	}
	return mapDesktopStoreError(tx.Commit())
}

func (s *Store) GetDesktopTrustRoot(ctx context.Context, homeID string) (domain.DesktopTrustRoot, error) {
	row := s.queryRow(ctx, `SELECT home_id, generation, algorithm, public_key_spki, fingerprint,
			recovery_envelope, created_at, rotated_at, authority_mode, encrypted_private_key
		FROM desktop_trust_roots
		WHERE home_id = ?`, homeID)
	var root domain.DesktopTrustRoot
	var recoveryEnvelope sql.Null[[]byte]
	var encryptedPrivateKey sql.NullString
	err := row.Scan(
		&root.HomeID,
		&root.Generation,
		&root.Algorithm,
		&root.PublicKeySPKI,
		&root.Fingerprint,
		&recoveryEnvelope,
		&root.CreatedAt,
		&root.RotatedAt,
		&root.AuthorityMode,
		&encryptedPrivateKey,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DesktopTrustRoot{}, ErrNotFound
	}
	root.RecoveryEnvelope = recoveryEnvelope.V
	root.EncryptedPrivateKey = encryptedPrivateKey.String
	return root, err
}

func (s *Store) RotateDesktopTrust(ctx context.Context, root domain.DesktopTrustRoot, replacementOperator domain.DesktopIdentity, rotatedAt time.Time, reason string) error {
	return s.replaceDesktopTrust(ctx, root, replacementOperator, rotatedAt, reason)
}

func (s *Store) CreateDesktopIdentity(ctx context.Context, identity domain.DesktopIdentity) error {
	if err := validateDesktopIdentity(identity); err != nil {
		return err
	}
	_, err := s.exec(ctx, `INSERT INTO desktop_identities (
			id, home_id, identity_type, user_id, device_id, agent_id,
			public_key_spki, certificate, fingerprint, capabilities, trust_root_generation,
			created_at, expires_at, revoked_at, revocation_reason, auth_session_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, desktopIdentityArgs(identity)...)
	return mapDesktopStoreError(err)
}

func (s *Store) GetActiveDesktopOperatorIdentity(ctx context.Context, homeID, userID, deviceID string, now time.Time) (domain.DesktopIdentity, error) {
	row := s.queryRow(ctx, `SELECT `+desktopIdentityColumns+`
		FROM desktop_identities
		WHERE home_id = ? AND user_id = ? AND device_id = ?
			AND identity_type = 'operator_device' AND revoked_at IS NULL AND expires_at > ?`,
		homeID, userID, deviceID, now)
	return scanDesktopIdentity(row)
}

func (s *Store) GetCurrentDesktopOperatorIdentity(ctx context.Context, homeID, deviceID string) (domain.DesktopIdentity, error) {
	row := s.queryRow(ctx, `SELECT `+desktopIdentityColumns+`
		FROM desktop_identities
		WHERE home_id = ? AND device_id = ?
			AND identity_type = 'operator_device' AND revoked_at IS NULL`,
		homeID, deviceID)
	return scanDesktopIdentity(row)
}

func (s *Store) GetActiveDesktopEndpointIdentity(ctx context.Context, homeID, agentID string, now time.Time) (domain.DesktopIdentity, error) {
	row := s.queryRow(ctx, `SELECT `+desktopIdentityColumns+`
		FROM desktop_identities
		WHERE home_id = ? AND agent_id = ?
			AND identity_type = 'endpoint' AND revoked_at IS NULL AND expires_at > ?`,
		homeID, agentID, now)
	return scanDesktopIdentity(row)
}

func (s *Store) GetCurrentDesktopEndpointIdentity(ctx context.Context, homeID, agentID string) (domain.DesktopIdentity, error) {
	row := s.queryRow(ctx, `SELECT `+desktopIdentityColumns+`
		FROM desktop_identities
		WHERE home_id = ? AND agent_id = ?
			AND identity_type = 'endpoint' AND revoked_at IS NULL`,
		homeID, agentID)
	return scanDesktopIdentity(row)
}

func (s *Store) ListDesktopIdentities(ctx context.Context, homeID string) ([]domain.DesktopIdentity, error) {
	rows, err := s.query(ctx, `SELECT `+desktopIdentityColumns+`
		FROM desktop_identities
		WHERE home_id = ?
		ORDER BY created_at ASC, id ASC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	identities := make([]domain.DesktopIdentity, 0)
	for rows.Next() {
		identity, err := scanDesktopIdentity(rows)
		if err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func (s *Store) RevokeDesktopIdentity(ctx context.Context, homeID, identityID, reason string, revokedAt time.Time) (bool, []string, error) {
	if strings.TrimSpace(homeID) == "" || strings.TrimSpace(identityID) == "" || strings.TrimSpace(reason) == "" || revokedAt.IsZero() {
		return false, nil, errors.New("invalid desktop identity revocation")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback()
	sessionIDs, changed, err := revokeDesktopIdentityTx(ctx, tx, homeID, identityID, reason, revokedAt)
	if err != nil || !changed {
		return changed, sessionIDs, err
	}
	if err := tx.Commit(); err != nil {
		return false, nil, mapDesktopStoreError(err)
	}
	return true, sessionIDs, nil
}

func revokeDesktopIdentityTx(ctx context.Context, tx *dbTx, homeID, identityID, reason string, revokedAt time.Time) ([]string, bool, error) {
	var identityType string
	var agentID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT identity_type, agent_id FROM desktop_identities
		WHERE home_id = ? AND id = ? AND revoked_at IS NULL FOR UPDATE`, homeID, identityID).Scan(&identityType, &agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	scope, scopeID, err := desktopIdentitySessionScope(identityType, identityID, agentID)
	if err != nil {
		return nil, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM desktop_sessions WHERE home_id = ? AND `+scope+`
		AND state IN ('requested','offered','agent_ready','joining','active','reconnecting') FOR UPDATE`, homeID, scopeID)
	if err != nil {
		return nil, false, err
	}
	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			rows.Close()
			return nil, false, err
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_identities SET revoked_at = ?, revocation_reason = ?
		WHERE home_id = ? AND id = ? AND revoked_at IS NULL`, revokedAt, reason, homeID, identityID); err != nil {
		return nil, false, err
	}
	for _, sessionID := range sessionIDs {
		if err := appendDesktopSessionEvent(ctx, tx, domain.DesktopSessionEvent{SessionID: sessionID, EventType: "desktop.identity.revoked", ActorType: "server", OccurredAt: revokedAt, Severity: "security", ReasonCode: reason, MetadataJSON: `{}`}); err != nil {
			return nil, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_sessions
		SET state = 'terminated', terminated_at = ?, termination_reason = ?
		WHERE home_id = ? AND `+scope+` AND state IN ('requested','offered','agent_ready','joining','active','reconnecting')`, revokedAt, reason, homeID, scopeID); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_join_credentials AS credential
		SET revoked_at = ?
		FROM desktop_sessions AS session
		WHERE credential.session_id = session.id AND session.home_id = ? AND `+scope+`
			AND credential.consumed_at IS NULL AND credential.revoked_at IS NULL`, revokedAt, homeID, scopeID); err != nil {
		return nil, false, err
	}
	return sessionIDs, true, nil
}

func desktopIdentitySessionScope(identityType, identityID string, agentID sql.NullString) (string, string, error) {
	if identityType != domain.DesktopIdentityEndpoint {
		return `operator_device_identity_id = ?`, identityID, nil
	}
	if !agentID.Valid || strings.TrimSpace(agentID.String) == "" {
		return "", "", errors.New("desktop endpoint identity has no agent scope")
	}
	return `agent_id = ?`, agentID.String, nil
}

func (s *Store) ReplaceDesktopIdentity(ctx context.Context, previousID string, replacement domain.DesktopIdentity, changedAt time.Time, reason string) ([]string, error) {
	if err := validateDesktopIdentity(replacement); err != nil {
		return nil, err
	}
	if previousID == "" || changedAt.IsZero() || reason == "" {
		return nil, errors.New("invalid desktop identity replacement")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sessionIDs, changed, err := revokeDesktopIdentityTx(ctx, tx, replacement.HomeID, previousID, reason, changedAt)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, ErrNotFound
	}
	if err := insertDesktopIdentity(ctx, tx, replacement); err != nil {
		return nil, mapDesktopStoreError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, mapDesktopStoreError(err)
	}
	return sessionIDs, nil
}

func (s *Store) RefreshDesktopIdentity(ctx context.Context, previousID string, replacement domain.DesktopIdentity, changedAt time.Time, reason string) ([]string, error) {
	if err := validateDesktopIdentity(replacement); err != nil {
		return nil, err
	}
	if previousID == "" || replacement.ID != previousID || changedAt.IsZero() || reason == "" {
		return nil, errors.New("invalid desktop identity refresh")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	sessionIDs, changed, err := revokeDesktopIdentityTx(ctx, tx, replacement.HomeID, previousID, reason, changedAt)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_identities SET
			user_id = ?, device_id = ?, agent_id = ?, public_key_spki = ?, certificate = ?,
			fingerprint = ?, capabilities = ?, trust_root_generation = ?, created_at = ?,
			expires_at = ?, revoked_at = NULL, revocation_reason = NULL, auth_session_id = ?
		WHERE home_id = ? AND id = ?`,
		nilIfEmptyString(replacement.UserID), nilIfEmptyString(replacement.DeviceID), nilIfEmptyString(replacement.AgentID),
		replacement.PublicKeySPKI, replacement.Certificate, replacement.Fingerprint, replacement.Capabilities,
		replacement.TrustRootGeneration, replacement.CreatedAt, replacement.ExpiresAt, nilIfEmptyString(replacement.AuthSessionID),
		replacement.HomeID, previousID); err != nil {
		return nil, mapDesktopStoreError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, mapDesktopStoreError(err)
	}
	return sessionIDs, nil
}

func (s *Store) ReplaceDesktopRecoveryEnvelope(ctx context.Context, homeID string, generation int, envelope []byte, rotatedAt time.Time) error {
	if strings.TrimSpace(homeID) == "" || generation <= 0 || len(envelope) == 0 || rotatedAt.IsZero() {
		return errors.New("invalid desktop recovery envelope replacement")
	}
	result, err := s.exec(ctx, `UPDATE desktop_trust_roots
		SET recovery_envelope = ?, rotated_at = ?
		WHERE home_id = ? AND generation = ?`, envelope, rotatedAt, homeID, generation)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("%w: desktop trust generation changed", ErrConflict)
	}
	return nil
}

func (s *Store) IssueDesktopRecoveryChallenge(ctx context.Context, homeID string, challengeHash []byte, expiresAt time.Time) error {
	if strings.TrimSpace(homeID) == "" || len(challengeHash) == 0 || expiresAt.IsZero() {
		return errors.New("invalid desktop recovery challenge")
	}
	result, err := s.exec(ctx, `UPDATE desktop_trust_roots
		SET recovery_challenge_hash = ?, recovery_challenge_expires_at = ?, recovery_challenge_consumed_at = NULL
		WHERE home_id = ?`, challengeHash, expiresAt, homeID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ConsumeDesktopRecoveryChallengeAndCreateOperator(ctx context.Context, homeID string, generation int, challengeHash []byte, now time.Time, operator domain.DesktopIdentity) error {
	if strings.TrimSpace(homeID) == "" || generation <= 0 || len(challengeHash) == 0 || now.IsZero() {
		return errors.New("invalid desktop recovery challenge consumption")
	}
	if err := validateDesktopIdentity(operator); err != nil {
		return err
	}
	if operator.IdentityType != domain.DesktopIdentityOperatorDevice || operator.HomeID != homeID || operator.TrustRootGeneration != generation {
		return errors.New("recovered operator does not match desktop trust scope")
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var updatedHomeID string
	err = tx.QueryRowContext(ctx, `UPDATE desktop_trust_roots
		SET recovery_challenge_consumed_at = ?
		WHERE home_id = ? AND generation = ? AND recovery_challenge_hash = ?
			AND recovery_challenge_consumed_at IS NULL AND recovery_challenge_expires_at > ?
		RETURNING home_id`, now, homeID, generation, challengeHash, now).Scan(&updatedHomeID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: recovery challenge is invalid, expired, or consumed", ErrConflict)
	}
	if err != nil {
		return err
	}
	if err := insertDesktopIdentity(ctx, tx, operator); err != nil {
		return mapDesktopStoreError(err)
	}
	return mapDesktopStoreError(tx.Commit())
}

func (s *Store) ResetDesktopTrust(ctx context.Context, root domain.DesktopTrustRoot, replacementOperator domain.DesktopIdentity, resetAt time.Time, reason string) error {
	return s.replaceDesktopTrust(ctx, root, replacementOperator, resetAt, reason)
}

func (s *Store) replaceDesktopTrust(ctx context.Context, root domain.DesktopTrustRoot, replacementOperator domain.DesktopIdentity, changedAt time.Time, reason string) error {
	if err := validateDesktopTrustRoot(root); err != nil {
		return err
	}
	if err := validateDesktopIdentity(replacementOperator); err != nil {
		return err
	}
	if replacementOperator.IdentityType != domain.DesktopIdentityOperatorDevice ||
		replacementOperator.HomeID != root.HomeID ||
		replacementOperator.TrustRootGeneration != root.Generation ||
		changedAt.IsZero() || strings.TrimSpace(reason) == "" {
		return errors.New("replacement desktop trust scope is invalid")
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentGeneration int
	err = tx.QueryRowContext(ctx, `SELECT generation FROM desktop_trust_roots WHERE home_id = ? FOR UPDATE`, root.HomeID).Scan(&currentGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if root.Generation != currentGeneration+1 {
		return fmt.Errorf("%w: desktop trust generation must advance by one", ErrConflict)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE desktop_identities
		SET revoked_at = ?, revocation_reason = ?
		WHERE home_id = ? AND revoked_at IS NULL`, changedAt, reason, root.HomeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_sessions
		SET state = 'terminated', terminated_at = ?, termination_reason = ?
		WHERE home_id = ? AND state IN ('requested','offered','agent_ready','joining','active','reconnecting')`, changedAt, reason, root.HomeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_join_credentials AS credential
		SET revoked_at = ?
		FROM desktop_sessions AS session
		WHERE credential.session_id = session.id AND session.home_id = ?
			AND credential.consumed_at IS NULL AND credential.revoked_at IS NULL`, changedAt, root.HomeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM desktop_pending_enrollments WHERE home_id = ?`, root.HomeID); err != nil {
		return mapDesktopStoreError(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_trust_roots
		SET generation = ?, algorithm = ?, public_key_spki = ?, fingerprint = ?,
			recovery_envelope = ?, recovery_challenge_hash = NULL,
			recovery_challenge_expires_at = NULL, recovery_challenge_consumed_at = NULL,
			rotated_at = ?
		WHERE home_id = ?`, root.Generation, root.Algorithm, root.PublicKeySPKI, root.Fingerprint, root.RecoveryEnvelope, changedAt, root.HomeID); err != nil {
		return mapDesktopStoreError(err)
	}
	if err := insertDesktopIdentity(ctx, tx, replacementOperator); err != nil {
		return mapDesktopStoreError(err)
	}
	return mapDesktopStoreError(tx.Commit())
}

func insertDesktopTrustRoot(ctx context.Context, tx *dbTx, root domain.DesktopTrustRoot) error {
	authorityMode := strings.TrimSpace(root.AuthorityMode)
	if authorityMode == "" {
		authorityMode = "client_managed"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO desktop_trust_roots (
				home_id, generation, algorithm, public_key_spki, fingerprint,
				recovery_envelope, created_at, rotated_at, authority_mode, encrypted_private_key
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		root.HomeID,
		root.Generation,
		root.Algorithm,
		root.PublicKeySPKI,
		root.Fingerprint,
		nilIfEmptyBytes(root.RecoveryEnvelope),
		root.CreatedAt,
		root.RotatedAt,
		authorityMode,
		nilIfEmptyString(root.EncryptedPrivateKey),
	)
	return err
}

func insertDesktopIdentity(ctx context.Context, tx *dbTx, identity domain.DesktopIdentity) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO desktop_identities (
			id, home_id, identity_type, user_id, device_id, agent_id,
			public_key_spki, certificate, fingerprint, capabilities, trust_root_generation,
			created_at, expires_at, revoked_at, revocation_reason, auth_session_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, desktopIdentityArgs(identity)...)
	return err
}

func desktopIdentityArgs(identity domain.DesktopIdentity) []any {
	return []any{
		identity.ID,
		identity.HomeID,
		identity.IdentityType,
		nilIfEmptyString(identity.UserID),
		nilIfEmptyString(identity.DeviceID),
		nilIfEmptyString(identity.AgentID),
		identity.PublicKeySPKI,
		identity.Certificate,
		identity.Fingerprint,
		identity.Capabilities,
		identity.TrustRootGeneration,
		identity.CreatedAt,
		identity.ExpiresAt,
		identity.RevokedAt,
		nilIfEmptyString(identity.RevocationReason),
		nilIfEmptyString(identity.AuthSessionID),
	}
}

func scanDesktopIdentity(scanner interface{ Scan(dest ...any) error }) (domain.DesktopIdentity, error) {
	var identity domain.DesktopIdentity
	var userID, deviceID, agentID, revocationReason, authSessionID sql.NullString
	var capabilitiesJSON string
	err := scanner.Scan(
		&identity.ID,
		&identity.HomeID,
		&identity.IdentityType,
		&userID,
		&deviceID,
		&agentID,
		&identity.PublicKeySPKI,
		&identity.Certificate,
		&identity.Fingerprint,
		&capabilitiesJSON,
		&identity.TrustRootGeneration,
		&identity.CreatedAt,
		&identity.ExpiresAt,
		&identity.RevokedAt,
		&revocationReason,
		&authSessionID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DesktopIdentity{}, ErrNotFound
	}
	if err != nil {
		return domain.DesktopIdentity{}, err
	}
	if err := json.Unmarshal([]byte(capabilitiesJSON), &identity.Capabilities); err != nil {
		return domain.DesktopIdentity{}, fmt.Errorf("decode desktop identity capabilities: %w", err)
	}
	identity.UserID = userID.String
	identity.DeviceID = deviceID.String
	identity.AgentID = agentID.String
	identity.RevocationReason = revocationReason.String
	identity.AuthSessionID = authSessionID.String
	return identity, nil
}

func validateDesktopTrustRoot(root domain.DesktopTrustRoot) error {
	if strings.TrimSpace(root.HomeID) == "" || root.Generation <= 0 ||
		root.Algorithm != domain.DesktopTrustAlgorithm || len(root.PublicKeySPKI) == 0 ||
		strings.TrimSpace(root.Fingerprint) == "" || root.CreatedAt.IsZero() ||
		(root.AuthorityMode != "" && root.AuthorityMode != "client_managed" && root.AuthorityMode != "server_managed") ||
		(root.AuthorityMode == "server_managed" && strings.TrimSpace(root.EncryptedPrivateKey) == "") ||
		(root.AuthorityMode != "server_managed" && len(root.RecoveryEnvelope) == 0) {
		return errors.New("invalid desktop trust root")
	}
	return nil
}

func validateDesktopIdentity(identity domain.DesktopIdentity) error {
	if strings.TrimSpace(identity.ID) == "" || strings.TrimSpace(identity.HomeID) == "" ||
		len(identity.PublicKeySPKI) == 0 || len(identity.Certificate) == 0 ||
		strings.TrimSpace(identity.Fingerprint) == "" || identity.TrustRootGeneration <= 0 ||
		identity.CreatedAt.IsZero() || !identity.ExpiresAt.After(identity.CreatedAt) {
		return errors.New("invalid desktop identity")
	}
	switch identity.IdentityType {
	case domain.DesktopIdentityOperatorDevice:
		if strings.TrimSpace(identity.UserID) == "" || strings.TrimSpace(identity.DeviceID) == "" || identity.AgentID != "" {
			return errors.New("invalid desktop operator-device scope")
		}
	case domain.DesktopIdentityEndpoint:
		if identity.UserID != "" || identity.DeviceID != "" || strings.TrimSpace(identity.AgentID) == "" {
			return errors.New("invalid desktop endpoint scope")
		}
	default:
		return errors.New("unsupported desktop identity type")
	}
	if identity.RevokedAt == nil && identity.RevocationReason != "" {
		return errors.New("desktop identity revocation reason requires revoked_at")
	}
	if identity.RevokedAt != nil && strings.TrimSpace(identity.RevocationReason) == "" {
		return errors.New("revoked desktop identity requires a reason")
	}
	return nil
}

func mapDesktopStoreError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "40001":
			return fmt.Errorf("%w: concurrent or duplicate desktop state", ErrConflict)
		}
	}
	return err
}

func nilIfEmptyString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nilIfEmptyBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
