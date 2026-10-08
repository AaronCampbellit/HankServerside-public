#!/usr/bin/env bash
set -Eeuo pipefail
set +x

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
env_file="${HANK_CLOUD_ENV_FILE:-.env.cloud}"
secret_dir="$repo_root/ops/prometheus/secrets"
alert_secret_dir="$repo_root/ops/alertmanager/secrets"
inbox=0
case "${1:-}" in
  "") ;;
  --inbox) inbox=1 ;;
  *) echo 'usage: scripts/configure-monitoring.sh [--inbox]' >&2; exit 2 ;;
esac
[ "$#" -le 1 ] || exit 2

# Credentials travel through captured stdout and stdin, never command arguments.
load_cloud_token() {
  docker compose --env-file "$env_file" exec -T cloud sh -ceu '
    token="$(printenv "$1")"
    case "$token" in
      ""|*[[:space:]]*) echo "Configure a nonempty dedicated token and recreate cloud first." >&2; exit 1 ;;
    esac
    printf "%s" "$token"
  ' sh "$1"
}
token="$(load_cloud_token HANK_METRICS_SCRAPE_TOKEN)"
webhook_token=""
if [ "$inbox" -eq 1 ]; then
  webhook_token="$(load_cloud_token HANK_ALERTMANAGER_WEBHOOK_TOKEN)"
  [ "$webhook_token" != "$token" ] || { echo 'Inbox and scrape tokens must be distinct.' >&2; exit 1; }
fi
umask 077
mkdir -p "$secret_dir" "$alert_secret_dir"
printf '%s\n%s\n' "$token" "$webhook_token" | docker run --rm -i --network none --user 0:0 \
  --entrypoint /bin/sh --mount "type=bind,src=$secret_dir,dst=/secrets" \
  --mount "type=bind,src=$alert_secret_dir,dst=/alert-secrets" \
  --mount "type=bind,src=$repo_root/ops/prometheus/prometheus.yml,dst=/config/prometheus.yml" \
  --mount "type=bind,src=$repo_root/ops/prometheus/alerts.yml,dst=/config/alerts.yml" \
  --mount "type=bind,src=$repo_root/ops/alertmanager/alertmanager.yml,dst=/config/alertmanager.yml" \
  prom/prometheus:v3.5.0 -ceu '
    umask 077
    IFS= read -r token
    IFS= read -r webhook_token
    [ -n "$token" ] || { echo "No token received; existing file preserved." >&2; exit 1; }
    [ ! -d /secrets/metrics-token ] || { echo "Credential path is a directory; refusing replacement." >&2; exit 1; }
    chgrp 65534 /config/prometheus.yml /config/alerts.yml /config/alertmanager.yml /alert-secrets
    chmod 640 /config/prometheus.yml /config/alerts.yml /config/alertmanager.yml
    chmod 750 /alert-secrets
    write_secret() {
      tmp="$(mktemp "$1.tmp.XXXXXX")"
      trap '\''rm -f "$tmp"'\'' EXIT
      cat > "$tmp"
      chown 65534:65534 "$tmp"
      chmod 400 "$tmp"
      mv -f "$tmp" "$1"
    }
    printf "%s" "$token" | write_secret /secrets/metrics-token
    if [ -n "$webhook_token" ]; then
      [ ! -d /alert-secrets/hank-inbox-token ] || exit 1
      printf "%s" "$webhook_token" | write_secret /alert-secrets/hank-inbox-token
    fi
    # Optional email config/password are supplied separately by the operator.
    for file in /alert-secrets/alertmanager.yml /alert-secrets/smtp-password; do
      if [ -f "$file" ] && [ ! -L "$file" ]; then
        chgrp 65534 "$file"
        chmod 640 "$file"
      fi
    done
  '
printf '%s\n' 'Monitoring credentials synchronized without rotation. Recreate monitoring services, then run scripts/check-monitoring.py.'
