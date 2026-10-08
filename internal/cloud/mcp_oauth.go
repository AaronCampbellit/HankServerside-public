package cloud

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/store"
)

// MCPConfig configures the optional remote MCP endpoint and its OAuth surface.
type MCPConfig struct {
	Enabled       bool
	PublicBaseURL string
	DocsDir       string
}

// ConfigureMCP wires the remote MCP endpoint. Called once from main.go after
// NewServer; when Enabled is false the routes still exist but return 404.
func (s *Server) ConfigureMCP(cfg MCPConfig) {
	s.mcpEnabled = cfg.Enabled
	s.mcpPublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	s.mcpDocsDir = strings.TrimSpace(cfg.DocsDir)
	s.mcpDocs = newMCPDocsIndex(s.mcpDocsDir)
}

const (
	mcpAuthCodeTTL       = 90 * time.Second
	mcpAccessTokenTTL    = time.Hour
	mcpRefreshTokenTTL   = 30 * 24 * time.Hour
	mcpAuthorizePath     = "/v1/oauth/mcp/authorize"
	mcpTokenPath         = "/v1/oauth/mcp/token"
	mcpRegisterPath      = "/v1/oauth/mcp/register"
	mcpEndpointPath      = domain.MCPOAuthResourcePath // /v1/mcp
	mcpProtectedResWK    = "/.well-known/oauth-protected-resource"
	mcpAuthServerWK      = "/.well-known/oauth-authorization-server"
	mcpClientPrefix      = "mcpc"
	mcpTokenIDPrefix     = "mcpt"
	mcpConsentScriptPath = "/v1/oauth/mcp/consent-v2.js"
)

// mcpSupportedScopes is the full scope vocabulary the MCP endpoint understands.
// Notes reuse the existing notes:* scopes; docs add docs:read.
var mcpSupportedScopes = []string{
	domain.MCPScopeDocsRead,
	domain.NotesAPIScopeRead,
	domain.NotesAPIScopeAppend,
	domain.NotesAPIScopeWrite,
	domain.NotesAPIScopeDelete,
}

// mcpDefaultScopes is granted when a client requests no specific scope. Delete is
// intentionally excluded from the default.
var mcpDefaultScopes = []string{
	domain.MCPScopeDocsRead,
	domain.NotesAPIScopeRead,
	domain.NotesAPIScopeAppend,
	domain.NotesAPIScopeWrite,
}

func mcpScopeSupported(scope string) bool {
	for _, s := range mcpSupportedScopes {
		if s == scope {
			return true
		}
	}
	return false
}

// mcpFilterScopes keeps only supported, de-duplicated scopes, preserving order.
func mcpFilterScopes(requested []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, sc := range requested {
		sc = strings.TrimSpace(sc)
		if sc == "" || !mcpScopeSupported(sc) || seen[sc] {
			continue
		}
		seen[sc] = true
		out = append(out, sc)
	}
	return out
}

func mcpParseScopeParam(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return mcpFilterScopes(strings.Fields(raw))
}

// mcpBaseURL returns the externally reachable origin. Prefers the configured
// public base URL (needed for ChatGPT, whose servers reach us from outside);
// falls back to the request host for local/Claude-over-tunnel use.
func (s *Server) mcpBaseURL(r *http.Request) string {
	if s.mcpPublicBaseURL != "" {
		return s.mcpPublicBaseURL
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) mcpResourceURL(r *http.Request) string {
	return s.mcpBaseURL(r) + mcpEndpointPath
}

func (s *Server) mcpResourceMatches(r *http.Request, resource string) bool {
	return resource != "" && resource == s.mcpResourceURL(r)
}

func writeOAuthError(w http.ResponseWriter, status int, code string, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// --- Discovery metadata ---

func (s *Server) handleMCPProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.mcpIsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.mcpResourceURL(r),
		"authorization_servers":    []string{s.mcpBaseURL(r)},
		"scopes_supported":         mcpSupportedScopes,
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Server) handleMCPAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.mcpIsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	base := s.mcpBaseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         base,
		"authorization_endpoint":                         base + mcpAuthorizePath,
		"token_endpoint":                                 base + mcpTokenPath,
		"registration_endpoint":                          base + mcpRegisterPath,
		"scopes_supported":                               mcpSupportedScopes,
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none"},
		"authorization_response_iss_parameter_supported": true,
		"client_id_metadata_document_supported":          false,
	})
}

