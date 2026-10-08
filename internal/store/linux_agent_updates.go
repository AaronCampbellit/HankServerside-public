package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func updateID(prefix string) string {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(raw)
}

func (s *Store) RegisterLinuxAgentRelease(ctx context.Context, value domain.LinuxAgentRelease) error {
	if value.ID == "" {
		value.ID = updateID("lrel")
	}
	if value.State == "" {
		value.State = "hosted"
	}
	var version, digest string
	err := s.queryRow(ctx, `SELECT version, manifest_sha256 FROM linux_agent_releases WHERE version = ? OR manifest_sha256 = ? LIMIT 1`, value.Version, value.ManifestSHA256).Scan(&version, &digest)
	if err == nil {
		if version == value.Version && digest == value.ManifestSHA256 {
			return nil
		}
		return fmt.Errorf("%w: immutable Linux release version or digest collision", ErrConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO linux_agent_releases (id, version, manifest_sha256, source_commit, manifest_url, state, created_at, activated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.Version, value.ManifestSHA256, value.SourceCommit, value.ManifestURL, value.State, value.CreatedAt, value.ActivatedAt)
	return err
}

func (s *Store) GetLinuxAgentRelease(ctx context.Context, version string) (domain.LinuxAgentRelease, error) {
	var value domain.LinuxAgentRelease
	var activated sql.NullTime
	err := s.queryRow(ctx, `SELECT id, version, manifest_sha256, source_commit, manifest_url, state, created_at, activated_at FROM linux_agent_releases WHERE version = ?`, version).Scan(&value.ID, &value.Version, &value.ManifestSHA256, &value.SourceCommit, &value.ManifestURL, &value.State, &value.CreatedAt, &activated)
	if errors.Is(err, sql.ErrNoRows) {
		return value, ErrNotFound
	}
	if activated.Valid {
		value.ActivatedAt = &activated.Time
	}
	return value, err
}

func (s *Store) ActivateLinuxAgentRollout(ctx context.Context, version string, now time.Time, spread time.Duration) (domain.LinuxAgentRollout, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.LinuxAgentRollout{}, err
	}
	defer tx.Rollback()
	var release domain.LinuxAgentRelease
	if err := tx.QueryRowContext(ctx, `SELECT id, version, manifest_sha256, source_commit, manifest_url, state, created_at FROM linux_agent_releases WHERE version = ? FOR UPDATE`, version).Scan(&release.ID, &release.Version, &release.ManifestSHA256, &release.SourceCommit, &release.ManifestURL, &release.State, &release.CreatedAt); errors.Is(err, sql.ErrNoRows) {
		return domain.LinuxAgentRollout{}, ErrNotFound
	} else if err != nil {
		return domain.LinuxAgentRollout{}, err
	}
	var existing domain.LinuxAgentRollout
	err = tx.QueryRowContext(ctx, `SELECT r.id, r.release_id, rel.version, r.state, r.scope, r.created_at, r.updated_at FROM linux_agent_rollouts r JOIN linux_agent_releases rel ON rel.id = r.release_id WHERE r.state IN ('active','paused') LIMIT 1 FOR UPDATE OF r`).Scan(&existing.ID, &existing.ReleaseID, &existing.Version, &existing.State, &existing.Scope, &existing.CreatedAt, &existing.UpdatedAt)
	if err == nil {
		if existing.ReleaseID == release.ID {
			return existing, tx.Commit()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE linux_agent_rollouts SET state = 'cancelled', updated_at = ?, completed_at = ? WHERE id = ?`, now, now, existing.ID); err != nil {
			return domain.LinuxAgentRollout{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE linux_agent_update_assignments SET state = 'cancelled', error_code = 'superseded', updated_at = ? WHERE rollout_id = ? AND state NOT IN ('healthy','manual_update_required','cancelled')`, now, existing.ID); err != nil {
			return domain.LinuxAgentRollout{}, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.LinuxAgentRollout{}, err
	}

	rollout := domain.LinuxAgentRollout{ID: updateID("lroll"), ReleaseID: release.ID, Version: version, State: "active", Scope: "all-linux", CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `UPDATE linux_agent_releases SET state = 'superseded' WHERE state = 'active' AND id <> ?`, release.ID); err != nil {
		return rollout, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE linux_agent_releases SET state = 'active', activated_at = COALESCE(activated_at, ?) WHERE id = ?`, now, release.ID); err != nil {
		return rollout, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO linux_agent_rollouts (id, release_id, state, scope, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, rollout.ID, rollout.ReleaseID, rollout.State, rollout.Scope, now, now); err != nil {
		return rollout, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.home_id, COALESCE(a.app_version,''), COALESCE(a.installation_mode,''), a.status, p.version
		FROM agents a
		LEFT JOIN linux_agent_version_pins p ON p.agent_id = a.id
		WHERE a.platform = 'linux' AND (a.agent_type = 'worker')
		AND EXISTS (SELECT 1 FROM agent_tokens t WHERE t.agent_id = a.id AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > ?))
		ORDER BY a.id`, now)
	if err != nil {
		return rollout, err
	}
	type eligibleAgent struct {
		agentID, homeID, current, mode, status string
		pin                                    sql.NullString
	}
	var eligible []eligibleAgent
	for rows.Next() {
		var value eligibleAgent
		if err := rows.Scan(&value.agentID, &value.homeID, &value.current, &value.mode, &value.status, &value.pin); err != nil {
			return rollout, err
		}
		eligible = append(eligible, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return rollout, err
	}
	if err := rows.Close(); err != nil {
		return rollout, err
	}
	for _, value := range eligible {
		state := "pending"
		if value.current == version {
			state = "healthy"
		} else if value.pin.Valid && value.pin.String != version {
			state = "pinned"
		} else if value.mode != "system" {
			state = "manual_update_required"
		} else if value.status != "online" {
			state = "waiting_online"
		}
		notBefore := now
		if spread > 0 {
			upper := big.NewInt(int64(spread) + 1)
			offset, err := rand.Int(rand.Reader, upper)
			if err != nil {
				return rollout, fmt.Errorf("schedule Linux rollout: %w", err)
			}
			notBefore = now.Add(time.Duration(offset.Int64()))
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO linux_agent_update_assignments (id, rollout_id, home_id, agent_id, from_version, to_version, state, not_before, created_at, updated_at) VALUES (?, ?, ?, ?, NULLIF(?,''), ?, ?, ?, ?, ?)`, updateID("luasg"), rollout.ID, value.homeID, value.agentID, value.current, version, state, notBefore, now, now); err != nil {
			return rollout, err
		}
	}
	return rollout, tx.Commit()
}

func (s *Store) GetActiveLinuxAgentRollout(ctx context.Context) (domain.LinuxAgentRollout, error) {
	var value domain.LinuxAgentRollout
	var completed sql.NullTime
	err := s.queryRow(ctx, `SELECT r.id, r.release_id, rel.version, r.state, r.scope, r.created_at, r.updated_at, r.completed_at FROM linux_agent_rollouts r JOIN linux_agent_releases rel ON rel.id = r.release_id WHERE r.state IN ('active','paused') LIMIT 1`).Scan(&value.ID, &value.ReleaseID, &value.Version, &value.State, &value.Scope, &value.CreatedAt, &value.UpdatedAt, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return value, ErrNotFound
	}
	if completed.Valid {
		value.CompletedAt = &completed.Time
	}
	return value, err
}

func (s *Store) ListLinuxAgentRolloutAssignments(ctx context.Context, rolloutID, homeID string) ([]domain.LinuxAgentUpdateAssignment, error) {
	query := `SELECT id, rollout_id, home_id, agent_id, COALESCE(from_version,''), to_version, state, error_code, not_before, attempt, created_at, updated_at FROM linux_agent_update_assignments WHERE rollout_id = ?`
	args := []any{rolloutID}
	if homeID != "" {
		query += ` AND home_id = ?`
		args = append(args, homeID)
	}
	query += ` ORDER BY created_at, agent_id`
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.LinuxAgentUpdateAssignment
	for rows.Next() {
		value, err := scanLinuxAssignment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ListDispatchableLinuxAssignments(ctx context.Context, now time.Time, limit int) ([]domain.LinuxAgentUpdateAssignment, error) {
	rows, err := s.query(ctx, `SELECT a.id, a.rollout_id, a.home_id, a.agent_id, COALESCE(a.from_version,''), a.to_version, a.state, a.error_code, a.not_before, a.attempt, a.created_at, a.updated_at FROM linux_agent_update_assignments a JOIN linux_agent_rollouts r ON r.id = a.rollout_id JOIN agents ag ON ag.id = a.agent_id WHERE r.state = 'active' AND a.state IN ('pending','delayed','waiting_online') AND a.not_before <= ? AND ag.status = 'online' ORDER BY a.not_before LIMIT ?`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []domain.LinuxAgentUpdateAssignment
	for rows.Next() {
		value, err := scanLinuxAssignment(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) TransitionLinuxAgentAssignment(ctx context.Context, id string, from []string, to, errorCode string, now time.Time) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("assignment transition requires source states")
	}
	placeholders := make([]string, len(from))
	for i := range from {
		placeholders[i] = "?"
	}
	result, err := s.exec(ctx, `UPDATE linux_agent_update_assignments SET state = ?, error_code = ?, attempt = attempt + CASE WHEN ? = 'downloading' THEN 1 ELSE 0 END, updated_at = ? WHERE id = ? AND state IN (`+joinSQL(placeholders)+`)`, append([]any{to, errorCode, to, now, id}, toAny(from)...)...)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) TransitionLinuxAgentAssignmentForAgent(ctx context.Context, id, rolloutID, homeID, agentID string, from []string, to, errorCode string, now time.Time) (bool, error) {
	if len(from) == 0 {
		return false, errors.New("assignment transition requires source states")
	}
	placeholders := make([]string, len(from))
	for i := range from {
		placeholders[i] = "?"
	}
	args := []any{to, errorCode, to, now, id, rolloutID, homeID, agentID}
	args = append(args, toAny(from)...)
	result, err := s.exec(ctx, `UPDATE linux_agent_update_assignments SET state = ?, error_code = ?, attempt = attempt + CASE WHEN ? = 'downloading' THEN 1 ELSE 0 END, updated_at = ? WHERE id = ? AND rollout_id = ? AND home_id = ? AND agent_id = ? AND state IN (`+joinSQL(placeholders)+`)`, args...)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) AcknowledgeLinuxAgentVersion(ctx context.Context, homeID, agentID, assignmentID, version string, now time.Time) (bool, error) {
	if assignmentID == "" || version == "" {
		return false, nil
	}
	result, err := s.exec(ctx, `UPDATE linux_agent_update_assignments SET state = 'healthy', error_code = '', updated_at = ? WHERE id = ? AND home_id = ? AND agent_id = ? AND to_version = ? AND state IN ('downloading','installing','reconnecting')`, now, assignmentID, homeID, agentID, version)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) RetryLinuxAgentAssignment(ctx context.Context, homeID, assignmentID string, now time.Time) (bool, error) {
	result, err := s.exec(ctx, `UPDATE linux_agent_update_assignments SET state = 'pending', error_code = '', not_before = ?, updated_at = ? WHERE id = ? AND home_id = ? AND state IN ('failed','rolled_back','recovery_failed')`, now, now, assignmentID, homeID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func joinSQL(values []string) string {
	result := ""
	for i, value := range values {
		if i > 0 {
			result += ","
		}
		result += value
	}
	return result
}
func toAny(values []string) []any {
	result := make([]any, len(values))
	for i := range values {
		result[i] = values[i]
	}
	return result
}

func (s *Store) SetLinuxAgentRolloutState(ctx context.Context, id, state string, now time.Time) error {
	result, err := s.exec(ctx, `UPDATE linux_agent_rollouts SET state = ?, updated_at = ?, completed_at = CASE WHEN ? IN ('completed','cancelled') THEN ? ELSE completed_at END WHERE id = ?`, state, now, state, now, id)
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

func (s *Store) SetLinuxAgentVersionPin(ctx context.Context, pin domain.LinuxAgentVersionPin) error {
	var exists bool
	if err := s.queryRow(ctx, `SELECT EXISTS (SELECT 1 FROM linux_agent_releases WHERE version = ?)`, pin.Version).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := s.exec(ctx, `INSERT INTO linux_agent_version_pins (home_id, agent_id, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT(agent_id) DO UPDATE SET version = excluded.version, updated_at = excluded.updated_at WHERE linux_agent_version_pins.home_id = excluded.home_id`, pin.HomeID, pin.AgentID, pin.Version, pin.CreatedAt, pin.UpdatedAt); err != nil {
		return err
	}
	_, err := s.exec(ctx, `UPDATE linux_agent_update_assignments SET state = CASE WHEN to_version = ? THEN state ELSE 'pinned' END, error_code = '', updated_at = ? WHERE home_id = ? AND agent_id = ? AND state IN ('pending','delayed','waiting_online','failed','rolled_back','recovery_failed')`, pin.Version, pin.UpdatedAt, pin.HomeID, pin.AgentID)
	return err
}

func (s *Store) DeleteLinuxAgentVersionPin(ctx context.Context, homeID, agentID string) error {
	if _, err := s.exec(ctx, `DELETE FROM linux_agent_version_pins WHERE home_id = ? AND agent_id = ?`, homeID, agentID); err != nil {
		return err
	}
	_, err := s.exec(ctx, `UPDATE linux_agent_update_assignments a SET state = CASE WHEN ag.status = 'online' THEN 'pending' ELSE 'waiting_online' END, error_code = '', not_before = ?, updated_at = ? FROM agents ag, linux_agent_rollouts r WHERE a.home_id = ? AND a.agent_id = ? AND a.agent_id = ag.id AND a.rollout_id = r.id AND r.state IN ('active','paused') AND a.state = 'pinned'`, time.Now().UTC(), time.Now().UTC(), homeID, agentID)
	return err
}

func (s *Store) GetLinuxAgentVersionPin(ctx context.Context, homeID, agentID string) (domain.LinuxAgentVersionPin, error) {
	var pin domain.LinuxAgentVersionPin
	err := s.queryRow(ctx, `SELECT home_id, agent_id, version, created_at, updated_at FROM linux_agent_version_pins WHERE home_id = ? AND agent_id = ?`, homeID, agentID).Scan(&pin.HomeID, &pin.AgentID, &pin.Version, &pin.CreatedAt, &pin.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return pin, ErrNotFound
	}
	return pin, err
}

func (s *Store) ListLinuxAgentVersionPins(ctx context.Context, homeID string) ([]domain.LinuxAgentVersionPin, error) {
	rows, err := s.query(ctx, `SELECT home_id, agent_id, version, created_at, updated_at FROM linux_agent_version_pins WHERE home_id = ? ORDER BY agent_id`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pins []domain.LinuxAgentVersionPin
	for rows.Next() {
		var pin domain.LinuxAgentVersionPin
		if err := rows.Scan(&pin.HomeID, &pin.AgentID, &pin.Version, &pin.CreatedAt, &pin.UpdatedAt); err != nil {
			return nil, err
		}
		pins = append(pins, pin)
	}
	return pins, rows.Err()
}

func (s *Store) ListProtectedLinuxAgentVersions(ctx context.Context) (map[string]struct{}, error) {
	rows, err := s.query(ctx, `SELECT version FROM linux_agent_releases WHERE state = 'active'
		UNION SELECT version FROM linux_agent_version_pins
		UNION SELECT to_version FROM linux_agent_update_assignments WHERE state NOT IN ('healthy','cancelled','manual_update_required')
		UNION SELECT from_version FROM linux_agent_update_assignments WHERE from_version IS NOT NULL AND state IN ('installing','reconnecting','failed','rolled_back','recovery_failed')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]struct{})
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		result[version] = struct{}{}
	}
	return result, rows.Err()
}

func (s *Store) UpdateAgentRuntimeMetadata(ctx context.Context, homeID, agentID, platform, architecture, appVersion, installationMode string, capabilities []string, now time.Time) error {
	if installationMode == "" && platform == "linux" && slices.Contains(capabilities, "packages.install") && slices.Contains(capabilities, "services.restart") {
		installationMode = "system"
	}
	raw, err := json.Marshal(capabilities)
	if err != nil {
		return err
	}
	result, err := s.exec(ctx, `UPDATE agents SET platform = NULLIF(?,''), architecture = NULLIF(?,''), app_version = NULLIF(?,''), installation_mode = NULLIF(?,''), capabilities = ?, updated_at = ? WHERE home_id = ? AND id = ?`, platform, architecture, appVersion, installationMode, raw, now, homeID, agentID)
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

func scanLinuxAssignment(scanner interface{ Scan(...any) error }) (domain.LinuxAgentUpdateAssignment, error) {
	var value domain.LinuxAgentUpdateAssignment
	err := scanner.Scan(&value.ID, &value.RolloutID, &value.HomeID, &value.AgentID, &value.FromVersion, &value.ToVersion, &value.State, &value.ErrorCode, &value.NotBefore, &value.Attempt, &value.CreatedAt, &value.UpdatedAt)
	return value, err
}
