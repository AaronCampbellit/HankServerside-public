#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
project_directory="$repo_root"
env_file="$repo_root/.env.cloud"
database=""
role=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --project-directory)
      project_directory="$2"
      shift 2
      ;;
    --env-file)
      env_file="$2"
      shift 2
      ;;
    --database)
      database="$2"
      shift 2
      ;;
    --role)
      role="$2"
      shift 2
      ;;
    *)
      printf 'usage: %s [--project-directory DIR] [--env-file FILE] [--database NAME] [--role NAME]\n' "$0" >&2
      exit 2
      ;;
  esac
done

fail() {
  printf '[production-preflight] ERROR: %s\n' "$*" >&2
  exit 1
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

command -v docker >/dev/null 2>&1 || fail "docker is required"
docker compose version >/dev/null 2>&1 || fail "Docker Compose v2 is required"
command -v python3 >/dev/null 2>&1 || fail "python3 is required"
[ -d "$project_directory" ] || fail "project directory does not exist"
[ -f "$env_file" ] || fail "cloud environment file does not exist"
database="${database:-$(env_value POSTGRES_DB)}"
role="${role:-$(env_value POSTGRES_USER)}"
[[ "$database" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "POSTGRES_DB is missing or invalid"
[[ "$role" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "POSTGRES_USER is missing or invalid"

temp_root="$(mktemp -d)"
trap 'rm -rf "$temp_root"' EXIT
config_json="$temp_root/compose-config.json"

compose=(docker compose --project-directory "$project_directory" --env-file "$env_file")
"${compose[@]}" --profile restore config --format json >"$config_json"
mapfile -t volumes < <("$repo_root/scripts/compose-storage-preflight.py" --values "$config_json")
[ "${#volumes[@]}" -eq 5 ] || fail "storage topology parser returned an incomplete result"

postgres_live="${volumes[0]}"
postgres_restore="${volumes[1]}"
pgbackrest_repo="${volumes[2]}"
attachments_live="${volumes[3]}"
attachments_restore="${volumes[4]}"

assert_running_mount() {
  local service="$1"
  local target="$2"
  local expected="$3"
  local container_id actual
  container_id="$("${compose[@]}" --profile restore ps -q --status running "$service")"
  [ -n "$container_id" ] || return 0
  actual="$(docker inspect "$container_id" --format "{{range .Mounts}}{{if eq .Destination \"$target\"}}{{.Name}}{{end}}{{end}}")"
  [ -n "$actual" ] || fail "running $service has no named-volume mount at $target"
  [ "$actual" = "$expected" ] || fail "running $service uses a different volume at $target than the effective Compose configuration"
}

assert_running_mount postgres /var/lib/postgresql "$postgres_live"
assert_running_mount postgres /var/lib/pgbackrest "$pgbackrest_repo"
assert_running_mount db-ops /var/lib/postgresql "$postgres_live"
assert_running_mount db-ops /var/lib/postgresql/restore "$postgres_restore"
assert_running_mount db-ops /var/lib/pgbackrest "$pgbackrest_repo"
assert_running_mount db-ops /var/lib/hank/note-attachments "$attachments_live"
assert_running_mount db-ops /var/lib/hank/note-attachments-restore "$attachments_restore"
assert_running_mount cloud /var/lib/hank/note-attachments "$attachments_live"
assert_running_mount postgres-restore /var/lib/postgresql/restore "$postgres_restore"
assert_running_mount postgres-restore /var/lib/pgbackrest "$pgbackrest_repo"

attachment_rows="$("${compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U "$role" -d "$database" -Atc \
  "SELECT count(*) FROM note_attachments WHERE deleted_at IS NULL AND status <> 'deleted'")" || \
  fail "could not count active attachment rows"
[[ "$attachment_rows" =~ ^[0-9]+$ ]] || fail "active attachment count was invalid"

if [ "$attachment_rows" -gt 0 ]; then
  attachment_inventory="$("${compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U "$role" -d "$database" -F $'\t' -Atc "
    WITH active_files AS (
      SELECT id, storage_key, size_bytes FROM note_attachments
      WHERE deleted_at IS NULL AND status <> 'deleted'
      UNION ALL
      SELECT id, preview_storage_key, preview_size_bytes FROM note_attachments
      WHERE deleted_at IS NULL AND status <> 'deleted' AND preview_storage_key IS NOT NULL AND preview_storage_key <> ''
    )
    SELECT
      replace(encode(convert_to(id, 'UTF8'), 'base64'), chr(10), ''),
      replace(encode(convert_to(storage_key, 'UTF8'), 'base64'), chr(10), ''),
      octet_length(storage_key),
      size_bytes,
      count(*) OVER (PARTITION BY storage_key)
    FROM active_files
    ORDER BY id, storage_key")" || fail "could not load active attachment inventory"

  attachment_result="$(printf '%s\n' "$attachment_inventory" | "${compose[@]}" run -T --rm --no-deps --user postgres --entrypoint sh db-ops -eu -c '
    root=/var/lib/hank/note-attachments
    [ -d "$root" ] && [ ! -L "$root" ] && [ -r "$root" ] && [ -x "$root" ] || exit 20
    files=0
    tab=$(printf "\t")
    while IFS="$tab" read -r encoded_id encoded_key expected_key_bytes expected_size duplicate_count; do
      [ -n "$encoded_id" ] && [ -n "$encoded_key" ] || exit 21
      case "$expected_key_bytes:$expected_size:$duplicate_count" in
        *[!0-9:]*|:*|*:|*::*|*:*:*:*) exit 22 ;;
      esac
      [ "$duplicate_count" = 1 ] || exit 23
      key=$(printf "%s" "$encoded_key" | base64 -d) || exit 24
      [ "$(printf "%s" "$key" | wc -c)" = "$expected_key_bytes" ] || exit 24
      case "$key" in
        ""|/*|*\\*) exit 25 ;;
      esac
      current=$root
      rest=$key
      while [ -n "$rest" ]; do
        part=${rest%%/*}
        if [ "$part" = "$rest" ]; then rest=; else rest=${rest#*/}; fi
        case "$part" in ""|.|..) exit 26 ;; esac
        current=$current/$part
        [ ! -L "$current" ] || exit 27
        if [ -n "$rest" ]; then [ -d "$current" ] && [ -x "$current" ] || exit 28
        else
          [ -f "$current" ] && [ -r "$current" ] || exit 29
          actual_size=$(stat -c %s "$current") || exit 30
          [ "$actual_size" = "$expected_size" ] || exit 31
        fi
      done
      files=$((files + 1))
    done
    printf "checked_files=%s\n" "$files"
  ')" || fail "an active attachment or preview is missing, unreadable, size-mismatched, duplicated, or unsafe"
  [[ "$attachment_result" =~ ^checked_files=[0-9]+$ ]] || fail "attachment validation returned an invalid result"
else
  attachment_result="checked_files=0"
fi

printf '[production-preflight] storage topology matches the effective Compose configuration\n'
printf '[production-preflight] active attachment integrity matched (%s rows, %s)\n' "$attachment_rows" "$attachment_result"
