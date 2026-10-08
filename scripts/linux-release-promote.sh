#!/bin/sh
set -eu

authorized_public_url=https://hankdemo.campbellservers.com
version=${1:-}
if [ "$#" -ne 1 ] || ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "usage: linux-release-promote.sh MAJOR.MINOR.PATCH" >&2
	exit 64
fi

config_file=${HANK_LINUX_RELEASE_CONFIG_FILE:-/etc/hank-linux-release-deployer.conf}
config_value() {
	key=$1
	test -f "$config_file" || return 0
	awk -F= -v key="$key" '$1 == key { if (seen++) exit 65; print substr($0, index($0, "=") + 1) }' "$config_file"
}
if [ -f "$config_file" ]; then
	test "$(stat -c %u "$config_file")" -eq "$(id -u)" || { echo "release deployer config has an unexpected owner" >&2; exit 64; }
	if find "$config_file" -perm /022 -print -quit | grep -q .; then echo "release deployer config is writable by group or other" >&2; exit 64; fi
fi
release_root=${HANK_LINUX_RELEASES_ROOT:-$(config_value HANK_LINUX_RELEASES_ROOT)}
server_repo=${HANK_SERVER_REPO:-$(config_value HANK_SERVER_REPO)}
server_env=${HANK_SERVER_ENV_FILE:-$(config_value HANK_SERVER_ENV_FILE)}
compose_project=${HANK_SERVER_COMPOSE_PROJECT:-$(config_value HANK_SERVER_COMPOSE_PROJECT)}
release_root=${release_root:-/srv/hank/linux-agent-releases}
server_repo=${server_repo:-/srv/hank/HankServerside}
server_env=${server_env:-$server_repo/.env.cloud}
compose_project=${compose_project:-hankserverside}
public_url=${HANK_DEMO_PUBLIC_URL:-$authorized_public_url}
expected_hostname=${HANK_LINUX_RELEASE_EXPECTED_HOSTNAME:-Hankdemoserver}
agent_command=${HANK_LINUX_AGENT_COMMAND:-/usr/bin/hankagent}
manifest_url=$public_url/install/linux-release/release.json

test "$public_url" = "$authorized_public_url" || { echo "refusing non-demo public URL" >&2; exit 64; }
test "$(hostname)" = "$expected_hostname" || { echo "refusing non-demo hostname" >&2; exit 64; }
test -d "$release_root" || { echo "release root does not exist" >&2; exit 66; }
test -d "$server_repo" || { echo "server repository does not exist" >&2; exit 66; }
test -f "$server_env" || { echo "server environment file does not exist" >&2; exit 66; }
test "$(sed -n 's/^HANK_PUBLIC_BASE_URL=//p' "$server_env" | tail -n 1)" = "$authorized_public_url" || { echo "server environment is not the authorized demo" >&2; exit 64; }
test "$(sed -n 's/^HANK_LINUX_AGENT_RELEASE_HOST_DIR=//p' "$server_env" | tail -n 1)" = "$release_root/current" || { echo "server release mount is not the managed current link" >&2; exit 64; }

exec 9>"$release_root/.promote.lock"
flock -n 9 || { echo "another Linux release promotion is active" >&2; exit 75; }

destination=$release_root/$version
test ! -e "$destination" || { echo "release version already exists" >&2; exit 73; }
archive=$(mktemp "$release_root/.incoming.XXXXXX")
stage=$(mktemp -d "$release_root/.stage.XXXXXX")
live_manifest=$(mktemp "$release_root/.live-manifest.XXXXXX")
temporary_link=$release_root/.current.$$
previous=
switched=false
agent_updated=false

compose() { docker compose --project-name "$compose_project" --project-directory "$server_repo" --env-file "$server_env" "$@"; }

restore_previous() {
	set +e
	if [ "$switched" = true ]; then
		if [ -n "$previous" ]; then
			ln -s "$previous" "$temporary_link.rollback"
			mv -Tf "$temporary_link.rollback" "$release_root/current"
		else
			rm -f "$release_root/current"
		fi
		compose up -d --no-deps --force-recreate cloud >/dev/null 2>&1
	fi
	if [ "$agent_updated" = true ]; then
		"$agent_command" --system update rollback >/dev/null 2>&1
	fi
	if [ -d "$destination" ]; then rm -rf "$destination"; fi
	set -e
}

cleanup() {
	status=$?
	trap - EXIT HUP INT TERM
	if [ "$status" -ne 0 ]; then restore_previous; fi
	rm -f "$archive" "$live_manifest" "$temporary_link" "$temporary_link.rollback"
	if [ -n "$stage" ] && [ -d "$stage" ]; then rm -rf "$stage"; fi
	exit "$status"
}
trap cleanup EXIT HUP INT TERM

