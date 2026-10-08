#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
migration_script="$repo_root/scripts/migrate-hank-naming.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

new_fixture() {
  local fixture_root="$1"
  mkdir -p "$fixture_root/old/HankServerside/scripts"
  cp "$migration_script" "$fixture_root/old/HankServerside/scripts/migrate-hank-naming.sh"
  cp "$repo_root/scripts/production-naming-preflight.sh" "$fixture_root/old/HankServerside/scripts/production-naming-preflight.sh"
  cp "$repo_root/scripts/compose-storage-preflight.py" "$fixture_root/old/HankServerside/scripts/compose-storage-preflight.py"
  chmod +x "$fixture_root/old/HankServerside/scripts/migrate-hank-naming.sh" \
    "$fixture_root/old/HankServerside/scripts/production-naming-preflight.sh" \
    "$fixture_root/old/HankServerside/scripts/compose-storage-preflight.py"
  : >"$fixture_root/old/HankServerside/docker-compose.yml"
  cat >"$fixture_root/old/HankServerside/.env.cloud" <<'EOF'
HANK_REMOTE_CLOUD_HOST_BIND=127.0.0.1
HANK_REMOTE_CLOUD_HOST_PORT=18080
HANK_REMOTE_CLOUD_DATABASE_URL=postgres://hankremote:secret-hankremote-value@postgres:5432/hankremote?sslmode=disable
POSTGRES_DB=hankremote
POSTGRES_USER=hankremote
POSTGRES_PASSWORD=secret-hankremote-value
HANK_SECRET_ENCRYPTION_KEY=keep-hankremote-secret
EOF
  cat >"$fixture_root/old/HankServerside/.env.agent" <<'EOF'
HANK_REMOTE_AGENT_CLOUD_URL=ws://cloud:8080/ws/agent
HANK_REMOTE_AGENT_ID=home-main
HANK_REMOTE_AGENT_FILES_ROOT=/srv/hank-remote/files
EOF
  chmod 600 "$fixture_root/old/HankServerside/.env.cloud" "$fixture_root/old/HankServerside/.env.agent"
}

run_migration() {
  local fixture_root="$1"
  shift
  env \
    HANK_NAMING_OLD_ROOT="$fixture_root/old" \
    HANK_NAMING_NEW_ROOT="$fixture_root/new" \
    HANK_NAMING_TEST_MODE=true \
    HANK_NAMING_TEST_SKIP_DATABASE=true \
    "$@" \
    "$fixture_root/old/HankServerside/scripts/migrate-hank-naming.sh" --apply
}

run_database_migration() {
  local fixture_root="$1"
  local project_name="$2"
  env \
    COMPOSE_PROJECT_NAME="$project_name" \
    HANK_NAMING_OLD_ROOT="$fixture_root/old" \
    HANK_NAMING_NEW_ROOT="$fixture_root/new" \
    "$fixture_root/old/HankServerside/scripts/migrate-hank-naming.sh" --apply
}

write_database_compose_fixture() {
  local fixture_root="$1"
  cat >"$fixture_root/old/HankServerside/docker-compose.yml" <<'EOF'
services:
  postgres:
    image: postgres:18
    env_file:
      - .env.cloud
    environment:
      PGDATA: /var/lib/postgresql/data
    volumes:
      - postgres_data:/var/lib/postgresql
      - pgbackrest_repo:/var/lib/pgbackrest
  db-ops:
    image: postgres:18-alpine
    volumes:
      - postgres_data:/var/lib/postgresql
      - postgres_restore_data:/var/lib/postgresql/restore
      - pgbackrest_repo:/var/lib/pgbackrest
      - note_attachments:/var/lib/hank/note-attachments:ro
      - note_attachments_restore:/var/lib/hank/note-attachments-restore
  postgres-restore:
    image: postgres:18
    profiles: [restore]
    volumes:
      - postgres_restore_data:/var/lib/postgresql/restore
      - pgbackrest_repo:/var/lib/pgbackrest:ro
  cloud:
    image: busybox:1.37
    volumes:
      - note_attachments:/var/lib/hank/note-attachments
volumes:
  postgres_data:
  postgres_restore_data:
  pgbackrest_repo:
  note_attachments:
  note_attachments_restore:
EOF
}

