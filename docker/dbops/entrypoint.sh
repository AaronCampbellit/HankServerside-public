#!/bin/sh
set -eu

docker_gid="${HANK_DB_OPS_DOCKER_GID:-}"
if [ -n "$docker_gid" ]; then
	group_name="$(getent group "$docker_gid" 2>/dev/null | cut -d: -f1 || true)"
	if [ -z "$group_name" ]; then
		group_name="hankdockerhost"
		addgroup -S -g "$docker_gid" "$group_name" 2>/dev/null || true
	fi
	if [ -n "$group_name" ]; then
		addgroup postgres "$group_name" 2>/dev/null || true
	fi
fi

shared_gid="${HANK_DB_OPS_SHARED_GID:-101}"
shared_group="$(getent group "$shared_gid" 2>/dev/null | cut -d: -f1 || true)"
if [ -z "$shared_group" ]; then
	shared_group="hankshared"
	addgroup -S -g "$shared_gid" "$shared_group" 2>/dev/null || true
fi
if [ -n "$shared_group" ]; then
	addgroup postgres "$shared_group" 2>/dev/null || true
fi

compose_env="${HANK_DB_OPS_COMPOSE_ENV_FILE:-/run/hank/db-ops-compose.env}"
if [ -r /workspace/.env.cloud ]; then
	mkdir -p "$(dirname "$compose_env")"
	cp /workspace/.env.cloud "$compose_env"
	chown postgres:postgres "$compose_env"
	chmod 600 "$compose_env"
fi

for path in \
	/var/lib/hank/db-ops/state \
	/var/lib/hank/db-ops/state/intents \
	/var/lib/hank/db-ops/state/intents-done \
	/var/log/hank/db-ops
do
	mkdir -p "$path"
	chown -R postgres:"$shared_group" "$path"
	chmod -R ug+rwX,o-rwx "$path"
	find "$path" -type d -exec chmod g+s {} +
done

for path in \
	/var/lib/pgbackrest \
	/var/log/pgbackrest \
	/var/lib/postgresql/restore \
	/var/lib/hank/note-attachments-restore
do
	mkdir -p "$path"
	chown -R postgres:postgres "$path"
done

# Both services share attachment access; preserve file owners and private
# recovery artifacts. The cloud can create files in restored group-owned folders.
attachments=/var/lib/hank/note-attachments
mkdir -p "$attachments"
find "$attachments" -name .hank-recovery -prune -o -type d -exec chgrp "$shared_group" {} + -exec chmod 2770 {} +
find "$attachments" -name .hank-recovery -prune -o -type f -exec chgrp "$shared_group" {} + -exec chmod 660 {} +
mkdir -p /var/lib/postgresql/.hank-recovery
chown postgres:postgres /var/lib/postgresql/.hank-recovery
chmod 700 /var/lib/postgresql/.hank-recovery

exec gosu postgres "$@"
