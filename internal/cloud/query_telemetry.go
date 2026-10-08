package cloud

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
)

func (s *Server) handleHomeQueryTelemetry(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) != 1 || parts[0] != "query-telemetry" {
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
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	rows, err := s.store.TopQueryTelemetry(r.Context(), limit)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(strings.ToLower(err.Error()), "pg_stat_statements") {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), status)
		return true
	}
	writeJSON(w, http.StatusOK, map[string]any{"queries": nonNilSlice(rows)})
	return true
}
