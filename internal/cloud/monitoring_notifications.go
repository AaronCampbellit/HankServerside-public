package cloud

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Server) ConfigureAlertmanagerWebhookToken(token string) {
	s.alertmanagerWebhookToken = strings.TrimSpace(token)
}

type alertmanagerNotification struct {
	Version         string              `json:"version"`
	TruncatedAlerts int                 `json:"truncatedAlerts"`
	Alerts          []alertmanagerAlert `json:"alerts"`
}
type alertmanagerAlert struct {
	Status      string            `json:"status"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

func (s *Server) handleAlertmanagerWebhook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	tokenExpected := s.alertmanagerWebhookToken
	if s.monitoring != nil {
		home, err := s.store.GetSingletonHome(r.Context())
		if err != nil {
			http.Error(w, "Home is unavailable", 503)
			return
		}
		settings, err := s.store.GetMonitoringSettings(r.Context(), home.ID)
		if err != nil || settings.WebhookToken == "" {
			http.Error(w, "monitoring is unavailable", 503)
			return
		}
		tokenExpected = settings.WebhookToken
	}
	if tokenExpected == "" {
		http.NotFound(w, r)
		return
	}
	// This credential has only inbox-write authority. Cookies, admin sessions,
	// scrape credentials and query parameters cannot authorize this endpoint.
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(token), []byte(tokenExpected)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "query parameters are not supported", http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	defer r.Body.Close()
	var body alertmanagerNotification
	if err := decoder.Decode(&body); err != nil {
		http.Error(w, "invalid alert payload", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid alert payload", http.StatusBadRequest)
		return
	}
	if body.Version != "4" || body.TruncatedAlerts != 0 || len(body.Alerts) == 0 || len(body.Alerts) > 256 {
		http.Error(w, "unsupported alert payload", http.StatusBadRequest)
		return
	}
	events := make([]NotificationEvent, 0, len(body.Alerts))
	for _, alert := range body.Alerts {
		if (alert.Status != "firing" && alert.Status != "resolved") || alert.StartsAt.IsZero() || strings.TrimSpace(alert.Labels["alertname"]) == "" || len(alert.Labels) > 64 {
			http.Error(w, "invalid alert", http.StatusBadRequest)
			return
		}
		// Labels are hashed for identity, never copied into audit metadata or links.
		labels, _ := json.Marshal(alert.Labels)
		digest := sha256.Sum256(labels)
		identity := hex.EncodeToString(digest[:])
		key := sha256.Sum256([]byte(identity + "\x00" + alert.StartsAt.UTC().Format(time.RFC3339Nano) + "\x00" + alert.Status))
		summary := alert.Annotations["summary"]
		if strings.TrimSpace(summary) == "" {
			summary = alert.Labels["alertname"]
		}
		events = append(events, NotificationEvent{Kind: notificationKindMonitoring, ResourceID: identity, SourceEventKey: "alertmanager-" + hex.EncodeToString(key[:]), Outcome: alert.Status, Status: alert.Status, Severity: alert.Labels["severity"], DisplayName: summary, OccurredAt: time.Now().UTC()})
	}
	home, err := s.store.GetSingletonHome(r.Context())
	if err != nil {
		http.Error(w, "Home is unavailable", http.StatusServiceUnavailable)
		return
	}
	if s.notificationService == nil {
		http.Error(w, "notification service is unavailable", http.StatusServiceUnavailable)
		return
	}
	for _, event := range events {
		event.HomeID = home.ID
		if _, err = s.notificationService.Emit(r.Context(), event); err != nil {
			http.Error(w, "notification delivery could not be persisted", http.StatusServiceUnavailable)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
