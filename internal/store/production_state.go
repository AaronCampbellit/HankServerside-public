package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type CloudRuntimeStatus struct {
	DeploymentID string
	RuntimeID    string
	Version      string
	StartedAt    time.Time
	HeartbeatAt  time.Time
	ShutdownAt   *time.Time
}

type AppWebSocketTicketRecord struct {
	TokenHash  string
	SessionID  string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

type FileTransferRecord struct {
	ID          string
	TokenHash   string
	JobID       string
	HomeID      string
	UserID      string
	AgentID     string
	Operation   string
	SourceID    string
	Path        string
	Status      string
	BytesTotal  *int64
	BytesDone   int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	CompletedAt *time.Time
}

type AuditEvent struct {
	ID           string
	OccurredAt   time.Time
	ActorUserID  *string
	ActorAgentID *string
	HomeID       *string
	EventType    string
	Severity     string
	RequestID    string
	IPHash       string
	TargetType   string
	TargetID     string
	MetadataJSON string
}

type FileOperationJob struct {
	AgentID             string
	ID                  string
	HomeID              string
	UserID              string
	Operation           string
	SourceID            string
	DestinationSourceID string
	FromPath            string
	ToPath              string
	IsDirectory         bool
	Status              string
	BytesTotal          int64
	BytesDone           int64
	FilesTotal          int64
	FilesDone           int64
	ErrorMessage        string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CompletedAt         *time.Time
}

type QueryTelemetryRow struct {
	QueryID     string
	Calls       int64
	TotalExecMS float64
	MeanExecMS  float64
	Rows        int64
	Query       string
}

type LifecyclePruneSummary struct {
	AppSessionsDeleted              int64
	AgentTokensDeleted              int64
	HomeInvitationsDeleted          int64
	AppWebSocketTicketsDeleted      int64
	FileTransfersExpired            int64
	FileTransfersDeleted            int64
	RateLimitEventsDeleted          int64
	RelayRequestsDeleted            int64
	AppConnectionsDeleted           int64
	AgentConnectionsDeleted         int64
	AuditEventsDeleted              int64
	LoginBackoffDeleted             int64
	AssistantAttachmentsDeleted     int64
	NoteAttachmentRowsDeleted       int64
	MCPAttachmentUploadsExpired     int64
	MCPAttachmentUploadsDeleted     int64
	DesktopJoinCredentialsDeleted   int64
	DesktopSessionEventsDeleted     int64
	DesktopSessionsDeleted          int64
	UserNotificationsDeleted        int64
	NotificationSourceEventsDeleted int64
	WebPushDeliveriesDeleted        int64
	WebPushClaimsReclaimed          int64
}

func (s LifecyclePruneSummary) Empty() bool {
	return s.AppSessionsDeleted == 0 &&
		s.AgentTokensDeleted == 0 &&
		s.HomeInvitationsDeleted == 0 &&
		s.AppWebSocketTicketsDeleted == 0 &&
		s.FileTransfersExpired == 0 &&
		s.FileTransfersDeleted == 0 &&
		s.RateLimitEventsDeleted == 0 &&
		s.RelayRequestsDeleted == 0 &&
		s.AppConnectionsDeleted == 0 &&
		s.AgentConnectionsDeleted == 0 &&
		s.AuditEventsDeleted == 0 &&
		s.LoginBackoffDeleted == 0 &&
		s.AssistantAttachmentsDeleted == 0 &&
		s.NoteAttachmentRowsDeleted == 0 &&
		s.MCPAttachmentUploadsExpired == 0 &&
		s.MCPAttachmentUploadsDeleted == 0 &&
		s.DesktopJoinCredentialsDeleted == 0 &&
		s.DesktopSessionEventsDeleted == 0 &&
		s.DesktopSessionsDeleted == 0 &&
		s.UserNotificationsDeleted == 0 &&
		s.NotificationSourceEventsDeleted == 0 &&
		s.WebPushDeliveriesDeleted == 0 &&
		s.WebPushClaimsReclaimed == 0
}

func (s *Store) UpsertCloudRuntime(ctx context.Context, runtimeID string, version string) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `INSERT INTO cloud_runtime (deployment_id, runtime_id, version, started_at, heartbeat_at, shutdown_at)
		VALUES ('singleton', ?, ?, ?, ?, NULL)
		ON CONFLICT(deployment_id) DO UPDATE SET
			runtime_id = excluded.runtime_id,
			version = excluded.version,
			started_at = excluded.started_at,
			heartbeat_at = excluded.heartbeat_at,
			shutdown_at = NULL`,
		runtimeID, version, now, now)
	return err
}

func (s *Store) HeartbeatCloudRuntime(ctx context.Context, runtimeID string) error {
	_, err := s.exec(ctx, `UPDATE cloud_runtime SET heartbeat_at = ?, shutdown_at = NULL WHERE deployment_id = 'singleton' AND runtime_id = ?`, time.Now().UTC(), runtimeID)
	return err
}

