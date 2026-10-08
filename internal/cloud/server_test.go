package cloud

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/crypto/bcrypt"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
	"github.com/dropfile/HankServerside/internal/store"
	"github.com/dropfile/HankServerside/internal/testutil"
)

func TestAppCommandRoutesToAgentAndBack(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_1", Email: "user@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Test Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_1", HomeID: home.ID, Name: "Test Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	agentRawToken := "agent-token"
	sessionRawToken := "session-token"
	agentToken := domain.AgentToken{ID: "agtok_1", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(agentRawToken), CreatedAt: now}
	session := domain.AppSession{ID: "sess_1", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, agentToken))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	agentConn, _, err := websocket.Dial(ctx, wsURL(testServer.URL, "/ws/agent"), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":   []string{"Bearer " + agentRawToken},
			"X-Hank-Agent-ID": []string{agent.ID},
		},
	})
	if err != nil {
		t.Fatalf("agent websocket dial: %v", err)
	}
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agent.ID, "", protocol.AgentRegister{AgentID: agent.ID, HomeName: home.Name, AgentType: "home-agent"})
	if err != nil {
		t.Fatalf("NewEnvelope register: %v", err)
	}
	if err := wsjson.Write(ctx, agentConn, register); err != nil {
		t.Fatalf("agent register write: %v", err)
	}

	var registered protocol.Envelope
	if err := wsjson.Read(ctx, agentConn, &registered); err != nil {
		t.Fatalf("agent read registered: %v", err)
	}
	if registered.Type != protocol.TypeAgentRegistered {
		t.Fatalf("registered type = %q, want %q", registered.Type, protocol.TypeAgentRegistered)
	}

	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			if envelope.Type != protocol.TypeCloudCommand {
				continue
			}

			command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
			if err != nil {
				return
			}
			if command.Command != protocol.CommandSystemPing {
				return
			}

			response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agent.ID, home.ID, protocol.SystemPingResponse{
				Message: "pong",
				Time:    time.Now().UTC(),
			})
			if err != nil {
				return
			}
			_ = wsjson.Write(ctx, agentConn, response)
		}
	}()

	appConn, _, err := appWebSocketDial(ctx, testServer, sessionRawToken)
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	command, err := protocol.NewEnvelope(protocol.TypeAppCommand, "req_1", "", "", protocol.RoutedCommand{
		Command: protocol.CommandSystemPing,
	})
	if err != nil {
		t.Fatalf("NewEnvelope app command: %v", err)
	}
	if err := wsjson.Write(ctx, appConn, command); err != nil {
		t.Fatalf("app command write: %v", err)
	}

	var response protocol.Envelope
	if err := wsjson.Read(ctx, appConn, &response); err != nil {
		t.Fatalf("app response read: %v", err)
	}
	if response.Type != protocol.TypeAppResponse {
		t.Fatalf("app response type = %q, want %q", response.Type, protocol.TypeAppResponse)
	}

	payload, err := protocol.DecodePayload[protocol.SystemPingResponse](response)
	if err != nil {
		t.Fatalf("DecodePayload response: %v", err)
	}
	if payload.Message != "pong" {
		t.Fatalf("response message = %q, want %q", payload.Message, "pong")
	}
}

