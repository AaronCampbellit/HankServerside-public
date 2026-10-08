package cloud

import (
	"net/http"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Server) handleHomePostgresExtensions(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) != 2 || parts[0] != "postgres" || parts[1] != "extensions" {
		return false
	}
	_ = home
	_ = auth
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	health, err := s.store.RequiredExtensionHealth(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	writeJSON(w, http.StatusOK, health)
	return true
}
