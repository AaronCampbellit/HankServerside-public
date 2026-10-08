package cloud

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleAgentCredentials(w http.ResponseWriter, r *http.Request) {
	record, ok := s.authenticateAgentCredentialRequest(w, r)
	if !ok {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/agent/credentials/")
	now := time.Now().UTC()
	switch {
	case path == "status" && r.Method == http.MethodGet:
		state, err := s.store.GetAgentCredentialState(r.Context(), record.Home.ID, record.Agent.ID, record.Token.ID, now)
		if err != nil {
			http.Error(w, "credential unavailable", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, state)
	case path == "rotate" && r.Method == http.MethodPost:
		current, err := s.store.GetAgentCredentialState(r.Context(), record.Home.ID, record.Agent.ID, record.Token.ID, now)
		if err != nil {
			http.Error(w, "credential unavailable", http.StatusUnauthorized)
			return
		}
		if current.ReplacesCredentialID != "" && current.ConfirmedAt == nil {
			http.Error(w, "credential rotation pending confirmation", http.StatusConflict)
			return
		}
		var body struct {
			CredentialHash string `json:"credential_hash"`
		}
		if err := parseJSON(w, r, &body); err != nil || !lowerHexSHA256Pattern.MatchString(body.CredentialHash) {
			http.Error(w, "invalid credential rotation", http.StatusBadRequest)
			return
		}
		state, err := s.store.BeginAgentCredentialRotation(r.Context(), store.BeginAgentCredentialRotationInput{
			HomeID: record.Home.ID, AgentID: record.Agent.ID, CurrentTokenID: record.Token.ID,
			ReplacementTokenID: newID("agtok"), ReplacementHash: body.CredentialHash, Now: now,
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				http.Error(w, "credential rotation already pending", http.StatusConflict)
				return
			}
			http.Error(w, "credential rotation unavailable", http.StatusNotFound)
			return
		}
		s.audit(r.Context(), "agent.credential.rotation_started", auditSeverityCritical, "", record.Agent.ID, record.Home.ID, requestIDFromContext(r.Context()), "agent_token", state.CredentialID, map[string]any{"generation": state.Generation, "confirm_by": state.ConfirmBy})
		writeJSON(w, http.StatusCreated, state)
	case path == "confirm" && r.Method == http.MethodPost:
		var body struct {
			CredentialID string `json:"credential_id"`
		}
		if err := parseJSON(w, r, &body); err != nil || strings.TrimSpace(body.CredentialID) == "" {
			http.Error(w, "invalid credential confirmation", http.StatusBadRequest)
			return
		}
		state, err := s.store.ConfirmAgentCredentialRotation(r.Context(), record.Home.ID, record.Agent.ID, strings.TrimSpace(body.CredentialID), record.Token.ID, now)
		if err != nil {
			http.Error(w, "credential confirmation unavailable", http.StatusNotFound)
			return
		}
		s.audit(r.Context(), "agent.credential.rotation_confirmed", auditSeverityCritical, "", record.Agent.ID, record.Home.ID, requestIDFromContext(r.Context()), "agent_token", state.CredentialID, map[string]any{"generation": state.Generation})
		writeJSON(w, http.StatusOK, state)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) authenticateAgentCredentialRequest(w http.ResponseWriter, r *http.Request) (store.AgentTokenRecord, bool) {
	agentID := strings.TrimSpace(r.Header.Get("X-Hank-Agent-ID"))
	raw, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil || agentID == "" {
		http.Error(w, "unauthorized agent", http.StatusUnauthorized)
		return store.AgentTokenRecord{}, false
	}
	record, err := s.store.ValidateAgentToken(r.Context(), hashToken(raw))
	if err != nil || record.Agent.ID != agentID {
		http.Error(w, "unauthorized agent", http.StatusUnauthorized)
		return store.AgentTokenRecord{}, false
	}
	return record, true
}