wait_for_fixture_postgres() {
  local fixture_repo="$1"
  local project_name="$2"
  local attempt
  for attempt in $(seq 1 60); do
    if COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$fixture_repo" --env-file "$fixture_repo/.env.cloud" exec -T postgres \
      psql -X -v ON_ERROR_STOP=1 -U hankremote -d hankremote -Atc "SELECT 1" >/dev/null 2>&1
    then
      COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$fixture_repo" --env-file "$fixture_repo/.env.cloud" exec -T postgres \
        psql -X -v ON_ERROR_STOP=1 -U hankremote -d hankremote -c \
        "CREATE TABLE IF NOT EXISTS note_attachments (id TEXT PRIMARY KEY, storage_key TEXT NOT NULL, size_bytes BIGINT NOT NULL, preview_storage_key TEXT, preview_size_bytes BIGINT, status TEXT NOT NULL, deleted_at TIMESTAMPTZ)" \
        >/dev/null
      return
    fi
    sleep 1
  done
  fail "fixture PostgreSQL did not become ready"
}

test_hard_migration_rewrites_files_and_moves_installation() (
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' EXIT
  new_fixture "$fixture_root"

  run_migration "$fixture_root" >"$fixture_root/output.log" 2>&1

  [ ! -e "$fixture_root/old" ] || fail "legacy installation path still exists"
  [ -d "$fixture_root/new/HankServerside" ] || fail "canonical installation path was not created"
  [ ! -L "$fixture_root/old" ] || fail "hard migration created a compatibility symlink"

  local cloud_env="$fixture_root/new/HankServerside/.env.cloud"
  local agent_env="$fixture_root/new/HankServerside/.env.agent"
  grep -q '^HANK_CLOUD_HOST_BIND=127.0.0.1$' "$cloud_env" || fail "cloud key was not renamed"
  grep -q '^HANK_CLOUD_DATABASE_URL=postgres://hank:secret-hankremote-value@postgres:5432/hank?sslmode=disable$' "$cloud_env" || fail "database URL was not renamed safely"
  grep -q '^POSTGRES_DB=hank$' "$cloud_env" || fail "database name was not renamed"
  grep -q '^POSTGRES_USER=hank$' "$cloud_env" || fail "database role was not renamed"
  grep -q '^POSTGRES_PASSWORD=secret-hankremote-value$' "$cloud_env" || fail "database password was modified"
  grep -q '^HANK_SECRET_ENCRYPTION_KEY=keep-hankremote-secret$' "$cloud_env" || fail "application secret was modified"
  grep -q '^HANK_AGENT_FILES_ROOT=/srv/hank/files$' "$agent_env" || fail "agent path was not renamed"
  if grep -q 'HANK_REMOTE_\|/srv/hank-remote' "$cloud_env" "$agent_env"; then
    fail "legacy environment keys or paths remain after migration"
  fi
  [ "$(stat -c '%a' "$cloud_env")" = "600" ] || fail "cloud env mode changed"
  [ "$(stat -c '%a' "$agent_env")" = "600" ] || fail "agent env mode changed"
  compgen -G "$fixture_root/new/HankServerside/.env.cloud.pre-hank-naming-*.bak" >/dev/null || fail "cloud backup was not retained"
  compgen -G "$fixture_root/new/HankServerside/.env.agent.pre-hank-naming-*.bak" >/dev/null || fail "agent backup was not retained"
)

test_path_collision_fails_before_changes() (
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' EXIT
  new_fixture "$fixture_root"
  mkdir -p "$fixture_root/new"

  if run_migration "$fixture_root" >"$fixture_root/output.log" 2>&1; then
    fail "migration accepted conflicting old and new installation roots"
  fi
  [ -d "$fixture_root/old/HankServerside" ] || fail "legacy path moved despite collision"
  grep -q '^HANK_REMOTE_CLOUD_HOST_BIND=' "$fixture_root/old/HankServerside/.env.cloud" || fail "environment changed despite collision"
  grep -q 'refusing to merge or delete either path' "$fixture_root/output.log" || fail "collision error was not actionable"
)

