#!/usr/bin/env bash
set -Eeuo pipefail

repo_name="${HANK_NAMING_REPO_NAME:-HankServerside}"
source_repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
old_install_root="${HANK_NAMING_OLD_ROOT:-/srv/hank-remote}"
new_install_root="${HANK_NAMING_NEW_ROOT:-/srv/hank}"
if [ -z "${HANK_NAMING_OLD_ROOT:-}" ] && [ -z "${HANK_NAMING_NEW_ROOT:-}" ] && \
  [ "$(basename "$source_repo")" = "$repo_name" ] && [ "$(basename "$(dirname "$source_repo")")" = "hank-remote" ]; then
  old_install_root="$(dirname "$source_repo")"
  new_install_root="$(dirname "$old_install_root")/hank"
fi
old_repo="$old_install_root/$repo_name"
new_repo="$new_install_root/$repo_name"
original_source_repo="$source_repo"
active_repo="$source_repo"
mode="${1:---check}"

legacy_database="hankremote"
canonical_database="hank"
legacy_role="hankremote"
canonical_role="hank"

temp_root=""
cloud_backup=""
agent_backup=""
timestamp_cloud_backup=""
timestamp_agent_backup=""
database_renamed=false
role_renamed=false
path_moved=false
path_move_mode=""
migration_role_created=false
migration_role="hank_naming_migrator_$$"
rollback_active=false

log() {
  printf '[naming-migration] %s\n' "$*"
}

fail() {
  printf '[naming-migration] ERROR: %s\n' "$*" >&2
  return 1
}

truthy() {
  case "${1:-}" in
    1|true|TRUE|yes|YES|y|Y) return 0 ;;
    *) return 1 ;;
  esac
}

env_value() {
  local file="$1"
  local key="$2"
  local default_value="${3:-}"
  local value
  value="$(awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      gsub(/^"/, "")
      gsub(/"$/, "")
      print
      exit
    }
  ' "$file")"
  printf '%s' "${value:-$default_value}"
}

rewrite_env() {
  local source_file="$1"
  local target_file="$2"
  local rename_database_values="$3"
  awk -F= -v rename_database_values="$rename_database_values" -v source_repo="$source_repo" -v new_repo="$new_repo" -v old_install_root="$old_install_root" -v new_install_root="$new_install_root" '
    function replace_literal(value, old, replacement, position) {
      if (old == "" || old == replacement) {
        return value
      }
      while ((position = index(value, old)) > 0) {
        value = substr(value, 1, position - 1) replacement substr(value, position + length(old))
      }
      return value
    }
    function rewrite_value(key, value) {
      if (key ~ /(_DIR|_PATH|_ROOT|_ROOTS_JSON|_HOST_DIR|_STATE_DIR|_LOG_DIR|_PGDATA|_COMPOSE_FILE)$/) {
		value = replace_literal(value, "/srv/hank-remote", "/srv/hank")
		value = replace_literal(value, old_install_root, new_install_root)
        value = replace_literal(value, source_repo, new_repo)
      }
      if (key ~ /(_COMMAND|_BINARY|_EXECUTABLE)$/) {
        gsub(/hank-remote-cloud/, "hank-server", value)
        gsub(/hank-remote-agent/, "hank-agent", value)
      }
      if (rename_database_values == "true") {
        if (key == "POSTGRES_DB" && value == "hankremote") {
          value = "hank"
        } else if (key == "POSTGRES_USER" && value == "hankremote") {
          value = "hank"
        } else if (key ~ /_DATABASE_URL$/) {
          sub(/:\/\/hankremote:/, "://hank:", value)
          sub(/\/hankremote\?/, "/hank?", value)
          sub(/\/hankremote$/, "/hank", value)
        }
      }
      return value
    }
    /^[A-Za-z_][A-Za-z0-9_]*=/ {
      key = $1
      sub(/^HANK_REMOTE_/, "HANK_", key)
      value = $0
      sub(/^[^=]*=/, "", value)
      value = rewrite_value(key, value)
      if (seen[key]++) {
        printf "duplicate environment key after migration: %s\n", key > "/dev/stderr"
        exit 42
      }
      print key "=" value
      next
    }
    {
      line = $0
      gsub(/HANK_REMOTE_/, "HANK_", line)
      print line
    }
  ' "$source_file" >"$target_file"
  chmod 600 "$target_file"
}

