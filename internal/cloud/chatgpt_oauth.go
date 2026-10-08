package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

const (
	assistantProviderChatGPTCodex     = "chatgpt_codex"
	openAIAccountProviderChatGPTCodex = "chatgpt_codex"
	chatGPTDeviceAuthMode             = "device_code"
	chatGPTDeviceAuthTTL              = 15 * time.Minute
	chatGPTDeviceAuthDefaultPollAfter = 5
)

var errChatGPTRelinkRequired = errors.New("relink ChatGPT/Codex")

type chatGPTDeviceAuthRegistry struct {
	mu      sync.Mutex
	entries map[string]chatGPTDeviceAuthEntry
}

type chatGPTDeviceAuthEntry struct {
	DeviceAuthID     string
	VerificationURL  string
	UserCode         string
	ExpiresAt        time.Time
	PollAfterSeconds int
	State            string
	Error            string
	UpdatedAt        time.Time
}

type chatGPTDeviceAuthStatusResponse struct {
	State            string    `json:"state"`
	VerificationURL  string    `json:"verification_url,omitempty"`
	UserCode         string    `json:"user_code,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	PollAfterSeconds int       `json:"poll_after_seconds,omitempty"`
	Error            string    `json:"error,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
}

type chatGPTDeviceAuthStartResponse struct {
	AuthMode         string    `json:"auth_mode"`
	VerificationURL  string    `json:"verification_url"`
	UserCode         string    `json:"user_code"`
	ExpiresAt        time.Time `json:"expires_at"`
	PollAfterSeconds int       `json:"poll_after_seconds"`
}

type openAITokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

func newChatGPTDeviceAuthRegistry() *chatGPTDeviceAuthRegistry {
	return &chatGPTDeviceAuthRegistry{entries: make(map[string]chatGPTDeviceAuthEntry)}
}

func (s *Server) shouldUseChatGPTOAuth() bool {
	cfg := s.assistantAI
	cfg.normalize()
	return cfg.ChatGPTOAuthEnabled || cfg.Provider == assistantProviderChatGPTCodex
}

func (s *Server) chatGPTOAuthMissingConfig() []string {
	cfg := s.assistantAI
	cfg.normalize()
	missing := []string{}
	if !cfg.ChatGPTOAuthEnabled {
		missing = append(missing, "HANK_CHATGPT_OAUTH_ENABLED")
	}
	if strings.TrimSpace(cfg.ChatGPTAuthIssuer) == "" {
		missing = append(missing, "HANK_CHATGPT_AUTH_ISSUER")
	}
	if strings.TrimSpace(cfg.ChatGPTBackendBaseURL) == "" {
		missing = append(missing, "HANK_CHATGPT_BACKEND_BASE_URL")
	}
	if strings.TrimSpace(cfg.ChatGPTClientID) == "" {
		missing = append(missing, "HANK_CHATGPT_CLIENT_ID")
	}
	return missing
}

func (r *chatGPTDeviceAuthRegistry) setPending(userID string, entry chatGPTDeviceAuthEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry.State = "pending"
	entry.UpdatedAt = time.Now().UTC()
	r.entries[userID] = entry
}

func (r *chatGPTDeviceAuthRegistry) status(userID string) *chatGPTDeviceAuthStatusResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[userID]
	if !ok {
		return nil
	}
	if entry.State == "pending" && !entry.ExpiresAt.IsZero() && time.Now().UTC().After(entry.ExpiresAt) {
		entry.State = "expired"
		entry.Error = "device code expired"
		entry.UpdatedAt = time.Now().UTC()
		r.entries[userID] = entry
	}
	return &chatGPTDeviceAuthStatusResponse{
		State:            entry.State,
		VerificationURL:  entry.VerificationURL,
		UserCode:         entry.UserCode,
		ExpiresAt:        entry.ExpiresAt,
		PollAfterSeconds: entry.PollAfterSeconds,
		Error:            entry.Error,
		UpdatedAt:        entry.UpdatedAt,
	}
}

