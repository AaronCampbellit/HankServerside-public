package migrations

import (
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/dropfile/HankServerside/internal/testutil"
)

func TestFleetAccountMigrationPreservesScopeAndGuardsRollback(t *testing.T) {
	db, err := sql.Open("pgx", testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(query string) {
		t.Helper()
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE fleet_grants (id text PRIMARY KEY, home_id text, user_id text, mcp_token_id text, operations jsonb);
 INSERT INTO fleet_grants VALUES ('legacy','home','user','old-token','["job.read"]'::jsonb)`)
	up, err := os.ReadFile("sql/000052_fleet_account_access.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("sql/000052_fleet_account_access.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(string(up))
	var account bool
	var token, ops string
	if err := db.QueryRow(`SELECT mcp_account,mcp_token_id,operations::text FROM fleet_grants WHERE id='legacy'`).Scan(&account, &token, &ops); err != nil {
		t.Fatal(err)
	}
	if account || token != "old-token" || ops != `["job.read"]` {
		t.Fatal("legacy approval expanded")
	}
	if _, err := db.Exec(`UPDATE fleet_grants SET mcp_account=true WHERE id='legacy'`); err == nil {
		t.Fatal("partial bound account approved")
	}
	exec(`INSERT INTO fleet_grants(id,home_id,user_id,operations,mcp_account) VALUES('account','home','user','["workspace.read","workspace.write","job.run","job.read","job.cancel"]'::jsonb,true)`)
	if _, err := db.Exec(string(down)); err == nil || !strings.Contains(err.Error(), "Resolve account-wide fleet grants") {
		t.Fatalf("rollback guard: %v", err)
	}
	exec(`DELETE FROM fleet_grants WHERE id='account'`)
	exec(string(down))
	exec(string(up))
}
