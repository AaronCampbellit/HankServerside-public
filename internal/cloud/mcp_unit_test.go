package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

// These tests cover the DB-independent MCP logic and run without Postgres.

func TestMCPToolSuccessPreservesOrderedContentBlocks(t *testing.T) {
	result := mcpToolSuccess(mcpToolExecution{Content: []map[string]any{
		{"type": "text", "text": "preview"},
		{"type": "image", "data": "aW1hZ2U=", "mimeType": "image/png"},
	}})
	content, ok := result["content"].([]map[string]any)
	if !ok || len(content) != 2 || content[0]["text"] != "preview" || content[1]["type"] != "image" {
		t.Fatalf("content = %#v", result["content"])
	}
}

func TestMCPAttachmentToolsAreAdvertisedWithExpectedScopes(t *testing.T) {
	want := map[string]string{
		"list_note_attachments":         domain.NotesAPIScopeRead,
		"read_note_attachment":          domain.NotesAPIScopeRead,
		"start_note_attachment_upload":  domain.NotesAPIScopeWrite,
		"get_note_attachment_upload":    domain.NotesAPIScopeWrite,
		"upload_note_attachment_chunk":  domain.NotesAPIScopeWrite,
		"finish_note_attachment_upload": domain.NotesAPIScopeWrite,
		"abort_note_attachment_upload":  domain.NotesAPIScopeWrite,
	}
	for _, def := range mcpToolDefs() {
		scope, ok := want[def.Name]
		if !ok {
			continue
		}
		if !reflect.DeepEqual(def.Scopes, []string{scope}) {
			t.Fatalf("%s scopes = %v", def.Name, def.Scopes)
		}
		if len(def.InputSchema) == 0 || len(def.OutputSchema) == 0 || len(def.Annotations) == 0 {
			t.Fatalf("%s is missing schema or annotations", def.Name)
		}
		delete(want, def.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing attachment tools: %v", want)
	}
}

func TestMCPAttachmentErrorsReturnStableStructuredData(t *testing.T) {
	result := mcpToolFailure(&mcpAttachmentError{Code: "offset_conflict", Message: "wrong offset", NextOffset: 42})
	if result["isError"] != true {
		t.Fatalf("result = %#v", result)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["code"] != "offset_conflict" || structured["next_offset"] != float64(42) {
		t.Fatalf("structured error = %#v", result["structuredContent"])
	}
}

func TestMCPVerifyPKCES256(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk1234567890abcdef"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if !verifyPKCES256(verifier, challenge) {
		t.Fatalf("expected PKCE verification to pass")
	}
	if verifyPKCES256("wrong-verifier", challenge) {
		t.Fatalf("expected PKCE verification to fail for wrong verifier")
	}
	if verifyPKCES256(verifier, "not-the-challenge") {
		t.Fatalf("expected PKCE verification to fail for wrong challenge")
	}
}

func TestMCPScopeHelpers(t *testing.T) {
	got := mcpFilterScopes([]string{"notes:read", "bogus", "notes:read", "docs:read", ""})
	want := []string{"notes:read", "docs:read"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("mcpFilterScopes = %v, want %v", got, want)
	}
	if mcpParseScopeParam("  ") != nil {
		t.Fatalf("empty scope should parse to nil")
	}
	inter := mcpIntersectScopes([]string{"a", "notes:read", "notes:write"}, []string{"notes:read"})
	if strings.Join(inter, ",") != "notes:read" {
		t.Fatalf("intersect = %v", inter)
	}
	auth := mcpAuthContext{Token: domain.MCPToken{Scopes: []string{"notes:append"}}}
	if !mcpAuthHasAnyScope(auth, []string{"notes:append", "notes:write"}) {
		t.Fatalf("expected any-of scope match")
	}
	if mcpAuthHasAnyScope(auth, []string{"notes:delete"}) {
		t.Fatalf("did not expect delete scope")
	}
}

func TestMCPValidRedirectURI(t *testing.T) {
	cases := map[string]bool{
		"https://chatgpt.com/connector_platform_oauth_redirect": true,
		"https://claude.ai/api/mcp/auth_callback":               true,
		"http://localhost:8080/callback":                        true,
		"http://127.0.0.1/cb":                                   true,
		"http://evil.example.com/cb":                            false,
		"https://user@client.example/cb":                        false,
		"claudeai://mcp/callback":                               false,
		"":                                                      false,
		"https://host/cb#frag":                                  false,
	}
	for uri, want := range cases {
		if got := mcpValidRedirectURI(uri); got != want {
			t.Errorf("mcpValidRedirectURI(%q) = %v, want %v", uri, got, want)
		}
	}
}

func TestMCPAuthServerMetadataAdvertisesIssuerResponses(t *testing.T) {
	t.Parallel()

	server := &Server{mcpEnabled: true, mcpPublicBaseURL: "https://hank.example"}
	request := httptest.NewRequest(http.MethodGet, "https://hank.example/.well-known/oauth-authorization-server", nil)
	response := httptest.NewRecorder()
	server.handleMCPAuthServerMetadata(response, request)

	var metadata map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if response.Code != http.StatusOK || metadata["issuer"] != "https://hank.example" || metadata["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("status/metadata = %d/%#v", response.Code, metadata)
	}
}

func TestMCPResourceMatchesCanonicalEndpoint(t *testing.T) {
	t.Parallel()

	server := &Server{mcpPublicBaseURL: "https://hank.example"}
	request := httptest.NewRequest(http.MethodPost, "https://internal.example/v1/oauth/mcp/token", nil)
	tests := map[string]bool{
		"https://hank.example/v1/mcp":  true,
		"":                             false,
		"https://hank.example":         false,
		"https://hank.example/v1/mcp/": false,
		"https://evil.example/v1/mcp":  false,
	}
	for resource, want := range tests {
		if got := server.mcpResourceMatches(request, resource); got != want {
			t.Errorf("resource %q matches = %v, want %v", resource, got, want)
		}
	}
}

func TestMCPRedirectErrorIncludesIssuer(t *testing.T) {
	t.Parallel()

	server := &Server{mcpPublicBaseURL: "https://hank.example"}
	request := httptest.NewRequest(http.MethodGet, "https://hank.example/v1/oauth/mcp/authorize", nil)
	response := httptest.NewRecorder()
	server.mcpRedirectError(response, request, "https://client.example/callback", "state-1", "invalid_target", "wrong resource")

	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	if response.Code != http.StatusFound || location.Query().Get("error") != "invalid_target" || location.Query().Get("state") != "state-1" || location.Query().Get("iss") != "https://hank.example" {
		t.Fatalf("status/location = %d/%s", response.Code, location.String())
	}
}

func TestMCPConsentScriptEndpoint(t *testing.T) {
	if mcpConsentScriptPath == "/v1/oauth/mcp/consent.js" {
		t.Fatal("consent script path must not reuse the cached v1 asset URL")
	}
	server := &Server{mcpEnabled: true}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		server.handleMCPConsentScript(response, httptest.NewRequest(method, mcpConsentScriptPath, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "application/javascript") {
			t.Fatalf("%s status/content-type = %d/%q", method, response.Code, response.Header().Get("Content-Type"))
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
			t.Fatalf("%s cache-control = %q", method, got)
		}
		if got := response.Header().Get("Cloudflare-CDN-Cache-Control"); got != "no-store" {
			t.Fatalf("%s cloudflare cache-control = %q", method, got)
		}
		if method == http.MethodGet && !strings.Contains(response.Body.String(), "installMCPConsentForm") {
			t.Fatalf("script body = %q", response.Body.String())
		}
		if method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatalf("HEAD returned a body: %q", response.Body.String())
		}
	}

	rejected := httptest.NewRecorder()
	server.handleMCPConsentScript(rejected, httptest.NewRequest(http.MethodPost, mcpConsentScriptPath, nil))
	if rejected.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", rejected.Code)
	}

	disabled := httptest.NewRecorder()
	(&Server{}).handleMCPConsentScript(disabled, httptest.NewRequest(http.MethodGet, mcpConsentScriptPath, nil))
	if disabled.Code != http.StatusNotFound {
		t.Fatalf("disabled status = %d", disabled.Code)
	}
}