// --- Dynamic Client Registration (RFC 7591) ---

func (s *Server) handleMCPRegister(w http.ResponseWriter, r *http.Request) {
	if !s.mcpIsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if allowed, _ := s.store.AllowRateLimit(r.Context(), "mcp_register", clientIP(r), 30, time.Hour); !allowed {
		if !s.limiter.Allow("mcp_register:"+clientIP(r), 30, time.Hour) {
			writeOAuthError(w, http.StatusTooManyRequests, "invalid_request", "too many registrations")
			return
		}
	}
	var body struct {
		RedirectURIs            []string `json:"redirect_uris"`
		ClientName              string   `json:"client_name"`
		GrantTypes              []string `json:"grant_types"`
		TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
		Scope                   string   `json:"scope"`
	}
	if err := parseJSON(w, r, &body); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "invalid registration body")
		return
	}
	redirects := make([]string, 0, len(body.RedirectURIs))
	for _, uri := range body.RedirectURIs {
		uri = strings.TrimSpace(uri)
		if uri == "" {
			continue
		}
		if !mcpValidRedirectURI(uri) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris must be https (or http://localhost)")
			return
		}
		redirects = append(redirects, uri)
	}
	if len(redirects) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "at least one redirect_uri is required")
		return
	}
	grantTypes := body.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = []string{"authorization_code", "refresh_token"}
	}
	now := time.Now().UTC()
	client := domain.MCPOAuthClient{
		ID:                      newID(mcpClientPrefix),
		RedirectURIs:            redirects,
		ClientName:              strings.TrimSpace(body.ClientName),
		TokenEndpointAuthMethod: "none",
		GrantTypes:              grantTypes,
		Scope:                   strings.Join(mcpParseScopeParam(body.Scope), " "),
		CreatedAt:               now,
	}
	if err := s.store.CreateMCPOAuthClient(r.Context(), client); err != nil {
		s.logger.Error("mcp client registration failed", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not register client")
		return
	}
	s.audit(r.Context(), "mcp_oauth.client_registered", auditSeverityInfo, "", "", "", requestIDFromContext(r.Context()), "mcp_oauth_client", client.ID, map[string]any{
		"client_name":   client.ClientName,
		"redirect_uris": client.RedirectURIs,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  client.ID,
		"client_id_issued_at":        now.Unix(),
		"redirect_uris":              client.RedirectURIs,
		"grant_types":                client.GrantTypes,
		"token_endpoint_auth_method": client.TokenEndpointAuthMethod,
		"response_types":             []string{"code"},
		"scope":                      client.Scope,
		"client_name":                client.ClientName,
	})
}

func mcpValidRedirectURI(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if parsed.Fragment != "" || parsed.User != nil {
		return false
	}
	switch parsed.Scheme {
	case "https":
		return parsed.Host != ""
	case "http":
		host := parsed.Hostname()
		return host == "localhost" || host == "127.0.0.1" || host == "::1"
	default:
		return false
	}
}

func mcpConsentContentSecurityPolicy(redirectURI string) string {
	if !mcpValidRedirectURI(redirectURI) {
		return defaultContentSecurityPolicy
	}
	parsed, _ := url.Parse(redirectURI)
	return defaultContentSecurityPolicy + " " + parsed.Scheme + "://" + parsed.Host
}

// --- Authorization endpoint (consent) ---

type mcpAuthorizeParams struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scope               string
	State               string
	Resource            string
}

