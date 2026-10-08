package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

const desktopSessionColumns = `id, home_id, agent_id, operator_user_id, operator_device_identity_id,
	to_json(requested_permissions)::text, to_json(effective_permissions)::text, state, key_epoch, requested_at, join_expires_at,
	active_at, reconnect_expires_at, hard_expires_at, terminated_at, termination_reason,
	source_ip_hash, source_user_agent_hash, browser_to_agent_bytes, agent_to_browser_bytes`

func (s *Store) CreateDesktopSession(ctx context.Context, session domain.DesktopSession, browser, agent domain.DesktopJoinCredential, event domain.DesktopSessionEvent) error {
	if err := validateDesktopSession(session); err != nil {
		return err
	}
	if session.State != string(protocol.DesktopSessionRequested) || session.KeyEpoch != 1 {
		return errors.New("new desktop session must be requested at epoch 1")
	}
	if err := validateDesktopCredentialPair(session, browser, agent); err != nil {
		return err
	}
	if err := validateDesktopEvent(event, session.ID); err != nil {
		return err
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO desktop_sessions (
			id, home_id, agent_id, operator_user_id, operator_device_identity_id,
			requested_permissions, effective_permissions, state, key_epoch,
			requested_at, join_expires_at, active_at, reconnect_expires_at, hard_expires_at,
			terminated_at, termination_reason, source_ip_hash, source_user_agent_hash,
			browser_to_agent_bytes, agent_to_browser_bytes
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		desktopSessionArgs(session)...); err != nil {
		return mapDesktopStoreError(err)
	}
	if err := insertDesktopJoinCredential(ctx, tx, browser); err != nil {
		return mapDesktopStoreError(err)
	}
	if err := insertDesktopJoinCredential(ctx, tx, agent); err != nil {
		return mapDesktopStoreError(err)
	}
	if err := appendDesktopSessionEvent(ctx, tx, event); err != nil {
		return err
	}
	return mapDesktopStoreError(tx.Commit())
}

func (s *Store) GetDesktopSession(ctx context.Context, sessionID string) (domain.DesktopSession, error) {
	return scanDesktopSession(s.queryRow(ctx, `SELECT `+desktopSessionColumns+`
		FROM desktop_sessions WHERE id = ?`, sessionID))
}

func (s *Store) GetDesktopSessionForUser(ctx context.Context, sessionID, userID string) (domain.DesktopSession, error) {
	return scanDesktopSession(s.queryRow(ctx, `SELECT `+desktopSessionColumns+`
		FROM desktop_sessions WHERE id = ? AND operator_user_id = ?`, sessionID, userID))
}

func (s *Store) TransitionDesktopSession(ctx context.Context, sessionID string, allowedFrom []string, nextState, reason string, at time.Time, event domain.DesktopSessionEvent) (domain.DesktopSession, error) {
	if strings.TrimSpace(sessionID) == "" || len(allowedFrom) == 0 || at.IsZero() {
		return domain.DesktopSession{}, errors.New("invalid desktop session transition")
	}
	if err := validateDesktopEvent(event, sessionID); err != nil {
		return domain.DesktopSession{}, err
	}
	next := protocol.DesktopSessionState(nextState)
	if !knownDesktopSessionState(next) {
		return domain.DesktopSession{}, errors.New("unknown desktop session state")
	}
	if isTerminalDesktopState(next) && strings.TrimSpace(reason) == "" {
		return domain.DesktopSession{}, errors.New("terminal desktop transition requires a reason")
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return domain.DesktopSession{}, err
	}
	defer tx.Rollback()
	session, err := getDesktopSessionForUpdate(ctx, tx, sessionID)
	if err != nil {
		return domain.DesktopSession{}, err
	}
	current := protocol.DesktopSessionState(session.State)
	if !slices.Contains(allowedFrom, session.State) || !protocol.CanTransitionDesktopSession(current, next) {
		return domain.DesktopSession{}, fmt.Errorf("%w: illegal desktop transition %s -> %s", ErrConflict, current, next)
	}

	var activeAt any = session.ActiveAt
	if next == protocol.DesktopSessionActive && session.ActiveAt == nil {
		activeAt = at
	}
	var terminatedAt, terminationReason any
	if isTerminalDesktopState(next) {
		terminatedAt = at
		terminationReason = reason
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_sessions
		SET state = ?, active_at = ?, terminated_at = ?, termination_reason = ?
		WHERE id = ?`, nextState, activeAt, terminatedAt, terminationReason, sessionID); err != nil {
		return domain.DesktopSession{}, err
	}
	if isTerminalDesktopState(next) {
		if _, err := tx.ExecContext(ctx, `UPDATE desktop_join_credentials
			SET revoked_at = ?
			WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, at, sessionID); err != nil {
			return domain.DesktopSession{}, err
		}
	}
	if err := appendDesktopSessionEvent(ctx, tx, event); err != nil {
		return domain.DesktopSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DesktopSession{}, mapDesktopStoreError(err)
	}
	return s.GetDesktopSession(ctx, sessionID)
}

