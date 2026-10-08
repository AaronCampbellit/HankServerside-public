package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Store) GetHomeMembership(ctx context.Context, homeID string, userID string) (domain.HomeMembership, error) {
	row := s.queryRow(ctx, `SELECT home_id, user_id, role, created_at, updated_at FROM home_memberships WHERE home_id = ? AND user_id = ?`, homeID, userID)
	return scanHomeMembership(row)
}

func (s *Store) ListHomeMembers(ctx context.Context, homeID string) ([]domain.HomeMember, error) {
	rows, err := s.query(ctx, `SELECT u.id, u.email, u.display_name, hm.role, hm.created_at, hm.updated_at
		FROM home_memberships hm
		JOIN users u ON u.id = hm.user_id
		WHERE hm.home_id = ?
		ORDER BY hm.created_at ASC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []domain.HomeMember
	for rows.Next() {
		member, err := scanHomeMember(rows)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, rows.Err()
}

func (s *Store) GetHomeMember(ctx context.Context, homeID string, userID string) (domain.HomeMember, error) {
	row := s.queryRow(ctx, `SELECT u.id, u.email, u.display_name, hm.role, hm.created_at, hm.updated_at
		FROM home_memberships hm
		JOIN users u ON u.id = hm.user_id
		WHERE hm.home_id = ? AND hm.user_id = ?`, homeID, userID)
	return scanHomeMember(row)
}

func (s *Store) AddHomeMembership(ctx context.Context, membership domain.HomeMembership) error {
	_, err := s.exec(ctx, `INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(home_id, user_id) DO UPDATE SET
		role = excluded.role,
		updated_at = excluded.updated_at`,
		membership.HomeID,
		membership.UserID,
		membership.Role,
		membership.CreatedAt,
		membership.UpdatedAt,
	)
	return err
}

func (s *Store) RemoveHomeMembership(ctx context.Context, homeID string, userID string) error {
	result, err := s.exec(ctx, `DELETE FROM home_memberships WHERE home_id = ? AND user_id = ?`, homeID, userID)
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

func (s *Store) CountHomeOwners(ctx context.Context, homeID string) (int, error) {
	row := s.queryRow(ctx, `SELECT COUNT(*) FROM home_memberships WHERE home_id = ? AND role = ?`, homeID, domain.HomeRoleOwner)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) UpdateHomeMembershipRole(ctx context.Context, homeID string, userID string, role string) error {
	result, err := s.exec(ctx, `UPDATE home_memberships SET role = ?, updated_at = ? WHERE home_id = ? AND user_id = ?`,
		role,
		time.Now().UTC(),
		homeID,
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
	return nil
}

func (s *Store) CreateHomeInvitation(ctx context.Context, invitation domain.HomeInvitation) error {
	_, err := s.exec(ctx, `INSERT INTO home_invitations (id, home_id, email, role, token_hash, accepted_at, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		invitation.ID,
		invitation.HomeID,
		invitation.Email,
		invitation.Role,
		invitation.TokenHash,
		invitation.AcceptedAt,
		invitation.ExpiresAt,
		invitation.CreatedAt,
	)
	return err
}

func (s *Store) ListPendingHomeInvitations(ctx context.Context, homeID string) ([]domain.HomeInvitation, error) {
	rows, err := s.query(ctx, `SELECT id, home_id, email, role, token_hash, accepted_at, expires_at, created_at
		FROM home_invitations
		WHERE home_id = ? AND accepted_at IS NULL
		ORDER BY created_at DESC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invitations []domain.HomeInvitation
	for rows.Next() {
		invitation, err := scanHomeInvitation(rows)
		if err != nil {
			return nil, err
		}
		invitations = append(invitations, invitation)
	}
	return invitations, rows.Err()
}

func (s *Store) GetHomeInvitationByTokenHash(ctx context.Context, tokenHash string) (domain.HomeInvitation, error) {
	row := s.queryRow(ctx, `SELECT id, home_id, email, role, token_hash, accepted_at, expires_at, created_at FROM home_invitations WHERE token_hash = ?`, tokenHash)
	return scanHomeInvitation(row)
}

func (s *Store) DeletePendingHomeInvitation(ctx context.Context, homeID string, invitationID string) error {
	result, err := s.exec(ctx, `DELETE FROM home_invitations
		WHERE id = ? AND home_id = ? AND accepted_at IS NULL`, invitationID, homeID)
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

func (s *Store) AcceptHomeInvitation(ctx context.Context, invitationID string, user domain.User, role string) error {
	now := time.Now().UTC()
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `UPDATE home_invitations SET accepted_at = ? WHERE id = ?`, now, invitationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at)
		SELECT home_id, ?, ?, ?, ?
		FROM home_invitations
		WHERE id = ?
		ON CONFLICT(home_id, user_id) DO UPDATE SET role = excluded.role, updated_at = excluded.updated_at`,
		user.ID,
		role,
		now,
		now,
		invitationID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateUserAndAcceptHomeInvitation(ctx context.Context, invitationID string, user domain.User, role string) error {
	now := time.Now().UTC()
	if user.CreatedAt.IsZero() {
		user.CreatedAt = now
	}
	if user.UpdatedAt.IsZero() {
		user.UpdatedAt = now
	}
	user.PasswordLoginEnabled = true
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO users (
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
	); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE home_invitations SET accepted_at = ? WHERE id = ? AND accepted_at IS NULL`, now, invitationID)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at)
		SELECT home_id, ?, ?, ?, ?
		FROM home_invitations
		WHERE id = ?`,
		user.ID,
		role,
		now,
		now,
		invitationID,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateExternalIdentity(ctx context.Context, identity domain.ExternalIdentity) error {
	_, err := s.exec(ctx, `INSERT INTO external_identities (id, user_id, provider, tenant_id, subject_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, identity.ID, identity.UserID, identity.Provider, identity.TenantID, identity.SubjectID, identity.CreatedAt, identity.UpdatedAt)
	return err
}

func (s *Store) GetUserByExternalIdentity(ctx context.Context, provider, tenantID, subjectID string) (domain.User, error) {
	row := s.queryRow(ctx, `SELECT u.id, u.email, u.display_name, u.password_hash, u.password_login_enabled, u.password_change_required, u.password_changed_at, u.password_reset_at, u.password_reset_by, u.created_at, u.updated_at
		FROM external_identities e JOIN users u ON u.id = e.user_id WHERE e.provider = ? AND e.tenant_id = ? AND e.subject_id = ?`, provider, tenantID, subjectID)
	return scanUser(row)
}

// CreateSSOUserAndAcceptHomeInvitation creates an SSO-only user, links the
// immutable provider identity, and consumes the invitation as one transaction.
func (s *Store) CreateSSOUserAndAcceptHomeInvitation(ctx context.Context, invitationID string, user domain.User, identity domain.ExternalIdentity, role string) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, email, display_name, password_hash, password_login_enabled, password_change_required, password_changed_at, password_reset_at, password_reset_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, FALSE, ?, ?, ?, ?, ?, ?)`, user.ID, user.Email, user.DisplayName, user.PasswordHash, user.PasswordChangeRequired, user.PasswordChangedAt, user.PasswordResetAt, user.PasswordResetBy, user.CreatedAt, user.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO external_identities (id, user_id, provider, tenant_id, subject_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, identity.ID, identity.UserID, identity.Provider, identity.TenantID, identity.SubjectID, identity.CreatedAt, identity.UpdatedAt); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE home_invitations SET accepted_at = ? WHERE id = ? AND accepted_at IS NULL`, time.Now().UTC(), invitationID)
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at)
		SELECT home_id, ?, ?, ?, ? FROM home_invitations WHERE id = ?`, user.ID, role, user.CreatedAt, user.UpdatedAt, invitationID); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateSSOUserAndBootstrapSingletonHome atomically creates the first SSO-only
// owner and Home. Serializable isolation makes concurrent first setup retryable
// instead of leaving an unscoped identity behind.
func (s *Store) CreateSSOUserAndBootstrapSingletonHome(ctx context.Context, user domain.User, identity domain.ExternalIdentity, name string) (domain.Home, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.Home{}, err
	}
	defer tx.Rollback()
	var homes int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM homes`).Scan(&homes); err != nil {
		return domain.Home{}, err
	}
	if homes != 0 {
		return domain.Home{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, email, display_name, password_hash, password_login_enabled, password_change_required, password_changed_at, password_reset_at, password_reset_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, FALSE, ?, ?, ?, ?, ?, ?)`, user.ID, user.Email, user.DisplayName, user.PasswordHash, user.PasswordChangeRequired, user.PasswordChangedAt, user.PasswordResetAt, user.PasswordResetBy, user.CreatedAt, user.UpdatedAt); err != nil {
		return domain.Home{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO external_identities (id, user_id, provider, tenant_id, subject_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, identity.ID, identity.UserID, identity.Provider, identity.TenantID, identity.SubjectID, identity.CreatedAt, identity.UpdatedAt); err != nil {
		return domain.Home{}, err
	}
	home := domain.Home{ID: newSingletonHomeID(), UserID: user.ID, Name: name, CreatedAt: user.CreatedAt, UpdatedAt: user.UpdatedAt}
	if _, err := tx.ExecContext(ctx, `INSERT INTO homes (id, user_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, home.ID, home.UserID, home.Name, home.CreatedAt, home.UpdatedAt); err != nil {
		return domain.Home{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO home_memberships (home_id, user_id, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, home.ID, user.ID, domain.HomeRoleAdmin, user.CreatedAt, user.UpdatedAt); err != nil {
		return domain.Home{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO home_permissions (home_id, homeassistant_enabled, files_enabled, notes_enabled, updated_at, updated_by) VALUES (?, TRUE, TRUE, TRUE, ?, ?)`, home.ID, user.UpdatedAt, user.ID); err != nil {
		return domain.Home{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Home{}, err
	}
	return home, nil
}

func (s *Store) GetHomePermissions(ctx context.Context, homeID string) (domain.HomePermissions, error) {
	row := s.queryRow(ctx, `SELECT home_id, homeassistant_enabled, files_enabled, notes_enabled, updated_at, updated_by
		FROM home_permissions WHERE home_id = ?`, homeID)
	return scanHomePermissions(row)
}

func (s *Store) UpsertHomePermissions(ctx context.Context, permissions domain.HomePermissions) error {
	_, err := s.exec(ctx, `INSERT INTO home_permissions (home_id, homeassistant_enabled, files_enabled, notes_enabled, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id) DO UPDATE SET
			homeassistant_enabled = excluded.homeassistant_enabled,
			files_enabled = excluded.files_enabled,
			notes_enabled = excluded.notes_enabled,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by`,
		permissions.HomeID,
		permissions.HomeAssistantEnabled,
		permissions.FilesEnabled,
		permissions.NotesEnabled,
		permissions.UpdatedAt,
		permissions.UpdatedBy,
	)
	return err
}

func (s *Store) GetHomeMemberPermissions(ctx context.Context, homeID string, userID string) (domain.HomeMemberPermissions, error) {
	row := s.queryRow(ctx, `SELECT home_id, user_id, homeassistant_enabled, files_enabled, notes_enabled, updated_at, updated_by
		FROM home_member_permissions WHERE home_id = ? AND user_id = ?`, homeID, userID)
	return scanHomeMemberPermissions(row)
}

func (s *Store) UpsertHomeMemberPermissions(ctx context.Context, permissions domain.HomeMemberPermissions) error {
	_, err := s.exec(ctx, `INSERT INTO home_member_permissions (home_id, user_id, homeassistant_enabled, files_enabled, notes_enabled, updated_at, updated_by)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id, user_id) DO UPDATE SET
			homeassistant_enabled = excluded.homeassistant_enabled,
			files_enabled = excluded.files_enabled,
			notes_enabled = excluded.notes_enabled,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by`,
		permissions.HomeID,
		permissions.UserID,
		permissions.HomeAssistantEnabled,
		permissions.FilesEnabled,
		permissions.NotesEnabled,
		permissions.UpdatedAt,
		permissions.UpdatedBy,
	)
	return err
}

func (s *Store) GetLatestHomeNoteUpdate(ctx context.Context, homeID string) (*time.Time, error) {
	row := s.queryRow(ctx, `SELECT MAX(updated_at)
		FROM user_notes
		WHERE home_id = ?
			AND deleted_at IS NULL
			AND EXISTS (SELECT 1 FROM note_shares ns WHERE ns.note_id = user_notes.id)`, homeID)
	var latest sql.NullTime
	if err := row.Scan(&latest); err != nil {
		return nil, err
	}
	if !latest.Valid {
		return nil, nil
	}
	value := latest.Time.UTC()
	return &value, nil
}

func (s *Store) GetHomeNoteSyncState(ctx context.Context, homeID string) (domain.HomeNoteSyncState, error) {
	row := s.queryRow(ctx, `SELECT home_id, agent_id, last_manifest_at, last_pull_at, last_push_at, status, last_error, pending_pull_count, pending_push_count, last_successful_sync_at
		FROM home_note_sync_state
		WHERE home_id = ?`, homeID)
	return scanHomeNoteSyncState(row)
}

func (s *Store) UpsertHomeNoteSyncState(ctx context.Context, state domain.HomeNoteSyncState) error {
	_, err := s.exec(ctx, `INSERT INTO home_note_sync_state (
			home_id, agent_id, last_manifest_at, last_pull_at, last_push_at, status, last_error, pending_pull_count, pending_push_count, last_successful_sync_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id) DO UPDATE SET
			agent_id = excluded.agent_id,
			last_manifest_at = excluded.last_manifest_at,
			last_pull_at = excluded.last_pull_at,
			last_push_at = excluded.last_push_at,
			status = excluded.status,
			last_error = excluded.last_error,
			pending_pull_count = excluded.pending_pull_count,
			pending_push_count = excluded.pending_push_count,
			last_successful_sync_at = excluded.last_successful_sync_at`,
		state.HomeID,
		state.AgentID,
		state.LastManifestAt,
		state.LastPullAt,
		state.LastPushAt,
		state.Status,
		state.LastError,
		state.PendingPullCount,
		state.PendingPushCount,
		state.LastSuccessfulSyncAt,
	)
	return err
}

func (s *Store) ListHomeServiceProfiles(ctx context.Context, homeID string) ([]domain.HomeServiceProfile, error) {
	rows, err := s.query(ctx, `SELECT home_id, service_type, public_config_json, secret_version, applied_version, status, updated_at, updated_by, last_backup_at, last_error
		FROM home_service_profiles
		WHERE home_id = ?
		ORDER BY service_type ASC`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var profiles []domain.HomeServiceProfile
	for rows.Next() {
		profile, err := scanHomeServiceProfile(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func (s *Store) GetHomeServiceProfile(ctx context.Context, homeID string, serviceType string) (domain.HomeServiceProfile, error) {
	row := s.queryRow(ctx, `SELECT home_id, service_type, public_config_json, secret_version, applied_version, status, updated_at, updated_by, last_backup_at, last_error
		FROM home_service_profiles
		WHERE home_id = ? AND service_type = ?`, homeID, serviceType)
	return scanHomeServiceProfile(row)
}

func (s *Store) UpsertHomeServiceProfile(ctx context.Context, profile domain.HomeServiceProfile) error {
	_, err := s.exec(ctx, `INSERT INTO home_service_profiles (
			home_id, service_type, public_config_json, secret_version, applied_version, status, updated_at, updated_by, last_backup_at, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(home_id, service_type) DO UPDATE SET
			public_config_json = excluded.public_config_json,
			secret_version = excluded.secret_version,
			applied_version = excluded.applied_version,
			status = excluded.status,
			updated_at = excluded.updated_at,
			updated_by = excluded.updated_by,
			last_backup_at = excluded.last_backup_at,
			last_error = excluded.last_error`,
		profile.HomeID,
		profile.ServiceType,
		profile.PublicConfigJSON,
		profile.SecretVersion,
		profile.AppliedVersion,
		profile.Status,
		profile.UpdatedAt,
		profile.UpdatedBy,
		profile.LastBackupAt,
		profile.LastError,
	)
	return err
}

func scanHomeMembership(scanner interface{ Scan(dest ...any) error }) (domain.HomeMembership, error) {
	var membership domain.HomeMembership
	err := scanner.Scan(&membership.HomeID, &membership.UserID, &membership.Role, &membership.CreatedAt, &membership.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomeMembership{}, ErrNotFound
	}
	return membership, err
}

func scanHomeMember(scanner interface{ Scan(dest ...any) error }) (domain.HomeMember, error) {
	var member domain.HomeMember
	err := scanner.Scan(&member.UserID, &member.Email, &member.DisplayName, &member.Role, &member.CreatedAt, &member.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomeMember{}, ErrNotFound
	}
	return member, err
}

func scanHomeInvitation(scanner interface{ Scan(dest ...any) error }) (domain.HomeInvitation, error) {
	var invitation domain.HomeInvitation
	var acceptedAt sql.NullTime
	var expiresAt sql.NullTime
	err := scanner.Scan(&invitation.ID, &invitation.HomeID, &invitation.Email, &invitation.Role, &invitation.TokenHash, &acceptedAt, &expiresAt, &invitation.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomeInvitation{}, ErrNotFound
	}
	if err != nil {
		return domain.HomeInvitation{}, err
	}
	if acceptedAt.Valid {
		invitation.AcceptedAt = &acceptedAt.Time
	}
	if expiresAt.Valid {
		invitation.ExpiresAt = &expiresAt.Time
	}
	return invitation, nil
}

func scanHomePermissions(scanner interface{ Scan(dest ...any) error }) (domain.HomePermissions, error) {
	var permissions domain.HomePermissions
	err := scanner.Scan(
		&permissions.HomeID,
		&permissions.HomeAssistantEnabled,
		&permissions.FilesEnabled,
		&permissions.NotesEnabled,
		&permissions.UpdatedAt,
		&permissions.UpdatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomePermissions{}, ErrNotFound
	}
	return permissions, err
}

func scanHomeMemberPermissions(scanner interface{ Scan(dest ...any) error }) (domain.HomeMemberPermissions, error) {
	var permissions domain.HomeMemberPermissions
	var homeassistant sql.NullBool
	var files sql.NullBool
	var notes sql.NullBool
	err := scanner.Scan(
		&permissions.HomeID,
		&permissions.UserID,
		&homeassistant,
		&files,
		&notes,
		&permissions.UpdatedAt,
		&permissions.UpdatedBy,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomeMemberPermissions{}, ErrNotFound
	}
	if err != nil {
		return domain.HomeMemberPermissions{}, err
	}
	if homeassistant.Valid {
		permissions.HomeAssistantEnabled = &homeassistant.Bool
	}
	if files.Valid {
		permissions.FilesEnabled = &files.Bool
	}
	if notes.Valid {
		permissions.NotesEnabled = &notes.Bool
	}
	return permissions, nil
}

func scanHomeNoteSyncState(scanner interface{ Scan(dest ...any) error }) (domain.HomeNoteSyncState, error) {
	var state domain.HomeNoteSyncState
	var lastManifest sql.NullTime
	var lastPull sql.NullTime
	var lastPush sql.NullTime
	var lastSuccessful sql.NullTime
	err := scanner.Scan(&state.HomeID, &state.AgentID, &lastManifest, &lastPull, &lastPush, &state.Status, &state.LastError, &state.PendingPullCount, &state.PendingPushCount, &lastSuccessful)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomeNoteSyncState{}, ErrNotFound
	}
	if err != nil {
		return domain.HomeNoteSyncState{}, err
	}
	if lastManifest.Valid {
		state.LastManifestAt = &lastManifest.Time
	}
	if lastPull.Valid {
		state.LastPullAt = &lastPull.Time
	}
	if lastPush.Valid {
		state.LastPushAt = &lastPush.Time
	}
	if lastSuccessful.Valid {
		state.LastSuccessfulSyncAt = &lastSuccessful.Time
	}
	return state, nil
}

func scanHomeServiceProfile(scanner interface{ Scan(dest ...any) error }) (domain.HomeServiceProfile, error) {
	var profile domain.HomeServiceProfile
	var lastBackup sql.NullTime
	err := scanner.Scan(&profile.HomeID, &profile.ServiceType, &profile.PublicConfigJSON, &profile.SecretVersion, &profile.AppliedVersion, &profile.Status, &profile.UpdatedAt, &profile.UpdatedBy, &lastBackup, &profile.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.HomeServiceProfile{}, ErrNotFound
	}
	if err != nil {
		return domain.HomeServiceProfile{}, err
	}
	if lastBackup.Valid {
		profile.LastBackupAt = &lastBackup.Time
	}
	return profile, nil
}
