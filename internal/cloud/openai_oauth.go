package cloud

import (
	"errors"
	"net/http"
	"time"

	"github.com/dropfile/HankServerside/internal/store"
)

type openAIAccountStatusResponse struct {
	Configured      bool                             `json:"configured"`
	Linked          bool                             `json:"linked"`
	Missing         []string                         `json:"missing,omitempty"`
	AuthMode        string                           `json:"auth_mode,omitempty"`
	AuthProvider    string                           `json:"auth_provider,omitempty"`
	ChatGPTPlanType string                           `json:"chatgpt_plan_type,omitempty"`
	Pending         *chatGPTDeviceAuthStatusResponse `json:"pending,omitempty"`
	Scopes          string                           `json:"scopes,omitempty"`
	RedirectURI     string                           `json:"redirect_uri,omitempty"`
	TokenType       string                           `json:"token_type,omitempty"`
	Scope           string                           `json:"scope,omitempty"`
	ExpiresAt       *time.Time                       `json:"expires_at,omitempty"`
	UpdatedAt       *time.Time                       `json:"updated_at,omitempty"`
}

func (s *Server) handleOpenAIOAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}

	missing := s.chatGPTOAuthMissingConfig()
	response := openAIAccountStatusResponse{
		Configured:   len(missing) == 0,
		Missing:      missing,
		AuthMode:     chatGPTDeviceAuthMode,
		AuthProvider: openAIAccountProviderChatGPTCodex,
		Pending:      s.chatGPTDeviceAuths.status(auth.User.ID),
	}
	account, err := s.store.GetOpenAIAccount(r.Context(), auth.User.ID)
	if err == nil {
		if account.AuthProvider == openAIAccountProviderChatGPTCodex {
			response.Linked = true
			response.AuthProvider = account.AuthProvider
			response.ChatGPTPlanType = account.ChatGPTPlanType
			response.TokenType = account.TokenType
			response.Scope = account.Scope
			response.ExpiresAt = account.ExpiresAt
			response.UpdatedAt = &account.UpdatedAt
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleOpenAIOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	auth, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	if len(s.chatGPTOAuthMissingConfig()) > 0 {
		http.Error(w, "chatgpt oauth is not configured", http.StatusNotImplemented)
		return
	}
	response, err := s.beginChatGPTDeviceAuth(r.Context(), auth.User.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, response)
}
