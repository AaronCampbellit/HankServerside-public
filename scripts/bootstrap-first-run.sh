#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [ -f "$repo_root/.env.cloud" ] && {
  grep -q '^HANK_' "$repo_root/.env.cloud" ||
  { [ -f "$repo_root/.env.agent" ] && grep -q '^HANK_' "$repo_root/.env.agent"; } ||
  [ "$(awk -F= '$1 == "POSTGRES_DB" { print $2; exit }' "$repo_root/.env.cloud")" = "hank" ] ||
  [ "$repo_root" = "${HANK_NAMING_OLD_ROOT:-/srv/hank}/HankServerside" ];
}; then
  "$repo_root/scripts/migrate-hank-naming.sh" --apply
  repo_root="${HANK_NAMING_NEW_ROOT:-/srv/hank}/HankServerside"
fi
cd "$repo_root"

env_file=".env.cloud"

log() {
  printf '[bootstrap] %s\n' "$*"
}

fail() {
  printf '[bootstrap] ERROR: %s\n' "$*" >&2
  exit 1
}

truthy() {
  case "${1:-}" in
    1|true|TRUE|yes|YES|y|Y) return 0 ;;
    *) return 1 ;;
  esac
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

prompt_default() {
  local var_name="$1"
  local prompt="$2"
  local default_value="$3"
  local current_value="${!var_name:-}"
  local answer=""

  if [ -n "$current_value" ]; then
    printf -v "$var_name" '%s' "$current_value"
    return
  fi

  if truthy "${HANK_BOOTSTRAP_NONINTERACTIVE:-}" || [ ! -t 0 ]; then
    printf -v "$var_name" '%s' "$default_value"
    return
  fi

  read -r -p "$prompt [$default_value]: " answer
  printf -v "$var_name" '%s' "${answer:-$default_value}"
}

random_hex() {
  local bytes="$1"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
    return
  fi
  od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
  printf '\n'
}

detect_docker_gid() {
  if [ ! -S /var/run/docker.sock ]; then
    printf ''
    return
  fi
  if stat -c '%g' /var/run/docker.sock >/dev/null 2>&1; then
    stat -c '%g' /var/run/docker.sock
    return
  fi
  if stat -f '%g' /var/run/docker.sock >/dev/null 2>&1; then
    stat -f '%g' /var/run/docker.sock
    return
  fi
  printf ''
}

compose() {
  docker compose --env-file "$env_file" "$@"
}

env_value() {
  local key="$1"
  local default_value="$2"
  local value
  value="$(awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      gsub(/^"/, "")
      gsub(/"$/, "")
      print
      exit
    }
  ' "$env_file")"
  printf '%s' "${value:-$default_value}"
}

check_web_push_configuration() {
  local configured=0
  local key
  for key in \
    HANK_WEB_PUSH_VAPID_PUBLIC_KEY \
    HANK_WEB_PUSH_VAPID_PRIVATE_KEY \
    HANK_WEB_PUSH_VAPID_SUBJECT
  do
    [ -n "$(env_value "$key" "")" ] && configured=$((configured + 1))
  done

  case "$configured" in
    0)
      web_push_state="missing"
      web_push_subject="$(env_value HANK_PUBLIC_BASE_URL "")"
      case "$web_push_subject" in
        https://[!/?#]*) ;;
        *) fail "HANK_PUBLIC_BASE_URL must be a public HTTPS URL so onboarding can configure Web Push" ;;
      esac
      case "$web_push_subject" in
        *[[:space:]#]*) fail "HANK_PUBLIC_BASE_URL contains characters that are unsafe in .env.cloud" ;;
      esac
      ;;
    3)
      web_push_state="configured"
      web_push_subject=""
      ;;
    *)
      fail "Web Push requires public key, private key, and subject together; repair or remove the partial VAPID configuration"
      ;;
  esac
}

