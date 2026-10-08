package cloud

import (
	"crypto/elliptic"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleWebPushConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, ok := s.requireAuth(w, r); !ok {
		return
	}
	payload := map[string]any{"enabled": s.webPushConfig.Enabled}
	if s.webPushConfig.Enabled {
		payload["public_key"] = s.webPushConfig.PublicKey
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleWebPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/me/web-push/subscriptions"), "/")
	if path == "" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.registerWebPushSubscription(w, r, auth)
		return
	}
	if r.Method != http.MethodDelete || strings.Contains(path, "/") {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	err := s.store.DeleteWebPushSubscription(r.Context(), auth.User.ID, path)
	if errors.Is(err, store.ErrNotFound) {
		writeNotificationAPIError(w, http.StatusNotFound, "web_push_subscription_not_found")
		return
	}
	if err != nil {
		writeNotificationAPIError(w, http.StatusInternalServerError, "web_push_subscription_delete_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) registerWebPushSubscription(w http.ResponseWriter, r *http.Request, auth authContext) {
	if !s.webPushConfig.Enabled {
		writeNotificationAPIError(w, http.StatusServiceUnavailable, "web_push_unavailable")
		return
	}
	var body struct {
		Endpoint     string `json:"endpoint"`
		BrowserLabel string `json:"browser_label"`
		Keys         struct {
			P256DH string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := parseJSON(w, r, &body); err != nil {
		writeNotificationAPIError(w, http.StatusBadRequest, "invalid_web_push_subscription")
		return
	}
	body.Endpoint = strings.TrimSpace(body.Endpoint)
	body.Keys.P256DH = strings.TrimSpace(body.Keys.P256DH)
	body.Keys.Auth = strings.TrimSpace(body.Keys.Auth)
	body.BrowserLabel = strings.TrimSpace(body.BrowserLabel)
	if len(body.Endpoint) > 2048 || len(body.Keys.P256DH) > 512 || len(body.Keys.Auth) > 256 || utf8.RuneCountInString(body.BrowserLabel) > 120 || validateWebPushEndpoint(body.Endpoint) != nil {
		writeNotificationAPIError(w, http.StatusBadRequest, "invalid_web_push_subscription")
		return
	}
	publicKey, publicErr := base64.RawURLEncoding.DecodeString(body.Keys.P256DH)
	authSecret, authErr := base64.RawURLEncoding.DecodeString(body.Keys.Auth)
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKey)
	if publicErr != nil || x == nil || y == nil || authErr != nil || len(authSecret) < 16 || len(authSecret) > 64 {
		writeNotificationAPIError(w, http.StatusBadRequest, "invalid_web_push_subscription_keys")
		return
	}
	subscription, err := s.store.UpsertWebPushSubscription(r.Context(), domain.WebPushSubscription{
		ID: newID("wps"), UserID: auth.User.ID, SessionID: auth.Session.ID,
		Endpoint: body.Endpoint, P256DH: body.Keys.P256DH, Auth: body.Keys.Auth, BrowserLabel: body.BrowserLabel,
	})
	if errors.Is(err, store.ErrConflict) {
		writeNotificationAPIError(w, http.StatusConflict, "web_push_endpoint_in_use")
		return
	}
	if err != nil {
		writeNotificationAPIError(w, http.StatusInternalServerError, "web_push_subscription_save_failed")
		return
	}
	subscription.Endpoint = ""
	subscription.P256DH = ""
	subscription.Auth = ""
	writeJSON(w, http.StatusOK, map[string]any{"subscription": subscription})
}