func mcpAuthorizeParamsFromForm(get func(string) string) mcpAuthorizeParams {
	method := strings.TrimSpace(get("code_challenge_method"))
	if method == "" {
		method = "S256"
	}
	return mcpAuthorizeParams{
		ResponseType:        strings.TrimSpace(get("response_type")),
		ClientID:            strings.TrimSpace(get("client_id")),
		RedirectURI:         strings.TrimSpace(get("redirect_uri")),
		CodeChallenge:       strings.TrimSpace(get("code_challenge")),
		CodeChallengeMethod: method,
		Scope:               strings.TrimSpace(get("scope")),
		State:               get("state"),
		Resource:            strings.TrimSpace(get("resource")),
	}
}

func (s *Server) handleMCPAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.mcpIsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleMCPAuthorizeGet(w, r)
	case http.MethodPost:
		s.handleMCPAuthorizePost(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMCPConsentScript(w http.ResponseWriter, r *http.Request) {
	if !s.mcpIsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	script, err := readEmbeddedUIFile("mcp/consent.mjs")
	if err != nil {
		http.Error(w, "consent script unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Cloudflare-CDN-Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(script)
}

// validateAuthorizeRequest checks the client + redirect first (so we never
// redirect errors to an unverified URI), then the remaining params.
func (s *Server) validateAuthorizeRequest(ctx context.Context, p mcpAuthorizeParams) (domain.MCPOAuthClient, string, bool) {
	client, err := s.store.GetMCPOAuthClient(ctx, p.ClientID)
	if err != nil {
		return domain.MCPOAuthClient{}, "invalid client_id", false
	}
	if !client.AllowsRedirectURI(p.RedirectURI) {
		return domain.MCPOAuthClient{}, "redirect_uri does not match a registered value", false
	}
	return client, "", true
}

// redirectParamError sends an OAuth error back to the (already validated) redirect URI.
func (s *Server) mcpRedirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, desc string) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		http.Error(w, desc, http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("error", code)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if state != "" {
		q.Set("state", state)
	}
	q.Set("iss", s.mcpBaseURL(r))
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) handleMCPAuthorizeGet(w http.ResponseWriter, r *http.Request) {
	p := mcpAuthorizeParamsFromForm(r.URL.Query().Get)
	client, msg, ok := s.validateAuthorizeRequest(r.Context(), p)
	if !ok {
		s.renderMCPError(w, http.StatusBadRequest, msg)
		return
	}
	if p.ResponseType != "code" {
		s.mcpRedirectError(w, r, p.RedirectURI, p.State, "unsupported_response_type", "only response_type=code is supported")
		return
	}
	if p.CodeChallenge == "" || p.CodeChallengeMethod != "S256" {
		s.mcpRedirectError(w, r, p.RedirectURI, p.State, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	if !s.mcpResourceMatches(r, p.Resource) {
		s.mcpRedirectError(w, r, p.RedirectURI, p.State, "invalid_target", "resource must identify this MCP endpoint")
		return
	}

	auth, err := s.appAuthFromRequest(r)
	if err != nil {
		s.renderMCPSignInRequired(w, r)
		return
	}

	scopes := mcpParseScopeParam(p.Scope)
	if len(scopes) == 0 {
		scopes = mcpDefaultScopes
	}
	s.renderMCPConsent(w, r, client, auth.User, p, scopes)
}

func (s *Server) handleMCPAuthorizePost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderMCPError(w, http.StatusBadRequest, "invalid form submission")
		return
	}
	// No Origin/Referer check here: OAuth consent is reached via a cross-site
	// flow (the AI app navigates the browser here), where browsers may send
	// "Origin: null". CSRF is enforced below via the double-submit consent token,
	// and the Lax session cookie is not sent on genuine cross-site POSTs.
	auth, err := s.appAuthFromRequest(r)
	if err != nil {
		s.logger.Warn("mcp consent unauthenticated", "has_session_cookie", sessionTokenFromCookie(r) != "", "origin", r.Header.Get("Origin"))
		s.renderMCPSignInRequired(w, r)
		return
	}
	if !mcpConsentCSRFValid(r) {
		s.logger.Warn("mcp consent csrf mismatch", "has_csrf_cookie", csrfTokenFromCookie(r) != "", "has_form_token", strings.TrimSpace(r.PostForm.Get("csrf_token")) != "")
		s.renderMCPError(w, http.StatusForbidden, "invalid or missing CSRF token; reload and try again")
		return
	}

	p := mcpAuthorizeParamsFromForm(r.PostForm.Get)
	client, msg, ok := s.validateAuthorizeRequest(r.Context(), p)
	if !ok {
		s.renderMCPError(w, http.StatusBadRequest, msg)
		return
	}
	if p.CodeChallenge == "" || p.CodeChallengeMethod != "S256" {
		s.mcpRedirectError(w, r, p.RedirectURI, p.State, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	if !s.mcpResourceMatches(r, p.Resource) {
		s.mcpRedirectError(w, r, p.RedirectURI, p.State, "invalid_target", "resource must identify this MCP endpoint")
		return
	}
	if strings.TrimSpace(r.PostForm.Get("decision")) != "allow" {
		s.mcpRedirectError(w, r, p.RedirectURI, p.State, "access_denied", "the user denied the request")
		return
	}

	// The granted scopes are the approved checkboxes intersected with what the
	// client requested (or the default set), all clamped to supported scopes.
	requested := mcpParseScopeParam(p.Scope)
	if len(requested) == 0 {
		requested = mcpDefaultScopes
	}
	approved := mcpFilterScopes(r.PostForm["scope_grant"])
	granted := mcpIntersectScopes(requested, approved)
	if len(granted) == 0 {
		s.mcpRedirectError(w, r, p.RedirectURI, p.State, "invalid_scope", "no scopes were granted")
		return
	}

	rawCode := newToken()
	resource := s.mcpResourceURL(r)
	now := time.Now().UTC()
	code := domain.MCPAuthCode{
		CodeHash:            hashToken(rawCode),
		ClientID:            client.ID,
		UserID:              auth.User.ID,
		RedirectURI:         p.RedirectURI,
		CodeChallenge:       p.CodeChallenge,
		CodeChallengeMethod: p.CodeChallengeMethod,
		Scopes:              granted,
		Resource:            resource,
		ExpiresAt:           now.Add(mcpAuthCodeTTL),
		CreatedAt:           now,
	}
	if err := s.store.CreateMCPAuthCode(r.Context(), code); err != nil {
		s.logger.Error("mcp auth code create failed", "error", err)
		s.renderMCPError(w, http.StatusInternalServerError, "could not complete authorization")
		return
	}
	s.audit(r.Context(), "mcp_oauth.authorized", auditSeverityInfo, auth.User.ID, "", "", requestIDFromContext(r.Context()), "mcp_oauth_client", client.ID, map[string]any{
		"scopes": granted,
	})

	u, _ := url.Parse(p.RedirectURI)
	q := u.Query()
	q.Set("code", rawCode)
	if p.State != "" {
		q.Set("state", p.State)
	}
	q.Set("iss", s.mcpBaseURL(r))
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func mcpIntersectScopes(a, b []string) []string {
	inB := map[string]bool{}
	for _, s := range b {
		inB[s] = true
	}
	var out []string
	for _, s := range a {
		if inB[s] {
			out = append(out, s)
		}
	}
	return out
}

// mcpConsentCSRFValid checks the double-submit CSRF token on the consent form
// (the form posts a hidden csrf_token matching the non-HttpOnly csrf cookie).
func mcpConsentCSRFValid(r *http.Request) bool {
	cookie := csrfTokenFromCookie(r)
	form := strings.TrimSpace(r.PostForm.Get("csrf_token"))
	if cookie == "" || form == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie), []byte(form)) == 1
}

// --- Token endpoint ---

func (s *Server) handleMCPToken(w http.ResponseWriter, r *http.Request) {
	if !s.mcpIsEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "could not parse form")
		return
	}
	switch strings.TrimSpace(r.PostForm.Get("grant_type")) {
	case "authorization_code":
		s.handleMCPTokenAuthCode(w, r)
	case "refresh_token":
		s.handleMCPTokenRefresh(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

func (s *Server) handleMCPTokenAuthCode(w http.ResponseWriter, r *http.Request) {
	rawCode := strings.TrimSpace(r.PostForm.Get("code"))
	clientID := strings.TrimSpace(r.PostForm.Get("client_id"))
	redirectURI := strings.TrimSpace(r.PostForm.Get("redirect_uri"))
	codeVerifier := strings.TrimSpace(r.PostForm.Get("code_verifier"))
	resource := strings.TrimSpace(r.PostForm.Get("resource"))
	if rawCode == "" || clientID == "" || codeVerifier == "" || resource == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "code, client_id, code_verifier and resource are required")
		return
	}
	if !s.mcpResourceMatches(r, resource) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource must identify this MCP endpoint")
		return
	}
	codeHash := hashToken(rawCode)
	code, err := s.store.GetMCPAuthCode(r.Context(), codeHash)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid, expired, or already used")
		return
	}
	if code.ClientID != clientID || code.RedirectURI != redirectURI || code.Resource != resource {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id, redirect_uri or resource mismatch")
		return
	}
	if !verifyPKCES256(codeVerifier, code.CodeChallenge) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	code, err = s.store.ConsumeMCPAuthCode(r.Context(), codeHash)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid, expired, or already used")
		return
	}
	s.issueAndWriteMCPTokens(w, r, code.ClientID, code.UserID, code.Scopes, code.Resource, "")
}

