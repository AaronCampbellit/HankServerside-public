package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestChatGPTDeviceAuthStartAndPolling(t *testing.T) {
	cases := []struct {
		name       string
		wantLinked bool
		wantState  string
	}{
		{name: "polling success links account", wantLinked: true, wantState: "linked"},
		{name: "polling timeout exposes failed state", wantLinked: false, wantState: "failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := storeForTest(t)
			defer db.Close()

			now := time.Now().UTC()
			user := domain.User{ID: "usr_" + tc.wantState, Email: tc.wantState + "@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
			sessionToken := "session-" + tc.wantState
			session := domain.AppSession{ID: "sess_" + tc.wantState, UserID: user.ID, TokenHash: hashToken(sessionToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
			must(t, db.CreateUser(ctx, user))
			must(t, db.CreateSession(ctx, session))

			var pollCalls atomic.Int32
			authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/accounts/deviceauth/usercode":
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode usercode body: %v", err)
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					if body["client_id"] != "test-client" {
						t.Errorf("client_id = %q", body["client_id"])
					}
					expiresIn := 10
					if !tc.wantLinked {
						expiresIn = 1
					}
					writeJSON(w, http.StatusOK, map[string]any{
						"device_auth_id": "device-" + tc.wantState,
						"user_code":      "HANK-" + tc.wantState,
						"interval":       "1",
						"expires_in":     expiresIn,
					})
				case "/api/accounts/deviceauth/token":
					pollCalls.Add(1)
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode poll body: %v", err)
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					if body["device_auth_id"] != "device-"+tc.wantState || body["user_code"] != "HANK-"+tc.wantState {
						t.Errorf("poll body = %#v", body)
					}
					if !tc.wantLinked {
						http.Error(w, "pending", http.StatusForbidden)
						return
					}
					writeJSON(w, http.StatusOK, map[string]any{
						"authorization_code": "auth-code",
						"code_challenge":     "challenge",
						"code_verifier":      "verifier",
					})
				case "/oauth/token":
					if err := r.ParseForm(); err != nil {
						t.Errorf("ParseForm: %v", err)
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "auth-code" || r.Form.Get("code_verifier") != "verifier" {
						t.Errorf("token exchange form = %#v", r.Form)
					}
					writeJSON(w, http.StatusOK, map[string]any{
						"access_token":  "chatgpt-access",
						"refresh_token": "chatgpt-refresh",
						"id_token":      fakeChatGPTIDToken(t, "workspace-123", "plus", now.Add(time.Hour)),
						"token_type":    "Bearer",
						"scope":         "chatgpt_codex",
						"expires_in":    3600,
					})
				default:
					http.NotFound(w, r)
				}
			}))
			defer authServer.Close()

			server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
			server.ConfigureAssistantAI(AssistantAIConfig{
				Provider:              assistantProviderChatGPTCodex,
				ChatGPTOAuthEnabled:   true,
				ChatGPTAuthIssuer:     authServer.URL,
				ChatGPTBackendBaseURL: "https://chatgpt.invalid/backend-api/codex",
				ChatGPTClientID:       "test-client",
			})
			testServer := httptest.NewServer(server.http.Handler)
			defer testServer.Close()

			var start chatGPTDeviceAuthStartResponse
			requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/oauth/openai/start", nil, &start)
			if start.AuthMode != chatGPTDeviceAuthMode {
				t.Fatalf("auth_mode = %q", start.AuthMode)
			}
			if start.VerificationURL != authServer.URL+"/codex/device" {
				t.Fatalf("verification_url = %q", start.VerificationURL)
			}
			if start.UserCode != "HANK-"+tc.wantState {
				t.Fatalf("user_code = %q", start.UserCode)
			}

			status := waitForChatGPTDeviceStatus(t, testServer, sessionToken, tc.wantState)
			if status.Linked != tc.wantLinked {
				t.Fatalf("linked = %v, want %v; status=%#v", status.Linked, tc.wantLinked, status)
			}
			if status.Pending == nil || status.Pending.State != tc.wantState {
				t.Fatalf("pending state = %#v, want %q", status.Pending, tc.wantState)
			}
			if pollCalls.Load() == 0 {
				t.Fatal("poll endpoint was not called")
			}
			if tc.wantLinked {
				if status.ChatGPTPlanType != "plus" {
					t.Fatalf("plan = %q", status.ChatGPTPlanType)
				}
				account, err := db.GetOpenAIAccount(ctx, user.ID)
				if err != nil {
					t.Fatal(err)
				}
				if account.ProviderUserID != "workspace-123" || account.AuthProvider != openAIAccountProviderChatGPTCodex {
					t.Fatalf("account = %#v", account)
				}
			}
		})
	}
}

func waitForChatGPTDeviceStatus(t *testing.T, server *httptest.Server, sessionToken string, state string) openAIAccountStatusResponse {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		var status openAIAccountStatusResponse
		requestJSON(t, server, sessionToken, http.MethodGet, "/v1/oauth/openai/status", nil, &status)
		if status.Pending != nil && status.Pending.State == state {
			return status
		}
		time.Sleep(100 * time.Millisecond)
	}
	var status openAIAccountStatusResponse
	requestJSON(t, server, sessionToken, http.MethodGet, "/v1/oauth/openai/status", nil, &status)
	t.Fatalf("timed out waiting for state %q; status=%#v", state, status)
	return status
}

func fakeChatGPTIDToken(t *testing.T, accountID string, planType string, expiresAt time.Time) string {
	t.Helper()
	return fakeChatGPTJWT(t, map[string]any{
		"exp": expiresAt.Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_plan_type":  planType,
			"chatgpt_user_id":    "chatgpt-user",
		},
	})
}

func fakeChatGPTJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}
