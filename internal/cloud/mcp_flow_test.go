package cloud

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
)

func TestMCPKanbanToolsEndToEnd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := storeForTest(t)
	defer db.Close()
	now := time.Now().UTC()
	user := domain.User{ID: "usr_mcp_kanban_flow", Email: "mcp-kanban-flow@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	saveMCPKanbanBoard(t, ctx, server.notes, user.ID, "work", "Work", false, testMCPKanbanBoard())
	if _, err := db.SaveUserProfileSettings(ctx, user.ID, nil, json.RawMessage(`{"kanban_default_board_id":"work"}`)); err != nil {
		t.Fatal(err)
	}
	auth := mcpAuthContext{User: user, Token: domain.MCPToken{ClientID: "chatgpt", Scopes: []string{domain.NotesAPIScopeRead, domain.NotesAPIScopeWrite}}}

	execute := func(name, arguments string) mcpToolExecution {
		t.Helper()
		result, err := server.executeMCPTool(ctx, auth, name, json.RawMessage(arguments))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := result.Text
		if !json.Valid([]byte(text)) || strings.Contains(text, "board_json") {
			t.Fatalf("%s returned invalid or raw JSON: %s", name, text)
		}
		return result
	}
	call := func(name, arguments string) string {
		t.Helper()
		return execute(name, arguments).Text
	}

	listedBoards := execute("list_kanban_boards", `{}`)
	if !strings.Contains(listedBoards.Text, `"board_id": "work"`) || listedBoards.StructuredContent["state_version"] == "" {
		t.Fatalf("list boards = %#v", listedBoards)
	}
	listedCards := execute("list_kanban_cards", `{"query":"offline"}`)
	if !strings.Contains(listedCards.Text, `"card_id": "research"`) || listedCards.StructuredContent["state_version"] == "" {
		t.Fatalf("list cards = %#v", listedCards)
	}
	fetchedCard := execute("get_kanban_card", `{"card_id":"research"}`)
	cardObject, _ := fetchedCard.StructuredContent["card"].(map[string]any)
	if cardObject["card_id"] != "research" || fetchedCard.StructuredContent["state_version"] == "" {
		t.Fatalf("get card = %#v", fetchedCard)
	}
	var created mcpKanbanCardResult
	createdExecution := execute("create_kanban_card", `{"title":"MCP task","tags":["Hank"]}`)
	if err := json.Unmarshal([]byte(createdExecution.Text), &created); err != nil || created.CardID == "" || createdExecution.StructuredContent["state_version"] != created.BoardRevision {
		t.Fatalf("created = %#v err=%v", created, err)
	}
	call("update_kanban_card", fmt.Sprintf(`{"card_id":%q,"title":"Updated MCP task"}`, created.CardID))
	call("append_kanban_worklog", fmt.Sprintf(`{"card_id":%q,"kind":"verification","entry_markdown":"tests passed"}`, created.CardID))
	call("move_kanban_card", fmt.Sprintf(`{"card_id":%q,"target_column_id":"active"}`, created.CardID))

	readOnly := mcpAuthContext{User: user, Token: domain.MCPToken{Scopes: []string{domain.NotesAPIScopeRead}}}
	denied := server.mcpToolsCall(ctx, readOnly, json.RawMessage(`{"name":"create_kanban_card","arguments":{"title":"Denied"}}`))
	if denied["isError"] != true {
		t.Fatalf("write without notes:write = %#v", denied)
	}
}

func TestMCPKanbanToolsAdvertiseOutputSchemas(t *testing.T) {
	server := &Server{}
	server.ConfigureMCP(MCPConfig{Enabled: true})
	for _, name := range []string{
		"list_kanban_boards", "list_kanban_cards", "get_kanban_card", "create_kanban_card",
		"update_kanban_card", "append_kanban_worklog", "move_kanban_card", "open_kanban", "delete_kanban_card",
	} {
		def, ok := server.mcpToolByName(name)
		if !ok || len(def.OutputSchema) == 0 {
			t.Fatalf("%s output schema = %#v, found=%v", name, def.OutputSchema, ok)
		}
	}
}