func (s *Server) handleMCPTokenRefresh(w http.ResponseWriter, r *http.Request) {
	rawRefresh := strings.TrimSpace(r.PostForm.Get("refresh_token"))
	clientID := strings.TrimSpace(r.PostForm.Get("client_id"))
	resource := strings.TrimSpace(r.PostForm.Get("resource"))
	if rawRefresh == "" || clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "refresh_token and client_id are required")
		return
	}
	if resource == "" {
		resource = s.mcpResourceURL(r)
	}
	if !s.mcpResourceMatches(r, resource) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_target", "resource must identify this MCP endpoint")
		return
	}
	existing, err := s.store.GetMCPTokenByRefreshHash(r.Context(), hashToken(rawRefresh))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh_token is invalid or expired")
		return
	}
	if existing.ClientID != clientID || existing.Resource != resource {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id or resource mismatch")
		return
	}
	s.issueAndWriteMCPTokens(w, r, existing.ClientID, existing.UserID, existing.Scopes, existing.Resource, existing.ID)
}

func (s *Server) issueAndWriteMCPTokens(w http.ResponseWriter, r *http.Request, clientID, userID string, scopes []string, resource string, rotatedTokenID string) {
	rawAccess := newToken()
	rawRefresh := newToken()
	now := time.Now().UTC()
	refreshExpiry := now.Add(mcpRefreshTokenTTL)
	token := domain.MCPToken{
		ID:               newID(mcpTokenIDPrefix),
		ClientID:         clientID,
		UserID:           userID,
		AccessTokenHash:  hashToken(rawAccess),
		RefreshTokenHash: hashToken(rawRefresh),
		Scopes:           scopes,
		Resource:         resource,
		AccessExpiresAt:  now.Add(mcpAccessTokenTTL),
		RefreshExpiresAt: &refreshExpiry,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	var err error
	if rotatedTokenID == "" {
		err = s.store.CreateMCPToken(r.Context(), token)
	} else {
		err = s.store.RotateMCPToken(r.Context(), rotatedTokenID, token)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) && rotatedTokenID != "" {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh_token already used")
			return
		}
		s.logger.Error("mcp token issue failed", "error", err)
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not issue token")
		return
	}
	if rotatedTokenID != "" && s.mcpSubscriptions != nil {
		s.mcpSubscriptions.closeToken(rotatedTokenID, "token_rotated")
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  rawAccess,
		"token_type":    "Bearer",
		"expires_in":    int(mcpAccessTokenTTL.Seconds()),
		"refresh_token": rawRefresh,
		"scope":         strings.Join(scopes, " "),
	})
}