test_duplicate_environment_keys_fail_without_moving_installation() (
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' EXIT
  new_fixture "$fixture_root"
  printf 'HANK_CLOUD_HOST_BIND=0.0.0.0\n' >>"$fixture_root/old/HankServerside/.env.cloud"

  if run_migration "$fixture_root" >"$fixture_root/output.log" 2>&1; then
    fail "migration accepted duplicate canonical environment keys"
  fi
  [ -d "$fixture_root/old/HankServerside" ] || fail "installation moved after environment conflict"
  grep -q '^HANK_REMOTE_CLOUD_HOST_BIND=' "$fixture_root/old/HankServerside/.env.cloud" || fail "environment changed after duplicate-key failure"
  grep -q 'duplicate environment key after migration' "$fixture_root/output.log" || fail "duplicate-key error was not actionable"
)

test_nonstandard_repository_move_preserves_siblings() (
  local fixture_root source_repo
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' EXIT
  new_fixture "$fixture_root"
  mkdir -p "$fixture_root/custom"
  mv "$fixture_root/old/HankServerside" "$fixture_root/custom/HankServerside"
  rmdir "$fixture_root/old"
  source_repo="$fixture_root/custom/HankServerside"
  printf 'preserve\n' >"$fixture_root/custom/sibling.txt"
  printf 'HANK_REMOTE_LINUX_AGENT_RELEASE_HOST_DIR=%s/data/linux-agent-releases/current\n' "$source_repo" >>"$source_repo/.env.cloud"

  env \
    HANK_NAMING_OLD_ROOT="$fixture_root/unused-old" \
    HANK_NAMING_NEW_ROOT="$fixture_root/new" \
    HANK_NAMING_MOVE_REPOSITORY=true \
    HANK_NAMING_TEST_MODE=true \
    HANK_NAMING_TEST_SKIP_DATABASE=true \
    "$source_repo/scripts/migrate-hank-naming.sh" --apply >"$fixture_root/output.log" 2>&1

  [ ! -e "$source_repo" ] || fail "nonstandard repository remained at its old path"
  [ -d "$fixture_root/new/HankServerside" ] || fail "nonstandard repository did not move to the canonical path"
  grep -q '^preserve$' "$fixture_root/custom/sibling.txt" || fail "repository-only move changed a sibling"
  grep -q '^HANK_CLOUD_HOST_BIND=' "$fixture_root/new/HankServerside/.env.cloud" || fail "nonstandard repository environment was not migrated"
  grep -q "^HANK_LINUX_AGENT_RELEASE_HOST_DIR=$fixture_root/new/HankServerside/data/linux-agent-releases/current$" "$fixture_root/new/HankServerside/.env.cloud" || fail "checkout-relative environment path was not moved"
)

test_nonstandard_hank_remote_root_is_detected_and_moved_as_a_unit() (
  local fixture_root legacy_root canonical_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' EXIT
  legacy_root="$fixture_root/Dockerapps/hank-remote"
  canonical_root="$fixture_root/Dockerapps/hank"
  new_fixture "$fixture_root"
  mkdir -p "$fixture_root/Dockerapps"
  mv "$fixture_root/old" "$legacy_root"
  mkdir -p "$legacy_root/upgrade-evidence"
  printf 'HANK_REMOTE_AGENT_FILES_ROOT=%s/files\n' "$legacy_root" >"$legacy_root/HankServerside/.env.agent"
  chmod 600 "$legacy_root/HankServerside/.env.agent"

  env \
    HANK_NAMING_TEST_MODE=true \
    HANK_NAMING_TEST_SKIP_DATABASE=true \
    "$legacy_root/HankServerside/scripts/migrate-hank-naming.sh" --apply \
    >"$fixture_root/output.log" 2>&1

  [ ! -e "$legacy_root" ] || fail "detected nonstandard legacy root still exists"
  [ -d "$canonical_root/HankServerside" ] || fail "detected nonstandard root was not moved"
  [ -d "$canonical_root/upgrade-evidence" ] || fail "sibling evidence was not preserved with the root"
  grep -q "^HANK_AGENT_FILES_ROOT=$canonical_root/files$" "$canonical_root/HankServerside/.env.agent" || \
    fail "nonstandard legacy root was not rewritten in the environment"
)