// TestMCPOAuthEndToEndFlow exercises the full remote MCP path against a real
// store: DCR -> authorize/consent -> token (PKCE) -> POST /v1/mcp tools, plus
// scope enforcement, code single-use, unauthorized discovery, and refresh.
func TestMCPOAuthEndToEndFlow(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db := storeForTest(t)
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_mcp_flow", Email: "flow@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	must(t, db.CreateUser(ctx, user))
	sessionRaw := "mcp-flow-session"
	must(t, db.CreateSession(ctx, domain.AppSession{ID: "sess_mcp_flow", UserID: user.ID, TokenHash: hashToken(sessionRaw), ExpiresAt: now.Add(time.Hour), CreatedAt: now}))

	docsRoot := t.TempDir()
	mustWrite(t, filepath.Join(docsRoot, "README.md"), "# Hank\nproject knowledge for search\n")

	server := NewServer("127.0.0.1:0", db, time.Hour, 5*time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server.ConfigureMCP(MCPConfig{Enabled: true, PublicBaseURL: "https://hank.test", DocsDir: docsRoot})
	ts := httptest.NewServer(server.http.Handler)
	defer ts.Close()

	redirectURI := "https://chatgpt.com/connector_platform_oauth_redirect"

	// --- discovery metadata ---
	var prm map[string]any
	mcpGetJSON(t, ts, "/.well-known/oauth-protected-resource", &prm)
	if prm["resource"] != "https://hank.test/v1/mcp" {
		t.Fatalf("protected-resource metadata resource = %v", prm["resource"])
	}
	var asm map[string]any
	mcpGetJSON(t, ts, "/.well-known/oauth-authorization-server", &asm)
	if asm["token_endpoint"] != "https://hank.test/v1/oauth/mcp/token" {
		t.Fatalf("authorization-server metadata token_endpoint = %v", asm["token_endpoint"])
	}
	if asm["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("authorization-server metadata issuer support = %v", asm["authorization_response_iss_parameter_supported"])
	}

	// --- dynamic client registration ---
	var reg struct {
		ClientID string `json:"client_id"`
	}
	mcpPostJSON(t, ts, "/v1/oauth/mcp/register", map[string]any{
		"redirect_uris": []string{redirectURI},
		"client_name":   "ChatGPT",
	}, http.StatusCreated, &reg)
	if reg.ClientID == "" {
		t.Fatalf("registration returned no client_id")
	}

	// --- authorize + consent ---
	verifier := "test-verifier-abcdefghijklmnopqrstuvwxyz-0123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	form := url.Values{}
	form.Set("response_type", "code")
	form.Set("client_id", reg.ClientID)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")
	form.Set("state", "state-xyz")
	form.Set("resource", "https://hank.test/v1/mcp")
	form.Set("decision", "allow")
	form.Set("csrf_token", "csrftok")
	for _, sc := range []string{"docs:read", "notes:read", "notes:append", "notes:write"} {
		form.Add("scope_grant", sc) // deliberately NOT granting notes:delete
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	missingResourceForm := url.Values{}
	for key, values := range form {
		missingResourceForm[key] = append([]string(nil), values...)
	}
	missingResourceForm.Del("resource")
	mrreq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/oauth/mcp/authorize", strings.NewReader(missingResourceForm.Encode()))
	mrreq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mrreq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionRaw})
	mrreq.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrftok"})
	mrresp, err := noRedirect.Do(mrreq)
	if err != nil {
		t.Fatalf("authorize missing resource: %v", err)
	}
	mrresp.Body.Close()
	mrloc, _ := url.Parse(mrresp.Header.Get("Location"))
	if mrresp.StatusCode != http.StatusFound || mrloc.Query().Get("error") != "invalid_target" || mrloc.Query().Get("iss") != "https://hank.test" {
		t.Fatalf("authorize missing resource status/location = %d/%s", mrresp.StatusCode, mrresp.Header.Get("Location"))
	}

	areq, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/oauth/mcp/authorize", strings.NewReader(form.Encode()))
	areq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	areq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionRaw})
	areq.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrftok"})
	aresp, err := noRedirect.Do(areq)
	if err != nil {
		t.Fatalf("authorize POST: %v", err)
	}
	defer aresp.Body.Close()
	if aresp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(aresp.Body)
		t.Fatalf("authorize status = %d body = %s", aresp.StatusCode, body)
	}
	loc, _ := url.Parse(aresp.Header.Get("Location"))
	code := loc.Query().Get("code")
	if code == "" || loc.Query().Get("state") != "state-xyz" || loc.Query().Get("iss") != "https://hank.test" {
		t.Fatalf("authorize redirect missing code/state: %s", aresp.Header.Get("Location"))
	}

	// --- token exchange (PKCE) ---
	tokenForm := url.Values{}
	tokenForm.Set("grant_type", "authorization_code")
	tokenForm.Set("code", code)
	tokenForm.Set("client_id", reg.ClientID)
	tokenForm.Set("redirect_uri", redirectURI)
	tokenForm.Set("code_verifier", verifier)
	tokenForm.Set("resource", "https://hank.test/v1/mcp")
	wrongResourceTokenForm := url.Values{}
	for key, values := range tokenForm {
		wrongResourceTokenForm[key] = append([]string(nil), values...)
	}
	wrongResourceTokenForm.Set("resource", "https://evil.test/v1/mcp")
	var wrongTarget map[string]any
	if status := mcpPostFormStatus(t, ts, "/v1/oauth/mcp/token", wrongResourceTokenForm, &wrongTarget); status != http.StatusBadRequest || wrongTarget["error"] != "invalid_target" {
		t.Fatalf("wrong token resource status/body = %d/%v", status, wrongTarget)
	}
	wrongVerifierTokenForm := url.Values{}
	for key, values := range tokenForm {
		wrongVerifierTokenForm[key] = append([]string(nil), values...)
	}
	wrongVerifierTokenForm.Set("code_verifier", "wrong-verifier-that-must-not-consume-the-code")
	var wrongVerifier map[string]any
	if status := mcpPostFormStatus(t, ts, "/v1/oauth/mcp/token", wrongVerifierTokenForm, &wrongVerifier); status != http.StatusBadRequest || wrongVerifier["error"] != "invalid_grant" {
		t.Fatalf("wrong PKCE verifier status/body = %d/%v", status, wrongVerifier)
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		Scope        string `json:"scope"`
	}
	mcpPostForm(t, ts, "/v1/oauth/mcp/token", tokenForm, http.StatusOK, &tok)
	if tok.AccessToken == "" || tok.TokenType != "Bearer" {
		t.Fatalf("token response = %+v", tok)
	}
	if strings.Contains(tok.Scope, "notes:delete") {
		t.Fatalf("granted scope unexpectedly includes delete: %q", tok.Scope)
	}

	// code is single-use
	var reuse map[string]any
	if status := mcpPostFormStatus(t, ts, "/v1/oauth/mcp/token", tokenForm, &reuse); status != http.StatusBadRequest {
		t.Fatalf("authorization code reuse should fail, got %d", status)
	}

	// --- unauthorized MCP endpoint advertises discovery ---
	unauth, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	uresp, err := http.DefaultClient.Do(unauth)
	if err != nil {
		t.Fatalf("unauth mcp: %v", err)
	}
	uresp.Body.Close()
	if uresp.StatusCode != http.StatusUnauthorized || !strings.Contains(uresp.Header.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("expected 401 + WWW-Authenticate, got %d / %q", uresp.StatusCode, uresp.Header.Get("WWW-Authenticate"))
	}

	wrongAudienceRaw := "wrong-audience-access-token"
	must(t, db.CreateMCPToken(ctx, domain.MCPToken{
		ID:              "mcpt_wrong_audience",
		ClientID:        reg.ClientID,
		UserID:          user.ID,
		AccessTokenHash: hashToken(wrongAudienceRaw),
		Scopes:          []string{domain.MCPScopeDocsRead},
		Resource:        "https://evil.test/v1/mcp",
		AccessExpiresAt: now.Add(time.Hour),
		CreatedAt:       now,
		UpdatedAt:       now,
	}))
	wrongAudienceRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	wrongAudienceRequest.Header.Set("Authorization", "Bearer "+wrongAudienceRaw)
	wrongAudienceResponse, err := http.DefaultClient.Do(wrongAudienceRequest)
	if err != nil {
		t.Fatalf("wrong audience mcp: %v", err)
	}
	wrongAudienceResponse.Body.Close()
	if wrongAudienceResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong audience status = %d", wrongAudienceResponse.StatusCode)
	}

	crossOriginRequest, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	crossOriginRequest.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	crossOriginRequest.Header.Set("Origin", "https://evil.test")
	crossOriginResponse, err := http.DefaultClient.Do(crossOriginRequest)
	if err != nil {
		t.Fatalf("cross-origin mcp: %v", err)
	}
	crossOriginResponse.Body.Close()
	if crossOriginResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", crossOriginResponse.StatusCode)
	}

	var modernCall func(int, string, string, map[string]any) map[string]any
	call := func(body string) map[string]any {
		t.Helper()
		var envelope struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal([]byte(body), &envelope); err != nil {
			t.Fatalf("decode MCP test request: %v", err)
		}
		name, _ := envelope.Params["name"].(string)
		return modernCall(envelope.ID, envelope.Method, name, envelope.Params)
	}
	modernCall = func(id int, method string, name string, params map[string]any) map[string]any {
		t.Helper()
		if params == nil {
			params = map[string]any{}
		}
		params["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    mcpModernProtocolVersion,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "hank-test", "version": "1.0"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/mcp", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", mcpModernProtocolVersion)
		req.Header.Set("Mcp-Method", method)
		if name != "" {
			req.Header.Set("Mcp-Name", name)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("modern mcp call: %v", err)
		}
		defer resp.Body.Close()
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode modern mcp response: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("modern %s status/body = %d/%v", method, resp.StatusCode, out)
		}
		return out
	}

	if discovered := modernCall(100, "server/discover", "", nil); mcpResultType(discovered) != "complete" {
		t.Fatalf("modern discovery failed: %v", discovered)
	}
	if listed := modernCall(101, "tools/list", "", nil); mcpResultType(listed) != "complete" {
		t.Fatalf("modern tools/list failed: %v", listed)
	}

	saveMCPKanbanBoard(t, ctx, server.notes, user.ID, "work", "Work", false, testMCPKanbanBoard())
	if _, err := db.SaveUserProfileSettings(ctx, user.ID, nil, json.RawMessage(`{"kanban_default_board_id":"work"}`)); err != nil {
		t.Fatal(err)
	}
	if listed := call(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"list_kanban_boards","arguments":{}}}`); !strings.Contains(mcpResultText(listed), `"board_id": "work"`) {
		t.Fatalf("list_kanban_boards result = %v", listed)
	}
	if listed := modernCall(102, "tools/call", "list_kanban_boards", map[string]any{"name": "list_kanban_boards", "arguments": map[string]any{}}); mcpResultType(listed) != "complete" || !strings.Contains(mcpResultText(listed), `"board_id": "work"`) {
		t.Fatalf("modern list_kanban_boards result = %v", listed)
	}
	if listed := call(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"list_kanban_cards","arguments":{"query":"offline"}}}`); !strings.Contains(mcpResultText(listed), `"card_id": "research"`) {
		t.Fatalf("list_kanban_cards result = %v", listed)
	}
	if fetched := call(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"get_kanban_card","arguments":{"card_id":"research"}}}`); !strings.Contains(mcpResultText(fetched), "Capture requirements") {
		t.Fatalf("get_kanban_card result = %v", fetched)
	}
	createdCard := call(`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"create_kanban_card","arguments":{"title":"Voice capture","tags":["Hank"]}}}`)
	var cardResult mcpKanbanCardResult
	if err := json.Unmarshal([]byte(mcpResultText(createdCard)), &cardResult); err != nil || cardResult.CardID == "" {
		t.Fatalf("create_kanban_card result = %v err=%v", createdCard, err)
	}
	updatedCard := call(fmt.Sprintf(`{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"update_kanban_card","arguments":{"card_id":%q,"title":"Typed capture"}}}`, cardResult.CardID))
	if !strings.Contains(mcpResultText(updatedCard), "Typed capture") {
		t.Fatalf("update_kanban_card result = %v", updatedCard)
	}
	loggedCard := call(fmt.Sprintf(`{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"append_kanban_worklog","arguments":{"card_id":%q,"kind":"verification","entry_markdown":"tests passed"}}}`, cardResult.CardID))
	if !strings.Contains(mcpResultText(loggedCard), "Work log") {
		t.Fatalf("append_kanban_worklog result = %v", loggedCard)
	}
	movedCard := call(fmt.Sprintf(`{"jsonrpc":"2.0","id":16,"method":"tools/call","params":{"name":"move_kanban_card","arguments":{"card_id":%q,"target_column_id":"active"}}}`, cardResult.CardID))
	if !strings.Contains(mcpResultText(movedCard), `"column_id": "active"`) {
		t.Fatalf("move_kanban_card result = %v", movedCard)
	}

	created := call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_note","arguments":{"title":"From MCP","content":"hello from mcp"}}}`)
	if mcpToolIsError(created) {
		t.Fatalf("create_note failed: %v", created)
	}
	var noteResp struct {
		NoteID string `json:"note_id"`
	}
	if err := json.Unmarshal([]byte(mcpResultText(created)), &noteResp); err != nil || noteResp.NoteID == "" {
		t.Fatalf("create_note result = %q", mcpResultText(created))
	}

	got := call(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_note","arguments":{"note_id":%q}}}`, noteResp.NoteID))
	if !strings.Contains(mcpResultText(got), "hello from mcp") {
		t.Fatalf("get_note result = %q", mcpResultText(got))
	}
	if got := modernCall(103, "tools/call", "get_note", map[string]any{"name": "get_note", "arguments": map[string]any{"note_id": noteResp.NoteID}}); mcpResultType(got) != "complete" || !strings.Contains(mcpResultText(got), "hello from mcp") {
		t.Fatalf("modern get_note result = %v", got)
	}

	// delete_note must be denied: notes:delete was not granted.
	del := call(fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"delete_note","arguments":{"note_id":%q}}}`, noteResp.NoteID))
	if !mcpToolIsError(del) || !strings.Contains(mcpResultText(del), "not authorized") {
		t.Fatalf("delete_note should be denied for missing scope: %v", del)
	}

	sd := call(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"search_docs","arguments":{"query":"knowledge"}}}`)
	if !strings.Contains(mcpResultText(sd), "README.md") {
		t.Fatalf("search_docs result = %q", mcpResultText(sd))
	}
	if sd := modernCall(104, "tools/call", "search_docs", map[string]any{"name": "search_docs", "arguments": map[string]any{"query": "knowledge"}}); mcpResultType(sd) != "complete" || !strings.Contains(mcpResultText(sd), "README.md") {
		t.Fatalf("modern search_docs result = %v", sd)
	}
	if sources := modernCall(105, "tools/call", "list_context_sources", map[string]any{"name": "list_context_sources", "arguments": map[string]any{}}); mcpResultType(sources) != "complete" || mcpToolIsError(sources) {
		t.Fatalf("modern list_context_sources result = %v", sources)
	}

	// --- refresh rotation ---
	subscriptionContext, cancelSubscription := context.WithCancel(context.Background())
	defer cancelSubscription()
	subscriptionBody := `{"jsonrpc":"2.0","id":"listen-rotation","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	subscriptionRequest, _ := http.NewRequestWithContext(subscriptionContext, http.MethodPost, ts.URL+"/v1/mcp", strings.NewReader(subscriptionBody))
	subscriptionRequest.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	subscriptionRequest.Header.Set("Content-Type", "application/json")
	subscriptionRequest.Header.Set("Accept", "application/json, text/event-stream")
	subscriptionRequest.Header.Set("MCP-Protocol-Version", mcpModernProtocolVersion)
	subscriptionRequest.Header.Set("Mcp-Method", "subscriptions/listen")
	subscriptionResponse, err := http.DefaultClient.Do(subscriptionRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer subscriptionResponse.Body.Close()
	subscriptionReader := bufio.NewReader(subscriptionResponse.Body)
	readMCPSSETestFrame(t, subscriptionReader)

	refreshForm := url.Values{}
	refreshForm.Set("grant_type", "refresh_token")
	refreshForm.Set("refresh_token", tok.RefreshToken)
	refreshForm.Set("client_id", reg.ClientID)
	refreshWithoutResource := url.Values{}
	for key, values := range refreshForm {
		refreshWithoutResource[key] = append([]string(nil), values...)
	}
	var tok2 struct {
		AccessToken string `json:"access_token"`
	}
	mcpPostForm(t, ts, "/v1/oauth/mcp/token", refreshWithoutResource, http.StatusOK, &tok2)
	if tok2.AccessToken == "" || tok2.AccessToken == tok.AccessToken {
		t.Fatalf("refresh should issue a new access token")
	}
	if completed := readMCPSSETestFrame(t, subscriptionReader); completed["id"] != "listen-rotation" || completed["result"] == nil {
		t.Fatalf("rotated token subscription completion = %#v", completed)
	}
	// old refresh token cannot be reused after rotation
	var reused map[string]any
	if status := mcpPostFormStatus(t, ts, "/v1/oauth/mcp/token", refreshForm, &reused); status != http.StatusBadRequest {
		t.Fatalf("rotated refresh token reuse should fail, got %d", status)
	}

	rotated, err := db.GetMCPTokenByAccessHash(ctx, hashToken(tok2.AccessToken))
	if err != nil {
		t.Fatal(err)
	}
	revocationContext, cancelRevocationSubscription := context.WithCancel(context.Background())
	defer cancelRevocationSubscription()
	revocationBody := `{"jsonrpc":"2.0","id":"listen-revocation","method":"subscriptions/listen","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}},"notifications":{"toolsListChanged":true}}}`
	revocationSubscription, _ := http.NewRequestWithContext(revocationContext, http.MethodPost, ts.URL+"/v1/mcp", strings.NewReader(revocationBody))
	revocationSubscription.Header.Set("Authorization", "Bearer "+tok2.AccessToken)
	revocationSubscription.Header.Set("Content-Type", "application/json")
	revocationSubscription.Header.Set("Accept", "application/json, text/event-stream")
	revocationSubscription.Header.Set("MCP-Protocol-Version", mcpModernProtocolVersion)
	revocationSubscription.Header.Set("Mcp-Method", "subscriptions/listen")
	revocationSubscriptionResponse, err := http.DefaultClient.Do(revocationSubscription)
	if err != nil {
		t.Fatal(err)
	}
	defer revocationSubscriptionResponse.Body.Close()
	revocationReader := bufio.NewReader(revocationSubscriptionResponse.Body)
	readMCPSSETestFrame(t, revocationReader)

	revokeRequest, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/me/mcp/connections/"+rotated.ID, nil)
	revokeRequest.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionRaw})
	revokeRequest.AddCookie(&http.Cookie{Name: csrfCookieName, Value: "csrftok"})
	revokeRequest.Header.Set(csrfHeaderName, "csrftok")
	revokeResponse, err := http.DefaultClient.Do(revokeRequest)
	if err != nil {
		t.Fatal(err)
	}
	revokeResponse.Body.Close()
	if revokeResponse.StatusCode != http.StatusOK {
		t.Fatalf("dashboard MCP revoke status = %d", revokeResponse.StatusCode)
	}
	if completed := readMCPSSETestFrame(t, revocationReader); completed["id"] != "listen-revocation" || completed["result"] == nil {
		t.Fatalf("revoked token subscription completion = %#v", completed)
	}
}

