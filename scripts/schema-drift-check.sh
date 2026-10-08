#!/usr/bin/env bash
set -euo pipefail

status_file="${HANK_SCHEMA_STATUS_FILE:-/tmp/hank-schema-status.txt}"
mode="${HANK_SCHEMA_DRIFT_MODE:-auto}"
env_file="${HANK_CLOUD_ENV_FILE:-.env.cloud}"
report_dir="${HANK_SCHEMA_DRIFT_REPORT_DIR:-./data/schema-drift}"
deep="${HANK_SCHEMA_DRIFT_DEEP:-auto}"
schema_expected_db=""

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

run_direct_status() {
	go run ./cmd/hank-server migrate status --strict >"$status_file"
}

run_compose_status() {
	compose run -T --rm --entrypoint /usr/local/bin/hank-server cloud migrate status --strict >"$status_file"
}

normalize_schema_dump() {
	local input="$1"
	local output="$2"
	sed -E \
		-e '/^-- Dumped from database version /d' \
		-e '/^-- Dumped by pg_dump version /d' \
		-e '/^-- Started on /d' \
		-e '/^-- Completed on /d' \
		-e 's/^\\restrict .*/\\restrict/' \
		-e 's/^\\unrestrict .*/\\unrestrict/' \
		-e '/^SET transaction_timeout = /d' \
		"$input" >"$output"
}

run_deep_compose_check() {
	mkdir -p "$report_dir"
	local stamp expected_db live_dump expected_dump live_norm expected_norm diff_file
	stamp="$(date -u +%Y%m%dT%H%M%SZ)-$$"
	expected_db="hank_schema_expected_${stamp}"
	live_dump="$report_dir/live-${stamp}.sql"
	expected_dump="$report_dir/expected-${stamp}.sql"
	live_norm="$report_dir/live-${stamp}.normalized.sql"
	expected_norm="$report_dir/expected-${stamp}.normalized.sql"
	diff_file="$report_dir/schema-drift-${stamp}.diff"
	schema_expected_db="$expected_db"

	cleanup() {
		if [ -n "${schema_expected_db:-}" ]; then
			compose exec -T postgres sh -ceu 'dropdb --if-exists -U "$POSTGRES_USER" "$1" >/dev/null 2>&1 || true' sh "$schema_expected_db" >/dev/null 2>&1 || true
		fi
	}
	trap cleanup EXIT

	compose exec -T postgres sh -ceu '
		dropdb --if-exists -U "$POSTGRES_USER" "$1" >/dev/null 2>&1 || true
		createdb -U "$POSTGRES_USER" "$1"
	' sh "$expected_db" >/dev/null

	compose run -T --rm -e HANK_SCHEMA_DRIFT_EXPECTED_DB="$expected_db" --entrypoint sh cloud -ceu '
		if [ -z "${POSTGRES_USER:-}" ] || [ -z "${POSTGRES_PASSWORD:-}" ]; then
			echo "POSTGRES_USER and POSTGRES_PASSWORD are required for deep schema drift checks" >&2
			exit 1
		fi
		export HANK_CLOUD_DATABASE_URL="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/${HANK_SCHEMA_DRIFT_EXPECTED_DB}?sslmode=disable"
		/usr/local/bin/hank-server migrate up >/dev/null
	'

	compose exec -T postgres sh -ceu 'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --schema-only --no-owner --no-privileges' >"$live_dump"
	compose exec -T postgres sh -ceu 'pg_dump -U "$POSTGRES_USER" -d "$1" --schema-only --no-owner --no-privileges' sh "$expected_db" >"$expected_dump"
	normalize_schema_dump "$live_dump" "$live_norm"
	normalize_schema_dump "$expected_dump" "$expected_norm"

	if diff -u "$expected_norm" "$live_norm" >"$diff_file"; then
		rm -f "$diff_file"
		printf 'schema_drift=none\n'
		printf 'schema_expected_dump=%s\n' "$expected_norm"
		printf 'schema_live_dump=%s\n' "$live_norm"
		cleanup
		trap - EXIT
	else
		printf 'schema_drift=detected\n' >&2
		printf 'schema_drift_diff=%s\n' "$diff_file" >&2
		cleanup
		trap - EXIT
		return 1
	fi
}

use_compose=false
if [ "$mode" = "compose" ]; then
	use_compose=true
elif [ "$mode" = "direct" ]; then
	use_compose=false
elif compose_postgres_running; then
	use_compose=true
fi

if [ "$use_compose" = true ]; then
	run_compose_status
else
	run_direct_status
fi

cat "$status_file"

if [ "$deep" = "1" ] || [ "$deep" = "true" ] || { [ "$deep" = "auto" ] && [ "$use_compose" = true ]; }; then
	if [ "$use_compose" != true ]; then
		echo "schema_drift_deep=skipped reason=compose_not_selected"
	else
		run_deep_compose_check
	fi
fi
