package migrations

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/dropfile/HankServerside/internal/testutil"
)

func conflictingAssistantIndexDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", testutil.PostgreSQLTestURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	all, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureTable(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.Version <= 34 {
			if err := applyOne(context.Background(), db, m); err != nil {
				t.Fatal(err)
			}
		}
		if m.Version == 39 {
			m.Version = 35
			m.Checksum = ""
			if err := applyOne(context.Background(), db, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	return db
}

func TestReconcileAssistantIndexPreservesIndexAndCompletesUpgrade(t *testing.T) {
	ctx := context.Background()
	db := conflictingAssistantIndexDatabase(t)
	if err := CheckReadOnly(ctx, db); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("original conflict not reproduced: %v", err)
	}
	before, err := AppliedReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var oid uint32
	if err := db.QueryRowContext(ctx, "SELECT 'public.idx_assistant_runs_session_pending'::regclass::oid").Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO users(id,email,password_hash,created_at,updated_at) VALUES ('retained-user','retained@example.test','test-only',now(),now())`); err != nil {
		t.Fatal(err)
	}
	report, err := ReconcileAssistantIndex(ctx, db, false)
	if err != nil || report.Status != "eligible" {
		t.Fatalf("check: %+v %v", report, err)
	}
	after, err := AppliedReadOnly(ctx, db)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("check changed ledger", err)
	}
	report, err = ReconcileAssistantIndex(ctx, db, true)
	if err != nil || report.Status != "reconciled" {
		t.Fatalf("apply: %+v %v", report, err)
	}
	after, err = AppliedReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	old, new := before[len(before)-1], after[len(after)-1]
	if len(after) != len(before) || new.Version != 39 || new.Checksum != report.ToChecksum || new.Name != old.Name || !new.AppliedAt.Equal(old.AppliedAt) || new.DurationMS != old.DurationMS {
		t.Fatalf("ledger provenance lost: old=%+v new=%+v", old, new)
	}
	for i := 0; i < 34; i++ {
		if !reflect.DeepEqual(before[i], after[i]) {
			t.Fatal("unrelated history changed")
		}
	}
	if err := CheckReadOnly(ctx, db); !errors.Is(err, ErrPendingMigrations) {
		t.Fatalf("expected naming still pending: %v", err)
	}
	report, err = ReconcileAssistantIndex(ctx, db, true)
	if err != nil || report.Status != "already_reconciled" {
		t.Fatalf("repeat: %+v %v", report, err)
	}
	if err := ApplyPending(ctx, db); err != nil {
		t.Fatal("upgrade after reconciliation", err)
	}
	if err := CheckReadOnly(ctx, db); err != nil {
		t.Fatal(err)
	}
	var afterOID uint32
	var users int
	if err := db.QueryRowContext(ctx, "SELECT 'public.idx_assistant_runs_session_pending'::regclass::oid").Scan(&afterOID); err != nil {
		t.Fatal(err)
	}
	if oid != afterOID {
		t.Fatal("index was recreated")
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE id='retained-user' AND email='retained@example.test'").Scan(&users); err != nil || users != 1 {
		t.Fatal("user data changed", err)
	}
	report, err = ReconcileAssistantIndex(ctx, db, false)
	if err != nil || report.Status != "already_reconciled" {
		t.Fatalf("canonical history: %+v %v", report, err)
	}
}

func TestReconcileAssistantIndexRejectsUnverifiedHistories(t *testing.T) {
	for _, tc := range []struct{ name, mutate string }{
		{"wrong checksum", `UPDATE schema_migrations SET checksum='unverified' WHERE version=35`},
		{"wrong name", `UPDATE schema_migrations SET name='another_migration' WHERE version=35`},
		{"earlier checksum", `UPDATE schema_migrations SET checksum='unverified' WHERE version=34`},
		{"missing history", `DELETE FROM schema_migrations WHERE version=34`},
		{"occupied destination", `INSERT INTO schema_migrations SELECT 39,name,checksum,applied_at,duration_ms FROM schema_migrations WHERE version=35`},
		{"later history", `INSERT INTO schema_migrations VALUES(99,'unknown','unknown',now(),0)`},
		{"missing index", `DROP INDEX idx_assistant_runs_session_pending`},
		{"wrong index", `DROP INDEX idx_assistant_runs_session_pending; CREATE INDEX idx_assistant_runs_session_pending ON assistant_runs(session_id)`},
		{"partial naming", `ALTER TABLE notification_settings RENAME COLUMN connector_enabled TO agent_health_enabled`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			db := conflictingAssistantIndexDatabase(t)
			if _, err := db.ExecContext(ctx, tc.mutate); err != nil {
				t.Fatal(err)
			}
			before, err := AppliedReadOnly(ctx, db)
			if err != nil {
				t.Fatal(err)
			}
			for _, apply := range []bool{false, true} {
				if _, err := ReconcileAssistantIndex(ctx, db, apply); err == nil {
					t.Fatalf("unsafe history accepted apply=%v", apply)
				}
			}
			after, err := AppliedReadOnly(ctx, db)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("rejection changed ledger", err)
			}
		})
	}
}

func TestReconcileAssistantIndexAcceptsSupportedLegacyBaseline(t *testing.T) {
	ctx := context.Background()
	db := conflictingAssistantIndexDatabase(t)
	if _, err := db.ExecContext(ctx, `UPDATE schema_migrations SET name='baseline',checksum='50f7f293c05c9e0636f07dc8ddba6e0a682ef58d31686c820719a411a07ca035' WHERE version=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileAssistantIndex(ctx, db, true); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPending(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileAssistantIndexRollsBackFailedLedgerWrite(t *testing.T) {
	ctx := context.Background()
	db := conflictingAssistantIndexDatabase(t)
	if _, err := db.ExecContext(ctx, `CREATE FUNCTION reject_ledger_update() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test ledger failure'; END $$; CREATE TRIGGER reject_ledger_update BEFORE UPDATE ON schema_migrations FOR EACH ROW EXECUTE FUNCTION reject_ledger_update()`); err != nil {
		t.Fatal(err)
	}
	before, err := AppliedReadOnly(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileAssistantIndex(ctx, db, true); err == nil {
		t.Fatal("failed write reported success")
	}
	after, err := AppliedReadOnly(ctx, db)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("failed write changed history", err)
	}
}
