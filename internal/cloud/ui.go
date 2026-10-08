package cloud

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
)

//go:embed ui/*
var uiAssets embed.FS

var viteHashedAssetPattern = regexp.MustCompile(`(?:^|/)[^/]+-[A-Za-z0-9_-]{8,}\.(?:js|css)$`)

func readEmbeddedUIFile(name string) ([]byte, error) {
	return fs.ReadFile(uiAssets, "ui/"+name)
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if auth, err := s.appAuthFromRequest(r); err == nil {
		if auth.User.PasswordChangeRequired {
			http.Redirect(w, r, "/password-change", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	serveReactApp(w, r)
}

func (s *Server) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/join" {
		http.NotFound(w, r)
		return
	}
	serveReactApp(w, r)
}

func (s *Server) handlePasswordChangePage(w http.ResponseWriter, r *http.Request) {
	s.serveAuthenticatedUIPage(w, r, "/password-change")
}

func (s *Server) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard")
}

func (s *Server) handleHankPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/hank")
}

func (s *Server) handleHomeAssistantPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/home-assistant")
}

func (s *Server) handleProfileNotesPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/profile-notes")
}

func (s *Server) handleFileServerPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/file-server")
}

func (s *Server) handleAgentsPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/agents")
}

func (s *Server) handleNotificationEventPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/notifications/event")
}

func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.serveAuthenticatedUIPage(w, r, "/dashboard/settings")
}

func (s *Server) handleSettingsHomePage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/settings/home")
}

func (s *Server) handleSettingsQuickLinksPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/settings/quick-links")
}

func (s *Server) handleSettingsNotificationsPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/settings/notifications")
}

func (s *Server) handleSettingsPeoplePage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/settings/people")
}

func (s *Server) handleSettingsConnectionsPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/settings/connections")
}

func (s *Server) handleSettingsAIPage(w http.ResponseWriter, r *http.Request) {
	s.serveHomeMemberUIPage(w, r, "/dashboard/settings/ai")
}

func (s *Server) handleSettingsAppsPage(w http.ResponseWriter, r *http.Request) {
	s.serveAdminUIPage(w, r, "/dashboard/settings/apps")
}

func (s *Server) handleSettingsBackupsPage(w http.ResponseWriter, r *http.Request) {
	s.serveAdminUIPage(w, r, "/dashboard/settings/backups")
}

func (s *Server) handleSettingsRecoveryPage(w http.ResponseWriter, r *http.Request) {
	s.serveAdminUIPage(w, r, "/dashboard/settings/recovery")
}

func (s *Server) handleSettingsLogsPage(w http.ResponseWriter, r *http.Request) {
	s.serveAdminUIPage(w, r, "/dashboard/settings/logs")
}

func (s *Server) handleSettingsJoinHomePage(w http.ResponseWriter, r *http.Request) {
	s.serveAuthenticatedUIPage(w, r, "/dashboard/settings/join-home")
}

func serveDeploymentGuide(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/docs/deployment" {
		http.NotFound(w, r)
		return
	}
	serveReactApp(w, r)
}

func servePWAControlFile(w http.ResponseWriter, r *http.Request, expectedPath, embeddedName, contentType string) {
	if r.URL.Path != expectedPath || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	data, err := readEmbeddedUIFile("react/" + embeddedName)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func serveUIFavicon(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/favicon.ico" {
		http.NotFound(w, r)
		return
	}
	serveUIFile(w, r, "favicon.ico", "image/png")
}

func serveUIAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(strings.TrimPrefix(r.URL.Path, "/assets/"))
	if serveReactAsset(w, name) {
		return
	}
	switch name {
	case "favicon.ico", "favicon.png", "hank-icon.png", "hank-icon-192.png", "hank-icon-512.png", "apple-touch-icon.png":
		serveUIFile(w, r, name, "image/png")
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) serveAuthenticatedUIPage(w http.ResponseWriter, r *http.Request, expectedPath string) {
	if r.URL.Path != expectedPath {
		http.NotFound(w, r)
		return
	}
	auth, err := s.appAuthFromRequest(r)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if r.URL.Path != "/password-change" && auth.User.PasswordChangeRequired {
		http.Redirect(w, r, "/password-change", http.StatusSeeOther)
		return
	}
	serveReactApp(w, r)
}

func (s *Server) serveHomeMemberUIPage(w http.ResponseWriter, r *http.Request, expectedPath string) {
	if r.URL.Path != expectedPath {
		http.NotFound(w, r)
		return
	}
	auth, err := s.appAuthFromRequest(r)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if auth.User.PasswordChangeRequired {
		http.Redirect(w, r, "/password-change", http.StatusSeeOther)
		return
	}
	if _, _, err := s.requireSingletonHomeMembership(r.Context(), auth.User.ID); err != nil {
		http.Error(w, "home membership required", http.StatusForbidden)
		return
	}
	serveReactApp(w, r)
}

func (s *Server) serveAdminUIPage(w http.ResponseWriter, r *http.Request, expectedPath string) {
	if r.URL.Path != expectedPath {
		http.NotFound(w, r)
		return
	}
	auth, err := s.appAuthFromRequest(r)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if auth.User.PasswordChangeRequired {
		http.Redirect(w, r, "/password-change", http.StatusSeeOther)
		return
	}
	_, membership, err := s.requireSingletonHomeMembership(r.Context(), auth.User.ID)
	if err != nil || membership.Role != domain.HomeRoleAdmin {
		http.Error(w, "admin role required", http.StatusForbidden)
		return
	}
	serveReactApp(w, r)
}

func serveReactApp(w http.ResponseWriter, r *http.Request) {
	serveUIFile(w, r, "react/index.html", "text/html; charset=utf-8")
}

func serveReactAsset(w http.ResponseWriter, name string) bool {
	if strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return false
	}
	switch {
	case name == "THIRD_PARTY_NOTICES.txt":
		return serveUIFileIfExists(w, "react/assets/"+name, "text/plain; charset=utf-8")
	case strings.HasSuffix(name, ".css"):
		return serveUIFileIfExists(w, "react/assets/"+name, "text/css; charset=utf-8")
	case strings.HasSuffix(name, ".js"):
		return serveUIFileIfExists(w, "react/assets/"+name, "application/javascript; charset=utf-8")
	default:
		return false
	}
}

func serveUIFile(w http.ResponseWriter, r *http.Request, name string, contentType string) {
	data, err := fs.ReadFile(uiAssets, "ui/"+name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if strings.HasSuffix(name, ".html") {
		w.Header().Set("Cache-Control", "private, max-age=30")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func serveUIFileIfExists(w http.ResponseWriter, name string, contentType string) bool {
	data, err := fs.ReadFile(uiAssets, "ui/"+name)
	if err != nil {
		return false
	}
	writeUIFile(w, name, contentType, data)
	return true
}

func writeUIFile(w http.ResponseWriter, name string, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	if isViteHashedAsset(name) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if strings.HasSuffix(name, ".html") {
		w.Header().Set("Cache-Control", "private, max-age=30")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func isViteHashedAsset(name string) bool {
	return strings.HasPrefix(name, "react/assets/") && viteHashedAssetPattern.MatchString(name)
}

func redirectToSettingsRoute(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}
}
