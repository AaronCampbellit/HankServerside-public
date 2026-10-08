package cloud

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMCPModernDiscoveryAdvertisesOnlyCurrentProtocol(t *testing.T) {
	t.Parallel()

	server := &Server{}
	request := modernMCPRequest(`{"jsonrpc":"2.0","id":"discover-1","method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	request.Header.Set("Mcp-Method", "server/discover")
	response := httptest.NewRecorder()

	server.handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertMCPNoStoreHeaders(t, response.Header())
	var envelope struct {
		Result struct {
			ResultType        string         `json:"resultType"`
			SupportedVersions []string       `json:"supportedVersions"`
			Capabilities      map[string]any `json:"capabilities"`
			Meta              map[string]any `json:"_meta"`
			TTLMS             int            `json:"ttlMs"`
			CacheScope        string         `json:"cacheScope"`
			Instructions      string         `json:"instructions"`
		} `json:"result"`
	}
	decodeMCPTestResponse(t, response, &envelope)
	if envelope.Result.ResultType != "complete" {
		t.Fatalf("resultType = %q", envelope.Result.ResultType)
	}
	if got := strings.Join(envelope.Result.SupportedVersions, ","); got != mcpModernProtocolVersion {
		t.Fatalf("supportedVersions = %q", got)
	}
	tools, ok := envelope.Result.Capabilities["tools"].(map[string]any)
	if !ok || tools["listChanged"] != true {
		t.Fatalf("capabilities = %#v", envelope.Result.Capabilities)
	}
	serverInfo, _ := envelope.Result.Meta["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if serverInfo["name"] != "hank-mcp" || serverInfo["version"] != mcpServerVersion {
		t.Fatalf("serverInfo = %#v", serverInfo)
	}
	if envelope.Result.TTLMS != 0 || envelope.Result.CacheScope != "private" {
		t.Fatalf("cache hints = %d/%q", envelope.Result.TTLMS, envelope.Result.CacheScope)
	}
	if !strings.Contains(envelope.Result.Instructions, "Kanban") {
		t.Fatalf("instructions = %q", envelope.Result.Instructions)
	}
}

func TestMCPLegacyProtocolCannotDowngrade(t *testing.T) {
	t.Parallel()

	request := modernMCPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	request.Header.Set("MCP-Protocol-Version", "2025-11-25")
	request.Header.Set("Mcp-Method", "tools/list")
	response := httptest.NewRecorder()

	(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})

	var envelope struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				Requested string   `json:"requested"`
				Supported []string `json:"supported"`
			} `json:"data"`
		} `json:"error"`
	}
	decodeMCPTestResponse(t, response, &envelope)
	if response.Code != http.StatusBadRequest || envelope.Error.Code != mcpUnsupportedProtocolVersionCode {
		t.Fatalf("status/code = %d/%d, body = %s", response.Code, envelope.Error.Code, response.Body.String())
	}
	if envelope.Error.Data.Requested != "2025-11-25" || strings.Join(envelope.Error.Data.Supported, ",") != mcpModernProtocolVersion {
		t.Fatalf("unsupported version data = %#v", envelope.Error.Data)
	}
	assertMCPNoStoreHeaders(t, response.Header())
}

func TestMCPModernToolsListAndCallUseCompleteResults(t *testing.T) {
	t.Parallel()

	server := &Server{}
	listRequest := modernMCPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test-client","version":"1.0"},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
	listRequest.Header.Set("Mcp-Method", "tools/list")
	listResponse := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(listResponse, listRequest, mcpAuthContext{})

	var listed struct {
		Result struct {
			ResultType string           `json:"resultType"`
			Tools      []map[string]any `json:"tools"`
			Meta       map[string]any   `json:"_meta"`
			TTLMS      int              `json:"ttlMs"`
			CacheScope string           `json:"cacheScope"`
		} `json:"result"`
	}
	decodeMCPTestResponse(t, listResponse, &listed)
	if listResponse.Code != http.StatusOK || listed.Result.ResultType != "complete" || len(listed.Result.Tools) != 39 {
		t.Fatalf("tools/list status/result = %d/%#v", listResponse.Code, listed.Result)
	}
	if listed.Result.TTLMS != 0 || listed.Result.CacheScope != "private" {
		t.Fatalf("tools/list cache hints = %d/%q", listed.Result.TTLMS, listed.Result.CacheScope)
	}
	if _, ok := listed.Result.Meta["io.modelcontextprotocol/serverInfo"]; !ok {
		t.Fatalf("tools/list metadata = %#v", listed.Result.Meta)
	}

	callRequest := modernMCPRequest(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"does_not_exist","arguments":{}}}`)
	callRequest.Header.Set("Mcp-Method", "tools/call")
	callRequest.Header.Set("Mcp-Name", "does_not_exist")
	callResponse := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(callResponse, callRequest, mcpAuthContext{})

	var called struct {
		Result struct {
			ResultType string         `json:"resultType"`
			IsError    bool           `json:"isError"`
			Meta       map[string]any `json:"_meta"`
		} `json:"result"`
	}
	decodeMCPTestResponse(t, callResponse, &called)
	if callResponse.Code != http.StatusOK || called.Result.ResultType != "complete" || !called.Result.IsError {
		t.Fatalf("tools/call status/result = %d/%#v", callResponse.Code, called.Result)
	}
	if _, ok := called.Result.Meta["io.modelcontextprotocol/serverInfo"]; !ok {
		t.Fatalf("tools/call metadata = %#v", called.Result.Meta)
	}
}

