package cloud

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestMCPKanbanResourceIsAlwaysAvailable(t *testing.T) {
	server := &Server{}
	resources := server.mcpResourceList()
	if len(resources) != 1 || resources[0]["uri"] != mcpKanbanResourceURI || resources[0]["mimeType"] != mcpAppResourceMIME {
		t.Fatalf("resources = %#v", resources)
	}
	result, err := server.mcpResourceRead(mcpKanbanResourceURI)
	if err != nil {
		t.Fatal(err)
	}
	contents := result["contents"].([]map[string]any)
	if len(contents) != 1 || contents[0]["uri"] != mcpKanbanResourceURI || contents[0]["mimeType"] != mcpAppResourceMIME {
		t.Fatalf("contents = %#v", contents)
	}
	meta := contents[0]["_meta"].(map[string]any)
	ui := meta["ui"].(map[string]any)
	csp := ui["csp"].(map[string]any)
	empty := []string{}
	for _, key := range []string{"connectDomains", "resourceDomains", "frameDomains"} {
		if !reflect.DeepEqual(csp[key], empty) {
			t.Fatalf("CSP %s = %#v", key, csp[key])
		}
	}
	if meta["openai/widgetDescription"] == "" {
		t.Fatalf("metadata = %#v", meta)
	}
	if text, _ := contents[0]["text"].(string); text == "" {
		t.Fatal("resource HTML is empty")
	} else if embedded, err := fs.ReadFile(uiAssets, "ui/mcp/kanban-v1.html"); err != nil || !bytes.Equal([]byte(text), embedded) {
		t.Fatalf("resource bytes differ from embedded asset: %v", err)
	}
	if _, err := server.mcpResourceRead("ui://hank/other/v1"); err == nil {
		t.Fatal("unknown resource read succeeded")
	}
}

func TestMCPModernKanbanResourceListAndRead(t *testing.T) {
	server := &Server{}
	server.ConfigureMCP(MCPConfig{Enabled: true})
	for _, test := range []struct {
		method string
		body   string
		want   string
	}{
		{method: "resources/list", body: `{"jsonrpc":"2.0","id":1,"method":"resources/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, want: mcpKanbanResourceURI},
		{method: "resources/read", body: `{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"uri":"ui://hank/kanban/v1"}}`, want: mcpAppResourceMIME},
	} {
		request := modernMCPRequest(test.body)
		request.Header.Set("Mcp-Method", test.method)
		response := httptest.NewRecorder()
		server.handleMCPAuthenticatedEndpoint(response, request, mcpAuthContext{})
		if response.Code != http.StatusOK || !containsJSONText(response.Body.Bytes(), test.want) {
			t.Fatalf("%s status/body = %d/%s", test.method, response.Code, response.Body.String())
		}
	}
}

func containsJSONText(data []byte, want string) bool {
	var value any
	if json.Unmarshal(data, &value) != nil {
		return false
	}
	encoded, _ := json.Marshal(value)
	return len(encoded) > 0 && stringContains(string(encoded), want)
}

func stringContains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