func (s *Store) BeginDesktopReconnect(ctx context.Context, sessionID string, expectedEpoch uint32, reconnectExpiresAt time.Time, browser, agent domain.DesktopJoinCredential, event domain.DesktopSessionEvent) (domain.DesktopSession, error) {
	if strings.TrimSpace(sessionID) == "" || expectedEpoch == 0 || reconnectExpiresAt.IsZero() {
		return domain.DesktopSession{}, errors.New("invalid desktop reconnect")
	}
	if err := validateDesktopEvent(event, sessionID); err != nil {
		return domain.DesktopSession{}, err
	}

	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.DesktopSession{}, err
	}
	defer tx.Rollback()
	session, err := getDesktopSessionForUpdate(ctx, tx, sessionID)
	if err != nil {
		return domain.DesktopSession{}, err
	}
	if (session.State != string(protocol.DesktopSessionActive) && session.State != string(protocol.DesktopSessionReconnecting)) || session.KeyEpoch != expectedEpoch ||
		!reconnectExpiresAt.After(browser.CreatedAt) || reconnectExpiresAt.After(session.HardExpiresAt) {
		return domain.DesktopSession{}, fmt.Errorf("%w: desktop session cannot reconnect", ErrConflict)
	}
	nextEpoch := expectedEpoch + 1
	browser.SessionID, agent.SessionID = sessionID, sessionID
	if browser.KeyEpoch != nextEpoch || agent.KeyEpoch != nextEpoch || browser.ExpiresAt != reconnectExpiresAt || agent.ExpiresAt != reconnectExpiresAt {
		return domain.DesktopSession{}, errors.New("reconnect credentials do not match the next epoch")
	}
	prospective := session
	prospective.KeyEpoch = nextEpoch
	prospective.RequestedAt = browser.CreatedAt
	prospective.JoinExpiresAt = reconnectExpiresAt
	if err := validateDesktopCredentialPair(prospective, browser, agent); err != nil {
		return domain.DesktopSession{}, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE desktop_sessions
		SET state = 'reconnecting', key_epoch = ?, reconnect_expires_at = ?
		WHERE id = ?`, nextEpoch, reconnectExpiresAt, sessionID); err != nil {
		return domain.DesktopSession{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_join_credentials
		SET revoked_at = ?
		WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, browser.CreatedAt, sessionID); err != nil {
		return domain.DesktopSession{}, err
	}
	if err := insertDesktopJoinCredential(ctx, tx, browser); err != nil {
		return domain.DesktopSession{}, mapDesktopStoreError(err)
	}
	if err := insertDesktopJoinCredential(ctx, tx, agent); err != nil {
		return domain.DesktopSession{}, mapDesktopStoreError(err)
	}
	if err := appendDesktopSessionEvent(ctx, tx, event); err != nil {
		return domain.DesktopSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DesktopSession{}, mapDesktopStoreError(err)
	}
	return s.GetDesktopSession(ctx, sessionID)
}

func (s *Store) MarkDesktopTransportLost(ctx context.Context, sessionID string, expectedEpoch uint32, reconnectExpiresAt time.Time, event domain.DesktopSessionEvent) (domain.DesktopSession, error) {
	if strings.TrimSpace(sessionID) == "" || expectedEpoch == 0 || reconnectExpiresAt.IsZero() || validateDesktopEvent(event, sessionID) != nil {
		return domain.DesktopSession{}, errors.New("invalid desktop transport-loss transition")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return domain.DesktopSession{}, err
	}
	defer tx.Rollback()
	session, err := getDesktopSessionForUpdate(ctx, tx, sessionID)
	if err != nil {
		return domain.DesktopSession{}, err
	}
	if session.State != string(protocol.DesktopSessionActive) || session.KeyEpoch != expectedEpoch || !reconnectExpiresAt.After(event.OccurredAt) || reconnectExpiresAt.After(session.HardExpiresAt) {
		return domain.DesktopSession{}, fmt.Errorf("%w: desktop transport loss is stale", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE desktop_sessions SET state = 'reconnecting', reconnect_expires_at = ? WHERE id = ?`, reconnectExpiresAt, sessionID); err != nil {
		return domain.DesktopSession{}, err
	}
	if err := appendDesktopSessionEvent(ctx, tx, event); err != nil {
		return domain.DesktopSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DesktopSession{}, mapDesktopStoreError(err)
	}
	return s.GetDesktopSession(ctx, sessionID)
}

