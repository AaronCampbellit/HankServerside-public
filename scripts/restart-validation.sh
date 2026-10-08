#!/usr/bin/env bash
set -euo pipefail

out_dir="${HANK_RESTART_REPORT_DIR:-data/restart-validation}"
mkdir -p "$out_dir"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
log="$out_dir/restart-validation-$stamp.log"

if [[ -z "${HANK_TEST_DATABASE_URL:-}" ]] && command -v docker >/dev/null 2>&1 && [[ -f "${HANK_CLOUD_ENV_FILE:-.env.cloud}" ]]; then
  env_file="${HANK_CLOUD_ENV_FILE:-.env.cloud}"
  project="${COMPOSE_PROJECT_NAME:-hankserverside}"
  if docker compose --env-file "$env_file" -p "$project" ps --services --status running 2>/dev/null | grep -qx postgres; then
    pg_env="$(docker compose --env-file "$env_file" -p "$project" exec -T postgres sh -ceu 'printf "%s\n%s\n%s\n" "$POSTGRES_USER" "${POSTGRES_PASSWORD:-}" "$POSTGRES_DB"')"
    pg_user="$(printf '%s\n' "$pg_env" | sed -n '1p')"
    pg_password="$(printf '%s\n' "$pg_env" | sed -n '2p')"
    pg_db="$(printf '%s\n' "$pg_env" | sed -n '3p')"
    pg_container="$(docker compose --env-file "$env_file" -p "$project" ps -q postgres)"
    pg_host="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$pg_container")"
    if [[ -n "$pg_password" ]]; then
      export HANK_TEST_DATABASE_URL="postgres://${pg_user}:${pg_password}@${pg_host}:5432/${pg_db}?sslmode=disable"
    else
      export HANK_TEST_DATABASE_URL="postgres://${pg_user}@${pg_host}:5432/${pg_db}?sslmode=disable"
    fi
  fi
fi

go test -v ./internal/cloud -run 'TestRestartRecoveryPersistsTicketsTransfersAndLoginBackoff|TestAppCommandTimeoutReturnsError' | tee "$log"

echo "$log"