test_canonical_repository_path_rewrite_is_idempotent() (
  local fixture_root canonical_root canonical_repo
  fixture_root="$(mktemp -d)"
  canonical_root="$fixture_root/canonical"
  canonical_repo="$canonical_root/HankServerside"
  trap 'rm -rf "$fixture_root"' EXIT
  new_fixture "$fixture_root"
  mkdir -p "$canonical_root"
  mv "$fixture_root/old/HankServerside" "$canonical_repo"
  rmdir "$fixture_root/old"
  printf 'HANK_REMOTE_LINUX_AGENT_RELEASE_HOST_DIR=%s/data/linux-agent-releases/current\n' "$canonical_repo" >>"$canonical_repo/.env.cloud"

  timeout 10 env \
    HANK_NAMING_OLD_ROOT="$fixture_root/unused-old" \
    HANK_NAMING_NEW_ROOT="$canonical_root" \
    HANK_NAMING_TEST_MODE=true \
    HANK_NAMING_TEST_SKIP_DATABASE=true \
    "$canonical_repo/scripts/migrate-hank-naming.sh" --apply >"$fixture_root/output.log" 2>&1

  [ -d "$canonical_repo" ] || fail "canonical repository was moved during an in-place migration"
  grep -q "^HANK_LINUX_AGENT_RELEASE_HOST_DIR=$canonical_repo/data/linux-agent-releases/current$" "$canonical_repo/.env.cloud" || fail "canonical checkout-relative path was not preserved"
  if grep -q '^HANK_REMOTE_' "$canonical_repo/.env.cloud"; then
    fail "canonical in-place migration left retired environment keys"
  fi
)

test_database_and_role_are_renamed() (
  local fixture_root project_name canonical_repo
  fixture_root="$(mktemp -d)"
  project_name="hanknamingtest_${RANDOM}_$$"
  canonical_repo="$fixture_root/new/HankServerside"
  trap '
    if [ -d "$canonical_repo" ]; then
      COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$canonical_repo" --env-file "$canonical_repo/.env.cloud" down -v >/dev/null 2>&1 || true
    elif [ -d "$fixture_root/old/HankServerside" ]; then
      COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$fixture_root/old/HankServerside" --env-file "$fixture_root/old/HankServerside/.env.cloud" down -v >/dev/null 2>&1 || true
    fi
    rm -rf "$fixture_root"
  ' EXIT
  new_fixture "$fixture_root"
  write_database_compose_fixture "$fixture_root"
  COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$fixture_root/old/HankServerside" --env-file "$fixture_root/old/HankServerside/.env.cloud" up -d postgres >/dev/null
  wait_for_fixture_postgres "$fixture_root/old/HankServerside" "$project_name"

  run_database_migration "$fixture_root" "$project_name" >"$fixture_root/output.log" 2>&1

  [ -d "$canonical_repo" ] || fail "database migration did not move the installation"
  local state
  state="$(COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$canonical_repo" --env-file "$canonical_repo/.env.cloud" exec -T postgres \
    psql -X -U hank -d hank -Atc "SELECT current_database() || ':' || current_user;")"
  [ "$state" = "hank:hank" ] || fail "database and role were not both renamed: $state"
  local retired_count
  retired_count="$(COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$canonical_repo" --env-file "$canonical_repo/.env.cloud" exec -T postgres \
    psql -X -U hank -d hank -Atc "SELECT (SELECT count(*) FROM pg_database WHERE datname = 'hankremote') + (SELECT count(*) FROM pg_roles WHERE rolname = 'hankremote');")"
  [ "$retired_count" = "0" ] || fail "retired PostgreSQL identifiers remain"
)

