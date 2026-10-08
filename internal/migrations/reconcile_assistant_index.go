package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type AssistantIndexReconciliation struct {
	Status       string `json:"status"`
	FromVersion  int64  `json:"from_version"`
	ToVersion    int64  `json:"to_version"`
	FromChecksum string `json:"from_checksum"`
	ToChecksum   string `json:"to_checksum"`
}

// ReconcileAssistantIndex is an explicit operator migration-history operation.
// Normal startup, status and ApplyPending never call it or accept the conflicting
// checksum. The already applied index is reassigned; naming remains pending.
func ReconcileAssistantIndex(ctx context.Context, db *sql.DB, apply bool) (AssistantIndexReconciliation, error) {
	var report AssistantIndexReconciliation
	all, err := All()
	if err != nil {
		return report, err
	}
	known := map[int64]Migration{}
	for _, m := range all {
		known[m.Version] = m
	}
	index := known[39]
	if index.Name != "assistant_run_resume_index" || known[35].Name != "agent_health_naming" {
		return report, errors.New("unsupported reconciliation migration definitions")
	}
	legacy := index
	legacy.Version, legacy.Checksum = 35, ""
	report = AssistantIndexReconciliation{Status: "eligible", FromVersion: 35, ToVersion: 39, FromChecksum: Checksum(legacy), ToChecksum: Checksum(index)}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !apply, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '5s'; SET LOCAL statement_timeout = '15s'; SET LOCAL search_path = pg_catalog, public"); err != nil {
		return report, err
	}
	if apply {
		// Serialize ledger changes and prevent index DDL while inspecting its
		// definition. No external operations occur while these locks are held.
		if _, err := tx.ExecContext(ctx, "LOCK TABLE public.schema_migrations IN EXCLUSIVE MODE; LOCK TABLE public.assistant_runs IN SHARE MODE; LOCK TABLE public.notification_settings IN ACCESS SHARE MODE"); err != nil {
			return report, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT version, name, checksum FROM public.schema_migrations ORDER BY version")
	if err != nil {
		return report, err
	}
	statuses := map[int64]Status{}
	for rows.Next() {
		var s Status
		if err := rows.Scan(&s.Version, &s.Name, &s.Checksum); err != nil {
			rows.Close()
			return report, err
		}
		statuses[s.Version] = s
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	for version := int64(1); version <= 34; version++ {
		s, ok := statuses[version]
		if !ok || (s.Name != known[version].Name && !(version == 1 && s.Name == "baseline")) || !checksumMatches(s, known[version]) {
			return report, fmt.Errorf("refusing reconciliation: migration %d does not match the supported history", version)
		}
	}
	for version, s := range statuses {
		if version <= 34 {
			if version < 1 {
				return report, errors.New("refusing reconciliation: unknown history")
			}
			continue
		}
		if version == 35 && s.Name == legacy.Name && s.Checksum == report.FromChecksum {
			continue
		}
		if m, ok := known[version]; !ok || s.Name != m.Name || s.Checksum != Checksum(m) {
			return report, fmt.Errorf("refusing reconciliation: unexpected migration %d", version)
		}
	}
	old, hasOld := statuses[35]
	_, hasNew := statuses[39]
	if hasNew && (!hasOld || (old.Name == known[35].Name && old.Checksum == Checksum(known[35]))) {
		report.Status = "already_reconciled"
		return report, nil
	}
	if len(statuses) != 35 || !hasOld || hasNew || old.Name != legacy.Name || old.Checksum != report.FromChecksum {
		return report, errors.New("refusing reconciliation: expected only migrations 1–34 and the original assistant index at 35")
	}
	var definition string
	var valid bool
	err = tx.QueryRowContext(ctx, `SELECT pg_get_indexdef(i.indexrelid), i.indisvalid AND i.indisready AND i.indislive
		FROM pg_index i JOIN pg_class idx ON idx.oid=i.indexrelid JOIN pg_namespace n ON n.oid=idx.relnamespace
		WHERE n.nspname='public' AND idx.relname='idx_assistant_runs_session_pending' AND i.indrelid='public.assistant_runs'::regclass`).Scan(&definition, &valid)
	if err != nil || !valid || definition != "CREATE INDEX idx_assistant_runs_session_pending ON public.assistant_runs USING btree (session_id, created_at DESC) WHERE (completed_at IS NULL)" {
		return report, errors.New("refusing reconciliation: the original assistant index is missing, invalid or has a different definition")
	}
	var oldColumn, newColumn bool
	err = tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='public.notification_settings'::regclass AND attname='connector_enabled' AND NOT attisdropped),
		EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='public.notification_settings'::regclass AND attname='agent_health_enabled' AND NOT attisdropped)`).Scan(&oldColumn, &newColumn)
	if err != nil {
		return report, err
	}
	if !oldColumn || newColumn {
		return report, errors.New("refusing reconciliation: notification naming is already changed or incomplete")
	}
	if !apply {
		return report, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE public.schema_migrations SET version=39, checksum=$1
		WHERE version=35 AND name=$2 AND checksum=$3`, report.ToChecksum, legacy.Name, report.FromChecksum)
	if err != nil {
		return report, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return report, errors.New("reconciliation ledger changed concurrently")
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	report.Status = "reconciled"
	return report, nil
}
