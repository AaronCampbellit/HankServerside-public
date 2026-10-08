package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

func TestAgentCredentialHTTPRotationRequiresReplacementConfirmation(t *testing.T) {
	db, server, home, agent, adminToken, oldRaw := credentialHTTPFixture(t)
	ctx := context.Background()
	newRaw := strings.Repeat("b", 64)
	newHash := hashToken(newRaw)

	response, data := credentialAgentRequest(t, server, agent.ID, oldRaw, http.MethodPost, "/v1/agent/credentials/rotate", map[string]any{"credential_hash": newHash})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("rotate status=%d body=%s", response.StatusCode, data)
	}
	var rotation domain.AgentCredentialState
	if err := json.Unmarshal(data, &rotation); err != nil {
		t.Fatal(err)
	}
	if rotation.CredentialID == "" || rotation.Generation != 2 || rotation.ConfirmBy == nil || bytes.Contains(data, []byte(newRaw)) {
		t.Fatalf("rotation response = %s", data)
	}
	if _, err := db.ValidateAgentToken(ctx, hashToken(oldRaw)); err != nil {
		t.Fatalf("old credential unavailable during overlap: %v", err)
	}
	if _, err := db.ValidateAgentToken(ctx, newHash); err != nil {
		t.Fatalf("replacement credential unavailable during overlap: %v", err)
	}

	response, _ = credentialAgentRequest(t, server, agent.ID, oldRaw, http.MethodPost, "/v1/agent/credentials/confirm", map[string]any{"credential_id": rotation.CredentialID})
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("old credential confirmation status=%d, want %d", response.StatusCode, http.StatusNotFound)
	}
	response, data = credentialAgentRequest(t, server, agent.ID, newRaw, http.MethodPost, "/v1/agent/credentials/confirm", map[string]any{"credential_id": rotation.CredentialID})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replacement confirmation status=%d body=%s", response.StatusCode, data)
	}
	if _, err := db.ValidateAgentToken(ctx, hashToken(oldRaw)); err == nil {
		t.Fatal("old credential remained valid after confirmation")
	}
	if _, err := db.ValidateAgentToken(ctx, newHash); err != nil {
		t.Fatalf("replacement credential after confirmation: %v", err)
	}

	response, data = credentialAdminRequest(t, server, adminToken, http.MethodPost, "/v1/home/agents/"+agent.ID+"/credentials/rotation-request")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin rotation request status=%d body=%s", response.StatusCode, data)
	}
	response, data = credentialAgentRequest(t, server, agent.ID, newRaw, http.MethodGet, "/v1/agent/credentials/status", nil)
	if response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte(`"rotation_requested":true`)) {
		t.Fatalf("forced rotation status=%d body=%s", response.StatusCode, data)
	}

	server.router.RegisterAgent(home.ID, agent, nil, nil, AgentTypeWorker, nil)
	response, data = credentialAdminRequest(t, server, adminToken, http.MethodDelete, "/v1/home/agents/"+agent.ID+"/credentials")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin revocation status=%d body=%s", response.StatusCode, data)
	}
	if _, connected := server.router.ResolveAgent(home.ID, agent.ID); connected {
		t.Fatal("revoked agent remained in live routing")
	}
	if _, err := db.ValidateAgentToken(ctx, newHash); err == nil {
		t.Fatal("replacement remained valid after administrator revocation")
	}
}

func credentialHTTPFixture(t *testing.T) (*store.Store, *Server, domain.Home, domain.Agent, string, string) {
	t.Helper()
	db := storeForTest(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	admin := domain.User{ID: "usr_credential_http", Email: "credential-http@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_credential_http", UserID: admin.ID, Name: "Credential HTTP", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "linux_credential_http", HomeID: home.ID, Name: "Linux", Status: domain.AgentStatusOffline, AgentType: AgentTypeWorker, CreatedAt: now, UpdatedAt: now}
	oldRaw := strings.Repeat("a", 64)
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: admin.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_credential_http", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(oldRaw), Generation: 1, ActivatedAt: &now, ConfirmedAt: &now, CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_credential_http", UserID: admin.ID, TokenHash: hashToken("credential-http-admin-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	return db, NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))), home, agent, "credential-http-admin-token", oldRaw
}

func credentialAgentRequest(t *testing.T, server *Server, agentID, rawCredential, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	httpServer := httptest.NewServer(server.http.Handler)
	t.Cleanup(httpServer.Close)
	return credentialRequest(t, httpServer.URL, method, path, body, map[string]string{"Authorization": "Bearer " + rawCredential, "X-Hank-Agent-ID": agentID})
}

func credentialAdminRequest(t *testing.T, server *Server, rawSession, method, path string) (*http.Response, []byte) {
	t.Helper()
	httpServer := httptest.NewServer(server.http.Handler)
	t.Cleanup(httpServer.Close)
	return credentialRequest(t, httpServer.URL, method, path, nil, map[string]string{"Authorization": "Bearer " + rawSession})
}

func credentialRequest(t *testing.T, baseURL, method, path string, body any, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, baseURL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, data
}
