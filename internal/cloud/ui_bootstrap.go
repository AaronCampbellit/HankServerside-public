package cloud

import (
	"errors"
	"net/http"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleUIBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}

	response := map[string]any{
		"user": sanitizeUser(auth.User),
		"session": map[string]any{
			"id":         auth.Session.ID,
			"expires_at": auth.Session.ExpiresAt,
		},
		"home":         nil,
		"membership":   nil,
		"permissions":  uiBootstrapPermissions(false, false, false, false),
		"agent":        nil,
		"setup_status": map[string]any{"first_setup_visible": false},
		"features":     map[string]any{"mcp_enabled": s.mcpIsEnabled(r.Context())},
		"server":       map[string]any{"version": s.runtimeVersion},
		"navigation":   uiBootstrapNavigation(),
	}

	home, membership, err := s.requireSingletonHomeMembership(r.Context(), auth.User.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, response)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	canUseHomeAssistant, err := s.homeFeatureAllowed(r.Context(), home, membership, auth.User.ID, domain.HomePermissionFeatureHomeAssistant)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	canUseFiles, err := s.homeFeatureAllowed(r.Context(), home, membership, auth.User.ID, domain.HomePermissionFeatureFiles)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	canUseNotes, err := s.homeFeatureAllowed(r.Context(), home, membership, auth.User.ID, domain.HomePermissionFeatureNotes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	agent, err := s.uiBootstrapAgentSnapshot(r, home)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	response["home"] = home
	response["membership"] = membership
	response["permissions"] = uiBootstrapPermissions(membership.Role == domain.HomeRoleAdmin, canUseHomeAssistant, canUseFiles, canUseNotes)
	response["agent"] = agent
	response["setup_status"] = s.uiBootstrapSetupStatus(r, home)
	writeJSON(w, http.StatusOK, response)
}

func uiBootstrapPermissions(isAdmin bool, canUseHomeAssistant bool, canUseFiles bool, canUseNotes bool) map[string]bool {
	hasAnyHomeAccess := isAdmin || canUseHomeAssistant || canUseFiles || canUseNotes
	return map[string]bool{
		"is_admin":              isAdmin,
		"can_manage_people":     isAdmin,
		"can_manage_settings":   isAdmin,
		"can_use_homeassistant": canUseHomeAssistant,
		"can_use_files":         canUseFiles,
		"can_use_notes":         canUseNotes,
		"can_use_assistant":     hasAnyHomeAccess,
		"can_view_storage":      isAdmin,
		"can_manage_apps":       isAdmin,
	}
}

func (s *Server) uiBootstrapSetupStatus(r *http.Request, home domain.Home) map[string]any {
	firstSetupVisible := true
	if runtime, err := s.store.GetCloudRuntime(r.Context()); err == nil && runtime.StartedAt.After(home.CreatedAt) {
		firstSetupVisible = false
	}
	return map[string]any{"first_setup_visible": firstSetupVisible}
}

func (s *Server) uiBootstrapAgentSnapshot(r *http.Request, home domain.Home) (any, error) {
	agent, err := s.store.GetAgentByHomeID(r.Context(), home.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snapshot := map[string]any{
		"agent_id":     agent.ID,
		"name":         agent.Name,
		"status":       agent.Status,
		"last_seen_at": agent.LastSeenAt,
		"home_id":      home.ID,
		"home_name":    home.Name,
	}
	if online, ok := s.router.GetAgent(home.ID); ok {
		snapshot["status"] = online.agent.Status
		snapshot["capabilities"] = online.capabilities
	}
	return snapshot, nil
}

func uiBootstrapNavigation() []map[string]any {
	return []map[string]any{
		{"path": "/dashboard", "label": "Dashboard"},
		{"path": "/dashboard/hank", "label": "HankAI"},
		{"path": "/dashboard/home-assistant", "label": "Home Assistant"},
		{"path": "/dashboard/profile-notes", "label": "Profile Notes"},
		{"path": "/dashboard/file-server", "label": "File Server"},
		{"path": "/dashboard/settings/home", "label": "Home & Agent Setup"},
		{"path": "/dashboard/settings/quick-links", "label": "Quick Links"},
		{"path": "/dashboard/settings/notifications", "label": "Notifications"},
		{"path": "/dashboard/settings/people", "label": "People"},
		{"path": "/dashboard/settings/connections", "label": "Connections"},
		{"path": "/dashboard/settings/ai", "label": "AI & MCP"},
		{"path": "/dashboard/settings/apps", "label": "Apps", "admin_only": true},
		{"path": "/dashboard/settings/backups", "label": "Backups", "admin_only": true},
		{"path": "/dashboard/settings/recovery", "label": "Recovery", "admin_only": true},
		{"path": "/dashboard/settings/logs", "label": "Logs", "admin_only": true},
		{"path": "/dashboard/settings/join-home", "label": "Join Home"},
	}
}
