#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
container="hank-linux-fleet-integration-$$"
cleanup() { docker rm -f "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT HUP INT TERM

docker run -d --name "$container" --label hank.test=linux-fleet \
	-e POSTGRES_USER=hanktest -e POSTGRES_PASSWORD=hanktest -e POSTGRES_DB=postgres \
	-p 127.0.0.1::5432 pgvector/pgvector:pg18-trixie \
	-c shared_preload_libraries=pg_stat_statements >/dev/null

attempt=0
until docker exec "$container" pg_isready -U hanktest -d postgres >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	if [ "$attempt" -ge 60 ]; then echo "test PostgreSQL did not become ready" >&2; exit 1; fi
	sleep 1
done
port=$(docker port "$container" 5432/tcp | sed -n 's/.*://p')
test -n "$port"
database_url="postgres://hanktest:hanktest@127.0.0.1:$port/postgres?sslmode=disable"

cd "$repo_root"
HANK_TEST_DATABASE_URL="$database_url" go test ./internal/store -run 'TestLinuxAgentReleaseRolloutEligibilityPinsAndTransitions|TestUpdateAgentRuntimeMetadata' -count=1
HANK_TEST_DATABASE_URL="$database_url" go test ./internal/cloud -run 'TestLinuxUpdateDispatchAndAuthenticatedReconnectAcknowledgement|TestLegacyLinuxUpdateCommand' -count=1
echo "Linux fleet integration tests passed"
