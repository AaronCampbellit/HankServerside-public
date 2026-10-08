package cloud

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const notesAPITokenPrefix = "hank_note_"

type notesAuthContext struct {
	authContext
	APIToken *domain.NotesAPIToken
}

func (a notesAuthContext) isAPIToken() bool {
	return a.APIToken != nil
}

func (s *Server) requireNotesAuth(w http.ResponseWriter, r *http.Request) (notesAuthContext, bool) {
	auth, err := s.appAuthFromRequest(r)
	if err == nil {
		if err := s.requireCSRFForCookieWrite(r); err != nil {
			s.metrics.IncAuthFailure("csrf_invalid")
			http.Error(w, "invalid csrf token", http.StatusForbidden)
			return notesAuthContext{}, false
		}
		if auth.User.PasswordChangeRequired && !passwordChangeAllowedPath(r.URL.Path) {
			http.Error(w, "password_change_required", http.StatusForbidden)
			return notesAuthContext{}, false
		}
		return notesAuthContext{authContext: auth}, true
	}

	rawToken, bearerErr := bearerToken(r.Header.Get("Authorization"))
	if bearerErr != nil || !strings.HasPrefix(rawToken, notesAPITokenPrefix) {
		s.metrics.IncAuthFailure("app_http_unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return notesAuthContext{}, false
	}
	apiToken, err := s.store.GetNotesAPITokenByHash(r.Context(), hashToken(rawToken))
	if err != nil {
		s.metrics.IncAuthFailure("notes_api_token_unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return notesAuthContext{}, false
	}
	user, err := s.store.GetUserByID(r.Context(), apiToken.UserID)
	if err != nil {
		s.metrics.IncAuthFailure("notes_api_token_unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return notesAuthContext{}, false
	}
	if err := s.store.RecordNotesAPITokenUse(
		r.Context(),
		apiToken.ID,
		r.Method+" "+r.URL.Path,
		stableAuditTarget(clientIP(r)),
		stableAuditTarget(r.UserAgent()),
		time.Now().UTC(),
	); err != nil {
		s.logger.Warn("failed to record notes api token usage", "token_id", apiToken.ID, "error", err)
	}
	return notesAuthContext{authContext: authContext{User: user}, APIToken: &apiToken}, true
}

func (s *Server) requireNotesScope(w http.ResponseWriter, r *http.Request, auth notesAuthContext, scopes ...string) bool {
	if !auth.isAPIToken() {
		return true
	}
	for _, have := range auth.APIToken.Scopes {
		for _, want := range scopes {
			if have == want {
				return true
			}
		}
	}
	s.audit(r.Context(), "notes_api_token.denied", auditSeverityWarning, auth.User.ID, "", auth.APIToken.HomeID, requestIDFromContext(r.Context()), "notes_api_token", auth.APIToken.ID, map[string]any{
		"route":  r.Method + " " + r.URL.Path,
		"scopes": auth.APIToken.Scopes,
	})
	http.Error(w, "notes api token scope denied", http.StatusForbidden)
	return false
}

func (s *Server) rejectNotesAPIToken(w http.ResponseWriter, r *http.Request, auth notesAuthContext) bool {
	if !auth.isAPIToken() {
		return false
	}
	s.audit(r.Context(), "notes_api_token.denied", auditSeverityWarning, auth.User.ID, "", auth.APIToken.HomeID, requestIDFromContext(r.Context()), "notes_api_token", auth.APIToken.ID, map[string]any{
		"route":  r.Method + " " + r.URL.Path,
		"reason": "session_required",
	})
	http.Error(w, "session authentication required", http.StatusForbidden)
	return true
}

func (s *Server) requireNoteAttachmentScope(w http.ResponseWriter, r *http.Request, auth notesAuthContext, attachmentID string) bool {
	switch r.Method {
	case http.MethodGet:
		return s.requireNotesScope(w, r, auth, domain.NotesAPIScopeRead)
	case http.MethodPost:
		if attachmentID != "" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return false
		}
		return s.requireNotesScope(w, r, auth, domain.NotesAPIScopeAppend, domain.NotesAPIScopeWrite)
	case http.MethodDelete:
		if attachmentID == "" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return false
		}
		return s.requireNotesScope(w, r, auth, domain.NotesAPIScopeDelete, domain.NotesAPIScopeWrite)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
}

func (s *Server) handleHomeNotesWithNotesAuth(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if len(parts) == 0 || parts[0] != "notes" {
		return false
	}
	auth, ok := s.requireNotesAuth(w, r)
	if !ok {
		return true
	}
	home, membership, err := s.requireSingletonHomeMembership(r.Context(), auth.User.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return true
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	return s.handleHomeNotesHTTP(w, r, home, membership, auth, parts)
}

func (s *Server) handleHomeNotesAPITokens(w http.ResponseWriter, r *http.Request, home domain.Home, auth authContext, membership domain.HomeMembership, parts []string) bool {
	if len(parts) == 0 || parts[0] != "notes-api-tokens" {
		return false
	}
	if membership.Role != domain.HomeRoleAdmin {
		http.Error(w, errAdminRoleRequired.Error(), http.StatusForbidden)
		return true
	}

	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		tokens, err := s.store.ListNotesAPITokensByHome(r.Context(), home.ID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"tokens": nonNilSlice(tokens)})
		return true

	case len(parts) == 1 && r.Method == http.MethodPost:
		var body struct {
			Name           string     `json:"name"`
			Scopes         []string   `json:"scopes"`
			AllowHomeNotes bool       `json:"allow_home_notes"`
			ExpiresAt      *time.Time `json:"expires_at"`
		}
		if err := parseJSON(w, r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		name := strings.TrimSpace(body.Name)
		if name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return true
		}
		if len(name) > 80 {
			http.Error(w, "name is too long", http.StatusBadRequest)
			return true
		}
		scopes, err := normalizeNotesAPIScopes(body.Scopes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return true
		}
		now := time.Now().UTC()
		expiresAt := body.ExpiresAt
		if expiresAt == nil {
			defaultExpiry := now.Add(90 * 24 * time.Hour)
			expiresAt = &defaultExpiry
		}
		rawToken := notesAPITokenPrefix + newToken()
		token := domain.NotesAPIToken{
			ID:             newID("noteapi"),
			HomeID:         home.ID,
			UserID:         auth.User.ID,
			Name:           name,
			TokenHash:      hashToken(rawToken),
			Scopes:         scopes,
			AllowHomeNotes: body.AllowHomeNotes,
			ExpiresAt:      expiresAt,
			CreatedAt:      now,
			CreatedBy:      auth.User.ID,
			UpdatedAt:      now,
		}
		if err := s.store.CreateNotesAPIToken(r.Context(), token); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		s.audit(r.Context(), "notes_api_token.created", auditSeverityInfo, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "notes_api_token", token.ID, map[string]any{
			"name":             token.Name,
			"scopes":           token.Scopes,
			"allow_home_notes": token.AllowHomeNotes,
			"expires_at":       token.ExpiresAt,
		})
		writeJSON(w, http.StatusCreated, map[string]any{"token": rawToken, "api_token": token})
		return true

	case len(parts) == 2 && r.Method == http.MethodDelete:
		tokenID := strings.TrimSpace(parts[1])
		if tokenID == "" {
			http.Error(w, "token id is required", http.StatusBadRequest)
			return true
		}
		if err := s.store.RevokeNotesAPIToken(r.Context(), home.ID, tokenID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return true
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
		s.audit(r.Context(), "notes_api_token.revoked", auditSeverityWarning, auth.User.ID, "", home.ID, requestIDFromContext(r.Context()), "notes_api_token", tokenID, nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return true
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return true
}

func normalizeNotesAPIScopes(input []string) ([]string, error) {
	allowed := map[string]bool{
		domain.NotesAPIScopeRead:   true,
		domain.NotesAPIScopeAppend: true,
		domain.NotesAPIScopeWrite:  true,
		domain.NotesAPIScopeDelete: true,
	}
	seen := make(map[string]bool)
	var scopes []string
	for _, scope := range input {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if !allowed[scope] {
			return nil, errors.New("unsupported scope: " + scope)
		}
		if !seen[scope] {
			scopes = append(scopes, scope)
			seen[scope] = true
		}
	}
	if len(scopes) == 0 {
		return nil, errors.New("at least one notes api scope is required")
	}
	return scopes, nil
}
