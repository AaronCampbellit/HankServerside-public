#!/usr/bin/env bash
set -euo pipefail

# A disposable local demo. This never loads .env.cloud or .env.agent.
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_dir"
for dependency in docker go npm python3; do
  command -v "$dependency" >/dev/null || { echo "Required: $dependency" >&2; exit 1; }
done
if [[ ! -x web/dashboard/node_modules/.bin/vite ]]; then
  echo 'Install the locked dashboard dependencies first: npm ci --prefix web/dashboard' >&2
  exit 1
fi
demo_dir="$(mktemp -d "${TMPDIR:-/tmp}/hank-demo.XXXXXX")"
container_name="hank-demo-$(basename "$demo_dir")"
server_pid=""
cleanup() {
  [[ -z "$server_pid" ]] || kill "$server_pid" 2>/dev/null || true
  docker stop --time 2 "$container_name" >/dev/null 2>&1 || true
  rm -rf "$demo_dir"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Remove inherited deployment/provider settings from the child environment.
# Only a PATH and this demo's explicit configuration reach the server.
intent_secret="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
encryption_secret="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"
demo_port="${HANK_DEMO_PORT:-18080}"
[[ "$demo_port" =~ ^[0-9]+$ ]] || { echo 'HANK_DEMO_PORT must be a port number' >&2; exit 1; }
docker run --rm -d --name "$container_name" \
  --label hank.purpose=disposable-demo \
  -p 127.0.0.1::5432 \
  -e POSTGRES_USER=hank_demo -e POSTGRES_DB=hank_demo \
  -e POSTGRES_HOST_AUTH_METHOD=trust \
  pgvector/pgvector:pg18 postgres -c shared_preload_libraries=pg_stat_statements >/dev/null
database_port="$(docker port "$container_name" 5432/tcp | sed 's/.*://')"
for attempt in {1..60}; do
  docker exec "$container_name" pg_isready -U hank_demo -d hank_demo >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$container_name" pg_isready -U hank_demo -d hank_demo >/dev/null
npm --prefix web/dashboard run build
go build -o "$demo_dir/hank-server" ./cmd/hank-server
demo_environment=(
  "PATH=$PATH"
  "HANK_CLOUD_ADDR=127.0.0.1:$demo_port"
  "HANK_CLOUD_DATABASE_URL=postgres://hank_demo@127.0.0.1:$database_port/hank_demo?sslmode=disable"
  "HANK_DB_OPS_INTENT_SECRET=$intent_secret"
  "HANK_SECRET_ENCRYPTION_KEY=$encryption_secret"
  "HANK_DB_OPS_STATE_DIR=$demo_dir/db-ops"
  "HANK_DB_OPS_LOG_DIR=$demo_dir/logs"
  "HANK_NOTE_ATTACHMENTS_DIR=$demo_dir/attachments"
  "HANK_AI_PROVIDER=disabled"
)
env -i "${demo_environment[@]}" "$demo_dir/hank-server" migrate up
env -i "${demo_environment[@]}" "$demo_dir/hank-server" >"$demo_dir/server.log" 2>&1 &
server_pid=$!
for attempt in {1..60}; do
  if python3 -c 'import sys, urllib.request; urllib.request.urlopen(sys.argv[1], timeout=1)' "http://127.0.0.1:$demo_port/readyz" 2>/dev/null; then
    break
  fi
  kill -0 "$server_pid" 2>/dev/null || { cat "$demo_dir/server.log" >&2; exit 1; }
  sleep 1
done
python3 docs/demo/seed.py "http://127.0.0.1:$demo_port" "$demo_dir"
echo "Open http://127.0.0.1:$demo_port"
echo "Read login details with: cat $demo_dir/login.txt"
echo 'Press Ctrl+C to stop and remove the demo database and temporary files.'
wait "$server_pid"
