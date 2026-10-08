#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
scenarios=(credential_replay wrong_side wrong_session wrong_epoch process_session_33 home_session_5 duplicate_operator frame_4mib_plus_1 rate_50mib_plus_1 queue_16mib_plus_1 idle_expiry reconnect_expiry hard_expiry slow_consumer revocation server_restart malformed_frame unrelated_api_files_isolation)
test_for_scenario() {
  case "$1" in
    credential_replay) echo TestDesktopLoadCredentialReplay ;;
    wrong_side) echo TestDesktopLoadWrongSide ;;
    wrong_session) echo TestDesktopLoadWrongSession ;;
    wrong_epoch) echo TestDesktopLoadWrongEpoch ;;
    process_session_33) echo TestDesktopLoadProcessSession33 ;;
    home_session_5) echo TestDesktopLoadHomeSession5 ;;
    duplicate_operator) echo TestDesktopLoadDuplicateOperator ;;
    frame_4mib_plus_1) echo TestDesktopLoadFrameLimit ;;
    rate_50mib_plus_1) echo TestDesktopLoadRateLimit ;;
    queue_16mib_plus_1) echo TestDesktopLoadQueueLimit ;;
    idle_expiry) echo TestDesktopLoadIdleExpiry ;;
    reconnect_expiry) echo TestDesktopLoadReconnectExpiry ;;
    hard_expiry) echo TestDesktopLoadHardExpiry ;;
    slow_consumer) echo TestDesktopLoadSlowConsumer ;;
    revocation) echo TestDesktopLoadRevocation ;;
    malformed_frame|server_restart|unrelated_api_files_isolation) return 1 ;;
    *) return 1 ;;
  esac
}
evidence="${HANK_DESKTOP_EVIDENCE_DIR:-.codex/remote-desktop-evidence}"
mkdir -p "$evidence"; umask 077
report="$evidence/load-$(date -u +%Y%m%dT%H%M%SZ).jsonl"
python3 ./scripts/validate-remote-desktop-receipt.py --self-test
run_exact_go_test() {
  package="$1"; test_name="$2"
  listed="$(go test "$package" -list "^${test_name}$" 2>&1)" || { printf '%s\n' "$listed" >&2; return 1; }
  [[ "$(printf '%s\n' "$listed" | grep -c "^${test_name}$")" -eq 1 ]] || { echo "portable test absent or ambiguous: $package $test_name" >&2; return 1; }
  json="$(go test -json "$package" -run "^${test_name}$" -count=1 2>&1)" || { printf '%s\n' "$json" >&2; return 1; }
  printf '%s\n' "$json" | grep -F '"Action":"run"' | grep -F "\"Test\":\"$test_name\"" >/dev/null || { echo "portable test did not run: $test_name" >&2; return 1; }
  printf '%s\n' "$json" | grep -F '"Action":"pass"' | grep -F "\"Test\":\"$test_name\"" >/dev/null || { echo "portable test did not pass: $test_name" >&2; return 1; }
}

if [[ "${1:-}" == "--contract-only" ]]; then
  for scenario in "${scenarios[@]}"; do
    if test_name="$(test_for_scenario "$scenario")"; then
      run_exact_go_test ./internal/cloud "$test_name" || exit 1
      printf '{"schema":1,"mode":"contract_only","scenario":"%s","package":"./internal/cloud","test":"%s","status":"contract_pass","native_evidence":false}\n' "$scenario" "$test_name" >>"$report"
    else
      printf '{"schema":1,"mode":"contract_only","scenario":"%s","status":"not_run_integration_required","native_evidence":false}\n' "$scenario" >>"$report"
    fi
  done
  [[ "$(wc -l < "$report" | tr -d ' ')" -eq "${#scenarios[@]}" ]] || { echo 'incomplete contract receipt' >&2; exit 1; }
  echo "Remote Desktop portable load-contract receipt: $report"
  exit 0
fi

driver="${HANK_DESKTOP_ABUSE_DRIVER:-}"
[[ -n "$driver" && -x "$driver" ]] || { echo 'set HANK_DESKTOP_ABUSE_DRIVER to the physical/staging abuse driver' >&2; exit 77; }
for scenario in "${scenarios[@]}"; do
  raw="$($driver --scenario "$scenario" --metadata-only)"
  line="$(python3 ./scripts/validate-remote-desktop-receipt.py --kind scenario --name "$scenario" --platform any-native <<<"$raw")" || exit 1
  printf '%s\n' "$line" >>"$report"
done
[[ "$(wc -l < "$report" | tr -d ' ')" -eq "${#scenarios[@]}" ]] || { echo 'incomplete native load receipt' >&2; exit 1; }
echo "Remote Desktop native load evidence: $report"
