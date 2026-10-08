package cloud

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpInitializeProtocolVersion = "2025-11-25"

type mcpLegacySessionBinding struct {
	ProtocolVersion string
	TokenID         string
	ExpiresAt       time.Time
}

type mcpLegacyAuthContextKey struct{}
type mcpLegacyInitializeVersionKey struct{}

type mcpNoStoreResponseWriter struct {
	http.ResponseWriter
}

func (w *mcpNoStoreResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *mcpNoStoreResponseWriter) WriteHeader(statusCode int) {
	setMCPNoStoreHeaders(w.Header())
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *mcpNoStoreResponseWriter) Write(body []byte) (int, error) {
	setMCPNoStoreHeaders(w.Header())
	return w.ResponseWriter.Write(body)
}

func (s *Server) mcpLegacySDKHandler() http.Handler {
	s.mcpLegacyHandlerOnce.Do(func() {
		transport := mcp.NewStreamableHTTPHandler(s.newMCPLegacySDKServer, &mcp.StreamableHTTPOptions{
			JSONResponse:        true,
			Logger:              s.logger,
			SessionTimeout:      time.Hour,
			MaxRequestBodyBytes: maxMCPHTTPBodyBytes,
		})
		verifier := func(ctx context.Context, rawToken string, _ *http.Request) (*mcpauth.TokenInfo, error) {
			auth, ok := ctx.Value(mcpLegacyAuthContextKey{}).(mcpAuthContext)
			if !ok || auth.Token.ID == "" || subtle.ConstantTimeCompare([]byte(hashToken(rawToken)), []byte(auth.Token.AccessTokenHash)) != 1 {
				return nil, mcpauth.ErrInvalidToken
			}
			return &mcpauth.TokenInfo{
				Scopes:     append([]string(nil), auth.Token.Scopes...),
				Expiration: auth.Token.AccessExpiresAt,
				UserID:     auth.Token.ID,
			}, nil
		}
		s.mcpLegacyHandler = mcpauth.RequireBearerToken(verifier, nil)(transport)
	})
	return s.mcpLegacyHandler
}

func (s *Server) newMCPLegacySDKServer(r *http.Request) *mcp.Server {
	auth, ok := r.Context().Value(mcpLegacyAuthContextKey{}).(mcpAuthContext)
	if !ok || auth.Token.ID == "" {
		return nil
	}
	requestedVersion, _ := r.Context().Value(mcpLegacyInitializeVersionKey{}).(string)
	protocolVersion := mcpInitializeProtocolVersion
	if mcpLegacyProtocolVersionSupported(requestedVersion) {
		protocolVersion = requestedVersion
	}
	sessionID := newID("mcpsess")
	s.mcpLegacySessionsMu.Lock()
	if s.mcpLegacySessions == nil {
		s.mcpLegacySessions = make(map[string]mcpLegacySessionBinding)
	}
	s.mcpLegacySessions[sessionID] = mcpLegacySessionBinding{
		ProtocolVersion: protocolVersion,
		TokenID:         auth.Token.ID,
		ExpiresAt:       auth.Token.AccessExpiresAt,
	}
	s.mcpLegacySessionsMu.Unlock()

	server := mcp.NewServer(&mcp.Implementation{Name: "hank-mcp", Version: mcpServerVersion}, &mcp.ServerOptions{
		Instructions: mcpServerInstructions(),
		Logger:       s.logger,
		Capabilities: &mcp.ServerCapabilities{},
		GetSessionID: func() string { return sessionID },
	})
	for _, def := range mcpToolDefs() {
		def := def
		server.AddTool(mcpLegacyToolDefinition(def), func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if !strings.HasPrefix(def.Name, "fleet_") && !mcpAuthHasAnyScope(auth, def.Scopes) {
				return mcpLegacyToolError("This connection is not authorized for " + def.Name + " (missing required scope)."), nil
			}
			result, err := s.executeMCPTool(ctx, auth, def.Name, request.Params.Arguments)
			if err != nil {
				return mcpLegacyExecutionFailure(err), nil
			}
			return mcpLegacyExecutionResult(result), nil
		})
	}
	server.AddResource(&mcp.Resource{
		URI:         mcpKanbanResourceURI,
		Name:        "Hank Kanban",
		Title:       "Hank Kanban",
		Description: "Interactive access to the authenticated user's MCP-visible Hank Kanban boards.",
		MIMEType:    mcpAppResourceMIME,
	}, func(_ context.Context, request *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		result, err := s.mcpResourceRead(request.Params.URI)
		if errors.Is(err, errMCPResourceNotFound) {
			return nil, mcp.ResourceNotFoundError(request.Params.URI)
		}
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		var decoded struct {
			Contents []*mcp.ResourceContents `json:"contents"`
		}
		if err := json.Unmarshal(data, &decoded); err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: decoded.Contents}, nil
	})
	return server
}