func TestMCPKanbanToolDiscoveryIsAvailable(t *testing.T) {
	server := &Server{}
	request := modernMCPRequest(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	request.Header.Set("Mcp-Method", "tools/list")
	response := httptest.NewRecorder()
	server.handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
	if response.Code != http.StatusOK || !containsJSONText(response.Body.Bytes(), "open_kanban") || !containsJSONText(response.Body.Bytes(), mcpKanbanResourceURI) || !containsJSONText(response.Body.Bytes(), "io.modelcontextprotocol/serverInfo") {
		t.Fatalf("Kanban discovery status/body=%d/%s", response.Code, response.Body.String())
	}
}

func TestMCPModernRejectsInvalidTransportEnvelopes(t *testing.T) {
	t.Parallel()

	validBody := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	tests := []struct {
		name       string
		body       string
		version    string
		method     string
		wantStatus int
		wantCode   int
	}{
		{name: "batch", body: `[` + validBody + `]`, version: "2026-07-28", method: "tools/list", wantStatus: http.StatusBadRequest, wantCode: -32600},
		{name: "missing method header", body: validBody, version: "2026-07-28", wantStatus: http.StatusBadRequest, wantCode: -32020},
		{name: "mismatched method header", body: validBody, version: "2026-07-28", method: "tools/call", wantStatus: http.StatusBadRequest, wantCode: -32020},
		{name: "missing body protocol", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/clientCapabilities":{}}}}`, version: "2026-07-28", method: "tools/list", wantStatus: http.StatusBadRequest, wantCode: -32020},
		{name: "boolean id", body: `{"jsonrpc":"2.0","id":true,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, version: "2026-07-28", method: "tools/list", wantStatus: http.StatusBadRequest, wantCode: -32600},
		{name: "unsupported version", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01","io.modelcontextprotocol/clientCapabilities":{}}}}`, version: "2099-01-01", method: "tools/list", wantStatus: http.StatusBadRequest, wantCode: -32022},
		{name: "legacy ping in modern era", body: `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, version: "2026-07-28", method: "ping", wantStatus: http.StatusNotFound, wantCode: -32601},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := modernMCPRequest(test.body)
			request.Header.Set("MCP-Protocol-Version", test.version)
			if test.method != "" {
				request.Header.Set("Mcp-Method", test.method)
			}
			response := httptest.NewRecorder()
			(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})

			var envelope struct {
				Error struct {
					Code int `json:"code"`
					Data struct {
						Requested string   `json:"requested"`
						Supported []string `json:"supported"`
					} `json:"data"`
				} `json:"error"`
			}
			decodeMCPTestResponse(t, response, &envelope)
			if response.Code != test.wantStatus || envelope.Error.Code != test.wantCode {
				t.Fatalf("status/code = %d/%d, want %d/%d; body = %s", response.Code, envelope.Error.Code, test.wantStatus, test.wantCode, response.Body.String())
			}
			if test.wantCode == -32022 && (envelope.Error.Data.Requested != "2099-01-01" || strings.Join(envelope.Error.Data.Supported, ",") != mcpModernProtocolVersion) {
				t.Fatalf("unsupported version data = %#v", envelope.Error.Data)
			}
		})
	}
}

func TestMCPModernToolsCallRequiresMatchingNameHeader(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"", "other_tool"} {
		request := modernMCPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"list_docs","arguments":{}}}`)
		request.Header.Set("Mcp-Method", "tools/call")
		if header != "" {
			request.Header.Set("Mcp-Name", header)
		}
		response := httptest.NewRecorder()
		(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})

		var envelope struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		decodeMCPTestResponse(t, response, &envelope)
		if response.Code != http.StatusBadRequest || envelope.Error.Code != -32020 {
			t.Fatalf("header %q status/code = %d/%d, body = %s", header, response.Code, envelope.Error.Code, response.Body.String())
		}
	}
}

func TestMCPModernRequiresJSONAndSSEMediaTypes(t *testing.T) {
	t.Parallel()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	tests := []struct {
		name        string
		contentType string
		accept      string
		wantStatus  int
	}{
		{name: "wrong content type", contentType: "text/plain", accept: "application/json, text/event-stream", wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing event stream accept", contentType: "application/json", accept: "application/json", wantStatus: http.StatusNotAcceptable},
		{name: "missing json accept", contentType: "application/json", accept: "text/event-stream", wantStatus: http.StatusNotAcceptable},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := modernMCPRequest(body)
			request.Header.Set("Content-Type", test.contentType)
			request.Header.Set("Accept", test.accept)
			request.Header.Set("Mcp-Method", "tools/list")
			response := httptest.NewRecorder()
			(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestMCPModernRequiresCapabilitiesAndValidOptionalClientInfo(t *testing.T) {
	t.Parallel()

	tests := []string{
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`,
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":null}`,
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{}}`,
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"client"}}`,
	}
	for _, meta := range tests {
		request := modernMCPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":` + meta + `}}`)
		request.Header.Set("Mcp-Method", "tools/list")
		response := httptest.NewRecorder()
		(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("meta %s status = %d, body = %s", meta, response.Code, response.Body.String())
		}
	}
}

func TestMCPModernDecodesBase64NameHeader(t *testing.T) {
	t.Parallel()

	name := "mañana"
	request := modernMCPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"` + name + `","arguments":{}}}`)
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "=?base64?"+base64.StdEncoding.EncodeToString([]byte(name))+"?=")
	response := httptest.NewRecorder()
	(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestMCPModernRejectsUnencodedNonASCIINameHeader(t *testing.T) {
	t.Parallel()

	request := modernMCPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"mañana","arguments":{}}}`)
	request.Header.Set("Mcp-Method", "tools/call")
	request.Header.Set("Mcp-Name", "mañana")
	response := httptest.NewRecorder()
	(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
	var envelope struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	decodeMCPTestResponse(t, response, &envelope)
	if response.Code != http.StatusBadRequest || envelope.Error.Code != mcpHeaderMismatchCode {
		t.Fatalf("status/code = %d/%d, body = %s", response.Code, envelope.Error.Code, response.Body.String())
	}
}

func TestMCPModernRoutingHeadersAndBatchMetadataPreventLegacyDowngrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		body   string
		method string
	}{
		{name: "routing header", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, method: "tools/list"},
		{name: "batch metadata", body: `[{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}]`},
		{name: "modern version header", body: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := modernMCPRequest(test.body)
			if test.name != "modern version header" {
				request.Header.Del("MCP-Protocol-Version")
			}
			if test.method != "" {
				request.Header.Set("Mcp-Method", test.method)
			}
			response := httptest.NewRecorder()
			(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMCPModernAcceptsOptionalWhitespaceAroundRoutingHeaders(t *testing.T) {
	t.Parallel()

	request := modernMCPRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"name":"does_not_exist","arguments":{}}}`)
	request.Header.Set("Mcp-Method", " tools/call ")
	request.Header.Set("Mcp-Name", " does_not_exist ")
	response := httptest.NewRecorder()
	(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestMCPTransportAcceptsOneMaximumAttachmentChunk(t *testing.T) {
	encodedChunk := strings.Repeat("A", 5592408) // base64 length for exactly 4 MiB.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"padding":"` + encodedChunk + `"}}`
	request := modernMCPRequest(body)
	request.Header.Set("Mcp-Method", "tools/list")
	response := httptest.NewRecorder()

	(&Server{}).handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})

	if response.Code != http.StatusOK {
		t.Fatalf("maximum chunk envelope status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestMCPOriginValidationAllowsCanonicalOriginOnly(t *testing.T) {
	t.Parallel()

	server := &Server{mcpPublicBaseURL: "https://hank.example"}
	tests := map[string]bool{
		"":                          true,
		"https://hank.example":      true,
		"https://HANK.example":      true,
		"https://evil.example":      false,
		"http://hank.example":       false,
		"https://hank.example/path": false,
		"null":                      false,
	}
	for origin, want := range tests {
		request := httptest.NewRequest(http.MethodPost, "https://hank.example/v1/mcp", nil)
		request.Header.Set("Origin", origin)
		if got := server.mcpOriginAllowed(request); got != want {
			t.Errorf("origin %q allowed = %v, want %v", origin, got, want)
		}
	}
}

func modernMCPRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "https://hank.example/v1/mcp", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	return request
}

func decodeMCPTestResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, response.Body.String())
	}
}

func assertMCPNoStoreHeaders(t *testing.T, header http.Header) {
	t.Helper()
	if got := header.Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q", got)
	}
	if got := header.Get("Expires"); got != "0" {
		t.Fatalf("Expires = %q", got)
	}
}
