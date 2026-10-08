#!/usr/bin/env bash
set -euo pipefail

env_file="${HANK_CLOUD_ENV_FILE:-.env.cloud}"
run_id="${HANK_MIGRATION_BASELINE_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
report_dir="${HANK_MIGRATION_BASELINE_REPORT_DIR:-data/migration-baseline}"
test_db="hank_migration_baseline_${run_id//[^A-Za-z0-9_]/_}"

compose() {
	if [ -n "${COMPOSE_PROJECT_NAME:-}" ]; then
		docker compose --env-file "$env_file" -p "$COMPOSE_PROJECT_NAME" "$@"
	else
		docker compose --env-file "$env_file" "$@"
	fi
}

if ! compose ps --services --status running 2>/dev/null | grep -qx postgres; then
	echo "postgres service is not running in the compose project" >&2
	exit 1
fi
if ! compose ps --services --status running 2>/dev/null | grep -qx cloud; then
	echo "cloud service is not running in the compose project" >&2
	exit 1
fi

mkdir -p "$report_dir"
report="$report_dir/migration-baseline-${run_id}.txt"
status_file="$report_dir/migration-status-${run_id}.txt"

pg_env="$(compose exec -T postgres sh -ceu 'printf "%s\n%s\n" "$POSTGRES_USER" "${POSTGRES_PASSWORD:-}"')"
pg_user="$(printf '%s\n' "$pg_env" | sed -n '1p')"
pg_password="$(printf '%s\n' "$pg_env" | sed -n '2p')"
if [ -z "$pg_user" ]; then
	echo "POSTGRES_USER is empty" >&2
	exit 1
fi

if [ -n "$pg_password" ]; then
	test_database_url="postgres://${pg_user}:${pg_password}@postgres:5432/${test_db}?sslmode=disable"
else
	test_database_url="postgres://${pg_user}@postgres:5432/${test_db}?sslmode=disable"
fi

cleanup() {
	compose exec -T -e HANK_MIGRATION_TEST_DB="$test_db" postgres sh -ceu 'dropdb -U "$POSTGRES_USER" --if-exists "$HANK_MIGRATION_TEST_DB"' >/dev/null 2>&1 || true
}
trap cleanup EXIT

compose exec -T -e HANK_MIGRATION_TEST_DB="$test_db" postgres sh -ceu 'dropdb -U "$POSTGRES_USER" --if-exists "$HANK_MIGRATION_TEST_DB"; createdb -U "$POSTGRES_USER" "$HANK_MIGRATION_TEST_DB"'

started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

compose exec -T \
	-e HANK_CLOUD_DATABASE_URL="$test_database_url" \
	cloud /usr/local/bin/hank-server migrate up >"$report_dir/migration-up-${run_id}.log"

# Baseline adoption starts with the original schema, not a current schema whose
# migration ledger was discarded. Later migrations are intentionally not all
# idempotent. Recreate only this disposable validation database before seeding v1.
compose exec -T -e HANK_MIGRATION_TEST_DB="$test_db" postgres sh -ceu 'dropdb -U "$POSTGRES_USER" "$HANK_MIGRATION_TEST_DB"; createdb -U "$POSTGRES_USER" "$HANK_MIGRATION_TEST_DB"'
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
baseline_files=("$repo_root"/internal/migrations/sql/000001_*.up.sql)
if [ "${#baseline_files[@]}" -ne 1 ] || [ ! -f "${baseline_files[0]}" ]; then
	echo "expected exactly one version-one baseline migration" >&2
	exit 1
fi
compose exec -T -e HANK_MIGRATION_TEST_DB="$test_db" postgres sh -ceu 'psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$HANK_MIGRATION_TEST_DB"' <"${baseline_files[0]}" >"$report_dir/legacy-schema-${run_id}.log"

compose exec -T \
	-e HANK_CLOUD_DATABASE_URL="$test_database_url" \
	cloud /usr/local/bin/hank-server migrate baseline >"$report_dir/migration-baseline-${run_id}.log"

compose exec -T \
	-e HANK_CLOUD_DATABASE_URL="$test_database_url" \
	cloud /usr/local/bin/hank-server migrate up >"$report_dir/migration-post-baseline-up-${run_id}.log"

compose exec -T \
	-e HANK_CLOUD_DATABASE_URL="$test_database_url" \
	cloud /usr/local/bin/hank-server migrate status --strict >"$status_file"

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
{
	echo "started_at=$started_at"
	echo "finished_at=$finished_at"
	echo "run_id=$run_id"
	echo "test_database=$test_db"
	echo "migration_status=$status_file"
	echo "status=pass"
	echo
	cat "$status_file"
} >"$report"

cat "$report"
