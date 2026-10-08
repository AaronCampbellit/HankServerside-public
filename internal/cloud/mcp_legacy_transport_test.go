package cloud

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestMCPLegacyInitializeNegotiatesFromParamsThenRequiresVersionHeader(t *testing.T) {
	t.Parallel()

	docsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(docsDir, "README.md"), []byte("# Hank\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{mcpDocs: newMCPDocsIndex(docsDir)}
	auth := mcpLegacyTestAuth("mcpt_test", "test-token")

	initialize := mcpLegacyTestRequest(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"codex","version":"0.148.0"}}}`, "test-token")
	// A pre-negotiation header is deliberately contradictory. Initialize must
	// still negotiate from params.protocolVersion.
	initialize.Header.Set("MCP-Protocol-Version", mcpModernProtocolVersion)
	initialized := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(initialized, initialize, auth)
	if initialized.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, body = %s", initialized.Code, initialized.Body.String())
	}
	assertMCPNoStoreHeaders(t, initialized.Header())
	sessionID := initialized.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("initialize response omitted Mcp-Session-Id")
	}
	var initializeEnvelope struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Instructions    string `json:"instructions"`
			ServerInfo      struct {
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	decodeMCPTestResponse(t, initialized, &initializeEnvelope)
	if initializeEnvelope.Result.ProtocolVersion != mcpInitializeProtocolVersion || initializeEnvelope.Result.ServerInfo.Version != mcpServerVersion {
		t.Fatalf("initialize result = %#v", initializeEnvelope.Result)
	}
	if !strings.Contains(initializeEnvelope.Result.Instructions, "list_docs") {
		t.Fatalf("instructions = %q", initializeEnvelope.Result.Instructions)
	}

	missingVersion := mcpLegacyTestRequest(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, "test-token")
	missingVersion.Header.Set("Mcp-Session-Id", sessionID)
	missingResponse := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(missingResponse, missingVersion, auth)
	if missingResponse.Code != http.StatusBadRequest {
		t.Fatalf("missing version status = %d, body = %s", missingResponse.Code, missingResponse.Body.String())
	}

	initializedNotification := mcpLegacyTestRequest(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`, "test-token")
	initializedNotification.Header.Set("Mcp-Session-Id", sessionID)
	initializedNotification.Header.Set("MCP-Protocol-Version", mcpInitializeProtocolVersion)
	notificationResponse := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(notificationResponse, initializedNotification, auth)
	if notificationResponse.Code != http.StatusAccepted {
		t.Fatalf("initialized notification status = %d, body = %s", notificationResponse.Code, notificationResponse.Body.String())
	}

	listTools := mcpLegacyTestRequest(`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`, "test-token")
	listTools.Header.Set("Mcp-Session-Id", sessionID)
	listTools.Header.Set("MCP-Protocol-Version", mcpInitializeProtocolVersion)
	toolsResponse := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(toolsResponse, listTools, auth)
	if toolsResponse.Code != http.StatusOK || !containsJSONText(toolsResponse.Body.Bytes(), "list_docs") {
		t.Fatalf("tools/list status/body = %d/%s", toolsResponse.Code, toolsResponse.Body.String())
	}
	var toolsEnvelope struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Title       string         `json:"title"`
				Annotations map[string]any `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(toolsResponse.Body.Bytes(), &toolsEnvelope); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	if len(toolsEnvelope.Result.Tools) != 39 {
		t.Fatalf("legacy tools/list advertised %d tools", len(toolsEnvelope.Result.Tools))
	}
	for _, tool := range toolsEnvelope.Result.Tools {
		if strings.TrimSpace(tool.Title) == "" {
			t.Fatalf("legacy tool %s has no title", tool.Name)
		}
		for _, required := range []string{"readOnlyHint", "destructiveHint", "openWorldHint"} {
			if _, ok := tool.Annotations[required].(bool); !ok {
				t.Fatalf("legacy tool %s is missing boolean annotation %s: %#v", tool.Name, required, tool.Annotations)
			}
		}
	}

	listDocs := mcpLegacyTestRequest(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_docs","arguments":{}}}`, "test-token")
	listDocs.Header.Set("Mcp-Session-Id", sessionID)
	listDocs.Header.Set("MCP-Protocol-Version", mcpInitializeProtocolVersion)
	listDocsResponse := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(listDocsResponse, listDocs, auth)
	if listDocsResponse.Code != http.StatusOK || !containsJSONText(listDocsResponse.Body.Bytes(), "README.md") {
		t.Fatalf("list_docs status/body = %d/%s", listDocsResponse.Code, listDocsResponse.Body.String())
	}
}

func TestMCPLegacySessionIsBoundToExactOAuthToken(t *testing.T) {
	t.Parallel()

	server := &Server{mcpDocs: newMCPDocsIndex(t.TempDir())}
	auth := mcpLegacyTestAuth("mcpt_owner", "owner-token")
	initialize := mcpLegacyTestRequest(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"codex","version":"0.148.0"}}}`, "owner-token")
	initializeResponse := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(initializeResponse, initialize, auth)
	sessionID := initializeResponse.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatalf("initialize status/body = %d/%s", initializeResponse.Code, initializeResponse.Body.String())
	}

	otherAuth := mcpLegacyTestAuth("mcpt_other", "other-token")
	request := mcpLegacyTestRequest(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, "other-token")
	request.Header.Set("Mcp-Session-Id", sessionID)
	request.Header.Set("MCP-Protocol-Version", mcpInitializeProtocolVersion)
	response := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(response, request, otherAuth)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-token session status = %d, body = %s", response.Code, response.Body.String())
	}
}

func mcpLegacyTestAuth(tokenID, rawToken string) mcpAuthContext {
	return mcpAuthContext{
		User: domain.User{ID: "usr_test"},
		Token: domain.MCPToken{
			ID:              tokenID,
			UserID:          "usr_test",
			AccessTokenHash: hashToken(rawToken),
			Scopes:          []string{domain.MCPScopeDocsRead},
			AccessExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
}

func mcpLegacyTestRequest(body, rawToken string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://hank.example/v1/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+rawToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	return request
}
