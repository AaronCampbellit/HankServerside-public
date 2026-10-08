#!/usr/bin/env sh
set -eu

COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.yml}"
COMPOSE_ENV_FILE="${COMPOSE_ENV_FILE:-.env.cloud}"
SERVICE="${POSTGRES_SERVICE:-postgres}"

compose() {
	docker compose --env-file "$COMPOSE_ENV_FILE" -f "$COMPOSE_FILE" "$@"
}

echo "Stopping Hank cloud and PostgreSQL before enabling checksums..."
compose stop cloud db-ops "$SERVICE"

echo "Enabling PostgreSQL data checksums. This requires the database to stay stopped."
compose run --rm --no-deps --entrypoint sh "$SERVICE" -c 'su-exec postgres pg_checksums --enable -D /var/lib/postgresql/data'

echo "Starting PostgreSQL, db-ops, and cloud..."
compose up -d "$SERVICE" db-ops cloud

echo "Checksum enablement requested. Check /dashboard/settings#backups after PostgreSQL is healthy."
