package cloud

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleHomeEntraSettings(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) != 1 || parts[0] != "entra-sso" {
		return false
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := s.store.GetEntraSSOSettings(r.Context(), home.ID)
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "tenant_id": "", "client_id": "", "public_base_url": "", "client_secret_set": false, "managed_by_environment": s.entra != nil})
			return true
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"enabled": settings.Enabled, "tenant_id": settings.TenantID, "client_id": settings.ClientID, "public_base_url": settings.PublicBaseURL, "client_secret_set": strings.TrimSpace(settings.ClientSecret) != "", "managed_by_environment": false, "updated_at": settings.UpdatedAt})
		return true
	case http.MethodPut:
		var body struct {
			Enabled       bool   `json:"enabled"`
			TenantID      string `json:"tenant_id"`
			ClientID      string `json:"client_id"`
			ClientSecret  string `json:"client_secret"`
			PublicBaseURL string `json:"public_base_url"`
		}
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		existing, err := s.store.GetEntraSSOSettings(r.Context(), home.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		secret := strings.TrimSpace(body.ClientSecret)
		if secret == "" && err == nil {
			secret = existing.ClientSecret
		}
		candidate := EntraConfig{Enabled: body.Enabled, TenantID: strings.TrimSpace(body.TenantID), ClientID: strings.TrimSpace(body.ClientID), ClientSecret: secret, PublicBaseURL: strings.TrimSpace(body.PublicBaseURL)}
		service, err := newEntraService(r.Context(), candidate)
		if err != nil {
			http.Error(w, "invalid Entra settings: "+err.Error(), http.StatusBadRequest)
			return true
		}
		settings := domain.EntraSSOSettings{HomeID: home.ID, Enabled: candidate.Enabled, TenantID: candidate.TenantID, ClientID: candidate.ClientID, ClientSecret: candidate.ClientSecret, PublicBaseURL: candidate.PublicBaseURL, UpdatedAt: time.Now().UTC(), UpdatedBy: auth.User.ID}
		if err := s.store.UpsertEntraSSOSettings(r.Context(), settings); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		s.entra = service
		s.audit(r.Context(), "sso.settings.updated", auditSeverityCritical, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "home", home.ID, map[string]any{"enabled": candidate.Enabled})
		writeJSON(w, http.StatusOK, map[string]any{"enabled": candidate.Enabled, "tenant_id": candidate.TenantID, "client_id": candidate.ClientID, "public_base_url": candidate.PublicBaseURL, "client_secret_set": strings.TrimSpace(candidate.ClientSecret) != ""})
		return true
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
}
