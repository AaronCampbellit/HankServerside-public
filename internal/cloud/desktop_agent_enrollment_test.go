package cloud

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestAgentDesktopEnrollmentExposesOnlyOwnPublicTrustMaterial(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user := domain.User{ID: "usr_agent_enroll", Email: "enroll@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_agent_enroll", UserID: user.ID, Name: "Enroll", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_windows_enroll", HomeID: home.ID, Name: "Windows", Status: domain.AgentStatusOnline, CreatedAt: now, UpdatedAt: now}
	token := "agent-enrollment-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_enroll", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(token), CreatedAt: now}))
	root := domain.DesktopTrustRoot{HomeID: home.ID, Generation: 1, Algorithm: domain.DesktopTrustAlgorithm, PublicKeySPKI: []byte("public-root"), Fingerprint: "root-fingerprint", RecoveryEnvelope: []byte("private-recovery-envelope"), CreatedAt: now}
	operator := domain.DesktopIdentity{ID: "did_operator_enroll", HomeID: home.ID, IdentityType: domain.DesktopIdentityOperatorDevice, UserID: user.ID, DeviceID: "browser", PublicKeySPKI: []byte("operator-public"), Certificate: []byte("operator-certificate"), Fingerprint: "operator-fingerprint", Capabilities: []string{"endpoint.approve"}, TrustRootGeneration: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	must(t, db.BootstrapDesktopTrust(ctx, root, operator))

	server := httptest.NewServer(NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))).http.Handler)
	defer server.Close()
	do := func(method string, body []byte, agentID, credential string) *http.Response {
		request, err := http.NewRequest(method, server.URL+"/v1/agent/desktop-enrollment", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set("X-Hank-Agent-ID", agentID)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	// Legacy bootstrap requires migration; server-managed trust uses encrypted keys.
	legacy := do(http.MethodGet, nil, agent.ID, token)
	legacy.Body.Close()
	if legacy.StatusCode != http.StatusConflict {
		t.Fatalf("legacy trust status = %d", legacy.StatusCode)
	}
	must(t, db.ConfigureSecretEncryption("desktop-enrollment-test-key"))
	managed, _, _, err := db.EnsureServerManagedDesktopTrust(ctx, home.ID, now)
	must(t, err)
	response := do(http.MethodGet, nil, agent.ID, token)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || bytes.Contains(data, []byte("recovery_envelope")) || bytes.Contains(data, []byte(managed.EncryptedPrivateKey)) || bytes.Contains(data, []byte("operator-fingerprint")) {
		t.Fatalf("public enrollment response status=%d body=%s", response.StatusCode, data)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"identity_type": "endpoint", "identity_id": "dep_windows", "agent_id": agent.ID,
		"public_key_spki": base64.RawURLEncoding.EncodeToString(spki), "platform": "windows",
		"capabilities": []string{"desktop.view", "desktop.control", "desktop.clipboard.read", "desktop.clipboard.write", "desktop.elevate", "desktop.secure_desktop", "desktop.unattended"},
		"created_at":   now, "expires_at": now.AddDate(1, 0, 0),
	})
	response = do(http.MethodPost, payload, agent.ID, token)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("pending enrollment status=%d", response.StatusCode)
	}
	if _, err := db.GetDesktopPendingEnrollment(ctx, home.ID, agent.ID, now); err != nil {
		t.Fatalf("pending enrollment not stored: %v", err)
	}
	response = do(http.MethodGet, nil, "agent_other", token)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cross-agent credential status=%d", response.StatusCode)
	}
}
