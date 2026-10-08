package cloud

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/observability"
)

func TestMCPSubscriptionFilter(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantTools bool
		wantError bool
	}{
		{name: "tools", raw: `{"toolsListChanged":true}`, wantTools: true},
		{name: "not object", raw: `[]`, wantError: true},
		{name: "malformed tools", raw: `{"toolsListChanged":"yes"}`, wantError: true},
		{name: "unsupported only", raw: `{"promptsListChanged":true,"resourcesListChanged":true}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter, err := decodeMCPSubscriptionFilter(json.RawMessage(test.raw))
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError = %v", err, test.wantError)
			}
			if filter.ToolsListChanged != test.wantTools {
				t.Fatalf("toolsListChanged = %v", filter.ToolsListChanged)
			}
		})
	}
}

func TestMCPSubscriptionsListenAcknowledgesAndDeliversToolChange(t *testing.T) {
	metrics := observability.NewMetrics()
	server := &Server{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics: metrics,
	}
	server.mcpSubscriptions = newMCPSubscriptionHub(testMCPSubscriptionConfig(), metrics)
	auth := mcpAuthContext{
		User: domain.User{ID: "user-a"},
		Token: domain.MCPToken{
			ID:              "token-a",
			UserID:          "user-a",
			AccessExpiresAt: time.Now().Add(time.Hour),
		},
	}
	harness := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.handleMCPAuthenticatedEndpoint(w, r, auth)
	}))
	defer harness.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"jsonrpc":"2.0","id":"listen-1","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, harness.URL, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", mcpModernProtocolVersion)
	request.Header.Set("Mcp-Method", "subscriptions/listen")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status/content-type = %d/%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	assertMCPNoStoreHeaders(t, response.Header)

	reader := bufio.NewReader(response.Body)
	acknowledged := readMCPSSETestFrame(t, reader)
	assertMCPSubscriptionFrame(t, acknowledged, "notifications/subscriptions/acknowledged", "listen-1")
	server.notifyMCPToolsChanged()
	changed := readMCPSSETestFrame(t, reader)
	assertMCPSubscriptionFrame(t, changed, "notifications/tools/list_changed", "listen-1")

	cancel()
	response.Body.Close()
	deadline := time.Now().Add(time.Second)
	for server.mcpSubscriptions.activeCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := server.mcpSubscriptions.activeCount(); got != 0 {
		t.Fatalf("active subscriptions after cancellation = %d", got)
	}
}

func TestMCPSubscriptionsEmptyFilterAcknowledgesThenCompletes(t *testing.T) {
	server, auth, harness := newMCPSubscriptionTestServer(t, testMCPSubscriptionConfig())
	defer harness.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"jsonrpc":"2.0","id":"listen-empty","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"promptsListChanged":true}}}`
	response := openMCPSubscriptionTestRequest(t, ctx, harness.URL, body)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	acknowledged := readMCPSSETestFrame(t, reader)
	assertMCPSubscriptionFrame(t, acknowledged, "notifications/subscriptions/acknowledged", "listen-empty")
	params, _ := acknowledged["params"].(map[string]any)
	if notifications, _ := params["notifications"].(map[string]any); len(notifications) != 0 {
		t.Fatalf("honored notifications = %#v", notifications)
	}
	completed := readMCPSSETestFrame(t, reader)
	if completed["id"] != "listen-empty" {
		t.Fatalf("completion ID = %#v", completed["id"])
	}
	result, _ := completed["result"].(map[string]any)
	meta, _ := result["_meta"].(map[string]any)
	if result["resultType"] != "complete" || meta[mcpSubscriptionIDMetaKey] != "listen-empty" {
		t.Fatalf("completion = %#v", completed)
	}
	if auth.Token.ID == "" || server.mcpSubscriptions.activeCount() != 0 {
		t.Fatalf("test auth/active state = %q/%d", auth.Token.ID, server.mcpSubscriptions.activeCount())
	}
}

func TestMCPSubscriptionsMalformedFilterErrorsBeforeSSE(t *testing.T) {
	server, auth, harness := newMCPSubscriptionTestServer(t, testMCPSubscriptionConfig())
	harness.Close()
	body := `{"jsonrpc":"2.0","id":"bad-filter","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":"yes"}}}`
	request := modernMCPRequest(body)
	request.Header.Set("Mcp-Method", "subscriptions/listen")
	response := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(response, request, auth)
	if response.Code != http.StatusBadRequest || strings.Contains(response.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status/content-type/body = %d/%q/%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
}

func TestMCPSubscriptionsRejectOversizedRequestIDBeforeRegistration(t *testing.T) {
	server, auth, harness := newMCPSubscriptionTestServer(t, testMCPSubscriptionConfig())
	harness.Close()
	body := `{"jsonrpc":"2.0","id":"` + strings.Repeat("a", maxMCPSubscriptionRequestIDBytes+1) + `","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	request := modernMCPRequest(body)
	request.Header.Set("Mcp-Method", "subscriptions/listen")
	response := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(response, request, auth)
	if response.Code != http.StatusBadRequest || server.mcpSubscriptions.activeCount() != 0 {
		t.Fatalf("status/active/body = %d/%d/%s", response.Code, server.mcpSubscriptions.activeCount(), response.Body.String())
	}
}

func TestMCPSubscriptionsHeartbeatIsCommentOnly(t *testing.T) {
	config := testMCPSubscriptionConfig()
	config.HeartbeatInterval = 5 * time.Millisecond
	config.CredentialCheckInterval = time.Second
	_, _, harness := newMCPSubscriptionTestServer(t, config)
	defer harness.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := `{"jsonrpc":"2.0","id":"listen-heartbeat","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	response := openMCPSubscriptionTestRequest(t, ctx, harness.URL, body)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readMCPSSETestFrame(t, reader)
	line, err := reader.ReadString('\n')
	if err != nil || line != ": keepalive\n" {
		t.Fatalf("heartbeat = %q, err = %v", line, err)
	}
}

func TestMCPSubscriptionsEnforcePerTokenLimit(t *testing.T) {
	config := testMCPSubscriptionConfig()
	config.MaxPerToken = 1
	_, _, harness := newMCPSubscriptionTestServer(t, config)
	defer harness.Close()
	firstContext, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	body := `{"jsonrpc":"2.0","id":"listen-limit","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	first := openMCPSubscriptionTestRequest(t, firstContext, harness.URL, body)
	defer first.Body.Close()
	readMCPSSETestFrame(t, bufio.NewReader(first.Body))

	secondContext, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	second := openMCPSubscriptionTestRequest(t, secondContext, harness.URL, body)
	defer second.Body.Close()
	var envelope struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(second.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if second.Header.Get("Content-Type") == "text/event-stream" || envelope.Error.Code != -32603 {
		t.Fatalf("limit response content-type/code = %q/%d", second.Header.Get("Content-Type"), envelope.Error.Code)
	}
}

func TestMCPSubscriptionClosesAtAccessTokenExpiry(t *testing.T) {
	config := testMCPSubscriptionConfig()
	config.HeartbeatInterval = time.Second
	server, auth, originalHarness := newMCPSubscriptionTestServer(t, config)
	originalHarness.Close()
	auth.Token.AccessExpiresAt = time.Now().Add(15 * time.Millisecond)
	harness := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.handleMCPAuthenticatedEndpoint(w, r, auth)
	}))
	defer harness.Close()
	body := `{"jsonrpc":"2.0","id":"listen-expiry","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	response := openMCPSubscriptionTestRequest(t, context.Background(), harness.URL, body)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readMCPSSETestFrame(t, reader)
	completed := readMCPSSETestFrame(t, reader)
	if completed["id"] != "listen-expiry" || completed["result"] == nil {
		t.Fatalf("expiry completion = %#v", completed)
	}
}

func TestMCPShutdownGracefullyCompletesSubscriptions(t *testing.T) {
	config := testMCPSubscriptionConfig()
	config.HeartbeatInterval = time.Second
	server, _, harness := newMCPSubscriptionTestServer(t, config)
	defer harness.Close()
	server.http = &http.Server{}
	body := `{"jsonrpc":"2.0","id":"listen-shutdown","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	response := openMCPSubscriptionTestRequest(t, context.Background(), harness.URL, body)
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readMCPSSETestFrame(t, reader)

	shutdownDone := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				shutdownDone <- fmt.Errorf("shutdown panic: %v", recovered)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		shutdownDone <- server.Shutdown(ctx)
	}()
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not complete")
	}
	completed := readMCPSSETestFrame(t, reader)
	if completed["id"] != "listen-shutdown" || completed["result"] == nil {
		t.Fatalf("shutdown completion = %#v", completed)
	}
}

func TestMCPEndpointGETRequiresInitializedSession(t *testing.T) {
	server := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	request := httptest.NewRequest(http.MethodGet, "https://hank.example/v1/mcp", nil)
	response := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertMCPNoStoreHeaders(t, response.Header())
}

func newMCPSubscriptionTestServer(t *testing.T, config mcpSubscriptionConfig) (*Server, mcpAuthContext, *httptest.Server) {
	t.Helper()
	metrics := observability.NewMetrics()
	server := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: metrics}
	server.mcpSubscriptions = newMCPSubscriptionHub(config, metrics)
	auth := mcpAuthContext{
		User:  domain.User{ID: "user-a"},
		Token: domain.MCPToken{ID: "token-a", UserID: "user-a", AccessExpiresAt: time.Now().Add(time.Hour)},
	}
	harness := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		server.handleMCPAuthenticatedEndpoint(w, r, auth)
	}))
	return server, auth, harness
}

func openMCPSubscriptionTestRequest(t *testing.T, ctx context.Context, endpoint, body string) *http.Response {
	t.Helper()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", mcpModernProtocolVersion)
	request.Header.Set("Mcp-Method", "subscriptions/listen")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("subscription status = %d", response.StatusCode)
	}
	return response
}

func readMCPSSETestFrame(t *testing.T, reader *bufio.Reader) map[string]any {
	t.Helper()
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			if data.Len() == 0 {
				continue
			}
			var frame map[string]any
			if err := json.Unmarshal([]byte(data.String()), &frame); err != nil {
				t.Fatalf("decode SSE frame: %v; data = %s", err, data.String())
			}
			return frame
		}
		if strings.HasPrefix(line, "data: ") {
			data.WriteString(strings.TrimPrefix(line, "data: "))
		}
	}
}

func assertMCPSubscriptionFrame(t *testing.T, frame map[string]any, method string, subscriptionID any) {
	t.Helper()
	if frame["method"] != method {
		t.Fatalf("method = %#v, frame = %#v", frame["method"], frame)
	}
	params, _ := frame["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	if meta["io.modelcontextprotocol/subscriptionId"] != subscriptionID {
		t.Fatalf("subscription ID = %#v, frame = %#v", meta["io.modelcontextprotocol/subscriptionId"], frame)
	}
}
