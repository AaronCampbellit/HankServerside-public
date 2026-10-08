package migrations

import (
	"context"
	"database/sql"
	"github.com/dropfile/HankServerside/internal/testutil"
	"os"
	"strings"
	"testing"
)

func TestFleetExpiryMigrationPreservesRowsAndGuardsRollback(t *testing.T) {
	db, err := sql.Open("pgx", testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	exec := func(s string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE fleet_grants (id text PRIMARY KEY, created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL CHECK(expires_at>created_at)); INSERT INTO fleet_grants(id,expires_at) VALUES('finite',now()+interval '1 month')`)
	up, err := os.ReadFile("sql/000048_fleet_grant_expiry.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("sql/000048_fleet_grant_expiry.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := db.QueryRow(`SELECT expires_at::text FROM fleet_grants WHERE id='finite'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	exec(string(up))
	if err := db.QueryRow(`SELECT expires_at::text FROM fleet_grants WHERE id='finite'`).Scan(&after); err != nil || before != after {
		t.Fatal("existing expiry changed")
	}
	exec(`INSERT INTO fleet_grants(id,expires_at) VALUES('infinite',NULL)`)
	if _, err := db.ExecContext(ctx, string(down)); err == nil || !strings.Contains(err.Error(), "Resolve non-expiring fleet grants") {
		t.Fatalf("rollback guard: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO fleet_grants(id,expires_at) VALUES('invalid',now()-interval '1 day')`); err == nil {
		t.Fatal("finite expiry constraint lost")
	}
	exec(`DELETE FROM fleet_grants WHERE id='infinite'`)
	exec(string(down))
	if _, err := db.ExecContext(ctx, `INSERT INTO fleet_grants(id,expires_at) VALUES('null-denied',NULL)`); err == nil {
		t.Fatal("rollback failed to restore not-null")
	}
	exec(string(up))
	exec(`INSERT INTO fleet_grants(id,expires_at) VALUES('infinite',NULL)`)
}
