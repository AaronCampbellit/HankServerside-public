package cloud

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	mcpHeaderMismatchCode             = -32020
	mcpUnsupportedProtocolVersionCode = -32022
	// Tool and resource metadata can change between deployments. Keep discovery
	// immediately stale so clients re-fetch instead of retaining an older catalog.
	mcpCacheTTLMilliseconds = 0
)

type mcpModernRequestParams struct {
	Meta          json.RawMessage `json:"_meta"`
	Name          string          `json:"name"`
	URI           string          `json:"uri"`
	Arguments     json.RawMessage `json:"arguments"`
	Notifications json.RawMessage `json:"notifications"`
}

type mcpModernRequestMeta struct {
	ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion"`
	ClientInfo         json.RawMessage `json:"io.modelcontextprotocol/clientInfo"`
	ClientCapabilities json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities"`
}

func (s *Server) handleMCPModernRequest(w http.ResponseWriter, r *http.Request, auth mcpAuthContext, body []byte) {
	if contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || contentType != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, jsonrpcErrorResponse(nil, -32600, "Content-Type must be application/json"))
		return
	}
	if !mcpAcceptsModernResponses(r.Header.Values("Accept")) {
		writeJSON(w, http.StatusNotAcceptable, jsonrpcErrorResponse(nil, -32600, "Accept must include application/json and text/event-stream"))
		return
	}
	if len(body) == 0 || body[0] == '[' {
		writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(nil, -32600, "JSON-RPC batches are not supported"))
		return
	}

	var request jsonrpcRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(nil, -32700, "parse error"))
		return
	}
	if request.JSONRPC != "2.0" || request.Method == "" || !mcpValidRequestID(request.ID) {
		writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(request.ID, -32600, "invalid request"))
		return
	}

	headerVersion := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
	bodyVersion := mcpModernBodyProtocolVersion(request.Params)
	if headerVersion == "" || bodyVersion == "" || headerVersion != bodyVersion {
		writeMCPHeaderMismatch(w, request.ID, "MCP-Protocol-Version does not match request metadata")
		return
	}
	params, meta, ok := decodeMCPModernParams(request.Params)
	if !ok {
		writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(request.ID, -32602, "required request metadata is missing or malformed"))
		return
	}
	if meta.ProtocolVersion != mcpModernProtocolVersion {
		writeJSON(w, http.StatusBadRequest, mcpUnsupportedProtocolVersionResponse(request.ID, meta.ProtocolVersion))
		return
	}
	if strings.TrimSpace(r.Header.Get("Mcp-Method")) != request.Method {
		writeMCPHeaderMismatch(w, request.ID, "Mcp-Method does not match request method")
		return
	}
	if request.Method == "tools/call" {
		name, valid := decodeMCPHeaderValue(r.Header.Get("Mcp-Name"))
		if !valid || name == "" || name != params.Name {
			writeMCPHeaderMismatch(w, request.ID, "Mcp-Name does not match tool name")
			return
		}
	}
	if request.Method == "subscriptions/listen" {
		s.handleMCPSubscription(w, r, auth, request.ID, params.Notifications)
		return
	}

	var result map[string]any
	switch request.Method {
	case "server/discover":
		result = s.mcpDiscoveryResult()
	case "tools/list":
		result = map[string]any{
			"resultType": "complete",
			"tools":      s.mcpToolList(),
			"ttlMs":      mcpCacheTTLMilliseconds,
			"cacheScope": "private",
			"_meta":      mcpModernResultMeta(),
		}
	case "tools/call":
		result = s.mcpToolsCall(r.Context(), auth, request.Params)
		result["resultType"] = "complete"
		mcpMergeResultMeta(result, mcpModernResultMeta())
	case "resources/list":
		result = map[string]any{
			"resultType": "complete",
			"resources":  s.mcpResourceList(),
			"_meta":      mcpModernResultMeta(),
		}
	case "resources/read":
		var err error
		result, err = s.mcpResourceRead(params.URI)
		if err != nil {
			writeJSON(w, http.StatusNotFound, jsonrpcErrorResponse(request.ID, -32002, "Resource not found"))
			return
		}
		result["resultType"] = "complete"
		mcpMergeResultMeta(result, mcpModernResultMeta())
	default:
		writeJSON(w, http.StatusNotFound, jsonrpcErrorResponse(request.ID, -32601, "Method not found: "+request.Method))
		return
	}
	writeJSON(w, http.StatusOK, jsonrpcResultResponse(request.ID, result))
}