func verifyPKCES256(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(challenge)) == 1
}

// --- Resource-server auth for the MCP endpoint ---

type mcpAuthContext struct {
	User  domain.User
	Token domain.MCPToken
}

func (a mcpAuthContext) hasScope(scope string) bool { return a.Token.HasScope(scope) }

func (s *Server) requireMCPAuth(w http.ResponseWriter, r *http.Request) (mcpAuthContext, bool) {
	raw, err := bearerToken(r.Header.Get("Authorization"))
	if err != nil {
		s.logger.Info("mcp auth failed: no/invalid bearer header", "error", err)
		s.writeMCPUnauthorized(w, r)
		return mcpAuthContext{}, false
	}
	token, err := s.store.GetMCPTokenByAccessHash(r.Context(), hashToken(raw))
	if err != nil {
		s.logger.Info("mcp auth failed: access token not found/expired/revoked", "token_len", len(raw))
		s.writeMCPUnauthorized(w, r)
		return mcpAuthContext{}, false
	}
	if !s.mcpResourceMatches(r, token.Resource) {
		s.logger.Info("mcp auth failed: token resource mismatch", "token_id", token.ID)
		s.writeMCPUnauthorized(w, r)
		return mcpAuthContext{}, false
	}
	user, err := s.store.GetUserByID(r.Context(), token.UserID)
	if err != nil {
		s.logger.Info("mcp auth failed: user lookup", "error", err)
		s.writeMCPUnauthorized(w, r)
		return mcpAuthContext{}, false
	}
	if err := s.store.RecordMCPTokenUse(r.Context(), token.ID, r.Method+" "+r.URL.Path, stableAuditTarget(clientIP(r)), time.Now().UTC()); err != nil {
		s.logger.Warn("failed to record mcp token use", "token_id", token.ID, "error", err)
	}
	return mcpAuthContext{User: user, Token: token}, true
}

