#!/usr/bin/env bash
set -euo pipefail

base_url="${HANK_LOADTEST_BASE_URL:-}"
token="${HANK_LOADTEST_SESSION_TOKEN:-}"
if [[ -z "$base_url" || -z "$token" ]]; then
  echo "set HANK_LOADTEST_BASE_URL and HANK_LOADTEST_SESSION_TOKEN" >&2
  exit 2
fi

out_dir="${HANK_BACKUP_TRAFFIC_REPORT_DIR:-data/backup-traffic/$(date -u +%Y%m%dT%H%M%SZ)}"
mkdir -p "$out_dir"
out_dir="$(cd "$out_dir" && pwd)"
export HANK_LOADTEST_REPORT="$out_dir/loadtest.json"

status_json="$out_dir/storage-status.json"
curl -fsS -H "Authorization: Bearer $token" "${base_url%/}/v1/home/storage/status" > "$out_dir/storage-status-before.json"
initial_backup_label="$(python3 - "$out_dir/storage-status-before.json" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
print(data.get("backup", {}).get("last_backup_label", ""))
PY
)"
initial_restore_at="$(python3 - "$out_dir/storage-status-before.json" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
print(data.get("restore", {}).get("last_test_at", ""))
PY
)"

go test -count=1 -v ./tools/loadtest > "$out_dir/loadtest.log" 2>&1 &
load_pid=$!

sleep "${HANK_BACKUP_TRAFFIC_WARMUP_SECONDS:-5}"
curl -fsS -X POST \
  -H "Authorization: Bearer $token" \
  -H "Content-Type: application/json" \
  -d '{"backup_type":"full"}' \
  "${base_url%/}/v1/home/storage/backup" > "$out_dir/backup-intent.json"

wait "$load_pid"

backup_label=""
deadline=$((SECONDS + ${HANK_BACKUP_TRAFFIC_WAIT_SECONDS:-600}))
while (( SECONDS < deadline )); do
  curl -fsS -H "Authorization: Bearer $token" "${base_url%/}/v1/home/storage/status" > "$status_json"
  backup_label="$(python3 - "$status_json" "$initial_backup_label" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
label = data.get("backup", {}).get("last_backup_label", "")
initial = sys.argv[2]
print(label if label and label != initial else "")
PY
  )"
  [[ -n "$backup_label" ]] && break
  sleep 5
done
if [[ -z "$backup_label" ]]; then
  echo "backup did not complete with a new label before timeout" >&2
  exit 1
fi

curl -fsS -X POST \
  -H "Authorization: Bearer $token" \
  -H "Content-Type: application/json" \
  -d "{\"backup_label\":\"$backup_label\"}" \
  "${base_url%/}/v1/home/storage/restore-test" > "$out_dir/restore-test-intent.json"

restore_completed=""
deadline=$((SECONDS + ${HANK_RESTORE_TEST_WAIT_SECONDS:-900}))
while (( SECONDS < deadline )); do
  curl -fsS -H "Authorization: Bearer $token" "${base_url%/}/v1/home/storage/status" > "$out_dir/storage-status-after-restore.json"
  restore_completed="$(python3 - "$out_dir/storage-status-after-restore.json" "$initial_restore_at" <<'PY'
import json, sys
data = json.load(open(sys.argv[1]))
last_test_at = data.get("restore", {}).get("last_test_at", "")
initial = sys.argv[2]
print(last_test_at if last_test_at and last_test_at != initial else "")
PY
  )"
  [[ -n "$restore_completed" ]] && break
  sleep 5
done
if [[ -z "$restore_completed" ]]; then
  echo "restore-test did not complete before timeout" >&2
  exit 1
fi

{
  echo "# Backup During Traffic"
  echo
  echo "- generated_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "- backup_label: ${backup_label:-none}"
  echo "- restore_test_at: ${restore_completed:-none}"
  echo "- loadtest_json: loadtest.json"
  echo "- storage_status: storage-status.json"
} > "$out_dir/report.md"

echo "$out_dir/report.md"
