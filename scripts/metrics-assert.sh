#!/usr/bin/env bash
set -euo pipefail

base_url="${HANK_METRICS_BASE_URL:-${HANK_LOADTEST_BASE_URL:-}}"
token="${HANK_METRICS_SESSION_TOKEN:-${HANK_LOADTEST_SESSION_TOKEN:-}}"
if [[ -z "$base_url" || -z "$token" ]]; then
  echo "set HANK_METRICS_BASE_URL and HANK_METRICS_SESSION_TOKEN" >&2
  exit 2
fi

metrics="$(curl -fsS -H "Authorization: Bearer $token" "${base_url%/}/metrics")"
required=(
  "hank_http_requests_total"
  "hank_http_latency_seconds_sum"
  "hank_relay_latency_seconds_sum"
  "hank_db_backup_last_success_unixtime"
  "hank_db_restore_test_last_success_unixtime"
  "hank_attachment_storage_bytes"
  "hank_file_operation_jobs"
  "hank_assistant_provider_requests_total"
  "hank_go_alloc_bytes"
  "hank_db_open_connections"
  "hank_primary_agent_online"
  "hank_db_ping_success"
  "hank_cloud_runtime_heartbeat_age_seconds"
  "hank_cloud_runtime_up"
  "hank_desktop_sessions"
  "hank_desktop_join_total"
  "hank_desktop_reconnect_total"
  "hank_desktop_terminated_total"
  "hank_desktop_relay_bytes_total"
  "hank_desktop_relay_backpressure_total"
  "hank_desktop_readiness"
  "hank_desktop_readiness_reported"
)

missing=0
for name in "${required[@]}"; do
  if ! grep -q "^$name" <<<"$metrics"; then
    echo "missing metric: $name" >&2
    missing=1
  fi
done

if [[ "$missing" -ne 0 ]]; then
  exit 1
fi

echo "metrics assertions passed"