// --- test helpers ---

func mcpGetJSON(t *testing.T, ts *httptest.Server, path string, out any) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s status = %d body = %s", path, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func mcpPostJSON(t *testing.T, ts *httptest.Server, path string, body any, wantStatus int, out any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s status = %d (want %d) body = %s", path, resp.StatusCode, wantStatus, b)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
}

func mcpPostForm(t *testing.T, ts *httptest.Server, path string, form url.Values, wantStatus int, out any) {
	t.Helper()
	if status := mcpPostFormStatus(t, ts, path, form, out); status != wantStatus {
		t.Fatalf("POST %s status = %d, want %d", path, status, wantStatus)
	}
}

func mcpPostFormStatus(t *testing.T, ts *httptest.Server, path string, form url.Values, out any) int {
	t.Helper()
	resp, err := http.PostForm(ts.URL+path, form)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func mcpResultText(resp map[string]any) string {
	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return ""
	}
	first, _ := content[0].(map[string]any)
	s, _ := first["text"].(string)
	return s
}

func mcpToolIsError(resp map[string]any) bool {
	result, _ := resp["result"].(map[string]any)
	b, _ := result["isError"].(bool)
	return b
}

func mcpResultType(resp map[string]any) string {
	result, _ := resp["result"].(map[string]any)
	resultType, _ := result["resultType"].(string)
	return resultType
}