func (s *Server) writeMCPUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+s.mcpBaseURL(r)+mcpProtectedResWK+`"`)
	s.metrics.IncAuthFailure("mcp_unauthorized")
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
}

// --- HTML pages (no inline JS: CSP forbids it; inline styles are allowed) ---

var mcpConsentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Connect to Hank</title>
<script type="module" src="{{.ConsentScriptPath}}"></script>
<style>
body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}
.card{background:#1e293b;padding:28px 32px;border-radius:14px;max-width:460px;width:90%;box-shadow:0 10px 40px rgba(0,0,0,.4)}
h1{font-size:19px;margin:0 0 6px}
p.sub{color:#94a3b8;font-size:13px;margin:0 0 18px}
.scopes{list-style:none;padding:0;margin:0 0 20px}
.scopes li{display:flex;align-items:center;gap:10px;padding:8px 0;border-top:1px solid #334155}
.scopes label{font-size:14px}
.scopes small{color:#94a3b8;display:block;font-size:12px}
.btns{display:flex;gap:10px;margin-top:8px}
button{flex:1;padding:11px;border:0;border-radius:8px;font-size:14px;font-weight:600;cursor:pointer}
.allow{background:#22c55e;color:#04210f}
.deny{background:#334155;color:#e2e8f0}
.who{font-size:12px;color:#64748b;margin-top:16px}
</style></head><body>
<form class="card" method="post" action="{{.AuthorizePath}}" data-mcp-consent-form>
<h1>Connect {{.ClientName}} to Hank</h1>
<p class="sub">{{.ClientName}} is requesting access to your Hank account. Choose what to allow:</p>
<ul class="scopes">
{{range .Scopes}}<li>
  <input type="checkbox" id="s_{{.Key}}" name="scope_grant" value="{{.Key}}" {{if .Checked}}checked{{end}}>
  <label for="s_{{.Key}}">{{.Label}}<small>{{.Desc}}</small></label>
</li>{{end}}
</ul>
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<input type="hidden" name="response_type" value="code">
<input type="hidden" name="client_id" value="{{.Params.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.Params.RedirectURI}}">
<input type="hidden" name="code_challenge" value="{{.Params.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.Params.CodeChallengeMethod}}">
<input type="hidden" name="scope" value="{{.Params.Scope}}">
<input type="hidden" name="state" value="{{.Params.State}}">
<input type="hidden" name="resource" value="{{.Params.Resource}}">
<div class="btns">
  <button class="deny" type="submit" name="decision" value="deny">Deny</button>
  <button class="allow" type="submit" name="decision" value="allow">Allow</button>
</div>
<div class="who">Signed in as {{.UserEmail}}</div>
</form></body></html>`))

type mcpConsentScope struct {
	Key     string
	Label   string
	Desc    string
	Checked bool
}

func mcpScopeDisplay(scope string, requested []string) mcpConsentScope {
	checked := false
	for _, s := range requested {
		if s == scope {
			checked = true
			break
		}
	}
	d := mcpConsentScope{Key: scope, Checked: checked}
	switch scope {
	case domain.MCPScopeDocsRead:
		d.Label, d.Desc = "Read project documentation", "Search and read HankServerside docs"
	case domain.NotesAPIScopeRead:
		d.Label, d.Desc = "Read your notes", "List, search, and read note contents"
	case domain.NotesAPIScopeAppend:
		d.Label, d.Desc = "Append to your notes", "Add text to existing notes"
	case domain.NotesAPIScopeWrite:
		d.Label, d.Desc = "Create and edit your notes", "Create new notes and update existing ones"
	case domain.NotesAPIScopeDelete:
		d.Label, d.Desc = "Delete your notes", "Permanently remove notes"
	default:
		d.Label, d.Desc = scope, ""
	}
	return d
}

func (s *Server) renderMCPConsent(w http.ResponseWriter, r *http.Request, client domain.MCPOAuthClient, user domain.User, p mcpAuthorizeParams, requestedScopes []string) {
	// Offer every supported scope; pre-check the ones the client asked for.
	var scopes []mcpConsentScope
	for _, sc := range mcpSupportedScopes {
		scopes = append(scopes, mcpScopeDisplay(sc, requestedScopes))
	}
	clientName := client.ClientName
	if strings.TrimSpace(clientName) == "" {
		clientName = "An application"
	}
	csrf := csrfTokenFromCookie(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", mcpConsentContentSecurityPolicy(p.RedirectURI))
	_ = mcpConsentTemplate.Execute(w, map[string]any{
		"ClientName":        clientName,
		"UserEmail":         user.Email,
		"Scopes":            scopes,
		"CSRF":              csrf,
		"AuthorizePath":     mcpAuthorizePath,
		"ConsentScriptPath": mcpConsentScriptPath,
		"Params":            p,
	})
}

func (s *Server) renderMCPSignInRequired(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Sign in to Hank</title>
<style>body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}.card{background:#1e293b;padding:28px 32px;border-radius:14px;max-width:420px;width:90%;text-align:center}a{color:#38bdf8}</style>
</head><body><div class="card"><h1>Sign in required</h1>
<p>Open the <a href="/">Hank dashboard</a> in this browser and sign in, then return to your app and add the connector again.</p>
</div></body></html>`))
}

func (s *Server) renderMCPError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	page := `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Hank</title>
<style>body{font-family:-apple-system,Segoe UI,Roboto,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;min-height:100vh;align-items:center;justify-content:center;margin:0}.card{background:#1e293b;padding:28px 32px;border-radius:14px;max-width:420px;width:90%;text-align:center}</style>
</head><body><div class="card"><h1>Could not continue</h1><p>` + template.HTMLEscapeString(message) + `</p></div></body></html>`
	_, _ = w.Write([]byte(page))
}

// The environment supplies the initial default; GUI changes persist in Postgres.
func (s *Server) mcpIsEnabled(ctx context.Context) bool {
	if s.store == nil {
		return s.mcpEnabled
	}
	enabled, err := s.store.MCPEnabled(ctx, s.mcpEnabled)
	return err == nil && enabled
}
