package store

import (
	"context"
	"testing"
	"time"

	"github.com/dropfile/HankServerside/internal/domain"
	"github.com/dropfile/HankServerside/internal/protocol"
)

func TestFleetAccountScopeExpiryAndRevocation(t *testing.T) {
	db := openTestStore(t)
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	user := domain.User{ID: "account-user", Email: "account@example.com", PasswordHash: "hash", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	home := domain.Home{ID: "account-home", UserID: user.ID, Name: "Home", CreatedAt: now, UpdatedAt: now}
	if err := db.CreateHome(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateMCPOAuthClient(ctx, domain.MCPOAuthClient{ID: "account-client", ClientName: "Test", RedirectURIs: []string{"https://example.com/callback"}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	token := domain.MCPToken{ID: "account-token", UserID: user.ID, ClientID: "account-client", AccessTokenHash: "access", Scopes: []string{"docs:read"}, Resource: "https://example.com/v1/mcp", AccessExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	if err := db.CreateMCPToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	grant := FleetGrant{ID: "account-grant", HomeID: home.ID, UserID: user.ID, MCPAccount: true, Operations: protocol.FleetOperations}
	invalid := grant
	invalid.Operations = []string{"job.read"}
	if db.CreateFleetGrant(ctx, invalid, "known-hash") == nil {
		t.Fatal("partial account authority accepted")
	}
	invalid = grant
	invalid.MCPTokenID = token.ID
	if db.CreateFleetGrant(ctx, invalid, "known-hash") == nil {
		t.Fatal("account grant bound to connection")
	}
	invalid = grant
	invalid.Agents = []string{"chosen-device"}
	if db.CreateFleetGrant(ctx, invalid, "known-hash") == nil {
		t.Fatal("account grant selected devices")
	}
	if err := db.CreateFleetGrant(ctx, grant, "known-hash"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFleetGrantState(ctx, home.ID, grant.ID, user.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FleetGrantByHash(ctx, "known-hash"); err == nil {
		t.Fatal("account grant accepted bearer credential")
	}
	if _, err := db.FleetGrantForMCP(ctx, grant.ID, token.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.exec(ctx, `UPDATE fleet_grants SET operations='["job.read"]'::jsonb WHERE id=?`, grant.ID); err == nil {
		t.Fatal("database allowed reduced account scope")
	}
	if _, err := db.exec(ctx, `UPDATE fleet_grants SET mcp_token_id=? WHERE id=?`, token.ID, grant.ID); err == nil {
		t.Fatal("database allowed account binding")
	}
	if _, err := db.exec(ctx, `UPDATE fleet_grants SET expires_at=created_at+interval '1 microsecond' WHERE id=?`, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FleetGrantForMCP(ctx, grant.ID, token.ID, user.ID); err == nil {
		t.Fatal("expired account authorized")
	}
	if _, err := db.exec(ctx, `UPDATE fleet_grants SET expires_at=NULL WHERE id=?`, grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFleetGrantState(ctx, home.ID, grant.ID, user.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.FleetGrantForMCP(ctx, grant.ID, token.ID, user.ID); err == nil {
		t.Fatal("revoked account authorized")
	}
}