func (s *Store) ShutdownCloudRuntime(ctx context.Context, runtimeID string) error {
	now := time.Now().UTC()
	_, err := s.exec(ctx, `UPDATE cloud_runtime SET heartbeat_at = ?, shutdown_at = ? WHERE deployment_id = 'singleton' AND runtime_id = ?`, now, now, runtimeID)
	return err
}

func (s *Store) GetCloudRuntime(ctx context.Context) (CloudRuntimeStatus, error) {
	row := s.queryRow(ctx, `SELECT deployment_id, runtime_id, version, started_at, heartbeat_at, shutdown_at FROM cloud_runtime WHERE deployment_id = 'singleton'`)
	var status CloudRuntimeStatus
	var shutdownAt sql.NullTime
	if err := row.Scan(&status.DeploymentID, &status.RuntimeID, &status.Version, &status.StartedAt, &status.HeartbeatAt, &shutdownAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CloudRuntimeStatus{}, ErrNotFound
		}
		return CloudRuntimeStatus{}, err
	}
	if shutdownAt.Valid {
		status.ShutdownAt = &shutdownAt.Time
	}
	return status, nil
}

func (s *Store) CountHomes(ctx context.Context) (int, error) {
	row := s.queryRow(ctx, `SELECT COUNT(*) FROM homes`)
	var count int
	return count, row.Scan(&count)
}

func (s *Store) CreateAppWebSocketTicket(ctx context.Context, tokenHash string, sessionID string, userID string, ttl time.Duration) (time.Time, error) {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	_, err := s.exec(ctx, `INSERT INTO app_ws_tickets (token_hash, session_id, user_id, created_at, expires_at, consumed_at)
		VALUES (?, ?, ?, ?, ?, NULL)`, tokenHash, sessionID, userID, now, expiresAt)
	return expiresAt, err
}

func (s *Store) ConsumeAppWebSocketTicket(ctx context.Context, tokenHash string) (AppWebSocketTicketRecord, error) {
	now := time.Now().UTC()
	row := s.queryRow(ctx, `UPDATE app_ws_tickets
		SET consumed_at = ?
		WHERE token_hash = ? AND consumed_at IS NULL AND expires_at > ?
		RETURNING token_hash, session_id, user_id, created_at, expires_at, consumed_at`,
		now, tokenHash, now)
	return scanAppWebSocketTicket(row)
}

