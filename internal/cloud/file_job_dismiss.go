package cloud

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

type fileJobDismissRequest struct {
	ExpectedUpdatedAt  time.Time `json:"expected_updated_at"`
	RequestActionToken bool      `json:"request_action_token"`
	AdminActionToken   string    `json:"admin_action_token"`
	Confirmation       string    `json:"confirmation"`
}

// Dismissal only removes recovery history; it must never dispatch a file command.
func (s *Server) handleFileJobDismiss(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, jobID string) {
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return
	}
	var body fileJobDismissRequest
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.fileJobRecoveryMu.Lock()
	defer s.fileJobRecoveryMu.Unlock()
	job, err := s.store.GetFileOperationJob(r.Context(), jobID)
	if errors.Is(err, store.ErrNotFound) || err == nil && job.HomeID != home.ID {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not load file job", http.StatusInternalServerError)
		return
	}
	if job.Operation != "move" || job.Status != "rollback_required" || !job.UpdatedAt.Equal(body.ExpectedUpdatedAt) {
		http.Error(w, "This job changed or does not need recovery review. Refresh its history.", http.StatusConflict)
		return
	}
	scope, _ := json.Marshal([]string{home.ID, job.ID, job.AgentID, job.Status, job.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	action := "files.dismiss-history:" + hashToken(string(scope))
	if body.RequestActionToken {
		token, expiresAt := s.adminActionTokens.Issue(auth.User.ID, action, 5*time.Minute)
		writeJSON(w, http.StatusCreated, map[string]any{"admin_action_token": token, "expires_at": expiresAt})
		return
	}
	if body.Confirmation != "REMOVE HISTORY" {
		http.Error(w, "history removal confirmation is required", http.StatusBadRequest)
		return
	}
	if err := s.adminActionTokens.Consume(body.AdminActionToken, auth.User.ID, action); err != nil {
		http.Error(w, "a fresh history review is required", http.StatusForbidden)
		return
	}
	changed, err := s.store.DeleteReviewedFileOperationJob(r.Context(), home.ID, job.ID, job.UpdatedAt)
	if err != nil {
		http.Error(w, "history could not be removed", http.StatusInternalServerError)
		return
	}
	if !changed {
		http.Error(w, "job changed during review; refresh and review again", http.StatusConflict)
		return
	}
	s.audit(r.Context(), "file_operation.history_removed", auditSeverityWarning, auth.User.ID, job.AgentID, home.ID, requestIDFromContext(r.Context()), "file_operation_job", job.ID, map[string]any{"operation": job.Operation, "previous_status": job.Status, "files_changed": false})
	s.broadcastFileHistoryChanged(r.Context(), home.ID)
	writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}
