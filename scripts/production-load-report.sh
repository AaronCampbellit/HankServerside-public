#!/usr/bin/env bash
set -euo pipefail

base_url="${HANK_LOADTEST_BASE_URL:-}"
token="${HANK_LOADTEST_SESSION_TOKEN:-}"
if [[ -z "$base_url" || -z "$token" ]]; then
  echo "set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN" >&2
  exit 2
fi

out_dir="${HANK_LOAD_REPORT_DIR:-data/load-reports/$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"
export HANK_LOADTEST_REPORT="$out_dir/loadtest.json"

echo "running load test report into $out_dir"
go test -count=1 -v ./tools/loadtest | tee "$out_dir/go-test.log"

required_scenarios="${HANK_LOADTEST_REQUIRED_SCENARIOS:-health,session_validation,app_websocket_relay,app_websocket_reconnect,file_transfers,cross_source_move_jobs,notes_list,assistant_requests}"
if [[ -f "$out_dir/loadtest.json" ]]; then
  python3 - "$out_dir/loadtest.json" "$required_scenarios" > "$out_dir/scenario-summary.txt" <<'PY'
import json, sys

path = sys.argv[1]
required = [item for item in sys.argv[2].split(",") if item]
data = json.load(open(path))
seen = {row.get("scenario"): row for row in data.get("scenarios", [])}
missing = [name for name in required if name not in seen]
print("scenario,count,errors,error_rate,p50_ms,p95_ms,p99_ms")
for name in required:
    row = seen.get(name)
    if not row:
        print(f"{name},missing,missing,missing,missing,missing,missing")
        continue
    print(
        f"{name},{row.get('count', 0)},{row.get('errors', 0)},"
        f"{row.get('error_rate', 0):.6f},{row.get('p50_ms', 0)},"
        f"{row.get('p95_ms', 0)},{row.get('p99_ms', 0)}"
    )
if missing:
    print("missing_scenarios=" + ",".join(missing), file=sys.stderr)
    sys.exit(3)
PY
fi

project="${COMPOSE_PROJECT_NAME:-hankserverside}"
if command -v docker >/dev/null 2>&1; then
  docker stats --no-stream --format '{{json .}}' > "$out_dir/docker-stats.jsonl" || true
  docker compose -p "$project" ps > "$out_dir/compose-ps.txt" || true
  if [[ -s "$out_dir/docker-stats.jsonl" ]]; then
    python3 - "$out_dir/docker-stats.jsonl" > "$out_dir/resource-summary.txt" <<'PY' || true
import json, sys

interesting = ("cloud", "postgres", "db-ops", "agent")
for line in open(sys.argv[1]):
    line = line.strip()
    if not line:
        continue
    row = json.loads(line)
    name = row.get("Name", "")
    if not any(part in name for part in interesting):
        continue
    print(
        f"{name}: cpu={row.get('CPUPerc', '')} mem={row.get('MemUsage', '')} "
        f"net={row.get('NetIO', '')} block={row.get('BlockIO', '')}"
    )
PY
  fi
fi

if [[ -n "${HANK_DATABASE_URL:-}" ]] && command -v psql >/dev/null 2>&1; then
  psql "$HANK_DATABASE_URL" -v ON_ERROR_STOP=1 -P pager=off -c "SELECT now() AS sampled_at, datname, numbackends, xact_commit, xact_rollback, blks_read, blks_hit FROM pg_stat_database WHERE datname = current_database();" > "$out_dir/db-stats.txt" || true
elif command -v docker >/dev/null 2>&1; then
  docker compose -p "$project" exec -T postgres sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -P pager=off -c "SELECT now() AS sampled_at, datname, numbackends, xact_commit, xact_rollback, blks_read, blks_hit FROM pg_stat_database WHERE datname = current_database();"' > "$out_dir/db-stats.txt" || true
fi

{
  echo "# Hank Production Load Report"
  echo
  echo "- generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "- base_url: $base_url"
  echo "- loadtest_json: loadtest.json"
  echo "- docker_stats: docker-stats.jsonl"
  echo "- db_stats: db-stats.txt"
  echo "- scenario_summary: scenario-summary.txt"
  echo "- resource_summary: resource-summary.txt"
  echo
  echo "## Load Test"
  echo
  if [[ -f "$out_dir/loadtest.json" ]]; then
    cat "$out_dir/loadtest.json"
  else
    echo "loadtest report was not produced"
  fi
  echo
  echo "## Scenario Summary"
  echo
  if [[ -f "$out_dir/scenario-summary.txt" ]]; then
    cat "$out_dir/scenario-summary.txt"
  else
    echo "scenario summary was not produced"
  fi
  echo
  echo "## Resource Summary"
  echo
  if [[ -f "$out_dir/resource-summary.txt" ]]; then
    cat "$out_dir/resource-summary.txt"
  else
    echo "resource summary was not produced"
  fi
  echo
  echo "## Database Summary"
  echo
  if [[ -f "$out_dir/db-stats.txt" ]]; then
    cat "$out_dir/db-stats.txt"
  else
    echo "database summary was not produced"
  fi
} > "$out_dir/report.md"

echo "$out_dir/report.md"
