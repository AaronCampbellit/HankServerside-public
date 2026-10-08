package cloud

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/storageops"
)

func (s *Server) handleHomeStorage(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) == 0 || parts[0] != "storage" {
		return false
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}
	if s.storage == nil {
		http.Error(w, "storage operations are not configured", http.StatusServiceUnavailable)
		return true
	}

	switch {
	case len(parts) == 2 && parts[1] == "status" && r.Method == http.MethodGet:
		status, err := s.storage.Status()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, status)
		return true

	case len(parts) == 2 && parts[1] == "config" && r.Method == http.MethodGet:
		cfg, err := s.storage.Config()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": cfg})
		return true

	case len(parts) == 2 && parts[1] == "config" && r.Method == http.MethodPut:
		var cfg storageops.Config
		if err := parseJSON(w, r, &cfg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		saved, err := s.storage.SaveConfig(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		event := storageops.NewEvent(storageops.EventOperationConfig, storageops.EventStatusSuccess, storageops.EventSeverityInfo, "Storage settings saved.")
		event.Details = map[string]any{"home_id": home.ID, "updated_by": auth.User.ID}
		if storedEvent, err := storageops.AppendEvent(s.storage.LogDir, event); err == nil {
			s.publishStorageEvent(r.Context(), storedEvent)
		}
		writeJSON(w, http.StatusOK, map[string]any{"config": saved})
		return true

	case len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet:
		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err == nil && parsed > 0 && parsed <= 500 {
				limit = parsed
			}
		}
		events, err := s.storage.Events(storageops.EventFilter{
			Limit:        limit,
			Severity:     strings.TrimSpace(r.URL.Query().Get("severity")),
			Operation:    strings.TrimSpace(r.URL.Query().Get("operation")),
			FailuresOnly: r.URL.Query().Get("failures_only") == "true",
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": nonNilSlice(events)})
		return true

	case len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodDelete:
		if err := s.storage.ClearEvents(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
		return true

	case len(parts) == 2 && parts[1] == "backup" && r.Method == http.MethodPost:
		var body struct {
			BackupType string `json:"backup_type"`
		}
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		intent, err := s.storage.RequestBackup(home.ID, auth.User.ID, body.BackupType)
		if err != nil {
			http.Error(w, err.Error(), statusForStorageRequestError(err))
			return true
		}
		event := storageops.NewEvent(storageops.EventOperationBackup, storageops.EventStatusPending, storageops.EventSeverityInfo, "Manual pgBackRest backup requested.")
		event.Details = map[string]any{"home_id": home.ID, "intent_id": intent.ID, "backup_type": intent.BackupType, "requested_by": auth.User.ID}
		if storedEvent, err := storageops.AppendEvent(s.storage.LogDir, event); err == nil {
			s.publishStorageEvent(r.Context(), storedEvent)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"intent": intent})
		return true

	case len(parts) == 2 && parts[1] == "restore-test" && r.Method == http.MethodPost:
		var body struct {
			BackupLabel string `json:"backup_label"`
		}
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		intent, err := s.storage.RequestRestoreTest(home.ID, auth.User.ID, body.BackupLabel)
		if err != nil {
			http.Error(w, err.Error(), statusForStorageRequestError(err))
			return true
		}
		event := storageops.NewEvent(storageops.EventOperationRestoreTest, storageops.EventStatusPending, storageops.EventSeverityInfo, "Restore verification requested.")
		event.BackupLabel = intent.BackupLabel
		event.Details = map[string]any{"home_id": home.ID, "intent_id": intent.ID, "requested_by": auth.User.ID}
		if storedEvent, err := storageops.AppendEvent(s.storage.LogDir, event); err == nil {
			s.publishStorageEvent(r.Context(), storedEvent)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"intent": intent})
		return true

	case len(parts) == 2 && parts[1] == "restore-primary" && r.Method == http.MethodPost:
		var body struct {
			BackupLabel        string `json:"backup_label"`
			Confirmation       string `json:"confirmation"`
			AdminActionToken   string `json:"admin_action_token"`
			RequestActionToken bool   `json:"request_action_token"`
		}
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		body.BackupLabel = strings.TrimSpace(body.BackupLabel)
		if !storageops.ValidBackupLabel(body.BackupLabel) {
			http.Error(w, "a valid backup label is required", http.StatusBadRequest)
			return true
		}
		action := "storage.restore-primary:" + hashToken(home.ID+"\n"+body.BackupLabel)
		if body.RequestActionToken {
			token, expiresAt := s.adminActionTokens.Issue(auth.User.ID, action, 5*time.Minute)
			s.logger.Warn("primary restore action token issued", "request_id", requestIDFromContext(r.Context()), "home_id", home.ID, "requested_by", auth.User.ID, "expires_at", expiresAt)
			writeJSON(w, http.StatusCreated, map[string]any{"admin_action_token": token, "expires_at": expiresAt})
			return true
		}
		if err := s.adminActionTokens.Consume(strings.TrimSpace(body.AdminActionToken), auth.User.ID, action); err != nil {
			http.Error(w, "admin action token is required", http.StatusForbidden)
			return true
		}
		intent, err := s.storage.RequestPrimaryRestore(home.ID, auth.User.ID, body.BackupLabel, body.Confirmation)
		if err != nil {
			http.Error(w, err.Error(), statusForStorageRequestError(err))
			return true
		}
		event := storageops.NewEvent(storageops.EventOperationPrimaryRestore, storageops.EventStatusPending, storageops.EventSeverityWarning, "Database and matching attachment restore requested.")
		event.BackupLabel = intent.BackupLabel
		event.Details = map[string]any{"home_id": home.ID, "intent_id": intent.ID, "requested_by": auth.User.ID}
		if storedEvent, err := storageops.AppendEvent(s.storage.LogDir, event); err == nil {
			s.publishStorageEvent(r.Context(), storedEvent)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"intent": intent})
		return true
	}

	http.NotFound(w, r)
	return true
}

func statusForStorageRequestError(err error) int {
	if errors.Is(err, storageops.ErrInvalidRequest) {
		return http.StatusBadRequest
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "confirmation") || strings.Contains(message, "backup_type") || strings.Contains(message, "backup_label") {
		return http.StatusBadRequest
	}
	if strings.Contains(message, "secret") || strings.Contains(message, "not configured") {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}
