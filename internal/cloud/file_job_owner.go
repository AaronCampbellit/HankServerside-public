package cloud

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

type fileJobOwnerRequest struct {
	AgentID            string    `json:"agent_id"`
	ExpectedUpdatedAt  time.Time `json:"expected_updated_at"`
	RequestActionToken bool      `json:"request_action_token"`
	AdminActionToken   string    `json:"admin_action_token"`
	Confirmation       string    `json:"confirmation"`
}

func (s *Server) handleFileJobOwner(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, jobID string) {
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return
	}
	var body fileJobOwnerRequest
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.AgentID = strings.TrimSpace(body.AgentID)
	job, err := s.store.GetFileOperationJob(r.Context(), jobID)
	if errors.Is(err, store.ErrNotFound) || err == nil && job.HomeID != home.ID {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not load file job", http.StatusInternalServerError)
		return
	}
	if job.AgentID != "" || job.Operation != "move" || !fileJobOwnerAssignable(job.Status) || !job.UpdatedAt.Equal(body.ExpectedUpdatedAt) {
		http.Error(w, "This job changed or does not need an owner. Refresh its history before reviewing it again.", http.StatusConflict)
		return
	}
	if body.AgentID == "" {
		http.Error(w, "select the machine that performed this move", http.StatusBadRequest)
		return
	}
	agent, err := s.store.GetAgentByID(r.Context(), body.AgentID)
	if errors.Is(err, store.ErrNotFound) || err == nil && agent.HomeID != home.ID {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "could not load agent", http.StatusInternalServerError)
		return
	}
	scope, _ := json.Marshal([]string{home.ID, job.ID, body.AgentID, job.Status, job.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	action := "files.assign-owner:" + hashToken(string(scope))
	if body.RequestActionToken {
		token, expiresAt := s.adminActionTokens.Issue(auth.User.ID, action, 5*time.Minute)
		writeJSON(w, http.StatusCreated, map[string]any{"admin_action_token": token, "expires_at": expiresAt})
		return
	}
	if body.Confirmation != "CONFIRM OWNER" {
		http.Error(w, "owner confirmation is required", http.StatusBadRequest)
		return
	}
	if err := s.adminActionTokens.Consume(body.AdminActionToken, auth.User.ID, action); err != nil {
		http.Error(w, "a fresh owner review is required", http.StatusForbidden)
		return
	}
	changed, err := s.store.AssignUnknownFileJobOwner(r.Context(), home.ID, job.ID, agent.ID, job.Status, job.UpdatedAt)
	if err != nil {
		http.Error(w, "owner could not be saved; refresh the job and agent list", http.StatusConflict)
		return
	}
	if !changed {
		http.Error(w, "job changed during review; refresh and review it again", http.StatusConflict)
		return
	}
	s.audit(r.Context(), "file_operation.owner_assigned", auditSeverityWarning, auth.User.ID, agent.ID, home.ID, requestIDFromContext(r.Context()), "file_operation_job", job.ID, map[string]any{"agent_id": agent.ID, "status": job.Status, "previous_owner": "unknown"})
	s.broadcastFileJobChanged(r.Context(), home.ID, job.ID)
	updated, err := s.store.GetFileOperationJob(r.Context(), job.ID)
	if err != nil {
		http.Error(w, "owner saved; refresh history to see it", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, fileOperationJobSnapshot(updated))
}

func fileJobOwnerAssignable(status string) bool {
	return status == "failed" || status == "cancelled" || status == "rollback_required"
}