configure_web_push() {
  [ "$web_push_state" = "missing" ] || {
    log "preserving existing Web Push configuration"
    return
  }

  local generated public_key private_key tmp_file
  generated="$(compose run -T --rm --no-deps --entrypoint /usr/local/bin/hank-server cloud web-push-keys)" || fail "could not generate Web Push VAPID keys"
  public_key="$(printf '%s\n' "$generated" | awk -F= '$1 == "HANK_WEB_PUSH_VAPID_PUBLIC_KEY" { sub(/^[^=]*=/, ""); print; exit }')"
  private_key="$(printf '%s\n' "$generated" | awk -F= '$1 == "HANK_WEB_PUSH_VAPID_PRIVATE_KEY" { sub(/^[^=]*=/, ""); print; exit }')"
  if [[ ! "$public_key" =~ ^[A-Za-z0-9_-]+$ ]] || [[ ! "$private_key" =~ ^[A-Za-z0-9_-]+$ ]]; then
    fail "Web Push key generator returned malformed output"
  fi

  umask 077
  tmp_file="$(mktemp "${env_file}.tmp.XXXXXX")"
  if ! awk -F= '
       $1 != "HANK_WEB_PUSH_VAPID_PUBLIC_KEY" &&
       $1 != "HANK_WEB_PUSH_VAPID_PRIVATE_KEY" &&
       $1 != "HANK_WEB_PUSH_VAPID_SUBJECT" { print }
     ' "$env_file" >"$tmp_file" ||
     ! printf '\nHANK_WEB_PUSH_VAPID_PUBLIC_KEY=%s\nHANK_WEB_PUSH_VAPID_PRIVATE_KEY=%s\nHANK_WEB_PUSH_VAPID_SUBJECT=%s\n' \
       "$public_key" "$private_key" "$web_push_subject" >>"$tmp_file" ||
     ! chmod 600 "$tmp_file" ||
     ! mv "$tmp_file" "$env_file"; then
    rm -f "$tmp_file"
    fail "could not save Web Push configuration"
  fi
  log "configured Web Push delivery"
}

wait_for_postgres() {
  local attempt
  for attempt in $(seq 1 60); do
    if compose exec -T postgres pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; then
      return
    fi
    sleep 2
  done
  fail "postgres did not become ready"
}

wait_for_http() {
  local path="$1"
  local label="$2"
  local url="http://127.0.0.1:${HANK_CLOUD_HOST_PORT}${path}"
  local attempt
  for attempt in $(seq 1 60); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      log "$label is ready at $url"
      return
    fi
    sleep 2
  done
  fail "$label did not become ready at $url"
}

write_cloud_env() {
  local db_password db_ops_secret repo_cipher secret_key docker_gid

  prompt_default HANK_BOOTSTRAP_HOST_BIND "Cloud host bind" "127.0.0.1"
  prompt_default HANK_BOOTSTRAP_HOST_PORT "Cloud host port" "18080"
  prompt_default HANK_BOOTSTRAP_POSTGRES_DB "Postgres database name" "hank"
  prompt_default HANK_BOOTSTRAP_POSTGRES_USER "Postgres username" "hank"
  prompt_default HANK_BOOTSTRAP_PUBLIC_BASE_URL "Public Hank HTTPS URL" ""

  db_password="${HANK_BOOTSTRAP_POSTGRES_PASSWORD:-$(random_hex 24)}"
  secret_key="${HANK_BOOTSTRAP_SECRET_ENCRYPTION_KEY:-$(random_hex 32)}"
  db_ops_secret="${HANK_BOOTSTRAP_DB_OPS_INTENT_SECRET:-$(random_hex 32)}"
  repo_cipher="${HANK_BOOTSTRAP_DB_OPS_REPO_CIPHER_PASS:-$(random_hex 32)}"
  docker_gid="${HANK_DB_OPS_DOCKER_GID:-$(detect_docker_gid)}"

  umask 077
  local tmp_file
  tmp_file="$(mktemp "${env_file}.tmp.XXXXXX")"
  cat >"$tmp_file" <<EOF
HANK_CLOUD_ADDR=:8080
HANK_CLOUD_HOST_BIND=${HANK_BOOTSTRAP_HOST_BIND}
HANK_CLOUD_HOST_PORT=${HANK_BOOTSTRAP_HOST_PORT}

POSTGRES_DB=${HANK_BOOTSTRAP_POSTGRES_DB}
POSTGRES_USER=${HANK_BOOTSTRAP_POSTGRES_USER}
POSTGRES_PASSWORD=${db_password}
HANK_CLOUD_DATABASE_URL=postgres://${HANK_BOOTSTRAP_POSTGRES_USER}:${db_password}@postgres:5432/${HANK_BOOTSTRAP_POSTGRES_DB}?sslmode=disable

HANK_SESSION_TTL_SECONDS=604800
HANK_REQUEST_TIMEOUT_SECONDS=120
HANK_SECRET_ENCRYPTION_KEY=${secret_key}
HANK_PUBLIC_BASE_URL=${HANK_BOOTSTRAP_PUBLIC_BASE_URL}

HANK_AI_PROVIDER=auto
HANK_ASSISTANT_EXECUTION_ENABLED=false
HANK_OLLAMA_BASE_URL=
HANK_OLLAMA_CHAT_MODEL=llama3.1
HANK_OLLAMA_EMBEDDING_MODEL=nomic-embed-text
HANK_PROJECT_DOCS_DIR=/app

HANK_OPENAI_API_KEY=
HANK_OPENAI_CHAT_MODEL=gpt-4o-mini
HANK_OPENAI_EMBEDDING_MODEL=text-embedding-3-small

HANK_CHATGPT_OAUTH_ENABLED=false
HANK_CHATGPT_AUTH_ISSUER=https://auth.openai.com
HANK_CHATGPT_BACKEND_BASE_URL=https://chatgpt.com/backend-api/codex
HANK_CHATGPT_CLIENT_ID=app_EMoamEEZ73f0CkXaXp7hrann
HANK_CHATGPT_CHAT_MODEL=gpt-5.4-mini

HANK_APNS_TEAM_ID=
HANK_APNS_KEY_ID=
HANK_APNS_PRIVATE_KEY=
HANK_APNS_TOPIC=com.dropfile.Hank
HANK_APNS_ENVIRONMENT=sandbox

HANK_DB_OPS_STATE_DIR=/var/lib/hank/db-ops/state
HANK_DB_OPS_LOG_DIR=/var/log/hank/db-ops
HANK_DB_OPS_INTENT_SECRET=${db_ops_secret}
HANK_DB_OPS_REPO_CIPHER_PASS=${repo_cipher}
HANK_DB_OPS_STANZA=hank
HANK_DB_OPS_PGDATA=/var/lib/postgresql/data
HANK_DB_OPS_RESTORE_PGDATA=/var/lib/postgresql/restore
HANK_DB_OPS_RESTORE_DATABASE_URL=postgres://${HANK_BOOTSTRAP_POSTGRES_USER}:${db_password}@postgres-restore:5432/${HANK_BOOTSTRAP_POSTGRES_DB}?sslmode=disable
HANK_DB_OPS_COMPOSE_FILE=/workspace/docker-compose.yml
HANK_DB_OPS_DOCKER_GID=${docker_gid}
EOF

  mv "$tmp_file" "$env_file"
  chmod 600 "$env_file"
  log "created $env_file"
}