test_database_failure_rolls_back_all_names() (
  local fixture_root project_name legacy_repo
  fixture_root="$(mktemp -d)"
  project_name="hanknamingrollback_${RANDOM}_$$"
  legacy_repo="$fixture_root/old/HankServerside"
  trap '
    if [ -d "$legacy_repo" ]; then
      COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$legacy_repo" --env-file "$legacy_repo/.env.cloud" down -v >/dev/null 2>&1 || true
    elif [ -d "$fixture_root/new/HankServerside" ]; then
      COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$fixture_root/new/HankServerside" --env-file "$fixture_root/new/HankServerside/.env.cloud" down -v >/dev/null 2>&1 || true
    fi
    rm -rf "$fixture_root"
  ' EXIT
  new_fixture "$fixture_root"
  write_database_compose_fixture "$fixture_root"
  COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$legacy_repo" --env-file "$legacy_repo/.env.cloud" up -d postgres >/dev/null
  wait_for_fixture_postgres "$legacy_repo" "$project_name"

  if env \
    COMPOSE_PROJECT_NAME="$project_name" \
    HANK_NAMING_OLD_ROOT="$fixture_root/old" \
    HANK_NAMING_NEW_ROOT="$fixture_root/new" \
    HANK_NAMING_TEST_MODE=true \
    HANK_NAMING_TEST_FAIL_AFTER_DATABASE=true \
    "$legacy_repo/scripts/migrate-hank-naming.sh" --apply >"$fixture_root/output.log" 2>&1
  then
    fail "injected database failure unexpectedly succeeded"
  fi

  [ -d "$legacy_repo" ] || fail "rollback did not retain the legacy installation path"
  [ ! -e "$fixture_root/new" ] || fail "rollback left the canonical installation path"
  grep -q '^HANK_REMOTE_CLOUD_HOST_BIND=' "$legacy_repo/.env.cloud" || fail "rollback did not restore cloud environment"
  local state
  state="$(COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$legacy_repo" --env-file "$legacy_repo/.env.cloud" exec -T postgres \
    psql -X -U hankremote -d hankremote -Atc "SELECT current_database() || ':' || current_user;")"
  [ "$state" = "hankremote:hankremote" ] || fail "rollback did not restore database and role: $state"
  grep -q 'migration rolled back' "$fixture_root/output.log" || fail "rollback result was not reported"
)

test_database_collision_fails_without_changes() (
  local fixture_root project_name legacy_repo
  fixture_root="$(mktemp -d)"
  project_name="hanknamingcollision_${RANDOM}_$$"
  legacy_repo="$fixture_root/old/HankServerside"
  trap '
    if [ -d "$legacy_repo" ]; then
      COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$legacy_repo" --env-file "$legacy_repo/.env.cloud" down -v >/dev/null 2>&1 || true
    fi
    rm -rf "$fixture_root"
  ' EXIT
  new_fixture "$fixture_root"
  write_database_compose_fixture "$fixture_root"
  COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$legacy_repo" --env-file "$legacy_repo/.env.cloud" up -d postgres >/dev/null
  wait_for_fixture_postgres "$legacy_repo" "$project_name"
  COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$legacy_repo" --env-file "$legacy_repo/.env.cloud" exec -T postgres \
    createdb -U hankremote hank

  if run_database_migration "$fixture_root" "$project_name" >"$fixture_root/output.log" 2>&1; then
    fail "migration accepted colliding legacy and canonical databases"
  fi
  [ -d "$legacy_repo" ] || fail "database collision moved the installation"
  grep -q '^HANK_REMOTE_CLOUD_HOST_BIND=' "$legacy_repo/.env.cloud" || fail "database collision changed the environment"
  local database_count
  database_count="$(COMPOSE_PROJECT_NAME="$project_name" docker compose --project-directory "$legacy_repo" --env-file "$legacy_repo/.env.cloud" exec -T postgres \
    psql -X -U hankremote -d hankremote -Atc "SELECT count(*) FROM pg_database WHERE datname IN ('hankremote', 'hank');")"
  [ "$database_count" = "2" ] || fail "database collision changed PostgreSQL state"
  grep -q 'both hankremote and hank databases exist' "$fixture_root/output.log" || fail "database collision error was not actionable"
)

test_hard_migration_rewrites_files_and_moves_installation
test_path_collision_fails_before_changes
test_duplicate_environment_keys_fail_without_moving_installation
test_nonstandard_repository_move_preserves_siblings
test_nonstandard_hank_remote_root_is_detected_and_moved_as_a_unit
test_canonical_repository_path_rewrite_is_idempotent
if [ "${HANK_NAMING_TEST_DATABASE:-}" = "true" ]; then
  test_database_and_role_are_renamed
  test_database_failure_rolls_back_all_names
  test_database_collision_fails_without_changes
fi
printf 'PASS: Hank naming migration tests\n'
