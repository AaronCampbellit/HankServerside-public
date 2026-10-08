#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

env_file=".env.agent"
cloud_env_file=".env.cloud"

log() {
  printf '[agent-env] %s\n' "$*"
}

fail() {
  printf '[agent-env] ERROR: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

if [ ! -f "$cloud_env_file" ]; then
  fail "$cloud_env_file is missing; run scripts/bootstrap-first-run.sh first"
fi

require_command docker
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required"

tmp_file="$(mktemp "${env_file}.tmp.XXXXXX")"
trap 'rm -f "$tmp_file"' EXIT

cat >"$tmp_file"

if [ ! -s "$tmp_file" ]; then
  fail "no .env.agent content received on stdin"
fi

for key in HANK_AGENT_CLOUD_URL HANK_AGENT_ID HANK_AGENT_TOKEN HANK_AGENT_HOME_NAME; do
  if ! grep -Eq "^${key}=.+" "$tmp_file"; then
    fail "input is missing required ${key}"
  fi
done

if [ -e "$env_file" ]; then
  backup="${env_file}.$(date +%Y%m%d%H%M%S).bak"
  cp "$env_file" "$backup"
  chmod 600 "$backup"
  log "backed up existing $env_file to $backup"
fi

install -m 600 "$tmp_file" "$env_file"
log "wrote $env_file"

docker compose --env-file "$cloud_env_file" --profile agent up -d agent
log "agent service started"