func (r *chatGPTDeviceAuthRegistry) markCompleted(userID string, deviceAuthID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[userID]
	if !ok || entry.DeviceAuthID != deviceAuthID {
		return
	}
	entry.State = "linked"
	entry.Error = ""
	entry.UserCode = ""
	entry.UpdatedAt = time.Now().UTC()
	r.entries[userID] = entry
}

func (r *chatGPTDeviceAuthRegistry) markFailed(userID string, deviceAuthID string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[userID]
	if !ok || entry.DeviceAuthID != deviceAuthID {
		return
	}
	entry.State = "failed"
	if err != nil {
		entry.Error = err.Error()
	}
	entry.UpdatedAt = time.Now().UTC()
	r.entries[userID] = entry
}

type chatGPTDeviceCodeResponse struct {
	DeviceAuthID    string `json:"device_auth_id"`
	UserCode        string `json:"user_code"`
	UserCodeLegacy  string `json:"usercode"`
	Interval        int    `json:"-"`
	ExpiresIn       int    `json:"expires_in"`
	VerificationURI string `json:"verification_uri"`
	VerificationURL string `json:"verification_url"`
}

func (r *chatGPTDeviceCodeResponse) UnmarshalJSON(data []byte) error {
	var raw struct {
		DeviceAuthID    string          `json:"device_auth_id"`
		UserCode        string          `json:"user_code"`
		UserCodeLegacy  string          `json:"usercode"`
		Interval        json.RawMessage `json:"interval"`
		ExpiresIn       int             `json:"expires_in"`
		VerificationURI string          `json:"verification_uri"`
		VerificationURL string          `json:"verification_url"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.DeviceAuthID = raw.DeviceAuthID
	r.UserCode = raw.UserCode
	r.UserCodeLegacy = raw.UserCodeLegacy
	r.ExpiresIn = raw.ExpiresIn
	r.VerificationURI = raw.VerificationURI
	r.VerificationURL = raw.VerificationURL
	if len(raw.Interval) == 0 {
		return nil
	}
	var intervalInt int
	if err := json.Unmarshal(raw.Interval, &intervalInt); err == nil {
		r.Interval = intervalInt
		return nil
	}
	var intervalString string
	if err := json.Unmarshal(raw.Interval, &intervalString); err == nil {
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(intervalString))
		if parseErr != nil {
			return parseErr
		}
		r.Interval = parsed
	}
	return nil
}

type chatGPTDeviceTokenPollResponse struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeChallenge     string `json:"code_challenge"`
	CodeVerifier      string `json:"code_verifier"`
}

type chatGPTTokenClaims struct {
	Email   string `json:"email"`
	Profile struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
	Auth struct {
		ChatGPTPlanType         string `json:"chatgpt_plan_type"`
		ChatGPTUserID           string `json:"chatgpt_user_id"`
		UserID                  string `json:"user_id"`
		ChatGPTAccountID        string `json:"chatgpt_account_id"`
		ChatGPTAccountIsFedRAMP bool   `json:"chatgpt_account_is_fedramp"`
	} `json:"https://api.openai.com/auth"`
	Exp int64 `json:"exp"`
}

type chatGPTRefreshError struct {
	statusCode int
	body       string
	permanent  bool
}

func (e *chatGPTRefreshError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("chatgpt token refresh failed with status %d", e.statusCode)
	}
	return fmt.Sprintf("chatgpt token refresh failed with status %d: %s", e.statusCode, e.body)
}

func (s *Server) beginChatGPTDeviceAuth(ctx context.Context, userID string) (chatGPTDeviceAuthStartResponse, error) {
	cfg := s.assistantAI
	cfg.normalize()
	deviceCode, err := s.requestChatGPTDeviceCode(ctx, cfg)
	if err != nil {
		return chatGPTDeviceAuthStartResponse{}, err
	}

	now := time.Now().UTC()
	expiresAt := now.Add(chatGPTDeviceAuthTTL)
	if deviceCode.ExpiresIn > 0 {
		expiresAt = now.Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
	}
	userCode := strings.TrimSpace(deviceCode.UserCode)
	if userCode == "" {
		userCode = strings.TrimSpace(deviceCode.UserCodeLegacy)
	}
	pollAfter := deviceCode.Interval
	if pollAfter <= 0 {
		pollAfter = chatGPTDeviceAuthDefaultPollAfter
	}
	verificationURL := strings.TrimSpace(deviceCode.VerificationURL)
	if verificationURL == "" {
		verificationURL = strings.TrimSpace(deviceCode.VerificationURI)
	}
	if verificationURL == "" {
		verificationURL = strings.TrimRight(cfg.ChatGPTAuthIssuer, "/") + "/codex/device"
	}

	entry := chatGPTDeviceAuthEntry{
		DeviceAuthID:     strings.TrimSpace(deviceCode.DeviceAuthID),
		VerificationURL:  verificationURL,
		UserCode:         userCode,
		ExpiresAt:        expiresAt,
		PollAfterSeconds: pollAfter,
	}
	if entry.DeviceAuthID == "" || entry.UserCode == "" {
		return chatGPTDeviceAuthStartResponse{}, errors.New("chatgpt device auth response is missing device id or user code")
	}
	s.chatGPTDeviceAuths.setPending(userID, entry)
	go s.completeChatGPTDeviceAuth(userID, entry)

	return chatGPTDeviceAuthStartResponse{
		AuthMode:         chatGPTDeviceAuthMode,
		VerificationURL:  entry.VerificationURL,
		UserCode:         entry.UserCode,
		ExpiresAt:        entry.ExpiresAt,
		PollAfterSeconds: entry.PollAfterSeconds,
	}, nil
}

func (s *Server) completeChatGPTDeviceAuth(userID string, entry chatGPTDeviceAuthEntry) {
	cfg := s.assistantAI
	cfg.normalize()
	ctx, cancel := context.WithDeadline(context.Background(), entry.ExpiresAt)
	defer cancel()

	firstPoll := true
	for {
		if firstPoll {
			firstPoll = false
		} else {
			timer := time.NewTimer(time.Duration(entry.PollAfterSeconds) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				s.chatGPTDeviceAuths.markFailed(userID, entry.DeviceAuthID, errors.New("device auth timed out"))
				return
			case <-timer.C:
			}
		}

		poll, pending, err := s.pollChatGPTDeviceToken(ctx, cfg, entry)
		if pending {
			continue
		}
		if err != nil {
			s.chatGPTDeviceAuths.markFailed(userID, entry.DeviceAuthID, err)
			return
		}

		tokens, err := s.exchangeChatGPTDeviceCode(ctx, cfg, poll.AuthorizationCode, poll.CodeVerifier)
		if err != nil {
			s.chatGPTDeviceAuths.markFailed(userID, entry.DeviceAuthID, err)
			return
		}
		account, err := chatGPTAccountFromTokenResponse(userID, tokens, nil)
		if err != nil {
			s.chatGPTDeviceAuths.markFailed(userID, entry.DeviceAuthID, err)
			return
		}
		if err := s.store.UpsertOpenAIAccount(ctx, account); err != nil {
			s.chatGPTDeviceAuths.markFailed(userID, entry.DeviceAuthID, err)
			return
		}
		s.chatGPTDeviceAuths.markCompleted(userID, entry.DeviceAuthID)
		return
	}
}

func (s *Server) requestChatGPTDeviceCode(ctx context.Context, cfg AssistantAIConfig) (chatGPTDeviceCodeResponse, error) {
	endpoint := strings.TrimRight(cfg.ChatGPTAuthIssuer, "/") + "/api/accounts/deviceauth/usercode"
	body := map[string]string{"client_id": cfg.ChatGPTClientID}
	var response chatGPTDeviceCodeResponse
	if err := postJSONToEndpoint(ctx, endpoint, body, nil, &response); err != nil {
		return chatGPTDeviceCodeResponse{}, err
	}
	return response, nil
}

func (s *Server) pollChatGPTDeviceToken(ctx context.Context, cfg AssistantAIConfig, entry chatGPTDeviceAuthEntry) (chatGPTDeviceTokenPollResponse, bool, error) {
	endpoint := strings.TrimRight(cfg.ChatGPTAuthIssuer, "/") + "/api/accounts/deviceauth/token"
	body := map[string]string{
		"device_auth_id": entry.DeviceAuthID,
		"user_code":      entry.UserCode,
	}
	var response chatGPTDeviceTokenPollResponse
	err := postJSONToEndpoint(ctx, endpoint, body, nil, &response)
	if err == nil {
		return response, false, nil
	}
	var providerErr *providerHTTPError
	if errors.As(err, &providerErr) && (providerErr.StatusCode == http.StatusForbidden || providerErr.StatusCode == http.StatusNotFound) {
		return chatGPTDeviceTokenPollResponse{}, true, nil
	}
	return chatGPTDeviceTokenPollResponse{}, false, err
}

func (s *Server) exchangeChatGPTDeviceCode(ctx context.Context, cfg AssistantAIConfig, code string, codeVerifier string) (openAITokenResponse, error) {
	values := url.Values{}
	values.Set("grant_type", "authorization_code")
	values.Set("client_id", cfg.ChatGPTClientID)
	values.Set("redirect_uri", strings.TrimRight(cfg.ChatGPTAuthIssuer, "/")+"/deviceauth/callback")
	values.Set("code", code)
	values.Set("code_verifier", codeVerifier)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.ChatGPTAuthIssuer, "/")+"/oauth/token", strings.NewReader(values.Encode()))
	if err != nil {
		return openAITokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return openAITokenResponse{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return openAITokenResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openAITokenResponse{}, &providerHTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	var parsed openAITokenResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return openAITokenResponse{}, err
	}
	return parsed, nil
}

func (s *Server) chatGPTCodexAccount(ctx context.Context, userID string) (domain.OpenAIAccount, error) {
	account, err := s.store.GetOpenAIAccount(ctx, userID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.OpenAIAccount{}, errChatGPTRelinkRequired
		}
		return domain.OpenAIAccount{}, err
	}
	if account.AuthProvider != openAIAccountProviderChatGPTCodex || strings.TrimSpace(account.AccessToken) == "" {
		return domain.OpenAIAccount{}, errChatGPTRelinkRequired
	}
	if account.ExpiresAt != nil && time.Now().UTC().After(account.ExpiresAt.Add(-1*time.Minute)) {
		return s.refreshChatGPTCodexAccount(ctx, account)
	}
	return account, nil
}

func (s *Server) refreshChatGPTCodexAccount(ctx context.Context, account domain.OpenAIAccount) (domain.OpenAIAccount, error) {
	if strings.TrimSpace(account.RefreshToken) == "" {
		_ = s.store.DeleteOpenAIAccount(ctx, account.UserID)
		return domain.OpenAIAccount{}, errChatGPTRelinkRequired
	}
	cfg := s.assistantAI
	cfg.normalize()
	tokens, err := s.requestChatGPTTokenRefresh(ctx, cfg, account.RefreshToken)
	if err != nil {
		var refreshErr *chatGPTRefreshError
		if errors.As(err, &refreshErr) && refreshErr.permanent {
			_ = s.store.DeleteOpenAIAccount(ctx, account.UserID)
			return domain.OpenAIAccount{}, errChatGPTRelinkRequired
		}
		return domain.OpenAIAccount{}, err
	}
	refreshed, err := chatGPTAccountFromTokenResponse(account.UserID, tokens, &account)
	if err != nil {
		return domain.OpenAIAccount{}, err
	}
	if account.ProviderUserID != "" && refreshed.ProviderUserID != "" && refreshed.ProviderUserID != account.ProviderUserID {
		_ = s.store.DeleteOpenAIAccount(ctx, account.UserID)
		return domain.OpenAIAccount{}, errChatGPTRelinkRequired
	}
	if err := s.store.UpsertOpenAIAccount(ctx, refreshed); err != nil {
		return domain.OpenAIAccount{}, err
	}
	return refreshed, nil
}

func (s *Server) requestChatGPTTokenRefresh(ctx context.Context, cfg AssistantAIConfig, refreshToken string) (openAITokenResponse, error) {
	endpoint := strings.TrimRight(cfg.ChatGPTAuthIssuer, "/") + "/oauth/token"
	body := map[string]string{
		"client_id":     cfg.ChatGPTClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return openAITokenResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return openAITokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return openAITokenResponse{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return openAITokenResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openAITokenResponse{}, &chatGPTRefreshError{
			statusCode: response.StatusCode,
			body:       strings.TrimSpace(string(data)),
			permanent:  response.StatusCode == http.StatusUnauthorized,
		}
	}
	var parsed openAITokenResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return openAITokenResponse{}, err
	}
	return parsed, nil
}

func chatGPTAccountFromTokenResponse(userID string, tokens openAITokenResponse, previous *domain.OpenAIAccount) (domain.OpenAIAccount, error) {
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return domain.OpenAIAccount{}, errors.New("chatgpt token response is missing access token")
	}
	claims := chatGPTClaimsFromToken(tokens.IDToken)
	if claims.Auth.ChatGPTAccountID == "" {
		accessClaims := chatGPTClaimsFromToken(tokens.AccessToken)
		if accessClaims.Auth.ChatGPTAccountID != "" || accessClaims.Auth.ChatGPTUserID != "" || accessClaims.Auth.UserID != "" {
			claims = accessClaims
		}
	}
	providerUserID := strings.TrimSpace(claims.Auth.ChatGPTAccountID)
	if providerUserID == "" {
		providerUserID = strings.TrimSpace(claims.Auth.ChatGPTUserID)
	}
	if providerUserID == "" {
		providerUserID = strings.TrimSpace(claims.Auth.UserID)
	}

	now := time.Now().UTC()
	var expiresAt *time.Time
	if tokens.ExpiresIn > 0 {
		value := now.Add(time.Duration(tokens.ExpiresIn) * time.Second)
		expiresAt = &value
	} else if claims.Exp > 0 {
		value := time.Unix(claims.Exp, 0).UTC()
		expiresAt = &value
	} else if previous != nil {
		expiresAt = previous.ExpiresAt
	}

	refreshToken := strings.TrimSpace(tokens.RefreshToken)
	if refreshToken == "" && previous != nil {
		refreshToken = previous.RefreshToken
	}
	tokenType := strings.TrimSpace(tokens.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	scope := strings.TrimSpace(tokens.Scope)
	if scope == "" && previous != nil {
		scope = previous.Scope
	}
	if scope == "" {
		scope = openAIAccountProviderChatGPTCodex
	}
	createdAt := now
	if previous != nil && !previous.CreatedAt.IsZero() {
		createdAt = previous.CreatedAt
	}
	if providerUserID == "" && previous != nil {
		providerUserID = previous.ProviderUserID
	}
	planType := strings.TrimSpace(claims.Auth.ChatGPTPlanType)
	if planType == "" && previous != nil {
		planType = previous.ChatGPTPlanType
	}

	return domain.OpenAIAccount{
		UserID:          userID,
		ProviderUserID:  providerUserID,
		AuthProvider:    openAIAccountProviderChatGPTCodex,
		ChatGPTPlanType: planType,
		AccessToken:     strings.TrimSpace(tokens.AccessToken),
		RefreshToken:    refreshToken,
		TokenType:       tokenType,
		Scope:           scope,
		ExpiresAt:       expiresAt,
		CreatedAt:       createdAt,
		UpdatedAt:       now,
	}, nil
}

func chatGPTClaimsFromToken(token string) chatGPTTokenClaims {
	var claims chatGPTTokenClaims
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return claims
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims
	}
	_ = json.Unmarshal(payload, &claims)
	return claims
}

func postJSONToEndpoint(ctx context.Context, endpoint string, body any, headers map[string]string, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if strings.TrimSpace(value) != "" {
			request.Header.Set(key, value)
		}
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &providerHTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
