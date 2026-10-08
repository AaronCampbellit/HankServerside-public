#!/usr/bin/env bash
set -euo pipefail

out_dir="${HANK_QUERY_REPORT_DIR:-data/query-telemetry}"
mkdir -p "$out_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
report="$out_dir/top-queries-$stamp.md"

sql=$(cat <<'SQL'
SELECT
  queryid,
  calls,
  round(total_exec_time::numeric, 3) AS total_exec_ms,
  round(mean_exec_time::numeric, 3) AS mean_exec_ms,
  rows,
  left(regexp_replace(query, '\s+', ' ', 'g'), 240) AS query
FROM pg_stat_statements
ORDER BY total_exec_time DESC
LIMIT 20;
SQL
)

run_psql() {
  if [[ -n "${HANK_DATABASE_URL:-}" ]]; then
    psql "$HANK_DATABASE_URL" -v ON_ERROR_STOP=1 -P pager=off -c "$sql"
    return
  fi
  project="${COMPOSE_PROJECT_NAME:-hankserverside}"
  docker compose -p "$project" exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -P pager=off' <<<"$sql"
}

{
  echo "# Hank Query Telemetry"
  echo
  echo "- generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "- source: pg_stat_statements"
  echo
  echo '```'
  run_psql
  echo '```'
} > "$report"

echo "$report"
