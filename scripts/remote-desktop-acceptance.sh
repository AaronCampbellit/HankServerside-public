#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
requirements=(physical_console concurrent_local_remote_input view_only clipboard_both_directions fresh_key_reconnect local_indicator_and_termination three_termination_actors relay_opacity content_free_audit windows_elevated_uac macos_permission_loss_recovery)
mapping_for_requirement() {
  case "$1" in
    physical_console|concurrent_local_remote_input|windows_elevated_uac|macos_permission_loss_recovery) return 1 ;;
    view_only) echo './internal/protocol TestDesktopPermissionSetRejectsInvalidCombinations' ;;
    clipboard_both_directions) echo './internal/protocol TestDesktopClipboardAndSpecialKeyBounds' ;;
    fresh_key_reconnect) echo './internal/cloud TestDesktopServiceReconnectClampsHardExpiryAndChecksOwner' ;;
    local_indicator_and_termination) echo './internal/cloud TestDesktopTerminalAgentAcknowledgementsAreIdempotentAfterAuthoritativeClose' ;;
    three_termination_actors) echo './internal/cloud TestDesktopSyntheticTerminationActorsCloseBothSockets' ;;
    relay_opacity) echo './internal/cloud TestDesktopRelayForwardsOpaqueBinaryWithoutRetention' ;;
    content_free_audit) echo './internal/cloud TestDesktopAuditNeverIncludesSensitivePayloads' ;;
    *) return 1 ;;
  esac
}

evidence="${HANK_DESKTOP_EVIDENCE_DIR:-.codex/remote-desktop-evidence}"
mkdir -p "$evidence"; umask 077
report="$evidence/acceptance-$(date -u +%Y%m%dT%H%M%SZ).jsonl"
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
  ./scripts/remote-desktop-load-validation.sh --contract-only
  test -f docs/remote-desktop/v1-acceptance.md
  for requirement in "${requirements[@]}"; do
    if mapping="$(mapping_for_requirement "$requirement")"; then
      package="${mapping%% *}"; test_name="${mapping#* }"
      run_exact_go_test "$package" "$test_name" || exit 1
      printf '{"schema":1,"mode":"contract_only","requirement":"%s","package":"%s","test":"%s","status":"contract_pass","native_evidence":false}\n' "$requirement" "$package" "$test_name" >>"$report"
    else
      printf '{"schema":1,"mode":"contract_only","requirement":"%s","status":"not_run_physical_device_required","native_evidence":false}\n' "$requirement" >>"$report"
    fi
  done
  [[ "$(wc -l < "$report" | tr -d ' ')" -eq "${#requirements[@]}" ]] || { echo 'incomplete portable acceptance receipt' >&2; exit 1; }
  echo "Remote Desktop portable acceptance receipt (not native release evidence): $report"
  exit 0
fi

driver="${HANK_DESKTOP_NATIVE_DRIVER:-}"
[[ -n "$driver" && -x "$driver" ]] || { echo 'set HANK_DESKTOP_NATIVE_DRIVER on packaged physical Windows/macOS test devices' >&2; exit 77; }
duration="${HANK_DESKTOP_LONGEVITY_HOURS:-8}"
[[ "$duration" =~ ^[0-9]+$ && "$duration" -ge 8 ]] || { echo 'longevity must be at least eight hours' >&2; exit 2; }
for platform in windows macos; do
  for requirement in "${requirements[@]}"; do
    raw="$($driver --platform "$platform" --requirement "$requirement" --metadata-only)"
    line="$(python3 ./scripts/validate-remote-desktop-receipt.py --kind requirement --name "$requirement" --platform "$platform" <<<"$raw")" || exit 1
    printf '%s\n' "$line" >>"$report"
  done
  for scenario in longevity compatibility_upgrade_rollback; do
    if [[ "$scenario" == longevity ]]; then
      line="$($driver --platform "$platform" --scenario "$scenario" --longevity-hours "$duration" --reconnects 3 --display-changes --lock-unlock --quality-degrade-recover --local-input --assert-hard-expiry --metadata-only)"
    else
      line="$($driver --platform "$platform" --scenario "$scenario" --current --prior-compatible --reject-ipc-major --upgrade --rollback --service-restart --server-restart --rotate --trust-reset --metadata-only)"
    fi
    raw="$line"
    line="$(python3 ./scripts/validate-remote-desktop-receipt.py --kind scenario --name "$scenario" --platform "$platform" <<<"$raw")" || exit 1
    printf '%s\n' "$line" >>"$report"
  done
done
expected=$(( ${#requirements[@]} * 2 + 4 ))
[[ "$(wc -l < "$report" | tr -d ' ')" -eq "$expected" ]] || { echo 'incomplete native acceptance receipt' >&2; exit 1; }
echo "Remote Desktop native acceptance evidence: $report"
