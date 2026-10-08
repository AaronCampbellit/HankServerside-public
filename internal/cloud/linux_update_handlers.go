package cloud

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleHomeLinuxUpdates(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) == 0 || parts[0] != "linux-updates" {
		return false
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		rollout, err := s.store.GetActiveLinuxAgentRollout(r.Context())
		if errors.Is(err, store.ErrNotFound) {
			pins, pinErr := s.store.ListLinuxAgentVersionPins(r.Context(), home.ID)
			if pinErr != nil {
				http.Error(w, "Linux update state unavailable", http.StatusInternalServerError)
				return true
			}
			writeJSON(w, http.StatusOK, map[string]any{"rollout": nil, "assignments": []any{}, "pins": nonNilSlice(pins)})
			return true
		}
		if err != nil {
			http.Error(w, "Linux update state unavailable", http.StatusInternalServerError)
			return true
		}
		assignments, err := s.store.ListLinuxAgentRolloutAssignments(r.Context(), rollout.ID, home.ID)
		if err != nil {
			http.Error(w, "Linux update state unavailable", http.StatusInternalServerError)
			return true
		}
		pins, err := s.store.ListLinuxAgentVersionPins(r.Context(), home.ID)
		if err != nil {
			http.Error(w, "Linux update state unavailable", http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"rollout": rollout, "assignments": nonNilSlice(assignments), "pins": nonNilSlice(pins)})
		return true
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}
	now := time.Now().UTC()
	if len(parts) == 2 && r.Method == http.MethodPost && (parts[1] == "pause" || parts[1] == "resume") {
		rollout, err := s.store.GetActiveLinuxAgentRollout(r.Context())
		if err != nil {
			http.Error(w, err.Error(), linuxUpdateHTTPStatus(err))
			return true
		}
		state := "paused"
		if parts[1] == "resume" {
			state = "active"
		}
		if err := s.store.SetLinuxAgentRolloutState(r.Context(), rollout.ID, state, now); err != nil {
			http.Error(w, err.Error(), linuxUpdateHTTPStatus(err))
			return true
		}
		s.audit(r.Context(), "linux_agent.rollout."+parts[1], auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "linux_rollout", rollout.ID, map[string]any{"version": rollout.Version})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "state": state})
		if state == "active" {
			go s.reconcileLinuxUpdates(context.WithoutCancel(r.Context()), "rollout resumed")
		}
		return true
	}
	if len(parts) == 3 && parts[1] == "assignments" && r.Method == http.MethodPost {
		changed, err := s.store.RetryLinuxAgentAssignment(r.Context(), home.ID, parts[2], now)
		if err != nil {
			http.Error(w, "Linux update retry failed", http.StatusInternalServerError)
			return true
		}
		if !changed {
			http.Error(w, "assignment is not retryable", http.StatusConflict)
			return true
		}
		s.audit(r.Context(), "linux_agent.assignment.retry", auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "linux_assignment", parts[2], nil)
		go s.reconcileLinuxUpdates(context.WithoutCancel(r.Context()), "assignment retry")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		if r.Method == http.MethodDelete {
			go s.reconcileLinuxUpdates(context.WithoutCancel(r.Context()), "version unpinned")
		}
		return true
	}
	if len(parts) == 4 && parts[1] == "agents" && parts[3] == "pin" && (r.Method == http.MethodPut || r.Method == http.MethodDelete) {
		agentID := strings.TrimSpace(parts[2])
		agent, err := s.store.GetAgentByID(r.Context(), agentID)
		if err != nil || agent.HomeID != home.ID || agent.Platform != "linux" || agent.AgentType != AgentTypeWorker {
			http.NotFound(w, r)
			return true
		}
		if r.Method == http.MethodDelete {
			err = s.store.DeleteLinuxAgentVersionPin(r.Context(), home.ID, agentID)
		} else {
			var body struct {
				Version string `json:"version"`
			}
			if err = parseJSON(w, r, &body); err == nil {
				body.Version = strings.TrimSpace(body.Version)
				if body.Version == "" {
					err = errors.New("version is required")
				} else {
					err = s.store.SetLinuxAgentVersionPin(r.Context(), domain.LinuxAgentVersionPin{HomeID: home.ID, AgentID: agentID, Version: body.Version, CreatedAt: now, UpdatedAt: now})
				}
			}
		}
		if err != nil {
			http.Error(w, err.Error(), linuxUpdateHTTPStatus(err))
			return true
		}
		s.audit(r.Context(), "linux_agent.version_pin.updated", auditSeverityCritical, auth.User.ID, agentID, home.ID, requestIDFromContext(r.Context()), "agent", agentID, map[string]any{"removed": r.Method == http.MethodDelete})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return true
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return true
}

func (s *Server) reconcileLinuxUpdates(ctx context.Context, reason string) {
	if err := s.linuxUpdates.Reconcile(ctx); err != nil {
		s.logger.Warn("Linux update reconciliation failed", "reason", reason, "error", err)
	}
}

func linuxUpdateHTTPStatus(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, store.ErrConflict) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}
