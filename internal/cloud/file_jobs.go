package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleHomeFileJobs(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) == 0 || parts[0] != "file-jobs" {
		return false
	}
	if err := s.requireHomeFeature(r.Context(), home, membership, auth.User.ID, domain.HomePermissionFeatureFiles); err != nil {
		if errors.Is(err, errFeaturePermissionDenied) {
			http.Error(w, err.Error(), http.StatusForbidden)
			return true
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	if len(parts) == 1 {
		if r.Method == http.MethodDelete {
			cleared, err := s.store.DeleteTerminalFileOperationJobs(r.Context(), home.ID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return true
			}
			s.audit(r.Context(), "file_operation.history_cleared", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "file_operation_job", "", map[string]any{"cleared": cleared})
			s.broadcastFileHistoryChanged(r.Context(), home.ID)
			writeJSON(w, http.StatusOK, map[string]any{"cleared": cleared})
			return true
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		if r.URL.Query().Get("owner") == "unknown" {
			if membership.Role != domain.HomeRoleAdmin {
				http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
				return true
			}
			jobs, next, err := s.store.ListUnknownFileJobOwners(r.Context(), home.ID, r.URL.Query().Get("after"))
			if err != nil {
				http.Error(w, "owner reviews could not be loaded", http.StatusInternalServerError)
				return true
			}
			writeJSON(w, http.StatusOK, map[string]any{"jobs": nonNilSlice(fileOperationJobSnapshots(jobs)), "next_cursor": next})
			return true
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		jobs, err := s.store.ListFileOperationJobs(r.Context(), home.ID, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": nonNilSlice(fileOperationJobSnapshots(jobs))})
		return true
	}

	jobID := strings.TrimSpace(parts[1])
	if jobID == "" {
		http.NotFound(w, r)
		return true
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		job, err := s.store.GetFileOperationJob(r.Context(), jobID)
		if errors.Is(err, store.ErrNotFound) || err == nil && job.HomeID != home.ID {
			http.NotFound(w, r)
			return true
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, fileOperationJobSnapshot(job))
		return true
	}
	if len(parts) == 3 && parts[2] == "dismiss" && r.Method == http.MethodPost {
		s.handleFileJobDismiss(w, r, home, auth, membership, jobID)
		return true
	}
	if len(parts) == 3 && parts[2] == "owner" && r.Method == http.MethodPost {
		s.handleFileJobOwner(w, r, home, auth, membership, jobID)
		return true
	}
	if len(parts) == 3 && parts[2] == "cancel" && r.Method == http.MethodPost {
		job, err := s.cancelFileOperationJob(r.Context(), home, auth, jobID)
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return true
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return true
		}
		writeJSON(w, http.StatusOK, fileOperationJobSnapshot(job))
		return true
	}

	if len(parts) == 3 && parts[2] == "retry" && r.Method == http.MethodPost {
		job, err := s.retryFileOperationJob(r.Context(), home, auth, jobID)
		if errors.Is(err, store.ErrNotFound) || err == nil && job.HomeID != home.ID {
			http.NotFound(w, r)
			return true
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		writeJSON(w, http.StatusOK, fileOperationJobSnapshot(job))
		return true
	}
	if len(parts) == 3 && parts[2] == "rollback" && r.Method == http.MethodPost {
		job, err := s.rollbackFileOperationJob(r.Context(), home, auth, jobID)
		if errors.Is(err, store.ErrNotFound) || err == nil && job.HomeID != home.ID {
			http.NotFound(w, r)
			return true
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return true
		}
		writeJSON(w, http.StatusOK, fileOperationJobSnapshot(job))
		return true
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return true
}

func (s *Server) prepareManagedFileCommand(ctx context.Context, home domain.Home, auth authContext, agentID string, command protocol.RoutedCommand) (protocol.RoutedCommand, string, error) {
	if command.Command != "files.move" {
		return command, "", nil
	}
	request, err := decodeBody[protocol.FilesMoveRequest](command.Body)
	if err != nil {
		return command, "", err
	}
	if strings.TrimSpace(request.DestinationSourceID) == "" {
		request.DestinationSourceID = request.SourceID
	}
	jobID := newID("filejob")
	request.JobID = jobID
	body, err := protocol.EncodeBody(request)
	if err != nil {
		return command, "", err
	}
	now := time.Now().UTC()
	job := store.FileOperationJob{
		ID:                  jobID,
		AgentID:             agentID,
		HomeID:              home.ID,
		UserID:              auth.User.ID,
		Operation:           protocol.FileOperationMove,
		SourceID:            strings.TrimSpace(request.SourceID),
		DestinationSourceID: strings.TrimSpace(request.DestinationSourceID),
		FromPath:            strings.TrimSpace(request.From),
		ToPath:              strings.TrimSpace(request.To),
		IsDirectory:         request.IsDirectory,
		Status:              "queued",
		FilesTotal:          1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := s.store.CreateFileOperationJob(ctx, job); err != nil {
		return command, "", err
	}
	s.audit(ctx, "file_operation.requested", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(ctx), "file_operation_job", jobID, map[string]any{
		"operation":             protocol.FileOperationMove,
		"source_id":             request.SourceID,
		"destination_source_id": request.DestinationSourceID,
		"from_path_hash":        stableAuditTarget(request.From),
		"to_path_hash":          stableAuditTarget(request.To),
		"is_directory":          request.IsDirectory,
	})
	command.Body = body
	return command, jobID, nil
}

func (s *Server) markFileJobRunning(ctx context.Context, jobID string) {
	if jobID == "" {
		return
	}
	if _, err := s.store.UpdateFileOperationJobMonotonic(ctx, jobID, "running", 0, 0, "", nil); err != nil {
		s.logger.Warn("failed to mark file job running", "job_id", jobID, "error", err)
	}
}

func decodeMoveJobResponse(body json.RawMessage, jobID, defaultStatus string) (protocol.FileOperationJobResponse, error) {
	response := protocol.FileOperationJobResponse{OK: true, JobID: jobID, Status: defaultStatus, FilesDone: 1}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &response); err != nil || strings.TrimSpace(string(body)) == "null" {
			return response, errors.New("invalid file job response")
		}
	}
	if response.JobID != "" && response.JobID != jobID {
		return response, errors.New("file job response does not match dispatched job")
	}
	response.JobID = jobID
	if response.Status == "" {
		response.Status = defaultStatus
	}
	if !response.OK || !validMoveJobStatus(response.Status) || response.BytesDone < 0 || response.FilesDone < 0 || response.BytesTotal < 0 || response.FilesTotal < 0 {
		return response, errors.New("invalid file job response state")
	}
	return response, nil
}

func validMoveJobStatus(status string) bool {
	switch status {
	case "running", "completed", "failed", "cancelled", "rollback_required", "rolled_back":
		return true
	}
	return false
}

func fileJobCompletedAt(status string) *time.Time {
	if status == "running" {
		return nil
	}
	now := time.Now().UTC()
	return &now
}

func (s *Server) completePendingFileJob(ctx context.Context, pending *pendingRequest, envelope protocol.Envelope) error {
	if pending == nil || pending.fileJobID == "" {
		return nil
	}
	job, err := s.store.GetFileOperationJob(ctx, pending.fileJobID)
	if err != nil {
		return err
	}
	if job.HomeID != pending.homeID || job.AgentID == "" || job.AgentID != envelope.AgentID || job.Operation != protocol.FileOperationMove {
		return errors.New("file job owner does not match dispatched agent")
	}
	response, err := decodeMoveJobResponse(envelope.Payload, job.ID, "completed")
	message := ""
	if envelope.Error != nil {
		response = protocol.FileOperationJobResponse{Status: "failed"}
		message = envelope.Error.Message
		err = nil
	}
	if err != nil {
		return err
	}
	changed, err := s.store.UpdateOwnedMoveJob(ctx, job, response.Status, response.BytesDone, response.FilesDone, response.BytesTotal, response.FilesTotal, message, fileJobCompletedAt(response.Status))
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	s.broadcastFileJobChanged(ctx, job.HomeID, job.ID)
	if response.Status == "completed" {
		s.audit(ctx, "file_operation.completed", auditSeverityInfo, job.UserID, "", job.HomeID, pending.requestID, "file_operation_job", job.ID, map[string]any{"status": response.Status})
	}
	return nil
}

func (s *Server) handleFileMoveJobEvent(ctx context.Context, homeID, agentID string, event string, body json.RawMessage) {
	payload := protocol.FileOperationJobEvent{}
	if err := json.Unmarshal(body, &payload); err != nil {
		s.logger.Warn("bad file move job event payload", "home_id", homeID, "event", event, "error", err)
		return
	}
	if strings.TrimSpace(payload.JobID) == "" {
		s.logger.Warn("file move job event missing job id", "home_id", homeID, "event", event)
		return
	}
	job, err := s.store.GetFileOperationJob(ctx, payload.JobID)
	if err != nil {
		s.logger.Warn("file move job event for unknown job", "home_id", homeID, "job_id", payload.JobID, "event", event, "error", err)
		return
	}
	if job.HomeID != homeID || agentID == "" || job.AgentID != agentID || job.Operation != protocol.FileOperationMove {
		s.logger.Warn("file move job event home mismatch", "home_id", homeID, "job_id", payload.JobID, "job_home_id", job.HomeID)
		return
	}
	status := strings.TrimSpace(payload.Status)
	if status == "" && event == "files.move_progress" {
		status = "running"
	}
	valid := event == "files.move_progress" && status == "running" || event == "files.move_completed" && status == "completed" || event == "files.move_failed" && (status == "failed" || status == "cancelled" || status == "rollback_required")
	if !valid || payload.BytesDone < 0 || payload.FilesDone < 0 || payload.BytesTotal < 0 || payload.FilesTotal < 0 {
		return
	}
	changed, err := s.store.UpdateOwnedMoveJob(ctx, job, status, payload.BytesDone, payload.FilesDone, payload.BytesTotal, payload.FilesTotal, payload.ErrorMessage, fileJobCompletedAt(status))
	if err != nil {
		s.logger.Warn("failed to update file move job from event", "job_id", payload.JobID, "event", event, "error", err)
		return
	}
	if !changed {
		return
	}

	s.broadcastFileJobChanged(ctx, homeID, payload.JobID)
	if status == "completed" {
		for _, ref := range []fileSearchDirectoryRef{
			{SourceID: job.SourceID, Path: fileSearchParent(job.FromPath)},
			{SourceID: job.DestinationSourceID, Path: fileSearchParent(job.ToPath)},
		} {
			if err := s.store.MarkFileSearchDirectoryDue(ctx, homeID, job.AgentID, ref.SourceID, ref.Path); err != nil {
				s.logger.Warn("file search move rescan scheduling failed", "job_id", payload.JobID, "error", err)
			}
		}
		s.audit(ctx, "file_operation.completed", auditSeverityInfo, job.UserID, "", homeID, "", "file_operation_job", payload.JobID, map[string]any{"status": status})
		s.emitFileDirectoryChanged(ctx, "/", map[string]any{"home_id": homeID, "path": "/"})
	}
	if status == "failed" || status == "rollback_required" {
		s.audit(ctx, "file_operation.failed", auditSeverityWarning, job.UserID, "", homeID, "", "file_operation_job", payload.JobID, map[string]any{"status": status})
	}
}

func (s *Server) broadcastFileJobChanged(ctx context.Context, homeID string, jobID string) {
	job, err := s.store.GetFileOperationJob(ctx, jobID)
	if err != nil {
		return
	}
	body, err := json.Marshal(map[string]any{
		"home_id": homeID,
		"job":     fileOperationJobSnapshot(job),
	})
	if err != nil {
		return
	}
	s.broadcastRawAppEventOnKey(ctx, scopedHomeTopic(homeID, "files.jobs"), "files.jobs", "files.job_changed", body)
}

func (s *Server) failFileJob(ctx context.Context, jobID string, status string, message string) {
	if jobID == "" {
		return
	}
	if status == "" {
		status = "failed"
	}
	now := time.Now().UTC()
	if _, err := s.store.UpdateFileOperationJobMonotonic(ctx, jobID, status, 0, 0, message, &now); err != nil {
		s.logger.Warn("failed to update file job failure", "job_id", jobID, "status", status, "error", err)
	}
}

func (s *Server) retryFileOperationJob(ctx context.Context, home domain.Home, auth authContext, jobID string) (store.FileOperationJob, error) {
	job, err := s.store.GetFileOperationJob(ctx, jobID)
	if err != nil {
		return store.FileOperationJob{}, err
	}
	if job.HomeID != home.ID {
		return job, nil
	}
	if job.Operation != protocol.FileOperationMove {
		return store.FileOperationJob{}, errors.New("only move jobs can be retried")
	}
	if job.Status != "failed" && job.Status != "cancelled" {
		return job, nil
	}
	request := protocol.FilesMoveRequest{
		SourceID:            job.SourceID,
		DestinationSourceID: job.DestinationSourceID,
		JobID:               job.ID,
		From:                job.FromPath,
		To:                  job.ToPath,
		IsDirectory:         job.IsDirectory,
	}
	if err := s.authorizeFileJobAction(ctx, job, "files.move", request); err != nil {
		return job, err
	}
	if err := s.store.UpdateFileOperationJob(ctx, job.ID, "running", job.BytesDone, job.FilesDone, "", nil); err != nil {
		return store.FileOperationJob{}, err
	}
	response, err := s.sendAgentCommandTo(ctx, home.ID, job.AgentID, "files.move", request)
	if err != nil {
		now := time.Now().UTC()
		_ = s.store.UpdateFileOperationJob(ctx, job.ID, "failed", job.BytesDone, job.FilesDone, err.Error(), &now)
		return s.store.GetFileOperationJob(ctx, job.ID)
	}
	if response.Error != nil {
		now := time.Now().UTC()
		_ = s.store.UpdateFileOperationJob(ctx, job.ID, "failed", job.BytesDone, job.FilesDone, response.Error.Message, &now)
		return s.store.GetFileOperationJob(ctx, job.ID)
	}
	payload, err := decodeMoveJobResponse(response.Payload, job.ID, "completed")
	if err != nil {
		return job, err
	}
	var completedAt *time.Time
	if payload.Status == "completed" || payload.Status == "failed" || payload.Status == "cancelled" || payload.Status == "rollback_required" || payload.Status == "rolled_back" {
		now := time.Now().UTC()
		completedAt = &now
	}
	if _, err := s.store.UpdateFileOperationJobMonotonic(ctx, job.ID, payload.Status, payload.BytesDone, payload.FilesDone, "", completedAt); err != nil {
		return store.FileOperationJob{}, err
	}
	s.audit(ctx, "file_operation.retried", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(ctx), "file_operation_job", job.ID, map[string]any{"status": payload.Status})
	return s.store.GetFileOperationJob(ctx, job.ID)
}

func (s *Server) rollbackFileOperationJob(ctx context.Context, home domain.Home, auth authContext, jobID string) (store.FileOperationJob, error) {
	// Keep reviewed history removal from racing a dispatched rollback.
	s.fileJobRecoveryMu.Lock()
	defer s.fileJobRecoveryMu.Unlock()
	job, err := s.store.GetFileOperationJob(ctx, jobID)
	if err != nil {
		return store.FileOperationJob{}, err
	}
	if job.HomeID != home.ID {
		return job, nil
	}
	if job.Operation != protocol.FileOperationMove {
		return store.FileOperationJob{}, errors.New("only move jobs can be rolled back")
	}
	if job.Status != "rollback_required" {
		return job, nil
	}
	request := protocol.FilesMoveRollbackRequest{
		JobID:               job.ID,
		DestinationSourceID: job.DestinationSourceID,
		To:                  job.ToPath,
		IsDirectory:         job.IsDirectory,
	}
	if err := s.authorizeFileJobAction(ctx, job, "files.move_rollback", request); err != nil {
		return job, err
	}
	response, err := s.sendAgentCommandTo(ctx, home.ID, job.AgentID, "files.move_rollback", request)
	if err != nil {
		return job, err
	}
	if response.Error != nil {
		return job, errors.New(response.Error.Message)
	}
	payload, err := decodeMoveJobResponse(response.Payload, job.ID, "rolled_back")
	if err != nil {
		return job, err
	}
	if payload.Status != "rolled_back" {
		return job, errors.New("agent did not confirm rollback")
	}
	now := time.Now().UTC()
	if err := s.store.UpdateFileOperationJob(ctx, job.ID, payload.Status, job.BytesDone, job.FilesDone, "", &now); err != nil {
		return store.FileOperationJob{}, err
	}
	s.audit(ctx, "file_operation.rolled_back", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(ctx), "file_operation_job", job.ID, map[string]any{"status": payload.Status})
	s.broadcastFileJobChanged(ctx, home.ID, job.ID)
	return s.store.GetFileOperationJob(ctx, job.ID)
}

func fileOperationJobSnapshots(jobs []store.FileOperationJob) []map[string]any {
	out := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, fileOperationJobSnapshot(job))
	}
	return out
}

func fileOperationJobSnapshot(job store.FileOperationJob) map[string]any {
	return map[string]any{
		"id":                    job.ID,
		"home_id":               job.HomeID,
		"agent_id":              job.AgentID,
		"user_id":               job.UserID,
		"operation":             job.Operation,
		"source_id":             job.SourceID,
		"destination_source_id": job.DestinationSourceID,
		"from_path":             job.FromPath,
		"to_path":               job.ToPath,
		"is_directory":          job.IsDirectory,
		"status":                job.Status,
		"bytes_total":           job.BytesTotal,
		"bytes_done":            job.BytesDone,
		"files_total":           job.FilesTotal,
		"files_done":            job.FilesDone,
		"error_message":         job.ErrorMessage,
		"created_at":            job.CreatedAt,
		"updated_at":            job.UpdatedAt,
		"completed_at":          job.CompletedAt,
	}
}

func (s *Server) authorizeFileJobAction(ctx context.Context, job store.FileOperationJob, command string, request any) error {
	if job.Operation != protocol.FileOperationMove {
		return errors.New("this action is only available for move jobs")
	}
	if job.AgentID == "" {
		return errors.New("file job has no verified owning agent; administrator review is required")
	}
	if _, ok := s.router.ResolveAgent(job.HomeID, job.AgentID); !ok {
		return errors.New("the file job's owning agent is offline")
	}
	body, err := protocol.EncodeBody(request)
	if err != nil {
		return err
	}
	return s.authorizeFileCommandPolicy(ctx, job.HomeID, protocol.RoutedCommand{Command: command, Body: body})
}

func (s *Server) cancelFileOperationJob(ctx context.Context, home domain.Home, auth authContext, jobID string) (store.FileOperationJob, error) {
	job, err := s.store.GetFileOperationJob(ctx, jobID)
	if err != nil {
		return job, err
	}
	if job.HomeID != home.ID {
		return job, store.ErrNotFound
	}
	isTransfer := job.Operation == protocol.FileTransferOperationUpload || job.Operation == protocol.FileTransferOperationDownload
	if !isTransfer && job.Status != "queued" && job.Status != "running" {
		return job, nil
	}
	if job.Operation == protocol.FileTransferOperationUpload || job.Operation == protocol.FileTransferOperationDownload {
		s.transferControlMu.Lock()
		cancelled, cancelErr := s.store.CancelFileTransferJob(ctx, home.ID, job.ID)
		var attempts []*transferAttempt
		if cancelErr == nil && cancelled {
			attempts = s.transfers.CancelJob(home.ID, job.ID)
		}
		s.transferControlMu.Unlock()
		if cancelErr != nil {
			return job, cancelErr
		}
		for _, attempt := range attempts {
			envelope, _ := protocol.NewEnvelope(protocol.TypeFileTransferCancel, attempt.ID, attempt.target.agentID, attempt.target.homeID, protocol.FileTransferCancel{Reason: "cancelled_by_user"})
			sendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_ = attempt.target.peer.Write(sendCtx, envelope)
			cancel()
		}
	} else {
		request := protocol.FilesMoveCancelRequest{JobID: job.ID}
		if err := s.authorizeFileJobAction(ctx, job, "files.move_cancel", request); err != nil {
			return job, err
		}
		response, err := s.sendAgentCommandTo(ctx, home.ID, job.AgentID, "files.move_cancel", request)
		if err != nil {
			return job, err
		}
		if response.Error != nil {
			return job, errors.New(response.Error.Message)
		}
		var acknowledged protocol.EmptyResponse
		if err := json.Unmarshal(response.Payload, &acknowledged); err != nil || !acknowledged.OK {
			return job, errors.New("the owning agent did not confirm cancellation")
		}
		now := time.Now().UTC()
		if _, err := s.store.UpdateOwnedMoveJob(ctx, job, "cancelled", job.BytesDone, job.FilesDone, 0, 0, "cancelled by user", &now); err != nil {
			return job, err
		}
	}
	s.audit(ctx, "file_operation.cancelled", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(ctx), "file_operation_job", job.ID, map[string]any{"operation": job.Operation})
	s.broadcastFileJobChanged(ctx, home.ID, job.ID)
	return s.store.GetFileOperationJob(ctx, job.ID)
}

// WebSocket recovery commands share the durable owner and policy checks with
// HTTP recovery. Request-supplied machine IDs and paths cannot redirect a job.
func (s *Server) managedFileRecovery(ctx context.Context, home domain.Home, auth authContext, command protocol.RoutedCommand) (any, bool, error) {
	if command.Command != "files.move_cancel" && command.Command != "files.move_rollback" {
		return nil, false, nil
	}
	var request protocol.FilesMoveCancelRequest
	if err := json.Unmarshal(command.Body, &request); err != nil || strings.TrimSpace(request.JobID) == "" {
		return nil, true, errors.New("a managed file job ID is required")
	}
	var job store.FileOperationJob
	var err error
	if command.Command == "files.move_cancel" {
		job, err = s.cancelFileOperationJob(ctx, home, auth, request.JobID)
	} else {
		job, err = s.rollbackFileOperationJob(ctx, home, auth, request.JobID)
		if err == nil && job.HomeID != home.ID {
			err = store.ErrNotFound
		}
	}
	if err != nil {
		return nil, true, err
	}
	if command.Command == "files.move_cancel" {
		return protocol.EmptyResponse{OK: true}, true, nil
	}
	return protocol.FileOperationJobResponse{OK: true, JobID: job.ID, Status: job.Status, BytesDone: job.BytesDone, FilesDone: job.FilesDone}, true, nil
}

func (s *Server) broadcastFileHistoryChanged(ctx context.Context, homeID string) {
	body, _ := json.Marshal(map[string]string{"home_id": homeID})
	s.broadcastRawAppEventOnKey(ctx, scopedHomeTopic(homeID, "files.jobs"), "files.jobs", "files.history_changed", body)
}