func mcpModernBodyProtocolVersion(raw json.RawMessage) string {
	var params struct {
		Meta struct {
			ProtocolVersion string `json:"io.modelcontextprotocol/protocolVersion"`
		} `json:"_meta"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return ""
	}
	return strings.TrimSpace(params.Meta.ProtocolVersion)
}

func mcpValidRequestID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return false
	}
	var value any
	if json.Unmarshal(trimmed, &value) != nil {
		return false
	}
	switch value.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func decodeMCPModernParams(raw json.RawMessage) (mcpModernRequestParams, mcpModernRequestMeta, bool) {
	var params mcpModernRequestParams
	if len(raw) == 0 || json.Unmarshal(raw, &params) != nil || len(params.Meta) == 0 {
		return mcpModernRequestParams{}, mcpModernRequestMeta{}, false
	}
	var meta mcpModernRequestMeta
	if json.Unmarshal(params.Meta, &meta) != nil || meta.ProtocolVersion == "" || !mcpJSONObject(meta.ClientCapabilities) {
		return mcpModernRequestParams{}, mcpModernRequestMeta{}, false
	}
	if len(meta.ClientInfo) > 0 {
		var clientInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if !mcpJSONObject(meta.ClientInfo) || json.Unmarshal(meta.ClientInfo, &clientInfo) != nil || strings.TrimSpace(clientInfo.Name) == "" || strings.TrimSpace(clientInfo.Version) == "" {
			return mcpModernRequestParams{}, mcpModernRequestMeta{}, false
		}
	}
	return params, meta, true
}

func mcpJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return false
	}
	var value map[string]any
	return json.Unmarshal(trimmed, &value) == nil
}

func mcpAcceptsModernResponses(values []string) bool {
	hasJSON := false
	hasSSE := false
	for _, part := range strings.Split(strings.Join(values, ","), ",") {
		mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		switch mediaType {
		case "application/json":
			hasJSON = true
		case "text/event-stream":
			hasSSE = true
		}
	}
	return hasJSON && hasSSE
}

func (s *Server) mcpDiscoveryResult() map[string]any {
	capabilities := map[string]any{
		"tools":     map[string]any{"listChanged": true},
		"resources": map[string]any{"subscribe": false, "listChanged": false},
	}
	return map[string]any{
		"resultType":        "complete",
		"supportedVersions": append([]string(nil), mcpAdvertisedProtocolVersions...),
		"capabilities":      capabilities,
		"_meta":             mcpModernResultMeta(),
		"instructions":      mcpServerInstructions(),
		"ttlMs":             mcpCacheTTLMilliseconds,
		"cacheScope":        "private",
	}
}

func mcpModernResultMeta() map[string]any {
	return map[string]any{"io.modelcontextprotocol/serverInfo": mcpServerInfo()}
}

func mcpMergeResultMeta(result map[string]any, additions map[string]any) {
	meta, _ := result["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	for key, value := range additions {
		meta[key] = value
	}
	result["_meta"] = meta
}

func writeMCPHeaderMismatch(w http.ResponseWriter, id json.RawMessage, message string) {
	writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(id, mcpHeaderMismatchCode, "Header mismatch: "+message))
}

func mcpUnsupportedProtocolVersionResponse(id json.RawMessage, requested string) map[string]any {
	response := jsonrpcErrorResponse(id, mcpUnsupportedProtocolVersionCode, "Unsupported protocol version")
	errorBody := response["error"].(map[string]any)
	errorBody["data"] = map[string]any{
		"supported": append([]string(nil), mcpAdvertisedProtocolVersions...),
		"requested": requested,
	}
	return response
}

func decodeMCPHeaderValue(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "=?base64?") || !strings.HasSuffix(value, "?=") {
		if value == "" {
			return "", false
		}
		for _, char := range []byte(value) {
			if char < 0x20 || char > 0x7e {
				return "", false
			}
		}
		return value, true
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(value, "=?base64?"), "?=")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !utf8.Valid(decoded) {
		return "", false
	}
	return string(decoded), true
}

func (s *Server) mcpOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil || parsedOrigin.Scheme == "" || parsedOrigin.Host == "" || parsedOrigin.User != nil || parsedOrigin.Path != "" || parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
		return false
	}
	base, err := url.Parse(s.mcpBaseURL(r))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsedOrigin.Scheme, base.Scheme) && strings.EqualFold(parsedOrigin.Host, base.Host)
}
