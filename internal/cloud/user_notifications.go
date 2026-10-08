package cloud

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func (s *Server) handleUserNotifications(w http.ResponseWriter, r *http.Request) {
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/me/notifications"), "/")
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			s.listUserNotifications(w, r, auth.User.ID)
		case http.MethodDelete:
			deleted, err := s.store.DeleteAllUserNotifications(r.Context(), auth.User.ID)
			if err != nil {
				writeNotificationAPIError(w, http.StatusInternalServerError, "notification_clear_failed")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "unread_count": 0})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if path == "read-all" {
		if r.Method != http.MethodPut {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		updated, err := s.store.MarkAllUserNotificationsRead(r.Context(), auth.User.ID, time.Now().UTC())
		if err != nil {
			writeNotificationAPIError(w, http.StatusInternalServerError, "notification_read_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "unread_count": 0})
		return
	}
	parts := strings.Split(path, "/")
	notificationID := strings.TrimSpace(parts[0])
	if notificationID == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && parts[1] == "event" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		event, err := s.store.GetAuditEventForUserNotification(r.Context(), auth.User.ID, notificationID)
		if errors.Is(err, store.ErrNotFound) {
			writeNotificationAPIError(w, http.StatusNotFound, "notification_event_not_found")
			return
		}
		if err != nil {
			writeNotificationAPIError(w, http.StatusInternalServerError, "notification_event_load_failed")
			return
		}
		home, _, membershipErr := s.requireSingletonHomeMembership(r.Context(), auth.User.ID)
		if membershipErr != nil || event.HomeID == nil || home.ID != *event.HomeID {
			writeNotificationAPIError(w, http.StatusNotFound, "notification_event_not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"event": s.auditEventSnapshots(r.Context(), home.ID, []store.AuditEvent{event})[0]})
		return
	}
	var err error
	switch {
	case len(parts) == 2 && parts[1] == "read" && r.Method == http.MethodPut:
		err = s.store.MarkUserNotificationRead(r.Context(), auth.User.ID, notificationID, time.Now().UTC())
	case len(parts) == 1 && r.Method == http.MethodDelete:
		err = s.store.DeleteUserNotification(r.Context(), auth.User.ID, notificationID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeNotificationAPIError(w, http.StatusNotFound, "notification_not_found")
		return
	}
	if err != nil {
		writeNotificationAPIError(w, http.StatusInternalServerError, "notification_mutation_failed")
		return
	}
	unread, err := s.store.CountUnreadUserNotifications(r.Context(), auth.User.ID)
	if err != nil {
		writeNotificationAPIError(w, http.StatusInternalServerError, "notification_count_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unread_count": unread})
}

func (s *Server) listUserNotifications(w http.ResponseWriter, r *http.Request, userID string) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeNotificationAPIError(w, http.StatusBadRequest, "invalid_notification_limit")
			return
		}
		limit = parsed
	}
	unreadOnly := false
	if raw := strings.TrimSpace(r.URL.Query().Get("unread")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeNotificationAPIError(w, http.StatusBadRequest, "invalid_notification_unread_filter")
			return
		}
		unreadOnly = parsed
	}
	page, err := s.store.ListUserNotifications(r.Context(), userID, domain.NotificationListOptions{Cursor: r.URL.Query().Get("cursor"), Limit: limit, Unread: unreadOnly})
	if errors.Is(err, store.ErrInvalidCursor) {
		writeNotificationAPIError(w, http.StatusBadRequest, "invalid_notification_cursor")
		return
	}
	if err != nil {
		writeNotificationAPIError(w, http.StatusInternalServerError, "notification_list_failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": nonNilSlice(page.Items), "next_cursor": page.NextCursor, "unread_count": page.UnreadCount})
}

func writeNotificationAPIError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code}})
}
