#!/usr/bin/env bash
set -euo pipefail

# One-time migration tooling: legacy environment names are accepted only here.
# Use an isolated maintenance image; never replace the running application image.
mode=""
image=""
postgres_container=""
env_file=".env.cloud"
backup_label=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --check|--apply) [ -z "$mode" ] || { echo 'Choose one mode' >&2; exit 2; }; mode="$1"; shift ;;
    --image|--postgres-container|--env-file|--backup-label)
      [ "$#" -ge 2 ] || { echo 'Missing option value' >&2; exit 2; }
      case "$1" in
        --image) image="$2" ;;
        --postgres-container) postgres_container="$2" ;;
        --env-file) env_file="$2" ;;
        --backup-label) backup_label="$2" ;;
      esac
      shift 2 ;;
    *) echo 'Unknown reconciliation option' >&2; exit 2 ;;
  esac
done
[ -n "$mode" ] && [ -n "$image" ] && [ -n "$postgres_container" ] && [ -f "$env_file" ] || {
  echo 'Usage: reconcile-assistant-index-migration.sh --check|--apply --image IMAGE --postgres-container NAME [--env-file FILE] [--backup-label LABEL]' >&2
  exit 2
}
if [ "$mode" = --apply ] && [[ ! "$backup_label" =~ ^[0-9]{8}-[0-9]{6}F(_[0-9]{8}-[0-9]{6}[DI])?$ ]]; then
  echo '--apply requires the label of a verified retained backup' >&2
  exit 2
fi
service=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.service"}}' "$postgres_container")
running=$(docker inspect --format '{{.State.Running}}' "$postgres_container")
[ "$service" = postgres ] && [ "$running" = true ] || { echo 'Expected the running Compose postgres container' >&2; exit 1; }
args=("$mode")
if [ -n "$backup_label" ]; then args+=("--backup-label=$backup_label"); fi
docker run --rm --read-only --cap-drop ALL --security-opt no-new-privileges \
  --network "container:$postgres_container" --env-file "$env_file" \
  --entrypoint sh "$image" -ceu '
    if [ -n "${HANK_CLOUD_DATABASE_URL:-}" ] && [ -n "${HANK_REMOTE_CLOUD_DATABASE_URL:-}" ] && [ "$HANK_CLOUD_DATABASE_URL" != "$HANK_REMOTE_CLOUD_DATABASE_URL" ]; then
      echo "Conflicting database URL variables; reconcile the environment before proceeding" >&2
      exit 2
    fi
    export HANK_CLOUD_DATABASE_URL="${HANK_CLOUD_DATABASE_URL:-${HANK_REMOTE_CLOUD_DATABASE_URL:-}}"
    exec /usr/local/bin/hank-server migrate reconcile-assistant-index "$@"
  ' sh "${args[@]}"
