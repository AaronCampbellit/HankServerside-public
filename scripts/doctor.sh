#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

env_file=".env.cloud"
failures=0
warnings=0

pass() {
  printf '[doctor] ok: %s\n' "$*"
}

warn() {
  warnings=$((warnings + 1))
  printf '[doctor] warn: %s\n' "$*" >&2
}

fail() {
  failures=$((failures + 1))
  printf '[doctor] fail: %s\n' "$*" >&2
}

command_exists() {
  command -v "$1" >/dev/null 2>&1
}

file_mode() {
  local path="$1"
  if stat -c '%a' "$path" >/dev/null 2>&1; then
    stat -c '%a' "$path"
    return
  fi
  stat -f '%Lp' "$path"
}

check_secret_mode() {
  local path="$1"
  local mode
  mode="$(file_mode "$path")"
  if (( (8#$mode & 077) != 0 )); then
    fail "$path mode is $mode; run chmod 600 $path"
  else
    pass "$path mode is $mode"
  fi
}

env_value() {
  local key="$1"
  awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      gsub(/^"/, "")
      gsub(/"$/, "")
      print
      exit
    }
  ' "$env_file"
}

compose() {
  docker compose --env-file "$env_file" "$@"
}

if [ -n "${HANK_CLOUD_ENV_FILE:-}" ] && [ "${HANK_CLOUD_ENV_FILE}" != "$env_file" ] && [ "${HANK_CLOUD_ENV_FILE}" != "./$env_file" ]; then
  fail "doctor expects repo-root .env.cloud; unset HANK_CLOUD_ENV_FILE for production Compose checks"
fi

if command_exists docker; then
  pass "docker command is available"
else
  fail "docker command is missing"
fi

if command_exists docker && docker compose version >/dev/null 2>&1; then
  pass "docker compose v2 is available"
else
  fail "docker compose v2 is missing"
fi

if command_exists curl; then
  pass "curl command is available"
else
  fail "curl command is missing"
fi

if [ -f "$env_file" ]; then
  pass "$env_file exists"
  check_secret_mode "$env_file"

  web_push_fields=0
  [ -n "$(env_value HANK_WEB_PUSH_VAPID_PUBLIC_KEY)" ] && web_push_fields=$((web_push_fields + 1))
  [ -n "$(env_value HANK_WEB_PUSH_VAPID_PRIVATE_KEY)" ] && web_push_fields=$((web_push_fields + 1))
  [ -n "$(env_value HANK_WEB_PUSH_VAPID_SUBJECT)" ] && web_push_fields=$((web_push_fields + 1))
  if [ "$web_push_fields" -eq 0 ]; then
    pass "Web Push is disabled (no VAPID configuration)"
  elif [ "$web_push_fields" -eq 3 ]; then
    pass "Web Push VAPID configuration is complete"
  else
    fail "Web Push requires public key, private key, and subject together"
  fi
else
  fail "$env_file is missing; run scripts/bootstrap-first-run.sh first"
fi

if [ -f ".env.agent" ]; then
  pass ".env.agent exists"
  check_secret_mode ".env.agent"
else
  warn ".env.agent is not present yet; the Hank Agent has not been started"
fi

if [ "$failures" -eq 0 ]; then
  if compose config --quiet >/dev/null 2>&1; then
    pass "docker compose config is valid"
  else
    fail "docker compose config is invalid"
  fi

  printf '\n[doctor] compose services:\n'
  compose ps || fail "docker compose ps failed"

  port="$(env_value HANK_CLOUD_HOST_PORT)"
  port="${port:-18080}"
  if curl -fsS "http://127.0.0.1:${port}/healthz" >/dev/null 2>&1; then
    pass "cloud /healthz responds on localhost:${port}"
  else
    fail "cloud /healthz did not respond on localhost:${port}"
  fi

  if curl -fsS "http://127.0.0.1:${port}/readyz" >/dev/null 2>&1; then
    pass "cloud /readyz responds on localhost:${port}"
  else
    fail "cloud /readyz did not respond on localhost:${port}"
  fi

  if compose run --rm --entrypoint /usr/local/bin/hank-server cloud migrate status --strict >/tmp/hank-migration-status.$$ 2>/tmp/hank-migration-status-err.$$; then
    pass "migration status is strict-clean"
  else
    fail "migration status check failed"
    sed -n '1,80p' /tmp/hank-migration-status-err.$$ >&2 || true
  fi
  rm -f /tmp/hank-migration-status.$$ /tmp/hank-migration-status-err.$$

  if compose run --rm --entrypoint /usr/local/bin/hank-server cloud secrets status --strict >/tmp/hank-secrets-status.$$ 2>/tmp/hank-secrets-status-err.$$; then
    pass "secret storage is strict-clean (no plaintext legacy rows)"
  else
    fail "secret storage strict check failed; set HANK_SECRET_ENCRYPTION_KEY and run 'hank-server secrets reencrypt', then re-run doctor"
    sed -n '1,40p' /tmp/hank-secrets-status-err.$$ >&2 || true
    sed -n '1,40p' /tmp/hank-secrets-status.$$ >&2 || true
  fi
  rm -f /tmp/hank-secrets-status.$$ /tmp/hank-secrets-status-err.$$

  monitoring_services="$(docker compose --env-file "$env_file" --profile monitoring ps --services --status running 2>/dev/null || true)"
  if printf '%s\n' "$monitoring_services" | grep -qx "prometheus"; then
    pass "prometheus is running (monitoring profile)"
    if command_exists python3 && python3 scripts/check-monitoring.py; then
      pass "monitoring collection and configuration checks passed"
    else
      fail "monitoring checks failed; check scrapes, receiver configuration, and python3 availability"
    fi
  else
    warn "monitoring profile is not running; alert rules in ops/prometheus/alerts.yml are not being evaluated"
  fi

  if postgres_version_num="$(compose exec -T postgres sh -ceu 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SHOW server_version_num"')"; then
    if postgres_version_result="$(scripts/check-postgres-major.sh "$postgres_version_num" 2>&1)"; then
      pass "$postgres_version_result"
    else
      fail "$postgres_version_result"
    fi
  else
    fail "could not read PostgreSQL server_version_num"
  fi

  if compose exec -T postgres sh -ceu '
    preload="$(psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT current_setting('\''shared_preload_libraries'\'', true)")"
    for lib in pg_stat_statements; do
      case ",${preload}," in
        *",${lib},"*) ;;
        *) echo "missing preload library ${lib}" >&2; exit 1 ;;
      esac
    done
    psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT extname FROM pg_extension WHERE extname IN ('\''vector'\'','\''pg_trgm'\'','\''pg_stat_statements'\'','\''pg_buffercache'\'','\''amcheck'\'') ORDER BY extname" |
      awk '\''BEGIN { expected["vector"]; expected["pg_trgm"]; expected["pg_stat_statements"]; expected["pg_buffercache"]; expected["amcheck"] } { found[$1]=1 } END { for (name in expected) if (!found[name]) { print "missing extension " name > "/dev/stderr"; missing=1 } exit missing }'\''
  ' >/dev/null; then
    pass "required Postgres extensions are installed and preload libraries are configured"
  else
    fail "required Postgres extension check failed"
  fi

  running_services="$(compose ps --services --status running 2>/dev/null || true)"
  if printf '%s\n' "$running_services" | grep -qx "db-ops"; then
    if compose exec -T db-ops docker version >/dev/null 2>&1; then
      pass "db-ops can reach the Docker socket"
    else
      fail "db-ops cannot reach the Docker socket; check HANK_DB_OPS_DOCKER_GID"
    fi
  else
    fail "db-ops is not running"
  fi

  if [ -f ".env.agent" ]; then
    agent_running_services="$(docker compose --env-file "$env_file" --profile agent ps --services --status running 2>/dev/null || true)"
    if printf '%s\n' "$agent_running_services" | grep -qx "agent"; then
      pass "agent is running"
    else
      fail ".env.agent exists but the agent service is not running"
    fi
  fi
fi

printf '\n[doctor] complete: %d failure(s), %d warning(s)\n' "$failures" "$warnings"
if [ "$failures" -ne 0 ]; then
  exit 1
fi