head -c 1073741825 > "$archive"
test "$(wc -c < "$archive")" -le 1073741824 || { echo "release archive is too large" >&2; exit 65; }
entries=$(tar --quoting-style=literal -tf "$archive")
test -n "$entries" || { echo "release archive is empty" >&2; exit 65; }
test "$(printf '%s\n' "$entries" | wc -l)" -le 64 || { echo "release archive has too many files" >&2; exit 65; }
test "$(printf '%s\n' "$entries" | sort -u | wc -l)" -eq "$(printf '%s\n' "$entries" | wc -l)" || { echo "release archive contains duplicate filenames" >&2; exit 65; }
printf '%s\n' "$entries" | while IFS= read -r name; do
	case "$name" in ''|*/*|*[!A-Za-z0-9._+-]*) echo "invalid release archive filename: $name" >&2; exit 65 ;; esac
done
tar --quoting-style=literal -tvf "$archive" | awk 'substr($1,1,1) != "-" { exit 1 }' || { echo "release archive may contain only regular files" >&2; exit 65; }

tar --no-same-owner --no-same-permissions -xf "$archive" -C "$stage"
printf '%s\n' "$entries" | while IFS= read -r name; do
	test -f "$stage/$name" && test ! -L "$stage/$name" || { echo "extracted release entry is not a regular file" >&2; exit 65; }
	test "$(wc -c < "$stage/$name")" -le 268435456 || { echo "release file is too large: $name" >&2; exit 65; }
done
for required in release.json checksums.txt hankagent-linux-amd64 hankagent-linux-arm64; do
	test -f "$stage/$required" || { echo "release archive is missing $required" >&2; exit 65; }
done

checksum_entries=$(awk 'NF != 2 || length($1) != 64 || $1 !~ /^[0-9a-fA-F]+$/ || $2 !~ /^[A-Za-z0-9._+-]+$/ || $2 == "checksums.txt" { exit 1 } { print $2 }' "$stage/checksums.txt") || { echo "checksums.txt is invalid" >&2; exit 65; }
test "$(printf '%s\n' "$checksum_entries" | sort -u | wc -l)" -eq "$(printf '%s\n' "$checksum_entries" | wc -l)" || { echo "checksums.txt contains duplicates" >&2; exit 65; }
for name in $entries; do
	if [ "$name" != checksums.txt ]; then printf '%s\n' "$checksum_entries" | grep -Fx "$name" >/dev/null || { echo "checksums.txt omits $name" >&2; exit 65; }; fi
done
for name in $checksum_entries; do printf '%s\n' "$entries" | grep -Fx "$name" >/dev/null || { echo "checksums.txt names an absent file" >&2; exit 65; }; done
(cd "$stage" && sha256sum -c checksums.txt >/dev/null)
test "$(jq -er '.version' "$stage/release.json")" = "$version" || { echo "manifest version does not match promotion" >&2; exit 65; }
chmod 0755 "$stage/hankagent-linux-amd64" "$stage/hankagent-linux-arm64"

if [ -n "${HANK_LINUX_RELEASE_VERIFY_COMMAND:-}" ]; then
	"$HANK_LINUX_RELEASE_VERIFY_COMMAND" "$stage"
else
	compose run -T --rm --no-deps --user root -v "$stage:/release:ro" --entrypoint /usr/local/bin/hank-server cloud linux-release verify --dir /release
fi

if [ -L "$release_root/current" ]; then
	previous=$(readlink "$release_root/current")
	case "$previous" in
		"$release_root"/*) ;;
		*) printf '%s\n' "$previous" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' && test -d "$release_root/$previous" || { echo "current release link escapes release root" >&2; exit 65; } ;;
	esac
elif [ -e "$release_root/current" ]; then
	echo "current release path is not a symbolic link" >&2
	exit 65
fi

chmod 0755 "$stage"
mv "$stage" "$destination"
stage=
ln -s "$version" "$temporary_link"
mv -Tf "$temporary_link" "$release_root/current"
switched=true

compose up -d --no-deps --force-recreate cloud
attempt=0
ready=false
while [ "$attempt" -lt "${HANK_LINUX_RELEASE_READY_ATTEMPTS:-30}" ]; do
	if curl -fsS --max-time 20 "$public_url/readyz" >/dev/null; then ready=true; break; fi
	attempt=$((attempt + 1))
	sleep 1
done
test "$ready" = true || { echo "updated demo cloud did not become ready" >&2; exit 70; }
curl -fsS --max-time 20 "$manifest_url" > "$live_manifest"
cmp "$destination/release.json" "$live_manifest" >/dev/null || { echo "live manifest does not match promoted bytes" >&2; exit 70; }

compose run -T --rm --no-deps --entrypoint /usr/local/bin/hank-server cloud linux-release register --dir /var/lib/hank/linux-agent-release --manifest-url "$manifest_url"

current_agent_version=$("$agent_command" --system version 2>/dev/null || true)
if [ "$current_agent_version" != "$version" ]; then
	"$agent_command" --system update apply --manifest "$manifest_url"
	agent_updated=true
fi
agent_id=$("$agent_command" --system status | jq -er '.agent_id | strings | select(length > 0)')
attempt=0
healthy=false
while [ "$attempt" -lt "${HANK_LINUX_RELEASE_HEALTH_ATTEMPTS:-30}" ]; do
	if [ "$("$agent_command" --system version 2>/dev/null || true)" = "$version" ] && compose logs --since=2m cloud 2>&1 | grep -F 'agent websocket connected' | grep -Fq "agent_id=$agent_id"; then healthy=true; break; fi
	attempt=$((attempt + 1))
	sleep 1
done
test "$healthy" = true || { echo "updated demo agent did not authenticate at the target version" >&2; exit 70; }

if [ -n "${HANK_LINUX_RELEASE_ACCEPTANCE_COMMAND:-}" ]; then
	"$HANK_LINUX_RELEASE_ACCEPTANCE_COMMAND" "$version"
else
	systemctl is-active --quiet hankagent.service
	"$agent_command" --system doctor >/dev/null
fi

compose run -T --rm --no-deps -v "$release_root:/releases" --entrypoint /usr/local/bin/hank-server cloud linux-release prune --root /releases --retain 3
compose run -T --rm --no-deps --entrypoint /usr/local/bin/hank-server cloud linux-release activate --dir /var/lib/hank/linux-agent-release --spread 15m

switched=false
agent_updated=false
echo "promoted Linux agent release $version to the authorized demo"