func mcpLegacyToolDefinition(def mcpToolDef) *mcp.Tool {
	meta := make(mcp.Meta, len(def.Meta)+1)
	for key, value := range def.Meta {
		meta[key] = value
	}
	if schemes := mcpOAuthSecuritySchemes(def.Scopes); len(schemes) > 0 {
		meta["securitySchemes"] = schemes
	}
	annotations := &mcp.ToolAnnotations{}
	if value, ok := def.Annotations["readOnlyHint"].(bool); ok {
		annotations.ReadOnlyHint = value
	}
	if value, ok := def.Annotations["destructiveHint"].(bool); ok {
		annotations.DestructiveHint = &value
	}
	if value, ok := def.Annotations["openWorldHint"].(bool); ok {
		annotations.OpenWorldHint = &value
	}
	if value, ok := def.Annotations["idempotentHint"].(bool); ok {
		annotations.IdempotentHint = value
	}
	if len(def.Annotations) == 0 {
		annotations = nil
	}
	return &mcp.Tool{
		Meta:         meta,
		Annotations:  annotations,
		Name:         def.Name,
		Title:        mcpToolTitle(def),
		Description:  def.Description,
		InputSchema:  def.InputSchema,
		OutputSchema: def.OutputSchema,
	}
}

func mcpLegacyExecutionResult(result mcpToolExecution) *mcp.CallToolResult {
	content := make([]mcp.Content, 0, max(1, len(result.Content)))
	for _, item := range result.Content {
		if item["type"] == "text" {
			text, _ := item["text"].(string)
			content = append(content, &mcp.TextContent{Text: text})
		}
	}
	if len(content) == 0 {
		content = append(content, &mcp.TextContent{Text: result.Text})
	}
	return &mcp.CallToolResult{
		Meta:              mcp.Meta(result.Meta),
		Content:           content,
		StructuredContent: result.StructuredContent,
	}
}

func mcpLegacyExecutionFailure(err error) *mcp.CallToolResult {
	result := mcpLegacyToolError(mcpFriendlyError(err))
	var attachmentErr *mcpAttachmentError
	if errors.As(err, &attachmentErr) {
		if structured, marshalErr := mcpStructuredContent(attachmentErr); marshalErr == nil {
			result.StructuredContent = structured
		}
	}
	return result
}

func mcpLegacyToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
		IsError: true,
	}
}

func mcpLegacyProtocolVersionSupported(version string) bool {
	switch version {
	case "2024-11-05", "2025-03-26", "2025-06-18", mcpInitializeProtocolVersion:
		return true
	default:
		return false
	}
}

func mcpLegacyInitializeVersion(body []byte) (string, bool) {
	var request struct {
		Method string `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &request) != nil || request.Method != "initialize" {
		return "", false
	}
	return request.Params.ProtocolVersion, true
}

func (s *Server) mcpValidateLegacySession(w http.ResponseWriter, r *http.Request, auth mcpAuthContext) bool {
	sessionID := r.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		http.Error(w, "Mcp-Session-Id is required", http.StatusBadRequest)
		return false
	}
	now := time.Now().UTC()
	s.mcpLegacySessionsMu.Lock()
	binding, ok := s.mcpLegacySessions[sessionID]
	if ok && (!binding.ExpiresAt.IsZero() && !binding.ExpiresAt.After(now)) {
		delete(s.mcpLegacySessions, sessionID)
		ok = false
	}
	if r.Method == http.MethodDelete && ok {
		delete(s.mcpLegacySessions, sessionID)
	}
	s.mcpLegacySessionsMu.Unlock()
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return false
	}
	if binding.TokenID != auth.Token.ID {
		http.Error(w, "session user mismatch", http.StatusForbidden)
		return false
	}
	if got := r.Header.Get("MCP-Protocol-Version"); got != binding.ProtocolVersion {
		writeJSON(w, http.StatusBadRequest, jsonrpcErrorResponse(nil, -32020, "MCP-Protocol-Version must match the negotiated session version"))
		return false
	}
	return true
}

func (s *Server) serveMCPLegacy(w http.ResponseWriter, r *http.Request, auth mcpAuthContext, initializeVersion string) {
	ctx := context.WithValue(r.Context(), mcpLegacyAuthContextKey{}, auth)
	if initializeVersion != "" {
		ctx = context.WithValue(ctx, mcpLegacyInitializeVersionKey{}, initializeVersion)
	}
	s.mcpLegacySDKHandler().ServeHTTP(&mcpNoStoreResponseWriter{ResponseWriter: w}, r.WithContext(ctx))
}
