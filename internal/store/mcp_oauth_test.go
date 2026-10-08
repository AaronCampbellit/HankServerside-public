package store

import (
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/testutil"
)

func TestMCPOAuthStoreLifecycle(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	db, err := OpenMigrating(ctx, testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatalf("OpenMigrating: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	user := domain.User{ID: "usr_mcp", Email: "mcp@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// --- client ---
	client := domain.MCPOAuthClient{
		ID:                      "mcpc_1",
		RedirectURIs:            []string{"https://chatgpt.com/connector_platform_oauth_redirect"},
		ClientName:              "ChatGPT",
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		Scope:                   "docs:read notes:read notes:write",
		CreatedAt:               now,
	}
	if err := db.CreateMCPOAuthClient(ctx, client); err != nil {
		t.Fatalf("CreateMCPOAuthClient: %v", err)
	}
	gotClient, err := db.GetMCPOAuthClient(ctx, client.ID)
	if err != nil {
		t.Fatalf("GetMCPOAuthClient: %v", err)
	}
	if !gotClient.IsPublic() {
		t.Fatalf("expected public client")
	}
	if !gotClient.AllowsRedirectURI(client.RedirectURIs[0]) {
		t.Fatalf("redirect uri not round-tripped: %+v", gotClient.RedirectURIs)
	}
	if len(gotClient.GrantTypes) != 2 {
		t.Fatalf("grant types not round-tripped: %+v", gotClient.GrantTypes)
	}

	// --- auth code single-use ---
	code := domain.MCPAuthCode{
		CodeHash:            "codehash_1",
		ClientID:            client.ID,
		UserID:              user.ID,
		RedirectURI:         client.RedirectURIs[0],
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
		Scopes:              []string{"docs:read", "notes:read"},
		Resource:            "https://hank.example/v1/mcp",
		ExpiresAt:           now.Add(time.Minute),
		CreatedAt:           now,
	}
	if err := db.CreateMCPAuthCode(ctx, code); err != nil {
		t.Fatalf("CreateMCPAuthCode: %v", err)
	}
	inspected, err := db.GetMCPAuthCode(ctx, code.CodeHash)
	if err != nil || inspected.ClientID != client.ID {
		t.Fatalf("GetMCPAuthCode: code=%+v err=%v", inspected, err)
	}
	consumed, err := db.ConsumeMCPAuthCode(ctx, code.CodeHash)
	if err != nil {
		t.Fatalf("ConsumeMCPAuthCode: %v", err)
	}
	if consumed.UserID != user.ID || len(consumed.Scopes) != 2 {
		t.Fatalf("consumed code mismatch: %+v", consumed)
	}
	if _, err := db.ConsumeMCPAuthCode(ctx, code.CodeHash); err != ErrNotFound {
		t.Fatalf("second consume should be ErrNotFound, got %v", err)
	}

	// expired code
	expired := code
	expired.CodeHash = "codehash_expired"
	expired.ExpiresAt = now.Add(-time.Minute)
	if err := db.CreateMCPAuthCode(ctx, expired); err != nil {
		t.Fatalf("CreateMCPAuthCode expired: %v", err)
	}
	if _, err := db.ConsumeMCPAuthCode(ctx, expired.CodeHash); err != ErrNotFound {
		t.Fatalf("expired consume should be ErrNotFound, got %v", err)
	}

	// --- tokens ---
	refreshExpiry := now.Add(24 * time.Hour)
	token := domain.MCPToken{
		ID:               "mcpt_1",
		ClientID:         client.ID,
		UserID:           user.ID,
		AccessTokenHash:  "accesshash_1",
		RefreshTokenHash: "refreshhash_1",
		Scopes:           []string{"docs:read", "notes:read", "notes:write"},
		Resource:         "https://hank.example/v1/mcp",
		AccessExpiresAt:  now.Add(time.Hour),
		RefreshExpiresAt: &refreshExpiry,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := db.CreateMCPToken(ctx, token); err != nil {
		t.Fatalf("CreateMCPToken: %v", err)
	}
	gotToken, err := db.GetMCPTokenByAccessHash(ctx, token.AccessTokenHash)
	if err != nil {
		t.Fatalf("GetMCPTokenByAccessHash: %v", err)
	}
	if !gotToken.HasScope("notes:write") || gotToken.RefreshTokenHash != "refreshhash_1" {
		t.Fatalf("token round-trip mismatch: %+v", gotToken)
	}
	if _, err := db.GetMCPTokenByRefreshHash(ctx, "refreshhash_1"); err != nil {
		t.Fatalf("GetMCPTokenByRefreshHash: %v", err)
	}

	rotated := token
	rotated.ID = "mcpt_2"
	rotated.AccessTokenHash = "accesshash_2"
	rotated.RefreshTokenHash = "refreshhash_2"
	if err := db.RotateMCPToken(ctx, token.ID, rotated); err != nil {
		t.Fatalf("RotateMCPToken: %v", err)
	}
	if _, err := db.GetMCPTokenByRefreshHash(ctx, token.RefreshTokenHash); err != ErrNotFound {
		t.Fatalf("rotated old token should be ErrNotFound, got %v", err)
	}
	if _, err := db.GetMCPTokenByRefreshHash(ctx, rotated.RefreshTokenHash); err != nil {
		t.Fatalf("rotated new token lookup: %v", err)
	}
	token = rotated

	if err := db.RecordMCPTokenUse(ctx, token.ID, "POST /v1/mcp", "iphash", now); err != nil {
		t.Fatalf("RecordMCPTokenUse: %v", err)
	}

	// revoke -> access lookup now fails
	if err := db.RevokeMCPToken(ctx, token.ID); err != nil {
		t.Fatalf("RevokeMCPToken: %v", err)
	}
	if _, err := db.GetMCPTokenByAccessHash(ctx, token.AccessTokenHash); err != ErrNotFound {
		t.Fatalf("revoked token access lookup should be ErrNotFound, got %v", err)
	}
	if err := db.RevokeMCPToken(ctx, token.ID); err != ErrNotFound {
		t.Fatalf("double revoke should be ErrNotFound, got %v", err)
	}

	// expired access token
	expiredToken := domain.MCPToken{
		ID:              "mcpt_expired",
		ClientID:        client.ID,
		UserID:          user.ID,
		AccessTokenHash: "accesshash_expired",
		Scopes:          []string{"docs:read"},
		AccessExpiresAt: now.Add(-time.Minute),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.CreateMCPToken(ctx, expiredToken); err != nil {
		t.Fatalf("CreateMCPToken expired: %v", err)
	}
	if _, err := db.GetMCPTokenByAccessHash(ctx, expiredToken.AccessTokenHash); err != ErrNotFound {
		t.Fatalf("expired access lookup should be ErrNotFound, got %v", err)
	}
}
