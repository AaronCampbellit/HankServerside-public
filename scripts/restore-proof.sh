#!/usr/bin/env bash
set -euo pipefail

started_epoch="$(date -u +%s)"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
report_dir="${HANK_RESTORE_REPORT_DIR:-./data/restore-reports}"
mode="${HANK_SCHEMA_DRIFT_MODE:-auto}"
env_file="${HANK_CLOUD_ENV_FILE:-.env.cloud}"
mkdir -p "$report_dir"
report="$report_dir/restore-proof-$(date -u +%Y%m%dT%H%M%SZ).txt"

compose() {
	if [ -n "${COMPOSE_PROJECT_NAME:-}" ]; then
		docker compose --env-file "$env_file" -p "$COMPOSE_PROJECT_NAME" "$@"
	else
		docker compose --env-file "$env_file" "$@"
	fi
}

compose_postgres_running() {
	[ -f "$env_file" ] &&
		command -v docker >/dev/null 2>&1 &&
		compose ps --services --status running 2>/dev/null | grep -qx postgres
}

run_schema_status() {
	if [ "$mode" = "compose" ]; then
		compose run -T --rm cloud /usr/local/bin/hank-server migrate status --strict
	elif [ "$mode" = "direct" ]; then
		go run ./cmd/hank-server migrate status --strict
	elif compose_postgres_running; then
		compose run -T --rm cloud /usr/local/bin/hank-server migrate status --strict
	else
		go run ./cmd/hank-server migrate status --strict
	fi
}

psql_live() {
	compose exec -T postgres sh -ceu 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"' sh "$@"
}

psql_restore() {
	compose exec -T postgres-restore sh -ceu 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" "$@"' sh "$@"
}

table_count_query='WITH table_counts(name, count_value) AS (
	VALUES
		('"'"'users'"'"', (SELECT count(*) FROM users)),
		('"'"'homes'"'"', (SELECT count(*) FROM homes)),
		('"'"'agents'"'"', (SELECT count(*) FROM agents)),
		('"'"'user_notes'"'"', (SELECT count(*) FROM user_notes)),
		('"'"'note_attachments'"'"', (SELECT count(*) FROM note_attachments)),
		('"'"'assistant_file_index'"'"', (SELECT count(*) FROM assistant_file_index)),
		('"'"'file_transfers'"'"', (SELECT count(*) FROM file_transfers)),
		('"'"'file_operation_jobs'"'"', (SELECT count(*) FROM file_operation_jobs))
)
SELECT string_agg(name || '"'"'='"'"' || count_value::text, '"'"','"'"' ORDER BY name) FROM table_counts;'

schema_status="$(run_schema_status 2>&1 || true)"
status_result="pass"
if ! printf '%s\n' "$schema_status" | grep -q '^000001 '; then
	status_result="failed"
fi

restore_available=false
live_counts=""
restore_counts=""
attachment_restore=""
storage_events=""
if [ "$mode" = "compose" ] || { [ "$mode" = "auto" ] && compose_postgres_running; }; then
	compose --profile restore up -d postgres-restore >/dev/null
	for _ in $(seq 1 60); do
		if compose exec -T postgres-restore sh -ceu 'pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB"' >/dev/null 2>&1; then
			restore_available=true
			break
		fi
		sleep 2
	done
	if [ "$restore_available" = true ]; then
		live_counts="$(psql_live -Atc "$table_count_query")"
		restore_counts="$(psql_restore -Atc "$table_count_query")"
		attachment_restore="$(compose exec -T db-ops sh -ceu '
			root="${HANK_NOTE_ATTACHMENTS_RESTORE_DIR:-/var/lib/hank/note-attachments-restore}"
			if [ ! -d "$root" ]; then
				echo "attachment_restore_dir=missing"
				exit 0
			fi
			count="$(find "$root" -type f | wc -l | tr -d " ")"
			bytes=0
			for size in $(find "$root" -type f -exec stat -c %s {} \; 2>/dev/null); do
				bytes=$((bytes + size))
			done
			echo "attachment_restore_files=$count"
			echo "attachment_restore_bytes=$bytes"
		' 2>/dev/null || true)"
	fi
	storage_events="$(compose exec -T db-ops sh -ceu '
		log="${HANK_DB_OPS_LOG_DIR:-/var/log/hank/db-ops}/storage-events.jsonl"
		if [ -f "$log" ]; then
			grep -E "\"operation\":\"(backup|restore_test)\".*\"status\":\"success\"" "$log" | tail -n 5
		fi
	' 2>/dev/null || true)"
fi

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
finished_epoch="$(date -u +%s)"
duration_seconds=$((finished_epoch - started_epoch))

{
	echo "started_at=$started_at"
	echo "finished_at=$finished_at"
	echo "report_duration_seconds=$duration_seconds"
	echo "schema_status_result=$status_result"
	echo "schema_status_begin"
	printf '%s\n' "$schema_status"
	echo "schema_status_end"
	echo "restore_database_available=$restore_available"
	if [ -n "$live_counts" ]; then
		echo "live_counts=$live_counts"
	fi
	if [ -n "$restore_counts" ]; then
		echo "restore_counts=$restore_counts"
	fi
	if [ -n "$attachment_restore" ]; then
		printf '%s\n' "$attachment_restore"
	fi
	if [ -n "$storage_events" ]; then
		echo "storage_success_events_begin"
		printf '%s\n' "$storage_events"
		echo "storage_success_events_end"
	fi
} >"$report"

if [ "$status_result" != "pass" ]; then
	echo "$report"
	echo "schema status failed" >&2
	exit 1
fi
if [ "$restore_available" = true ] && [ "$live_counts" != "$restore_counts" ]; then
	echo "$report"
	echo "restore counts differ" >&2
	exit 1
fi

echo "$report"
