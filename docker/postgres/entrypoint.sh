#!/bin/sh
set -e

mkdir -p /var/lib/pgbackrest /var/log/pgbackrest
for dir in /var/lib/pgbackrest /var/log/pgbackrest; do
	if chown -R postgres:postgres "$dir" 2>/dev/null; then
		chmod 750 "$dir" 2>/dev/null || true
	else
		echo "hank-postgres-entrypoint: leaving read-only $dir ownership unchanged"
	fi
done

exec docker-entrypoint.sh "$@"
