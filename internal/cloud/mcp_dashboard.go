package cloud

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

// handleProfileMCP backs the "MCP Connector" panel on the AI settings page.
// GET  /v1/me/mcp                      -> connector status + the user's connected apps
// DELETE /v1/me/mcp/connections/{id}   -> disconnect one of the user's apps
//
// Per-user by design: a user sees and revokes only their own MCP grants.
func (s *Server) handleProfileMCP(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}

	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/me/mcp"), "/")
	if rest == "" && r.Method == http.MethodPatch {
		_, membership, err := s.requireSingletonHomeMembership(r.Context(), auth.User.ID)
		if err != nil || membership.Role != domain.HomeRoleAdmin {
			http.Error(w, "administrator required", 403)
			return
		}
		var body struct {
			Enabled *bool `json:"enabled"`
		}
		if parseJSON(w, r, &body) != nil || body.Enabled == nil {
			http.Error(w, "enabled is required", 400)
			return
		}
		if s.store.SetMCPEnabled(r.Context(), *body.Enabled, auth.User.ID) != nil {
			http.Error(w, "could not save connector setting", 500)
			return
		}
		if !*body.Enabled && s.mcpSubscriptions != nil {
			s.mcpSubscriptions.closeAll("connector_disabled")
		}
		s.audit(r.Context(), "mcp.settings.changed", auditSeverityWarning, auth.User.ID, "", "", requestIDFromContext(r.Context()), "mcp_settings", "", map[string]any{"enabled": *body.Enabled})
		writeJSON(w, 200, map[string]any{"enabled": *body.Enabled})
		return
	}

	if rest == "context-sources" || strings.HasPrefix(rest, "context-sources/") {
		s.handleMCPContextSources(w, r, auth, rest)
		return
	}

	if strings.HasPrefix(rest, "connections/") && r.Method == http.MethodDelete {
		tokenID := strings.TrimSpace(strings.TrimPrefix(rest, "connections/"))
		if tokenID == "" {
			http.Error(w, "connection id is required", http.StatusBadRequest)
			return
		}
		if err := s.store.RevokeMCPTokenForUser(r.Context(), tokenID, auth.User.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s.mcpSubscriptions != nil {
			s.mcpSubscriptions.closeToken(tokenID, "token_revoked")
		}
		s.audit(r.Context(), "mcp_oauth.connection_revoked", auditSeverityInfo, auth.User.ID, "", "", requestIDFromContext(r.Context()), "mcp_oauth_token", tokenID, nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	if rest != "" || r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tokens, err := s.store.ListMCPTokensByUser(r.Context(), auth.User.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	contextSources, err := s.store.ListMCPContextSourcesByUser(r.Context(), auth.User.ID, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	clientNames := map[string]string{}
	connections := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		name, resolved := clientNames[t.ClientID]
		if !resolved {
			if client, err := s.store.GetMCPOAuthClient(r.Context(), t.ClientID); err == nil {
				name = client.ClientName
			}
			clientNames[t.ClientID] = name
		}
		// A grant counts as "connected" while it can still be used: either the
		// access token is unexpired, or it can be refreshed (no/future refresh expiry).
		now := time.Now()
		connected := t.AccessExpiresAt.After(now) ||
			t.RefreshExpiresAt == nil || t.RefreshExpiresAt.After(now)
		connections = append(connections, map[string]any{
			"id":           t.ID,
			"client_id":    t.ClientID,
			"client_name":  name,
			"scopes":       t.Scopes,
			"created_at":   t.CreatedAt,
			"last_used_at": t.LastUsedAt,
			"connected":    connected,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": s.mcpIsEnabled(r.Context()),
		"resource_url": func() string {
			if s.mcpIsEnabled(r.Context()) {
				return s.mcpResourceURL(r)
			}
			return ""
		}(),
		"scopes_supported": mcpSupportedScopes,
		"connections":      connections,
		"context_sources":  contextSources,
	})
}