compose() {
  docker compose --project-directory "$active_repo" --env-file "$temp_root/cloud.database-old.env" "$@"
}

postgres_scalar() {
  local role="$1"
  local sql="$2"
  compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U "$role" -d postgres -Atc "$sql"
}

postgres_exec() {
  local role="$1"
  local sql="$2"
  compose exec -T postgres psql -X -v ON_ERROR_STOP=1 -U "$role" -d postgres -c "$sql" >/dev/null
}

wait_for_postgres() {
  local role="$1"
  local database="$2"
  local attempt
  for attempt in $(seq 1 60); do
    if compose exec -T postgres pg_isready -U "$role" -d "$database" >/dev/null 2>&1; then
      return
    fi
    sleep 2
  done
  fail "PostgreSQL did not become ready for the naming migration"
}

validate_running_compose_origin() {
  local container_id="$1"
  local working_dir config_files file resolved
  local -a files
  working_dir="$(docker inspect "$container_id" --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}')"
  if [ -z "$working_dir" ] || [ ! -d "$working_dir" ] || [ "$(cd "$working_dir" && pwd -P)" != "$source_repo" ]; then
    fail "running PostgreSQL belongs to Compose working directory ${working_dir:-unknown}, not $source_repo"
  fi
  config_files="$(docker inspect "$container_id" --format '{{index .Config.Labels "com.docker.compose.project.config_files"}}')"
  [ -n "$config_files" ] || fail "running PostgreSQL does not expose its Compose configuration provenance"
  IFS=',' read -r -a files <<<"$config_files"
  for file in "${files[@]}"; do
    [ -f "$file" ] || fail "running PostgreSQL references missing Compose file $file"
    resolved="$(cd "$(dirname "$file")" && pwd -P)/$(basename "$file")"
    case "$resolved" in
      "$source_repo"/*) ;;
      *) fail "running PostgreSQL uses Compose file $resolved outside $source_repo" ;;
    esac
  done
}

move_install_root() {
  local source="$1"
  local target="$2"
  if [ -w "$(dirname "$source")" ]; then
    mv "$source" "$target"
    return
  fi
  command -v sudo >/dev/null 2>&1 || fail "moving $source requires write access to $(dirname "$source") or sudo"
  sudo mv "$source" "$target"
}

create_install_root() {
  local target="$1"
  [ ! -e "$target" ] || fail "$target already exists"
  if [ -w "$(dirname "$target")" ]; then
    mkdir -p "$target"
    return
  fi
  command -v sudo >/dev/null 2>&1 || fail "creating $target requires write access to $(dirname "$target") or sudo"
  sudo install -d -m 0755 -o "$(id -u)" -g "$(id -g)" "$target"
}

move_repository() {
  local source="$1"
  local target="$2"
  if [ -w "$(dirname "$source")" ] && [ -w "$(dirname "$target")" ]; then
    mv "$source" "$target"
    return
  fi
  command -v sudo >/dev/null 2>&1 || fail "moving $source requires write access to both repository parents or sudo"
  sudo mv "$source" "$target"
}

remove_empty_install_root() {
  local target="$1"
  if [ -w "$(dirname "$target")" ]; then
    rmdir "$target"
    return
  fi
  command -v sudo >/dev/null 2>&1 || return 1
  sudo rmdir "$target"
}

restore_file() {
  local backup="$1"
  local destination="$2"
  [ -n "$backup" ] && [ -f "$backup" ] || return 0
  cp "$backup" "$destination"
  chmod 600 "$destination"
}

rollback() {
  local exit_code="$1"
  [ "$rollback_active" = false ] || exit "$exit_code"
  rollback_active=true
  set +e
  log "migration failed; restoring the prior naming state"

  if [ "$path_moved" = true ]; then
    if [ "$path_move_mode" = "root" ] && [ -d "$new_install_root" ] && [ ! -e "$old_install_root" ]; then
      move_install_root "$new_install_root" "$old_install_root"
      active_repo="$old_repo"
      path_moved=false
    elif [ "$path_move_mode" = "repository" ] && [ -d "$new_repo" ] && [ ! -e "$original_source_repo" ]; then
      move_repository "$new_repo" "$original_source_repo"
      remove_empty_install_root "$new_install_root"
      active_repo="$original_source_repo"
      path_moved=false
    fi
  fi

  restore_file "$cloud_backup" "$active_repo/.env.cloud"
  if [ -n "$agent_backup" ]; then
    restore_file "$agent_backup" "$active_repo/.env.agent"
  fi

  if [ "$role_renamed" = true ]; then
    postgres_exec "$migration_role" "ALTER ROLE $canonical_role RENAME TO $legacy_role;"
    role_renamed=false
  fi
  if [ "$database_renamed" = true ]; then
    postgres_exec "$migration_role" "ALTER DATABASE $canonical_database RENAME TO $legacy_database;"
    database_renamed=false
  fi
  if [ "$migration_role_created" = true ]; then
    postgres_exec "$legacy_role" "DROP ROLE IF EXISTS $migration_role;"
    migration_role_created=false
  fi

  printf '[naming-migration] ERROR: migration rolled back; review the preceding error\n' >&2
  exit "$exit_code"
}

legacy_env_present=false
for candidate in "$source_repo/.env.cloud" "$source_repo/.env.agent"; do
  if [ -f "$candidate" ] && grep -q '^HANK_REMOTE_' "$candidate"; then
    legacy_env_present=true
  fi
done

legacy_path_present=false
if [ "$source_repo" = "$old_repo" ]; then
  legacy_path_present=true
  path_move_mode="root"
elif [ "$source_repo" != "$new_repo" ] && truthy "${HANK_NAMING_MOVE_REPOSITORY:-}"; then
  legacy_path_present=true
  path_move_mode="repository"
fi

legacy_database_configured=false
if [ -f "$source_repo/.env.cloud" ] && {
  [ "$(env_value "$source_repo/.env.cloud" POSTGRES_DB "")" = "$legacy_database" ] ||
  [ "$(env_value "$source_repo/.env.cloud" POSTGRES_USER "")" = "$legacy_role" ];
}; then
  legacy_database_configured=true
fi

if [ "$legacy_env_present" = false ] && [ "$legacy_path_present" = false ] && [ "$legacy_database_configured" = false ]; then
  log "installation already uses the canonical Hank naming scheme"
  exit 0
fi

if [ "$mode" = "--check" ]; then
  log "legacy naming detected; run scripts/migrate-hank-naming.sh --apply"
  exit 0
fi
if [ "$mode" != "--apply" ]; then
  fail "usage: scripts/migrate-hank-naming.sh [--check|--apply]"
  exit 2
fi

if [ -e "$old_install_root" ] && [ -e "$new_install_root" ]; then
  fail "both $old_install_root and $new_install_root exist; refusing to merge or delete either path"
  exit 1
fi
if [ "$legacy_path_present" = true ] && [ -e "$new_install_root" ]; then
  fail "$new_install_root already exists; refusing to merge or delete the source or target path"
  exit 1
fi
if [ ! -f "$source_repo/.env.cloud" ]; then
  fail "$source_repo/.env.cloud is required for an existing-installation migration"
  exit 1
fi

umask 077
temp_root="$(mktemp -d)"
trap 'rollback $?' ERR INT TERM
trap 'rm -rf "$temp_root"' EXIT

cloud_backup="$temp_root/cloud.original.env"
cp "$source_repo/.env.cloud" "$cloud_backup"
rewrite_env "$source_repo/.env.cloud" "$temp_root/cloud.database-old.env" false
rewrite_env "$source_repo/.env.cloud" "$temp_root/cloud.final.env" true

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
timestamp_cloud_backup="$source_repo/.env.cloud.pre-hank-naming-$timestamp-$$.bak"
cp "$cloud_backup" "$timestamp_cloud_backup"
chmod 600 "$timestamp_cloud_backup"

if [ -f "$source_repo/.env.agent" ]; then
  agent_backup="$temp_root/agent.original.env"
  cp "$source_repo/.env.agent" "$agent_backup"
  rewrite_env "$source_repo/.env.agent" "$temp_root/agent.final.env" true
  timestamp_agent_backup="$source_repo/.env.agent.pre-hank-naming-$timestamp-$$.bak"
  cp "$agent_backup" "$timestamp_agent_backup"
  chmod 600 "$timestamp_agent_backup"
fi

if truthy "${HANK_NAMING_TEST_SKIP_DATABASE:-}"; then
  truthy "${HANK_NAMING_TEST_MODE:-}" || fail "HANK_NAMING_TEST_SKIP_DATABASE is available only in test mode"
else
  command -v docker >/dev/null 2>&1 || fail "docker is required to rename the existing PostgreSQL database and role"
  docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required"

  old_db="$(env_value "$source_repo/.env.cloud" POSTGRES_DB "$legacy_database")"
  old_role="$(env_value "$source_repo/.env.cloud" POSTGRES_USER "$legacy_role")"
  [ "$old_db" = "$legacy_database" ] || fail "expected POSTGRES_DB=$legacy_database before migration, found $old_db"
  [ "$old_role" = "$legacy_role" ] || fail "expected POSTGRES_USER=$legacy_role before migration, found $old_role"

  postgres_container="$(compose ps -q --status running postgres)" || fail "could not inspect the running PostgreSQL Compose service"
  if [ -n "$postgres_container" ]; then
    validate_running_compose_origin "$postgres_container"
  else
    compose up -d postgres
  fi
  wait_for_postgres "$legacy_role" "$legacy_database"

  "$source_repo/scripts/production-naming-preflight.sh" \
    --project-directory "$source_repo" \
    --env-file "$temp_root/cloud.database-old.env" \
    --database "$old_db" \
    --role "$old_role"

  old_db_exists="$(postgres_scalar "$legacy_role" "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$legacy_database');")"
  new_db_exists="$(postgres_scalar "$legacy_role" "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = '$canonical_database');")"
  old_role_exists="$(postgres_scalar "$legacy_role" "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$legacy_role');")"
  new_role_exists="$(postgres_scalar "$legacy_role" "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$canonical_role');")"

  if [ "$old_db_exists" = "t" ] && [ "$new_db_exists" = "t" ]; then
    fail "both $legacy_database and $canonical_database databases exist"
  fi
  if [ "$old_role_exists" = "t" ] && [ "$new_role_exists" = "t" ]; then
    fail "both $legacy_role and $canonical_role PostgreSQL roles exist"
  fi
  if [ "$old_db_exists" != "t" ] || [ "$new_db_exists" != "f" ] || [ "$old_role_exists" != "t" ] || [ "$new_role_exists" != "f" ]; then
    fail "PostgreSQL naming state is partial or unexpected; no database changes were made"
  fi

  compose --profile agent stop agent cloud db-ops >/dev/null 2>&1 || true
  postgres_exec "$legacy_role" "CREATE ROLE $migration_role WITH SUPERUSER LOGIN;"
  migration_role_created=true
  postgres_exec "$migration_role" "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$legacy_database' AND pid <> pg_backend_pid();"
  postgres_exec "$migration_role" "ALTER DATABASE $legacy_database RENAME TO $canonical_database;"
  database_renamed=true
  postgres_exec "$migration_role" "ALTER ROLE $legacy_role RENAME TO $canonical_role;"
  role_renamed=true
  if truthy "${HANK_NAMING_TEST_FAIL_AFTER_DATABASE:-}"; then
    truthy "${HANK_NAMING_TEST_MODE:-}" || fail "HANK_NAMING_TEST_FAIL_AFTER_DATABASE is available only in test mode"
    fail "injected failure after PostgreSQL rename"
  fi
fi

cp "$temp_root/cloud.final.env" "$source_repo/.env.cloud"
chmod 600 "$source_repo/.env.cloud"
if [ -n "$agent_backup" ]; then
  cp "$temp_root/agent.final.env" "$source_repo/.env.agent"
  chmod 600 "$source_repo/.env.agent"
fi

if [ "$legacy_path_present" = true ]; then
  if [ "$path_move_mode" = "root" ]; then
    move_install_root "$old_install_root" "$new_install_root"
  else
    create_install_root "$new_install_root"
    move_repository "$source_repo" "$new_repo"
  fi
  active_repo="$new_repo"
  path_moved=true
fi

if [ "$role_renamed" = true ]; then
  postgres_exec "$canonical_role" "DROP ROLE $migration_role;"
  migration_role_created=false
fi

trap - ERR INT TERM
log "hard naming migration completed"
log "canonical repository path: $active_repo"
log "environment backups: ${timestamp_cloud_backup/#$source_repo/$active_repo}${timestamp_agent_backup:+ and ${timestamp_agent_backup/#$source_repo/$active_repo}}"