func TestAppWebSocketTicketCanBeIssuedAndConsumed(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, _, _, sessionRawToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			if envelope.Type != protocol.TypeCloudCommand {
				continue
			}

			command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
			if err != nil || command.Command != protocol.CommandSystemPing {
				return
			}

			response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, envelope.AgentID, envelope.HomeID, protocol.SystemPingResponse{
				Message: "pong",
				Time:    time.Now().UTC(),
			})
			if err != nil {
				return
			}
			_ = wsjson.Write(ctx, agentConn, response)
		}
	}()

	var ticketResponse struct {
		Ticket        string    `json:"ticket"`
		ExpiresAt     time.Time `json:"expires_at"`
		WebSocketPath string    `json:"websocket_path"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodPost, "/v1/ws/app-ticket", nil, &ticketResponse)

	if ticketResponse.Ticket == "" {
		t.Fatal("expected app websocket ticket")
	}
	if ticketResponse.WebSocketPath == "" {
		t.Fatal("expected websocket path in ticket response")
	}

	appConn, _, err := websocket.Dial(ctx, wsURL(testServer.URL, ticketResponse.WebSocketPath), nil)
	if err != nil {
		t.Fatalf("ticket websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	command, err := protocol.NewEnvelope(protocol.TypeAppCommand, "req_ticket_1", "", "", protocol.RoutedCommand{
		Command: protocol.CommandSystemPing,
	})
	if err != nil {
		t.Fatalf("NewEnvelope app command: %v", err)
	}
	if err := wsjson.Write(ctx, appConn, command); err != nil {
		t.Fatalf("ticket app command write: %v", err)
	}

	var response protocol.Envelope
	if err := wsjson.Read(ctx, appConn, &response); err != nil {
		t.Fatalf("ticket app response read: %v", err)
	}
	if response.Type != protocol.TypeAppResponse {
		t.Fatalf("app response type = %q, want %q", response.Type, protocol.TypeAppResponse)
	}

	payload, err := protocol.DecodePayload[protocol.SystemPingResponse](response)
	if err != nil {
		t.Fatalf("DecodePayload response: %v", err)
	}
	if payload.Message != "pong" {
		t.Fatalf("response message = %q, want %q", payload.Message, "pong")
	}
}

func TestRequestIDHeaderIsEchoed(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	request, err := http.NewRequest(http.MethodGet, testServer.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("healthz request: %v", err)
	}
	request.Header.Set("X-Request-ID", "req_manual")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("healthz response: %v", err)
	}
	defer response.Body.Close()

	if got := response.Header.Get("X-Request-ID"); got != "req_manual" {
		t.Fatalf("X-Request-ID = %q, want %q", got, "req_manual")
	}
}

func TestLoginPageIsServed(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response, err := http.Get(testServer.URL + "/")
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("login content-type = %q", contentType)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("login body read: %v", err)
	}
	assertReactShell(t, "/", string(body))
}

func TestRegistrationDisabledAfterFirstAdminBootstrap(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	firstBody := strings.NewReader(`{"email":"first@example.com","password":"change-me-123"}`)
	firstRequest, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/auth/register", firstBody)
	if err != nil {
		t.Fatalf("first register request: %v", err)
	}
	firstRequest.Header.Set("Content-Type", "application/json")

	firstResponse, err := http.DefaultClient.Do(firstRequest)
	if err != nil {
		t.Fatalf("first register response: %v", err)
	}
	defer firstResponse.Body.Close()
	if firstResponse.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(firstResponse.Body)
		t.Fatalf("first register status = %d, want %d body=%s", firstResponse.StatusCode, http.StatusCreated, string(data))
	}
	if _, err := db.GetSingletonHomeForUser(ctx, mustRegisteredUserID(t, firstResponse)); err != nil {
		t.Fatalf("first registered user should have singleton home membership: %v", err)
	}

	secondBody := strings.NewReader(`{"email":"second@example.com","password":"change-me-123"}`)
	secondRequest, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/auth/register", secondBody)
	if err != nil {
		t.Fatalf("second register request: %v", err)
	}
	secondRequest.Header.Set("Content-Type", "application/json")

	secondResponse, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatalf("second register response: %v", err)
	}
	defer secondResponse.Body.Close()
	if secondResponse.StatusCode != http.StatusForbidden {
		data, _ := io.ReadAll(secondResponse.Body)
		t.Fatalf("second register status = %d, want %d body=%s", secondResponse.StatusCode, http.StatusForbidden, string(data))
	}
	for _, cookie := range secondResponse.Cookies() {
		if cookie.Name == sessionCookieName && cookie.Value != "" {
			t.Fatalf("second registration unexpectedly set a session cookie")
		}
	}
	if _, err := db.GetUserByEmail(ctx, "second@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second user lookup error = %v, want ErrNotFound", err)
	}
}

func TestDashboardPagesRedirectWhenUnauthenticated(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	paths := []string{
		"/dashboard",
		"/dashboard/hank",
		"/dashboard/home-assistant",
		"/dashboard/profile-notes",
		"/dashboard/file-server",
		"/dashboard/notifications/event",
		"/dashboard/settings",
		"/dashboard/settings/home",
		"/dashboard/settings/quick-links",
		"/dashboard/settings/notifications",
		"/dashboard/settings/people",
		"/dashboard/settings/connections",
		"/dashboard/settings/ai",
		"/dashboard/settings/backups",
		"/dashboard/settings/recovery",
		"/dashboard/settings/apps",
		"/dashboard/settings/logs",
		"/dashboard/settings/join-home",
	}

	for _, routePath := range paths {
		response, err := client.Get(testServer.URL + routePath)
		if err != nil {
			t.Fatalf("%s request: %v", routePath, err)
		}
		response.Body.Close()

		if response.StatusCode != http.StatusSeeOther {
			t.Fatalf("%s status = %d, want %d", routePath, response.StatusCode, http.StatusSeeOther)
		}
		if location := response.Header.Get("Location"); location != "/" {
			t.Fatalf("%s redirect location = %q, want %q", routePath, location, "/")
		}
	}

	response, err := client.Get(testServer.URL + "/dashboard/not-a-route")
	if err != nil {
		t.Fatalf("unknown dashboard route request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown dashboard route status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestLegacySettingsPaneRoutesRedirectToCanonicalRoutes(t *testing.T) {
	t.Parallel()

	routes := map[string]string{
		"/dashboard/settings/people-pane":      "/dashboard/settings/people",
		"/dashboard/settings/connections-pane": "/dashboard/settings/connections",
		"/dashboard/settings/ai-pane":          "/dashboard/settings/ai",
		"/dashboard/settings/backups-pane":     "/dashboard/settings/backups",
		"/dashboard/settings/recovery-pane":    "/dashboard/settings/recovery",
		"/dashboard/settings/apps-pane":        "/dashboard/settings/apps",
		"/dashboard/settings/logs-pane":        "/dashboard/settings/logs",
		"/dashboard/settings/join-home-pane":   "/dashboard/settings/join-home",
	}
	for from, to := range routes {
		request := httptest.NewRequest(http.MethodGet, from, nil)
		response := httptest.NewRecorder()
		redirectToSettingsRoute(to)(response, request)
		if response.Code != http.StatusMovedPermanently {
			t.Fatalf("%s status = %d, want %d", from, response.Code, http.StatusMovedPermanently)
		}
		if location := response.Header().Get("Location"); location != to {
			t.Fatalf("%s redirect location = %q, want %q", from, location, to)
		}
	}
}

func TestDashboardPagesRequireHomeMembership(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_admin", Email: "admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_member", Email: "member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	outsider := domain.User{ID: "usr_outsider", Email: "outsider@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: admin.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}

	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateUser(ctx, outsider))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{
		HomeID:    home.ID,
		UserID:    member.ID,
		Role:      domain.HomeRoleMember,
		CreatedAt: now,
		UpdatedAt: now,
	}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_member", UserID: member.ID, TokenHash: hashToken("member-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_outsider", UserID: outsider.ID, TokenHash: hashToken("outsider-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	normalPages := []string{
		"/dashboard",
		"/dashboard/hank",
		"/dashboard/home-assistant",
		"/dashboard/profile-notes",
		"/dashboard/file-server",
		"/dashboard/notifications/event",
		"/dashboard/settings/home",
		"/dashboard/settings/quick-links",
		"/dashboard/settings/notifications",
		"/dashboard/settings/people",
		"/dashboard/settings/connections",
		"/dashboard/settings/ai",
	}

	for _, routePath := range normalPages {
		response := requestDashboardPage(t, testServer, routePath, "member-token")
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("member %s status = %d, want %d body=%s", routePath, response.StatusCode, http.StatusOK, string(data))
		}
		response.Body.Close()
	}

	for _, routePath := range normalPages {
		response := requestDashboardPage(t, testServer, routePath, "outsider-token")
		if response.StatusCode != http.StatusForbidden {
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("outsider %s status = %d, want %d body=%s", routePath, response.StatusCode, http.StatusForbidden, string(data))
		}
		response.Body.Close()
	}

	legacyPaths := []string{
		"/dashboard/home-users",
		"/dashboard/service-profiles",
		"/dashboard/sync-status",
		"/dashboard/storage",
		"/dashboard/assistant-settings",
		"/dashboard/accept-invitation",
	}
	for _, routePath := range legacyPaths {
		response := requestDashboardPage(t, testServer, routePath, "member-token")
		if response.StatusCode != http.StatusNotFound {
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("member legacy %s status = %d, want %d body=%s", routePath, response.StatusCode, http.StatusNotFound, string(data))
		}
		response.Body.Close()
	}

	joinResponse := requestDashboardPage(t, testServer, "/dashboard/settings/join-home", "outsider-token")
	if joinResponse.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(joinResponse.Body)
		joinResponse.Body.Close()
		t.Fatalf("outsider join-home status = %d, want %d body=%s", joinResponse.StatusCode, http.StatusOK, string(data))
	}
	joinResponse.Body.Close()

	settingsResponse := requestDashboardPage(t, testServer, "/dashboard/settings", "outsider-token")
	if settingsResponse.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(settingsResponse.Body)
		settingsResponse.Body.Close()
		t.Fatalf("outsider settings status = %d, want %d body=%s", settingsResponse.StatusCode, http.StatusOK, string(data))
	}
	settingsResponse.Body.Close()

	unknownResponse := requestDashboardPage(t, testServer, "/dashboard/not-a-route", "member-token")
	if unknownResponse.StatusCode != http.StatusNotFound {
		data, _ := io.ReadAll(unknownResponse.Body)
		unknownResponse.Body.Close()
		t.Fatalf("member unknown dashboard route status = %d, want %d body=%s", unknownResponse.StatusCode, http.StatusNotFound, string(data))
	}
	unknownResponse.Body.Close()

	storageResponse := requestDashboardPage(t, testServer, "/dashboard/settings/backups", "member-token")
	if storageResponse.StatusCode != http.StatusForbidden {
		data, _ := io.ReadAll(storageResponse.Body)
		storageResponse.Body.Close()
		t.Fatalf("member storage status = %d, want %d body=%s", storageResponse.StatusCode, http.StatusForbidden, string(data))
	}
	storageResponse.Body.Close()

	recoveryResponse := requestDashboardPage(t, testServer, "/dashboard/settings/recovery", "member-token")
	if recoveryResponse.StatusCode != http.StatusForbidden {
		data, _ := io.ReadAll(recoveryResponse.Body)
		recoveryResponse.Body.Close()
		t.Fatalf("member recovery status = %d, want %d body=%s", recoveryResponse.StatusCode, http.StatusForbidden, string(data))
	}
	recoveryResponse.Body.Close()

	appsResponse := requestDashboardPage(t, testServer, "/dashboard/settings/apps", "member-token")
	if appsResponse.StatusCode != http.StatusForbidden {
		data, _ := io.ReadAll(appsResponse.Body)
		appsResponse.Body.Close()
		t.Fatalf("member apps status = %d, want %d body=%s", appsResponse.StatusCode, http.StatusForbidden, string(data))
	}
	appsResponse.Body.Close()

	logsResponse := requestDashboardPage(t, testServer, "/dashboard/settings/logs", "member-token")
	if logsResponse.StatusCode != http.StatusForbidden {
		data, _ := io.ReadAll(logsResponse.Body)
		logsResponse.Body.Close()
		t.Fatalf("member logs status = %d, want %d body=%s", logsResponse.StatusCode, http.StatusForbidden, string(data))
	}
	logsResponse.Body.Close()
}

func TestCanonicalSettingsRoutesAreServed(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	admin := domain.User{ID: "usr_settings_admin", Email: "settings-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_settings", UserID: admin.ID, Name: "Family Home", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: admin.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_settings_admin", UserID: admin.ID, TokenHash: hashToken("settings-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	routes := []string{
		"/dashboard/settings",
		"/dashboard/settings/home",
		"/dashboard/settings/quick-links",
		"/dashboard/settings/notifications",
		"/dashboard/settings/people",
		"/dashboard/settings/connections",
		"/dashboard/settings/ai",
		"/dashboard/settings/apps",
		"/dashboard/settings/backups",
		"/dashboard/settings/recovery",
		"/dashboard/settings/logs",
		"/dashboard/settings/join-home",
	}
	for _, routePath := range routes {
		response := requestDashboardPage(t, testServer, routePath, "settings-token")
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", routePath, response.StatusCode, string(data))
		}
		assertReactShell(t, routePath, string(data))
	}
}

func TestCanonicalSettingsRoutesAreRegistered(t *testing.T) {
	t.Parallel()

	server, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("server.go read: %v", err)
	}
	ui, err := os.ReadFile("ui.go")
	if err != nil {
		t.Fatalf("ui.go read: %v", err)
	}

	routes := map[string]string{
		"/dashboard/settings/home":          "handleSettingsHomePage",
		"/dashboard/settings/quick-links":   "handleSettingsQuickLinksPage",
		"/dashboard/settings/notifications": "handleSettingsNotificationsPage",
		"/dashboard/settings/people":        "handleSettingsPeoplePage",
		"/dashboard/settings/connections":   "handleSettingsConnectionsPage",
		"/dashboard/settings/ai":            "handleSettingsAIPage",
		"/dashboard/settings/apps":          "handleSettingsAppsPage",
		"/dashboard/settings/backups":       "handleSettingsBackupsPage",
		"/dashboard/settings/recovery":      "handleSettingsRecoveryPage",
		"/dashboard/settings/logs":          "handleSettingsLogsPage",
		"/dashboard/settings/join-home":     "handleSettingsJoinHomePage",
	}
	for routePath, handler := range routes {
		if !strings.Contains(string(server), `mux.HandleFunc("`+routePath+`", server.`+handler+`)`) {
			t.Fatalf("server.go missing canonical Settings route %s -> %s", routePath, handler)
		}
		if !strings.Contains(string(ui), `func (s *Server) `+handler) ||
			!strings.Contains(string(ui), `"`+routePath+`"`) {
			t.Fatalf("ui.go missing canonical Settings handler %s for %s", handler, routePath)
		}
	}
}

func TestReactBuiltAssetsAreServed(t *testing.T) {
	t.Parallel()

	entries, err := fs.ReadDir(uiAssets, "ui/react/assets")
	if err != nil {
		t.Fatalf("read react assets: %v", err)
	}
	found := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".js") && !strings.HasSuffix(name, ".css") {
			continue
		}
		request := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
		response := httptest.NewRecorder()
		serveUIAsset(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s asset status = %d, want %d", name, response.Code, http.StatusOK)
		}
		if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
			t.Fatalf("%s cache-control = %q", name, got)
		}
		contentType := response.Header().Get("Content-Type")
		switch {
		case strings.HasSuffix(name, ".js"):
			if !strings.Contains(contentType, "application/javascript") {
				t.Fatalf("%s content-type = %q", name, contentType)
			}
		case strings.HasSuffix(name, ".css"):
			if !strings.Contains(contentType, "text/css") {
				t.Fatalf("%s content-type = %q", name, contentType)
			}
		}
		found++
	}
	if found == 0 {
		t.Fatal("expected at least one built React JS or CSS asset")
	}
}

func TestViteHashedAssetDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want bool
	}{
		{name: "react/assets/index-CRNFtl9W.js", want: true},
		{name: "react/assets/index-7cHj2aKL.css", want: true},
		{name: "react/assets/index.js", want: false},
		{name: "react/assets/icon-abc.png", want: false},
		{name: "hank-icon-192.png", want: false},
	}
	for _, test := range tests {
		if got := isViteHashedAsset(test.name); got != test.want {
			t.Errorf("isViteHashedAsset(%q) = %t, want %t", test.name, got, test.want)
		}
	}
}

func TestLegacyDashboardPageAssetsAreNotServedAfterReactCutover(t *testing.T) {
	t.Parallel()

	for _, assetPath := range []string{
		"/assets/admin-nav.js",
		"/assets/api-client.js",
		"/assets/apps.js",
		"/assets/assistant-settings.js",
		"/assets/dashboard.js",
		"/assets/file-server.js",
		"/assets/hank.js",
		"/assets/home-assistant.js",
		"/assets/login.js",
		"/assets/profile-notes.js",
		"/assets/settings-nav.js",
		"/assets/settings.js",
		"/assets/storage.js",
		"/assets/styles.css",
		"/assets/ui-helpers.js",
	} {
		request := httptest.NewRequest(http.MethodGet, assetPath, nil)
		response := httptest.NewRecorder()
		serveUIAsset(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s asset status = %d, want %d", assetPath, response.Code, http.StatusNotFound)
		}
	}
}

func TestPublicReactAppRoutesAreServed(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	for _, routePath := range []string{"/", "/join"} {
		response, err := http.Get(testServer.URL + routePath)
		if err != nil {
			t.Fatalf("%s request: %v", routePath, err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", routePath, response.StatusCode, string(data))
		}
		assertReactShell(t, routePath, string(data))
	}
}

func TestPWAControlRoutes(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	tests := []struct {
		path        string
		contentType string
		marker      string
	}{
		{path: "/manifest.webmanifest", contentType: "application/manifest+json; charset=utf-8", marker: `"name":"Hank"`},
		{path: "/sw.js", contentType: "application/javascript; charset=utf-8", marker: "offline.html"},
		{path: "/offline.html", contentType: "text/html; charset=utf-8", marker: "Hank needs a connection for this release."},
	}
	for _, test := range tests {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			request, err := http.NewRequest(method, testServer.URL+test.path, nil)
			if err != nil {
				t.Fatalf("%s %s request: %v", method, test.path, err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("%s %s: %v", method, test.path, err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("%s %s status = %d, want %d", method, test.path, response.StatusCode, http.StatusOK)
			}
			if got := response.Header.Get("Content-Type"); got != test.contentType {
				t.Fatalf("%s %s content-type = %q, want %q", method, test.path, got, test.contentType)
			}
			if got := response.Header.Get("Cache-Control"); got != "no-cache, max-age=0, must-revalidate" {
				t.Fatalf("%s %s cache-control = %q", method, test.path, got)
			}
			if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("%s %s nosniff = %q", method, test.path, got)
			}
			if method == http.MethodHead && len(body) != 0 {
				t.Fatalf("HEAD %s body length = %d, want 0", test.path, len(body))
			}
			if method == http.MethodGet && !strings.Contains(string(body), test.marker) {
				t.Fatalf("GET %s missing %q", test.path, test.marker)
			}
		}
	}
}

func TestPWAControlFilesAreServedWithRevalidationHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path         string
		embeddedName string
		contentType  string
		marker       string
	}{
		{path: "/manifest.webmanifest", embeddedName: "manifest.webmanifest", contentType: "application/manifest+json; charset=utf-8", marker: `"name":"Hank"`},
		{path: "/sw.js", embeddedName: "sw.js", contentType: "application/javascript; charset=utf-8", marker: "offline.html"},
		{path: "/offline.html", embeddedName: "offline.html", contentType: "text/html; charset=utf-8", marker: "Hank needs a connection for this release."},
	}
	for _, test := range tests {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			request := httptest.NewRequest(method, test.path, nil)
			response := httptest.NewRecorder()

			servePWAControlFile(response, request, test.path, test.embeddedName, test.contentType)

			if response.Code != http.StatusOK {
				t.Fatalf("%s %s status = %d, want %d", method, test.path, response.Code, http.StatusOK)
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("%s %s content-type = %q, want %q", method, test.path, got, test.contentType)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-cache, max-age=0, must-revalidate" {
				t.Fatalf("%s %s cache-control = %q", method, test.path, got)
			}
			if got := response.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("%s %s nosniff = %q", method, test.path, got)
			}
			if method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatalf("HEAD %s body length = %d, want 0", test.path, response.Body.Len())
			}
			if method == http.MethodGet && !strings.Contains(response.Body.String(), test.marker) {
				t.Fatalf("GET %s missing %q", test.path, test.marker)
			}
		}
	}
}

func TestPWAControlRouteSuffixesAndLegacyRoutesReturnNotFound(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	for _, path := range []string{
		"/manifest.webmanifest/extra",
		"/sw.js/extra",
		"/offline.html/extra",
		"/pwa",
		"/pwa/",
		"/pwa/sw.js",
		"/pwa/manifest.webmanifest",
		"/assets/site.webmanifest",
	} {
		response, err := http.Get(testServer.URL + path)
		if err != nil {
			t.Fatalf("%s request: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("%s status = %d, want %d", path, response.StatusCode, http.StatusNotFound)
		}
	}
}

func TestSupportedPWAIsDocumented(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"../../docs/pwa.md": {
			"existing React dashboard",
			"/manifest.webmanifest",
			"/sw.js",
			"does not cache authenticated API responses",
			"30 days",
			"100 MiB",
			"preserve-both",
			"attachment upload and deletion",
		},
		"../../docs/notes.md": {
			"X-Hank-Expected-Revision",
			"unconditional deletion",
			"atomically",
			"note_conflict",
		},
		"../../README.md": {
			"installable Progressive Web App",
			"/dashboard",
			"docs/pwa.md",
		},
		"../../docs/architecture.md": {
			"dashboard",
			"Hank Agents",
			"PostgreSQL",
		},
		"../../docs/deployment.md": {
			"HTTPS",
			"/sw.js",
			"must-revalidate",
			"100 MiB",
		},
	}

	for path, required := range cases {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s read: %v", path, err)
		}
		body := string(data)
		for _, expected := range required {
			if !strings.Contains(body, expected) {
				t.Fatalf("%s missing %q", path, expected)
			}
		}
	}
}

func TestOpenAIStatusReportsChatGPTDeviceConfigAndLinkedAccount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_openai", Email: "openai@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_openai", UserID: user.ID, TokenHash: hashToken("openai-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	server.ConfigureAssistantAI(AssistantAIConfig{Provider: assistantProviderChatGPTCodex, ChatGPTOAuthEnabled: true, ChatGPTAuthIssuer: "https://auth.example.com", ChatGPTBackendBaseURL: "https://chatgpt.example.com/backend-api/codex", ChatGPTClientID: "test-client"})
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var status openAIAccountStatusResponse
	requestJSON(t, testServer, "openai-token", http.MethodGet, "/v1/oauth/openai/status", nil, &status)
	if !status.Configured {
		t.Fatalf("configured = false, want true; missing=%v", status.Missing)
	}
	if status.Linked {
		t.Fatal("linked = true before account exists")
	}
	if status.AuthMode != chatGPTDeviceAuthMode || status.AuthProvider != openAIAccountProviderChatGPTCodex {
		t.Fatalf("auth mode/provider = %q/%q", status.AuthMode, status.AuthProvider)
	}

	expiresAt := now.Add(2 * time.Hour)
	must(t, db.UpsertOpenAIAccount(ctx, domain.OpenAIAccount{
		UserID:          user.ID,
		ProviderUserID:  "workspace-123",
		AuthProvider:    openAIAccountProviderChatGPTCodex,
		ChatGPTPlanType: "plus",
		AccessToken:     "access",
		RefreshToken:    "refresh",
		TokenType:       "Bearer",
		Scope:           "openid profile",
		ExpiresAt:       &expiresAt,
		CreatedAt:       now,
		UpdatedAt:       now,
	}))

	status = openAIAccountStatusResponse{}
	requestJSON(t, testServer, "openai-token", http.MethodGet, "/v1/oauth/openai/status", nil, &status)
	if !status.Linked {
		t.Fatal("linked = false after account exists")
	}
	if status.Scope != "openid profile" {
		t.Fatalf("scope = %q, want %q", status.Scope, "openid profile")
	}
	if status.ChatGPTPlanType != "plus" {
		t.Fatalf("chatgpt plan = %q, want plus", status.ChatGPTPlanType)
	}
	if status.ExpiresAt == nil {
		t.Fatal("expires_at is nil")
	}
}

func TestOpenAIBrowserCallbackRouteIsRemoved(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response, err := http.Get(testServer.URL + "/v1/oauth/openai/callback")
	if err != nil {
		t.Fatalf("callback request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("callback status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
}

func TestLoginSetsStrictHTTPOnlySessionCookie(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	passwordHash, err := bcrypt.GenerateFromPassword([]byte("change-me-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("password hash: %v", err)
	}
	user := domain.User{ID: "usr_cookie", Email: "cookie@example.com", PasswordHash: string(passwordHash), CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	body := bytes.NewBufferString(`{"email":"cookie@example.com","password":"change-me-123"}`)
	request, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/auth/login", body)
	if err != nil {
		t.Fatalf("login request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("login response: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	cookies := response.Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie")
	}
	var sessionCookie *http.Cookie
	var csrfCookie *http.Cookie
	for _, cookie := range cookies {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
		if cookie.Name == csrfCookieName {
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatalf("expected %q cookie", sessionCookieName)
	}
	if !sessionCookie.HttpOnly {
		t.Fatal("session cookie should be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie SameSite = %v, want %v", sessionCookie.SameSite, http.SameSiteLaxMode)
	}
	if sessionCookie.Path != "/" {
		t.Fatalf("session cookie path = %q, want /", sessionCookie.Path)
	}
	if csrfCookie == nil || csrfCookie.Value == "" {
		t.Fatalf("expected %q cookie", csrfCookieName)
	}
	if csrfCookie.HttpOnly {
		t.Fatal("csrf cookie should be readable by dashboard javascript")
	}
}

func TestCookieWriteRequiresCSRFToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_csrf", Email: "csrf@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_csrf", UserID: user.ID, TokenHash: hashToken("session-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	request, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/auth/logout", nil)
	if err != nil {
		t.Fatalf("logout request: %v", err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token"})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("logout response: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("logout without csrf status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}

	request, err = http.NewRequest(http.MethodPost, testServer.URL+"/v1/auth/logout", nil)
	if err != nil {
		t.Fatalf("logout request with csrf: %v", err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-token"})
	request.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrf-token"})
	request.Header.Set(csrfHeaderName, "csrf-token")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("logout response with csrf: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("logout with csrf status = %d, want %d", response.StatusCode, http.StatusOK)
	}
}

func TestAppWebSocketTicketIsSingleUse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, _, _, sessionRawToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	var ticketResponse struct {
		Ticket        string `json:"ticket"`
		WebSocketPath string `json:"websocket_path"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodPost, "/v1/ws/app-ticket", nil, &ticketResponse)

	firstConn, _, err := websocket.Dial(ctx, wsURL(testServer.URL, ticketResponse.WebSocketPath), nil)
	if err != nil {
		t.Fatalf("first ticket websocket dial: %v", err)
	}
	_ = firstConn.Close(websocket.StatusNormalClosure, "done")

	_, response, err := websocket.Dial(ctx, wsURL(testServer.URL, ticketResponse.WebSocketPath), nil)
	if err == nil {
		t.Fatal("expected second ticket websocket dial to fail")
	}
	if response == nil {
		t.Fatal("expected unauthorized websocket response")
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("second ticket websocket status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}

func TestReadyzReturnsServiceUnavailableWhenStoreIsClosed(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	response, err := http.Get(testServer.URL + "/readyz")
	if err != nil {
		t.Fatalf("readyz request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want %d", response.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestMetricsEndpointRendersCounters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	admin := domain.User{ID: "usr_metrics", Email: "metrics@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_metrics", UserID: admin.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	session := domain.AppSession{ID: "sess_metrics", UserID: admin.ID, TokenHash: hashToken("metrics-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: admin.ID, Role: domain.HomeRoleAdmin, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	request, err := http.NewRequest(http.MethodGet, testServer.URL+"/v1/me", nil)
	if err != nil {
		t.Fatalf("me request: %v", err)
	}
	_, _ = http.DefaultClient.Do(request)

	request, err = http.NewRequest(http.MethodGet, testServer.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer metrics-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("metrics request: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("metrics body read: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "hank_online_agents") {
		t.Fatalf("metrics missing online agent gauge: %s", text)
	}
	if !strings.Contains(text, `hank_auth_failures_total{kind="app_http_unauthorized"} 1`) {
		t.Fatalf("metrics missing auth failure counter: %s", text)
	}
	assertPrimary := func(want string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		request.Header.Set("Authorization", "Bearer metrics-token")
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "hank_primary_agent_online "+want+"\n") {
			t.Fatalf("expected primary agent gauge %s (status %d)", want, response.Code)
		}
	}
	assertPrimary("0")
	server.router.RegisterAgent(home.ID, domain.Agent{ID: "worker"}, nil, nil, "worker", nil)
	assertPrimary("0")
	connectionID := server.router.RegisterAgent(home.ID, domain.Agent{ID: "primary"}, nil, nil, AgentTypePrimary, nil)
	assertPrimary("1")
	server.router.UnregisterAgent(home.ID, "primary", connectionID)
	assertPrimary("0")
}

func TestMetricsScrapeTokenAccess(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	server.ConfigureMetricsScrapeToken("scrape-secret")
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	fetch := func(token string) int {
		request, err := http.NewRequest(http.MethodGet, testServer.URL+"/metrics", nil)
		if err != nil {
			t.Fatalf("metrics request: %v", err)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("metrics request: %v", err)
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode
	}

	if status := fetch("scrape-secret"); status != http.StatusOK {
		t.Fatalf("scrape token metrics status = %d, want %d", status, http.StatusOK)
	}
	if status := fetch("wrong-token"); status != http.StatusUnauthorized {
		t.Fatalf("wrong token metrics status = %d, want %d", status, http.StatusUnauthorized)
	}
	if status := fetch(""); status != http.StatusUnauthorized {
		t.Fatalf("missing token metrics status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestLoginRateLimitIsEnforced(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_rate", Email: "rate@example.com", PasswordHash: string(mustPasswordHash(t, "correct-password")), CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	for i := 0; i < 20; i++ {
		request, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/auth/login", strings.NewReader(`{"email":"rate@example.com","password":"wrong-password"}`))
		if err != nil {
			t.Fatalf("login request %d: %v", i, err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Forwarded-For", "203.0.113.10")

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("login response %d: %v", i, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("login status %d = %d, want unauthorized or rate limited", i, response.StatusCode)
		}
	}

	request, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/auth/login", strings.NewReader(`{"email":"rate@example.com","password":"wrong-password"}`))
	if err != nil {
		t.Fatalf("rate limit request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Forwarded-For", "203.0.113.10")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("rate limit response: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rate limit status = %d, want %d", response.StatusCode, http.StatusTooManyRequests)
	}
	events, err := db.ListAuditEvents(ctx, "", "login.failed", auditSeverityWarning, "email", 50, "occurred_at", "desc")
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	foundThrottleAudit := false
	for _, event := range events {
		if strings.Contains(event.MetadataJSON, "login_backoff") || strings.Contains(event.MetadataJSON, "rate_limited") {
			foundThrottleAudit = true
			break
		}
	}
	if !foundThrottleAudit {
		t.Fatalf("login throttle audit event missing: %#v", events)
	}
}

func TestSuccessfulLoginAuditEventAppearsInHomeLogs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_login_audit", Email: "login-audit@example.com", PasswordHash: string(mustPasswordHash(t, "correct-password")), CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_login_audit", UserID: user.ID, Name: "Login Audit Home", CreatedAt: now, UpdatedAt: now}
	adminSession := domain.AppSession{ID: "sess_login_audit_admin", UserID: user.ID, TokenHash: hashToken("admin-token"), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, adminSession))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	requestJSON(t, testServer, "", http.MethodPost, "/v1/auth/login", map[string]string{
		"email":    user.Email,
		"password": "correct-password",
	}, nil)

	var payload struct {
		Events []struct {
			EventType string `json:"event_type"`
			HomeID    string `json:"home_id"`
		} `json:"events"`
	}
	requestJSON(t, testServer, "admin-token", http.MethodGet, "/v1/home/audit-events?event_type=login.succeeded", nil, &payload)
	if len(payload.Events) != 1 {
		t.Fatalf("home login audit events = %d, want 1", len(payload.Events))
	}
	if payload.Events[0].HomeID != home.ID {
		t.Fatalf("login audit home_id = %q, want %q", payload.Events[0].HomeID, home.ID)
	}
}

func TestListAgentTokensForHome(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_1", Email: "user@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Test Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_1", HomeID: home.ID, Name: "Test Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	sessionRawToken := "session-token"
	session := domain.AppSession{ID: "sess_1", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	token := domain.AgentToken{ID: "agtok_1", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken("agent-token"), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateSession(ctx, session))
	must(t, db.CreateAgentToken(ctx, token))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var payload struct {
		Tokens []domain.AgentToken `json:"tokens"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodGet, "/v1/home/agent/tokens", nil, &payload)

	if len(payload.Tokens) != 1 {
		t.Fatalf("token count = %d, want 1", len(payload.Tokens))
	}
	if payload.Tokens[0].ID != token.ID || payload.Tokens[0].AgentID != agent.ID {
		t.Fatalf("listed token = %#v", payload.Tokens[0])
	}
	if payload.Tokens[0].TokenHash != "" {
		t.Fatalf("token hash should be omitted from JSON response")
	}
}

func TestAgentTokenEnrollmentAssignsRolesAndPreventsPromotion(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_enrollment", Email: "enrollment@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_enrollment", UserID: user.ID, Name: "Enrollment Home", CreatedAt: now, UpdatedAt: now}
	sessionToken := "enrollment-session"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_enrollment", UserID: user.ID, TokenHash: hashToken(sessionToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var primaryPayload struct {
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/agent/tokens", map[string]any{
		"agent_id": "primary-agent", "name": "Primary", "agent_type": AgentTypePrimary,
	}, &primaryPayload)
	if primaryPayload.AgentID != "primary-agent" || primaryPayload.AgentType != AgentTypePrimary {
		t.Fatalf("primary enrollment payload = %#v", primaryPayload)
	}

	var workerPayload struct {
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/agent/tokens", map[string]any{
		"agent_id": "worker-agent", "name": "Worker", "agent_type": AgentTypeWorker,
	}, &workerPayload)
	if workerPayload.AgentID != "worker-agent" || workerPayload.AgentType != AgentTypeWorker {
		t.Fatalf("worker enrollment payload = %#v", workerPayload)
	}

	response := doJSONRequest(t, testServer, sessionToken, http.MethodPost, "/v1/home/agent/tokens", map[string]any{
		"agent_id": "worker-agent", "name": "Worker", "agent_type": AgentTypePrimary,
	})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("worker promotion status = %d, want %d; body=%s", response.StatusCode, http.StatusConflict, response.Body)
	}
	storedWorker, err := db.GetAgentByID(ctx, "worker-agent")
	if err != nil {
		t.Fatalf("GetAgentByID worker: %v", err)
	}
	if storedWorker.AgentType != AgentTypeWorker {
		t.Fatalf("worker type after rejected promotion = %q, want worker", storedWorker.AgentType)
	}
}

func TestEmptyListResponsesUseJSONArray(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_empty_lists", Email: "empty-lists@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_empty_lists", UserID: user.ID, Name: "Empty Lists Home", CreatedAt: now, UpdatedAt: now}
	sessionToken := "empty-list-session-token"

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_empty_lists", UserID: user.ID, TokenHash: hashToken(sessionToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	cases := map[string][]string{
		"/v1/home/apps":         {`"apps":[]`},
		"/v1/home/agent/tokens": {`"tokens":[]`},
		"/v1/home/audit-events": {`"events":[]`},
	}
	for path, wants := range cases {
		response := doJSONRequest(t, testServer, sessionToken, http.MethodGet, path, nil)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d body=%s", path, response.StatusCode, response.Body)
		}
		for _, want := range wants {
			if !strings.Contains(response.Body, want) {
				t.Fatalf("%s should contain %s, got %s", path, want, response.Body)
			}
		}
	}
}

func TestHomeQuickLinksListUsesEmptyArrayWhenNoLinks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_quick_empty", Email: "quick-empty@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_quick_empty", UserID: user.ID, Name: "Quick Empty Home", CreatedAt: now, UpdatedAt: now}
	sessionToken := "quick-empty-token"

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_quick_empty", UserID: user.ID, TokenHash: hashToken(sessionToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response := doJSONRequest(t, testServer, sessionToken, http.MethodGet, "/v1/home/quick-links", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("quick links status = %d body=%s", response.StatusCode, response.Body)
	}
	if !strings.Contains(response.Body, `"links":[]`) {
		t.Fatalf("quick links response should encode empty links as []: %s", response.Body)
	}
}

func TestQuickLinkListResponseNormalizesNilLinks(t *testing.T) {
	t.Parallel()

	payload := quickLinkListResponse(domain.Home{}, nil, domain.HomeMembership{})
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"links":[]`) {
		t.Fatalf("quick links response should encode nil links as []: %s", string(data))
	}
}

func TestHomeQuickLinksRequireAdminForMutations(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_quick_admin", Email: "quick-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_quick_member", Email: "quick-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	outsider := domain.User{ID: "usr_quick_outsider", Email: "quick-outsider@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_quick_links", UserID: admin.ID, Name: "Quick Links Home", CreatedAt: now, UpdatedAt: now}
	adminToken := "quick-admin-token"
	memberToken := "quick-member-token"
	outsiderToken := "quick-outsider-token"

	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateUser(ctx, outsider))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_quick_admin", UserID: admin.ID, TokenHash: hashToken(adminToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_quick_member", UserID: member.ID, TokenHash: hashToken(memberToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_quick_outsider", UserID: outsider.ID, TokenHash: hashToken(outsiderToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var created struct {
		Link domain.HomeQuickLink `json:"link"`
	}
	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/home/quick-links", map[string]any{
		"title":                "Docker",
		"url":                  "http://docker.local:9000",
		"description":          "Containers",
		"health_check_enabled": true,
	}, &created)
	if created.Link.ID == "" || created.Link.Status != domain.QuickLinkStatusUnchecked {
		t.Fatalf("created link = %#v", created.Link)
	}

	must(t, db.UpdateHomeMembershipRole(ctx, home.ID, admin.ID, domain.HomeRoleMember))
	var ownerList struct {
		Links   []domain.HomeQuickLink `json:"links"`
		CanEdit bool                   `json:"can_edit"`
	}
	requestJSON(t, testServer, adminToken, http.MethodGet, "/v1/home/quick-links", nil, &ownerList)
	if !ownerList.CanEdit || len(ownerList.Links) != 1 {
		t.Fatalf("deployment owner list should retain edit access after stale role: %#v", ownerList)
	}

	var staleOwnerCreated struct {
		Link domain.HomeQuickLink `json:"link"`
	}
	requestJSON(t, testServer, adminToken, http.MethodPost, "/v1/home/quick-links", map[string]any{
		"title": "GitHub",
		"url":   "https://github.com",
	}, &staleOwnerCreated)
	if staleOwnerCreated.Link.ID == "" {
		t.Fatalf("deployment owner create with stale role = %#v", staleOwnerCreated.Link)
	}

	var memberList struct {
		Links   []domain.HomeQuickLink `json:"links"`
		CanEdit bool                   `json:"can_edit"`
	}
	requestJSON(t, testServer, memberToken, http.MethodGet, "/v1/home/quick-links", nil, &memberList)
	if memberList.CanEdit || len(memberList.Links) != 2 {
		t.Fatalf("member list = %#v", memberList)
	}

	memberCreate := doJSONRequest(t, testServer, memberToken, http.MethodPost, "/v1/home/quick-links", map[string]any{
		"title": "GitHub",
		"url":   "https://github.com",
	})
	if memberCreate.StatusCode != http.StatusForbidden {
		t.Fatalf("member create status = %d, want %d", memberCreate.StatusCode, http.StatusForbidden)
	}

	invalidURL := doJSONRequest(t, testServer, adminToken, http.MethodPost, "/v1/home/quick-links", map[string]any{
		"title": "Credentials",
		"url":   "https://user:pass@example.com",
	})
	if invalidURL.StatusCode != http.StatusBadRequest {
		t.Fatalf("credential URL status = %d, want %d", invalidURL.StatusCode, http.StatusBadRequest)
	}

	outsiderList := doJSONRequest(t, testServer, outsiderToken, http.MethodGet, "/v1/home/quick-links", nil)
	if outsiderList.StatusCode != http.StatusNotFound {
		t.Fatalf("outsider list status = %d, want %d", outsiderList.StatusCode, http.StatusNotFound)
	}
}

func TestHomeQuickLinkChecksClassifyReachability(t *testing.T) {
	t.Parallel()

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		case "/error":
			w.WriteHeader(http.StatusInternalServerError)
		case "/slow":
			time.Sleep(200 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer target.Close()

	db := storeForTest(t)
	defer db.Close()

	// Database creation and migrations are fixture setup, outside the behavior deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now().UTC()
	admin := domain.User{ID: "usr_quick_check_admin", Email: "quick-check-admin@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	member := domain.User{ID: "usr_quick_check_member", Email: "quick-check-member@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_quick_check", UserID: admin.ID, Name: "Quick Check Home", CreatedAt: now, UpdatedAt: now}
	memberToken := "quick-check-member-token"

	must(t, db.CreateUser(ctx, admin))
	must(t, db.CreateUser(ctx, member))
	must(t, db.CreateHome(ctx, home))
	must(t, db.AddHomeMembership(ctx, domain.HomeMembership{HomeID: home.ID, UserID: member.ID, Role: domain.HomeRoleMember, CreatedAt: now, UpdatedAt: now}))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_quick_check_member", UserID: member.ID, TokenHash: hashToken(memberToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	for index, item := range []struct {
		id    string
		title string
		path  string
	}{
		{id: "ql_ok", title: "OK", path: "/ok"},
		{id: "ql_forbidden", title: "Forbidden", path: "/forbidden"},
		{id: "ql_error", title: "Error", path: "/error"},
		{id: "ql_slow", title: "Slow", path: "/slow"},
	} {
		must(t, db.CreateHomeQuickLink(ctx, domain.HomeQuickLink{
			ID:                 item.id,
			HomeID:             home.ID,
			Title:              item.title,
			URL:                target.URL + item.path,
			SortOrder:          index * 10,
			HealthCheckEnabled: true,
			Status:             domain.QuickLinkStatusUnchecked,
			CreatedAt:          now,
			UpdatedAt:          now,
			UpdatedBy:          admin.ID,
		}))
	}

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	server.quickLinkCheckTimeout = 35 * time.Millisecond
	server.quickLinkHTTPClient = &http.Client{}
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var payload struct {
		Links   []domain.HomeQuickLink `json:"links"`
		CanEdit bool                   `json:"can_edit"`
	}
	requestJSON(t, testServer, memberToken, http.MethodPost, "/v1/home/quick-links/checks", nil, &payload)
	if payload.CanEdit {
		t.Fatal("member status check response should not grant edit access")
	}
	byTitle := make(map[string]domain.HomeQuickLink)
	for _, link := range payload.Links {
		byTitle[link.Title] = link
	}
	if byTitle["OK"].Status != domain.QuickLinkStatusUp || byTitle["OK"].StatusCode != http.StatusOK {
		t.Fatalf("OK link = %#v", byTitle["OK"])
	}
	if byTitle["Forbidden"].Status != domain.QuickLinkStatusUp || byTitle["Forbidden"].StatusCode != http.StatusForbidden {
		t.Fatalf("Forbidden link = %#v", byTitle["Forbidden"])
	}
	if byTitle["Error"].Status != domain.QuickLinkStatusDown || byTitle["Error"].StatusCode != http.StatusInternalServerError {
		t.Fatalf("Error link = %#v", byTitle["Error"])
	}
	if byTitle["Slow"].Status != domain.QuickLinkStatusDown || byTitle["Slow"].StatusCode != 0 || byTitle["Slow"].LastError == "" {
		t.Fatalf("Slow link = %#v", byTitle["Slow"])
	}
	for title, link := range byTitle {
		if link.LastCheckedAt == nil {
			t.Fatalf("%s missing last_checked_at: %#v", title, link)
		}
	}
}

func TestHomeSetupStatusHidesAfterCloudRestart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_setup_status", Email: "setup-status@example.com", PasswordHash: "hash", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)}
	home := domain.Home{ID: "home_setup_status", UserID: user.ID, Name: "Test Home", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	sessionRawToken := "setup-status-session-token"
	session := domain.AppSession{ID: "sess_setup_status", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, session))
	must(t, db.UpsertCloudRuntime(ctx, "runtime_after_home", "test"))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var payload struct {
		FirstSetupVisible bool `json:"first_setup_visible"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodGet, "/v1/home/setup-status", nil, &payload)

	if payload.FirstSetupVisible {
		t.Fatal("first setup panel should be hidden after the cloud runtime starts later than the home was created")
	}
}

func TestHomeSetupStatusShowsDuringInitialRuntime(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	// Database creation and migrations are fixture setup, outside the behavior deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_setup_status_initial", Email: "setup-status-initial@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_setup_status_initial", UserID: user.ID, Name: "Test Home", CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour)}
	sessionRawToken := "setup-status-initial-session-token"
	session := domain.AppSession{ID: "sess_setup_status_initial", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.CreateSession(ctx, session))
	must(t, db.UpsertCloudRuntime(ctx, "runtime_before_home", "test"))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var payload struct {
		FirstSetupVisible bool `json:"first_setup_visible"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodGet, "/v1/home/setup-status", nil, &payload)

	if !payload.FirstSetupVisible {
		t.Fatal("first setup panel should remain visible during the initial cloud runtime")
	}
}

func TestUIBootstrapRequiresAuthentication(t *testing.T) {
	t.Parallel()

	db := storeForTest(t)
	defer db.Close()

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	response, err := http.Get(testServer.URL + "/v1/ui/bootstrap")
	if err != nil {
		t.Fatalf("bootstrap request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}

func TestUIBootstrapReturnsCurrentShellState(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_bootstrap", Email: "bootstrap@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_bootstrap", UserID: user.ID, Name: "Bootstrap Home", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)}
	agent := domain.Agent{ID: "agent_bootstrap", HomeID: home.ID, Name: "Bootstrap Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	sessionRawToken := "bootstrap-session-token"
	session := domain.AppSession{ID: "sess_bootstrap", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateSession(ctx, session))
	must(t, db.UpsertCloudRuntime(ctx, "runtime_bootstrap", "test-version"))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var payload map[string]any
	requestJSON(t, testServer, sessionRawToken, http.MethodGet, "/v1/ui/bootstrap", nil, &payload)

	userPayload, ok := payload["user"].(map[string]any)
	if !ok || userPayload["email"] != user.Email {
		t.Fatalf("user payload = %#v", payload["user"])
	}
	sessionPayload, ok := payload["session"].(map[string]any)
	if !ok || sessionPayload["id"] != session.ID {
		t.Fatalf("session payload = %#v", payload["session"])
	}
	homePayload, ok := payload["home"].(map[string]any)
	if !ok || homePayload["name"] != home.Name {
		t.Fatalf("home payload = %#v", payload["home"])
	}
	membershipPayload, ok := payload["membership"].(map[string]any)
	if !ok || membershipPayload["role"] != domain.HomeRoleAdmin {
		t.Fatalf("membership payload = %#v", payload["membership"])
	}
	permissions, ok := payload["permissions"].(map[string]any)
	if !ok || permissions["is_admin"] != true || permissions["can_manage_apps"] != true || permissions["can_use_files"] != true {
		t.Fatalf("permissions = %#v", payload["permissions"])
	}
	agentPayload, ok := payload["agent"].(map[string]any)
	if !ok || agentPayload["agent_id"] != agent.ID || agentPayload["status"] != domain.AgentStatusOffline {
		t.Fatalf("agent payload = %#v", payload["agent"])
	}
	setupPayload, ok := payload["setup_status"].(map[string]any)
	if !ok || setupPayload["first_setup_visible"] != false {
		t.Fatalf("setup_status payload = %#v", payload["setup_status"])
	}
	featuresPayload, ok := payload["features"].(map[string]any)
	if !ok {
		t.Fatalf("features payload = %#v", payload["features"])
	}
	if _, ok := featuresPayload["mcp_enabled"].(bool); !ok {
		t.Fatalf("mcp_enabled = %#v", featuresPayload["mcp_enabled"])
	}
	serverPayload, ok := payload["server"].(map[string]any)
	if !ok || serverPayload["version"] == "" {
		t.Fatalf("server payload = %#v", payload["server"])
	}
	navigationPayload, ok := payload["navigation"].([]any)
	if !ok || len(navigationPayload) == 0 {
		t.Fatalf("navigation payload = %#v", payload["navigation"])
	}
}

func TestDeleteAgentTokenCanPurgeDisabledSetupFile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_1", Email: "user@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Test Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_1", HomeID: home.ID, Name: "Test Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	sessionRawToken := "session-token"
	session := domain.AppSession{ID: "sess_1", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	token := domain.AgentToken{ID: "agtok_1", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken("agent-token"), RevokedAt: &now, CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateSession(ctx, session))
	must(t, db.CreateAgentToken(ctx, token))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var deletePayload struct {
		OK bool `json:"ok"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodDelete, "/v1/home/agent/tokens/agtok_1?purge=1", nil, &deletePayload)
	if !deletePayload.OK {
		t.Fatalf("delete response ok = false")
	}

	var listPayload struct {
		Tokens []domain.AgentToken `json:"tokens"`
	}
	requestJSON(t, testServer, sessionRawToken, http.MethodGet, "/v1/home/agent/tokens", nil, &listPayload)
	if len(listPayload.Tokens) != 0 {
		t.Fatalf("token count after purge = %d, want 0", len(listPayload.Tokens))
	}
}

func TestAdminCanPermanentlyRemoveAgent(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_remove_agent", Email: "remove-agent@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_remove_agent", UserID: user.ID, Name: "Removal Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_remove_me", HomeID: home.ID, Name: "Old Mac", Status: domain.AgentStatusOffline, AgentType: AgentTypeWorker, CreatedAt: now, UpdatedAt: now}
	sessionToken := "remove-agent-session-token"
	agentToken := "remove-agent-token"
	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_remove_agent", UserID: user.ID, TokenHash: hashToken(sessionToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	must(t, db.CreateAgentToken(ctx, domain.AgentToken{ID: "agtok_remove_agent", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(agentToken), CreatedAt: now}))

	server := NewServer("127.0.0.1:0", db, time.Hour, time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	var payload struct {
		OK bool `json:"ok"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodDelete, "/v1/home/agents/agent_remove_me", nil, &payload)
	if !payload.OK {
		t.Fatal("remove agent response ok = false")
	}
	if _, err := db.GetAgentByID(ctx, agent.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAgentByID after removal error = %v, want ErrNotFound", err)
	}
	if _, err := db.ValidateAgentToken(ctx, hashToken(agentToken)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("ValidateAgentToken after removal error = %v, want ErrNotFound", err)
	}

	var agents struct {
		Agents []domain.Agent `json:"agents"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/agents", nil, &agents)
	if len(agents.Agents) != 0 {
		t.Fatalf("agents after removal = %#v, want empty", agents.Agents)
	}
}

func TestHomeAgentRestartEndpointRoutesSystemRestart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	errCh := make(chan error, 1)
	commandCh := make(chan protocol.RoutedCommand, 1)
	go func() {
		var envelope protocol.Envelope
		if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
			errCh <- err
			return
		}
		command, err := protocol.DecodePayload[protocol.RoutedCommand](envelope)
		if err != nil {
			errCh <- err
			return
		}
		commandCh <- command
		response, err := protocol.NewEnvelope(protocol.TypeCloudResponse, envelope.RequestID, agentID, homeID, protocol.SystemRestartResponse{
			OK:        true,
			Message:   "agent restart scheduled",
			RestartAt: time.Now().UTC().Add(time.Second),
		})
		if err != nil {
			errCh <- err
			return
		}
		errCh <- wsjson.Write(ctx, agentConn, response)
	}()

	var payload map[string]any
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/agent/restart", nil, &payload)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	command := <-commandCh
	if command.Command != protocol.CommandSystemRestart {
		t.Fatalf("restart command = %q, want %q", command.Command, protocol.CommandSystemRestart)
	}
	if payload["ok"] != true || payload["message"] == "" {
		t.Fatalf("restart payload = %#v", payload)
	}

	var auditPayload struct {
		Events []struct {
			EventType  string         `json:"event_type"`
			Severity   string         `json:"severity"`
			TargetType string         `json:"target_type"`
			TargetID   string         `json:"target_id"`
			HelperText string         `json:"helper_text"`
			Metadata   map[string]any `json:"metadata"`
		} `json:"events"`
	}
	requestJSON(t, testServer, sessionToken, http.MethodGet, "/v1/home/audit-events?event_type=agent.restart_requested&severity=warning&target_type=agent", nil, &auditPayload)
	if len(auditPayload.Events) != 1 {
		t.Fatalf("restart audit events = %d, want 1", len(auditPayload.Events))
	}
	event := auditPayload.Events[0]
	if event.EventType != "agent.restart_requested" || event.Severity != auditSeverityWarning || event.TargetType != "agent" || event.TargetID != agentID {
		t.Fatalf("restart audit event = %#v", event)
	}
	if event.HelperText == "" || event.Metadata["restart_at"] == nil {
		t.Fatalf("restart audit helper/metadata = %#v", event)
	}
}

func TestRequestTimeoutReturnsAppError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_1", Email: "user@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Test Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_1", HomeID: home.ID, Name: "Test Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	agentRawToken := "agent-token"
	sessionRawToken := "session-token"
	agentToken := domain.AgentToken{ID: "agtok_1", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(agentRawToken), CreatedAt: now}
	session := domain.AppSession{ID: "sess_1", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, agentToken))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 150*time.Millisecond, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)
	defer testServer.Close()

	agentConn, _, err := websocket.Dial(ctx, wsURL(testServer.URL, "/ws/agent"), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":   []string{"Bearer " + agentRawToken},
			"X-Hank-Agent-ID": []string{agent.ID},
		},
	})
	if err != nil {
		t.Fatalf("agent websocket dial: %v", err)
	}
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agent.ID, "", protocol.AgentRegister{AgentID: agent.ID, HomeName: home.Name})
	if err != nil {
		t.Fatalf("NewEnvelope register: %v", err)
	}
	if err := wsjson.Write(ctx, agentConn, register); err != nil {
		t.Fatalf("agent register write: %v", err)
	}

	var registered protocol.Envelope
	if err := wsjson.Read(ctx, agentConn, &registered); err != nil {
		t.Fatalf("agent read registered: %v", err)
	}

	appConn, _, err := appWebSocketDial(ctx, testServer, sessionRawToken)
	if err != nil {
		t.Fatalf("app websocket dial: %v", err)
	}
	defer appConn.Close(websocket.StatusNormalClosure, "done")

	command, err := protocol.NewEnvelope(protocol.TypeAppCommand, "req_timeout", "", home.ID, protocol.RoutedCommand{
		Command: protocol.CommandSystemPing,
	})
	if err != nil {
		t.Fatalf("NewEnvelope app command: %v", err)
	}
	if err := wsjson.Write(ctx, appConn, command); err != nil {
		t.Fatalf("app command write: %v", err)
	}

	var response protocol.Envelope
	if err := wsjson.Read(ctx, appConn, &response); err != nil {
		t.Fatalf("app response read: %v", err)
	}
	if response.Type != protocol.TypeAppError {
		t.Fatalf("app response type = %q, want %q", response.Type, protocol.TypeAppError)
	}
	if response.Error == nil || response.Error.Code != "request_timeout" {
		t.Fatalf("app error = %#v, want request_timeout", response.Error)
	}
}

func TestFileDownloadTransferStreamsOverHTTP(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}

			if envelope.Type != protocol.TypeFileTransferOpen {
				continue
			}

			open, err := protocol.DecodePayload[protocol.FileTransferOpen](envelope)
			if err != nil || open.Operation != protocol.FileTransferOperationDownload {
				return
			}
			if open.SourceID != "media" {
				return
			}

			ready, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, envelope.RequestID, agentID, homeID, protocol.FileTransferReady{
				FlowControl: open.FlowControl, WindowBytes: open.WindowBytes,
				Operation: open.Operation,
				SourceID:  open.SourceID,
				Path:      open.Path,
				Offset:    open.Offset,
				Size:      int64(len("hello world")),
			})
			_ = wsjson.Write(ctx, agentConn, ready)

			chunk1, _ := protocol.NewEnvelope(protocol.TypeFileTransferData, envelope.RequestID, agentID, homeID, protocol.FileTransferChunk{
				Offset:        open.Offset,
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("hello ")),
			})
			_ = wsjson.Write(ctx, agentConn, chunk1)

			chunk2, _ := protocol.NewEnvelope(protocol.TypeFileTransferData, envelope.RequestID, agentID, homeID, protocol.FileTransferChunk{
				Offset:        open.Offset + int64(len("hello ")),
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("world")),
			})
			_ = wsjson.Write(ctx, agentConn, chunk2)

			complete, _ := protocol.NewEnvelope(protocol.TypeFileTransferComplete, envelope.RequestID, agentID, homeID, protocol.FileTransferComplete{
				Operation: open.Operation,
				SourceID:  open.SourceID,
				Path:      open.Path,
				Offset:    int64(len("hello world")),
				Size:      int64(len("hello world")),
			})
			_ = wsjson.Write(ctx, agentConn, complete)
		}
	}()

	setupResponse := struct {
		URL           string `json:"url"`
		TransferToken string `json:"transfer_token"`
		SourceID      string `json:"source_id"`
	}{}
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/files/downloads", map[string]string{"path": "docs/quarterly report.txt", "source_id": "media"}, &setupResponse)
	if setupResponse.SourceID != "media" {
		t.Fatalf("download source_id = %q, want media", setupResponse.SourceID)
	}

	request, err := http.NewRequest(http.MethodGet, testServer.URL+setupResponse.URL, nil)
	if err != nil {
		t.Fatalf("download request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+setupResponse.TransferToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("download GET: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("download status = %d, want 200", response.StatusCode)
	}
	if got := response.Header.Get("Content-Disposition"); got != `attachment; filename="quarterly report.txt"` {
		t.Fatalf("Content-Disposition = %q, want attachment filename", got)
	}
	if got := response.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", got)
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("download body read: %v", err)
	}
	if string(body) != "hello world" {
		t.Fatalf("download body = %q, want %q", string(body), "hello world")
	}
}

func TestFileDownloadTransferCanUsePathScopedCookie(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			if envelope.Type != protocol.TypeFileTransferOpen {
				continue
			}
			open, err := protocol.DecodePayload[protocol.FileTransferOpen](envelope)
			if err != nil {
				return
			}

			ready, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, envelope.RequestID, agentID, homeID, protocol.FileTransferReady{
				FlowControl: open.FlowControl, WindowBytes: open.WindowBytes,
				Operation: open.Operation,
				SourceID:  open.SourceID,
				Path:      open.Path,
				Offset:    open.Offset,
				Size:      int64(len("cookie download")),
			})
			_ = wsjson.Write(ctx, agentConn, ready)

			chunk, _ := protocol.NewEnvelope(protocol.TypeFileTransferData, envelope.RequestID, agentID, homeID, protocol.FileTransferChunk{
				Offset:        open.Offset,
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("cookie download")),
			})
			_ = wsjson.Write(ctx, agentConn, chunk)

			complete, _ := protocol.NewEnvelope(protocol.TypeFileTransferComplete, envelope.RequestID, agentID, homeID, protocol.FileTransferComplete{
				Operation: open.Operation,
				SourceID:  open.SourceID,
				Path:      open.Path,
				Offset:    int64(len("cookie download")),
				Size:      int64(len("cookie download")),
			})
			_ = wsjson.Write(ctx, agentConn, complete)
		}
	}()

	setupRequest, err := http.NewRequest(http.MethodPost, testServer.URL+"/v1/home/files/downloads", strings.NewReader(`{"path":"docs/from-cookie.txt"}`))
	if err != nil {
		t.Fatalf("setup request: %v", err)
	}
	setupRequest.Header.Set("Authorization", "Bearer "+sessionToken)
	setupRequest.Header.Set("Content-Type", "application/json")
	setupResponse, err := http.DefaultClient.Do(setupRequest)
	if err != nil {
		t.Fatalf("setup download: %v", err)
	}
	defer setupResponse.Body.Close()
	if setupResponse.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(setupResponse.Body)
		t.Fatalf("setup status = %d, want 201 body=%q", setupResponse.StatusCode, string(body))
	}
	var setup struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(setupResponse.Body).Decode(&setup); err != nil {
		t.Fatalf("decode setup: %v", err)
	}

	var transferCookie *http.Cookie
	for _, cookie := range setupResponse.Cookies() {
		if strings.HasPrefix(cookie.Name, "hank_transfer_") {
			transferCookie = cookie
			break
		}
	}
	if transferCookie == nil {
		t.Fatal("download setup did not set a transfer cookie")
	}
	if !transferCookie.HttpOnly {
		t.Fatal("transfer cookie should be HttpOnly")
	}
	if transferCookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("transfer cookie SameSite = %v, want Strict", transferCookie.SameSite)
	}
	if transferCookie.Path != setup.URL {
		t.Fatalf("transfer cookie path = %q, want %q", transferCookie.Path, setup.URL)
	}

	downloadRequest, err := http.NewRequest(http.MethodGet, testServer.URL+setup.URL, nil)
	if err != nil {
		t.Fatalf("download request: %v", err)
	}
	downloadRequest.AddCookie(transferCookie)
	downloadResponse, err := http.DefaultClient.Do(downloadRequest)
	if err != nil {
		t.Fatalf("download GET: %v", err)
	}
	defer downloadResponse.Body.Close()
	if downloadResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(downloadResponse.Body)
		t.Fatalf("download status = %d, want 200 body=%q", downloadResponse.StatusCode, string(body))
	}
	body, err := io.ReadAll(downloadResponse.Body)
	if err != nil {
		t.Fatalf("download body read: %v", err)
	}
	if string(body) != "cookie download" {
		t.Fatalf("download body = %q, want cookie download", string(body))
	}
}

func TestFilePreviewStreamsInlineRangeOverHTTP(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}

			if envelope.Type != protocol.TypeFileTransferOpen {
				continue
			}

			open, err := protocol.DecodePayload[protocol.FileTransferOpen](envelope)
			if err != nil || open.Operation != protocol.FileTransferOperationDownload {
				return
			}
			if open.SourceID != "media" || open.Path != "movies/demo.mp4" || open.Offset != 6 || open.Length != 5 {
				return
			}

			ready, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, envelope.RequestID, agentID, homeID, protocol.FileTransferReady{
				FlowControl: open.FlowControl, WindowBytes: open.WindowBytes,
				Operation: open.Operation,
				SourceID:  open.SourceID,
				Path:      open.Path,
				Offset:    open.Offset,
				Size:      int64(len("hello world")),
			})
			_ = wsjson.Write(ctx, agentConn, ready)

			chunk, _ := protocol.NewEnvelope(protocol.TypeFileTransferData, envelope.RequestID, agentID, homeID, protocol.FileTransferChunk{
				Offset:        open.Offset,
				ContentBase64: base64.StdEncoding.EncodeToString([]byte("world")),
			})
			_ = wsjson.Write(ctx, agentConn, chunk)

			complete, _ := protocol.NewEnvelope(protocol.TypeFileTransferComplete, envelope.RequestID, agentID, homeID, protocol.FileTransferComplete{
				Operation: open.Operation,
				SourceID:  open.SourceID,
				Path:      open.Path,
				Offset:    int64(len("hello world")),
				Size:      int64(len("hello world")),
			})
			_ = wsjson.Write(ctx, agentConn, complete)
		}
	}()

	request, err := http.NewRequest(http.MethodGet, testServer.URL+"/v1/home/files/preview?source_id=media&path=movies/demo.mp4", nil)
	if err != nil {
		t.Fatalf("preview request: %v", err)
	}
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionToken})
	request.Header.Set("Range", "bytes=6-10")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("preview GET: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusPartialContent {
		t.Fatalf("preview status = %d, want 206", response.StatusCode)
	}
	if got := response.Header.Get("Content-Range"); got != "bytes 6-10/11" {
		t.Fatalf("Content-Range = %q, want bytes 6-10/11", got)
	}
	if got := response.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	disposition, params, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition: %v", err)
	}
	if disposition != "inline" || params["filename"] != "demo.mp4" {
		t.Fatalf("Content-Disposition = %q params=%#v, want inline demo.mp4", disposition, params)
	}
	if got := response.Header.Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("preview body read: %v", err)
	}
	if string(body) != "world" {
		t.Fatalf("preview body = %q, want %q", string(body), "world")
	}
}

func TestPreviewContentTypeRecognizesMarkdown(t *testing.T) {
	for _, path := range []string{"readme.md", "docs/guide.markdown"} {
		if got := previewContentType(path); got != "text/markdown; charset=utf-8" {
			t.Fatalf("previewContentType(%q) = %q, want Markdown", path, got)
		}
	}
}

func TestFilePreviewHTMLUsesSandboxHeaders(t *testing.T) {
	t.Parallel()

	if got := previewContentSecurityPolicy(previewContentType("docs/index.html")); got != "sandbox" {
		t.Fatalf("html preview CSP = %q, want sandbox", got)
	}
	if got := previewContentSecurityPolicy(previewContentType("docs/readme.pdf")); got != "" {
		t.Fatalf("pdf preview CSP = %q, want empty", got)
	}
}

func TestFileUploadTransferStreamsOverHTTP(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	var uploaded bytes.Buffer
	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}

			switch envelope.Type {
			case protocol.TypeFileTransferOpen:
				open, err := protocol.DecodePayload[protocol.FileTransferOpen](envelope)
				if err != nil || open.Operation != protocol.FileTransferOperationUpload {
					return
				}
				ready, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, envelope.RequestID, agentID, homeID, protocol.FileTransferReady{
					FlowControl: open.FlowControl, WindowBytes: open.WindowBytes,
					Operation: open.Operation,
					Path:      open.Path,
					Offset:    open.Offset,
					Size:      open.Offset,
				})
				_ = wsjson.Write(ctx, agentConn, ready)

			case protocol.TypeFileTransferData:
				chunk, err := protocol.DecodePayload[protocol.FileTransferChunk](envelope)
				if err != nil {
					return
				}
				data, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
				if err != nil {
					return
				}
				_, _ = uploaded.Write(data)

			case protocol.TypeFileTransferComplete:
				complete, err := protocol.DecodePayload[protocol.FileTransferComplete](envelope)
				if err != nil || complete.Operation != protocol.FileTransferOperationUpload {
					return
				}
				reply, _ := protocol.NewEnvelope(protocol.TypeFileTransferComplete, envelope.RequestID, agentID, homeID, protocol.FileTransferComplete{
					Operation: complete.Operation,
					Path:      complete.Path,
					Offset:    int64(uploaded.Len()),
					Size:      int64(uploaded.Len()),
				})
				_ = wsjson.Write(ctx, agentConn, reply)
				return
			}
		}
	}()

	setupResponse := struct {
		URL           string `json:"url"`
		TransferToken string `json:"transfer_token"`
	}{}
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/files/uploads", map[string]string{"path": "docs/upload.txt"}, &setupResponse)

	putRequest, err := http.NewRequest(http.MethodPut, testServer.URL+setupResponse.URL, strings.NewReader("stream me"))
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	putRequest.Header.Set("Authorization", "Bearer "+setupResponse.TransferToken)
	putResponse, err := http.DefaultClient.Do(putRequest)
	if err != nil {
		t.Fatalf("upload PUT: %v", err)
	}
	defer putResponse.Body.Close()

	if putResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(putResponse.Body)
		t.Fatalf("upload status = %d, want 200 body=%s", putResponse.StatusCode, string(body))
	}
	if uploaded.String() != "stream me" {
		t.Fatalf("uploaded body = %q, want %q", uploaded.String(), "stream me")
	}
}

func TestFileDownloadTransferResumeFromOffset(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	content := "hello world"
	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}
			if envelope.Type != protocol.TypeFileTransferOpen {
				continue
			}

			open, err := protocol.DecodePayload[protocol.FileTransferOpen](envelope)
			if err != nil || open.Operation != protocol.FileTransferOperationDownload {
				return
			}

			ready, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, envelope.RequestID, agentID, homeID, protocol.FileTransferReady{
				FlowControl: open.FlowControl, WindowBytes: open.WindowBytes,
				Operation: open.Operation,
				Path:      open.Path,
				Offset:    open.Offset,
				Size:      int64(len(content)),
			})
			_ = wsjson.Write(ctx, agentConn, ready)

			remaining := content[open.Offset:]
			chunk, _ := protocol.NewEnvelope(protocol.TypeFileTransferData, envelope.RequestID, agentID, homeID, protocol.FileTransferChunk{
				Offset:        open.Offset,
				ContentBase64: base64.StdEncoding.EncodeToString([]byte(remaining)),
			})
			_ = wsjson.Write(ctx, agentConn, chunk)

			complete, _ := protocol.NewEnvelope(protocol.TypeFileTransferComplete, envelope.RequestID, agentID, homeID, protocol.FileTransferComplete{
				Operation: open.Operation,
				Path:      open.Path,
				Offset:    int64(len(content)),
				Size:      int64(len(content)),
			})
			_ = wsjson.Write(ctx, agentConn, complete)
		}
	}()

	setupResponse := struct {
		URL           string `json:"url"`
		TransferToken string `json:"transfer_token"`
	}{}
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/files/downloads", map[string]string{"path": "docs/report.txt"}, &setupResponse)

	firstRequest, err := http.NewRequest(http.MethodGet, testServer.URL+setupResponse.URL+"?offset=0", nil)
	if err != nil {
		t.Fatalf("first download request: %v", err)
	}
	firstRequest.Header.Set("Authorization", "Bearer "+setupResponse.TransferToken)
	firstResponse, err := http.DefaultClient.Do(firstRequest)
	if err != nil {
		t.Fatalf("first download GET: %v", err)
	}
	firstBody := make([]byte, 5)
	if _, err := io.ReadFull(firstResponse.Body, firstBody); err != nil {
		t.Fatalf("first partial read: %v", err)
	}
	_ = firstResponse.Body.Close()

	secondRequest, err := http.NewRequest(http.MethodGet, testServer.URL+setupResponse.URL+"?offset=5", nil)
	if err != nil {
		t.Fatalf("second download request: %v", err)
	}
	secondRequest.Header.Set("Authorization", "Bearer "+setupResponse.TransferToken)
	secondResponse, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatalf("second download GET: %v", err)
	}
	defer secondResponse.Body.Close()

	remaining, err := io.ReadAll(secondResponse.Body)
	if err != nil {
		t.Fatalf("second download body read: %v", err)
	}
	if string(firstBody)+string(remaining) != content {
		t.Fatalf("resumed download = %q, want %q", string(firstBody)+string(remaining), content)
	}
}

func TestFileUploadTransferResumeFromOffset(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testServer, homeID, agentID, sessionToken, agentConn := setupServerAndAgent(t, ctx)
	defer testServer.Close()
	defer agentConn.Close(websocket.StatusNormalClosure, "done")

	var uploaded bytes.Buffer
	go func() {
		for {
			var envelope protocol.Envelope
			if err := wsjson.Read(ctx, agentConn, &envelope); err != nil {
				return
			}

			switch envelope.Type {
			case protocol.TypeFileTransferOpen:
				open, err := protocol.DecodePayload[protocol.FileTransferOpen](envelope)
				if err != nil || open.Operation != protocol.FileTransferOperationUpload {
					return
				}
				if int64(uploaded.Len()) > open.Offset {
					uploaded.Truncate(int(open.Offset))
				}
				ready, _ := protocol.NewEnvelope(protocol.TypeFileTransferReady, envelope.RequestID, agentID, homeID, protocol.FileTransferReady{
					FlowControl: open.FlowControl, WindowBytes: open.WindowBytes,
					Operation: open.Operation,
					Path:      open.Path,
					Offset:    int64(uploaded.Len()),
					Size:      int64(uploaded.Len()),
				})
				_ = wsjson.Write(ctx, agentConn, ready)

			case protocol.TypeFileTransferData:
				chunk, err := protocol.DecodePayload[protocol.FileTransferChunk](envelope)
				if err != nil {
					return
				}
				data, err := base64.StdEncoding.DecodeString(chunk.ContentBase64)
				if err != nil {
					return
				}
				if int64(uploaded.Len()) != chunk.Offset {
					return
				}
				_, _ = uploaded.Write(data)

			case protocol.TypeFileTransferComplete:
				complete, err := protocol.DecodePayload[protocol.FileTransferComplete](envelope)
				if err != nil || complete.Operation != protocol.FileTransferOperationUpload {
					return
				}
				reply, _ := protocol.NewEnvelope(protocol.TypeFileTransferComplete, envelope.RequestID, agentID, homeID, protocol.FileTransferComplete{
					Operation: complete.Operation,
					Path:      complete.Path,
					Offset:    int64(uploaded.Len()),
					Size:      int64(uploaded.Len()),
				})
				_ = wsjson.Write(ctx, agentConn, reply)
			}
		}
	}()

	setupResponse := struct {
		URL           string `json:"url"`
		TransferToken string `json:"transfer_token"`
	}{}
	requestJSON(t, testServer, sessionToken, http.MethodPost, "/v1/home/files/uploads", map[string]string{"path": "docs/upload.txt"}, &setupResponse)

	firstRequest, err := http.NewRequest(http.MethodPut, testServer.URL+setupResponse.URL+"?offset=0", strings.NewReader("stream "))
	if err != nil {
		t.Fatalf("first upload request: %v", err)
	}
	firstRequest.Header.Set("Authorization", "Bearer "+setupResponse.TransferToken)
	firstResponse, err := http.DefaultClient.Do(firstRequest)
	if err != nil {
		t.Fatalf("first upload PUT: %v", err)
	}
	_ = firstResponse.Body.Close()
	if firstResponse.StatusCode != http.StatusOK {
		t.Fatalf("first upload status = %d, want 200", firstResponse.StatusCode)
	}

	secondRequest, err := http.NewRequest(http.MethodPut, testServer.URL+setupResponse.URL+"?offset=7", strings.NewReader("me"))
	if err != nil {
		t.Fatalf("second upload request: %v", err)
	}
	secondRequest.Header.Set("Authorization", "Bearer "+setupResponse.TransferToken)
	secondResponse, err := http.DefaultClient.Do(secondRequest)
	if err != nil {
		t.Fatalf("second upload PUT: %v", err)
	}
	defer secondResponse.Body.Close()
	if secondResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(secondResponse.Body)
		t.Fatalf("second upload status = %d, want 200 body=%s", secondResponse.StatusCode, string(body))
	}

	if uploaded.String() != "stream me" {
		t.Fatalf("resumed upload body = %q, want %q", uploaded.String(), "stream me")
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func wsURL(base string, path string) string {
	return "ws" + strings.TrimPrefix(base, "http") + path
}

func appWebSocketDial(ctx context.Context, server *httptest.Server, sessionToken string) (*websocket.Conn, *http.Response, error) {
	return websocket.Dial(ctx, wsURL(server.URL, "/ws/app"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + sessionToken}},
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustPasswordHash(t *testing.T, password string) []byte {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func storeForTest(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.OpenMigrating(context.Background(), testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func setupServerAndAgent(t *testing.T, ctx context.Context) (*httptest.Server, string, string, string, *websocket.Conn) {
	t.Helper()

	db := storeForTest(t)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	user := domain.User{ID: "usr_1", Email: "user@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	home := domain.Home{ID: "home_1", UserID: user.ID, Name: "Test Home", CreatedAt: now, UpdatedAt: now}
	agent := domain.Agent{ID: "agent_1", HomeID: home.ID, Name: "Test Agent", Status: domain.AgentStatusOffline, CreatedAt: now, UpdatedAt: now}
	agentRawToken := "agent-token"
	sessionRawToken := "session-token"
	agentToken := domain.AgentToken{ID: "agtok_1", HomeID: home.ID, AgentID: agent.ID, TokenHash: hashToken(agentRawToken), CreatedAt: now}
	session := domain.AppSession{ID: "sess_1", UserID: user.ID, TokenHash: hashToken(sessionRawToken), ExpiresAt: now.Add(time.Hour), CreatedAt: now}

	must(t, db.CreateUser(ctx, user))
	must(t, db.CreateHome(ctx, home))
	must(t, db.UpsertAgent(ctx, agent))
	must(t, db.CreateAgentToken(ctx, agentToken))
	must(t, db.CreateSession(ctx, session))

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(ioDiscard{}, nil)))
	testServer := httptest.NewServer(server.http.Handler)

	agentConn, _, err := websocket.Dial(ctx, wsURL(testServer.URL, "/ws/agent"), &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization":   []string{"Bearer " + agentRawToken},
			"X-Hank-Agent-ID": []string{agent.ID},
		},
	})
	if err != nil {
		t.Fatalf("agent websocket dial: %v", err)
	}

	register, err := protocol.NewEnvelope(protocol.TypeAgentRegister, "", agent.ID, "", protocol.AgentRegister{AgentID: agent.ID, HomeName: home.Name, Capabilities: []string{protocol.CapabilityAppsSandboxV1}})
	if err != nil {
		t.Fatalf("NewEnvelope register: %v", err)
	}
	if err := wsjson.Write(ctx, agentConn, register); err != nil {
		t.Fatalf("agent register write: %v", err)
	}

	var registered protocol.Envelope
	if err := wsjson.Read(ctx, agentConn, &registered); err != nil {
		t.Fatalf("agent read registered: %v", err)
	}
	if registered.Type != protocol.TypeAgentRegistered {
		t.Fatalf("registered type = %q, want %q", registered.Type, protocol.TypeAgentRegistered)
	}

	return testServer, home.ID, agent.ID, sessionRawToken, agentConn
}

func requestJSON(t *testing.T, server *httptest.Server, sessionToken string, method string, path string, body any, out any) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}

	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("request %s %s status = %d body=%s", method, path, response.StatusCode, string(data))
	}

	if out != nil {
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}

func assertReactShell(t *testing.T, routePath string, body string) {
	t.Helper()
	for _, expected := range []string{
		`<div id="root"></div>`,
		`<title>Hank</title>`,
		`<script type="module" crossorigin src="/assets/index-`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("%s missing React shell marker %q", routePath, expected)
		}
	}
}

type testJSONResponse struct {
	StatusCode int
	Body       string
}

func doJSONRequest(t *testing.T, server *httptest.Server, sessionToken string, method string, path string, body any) testJSONResponse {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}

	request, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+sessionToken)
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return testJSONResponse{StatusCode: response.StatusCode, Body: string(data)}
}

func requestDashboardPage(t *testing.T, server *httptest.Server, path string, sessionToken string) *http.Response {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sessionToken != "" {
		request.AddCookie(&http.Cookie{
			Name:  sessionCookieName,
			Value: sessionToken,
			Path:  "/",
		})
	}

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func mustRegisteredUserID(t *testing.T, response *http.Response) string {
	t.Helper()

	var payload struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		SessionID    string    `json:"session_id"`
		SessionToken string    `json:"session_token"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.User.ID == "" {
		t.Fatal("registered response missing user ID")
	}
	if payload.SessionID == "" || payload.SessionToken == "" || payload.ExpiresAt.IsZero() {
		t.Fatalf("registered response missing session fields: %#v", payload)
	}
	return payload.User.ID
}
