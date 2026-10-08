package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

var testLinuxInstallerTokenPattern = regexp.MustCompile(`/install/linux/([0-9a-f]{64})`)

func TestLinuxEnrollmentHTTPFlowIsAdminOnlySingleUseAndSecretSafe(t *testing.T) {
	db := storeForTest(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	admin := domain.User{ID: "usr_linux_http_admin", Email: "linux-http-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_linux_http_member", Email: "linux-http-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_linux_http", UserID: admin.ID, Name: "Linux HTTP", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: admin.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_linux_http_admin", UserID: admin.ID, TokenHash: hashToken("linux-http-admin-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_linux_http_member", UserID: member.ID, TokenHash: hashToken("linux-http-member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := httptest.NewServer(NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))).http.Handler)
	defer server.Close()

	doAdmin := func(token, method, path string, body any) (*http.Response, []byte) {
		t.Helper()
		var requestBody io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			requestBody = bytes.NewReader(encoded)
		}
		request, err := http.NewRequest(method, server.URL+path, requestBody)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		request.Header.Set("Content-Type", "application/json")
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

	response, _ := doAdmin("linux-http-member-token", http.MethodPost, "/v1/home/agent-enrollments/linux", map[string]any{"name_hint": "demo"})
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("member creation status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	response, data := doAdmin("linux-http-admin-token", http.MethodPost, "/v1/home/agent-enrollments/linux", map[string]any{"name_hint": "demo"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("creation status = %d body=%s", response.StatusCode, data)
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Pragma") != "no-cache" {
		t.Fatalf("creation cache=%q pragma=%q", response.Header.Get("Cache-Control"), response.Header.Get("Pragma"))
	}
	var created struct {
		ID             string    `json:"id"`
		InstallCommand string    `json:"install_command"`
		ExpiresAt      time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &created); err != nil {
		t.Fatal(err)
	}
	match := testLinuxInstallerTokenPattern.FindStringSubmatch(created.InstallCommand)
	if len(match) != 2 || created.ID == "" || !created.ExpiresAt.Equal(now.Add(linuxEnrollmentTTL)) && created.ExpiresAt.Sub(now.Add(linuxEnrollmentTTL)) > time.Second {
		t.Fatalf("invalid creation response: %#v", created)
	}
	raw := match[1]

	response, listed := doAdmin("linux-http-admin-token", http.MethodGet, "/v1/home/agent-enrollments/linux", nil)
	if response.StatusCode != http.StatusOK || bytes.Contains(listed, []byte(raw)) || bytes.Contains(listed, []byte(created.InstallCommand)) {
		t.Fatalf("unsafe list status=%d body=%s", response.StatusCode, listed)
	}

	response, script := doAdmin("", http.MethodGet, "/install/linux/"+raw, nil)
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Pragma") != "no-cache" {
		t.Fatalf("installer status=%d cache=%q pragma=%q body=%s", response.StatusCode, response.Header.Get("Cache-Control"), response.Header.Get("Pragma"), script)
	}
	if !bytes.Contains(script, []byte("--enrollment-token-stdin")) || bytes.Contains(script, []byte("set -x")) {
		t.Fatalf("unsafe installer body=%s", script)
	}

	credentialHash := strings.Repeat("c", 64)
	consumeBody := map[string]any{"device_id": "linux_0123456789abcdef", "name": "demo", "agent_type": "worker", "platform": "linux", "architecture": "amd64", "os_version": "Ubuntu 24.04", "agent_version": "0.2.0", "credential_hash": credentialHash}
	encoded, err := json.Marshal(consumeBody)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent/enrollments/linux/consume", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Hank-Enrollment "+raw)
	request.Header.Set("Content-Type", "application/json")
	consumeResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	consumeData, _ := io.ReadAll(consumeResponse.Body)
	consumeResponse.Body.Close()
	if consumeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("consume status=%d body=%s", consumeResponse.StatusCode, consumeData)
	}
	var grant struct {
		AgentID      string `json:"agent_id"`
		CredentialID string `json:"credential_id"`
	}
	if err := json.Unmarshal(consumeData, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.AgentID != "linux_0123456789abcdef" || grant.CredentialID == "" {
		t.Fatalf("invalid consume grant: %#v", grant)
	}
	if _, err := db.ValidateAgentToken(ctx, credentialHash); err != nil {
		t.Fatalf("submitted credential hash not active: %v", err)
	}

	replay, err := http.NewRequest(http.MethodPost, server.URL+"/v1/agent/enrollments/linux/consume", bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	replay.Header.Set("Authorization", "Hank-Enrollment "+raw)
	replay.Header.Set("Content-Type", "application/json")
	replayResponse, err := http.DefaultClient.Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, replayResponse.Body)
	replayResponse.Body.Close()
	if replayResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("replay status=%d, want %d", replayResponse.StatusCode, http.StatusNotFound)
	}
}

func TestRenderLinuxInstallerKeepsTokenOutOfAgentArguments(t *testing.T) {
	raw := strings.Repeat("b", 64)
	script := renderLinuxInstallerScript("https://hank.example", raw)
	if !strings.Contains(script, "--enrollment-token-stdin") {
		t.Fatal("missing stdin enrollment")
	}
	if strings.Contains(script, "--enrollment-token "+raw) {
		t.Fatal("token placed in agent arguments")
	}
	if strings.Contains(script, "set -x") {
		t.Fatal("unsafe trace")
	}
	if !strings.Contains(script, `-o "$tmp/hankagent-linux-$arch"`) || !strings.Contains(script, `(cd "$tmp" && sha256sum -c hankagent-linux-$arch.sha256)`) {
		t.Fatal("downloaded binary and checksum filenames must agree")
	}
}