func (s *Store) CreateFileTransfer(ctx context.Context, transfer FileTransferRecord) error {
	if transfer.Status == "" {
		transfer.Status = "pending"
	}
	_, err := s.exec(ctx, `INSERT INTO file_transfers (
			id, token_hash, file_job_id, home_id, user_id, agent_id, operation, source_id, path, status,
			bytes_total, bytes_done, created_at, expires_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		transfer.ID,
		transfer.TokenHash,
		transfer.JobID,
		transfer.HomeID,
		transfer.UserID,
		transfer.AgentID,
		transfer.Operation,
		transfer.SourceID,
		transfer.Path,
		transfer.Status,
		transfer.BytesTotal,
		transfer.BytesDone,
		transfer.CreatedAt,
		transfer.ExpiresAt,
		transfer.CompletedAt,
	)
	return err
}

func (s *Store) GetFileTransfer(ctx context.Context, id string) (FileTransferRecord, error) {
	row := s.queryRow(ctx, `SELECT id, token_hash, file_job_id, home_id, user_id, agent_id, operation, source_id, path, status,
			bytes_total, bytes_done, created_at, expires_at, completed_at
		FROM file_transfers
		WHERE id = ?`, id)
	return scanFileTransfer(row)
}

func (s *Store) UpdateFileTransferProgress(ctx context.Context, id string, status string, bytesTotal *int64, bytesDone int64, completedAt *time.Time) error {
	_, err := s.exec(ctx, `UPDATE file_transfers
		SET status = ?, bytes_total = ?, bytes_done = ?, completed_at = ?
		WHERE id = ? AND status <> 'expired' AND expires_at > CURRENT_TIMESTAMP`, status, bytesTotal, bytesDone, completedAt, id)
	return err
}

func (s *Store) CreateRelayRequest(ctx context.Context, requestID string, homeID string, appConnectionID string, command string, payload json.RawMessage, ttl time.Duration) error {
	if strings.HasPrefix(command, "apps.") || command == "shell.exec" {
		payload = nil
	}
	if ttl <= 0 {
		ttl = 120 * time.Second
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	now := time.Now().UTC()
	_, err := s.exec(ctx, `INSERT INTO relay_requests (
			request_id, home_id, app_connection_id, command, request_payload, status, created_at, expires_at
		) VALUES (?, ?, ?, ?, ?::jsonb, 'pending', ?, ?)`,
		requestID, homeID, appConnectionID, command, string(payload), now, now.Add(ttl))
	return err
}

func (s *Store) MarkRelayRequestSent(ctx context.Context, requestID string, agentConnectionID string) error {
	_, err := s.exec(ctx, `UPDATE relay_requests SET status = 'sent', agent_connection_id = ?, sent_at = ? WHERE request_id = ?`, agentConnectionID, time.Now().UTC(), requestID)
	return err
}

func (s *Store) CompleteRelayRequest(ctx context.Context, requestID string, payload json.RawMessage) error {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := s.exec(ctx, `UPDATE relay_requests
		SET status = 'completed', response_payload = CASE WHEN command LIKE 'apps.%' OR command = 'shell.exec' THEN '{}'::jsonb ELSE ?::jsonb END, completed_at = ?
		WHERE request_id = ?`, string(payload), time.Now().UTC(), requestID)
	return err
}

func (s *Store) FailRelayRequest(ctx context.Context, requestID string, status string, code string, message string) error {
	if status == "" {
		status = "failed"
	}
	_, err := s.exec(ctx, `UPDATE relay_requests
		SET status = ?, error_code = ?, error_message = CASE WHEN command LIKE 'apps.%' THEN 'App request failed' WHEN command = 'shell.exec' THEN 'Shell request failed' ELSE ? END, completed_at = ?
		WHERE request_id = ?`, status, code, message, time.Now().UTC(), requestID)
	return err
}

func (s *Store) ExpireRelayRequests(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.exec(ctx, `UPDATE relay_requests
		SET status = 'timed_out', error_code = 'request_timeout', error_message = 'request expired during cloud restart or timeout cleanup', completed_at = ?
		WHERE status IN ('pending', 'sent') AND expires_at <= ?`, now, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) MarkAgentConnection(ctx context.Context, connectionID string, runtimeID string, homeID string, agentID string, capabilities []string, connected bool) error {
	now := time.Now().UTC()
	data, _ := json.Marshal(capabilities)
	if connected {
		_, err := s.exec(ctx, `INSERT INTO agent_connections (
				connection_id, runtime_id, home_id, agent_id, capabilities_json, connected_at, heartbeat_at, disconnected_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT(connection_id) DO UPDATE SET
				capabilities_json = excluded.capabilities_json,
				heartbeat_at = excluded.heartbeat_at,
				disconnected_at = NULL`,
			connectionID, runtimeID, homeID, agentID, string(data), now, now)
		return err
	}
	_, err := s.exec(ctx, `UPDATE agent_connections SET disconnected_at = ?, heartbeat_at = ? WHERE connection_id = ?`, now, now, connectionID)
	return err
}

func (s *Store) HeartbeatAgentConnection(ctx context.Context, connectionID string, capabilities []string) error {
	data, _ := json.Marshal(capabilities)
	_, err := s.exec(ctx, `UPDATE agent_connections SET heartbeat_at = ?, capabilities_json = ? WHERE connection_id = ?`, time.Now().UTC(), string(data), connectionID)
	return err
}

func (s *Store) MarkAppConnection(ctx context.Context, connectionID string, runtimeID string, sessionID string, userID string, connected bool) error {
	now := time.Now().UTC()
	if connected {
		_, err := s.exec(ctx, `INSERT INTO app_connections (
				connection_id, runtime_id, session_id, user_id, connected_at, heartbeat_at, disconnected_at
			) VALUES (?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT(connection_id) DO UPDATE SET heartbeat_at = excluded.heartbeat_at, disconnected_at = NULL`,
			connectionID, runtimeID, sessionID, userID, now, now)
		return err
	}
	_, err := s.exec(ctx, `UPDATE app_connections SET disconnected_at = ?, heartbeat_at = ? WHERE connection_id = ?`, now, now, connectionID)
	return err
}

func (s *Store) CreateAuditEvent(ctx context.Context, event AuditEvent) error {
	if event.MetadataJSON == "" {
		event.MetadataJSON = "{}"
	}
	_, err := s.exec(ctx, `INSERT INTO audit_events (
			id, occurred_at, actor_user_id, actor_agent_id, home_id, event_type, severity,
			request_id, ip_hash, target_type, target_id, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)`,
		event.ID,
		event.OccurredAt,
		event.ActorUserID,
		event.ActorAgentID,
		event.HomeID,
		event.EventType,
		event.Severity,
		event.RequestID,
		event.IPHash,
		event.TargetType,
		event.TargetID,
		event.MetadataJSON,
	)
	return err
}

func (s *Store) EnsureAuditEvent(ctx context.Context, event AuditEvent) error {
	if event.MetadataJSON == "" {
		event.MetadataJSON = "{}"
	}
	_, err := s.exec(ctx, `INSERT INTO audit_events (
			id, occurred_at, actor_user_id, actor_agent_id, home_id, event_type, severity,
			request_id, ip_hash, target_type, target_id, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb)
		ON CONFLICT(id) DO NOTHING`,
		event.ID,
		event.OccurredAt,
		event.ActorUserID,
		event.ActorAgentID,
		event.HomeID,
		event.EventType,
		event.Severity,
		event.RequestID,
		event.IPHash,
		event.TargetType,
		event.TargetID,
		event.MetadataJSON,
	)
	return err
}

func (s *Store) GetAuditEventForUserNotification(ctx context.Context, userID, notificationID string) (AuditEvent, error) {
	return scanAuditEvent(s.queryRow(ctx, `SELECT a.id, a.occurred_at, a.actor_user_id, a.actor_agent_id, a.home_id,
			a.event_type, a.severity, a.request_id, a.ip_hash, a.target_type, a.target_id, a.metadata_json
		FROM user_notifications n
		JOIN audit_events a ON a.id = n.audit_event_id
		WHERE n.id = ? AND n.user_id = ?`, strings.TrimSpace(notificationID), strings.TrimSpace(userID)))
}

func (s *Store) ListAuditEvents(ctx context.Context, homeID string, eventType string, severity string, targetType string, limit int, sortField string, sortOrder string) ([]AuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	orderBy := "occurred_at"
	validSort := sortField == "" || sortField == "occurred_at"
	switch sortField {
	case "event_type", "severity", "target_type":
		orderBy = sortField
		validSort = true
	}
	order := "DESC"
	if validSort && strings.EqualFold(sortOrder, "asc") {
		order = "ASC"
	}
	rows, err := s.query(ctx, `SELECT id, occurred_at, actor_user_id, actor_agent_id, home_id,
			event_type, severity, request_id, ip_hash, target_type, target_id, metadata_json
		FROM audit_events
		WHERE (? = '' OR home_id = ?)
			AND (? = '' OR event_type = ?)
			AND (? <> '' OR event_type <> 'dashboard_entities.changed')
			AND (? = '' OR severity = ?)
			AND (? = '' OR target_type = ?)
		ORDER BY `+orderBy+` `+order+`, occurred_at DESC
		LIMIT ?`, homeID, homeID, eventType, eventType, eventType, severity, severity, targetType, targetType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		event, err := scanAuditEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) CreateFileOperationJob(ctx context.Context, job FileOperationJob) error {
	if job.Status == "" {
		job.Status = "queued"
	}
	_, err := s.exec(ctx, `INSERT INTO file_operation_jobs (
			id, home_id, agent_id, user_id, operation, source_id, destination_source_id, from_path, to_path,
			is_directory, status, bytes_total, bytes_done, files_total, files_done, error_message,
			created_at, updated_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		job.HomeID,
		sql.NullString{String: job.AgentID, Valid: job.AgentID != ""},
		job.UserID,
		job.Operation,
		job.SourceID,
		job.DestinationSourceID,
		job.FromPath,
		job.ToPath,
		job.IsDirectory,
		job.Status,
		job.BytesTotal,
		job.BytesDone,
		job.FilesTotal,
		job.FilesDone,
		job.ErrorMessage,
		job.CreatedAt,
		job.UpdatedAt,
		job.CompletedAt,
	)
	return err
}

func (s *Store) UpdateFileOperationJob(ctx context.Context, jobID string, status string, bytesDone int64, filesDone int64, errorMessage string, completedAt *time.Time) error {
	_, err := s.exec(ctx, `UPDATE file_operation_jobs
		SET status = ?, bytes_done = ?, files_done = ?, error_message = ?, updated_at = ?, completed_at = ?
		WHERE id = ?`, status, bytesDone, filesDone, errorMessage, time.Now().UTC(), completedAt, jobID)
	return err
}

func (s *Store) UpdateFileOperationJobMonotonic(ctx context.Context, jobID string, status string, bytesDone int64, filesDone int64, errorMessage string, completedAt *time.Time) (bool, error) {
	result, err := s.exec(ctx, `UPDATE file_operation_jobs
		SET status = ?, bytes_done = ?, files_done = ?, error_message = ?, updated_at = ?, completed_at = ?
		WHERE id = ?
		  AND status NOT IN ('completed', 'failed', 'cancelled', 'rollback_required', 'rolled_back')`,
		status, bytesDone, filesDone, errorMessage, time.Now().UTC(), completedAt, jobID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return rows > 0, nil
}

func (s *Store) UpdateFileOperationJobTotals(ctx context.Context, jobID string, bytesTotal int64, filesTotal int64) error {
	_, err := s.exec(ctx, `UPDATE file_operation_jobs
		SET bytes_total = ?, files_total = ?, updated_at = ?
		WHERE id = ?`, bytesTotal, filesTotal, time.Now().UTC(), jobID)
	return err
}

// UpdateOwnedMoveJob keeps authorization and the terminal-state guard in the
// same write. A cancellation may still reveal a verified copy needing rollback.
func (s *Store) UpdateOwnedMoveJob(ctx context.Context, job FileOperationJob, status string, bytesDone, filesDone, bytesTotal, filesTotal int64, message string, completedAt *time.Time) (bool, error) {
	result, err := s.exec(ctx, `UPDATE file_operation_jobs
		SET status = ?, bytes_done = ?, files_done = ?,
		bytes_total = CASE WHEN ? > 0 THEN ? ELSE bytes_total END,
		files_total = CASE WHEN ? > 0 THEN ? ELSE files_total END,
		error_message = ?, updated_at = ?, completed_at = ?
		WHERE id = ? AND home_id = ? AND agent_id = ? AND operation = 'move'
		AND (status IN ('queued', 'running') OR (status = 'cancelled' AND ? = 'rollback_required'))`,
		status, bytesDone, filesDone, bytesTotal, bytesTotal, filesTotal, filesTotal,
		message, time.Now().UTC(), completedAt, job.ID, job.HomeID, job.AgentID, status)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) GetFileOperationJob(ctx context.Context, jobID string) (FileOperationJob, error) {
	row := s.queryRow(ctx, `SELECT id, home_id, COALESCE(agent_id, ''), user_id, operation, source_id, destination_source_id,
			from_path, to_path, is_directory, status, bytes_total, bytes_done, files_total, files_done,
			error_message, created_at, updated_at, completed_at
		FROM file_operation_jobs
		WHERE id = ?`, jobID)
	return scanFileOperationJob(row)
}

func (s *Store) ListFileOperationJobs(ctx context.Context, homeID string, limit int) ([]FileOperationJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.query(ctx, `SELECT id, home_id, COALESCE(agent_id, ''), user_id, operation, source_id, destination_source_id,
			from_path, to_path, is_directory, status, bytes_total, bytes_done, files_total, files_done,
			error_message, created_at, updated_at, completed_at
		FROM file_operation_jobs
		WHERE home_id = ?
		ORDER BY updated_at DESC, created_at DESC
		LIMIT ?`, homeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []FileOperationJob
	for rows.Next() {
		job, err := scanFileOperationJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) DeleteTerminalFileOperationJobs(ctx context.Context, homeID string) (int64, error) {
	result, err := s.exec(ctx, `DELETE FROM file_operation_jobs
		WHERE home_id = ?
		  AND (status IN ('completed', 'failed', 'cancelled', 'rolled_back')
		       OR (operation IN ('upload', 'download') AND status = 'rollback_required'))`, homeID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) CountFileOperationJobsByStatus(ctx context.Context, homeID string) (map[string]int64, error) {
	rows, err := s.query(ctx, `SELECT status, COUNT(*) FROM file_operation_jobs WHERE home_id = ? GROUP BY status`, homeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Store) MarkInterruptedFileOperationJobs(ctx context.Context, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE file_operation_jobs
		SET status = CASE WHEN operation = 'move' THEN 'rollback_required' ELSE 'failed' END,
			error_message = CASE WHEN error_message = '' THEN 'cloud runtime restarted while job was active' ELSE error_message END,
			updated_at = ?, completed_at = CASE WHEN operation = 'move' THEN completed_at ELSE ? END
		WHERE status IN ('queued', 'running')`, now, now)
	return err
}

func (s *Store) TotalReadyNoteAttachmentBytes(ctx context.Context, homeID string) (int64, error) {
	row := s.queryRow(ctx, `SELECT COALESCE(SUM(size_bytes), 0)
		FROM note_attachments
		WHERE status = 'ready' AND deleted_at IS NULL AND (home_id = ? OR ? = '')`, homeID, homeID)
	var total int64
	return total, row.Scan(&total)
}

func (s *Store) TopQueryTelemetry(ctx context.Context, limit int) ([]QueryTelemetryRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.query(ctx, `SELECT queryid::text, calls, total_exec_time, mean_exec_time, rows,
			left(regexp_replace(query, '\s+', ' ', 'g'), 500)
		FROM pg_stat_statements
		ORDER BY total_exec_time DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []QueryTelemetryRow
	for rows.Next() {
		var item QueryTelemetryRow
		if err := rows.Scan(&item.QueryID, &item.Calls, &item.TotalExecMS, &item.MeanExecMS, &item.Rows, &item.Query); err != nil {
			return nil, err
		}
		results = append(results, item)
	}
	return results, rows.Err()
}

func (s *Store) AllowRateLimit(ctx context.Context, bucket string, key string, limit int, window time.Duration) (bool, error) {
	if limit <= 0 {
		return false, nil
	}
	now := time.Now().UTC()
	cutoff := now.Add(-window)
	keyHash := stableHash(key)
	_, _ = s.exec(ctx, `DELETE FROM rate_limit_events WHERE occurred_at < ?`, now.Add(-24*time.Hour))
	var count int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM rate_limit_events WHERE bucket = ? AND key_hash = ? AND occurred_at >= ?`, bucket, keyHash, cutoff).Scan(&count); err != nil {
		return false, err
	}
	if count >= limit {
		return false, nil
	}
	_, err := s.exec(ctx, `INSERT INTO rate_limit_events (bucket, key_hash, occurred_at) VALUES (?, ?, ?)`, bucket, keyHash, now)
	return err == nil, err
}

func (s *Store) LoginBackoffBlocked(ctx context.Context, email string) (time.Duration, bool, error) {
	row := s.queryRow(ctx, `SELECT blocked_until FROM login_backoff WHERE email_hash = ?`, stableHash(email))
	var blockedUntil sql.NullTime
	if err := row.Scan(&blockedUntil); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if !blockedUntil.Valid || !time.Now().UTC().Before(blockedUntil.Time) {
		return 0, false, nil
	}
	return time.Until(blockedUntil.Time), true, nil
}

func (s *Store) RecordLoginFailure(ctx context.Context, email string) error {
	now := time.Now().UTC()
	key := stableHash(email)
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var failures int
	row := tx.QueryRowContext(ctx, `SELECT failures FROM login_backoff WHERE email_hash = ? FOR UPDATE`, key)
	if err := row.Scan(&failures); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO login_backoff (email_hash, failures, blocked_until, updated_at) VALUES (?, 1, NULL, ?)`, key, now); err != nil {
			return err
		}
		return tx.Commit()
	}

	failures++
	var blockedUntil any
	if failures >= 5 {
		exponent := failures - 5
		if exponent > 5 {
			exponent = 5
		}
		blockedUntil = now.Add(time.Duration(1<<exponent) * time.Minute)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE login_backoff SET failures = ?, blocked_until = ?, updated_at = ? WHERE email_hash = ?`, failures, blockedUntil, now, key); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordLoginSuccess(ctx context.Context, email string) error {
	_, err := s.exec(ctx, `DELETE FROM login_backoff WHERE email_hash = ?`, stableHash(email))
	return err
}

func (s *Store) PruneLifecycle(ctx context.Context, now time.Time, retention time.Duration) error {
	_, err := s.PruneLifecycleWithSummary(ctx, now, retention)
	return err
}

func (s *Store) PruneLifecycleWithSummary(ctx context.Context, now time.Time, retention time.Duration) (LifecyclePruneSummary, error) {
	if retention <= 0 {
		retention = 30 * 24 * time.Hour
	}
	cutoff := now.Add(-retention)
	type pruneStatement struct {
		name string
		sql  string
		args []any
	}
	statements := []pruneStatement{
		{"web_push_claims_reclaimed", `UPDATE web_push_deliveries SET state = 'pending', claim_owner = '', claim_expires_at = NULL, updated_at = ? WHERE state = 'claimed' AND claim_expires_at < ?`, []any{now, now}},
		{"web_push_deliveries", `DELETE FROM web_push_deliveries WHERE state IN ('cancelled', 'terminal') AND updated_at < ?`, []any{cutoff}},
		{"user_notifications", `DELETE FROM user_notifications WHERE last_occurred_at < ?`, []any{now.Add(-30 * 24 * time.Hour)}},
		{"notification_source_events", `DELETE FROM notification_source_events WHERE emitted_at < ?`, []any{cutoff}},
		{"app_sessions", `DELETE FROM app_sessions WHERE expires_at < ? OR revoked_at < ?`, []any{now, cutoff}},
		{"agent_tokens", `DELETE FROM agent_tokens WHERE expires_at < ? OR revoked_at < ?`, []any{now, cutoff}},
		{"home_invitations", `DELETE FROM home_invitations WHERE expires_at < ? OR accepted_at < ?`, []any{now, cutoff}},
		{"app_ws_tickets", `DELETE FROM app_ws_tickets WHERE expires_at < ? OR consumed_at < ?`, []any{now, cutoff}},
		{"file_transfers_expired", `UPDATE file_transfers SET status = 'expired' WHERE status IN ('pending', 'active') AND expires_at < ?`, []any{now}},
		{"file_transfers_deleted", `DELETE FROM file_transfers WHERE status IN ('completed', 'failed', 'expired') AND (completed_at < ? OR expires_at < ?)`, []any{cutoff, cutoff}},
		{"rate_limit_events", `DELETE FROM rate_limit_events WHERE occurred_at < ?`, []any{cutoff}},
		{"relay_requests", `DELETE FROM relay_requests WHERE completed_at < ?`, []any{cutoff}},
		{"app_connections", `DELETE FROM app_connections WHERE disconnected_at < ?`, []any{cutoff}},
		{"agent_connections", `DELETE FROM agent_connections WHERE disconnected_at < ?`, []any{cutoff}},
		{"audit_events", `DELETE FROM audit_events WHERE occurred_at < ?`, []any{cutoff}},
		{"login_backoff", `DELETE FROM login_backoff WHERE updated_at < ? AND (blocked_until IS NULL OR blocked_until < ?)`, []any{cutoff, now}},
		{"assistant_attachments", `DELETE FROM assistant_attachments WHERE status IN ('expired', 'staged') AND updated_at < ?`, []any{cutoff}},
		{"note_attachments", `DELETE FROM note_attachments WHERE deleted_at < ?`, []any{cutoff}},
		{"mcp_attachment_uploads_expired", `UPDATE mcp_note_attachment_uploads SET status = 'expired', updated_at = ? WHERE status = 'open' AND expires_at < ?`, []any{now, now}},
		{"mcp_attachment_uploads_deleted", `DELETE FROM mcp_note_attachment_uploads WHERE status IN ('completed', 'aborted', 'failed', 'expired') AND updated_at < ?`, []any{cutoff}},
		{"desktop_join_credentials", `DELETE FROM desktop_join_credentials
			WHERE (consumed_at IS NOT NULL AND consumed_at < ?)
				OR (revoked_at IS NOT NULL AND revoked_at < ?)
				OR expires_at < ?`, []any{now.Add(-24 * time.Hour), now.Add(-24 * time.Hour), now.Add(-24 * time.Hour)}},
		{"desktop_session_events", `DELETE FROM desktop_session_events WHERE occurred_at < ?`, []any{now.Add(-180 * 24 * time.Hour)}},
		{"desktop_sessions", `DELETE FROM desktop_sessions AS session
			WHERE state IN ('denied','failed','expired','terminated') AND terminated_at < ?
				AND NOT EXISTS (SELECT 1 FROM desktop_join_credentials AS credential WHERE credential.session_id = session.id)
				AND NOT EXISTS (SELECT 1 FROM desktop_session_events AS event WHERE event.session_id = session.id)`, []any{now.Add(-365 * 24 * time.Hour)}},
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return LifecyclePruneSummary{}, err
	}
	defer tx.Rollback()
	var summary LifecyclePruneSummary
	for _, statement := range statements {
		result, err := tx.ExecContext(ctx, statement.sql, statement.args...)
		if err != nil {
			return LifecyclePruneSummary{}, err
		}
		affected, _ := result.RowsAffected()
		switch statement.name {
		case "app_sessions":
			summary.AppSessionsDeleted = affected
		case "agent_tokens":
			summary.AgentTokensDeleted = affected
		case "home_invitations":
			summary.HomeInvitationsDeleted = affected
		case "app_ws_tickets":
			summary.AppWebSocketTicketsDeleted = affected
		case "file_transfers_expired":
			summary.FileTransfersExpired = affected
		case "file_transfers_deleted":
			summary.FileTransfersDeleted = affected
		case "rate_limit_events":
			summary.RateLimitEventsDeleted = affected
		case "relay_requests":
			summary.RelayRequestsDeleted = affected
		case "app_connections":
			summary.AppConnectionsDeleted = affected
		case "agent_connections":
			summary.AgentConnectionsDeleted = affected
		case "audit_events":
			summary.AuditEventsDeleted = affected
		case "login_backoff":
			summary.LoginBackoffDeleted = affected
		case "assistant_attachments":
			summary.AssistantAttachmentsDeleted = affected
		case "note_attachments":
			summary.NoteAttachmentRowsDeleted = affected
		case "mcp_attachment_uploads_expired":
			summary.MCPAttachmentUploadsExpired = affected
		case "mcp_attachment_uploads_deleted":
			summary.MCPAttachmentUploadsDeleted = affected
		case "desktop_join_credentials":
			summary.DesktopJoinCredentialsDeleted = affected
		case "desktop_session_events":
			summary.DesktopSessionEventsDeleted = affected
		case "desktop_sessions":
			summary.DesktopSessionsDeleted = affected
		case "user_notifications":
			summary.UserNotificationsDeleted = affected
		case "notification_source_events":
			summary.NotificationSourceEventsDeleted = affected
		case "web_push_deliveries":
			summary.WebPushDeliveriesDeleted = affected
		case "web_push_claims_reclaimed":
			summary.WebPushClaimsReclaimed = affected
		}
	}
	if err := tx.Commit(); err != nil {
		return LifecyclePruneSummary{}, mapDesktopStoreError(err)
	}
	return summary, nil
}

func scanAppWebSocketTicket(scanner interface{ Scan(dest ...any) error }) (AppWebSocketTicketRecord, error) {
	var ticket AppWebSocketTicketRecord
	var consumedAt sql.NullTime
	if err := scanner.Scan(&ticket.TokenHash, &ticket.SessionID, &ticket.UserID, &ticket.CreatedAt, &ticket.ExpiresAt, &consumedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AppWebSocketTicketRecord{}, ErrNotFound
		}
		return AppWebSocketTicketRecord{}, err
	}
	if consumedAt.Valid {
		ticket.ConsumedAt = &consumedAt.Time
	}
	return ticket, nil
}

func scanFileTransfer(scanner interface{ Scan(dest ...any) error }) (FileTransferRecord, error) {
	var transfer FileTransferRecord
	var bytesTotal sql.NullInt64
	var completedAt sql.NullTime
	if err := scanner.Scan(
		&transfer.ID,
		&transfer.TokenHash,
		&transfer.JobID,
		&transfer.HomeID,
		&transfer.UserID,
		&transfer.AgentID,
		&transfer.Operation,
		&transfer.SourceID,
		&transfer.Path,
		&transfer.Status,
		&bytesTotal,
		&transfer.BytesDone,
		&transfer.CreatedAt,
		&transfer.ExpiresAt,
		&completedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FileTransferRecord{}, ErrNotFound
		}
		return FileTransferRecord{}, err
	}
	if bytesTotal.Valid {
		transfer.BytesTotal = &bytesTotal.Int64
	}
	if completedAt.Valid {
		transfer.CompletedAt = &completedAt.Time
	}
	return transfer, nil
}

func scanAuditEvent(scanner interface{ Scan(dest ...any) error }) (AuditEvent, error) {
	var event AuditEvent
	var actorUserID sql.NullString
	var actorAgentID sql.NullString
	var homeID sql.NullString
	if err := scanner.Scan(
		&event.ID,
		&event.OccurredAt,
		&actorUserID,
		&actorAgentID,
		&homeID,
		&event.EventType,
		&event.Severity,
		&event.RequestID,
		&event.IPHash,
		&event.TargetType,
		&event.TargetID,
		&event.MetadataJSON,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AuditEvent{}, ErrNotFound
		}
		return AuditEvent{}, err
	}
	if actorUserID.Valid {
		event.ActorUserID = &actorUserID.String
	}
	if actorAgentID.Valid {
		event.ActorAgentID = &actorAgentID.String
	}
	if homeID.Valid {
		event.HomeID = &homeID.String
	}
	return event, nil
}

func scanFileOperationJob(scanner interface{ Scan(dest ...any) error }) (FileOperationJob, error) {
	var job FileOperationJob
	var userID sql.NullString
	var completedAt sql.NullTime
	if err := scanner.Scan(
		&job.ID,
		&job.HomeID,
		&job.AgentID,
		&userID,
		&job.Operation,
		&job.SourceID,
		&job.DestinationSourceID,
		&job.FromPath,
		&job.ToPath,
		&job.IsDirectory,
		&job.Status,
		&job.BytesTotal,
		&job.BytesDone,
		&job.FilesTotal,
		&job.FilesDone,
		&job.ErrorMessage,
		&job.CreatedAt,
		&job.UpdatedAt,
		&completedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FileOperationJob{}, ErrNotFound
		}
		return FileOperationJob{}, err
	}
	if userID.Valid {
		job.UserID = userID.String
	}
	if completedAt.Valid {
		job.CompletedAt = &completedAt.Time
	}
	return job, nil
}

func stableHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// CancelFileTransferJob revokes the bearer lease and terminalizes its job atomically.
func (s *Store) CancelFileTransferJob(ctx context.Context, homeID, jobID string) (bool, error) {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	// Serialize against job completion; revocation is independent of terminal history.
	var operation string
	if err := tx.QueryRowContext(ctx, `SELECT operation FROM file_operation_jobs WHERE id = ? AND home_id = ? FOR UPDATE`, jobID, homeID).Scan(&operation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if operation != "upload" && operation != "download" {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE file_operation_jobs SET status = 'cancelled', error_message = 'cancelled by user', completed_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
 WHERE id = ? AND home_id = ? AND status IN ('queued','running')`, jobID, homeID)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE file_transfers SET status = 'expired', expires_at = LEAST(expires_at, CURRENT_TIMESTAMP), completed_at = CURRENT_TIMESTAMP WHERE home_id = ? AND file_job_id = ?`, homeID, jobID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// AssignUnknownFileJobOwner records an administrator's reviewed attribution.
// It never changes status, paths, progress, or an already known owner.
func (s *Store) AssignUnknownFileJobOwner(ctx context.Context, homeID, jobID, agentID, status string, updatedAt time.Time) (bool, error) {
	result, err := s.exec(ctx, `UPDATE file_operation_jobs SET agent_id = ?, updated_at = CURRENT_TIMESTAMP
 WHERE id = ? AND home_id = ? AND agent_id IS NULL AND operation = 'move'
 AND status = ? AND status IN ('failed','cancelled','rollback_required') AND updated_at = ?
 AND EXISTS (SELECT 1 FROM agents WHERE id = ? AND home_id = ?)`, agentID, jobID, homeID, status, updatedAt, agentID, homeID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) ListUnknownFileJobOwners(ctx context.Context, homeID, after string) ([]FileOperationJob, string, error) {
	rows, err := s.query(ctx, `SELECT id, home_id, COALESCE(agent_id, ''), user_id, operation, source_id, destination_source_id,
 from_path, to_path, is_directory, status, bytes_total, bytes_done, files_total, files_done,
 error_message, created_at, updated_at, completed_at FROM file_operation_jobs
 WHERE home_id = ? AND agent_id IS NULL AND operation = 'move'
 AND status IN ('failed','cancelled','rollback_required') AND id > ? ORDER BY id LIMIT 51`, homeID, after)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var jobs []FileOperationJob
	for rows.Next() {
		job, err := scanFileOperationJob(rows)
		if err != nil {
			return nil, "", err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(jobs) > 50 {
		jobs = jobs[:50]
		next = jobs[len(jobs)-1].ID
	}
	return jobs, next, nil
}

// DeleteReviewedFileOperationJob protects active jobs and concurrent recovery.
func (s *Store) DeleteReviewedFileOperationJob(ctx context.Context, homeID, jobID string, updatedAt time.Time) (bool, error) {
	result, err := s.exec(ctx, `DELETE FROM file_operation_jobs WHERE home_id = ? AND id = ?
 AND operation = 'move' AND status = 'rollback_required' AND updated_at = ?`, homeID, jobID, updatedAt)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}