if [ -n "${HANK_CLOUD_ENV_FILE:-}" ] && [ "${HANK_CLOUD_ENV_FILE}" != "$env_file" ] && [ "${HANK_CLOUD_ENV_FILE}" != "./$env_file" ]; then
  fail "first-run bootstrap expects repo-root .env.cloud; unset HANK_CLOUD_ENV_FILE for this flow"
fi

require_command docker
require_command curl

docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required"

if [ -e "$env_file" ]; then
  if truthy "${HANK_BOOTSTRAP_FORCE:-}"; then
    backup="${env_file}.$(date +%Y%m%d%H%M%S).bak"
    cp "$env_file" "$backup"
    chmod 600 "$backup"
    log "backed up existing $env_file to $backup"
    write_cloud_env
  else
    log "using existing $env_file"
  fi
else
  write_cloud_env
fi

chmod 600 "$env_file"

web_push_state=""
web_push_subject=""
check_web_push_configuration

POSTGRES_USER="$(env_value POSTGRES_USER hank)"
POSTGRES_DB="$(env_value POSTGRES_DB hank)"
HANK_CLOUD_HOST_PORT="$(env_value HANK_CLOUD_HOST_PORT 18080)"

log "building first-boot images"
if command -v git >/dev/null 2>&1 && git rev-parse --git-dir >/dev/null 2>&1; then
  HANK_BUILD_VERSION="${HANK_BUILD_VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
  HANK_SOURCE_COMMIT="${HANK_SOURCE_COMMIT:-$(git rev-parse HEAD 2>/dev/null || echo unknown)}"
  export HANK_BUILD_VERSION HANK_SOURCE_COMMIT
fi
compose build postgres cloud db-ops
configure_web_push

log "starting postgres"
compose up -d postgres
wait_for_postgres

log "running schema migrations"
compose run --rm cloud /usr/local/bin/hank-server migrate up
compose run --rm cloud /usr/local/bin/hank-server migrate status --strict >/dev/null

log "starting cloud and db-ops"
compose up -d cloud db-ops
wait_for_http "/healthz" "health check"
wait_for_http "/readyz" "readiness check"

log "first boot is complete"
printf '\nNext steps:\n'
printf '  1. Open the public URL or http://127.0.0.1:%s and register the first admin.\n' "$HANK_CLOUD_HOST_PORT"
printf '  2. Create the Hank Agent setup file in the dashboard and save it as .env.agent with chmod 600.\n'
printf '  3. Start the Hank Agent with: docker compose --env-file .env.cloud --profile agent up -d agent\n'
printf '  4. Run: scripts/doctor.sh\n'
