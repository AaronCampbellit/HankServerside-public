#!/bin/sh
set -eu

authorized_public_url=https://hankdemo.campbellservers.com
test "$(id -u)" -eq 0 || { echo "installer must run as root" >&2; exit 77; }
test "$(hostname)" = Hankdemoserver || { echo "refusing to install outside Hankdemoserver" >&2; exit 64; }
deploy_key=${HANK_LINUX_RELEASE_DEPLOY_PUBLIC_KEY:-}
test -n "$deploy_key" || { echo "HANK_LINUX_RELEASE_DEPLOY_PUBLIC_KEY is required" >&2; exit 64; }
case "$deploy_key" in ssh-ed25519\ *) ;; *) echo "deployment key must be ssh-ed25519" >&2; exit 64 ;; esac

release_root=${HANK_LINUX_RELEASES_ROOT:-/srv/hank/linux-agent-releases}
server_repo=${HANK_SERVER_REPO:-/srv/hank/HankServerside}
server_env=${HANK_SERVER_ENV_FILE:-$server_repo/.env.cloud}
compose_project=${HANK_SERVER_COMPOSE_PROJECT:-hankserverside}
deploy_user=${HANK_LINUX_RELEASE_DEPLOY_USER:-hankrelease}
config_file=${HANK_LINUX_RELEASE_CONFIG_FILE:-/etc/hank-linux-release-deployer.conf}
script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
test -d "$server_repo" && test -f "$server_env" || { echo "demo server checkout or environment is missing" >&2; exit 66; }
test "$(sed -n 's/^HANK_PUBLIC_BASE_URL=//p' "$server_env" | tail -n 1)" = "$authorized_public_url" || { echo "server environment is not the authorized demo" >&2; exit 64; }
for path in "$release_root" "$server_repo" "$server_env" "$config_file"; do
	case "$path" in /*) ;; *) echo "release deployer paths must be absolute" >&2; exit 64 ;; esac
	case "$path" in *[!A-Za-z0-9_./-]*) echo "release deployer path contains unsupported characters" >&2; exit 64 ;; esac
done
case "$compose_project" in ''|*[!A-Za-z0-9_-]*) echo "release deployer Compose project is invalid" >&2; exit 64 ;; esac

if ! id "$deploy_user" >/dev/null 2>&1; then useradd --system --create-home --shell /bin/sh "$deploy_user"; fi
install -o root -g root -m 0755 "$script_dir/linux-release-promote.sh" /usr/local/sbin/hank-linux-release-promote
install -o root -g root -m 0755 "$script_dir/linux-release-forced-command.sh" /usr/local/sbin/hank-linux-release-forced-command
install -o root -g root -m 0755 "$script_dir/linux-release-local-command.sh" /usr/local/sbin/hank-linux-release-local-command
install -d -o root -g root -m 0755 "$release_root"
temporary_config=$(mktemp "$config_file.XXXXXX")
printf 'HANK_LINUX_RELEASES_ROOT=%s\nHANK_SERVER_REPO=%s\nHANK_SERVER_ENV_FILE=%s\nHANK_SERVER_COMPOSE_PROJECT=%s\n' "$release_root" "$server_repo" "$server_env" "$compose_project" > "$temporary_config"
chmod 0600 "$temporary_config"
chown root:root "$temporary_config"
mv "$temporary_config" "$config_file"

deploy_home=$(getent passwd "$deploy_user" | cut -d: -f6)
install -d -o "$deploy_user" -g "$deploy_user" -m 0700 "$deploy_home/.ssh"
authorized_keys=$deploy_home/.ssh/authorized_keys
printf 'restrict,command="/usr/local/sbin/hank-linux-release-forced-command" %s\n' "$deploy_key" > "$authorized_keys"
chown "$deploy_user:$deploy_user" "$authorized_keys"
chmod 0600 "$authorized_keys"

sudoers=/etc/sudoers.d/hank-linux-release-deployer
printf '%s ALL=(root) NOPASSWD: /usr/local/sbin/hank-linux-release-local-command\n' "$deploy_user" > "$sudoers"
chmod 0440 "$sudoers"
visudo -cf "$sudoers" >/dev/null

temporary_env=$(mktemp "$server_env.XXXXXX")
awk -v value="$release_root/current" 'BEGIN { written=0 } /^HANK_LINUX_AGENT_RELEASE_HOST_DIR=/ { if (!written) { print "HANK_LINUX_AGENT_RELEASE_HOST_DIR=" value; written=1 }; next } { print } END { if (!written) print "HANK_LINUX_AGENT_RELEASE_HOST_DIR=" value }' "$server_env" > "$temporary_env"
chmod --reference="$server_env" "$temporary_env"
chown --reference="$server_env" "$temporary_env"
mv "$temporary_env" "$server_env"

echo "installed demo-only Linux release deployer for $deploy_user"