func TestMCPConsentTemplateLoadsSingleUseScript(t *testing.T) {
	var page strings.Builder
	err := mcpConsentTemplate.Execute(&page, map[string]any{
		"ClientName":        "ChatGPT",
		"UserEmail":         "person@example.com",
		"Scopes":            []mcpConsentScope{{Key: domain.NotesAPIScopeRead, Label: "Read your notes", Checked: true}},
		"CSRF":              "csrf-token",
		"AuthorizePath":     mcpAuthorizePath,
		"ConsentScriptPath": mcpConsentScriptPath,
		"Params":            mcpAuthorizeParams{ClientID: "client-id", RedirectURI: "https://chatgpt.com/callback"},
	})
	if err != nil {
		t.Fatalf("render consent template: %v", err)
	}
	body := page.String()
	for _, required := range []string{
		`type="module"`,
		fmt.Sprintf(`src="%s"`, mcpConsentScriptPath),
		`data-mcp-consent-form`,
		`name="decision" value="deny"`,
		`name="decision" value="allow"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("consent template missing %q", required)
		}
	}
}

func TestMCPConsentAllowsValidatedCallbackOriginInFormAction(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://hank.example/v1/oauth/mcp/authorize", nil)

	(&Server{}).renderMCPConsent(
		response,
		request,
		domain.MCPOAuthClient{ClientName: "ChatGPT"},
		domain.User{Email: "person@example.com"},
		mcpAuthorizeParams{RedirectURI: "https://chatgpt.com/connector/oauth/callback-id"},
		[]string{domain.NotesAPIScopeRead},
	)

	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action 'self' https://chatgpt.com") {
		t.Fatalf("consent CSP does not permit the validated OAuth callback origin: %q", csp)
	}
	if strings.Contains(csp, "/connector/oauth/") {
		t.Fatalf("consent CSP should allow only the callback origin, not interpolate its path: %q", csp)
	}
}

func TestMCPDocsIndex(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "README.md"), "# Readme\nThe notes api token lives here\n")
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "# Agents\ncanonical guidance\n")
	mustWrite(t, filepath.Join(root, "docs", "architecture.md"), "# Arch\nthe notes service design\n")
	mustWrite(t, filepath.Join(root, "docs", "product.md"), "# Product\ncanonical product\n")
	mustWrite(t, filepath.Join(root, "docs", "superpowers", "plans", "old.md"), "# Old plan\nstale knowledge\n")
	mustWrite(t, filepath.Join(root, ".env.cloud"), "SECRET=should-never-be-exposed\n")
	mustWrite(t, filepath.Join(root, "docs", "secret.bin"), "binary")
	outside := filepath.Join(t.TempDir(), "outside.md")
	mustWrite(t, outside, "outside secret")
	if err := os.Symlink(outside, filepath.Join(root, "docs", "outside.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// code-reference/ source snapshot: .go is exposed, but non-text files are not.
	mustWrite(t, filepath.Join(root, "code-reference", "internal", "cloud", "server.go"), "package cloud\n// the notes handler lives here\n")
	mustWrite(t, filepath.Join(root, "code-reference", "internal", "cloud", "server.test.bin"), "binary")

	idx := newMCPDocsIndex(root)
	paths := idx.listPaths()
	joined := strings.Join(paths, ",")
	if !strings.Contains(joined, "README.md") || !strings.Contains(joined, "AGENTS.md") || !strings.Contains(joined, "docs/architecture.md") || !strings.Contains(joined, "docs/product.md") {
		t.Fatalf("listPaths missing expected docs: %v", paths)
	}
	if !strings.Contains(joined, "code-reference/internal/cloud/server.go") {
		t.Fatalf("listPaths missing code-reference source: %v", paths)
	}
	if strings.Contains(joined, ".env.cloud") || strings.Contains(joined, "secret.bin") || strings.Contains(joined, "server.test.bin") || strings.Contains(joined, "outside.md") || strings.Contains(joined, "docs/superpowers/plans/old.md") {
		t.Fatalf("listPaths exposed disallowed file: %v", paths)
	}

	body, err := idx.read("README.md")
	if err != nil || !strings.Contains(body, "Readme") {
		t.Fatalf("read README: %v / %q", err, body)
	}
	if body, err := idx.read("code-reference/internal/cloud/server.go"); err != nil || !strings.Contains(body, "package cloud") {
		t.Fatalf("read code-reference source: %v / %q", err, body)
	}
	if _, err := idx.read("../etc/passwd"); err == nil {
		t.Fatalf("expected path traversal to be refused")
	}
	if _, err := idx.read(".env.cloud"); err == nil {
		t.Fatalf("expected non-allowlisted file to be refused")
	}
	if _, err := idx.read("docs/outside.md"); err == nil {
		t.Fatalf("expected symlinked document to be refused")
	}
	if _, err := idx.read("docs/superpowers/plans/old.md"); err == nil {
		t.Fatalf("expected temporary plan document to be refused")
	}
	out, err := idx.search("notes", 5)
	if err != nil || !strings.Contains(out, "README.md") {
		t.Fatalf("search: %v / %q", err, out)
	}
	if _, err := idx.search("  ", 5); err == nil {
		t.Fatalf("expected empty query to error")
	}
}

func TestMCPToolListAndLookup(t *testing.T) {
	defs := mcpToolDefs()
	server := &Server{}
	if len(defs) != 39 {
		t.Fatalf("expected 39 definitions, got %d", len(defs))
	}
	if len(server.mcpToolList()) != 39 {
		t.Fatalf("server advertised %d tools", len(server.mcpToolList()))
	}
	for _, name := range []string{"list_docs", "search_docs", "read_doc", "create_note", "delete_note", "append_note", "list_context_sources", "list_context_files", "search_context", "read_context_file", "list_note_attachments", "read_note_attachment", "start_note_attachment_upload", "get_note_attachment_upload", "upload_note_attachment_chunk", "finish_note_attachment_upload", "abort_note_attachment_upload", "open_kanban", "list_kanban_boards", "list_kanban_cards", "get_kanban_card", "create_kanban_card", "update_kanban_card", "append_kanban_worklog", "move_kanban_card", "delete_kanban_card"} {
		if _, ok := server.mcpToolByName(name); !ok {
			t.Fatalf("tool %q not found", name)
		}
	}
	if _, ok := server.mcpToolByName("nope"); ok {
		t.Fatalf("did not expect to find bogus tool")
	}
	// append_note must accept either append or write scope.
	def, _ := server.mcpToolByName("append_note")
	if len(def.Scopes) != 2 {
		t.Fatalf("append_note scopes = %v", def.Scopes)
	}
	for _, tool := range server.mcpToolList() {
		schema, _ := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Fatalf("tool %v has non-object inputSchema", tool["name"])
		}
		name, _ := tool["name"].(string)
		if title, _ := tool["title"].(string); strings.TrimSpace(title) == "" {
			t.Fatalf("tool %s has no human-readable title", name)
		}
		annotations, _ := tool["annotations"].(map[string]any)
		for _, required := range []string{"readOnlyHint", "destructiveHint", "openWorldHint"} {
			if _, ok := annotations[required].(bool); !ok {
				t.Fatalf("tool %s is missing boolean annotation %s: %#v", name, required, annotations)
			}
		}
		if annotations["openWorldHint"] != (name == "fleet_job_start") {
			t.Fatalf("closed-world Hank tool %s advertises open-world access: %#v", name, annotations)
		}
		if strings.Contains(name, "kanban") {
			readOnly, _ := annotations["readOnlyHint"].(bool)
			isRead := strings.HasPrefix(name, "list_") || strings.HasPrefix(name, "get_") || name == "open_kanban"
			if readOnly != isRead {
				t.Fatalf("tool %s annotations = %#v", name, annotations)
			}
			if !isRead && name != "delete_kanban_card" && annotations["destructiveHint"] != false {
				t.Fatalf("tool %s must be non-destructive: %#v", name, annotations)
			}
		}
	}
	readDef, _ := server.mcpToolByName("list_kanban_boards")
	writeDef, _ := server.mcpToolByName("create_kanban_card")
	if strings.Join(readDef.Scopes, ",") != domain.NotesAPIScopeRead || strings.Join(writeDef.Scopes, ",") != domain.NotesAPIScopeWrite {
		t.Fatalf("Kanban scopes read=%v write=%v", readDef.Scopes, writeDef.Scopes)
	}
	listCardsDef, _ := server.mcpToolByName("list_kanban_cards")
	worklogDef, _ := server.mcpToolByName("append_kanban_worklog")
	moveDef, _ := server.mcpToolByName("move_kanban_card")
	workflowDescriptions := strings.ToLower(strings.Join([]string{
		listCardsDef.Description,
		worklogDef.Description,
		moveDef.Description,
	}, " "))
	for _, required := range []string{"human", "review", "intake", "continue"} {
		if !strings.Contains(workflowDescriptions, required) {
			t.Fatalf("Kanban workflow descriptions missing %q: %s", required, workflowDescriptions)
		}
	}
}

func TestMCPToolDescriptorSupportsAppFields(t *testing.T) {
	outputSchema := mcpObjectSchema(map[string]any{"value": mcpStr("Value.")}, "value")
	originalMeta := map[string]any{"ui": map[string]any{"resourceUri": "ui://test/v1"}}
	tools := mcpToolListFromDefs([]mcpToolDef{{
		Name: "render_test", Title: "Render test result", Description: "Render a test.", InputSchema: mcpObjectSchema(map[string]any{}),
		OutputSchema: outputSchema,
		Scopes:       []string{domain.NotesAPIScopeRead, domain.NotesAPIScopeWrite},
		Annotations:  mcpReadOnlyAnnotations,
		Meta:         originalMeta,
	}})
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	if !reflect.DeepEqual(tools[0]["outputSchema"], outputSchema) {
		t.Fatalf("outputSchema = %#v", tools[0]["outputSchema"])
	}
	if tools[0]["title"] != "Render test result" || !reflect.DeepEqual(tools[0]["annotations"], mcpReadOnlyAnnotations) {
		t.Fatalf("title/annotations = %#v/%#v", tools[0]["title"], tools[0]["annotations"])
	}
	wantSchemes := []map[string]any{
		{"type": "oauth2", "scopes": []string{domain.NotesAPIScopeRead}},
		{"type": "oauth2", "scopes": []string{domain.NotesAPIScopeWrite}},
	}
	if !reflect.DeepEqual(tools[0]["securitySchemes"], wantSchemes) {
		t.Fatalf("securitySchemes = %#v", tools[0]["securitySchemes"])
	}
	meta := tools[0]["_meta"].(map[string]any)
	if !reflect.DeepEqual(meta["ui"], map[string]any{"resourceUri": "ui://test/v1"}) || !reflect.DeepEqual(meta["securitySchemes"], wantSchemes) {
		t.Fatalf("_meta = %#v", tools[0]["_meta"])
	}
	if _, mutated := originalMeta["securitySchemes"]; mutated {
		t.Fatal("descriptor serialization mutated source metadata")
	}
}

func TestMCPToolSuccessPreservesTextAndAddsStructuredContent(t *testing.T) {
	result := mcpToolSuccess(mcpToolExecution{
		Text:              `{"ok":true}`,
		StructuredContent: map[string]any{"ok": true},
		Meta:              map[string]any{"widget": "private"},
	})
	if result["isError"] != false {
		t.Fatalf("isError = %#v", result["isError"])
	}
	content := result["content"].([]map[string]any)
	if len(content) != 1 || content[0]["text"] != `{"ok":true}` {
		t.Fatalf("content = %#v", content)
	}
	if !reflect.DeepEqual(result["structuredContent"], map[string]any{"ok": true}) {
		t.Fatalf("structuredContent = %#v", result["structuredContent"])
	}
	if !reflect.DeepEqual(result["_meta"], map[string]any{"widget": "private"}) {
		t.Fatalf("_meta = %#v", result["_meta"])
	}
}

func TestMCPMergeResultMetaPreservesWidgetMetadata(t *testing.T) {
	result := map[string]any{"_meta": map[string]any{"widget": "private"}}
	mcpMergeResultMeta(result, map[string]any{"server": "hank"})
	meta := result["_meta"].(map[string]any)
	if !reflect.DeepEqual(meta, map[string]any{"widget": "private", "server": "hank"}) {
		t.Fatalf("merged metadata = %#v", meta)
	}
}

func TestMCPOpenKanbanDescriptorIsAlwaysAdvertised(t *testing.T) {
	def, ok := (&Server{}).mcpToolByName("open_kanban")
	if !ok {
		t.Fatal("open_kanban not advertised")
	}
	if def.Annotations["readOnlyHint"] != true || def.Meta["openai/outputTemplate"] != mcpKanbanResourceURI {
		t.Fatalf("open_kanban descriptor = %#v", def)
	}
	ui := def.Meta["ui"].(map[string]any)
	if ui["resourceUri"] != mcpKanbanResourceURI || len(def.OutputSchema) == 0 {
		t.Fatalf("open_kanban metadata/schema = %#v/%#v", def.Meta, def.OutputSchema)
	}
	if !reflect.DeepEqual(ui["visibility"], []string{"model", "app"}) {
		t.Fatalf("open_kanban visibility = %#v", ui["visibility"])
	}
}

func TestMCPDeleteKanbanDescriptorIsAlwaysAdvertisedAndDestructive(t *testing.T) {
	def, ok := (&Server{}).mcpToolByName("delete_kanban_card")
	if !ok {
		t.Fatal("delete_kanban_card not advertised")
	}
	if def.Annotations["readOnlyHint"] != false || def.Annotations["destructiveHint"] != true || def.Annotations["openWorldHint"] != false {
		t.Fatalf("delete annotations = %#v", def.Annotations)
	}
	if strings.Join(def.Scopes, ",") != domain.NotesAPIScopeDelete || len(def.OutputSchema) == 0 {
		t.Fatalf("delete scope/schema = %#v/%#v", def.Scopes, def.OutputSchema)
	}
}

func TestMCPServerInstructionsCoverHumanHandoff(t *testing.T) {
	instructions := strings.ToLower(mcpServerInstructions())
	for _, required := range []string{"human approval", "needs human", "review", "next ordered intake card", "rather than waiting"} {
		if !strings.Contains(instructions, required) {
			t.Fatalf("server instructions missing %q: %s", required, instructions)
		}
	}
}

type staticMCPKanbanStore struct {
	notes    []domain.UserNote
	settings domain.UserProfileSettings
}

func (s staticMCPKanbanStore) ListProfileNotes(context.Context, string, bool) ([]domain.UserNote, error) {
	return s.notes, nil
}

func (s staticMCPKanbanStore) GetUserProfileSettings(context.Context, string) (domain.UserProfileSettings, error) {
	return s.settings, nil
}

func (staticMCPKanbanStore) ListNoteAttachments(context.Context, string) ([]domain.NoteAttachment, error) {
	return nil, nil
}

type staticMCPKanbanNotes struct{ fetched protocol.NotesFetchResponse }

func (s *staticMCPKanbanNotes) FetchProfile(context.Context, string, string) (protocol.NotesFetchResponse, error) {
	return s.fetched, nil
}

func (s *staticMCPKanbanNotes) SaveProfile(context.Context, string, string, protocol.NotesSaveRequest) (protocol.NotesSaveResponse, error) {
	return protocol.NotesSaveResponse{}, errors.New("unexpected save")
}

func TestMCPDispatchesKanbanReadWithoutDB(t *testing.T) {
	board := testMCPKanbanBoard()
	kanbanStore := staticMCPKanbanStore{
		notes:    []domain.UserNote{{NoteID: "work", OwnerUserID: "u1", Title: "Work", PageType: protocol.NotePageTypeKanban}},
		settings: domain.UserProfileSettings{Settings: json.RawMessage(`{"kanban_default_board_id":"work"}`)},
	}
	kanbanNotes := &staticMCPKanbanNotes{fetched: protocol.NotesFetchResponse{NoteID: "work", Title: "Work", Revision: "1", PageType: protocol.NotePageTypeKanban, Board: board}}
	s := &Server{kanban: newMCPKanbanService(kanbanStore, kanbanNotes, time.Now)}
	auth := mcpAuthContext{User: domain.User{ID: "u1"}, Token: domain.MCPToken{Scopes: []string{domain.NotesAPIScopeRead}}}

	result := s.mcpToolsCall(context.Background(), auth, json.RawMessage(`{"name":"list_kanban_boards","arguments":{}}`))
	if result["isError"] == true || !strings.Contains(mcpFirstText(result), `"board_id": "work"`) {
		t.Fatalf("list_kanban_boards = %#v", result)
	}
}

func TestMCPKanbanAuditMetadataContainsOnlyIdentifiers(t *testing.T) {
	result := mcpKanbanCardResult{
		BoardID: "work", CardID: "card-1", ColumnID: "review",
		Title: "Secret title", DetailsMarkdown: "Secret details", Tags: []string{"Secret"},
	}
	metadata := mcpKanbanAuditMetadata("move_kanban_card", "chatgpt", result, map[string]string{"source_column_id": "active"})
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"Secret title", "Secret details", "Secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("audit metadata leaked card content: %s", text)
		}
	}
	for _, identifier := range []string{"work", "card-1", "review", "active", "chatgpt"} {
		if !strings.Contains(text, identifier) {
			t.Fatalf("audit metadata missing %q: %s", identifier, text)
		}
	}
}

func TestMCPKanbanArgumentsDecodeSnakeCaseFields(t *testing.T) {
	var create mcpKanbanCreateArgs
	if err := decodeMCPToolArgs(json.RawMessage(`{"board_id":"work","column_id":"ideas","title":"Task","details_markdown":"Details","due_date":"2026-07-25","tags":["Hank"]}`), &create); err != nil {
		t.Fatal(err)
	}
	if create.BoardID != "work" || create.ColumnID != "ideas" || create.DetailsMarkdown != "Details" || create.DueDate != "2026-07-25" {
		t.Fatalf("create args = %#v", create)
	}
	var move mcpKanbanMoveArgs
	if err := decodeMCPToolArgs(json.RawMessage(`{"board_id":"work","card_id":"card-1","target_column_id":"review","target_index":2}`), &move); err != nil {
		t.Fatal(err)
	}
	if move.BoardID != "work" || move.CardID != "card-1" || move.TargetColumnID != "review" || move.TargetIndex == nil || *move.TargetIndex != 2 {
		t.Fatalf("move args = %#v", move)
	}
}

func TestMCPExecuteDocsToolsNoDB(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "README.md"), "# Readme\nsearchable docs content\n")
	s := &Server{mcpDocs: newMCPDocsIndex(root)}
	auth := mcpAuthContext{
		User:  domain.User{ID: "u1"},
		Token: domain.MCPToken{Scopes: []string{domain.MCPScopeDocsRead}},
	}

	// scope gating: docs tool allowed, notes tool denied
	res := s.mcpToolsCall(context.Background(), auth, json.RawMessage(`{"name":"list_docs","arguments":{}}`))
	if res["isError"] == true {
		t.Fatalf("list_docs should succeed: %v", res)
	}
	res = s.mcpToolsCall(context.Background(), auth, json.RawMessage(`{"name":"create_note","arguments":{"title":"x","content":"y"}}`))
	if res["isError"] != true {
		t.Fatalf("create_note should be denied without notes:write scope")
	}
	res = s.mcpToolsCall(context.Background(), auth, json.RawMessage(`{"name":"search_docs","arguments":{"query":"searchable"}}`))
	text := mcpFirstText(res)
	if !strings.Contains(text, "README.md") {
		t.Fatalf("search_docs missing hit: %q", text)
	}
}

func TestMCPExcludedVisibilityHelpers(t *testing.T) {
	notes := []domain.UserNote{
		{NoteID: "private-notebook", Title: "Private Notebook", PageType: "notebook", MCPExcluded: true},
		{NoteID: "child-hidden.md", Title: "Hidden Child", PageType: "text", ParentID: "private-notebook"},
		{NoteID: "private-note.md", Title: "Private Note", PageType: "text", MCPExcluded: true},
		{NoteID: "visible-notebook", Title: "Visible Notebook", PageType: "notebook"},
		{NoteID: "child-visible.md", Title: "Visible Child", PageType: "text", ParentID: "visible-notebook"},
		{NoteID: "visible-note.md", Title: "Visible Note", PageType: "text"},
	}

	visible := mcpVisibleProfileNotes(notes)
	gotIDs := make([]string, 0, len(visible))
	for _, note := range visible {
		gotIDs = append(gotIDs, note.NoteID)
	}
	if strings.Join(gotIDs, ",") != "visible-notebook,child-visible.md,visible-note.md" {
		t.Fatalf("mcpVisibleProfileNotes ids = %v", gotIDs)
	}

	for _, tc := range []struct {
		noteID string
		want   bool
	}{
		{noteID: "private-notebook", want: false},
		{noteID: "child-hidden.md", want: false},
		{noteID: "private-note.md", want: false},
		{noteID: "child-visible.md", want: true},
		{noteID: "visible-note.md", want: true},
		{noteID: "missing.md", want: false},
	} {
		if got := mcpNoteVisible(notes, tc.noteID); got != tc.want {
			t.Fatalf("mcpNoteVisible(%q) = %v, want %v", tc.noteID, got, tc.want)
		}
	}
}

func mcpFirstText(res map[string]any) string {
	content, _ := res["content"].([]map[string]any)
	if len(content) == 0 {
		return ""
	}
	s, _ := content[0]["text"].(string)
	return s
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
