package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
)

type mcpContextSourceRequest struct {
	Name         string `json:"name"`
	FileSourceID string `json:"file_source_id"`
	RootPath     string `json:"root_path"`
	Enabled      *bool  `json:"enabled,omitempty"`
}

func normalizeMCPContextRoot(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	raw = strings.TrimLeft(raw, "/")
	clean := path.Clean(raw)
	if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("root_path must be a contained folder inside the selected share")
	}
	return clean, nil
}

func normalizeMCPContextRequest(body mcpContextSourceRequest) (mcpContextSourceRequest, error) {
	body.Name = strings.TrimSpace(body.Name)
	body.FileSourceID = strings.TrimSpace(body.FileSourceID)
	if body.Name == "" {
		return body, errors.New("name is required")
	}
	if body.FileSourceID == "" {
		return body, errors.New("file_source_id is required")
	}
	root, err := normalizeMCPContextRoot(body.RootPath)
	if err != nil {
		return body, err
	}
	body.RootPath = root
	return body, nil
}

func (s *Server) handleMCPContextSources(w http.ResponseWriter, r *http.Request, auth authContext, rest string) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 1 && parts[0] == "context-sources" {
		switch r.Method {
		case http.MethodGet:
			items, err := s.store.ListMCPContextSourcesByUser(r.Context(), auth.User.ID, false)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"sources": items})
		case http.MethodPost:
			var body mcpContextSourceRequest
			if parseJSON(w, r, &body) != nil {
				return
			}
			body, err := normalizeMCPContextRequest(body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			home, err := s.store.GetSingletonHomeForUser(r.Context(), auth.User.ID)
			if err != nil {
				http.Error(w, "home not found", http.StatusNotFound)
				return
			}
			now := time.Now().UTC()
			enabled := true
			if body.Enabled != nil {
				enabled = *body.Enabled
			}
			source := domain.MCPContextSource{ID: newID("mcpcs"), OwnerUserID: auth.User.ID, HomeID: home.ID, Name: body.Name, FileSourceID: body.FileSourceID, RootPath: body.RootPath, Enabled: enabled, CreatedAt: now, UpdatedAt: now}
			if err := s.store.CreateMCPContextSource(r.Context(), source); err != nil {
				http.Error(w, "could not create context source", http.StatusConflict)
				return
			}
			s.audit(r.Context(), "mcp_context_source.created", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "mcp_context_source", source.ID, map[string]any{"name": source.Name, "source_id": source.FileSourceID, "root_path": source.RootPath})
			writeJSON(w, http.StatusCreated, source)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if len(parts) < 2 || parts[0] != "context-sources" {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimSpace(parts[1])
	if id == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 3 && parts[2] == "test" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.testMCPContextSource(w, r, auth, id)
		return
	}
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	source, err := s.store.GetMCPContextSourceForUser(r.Context(), id, auth.User.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	switch r.Method {
	case http.MethodPut:
		var body mcpContextSourceRequest
		if parseJSON(w, r, &body) != nil {
			return
		}
		body, err = normalizeMCPContextRequest(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		source.Name, source.FileSourceID, source.RootPath = body.Name, body.FileSourceID, body.RootPath
		if body.Enabled != nil {
			source.Enabled = *body.Enabled
		}
		source.UpdatedAt = time.Now().UTC()
		if err := s.store.UpdateMCPContextSourceForUser(r.Context(), source); err != nil {
			http.Error(w, "could not update context source", http.StatusConflict)
			return
		}
		s.audit(r.Context(), "mcp_context_source.updated", auditSeverityInfo, auth.User.ID, "", source.HomeID, requestIDFromContext(r.Context()), "mcp_context_source", source.ID, map[string]any{"enabled": source.Enabled})
		writeJSON(w, http.StatusOK, source)
	case http.MethodDelete:
		if err := s.store.DeleteMCPContextSourceForUser(r.Context(), source.ID, auth.User.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.audit(r.Context(), "mcp_context_source.deleted", auditSeverityInfo, auth.User.ID, "", source.HomeID, requestIDFromContext(r.Context()), "mcp_context_source", source.ID, nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) testMCPContextSource(w http.ResponseWriter, r *http.Request, auth authContext, id string) {
	source, err := s.store.GetMCPContextSourceForUser(r.Context(), id, auth.User.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	_, callErr := s.callMCPContextAgent(r.Context(), source, protocol.CommandMCPContextTest, protocol.MCPContextTestRequest{SourceID: source.FileSourceID, RootPath: source.RootPath})
	now := time.Now().UTC()
	source.LastTestedAt = &now
	source.LastTestError = ""
	status := http.StatusOK
	if callErr != nil {
		source.LastTestError = "primary Hank Agent or share unavailable"
		status = http.StatusServiceUnavailable
	}
	source.UpdatedAt = now
	_ = s.store.UpdateMCPContextSourceForUser(r.Context(), source)
	s.audit(r.Context(), "mcp_context_source.tested", auditSeverityInfo, auth.User.ID, "", source.HomeID, requestIDFromContext(r.Context()), "mcp_context_source", source.ID, map[string]any{"ok": callErr == nil})
	writeJSON(w, status, map[string]any{"ok": callErr == nil, "source": source})
}

func (s *Server) mcpContextSourceForTool(ctx context.Context, userID, id string) (domain.MCPContextSource, error) {
	source, err := s.store.GetMCPContextSourceForUser(ctx, strings.TrimSpace(id), userID)
	if err != nil || !source.Enabled {
		return domain.MCPContextSource{}, store.ErrNotFound
	}
	return source, nil
}

func (s *Server) callMCPContextAgent(ctx context.Context, source domain.MCPContextSource, command string, body any) (protocol.Envelope, error) {
	response, err := s.sendAgentCommand(ctx, source.HomeID, command, body)
	if err != nil {
		return protocol.Envelope{}, fmt.Errorf("temporarily unavailable: %w", err)
	}
	if response.Error != nil {
		return protocol.Envelope{}, errors.New(response.Error.Message)
	}
	s.audit(ctx, "mcp.context_accessed", auditSeverityInfo, source.OwnerUserID, "", source.HomeID, requestIDFromContext(ctx), "mcp_context_source", source.ID, map[string]any{"operation": command})
	return response, nil
}