func (s *Store) ConsumeDesktopJoinCredential(ctx context.Context, hash []byte, side, sessionID string, epoch uint32, now time.Time) (domain.DesktopJoinCredential, error) {
	if len(hash) == 0 || (side != "browser" && side != "agent") || strings.TrimSpace(sessionID) == "" || epoch == 0 || now.IsZero() {
		return domain.DesktopJoinCredential{}, ErrNotFound
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return domain.DesktopJoinCredential{}, err
	}
	defer tx.Rollback()
	if _, err := getDesktopSessionForUpdate(ctx, tx, sessionID); err != nil {
		return domain.DesktopJoinCredential{}, err
	}
	var credential domain.DesktopJoinCredential
	err = tx.QueryRowContext(ctx, `UPDATE desktop_join_credentials
		SET consumed_at = ?
		WHERE credential_hash = ? AND side = ? AND session_id = ? AND key_epoch = ?
			AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?
		RETURNING id, session_id, side, credential_hash, key_epoch, created_at, expires_at, consumed_at, revoked_at`,
		now, hash, side, sessionID, epoch, now).Scan(
		&credential.ID,
		&credential.SessionID,
		&credential.Side,
		&credential.CredentialHash,
		&credential.KeyEpoch,
		&credential.CreatedAt,
		&credential.ExpiresAt,
		&credential.ConsumedAt,
		&credential.RevokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DesktopJoinCredential{}, ErrNotFound
	}
	if err != nil {
		return domain.DesktopJoinCredential{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"side": side, "key_epoch": epoch})
	if err := appendDesktopSessionEvent(ctx, tx, domain.DesktopSessionEvent{
		SessionID: sessionID, EventType: "credential.consumed", ActorType: side,
		OccurredAt: now, Severity: "info", MetadataJSON: string(metadata),
	}); err != nil {
		return domain.DesktopJoinCredential{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.DesktopJoinCredential{}, mapDesktopStoreError(err)
	}
	return credential, nil
}

func (s *Store) AddDesktopRelayBytes(ctx context.Context, sessionID string, browserToAgent, agentToBrowser int64) error {
	if strings.TrimSpace(sessionID) == "" || browserToAgent < 0 || agentToBrowser < 0 {
		return errors.New("invalid desktop relay byte counters")
	}
	result, err := s.exec(ctx, `UPDATE desktop_sessions
		SET browser_to_agent_bytes = browser_to_agent_bytes + ?,
			agent_to_browser_bytes = agent_to_browser_bytes + ?
		WHERE id = ?`, browserToAgent, agentToBrowser, sessionID)
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

func (s *Store) AppendDesktopSessionEvent(ctx context.Context, event domain.DesktopSessionEvent) error {
	if err := validateDesktopEvent(event, event.SessionID); err != nil {
		return err
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := getDesktopSessionForUpdate(ctx, tx, event.SessionID); err != nil {
		return err
	}
	if err := appendDesktopSessionEvent(ctx, tx, event); err != nil {
		return err
	}
	return mapDesktopStoreError(tx.Commit())
}

func (s *Store) ListDesktopSessionEvents(ctx context.Context, sessionID string, afterSequence int64, limit int) ([]domain.DesktopSessionEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.query(ctx, `SELECT session_id, sequence, event_type, actor_type, actor_id,
			occurred_at, severity, reason_code, metadata::text
		FROM desktop_session_events
		WHERE session_id = ? AND sequence > ?
		ORDER BY sequence ASC
		LIMIT ?`, sessionID, afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.DesktopSessionEvent, 0)
	for rows.Next() {
		var event domain.DesktopSessionEvent
		if err := rows.Scan(
			&event.SessionID,
			&event.Sequence,
			&event.EventType,
			&event.ActorType,
			&event.ActorID,
			&event.OccurredAt,
			&event.Severity,
			&event.ReasonCode,
			&event.MetadataJSON,
		); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) DesktopSessionStateCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.query(ctx, `SELECT state, COUNT(*) FROM desktop_sessions GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		counts[state] = count
	}
	return counts, rows.Err()
}

func (s *Store) DesktopSessionStatePlatformCounts(ctx context.Context) (map[string]int64, error) {
	rows, err := s.query(ctx, `SELECT state, platform, COUNT(*) FROM (
		SELECT session.state,
			COALESCE((SELECT CASE WHEN event.metadata->>'platform' IN ('windows','macos') THEN event.metadata->>'platform' END
				FROM desktop_session_events AS event WHERE event.session_id=session.id AND (event.metadata->>'platform') IS NOT NULL
				ORDER BY event.sequence DESC LIMIT 1), 'unknown') AS platform
		FROM desktop_sessions AS session
	) AS sessions GROUP BY state, platform`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var state, platform string
		var count int64
		if err := rows.Scan(&state, &platform, &count); err != nil {
			return nil, err
		}
		counts[platform+"\x00"+state] = count
	}
	return counts, rows.Err()
}

func (s *Store) ListLiveDesktopSessionIDs(ctx context.Context, homeID, operatorIdentityID, agentID string) ([]string, error) {
	if strings.TrimSpace(homeID) == "" {
		return nil, errors.New("desktop home is required")
	}
	rows, err := s.query(ctx, `SELECT id FROM desktop_sessions
		WHERE home_id = ? AND state IN ('requested','offered','agent_ready','joining','active','reconnecting')
			AND (? = '' OR operator_device_identity_id = ?)
			AND (? = '' OR agent_id = ?)
		ORDER BY requested_at ASC`, homeID, operatorIdentityID, operatorIdentityID, agentID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

type DesktopSessionAggregate struct {
	SessionID            string     `json:"session_id"`
	AgentID              string     `json:"agent_id"`
	OperatorUserID       string     `json:"operator_user_id"`
	State                string     `json:"state"`
	TerminationReason    string     `json:"termination_reason"`
	Platform             string     `json:"platform"`
	RequestedAt          time.Time  `json:"requested_at"`
	ActiveAt             *time.Time `json:"active_at"`
	TerminatedAt         *time.Time `json:"terminated_at"`
	DurationMilliseconds int64      `json:"duration_ms"`
	KeyEpoch             uint32     `json:"epoch_count"`
	BrowserToAgentBytes  int64      `json:"browser_to_agent_bytes"`
	AgentToBrowserBytes  int64      `json:"agent_to_browser_bytes"`
	Permissions          []string   `json:"permissions"`
}

func (s *Store) ListTerminalDesktopSessions(ctx context.Context, homeID string, offset, limit int) ([]DesktopSessionAggregate, error) {
	if strings.TrimSpace(homeID) == "" {
		return nil, errors.New("desktop home is required")
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 25
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.query(ctx, `SELECT session.id, session.agent_id, session.operator_user_id, session.state, COALESCE(session.termination_reason,''),
		session.requested_at, session.active_at, session.terminated_at, session.key_epoch, session.browser_to_agent_bytes, session.agent_to_browser_bytes,
		to_json(session.effective_permissions)::text,
		COALESCE((SELECT event.metadata->>'platform' FROM desktop_session_events AS event WHERE event.session_id = session.id AND (event.metadata->>'platform') IS NOT NULL ORDER BY event.sequence DESC LIMIT 1), 'unknown')
		FROM desktop_sessions AS session WHERE session.home_id = ? AND session.state IN ('denied','failed','expired','terminated')
		ORDER BY session.terminated_at DESC NULLS LAST, session.requested_at DESC, session.id DESC LIMIT ? OFFSET ?`, homeID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DesktopSessionAggregate, 0)
	for rows.Next() {
		var value DesktopSessionAggregate
		var permissionsJSON string
		if err := rows.Scan(&value.SessionID, &value.AgentID, &value.OperatorUserID, &value.State, &value.TerminationReason, &value.RequestedAt, &value.ActiveAt, &value.TerminatedAt, &value.KeyEpoch, &value.BrowserToAgentBytes, &value.AgentToBrowserBytes, &permissionsJSON, &value.Platform); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(permissionsJSON), &value.Permissions)
		if value.Permissions == nil {
			value.Permissions = []string{}
		}
		if value.ActiveAt != nil && value.TerminatedAt != nil && value.TerminatedAt.After(*value.ActiveAt) {
			value.DurationMilliseconds = value.TerminatedAt.Sub(*value.ActiveAt).Milliseconds()
		}
		items = append(items, value)
	}
	return items, rows.Err()
}

func (s *Store) ListDesktopAgentReadinessEvents(ctx context.Context, homeID, agentID string, limit int) ([]domain.DesktopSessionEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.query(ctx, `SELECT event.session_id,event.sequence,event.event_type,event.actor_type,event.actor_id,event.occurred_at,event.severity,event.reason_code,event.metadata::text
		FROM desktop_session_events AS event JOIN desktop_sessions AS session ON session.id=event.session_id
		WHERE session.home_id=? AND session.agent_id=? AND event.event_type IN ('desktop.session.ready','desktop.permission.required','desktop.permission.granted','desktop.permission.lost','desktop.helper.restarted','desktop.helper.failed','desktop.indicator.lost','desktop.indicator.restored','desktop.secure_desktop.unavailable')
		ORDER BY event.occurred_at DESC,event.sequence DESC LIMIT ?`, homeID, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]domain.DesktopSessionEvent, 0)
	for rows.Next() {
		var event domain.DesktopSessionEvent
		if err := rows.Scan(&event.SessionID, &event.Sequence, &event.EventType, &event.ActorType, &event.ActorID, &event.OccurredAt, &event.Severity, &event.ReasonCode, &event.MetadataJSON); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) ExpireDesktopSessions(ctx context.Context, now time.Time) (int64, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,
		CASE
			WHEN hard_expires_at <= ? THEN 'hard_expired'
			WHEN state = 'reconnecting' AND reconnect_expires_at <= ? THEN 'reconnect_expired'
			ELSE 'join_expired'
		END
		FROM desktop_sessions
		WHERE state IN ('requested','offered','agent_ready','joining','active','reconnecting')
			AND (hard_expires_at <= ?
				OR (state IN ('requested','offered','agent_ready','joining') AND join_expires_at <= ?)
				OR (state = 'reconnecting' AND reconnect_expires_at <= ?))
		FOR UPDATE`, now, now, now, now, now)
	if err != nil {
		return 0, err
	}
	type expiration struct{ id, reason string }
	var expirations []expiration
	for rows.Next() {
		var value expiration
		if err := rows.Scan(&value.id, &value.reason); err != nil {
			rows.Close()
			return 0, err
		}
		expirations = append(expirations, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, expiration := range expirations {
		if _, err := tx.ExecContext(ctx, `UPDATE desktop_sessions
			SET state = 'expired', terminated_at = ?, termination_reason = ?
			WHERE id = ?`, now, expiration.reason, expiration.id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE desktop_join_credentials
			SET revoked_at = ? WHERE session_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, now, expiration.id); err != nil {
			return 0, err
		}
		if err := appendDesktopSessionEvent(ctx, tx, domain.DesktopSessionEvent{
			SessionID: expiration.id, EventType: "session.expired", ActorType: "server",
			OccurredAt: now, Severity: "warning", ReasonCode: expiration.reason, MetadataJSON: `{}`,
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, mapDesktopStoreError(err)
	}
	return int64(len(expirations)), nil
}

func (s *Store) PruneDesktopState(ctx context.Context, credentialBefore, eventBefore, sessionBefore time.Time) (credentials, events, sessions int64, err error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()
	credentialResult, err := tx.ExecContext(ctx, `DELETE FROM desktop_join_credentials
		WHERE expires_at < ?`, credentialBefore)
	if err != nil {
		return 0, 0, 0, err
	}
	eventResult, err := tx.ExecContext(ctx, `DELETE FROM desktop_session_events WHERE occurred_at < ?`, eventBefore)
	if err != nil {
		return 0, 0, 0, err
	}
	sessionResult, err := tx.ExecContext(ctx, `DELETE FROM desktop_sessions
		WHERE state IN ('denied','failed','expired','terminated') AND terminated_at < ?`, sessionBefore)
	if err != nil {
		return 0, 0, 0, err
	}
	credentials, err = credentialResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, err
	}
	events, err = eventResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, err
	}
	sessions, err = sessionResult.RowsAffected()
	if err != nil {
		return 0, 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, mapDesktopStoreError(err)
	}
	return credentials, events, sessions, nil
}

func getDesktopSessionForUpdate(ctx context.Context, tx *dbTx, sessionID string) (domain.DesktopSession, error) {
	session, err := scanDesktopSession(tx.QueryRowContext(ctx, `SELECT `+desktopSessionColumns+`
		FROM desktop_sessions WHERE id = ? FOR UPDATE`, sessionID))
	return session, mapDesktopStoreError(err)
}

func scanDesktopSession(scanner interface{ Scan(dest ...any) error }) (domain.DesktopSession, error) {
	var session domain.DesktopSession
	var terminationReason sql.NullString
	var requestedPermissionsJSON, effectivePermissionsJSON string
	err := scanner.Scan(
		&session.ID,
		&session.HomeID,
		&session.AgentID,
		&session.OperatorUserID,
		&session.OperatorDeviceIdentityID,
		&requestedPermissionsJSON,
		&effectivePermissionsJSON,
		&session.State,
		&session.KeyEpoch,
		&session.RequestedAt,
		&session.JoinExpiresAt,
		&session.ActiveAt,
		&session.ReconnectExpiresAt,
		&session.HardExpiresAt,
		&session.TerminatedAt,
		&terminationReason,
		&session.SourceIPHash,
		&session.SourceUserAgentHash,
		&session.BrowserToAgentBytes,
		&session.AgentToBrowserBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DesktopSession{}, ErrNotFound
	}
	if err != nil {
		return domain.DesktopSession{}, err
	}
	if err := json.Unmarshal([]byte(requestedPermissionsJSON), &session.RequestedPermissions); err != nil {
		return domain.DesktopSession{}, fmt.Errorf("decode desktop requested permissions: %w", err)
	}
	if err := json.Unmarshal([]byte(effectivePermissionsJSON), &session.EffectivePermissions); err != nil {
		return domain.DesktopSession{}, fmt.Errorf("decode desktop effective permissions: %w", err)
	}
	session.TerminationReason = terminationReason.String
	return session, nil
}

func desktopSessionArgs(session domain.DesktopSession) []any {
	return []any{
		session.ID,
		session.HomeID,
		session.AgentID,
		session.OperatorUserID,
		session.OperatorDeviceIdentityID,
		session.RequestedPermissions,
		session.EffectivePermissions,
		session.State,
		session.KeyEpoch,
		session.RequestedAt,
		session.JoinExpiresAt,
		session.ActiveAt,
		session.ReconnectExpiresAt,
		session.HardExpiresAt,
		session.TerminatedAt,
		nilIfEmptyString(session.TerminationReason),
		session.SourceIPHash,
		session.SourceUserAgentHash,
		session.BrowserToAgentBytes,
		session.AgentToBrowserBytes,
	}
}

func insertDesktopJoinCredential(ctx context.Context, tx *dbTx, credential domain.DesktopJoinCredential) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO desktop_join_credentials (
			id, session_id, side, credential_hash, key_epoch, created_at, expires_at, consumed_at, revoked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		credential.ID,
		credential.SessionID,
		credential.Side,
		credential.CredentialHash,
		credential.KeyEpoch,
		credential.CreatedAt,
		credential.ExpiresAt,
		credential.ConsumedAt,
		credential.RevokedAt,
	)
	return err
}

func appendDesktopSessionEvent(ctx context.Context, tx *dbTx, event domain.DesktopSessionEvent) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1
		FROM desktop_session_events WHERE session_id = ?`, event.SessionID).Scan(&sequence); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO desktop_session_events (
			session_id, sequence, event_type, actor_type, actor_id,
			occurred_at, severity, reason_code, metadata
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)`,
		event.SessionID,
		sequence,
		event.EventType,
		event.ActorType,
		event.ActorID,
		event.OccurredAt,
		event.Severity,
		event.ReasonCode,
		event.MetadataJSON,
	)
	return err
}

func validateDesktopSession(session domain.DesktopSession) error {
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.HomeID) == "" ||
		strings.TrimSpace(session.AgentID) == "" || strings.TrimSpace(session.OperatorUserID) == "" ||
		strings.TrimSpace(session.OperatorDeviceIdentityID) == "" || session.KeyEpoch == 0 ||
		session.RequestedAt.IsZero() || !session.JoinExpiresAt.After(session.RequestedAt) ||
		session.JoinExpiresAt.After(session.RequestedAt.Add(time.Minute)) ||
		!session.HardExpiresAt.After(session.RequestedAt) ||
		session.HardExpiresAt.After(session.RequestedAt.Add(8*time.Hour)) {
		return errors.New("invalid desktop session")
	}
	if !knownDesktopSessionState(protocol.DesktopSessionState(session.State)) {
		return errors.New("unknown desktop session state")
	}
	if err := validateDesktopPermissionStrings(session.RequestedPermissions); err != nil {
		return err
	}
	if err := validateDesktopPermissionStrings(session.EffectivePermissions); err != nil {
		return err
	}
	for _, permission := range session.EffectivePermissions {
		if !slices.Contains(session.RequestedPermissions, permission) {
			return errors.New("effective desktop permission was not requested")
		}
	}
	return nil
}

func validateDesktopPermissionStrings(values []string) error {
	permissions := make([]protocol.DesktopPermission, len(values))
	for index, value := range values {
		permissions[index] = protocol.DesktopPermission(value)
	}
	return protocol.ValidateDesktopPermissions(permissions)
}

func validateDesktopCredentialPair(session domain.DesktopSession, browser, agent domain.DesktopJoinCredential) error {
	if err := validateDesktopJoinCredential(browser); err != nil {
		return err
	}
	if err := validateDesktopJoinCredential(agent); err != nil {
		return err
	}
	if browser.Side != "browser" || agent.Side != "agent" ||
		browser.SessionID != session.ID || agent.SessionID != session.ID ||
		browser.KeyEpoch != session.KeyEpoch || agent.KeyEpoch != session.KeyEpoch ||
		!browser.CreatedAt.Equal(agent.CreatedAt) || !browser.CreatedAt.Equal(session.RequestedAt) ||
		!browser.ExpiresAt.Equal(agent.ExpiresAt) ||
		!browser.ExpiresAt.Equal(session.JoinExpiresAt) || slices.Equal(browser.CredentialHash, agent.CredentialHash) {
		return errors.New("desktop credential pair does not match the session")
	}
	return nil
}

func validateDesktopJoinCredential(credential domain.DesktopJoinCredential) error {
	if strings.TrimSpace(credential.ID) == "" || strings.TrimSpace(credential.SessionID) == "" ||
		(credential.Side != "browser" && credential.Side != "agent") || len(credential.CredentialHash) != sha256.Size ||
		credential.KeyEpoch == 0 || credential.CreatedAt.IsZero() || !credential.ExpiresAt.After(credential.CreatedAt) ||
		credential.ConsumedAt != nil || credential.RevokedAt != nil {
		return errors.New("invalid desktop join credential")
	}
	return nil
}

func validateDesktopEvent(event domain.DesktopSessionEvent, sessionID string) error {
	if event.SessionID != sessionID || strings.TrimSpace(event.EventType) == "" ||
		!slices.Contains([]string{"user", "agent", "server", "browser"}, event.ActorType) ||
		event.OccurredAt.IsZero() ||
		!slices.Contains([]string{"info", "warning", "error", "security"}, event.Severity) {
		return errors.New("invalid desktop session event")
	}
	var metadata map[string]any
	if !json.Valid([]byte(event.MetadataJSON)) || json.Unmarshal([]byte(event.MetadataJSON), &metadata) != nil || metadata == nil {
		return errors.New("desktop event metadata must be a JSON object")
	}
	return nil
}

func knownDesktopSessionState(state protocol.DesktopSessionState) bool {
	return slices.Contains([]protocol.DesktopSessionState{
		protocol.DesktopSessionRequested,
		protocol.DesktopSessionOffered,
		protocol.DesktopSessionAgentReady,
		protocol.DesktopSessionJoining,
		protocol.DesktopSessionActive,
		protocol.DesktopSessionReconnecting,
		protocol.DesktopSessionDenied,
		protocol.DesktopSessionFailed,
		protocol.DesktopSessionExpired,
		protocol.DesktopSessionTerminated,
	}, state)
}

func isTerminalDesktopState(state protocol.DesktopSessionState) bool {
	return slices.Contains([]protocol.DesktopSessionState{
		protocol.DesktopSessionDenied,
		protocol.DesktopSessionFailed,
		protocol.DesktopSessionExpired,
		protocol.DesktopSessionTerminated,
	}, state)
}
