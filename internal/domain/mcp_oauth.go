package domain

import "time"

// MCP OAuth scopes. Notes reuse the existing notes:* scope vocabulary
// (NotesAPIScope* in models.go); docs add their own read scope.
const (
	MCPScopeDocsRead = "docs:read"
)

// MCPOAuthResource is the logical resource identifier path the MCP endpoint is
// served at. The full resource URL is built from the configured public base URL.
const MCPOAuthResourcePath = "/v1/mcp"

// MCPOAuthClient is an OAuth 2.1 client registered (typically via Dynamic Client
// Registration) to connect to the MCP endpoint. ChatGPT and Claude register as
// public clients that authenticate with PKCE, so ClientSecretHash is usually empty.
type MCPOAuthClient struct {
	ID                      string    `json:"client_id"`
	ClientSecretHash        string    `json:"-"`
	RedirectURIs            []string  `json:"redirect_uris"`
	ClientName              string    `json:"client_name,omitempty"`
	TokenEndpointAuthMethod string    `json:"token_endpoint_auth_method"`
	GrantTypes              []string  `json:"grant_types"`
	Scope                   string    `json:"scope,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
}

// IsPublic reports whether the client authenticates with PKCE only (no secret).
func (c MCPOAuthClient) IsPublic() bool {
	return c.ClientSecretHash == ""
}

// AllowsRedirectURI reports whether the given redirect URI was registered.
func (c MCPOAuthClient) AllowsRedirectURI(uri string) bool {
	for _, registered := range c.RedirectURIs {
		if registered == uri {
			return true
		}
	}
	return false
}

// MCPAuthCode is a short-lived authorization code issued by /authorize and
// exchanged once at /token. The raw code is never stored; CodeHash is its hash.
type MCPAuthCode struct {
	CodeHash            string
	ClientID            string
	UserID              string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	Scopes              []string
	Resource            string
	ExpiresAt           time.Time
	ConsumedAt          *time.Time
	CreatedAt           time.Time
}

// MCPToken is an issued access/refresh token grant. Raw tokens are never stored;
// only their hashes. Refresh rotation revokes the old row and inserts a new one.
type MCPToken struct {
	ID               string
	ClientID         string
	UserID           string
	AccessTokenHash  string
	RefreshTokenHash string
	Scopes           []string
	Resource         string
	AccessExpiresAt  time.Time
	RefreshExpiresAt *time.Time
	RevokedAt        *time.Time
	LastUsedAt       *time.Time
	LastUsedRoute    string
	LastUsedIPHash   string
	RequestCount     int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// HasScope reports whether the token grant includes the given scope.
func (t MCPToken) HasScope(scope string) bool {
	for _, have := range t.Scopes {
		if have == scope {
			return true
		}
	}
	return false
}
