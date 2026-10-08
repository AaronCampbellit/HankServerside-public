package cloud

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleAPNSDeviceRegistration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	var body struct {
		DeviceID          string   `json:"device_id"`
		Token             string   `json:"token"`
		Environment       string   `json:"environment"`
		BundleID          string   `json:"bundle_id"`
		EnabledCategories []string `json:"enabled_categories"`
	}
	if err := parseJSON(w, r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body.DeviceID = strings.TrimSpace(body.DeviceID)
	body.Token = strings.TrimSpace(body.Token)
	body.Environment = normalizeAPNSEnvironment(body.Environment)
	body.BundleID = strings.TrimSpace(body.BundleID)
	if body.DeviceID == "" || body.Token == "" {
		http.Error(w, "device_id and token are required", http.StatusBadRequest)
		return
	}
	if body.Environment == "" {
		body.Environment = "sandbox"
	}
	categories := sanitizeNotificationCategories(body.EnabledCategories)
	encodedCategories, err := json.Marshal(categories)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	device, err := s.store.UpsertAPNSDevice(r.Context(), domain.APNSDevice{
		UserID:            auth.User.ID,
		SessionID:         auth.Session.ID,
		DeviceID:          body.DeviceID,
		Token:             body.Token,
		Environment:       body.Environment,
		BundleID:          body.BundleID,
		EnabledCategories: encodedCategories,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"device_id":  device.DeviceID,
		"categories": categories,
	})
}

func (s *Server) handleAPNSDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/me/devices/"), "/")
	deviceID, suffix, ok := strings.Cut(path, "/")
	if !ok || suffix != "apns" || strings.TrimSpace(deviceID) == "" {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteAPNSDevice(r.Context(), auth.User.ID, deviceID); err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleNotificationSettings(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, notificationSettingsPayload(s.notificationSettingsOrDefault(r.Context(), auth.User.ID)))
	case http.MethodPut:
		var body struct {
			Monitoring        *bool `json:"monitoring"`
			AgentHealth       *bool `json:"agent_health"`
			QuickLinks        *bool `json:"quick_links"`
			Storage           *bool `json:"storage"`
			Notes             *bool `json:"notes"`
			DashboardEntities *bool `json:"dashboard_entities"`
		}
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		settings := s.notificationSettingsOrDefault(r.Context(), auth.User.ID)
		if body.Monitoring != nil {
			settings.MonitoringEnabled = *body.Monitoring
		}
		if body.AgentHealth != nil {
			settings.AgentHealthEnabled = *body.AgentHealth
		}
		if body.QuickLinks != nil {
			settings.QuickLinksEnabled = *body.QuickLinks
		}
		if body.Storage != nil {
			settings.StorageEnabled = *body.Storage
		}
		if body.Notes != nil {
			settings.NotesEnabled = *body.Notes
		}
		if body.DashboardEntities != nil {
			settings.DashboardEntitiesEnabled = *body.DashboardEntities
		}
		saved, err := s.store.SaveNotificationSettings(r.Context(), settings)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, notificationSettingsPayload(saved))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func notificationSettingsPayload(settings domain.NotificationSettings) map[string]any {
	return map[string]any{
		"user_id":            settings.UserID,
		"agent_health":       settings.AgentHealthEnabled,
		"monitoring":         settings.MonitoringEnabled,
		"quick_links":        settings.QuickLinksEnabled,
		"storage":            settings.StorageEnabled,
		"notes":              settings.NotesEnabled,
		"dashboard_entities": settings.DashboardEntitiesEnabled,
		"updated_at":         settings.UpdatedAt,
	}
}
