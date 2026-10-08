package cloud

import (
	"errors"
)

const (
	mcpKanbanResourceURI = "ui://hank/kanban/v1"
	mcpAppResourceMIME   = "text/html;profile=mcp-app"
)

var errMCPResourceNotFound = errors.New("MCP resource not found")

func (s *Server) mcpResourceList() []map[string]any {
	return []map[string]any{{
		"uri":         mcpKanbanResourceURI,
		"name":        "Hank Kanban",
		"title":       "Hank Kanban",
		"description": "Interactive access to the authenticated user's MCP-visible Hank Kanban boards.",
		"mimeType":    mcpAppResourceMIME,
	}}
}

func (s *Server) mcpResourceRead(uri string) (map[string]any, error) {
	if uri != mcpKanbanResourceURI {
		return nil, errMCPResourceNotFound
	}
	html, err := readEmbeddedUIFile("mcp/kanban-v1.html")
	if err != nil {
		return nil, err
	}
	emptyDomains := []string{}
	return map[string]any{"contents": []map[string]any{{
		"uri":      mcpKanbanResourceURI,
		"mimeType": mcpAppResourceMIME,
		"text":     string(html),
		"_meta": map[string]any{
			"ui": map[string]any{
				"prefersBorder": true,
				"csp": map[string]any{
					"connectDomains":  emptyDomains,
					"resourceDomains": emptyDomains,
					"frameDomains":    emptyDomains,
				},
			},
			"openai/widgetDescription": "An interactive Hank Kanban board for viewing and managing the user's MCP-visible cards.",
		},
	}}}, nil
}

func mcpResourceURI(raw []byte) string {
	var params struct {
		URI string `json:"uri"`
	}
	if decodeMCPToolArgs(raw, &params) != nil {
		return ""
	}
	return params.URI
}
