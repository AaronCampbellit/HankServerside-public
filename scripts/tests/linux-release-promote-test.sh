#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
promoter="$repo_root/scripts/linux-release-promote.sh"
forced="$repo_root/scripts/linux-release-forced-command.sh"
local_command="$repo_root/scripts/linux-release-local-command.sh"
test_root=$(mktemp -d)
trap 'rm -rf "$test_root"' EXIT HUP INT TERM

release_root="$test_root/releases"
server_repo="$test_root/server"
fake_bin="$test_root/bin"
mkdir -p "$release_root" "$server_repo" "$fake_bin"
cat > "$server_repo/.env.cloud" <<EOF
HANK_PUBLIC_BASE_URL=https://hankdemo.campbellservers.com
HANK_LINUX_AGENT_RELEASE_HOST_DIR=$release_root/current
EOF

cat > "$fake_bin/verifier" <<'EOF'
#!/bin/sh
set -eu
test -f "$1/release.json"
printf 'verified %s\n' "$1" >> "$TEST_COMMAND_LOG"
EOF
cat > "$fake_bin/docker" <<'EOF'
#!/bin/sh
set -eu
printf 'docker %s\n' "$*" >> "$TEST_COMMAND_LOG"
case " $* " in *' logs '*) echo 'agent websocket connected agent_id=linux-test' ;; esac
EOF
cat > "$fake_bin/curl" <<'EOF'
#!/bin/sh
set -eu
release_root=${HANK_LINUX_RELEASES_ROOT:-}
if [ -z "$release_root" ] && [ -f "${HANK_LINUX_RELEASE_CONFIG_FILE:-}" ]; then release_root=$(sed -n 's/^HANK_LINUX_RELEASES_ROOT=//p' "$HANK_LINUX_RELEASE_CONFIG_FILE"); fi
url=
for value in "$@"; do url=$value; done
if [ -f "$TEST_ROOT/fail-ready" ]; then exit 22; fi
if [ -f "$TEST_ROOT/transient-ready" ]; then
	remaining=$(cat "$TEST_ROOT/transient-ready")
	if [ "$remaining" -gt 0 ]; then printf '%s\n' "$((remaining - 1))" > "$TEST_ROOT/transient-ready"; exit 22; fi
fi
case "$url" in
  */readyz) echo ready ;;
  */install/linux-release/release.json) cat "$release_root/current/release.json" ;;
  *) exit 22 ;;
esac
EOF
cat > "$fake_bin/hankagent" <<'EOF'
#!/bin/sh
set -eu
release_root=${HANK_LINUX_RELEASES_ROOT:-}
if [ -z "$release_root" ] && [ -f "${HANK_LINUX_RELEASE_CONFIG_FILE:-}" ]; then release_root=$(sed -n 's/^HANK_LINUX_RELEASES_ROOT=//p' "$HANK_LINUX_RELEASE_CONFIG_FILE"); fi
printf 'hankagent %s\n' "$*" >> "$TEST_COMMAND_LOG"
case " $* " in
  *' update apply '*) sed -n 's/.*"version":"\([^"]*\)".*/\1/p' "$release_root/current/release.json" > "$TEST_ROOT/agent-version" ;;
  *' update rollback '*) exit 0 ;;
  *' status '*) printf '{"agent_id":"linux-test"}\n' ;;
  *' version '*) if [ -f "$TEST_ROOT/agent-version" ]; then cat "$TEST_ROOT/agent-version"; else echo 0.2.0; fi ;;
esac
EOF
cat > "$fake_bin/acceptance" <<'EOF'
#!/bin/sh
set -eu
printf 'acceptance\n' >> "$TEST_COMMAND_LOG"
EOF
chmod +x "$fake_bin"/*

make_release() {
	version=$1
	directory="$test_root/release-$version"
	mkdir -p "$directory"
	printf 'amd64-%s\n' "$version" > "$directory/hankagent-linux-amd64"
	printf 'arm64-%s\n' "$version" > "$directory/hankagent-linux-arm64"
	printf '{"version":"%s","artifacts":[]}\n' "$version" > "$directory/release.json"
	(cd "$directory" && sha256sum hankagent-linux-amd64 hankagent-linux-arm64 release.json > checksums.txt)
	(cd "$directory" && tar --sort=name --mtime='@0' --owner=0 --group=0 --numeric-owner -cf "$test_root/release-$version.tar" checksums.txt hankagent-linux-amd64 hankagent-linux-arm64 release.json)
}

run_promote() {
	version=$1
	env PATH="$fake_bin:$PATH" \
		TEST_ROOT="$test_root" TEST_COMMAND_LOG="$test_root/commands.log" \
		HANK_LINUX_RELEASES_ROOT="$release_root" \
		HANK_SERVER_REPO="$server_repo" \
		HANK_SERVER_ENV_FILE="$server_repo/.env.cloud" \
		HANK_LINUX_RELEASE_EXPECTED_HOSTNAME="$(hostname)" \
		HANK_LINUX_RELEASE_READY_ATTEMPTS=2 \
		HANK_LINUX_RELEASE_VERIFY_COMMAND="$fake_bin/verifier" \
		HANK_LINUX_AGENT_COMMAND="$fake_bin/hankagent" \
		HANK_LINUX_RELEASE_ACCEPTANCE_COMMAND="$fake_bin/acceptance" \
		"$promoter" "$version" < "$test_root/release-$version.tar"
}

run_configured_promote() {
	version=$1
	configured_root="$test_root/configured-releases"
	configured_server="$test_root/configured-server"
	configured_file="$test_root/deployer.conf"
	mkdir -p "$configured_root" "$configured_server"
	cat > "$configured_server/.env.cloud" <<EOF
HANK_PUBLIC_BASE_URL=https://hankdemo.campbellservers.com
HANK_LINUX_AGENT_RELEASE_HOST_DIR=$configured_root/current
EOF
	cat > "$configured_file" <<EOF
HANK_LINUX_RELEASES_ROOT=$configured_root
HANK_SERVER_REPO=$configured_server
HANK_SERVER_ENV_FILE=$configured_server/.env.cloud
HANK_SERVER_COMPOSE_PROJECT=hankserverside
EOF
	chmod 0600 "$configured_file"
	env PATH="$fake_bin:$PATH" \
		TEST_ROOT="$test_root" TEST_COMMAND_LOG="$test_root/commands.log" \
		HANK_LINUX_RELEASE_CONFIG_FILE="$configured_file" \
		HANK_LINUX_RELEASE_EXPECTED_HOSTNAME="$(hostname)" \
		HANK_LINUX_RELEASE_READY_ATTEMPTS=2 \
		HANK_LINUX_RELEASE_VERIFY_COMMAND="$fake_bin/verifier" \
		HANK_LINUX_AGENT_COMMAND="$fake_bin/hankagent" \
		HANK_LINUX_RELEASE_ACCEPTANCE_COMMAND="$fake_bin/acceptance" \
		"$promoter" "$version" < "$test_root/release-$version.tar"
	test "$(readlink "$configured_root/current")" = "$version"
}

make_release 0.3.0
printf '1\n' > "$test_root/transient-ready"
run_promote 0.3.0
test "$(cat "$test_root/transient-ready")" -eq 0
test "$(readlink "$release_root/current")" = 0.3.0
test -f "$release_root/0.3.0/release.json"
test "$(stat -c %a "$release_root/0.3.0")" = 755
grep -q 'verified' "$test_root/commands.log"
grep -q 'hankagent --system update apply' "$test_root/commands.log"
grep -q 'acceptance' "$test_root/commands.log"
grep -q 'linux-release register .*--manifest-url https://hankdemo.campbellservers.com/install/linux-release/release.json' "$test_root/commands.log"
grep -q 'linux-release activate .*--spread 15m' "$test_root/commands.log"
grep -q 'linux-release prune .*--root /releases --retain 3' "$test_root/commands.log"
grep -q 'docker compose --project-name hankserverside' "$test_root/commands.log"
test "$(grep -c 'docker compose .*run -T --rm --no-deps' "$test_root/commands.log")" -ge 3
grep -q 'docker compose .*up -d --no-deps --force-recreate cloud' "$test_root/commands.log"
if grep -q 'docker compose .*up -d --force-recreate cloud' "$test_root/commands.log"; then
	echo "cloud recreation may start or replace dependencies" >&2
	exit 1
fi

make_release 0.4.0
run_configured_promote 0.4.0

if run_promote 0.3.0 >/dev/null 2>&1; then
	echo "duplicate immutable version was accepted" >&2
	exit 1
fi

make_release 0.3.1
touch "$test_root/fail-ready"
activate_count=$(grep -c 'linux-release activate' "$test_root/commands.log")
if run_promote 0.3.1 >/dev/null 2>&1; then
	echo "failed readiness check was accepted" >&2
	exit 1
fi
test "$(readlink "$release_root/current")" = 0.3.0
test ! -e "$release_root/0.3.1"
test -d "$release_root/0.3.0"
test "$(grep -c 'linux-release activate' "$test_root/commands.log")" -eq "$activate_count"
rm "$test_root/fail-ready"

bad="$test_root/bad"
mkdir -p "$bad/nested"
printf bad > "$bad/nested/file"
(cd "$bad" && tar -cf "$test_root/release-0.3.2.tar" nested/file)
if run_promote 0.3.2 >/dev/null 2>&1; then
	echo "nested archive filename was accepted" >&2
	exit 1
fi

link_root="$test_root/link"
mkdir -p "$link_root"
ln -s /etc/passwd "$link_root/release.json"
(cd "$link_root" && tar -cf "$test_root/release-0.3.3.tar" release.json)
if run_promote 0.3.3 >/dev/null 2>&1; then
	echo "symbolic-link archive entry was accepted" >&2
	exit 1
fi

if flock "$release_root/.promote.lock" sh -c '"$@"' sh \
	env PATH="$fake_bin:$PATH" TEST_ROOT="$test_root" TEST_COMMAND_LOG="$test_root/commands.log" \
	HANK_LINUX_RELEASES_ROOT="$release_root" HANK_SERVER_REPO="$server_repo" HANK_SERVER_ENV_FILE="$server_repo/.env.cloud" \
	HANK_LINUX_RELEASE_EXPECTED_HOSTNAME="$(hostname)" HANK_LINUX_RELEASE_READY_ATTEMPTS=1 HANK_LINUX_RELEASE_VERIFY_COMMAND="$fake_bin/verifier" \
	HANK_LINUX_AGENT_COMMAND="$fake_bin/hankagent" HANK_LINUX_RELEASE_ACCEPTANCE_COMMAND="$fake_bin/acceptance" \
	"$promoter" 0.3.1 < "$test_root/release-0.3.1.tar" >/dev/null 2>&1; then
	echo "concurrent promotion acquired a held lock" >&2
	exit 1
fi

printf '0.3.1\narchive' | HANK_LINUX_RELEASE_PROMOTER=/bin/echo "$local_command" | grep -q '^0.3.1$'
if printf '0.3.1; id\narchive' | HANK_LINUX_RELEASE_PROMOTER=/bin/echo "$local_command" >/dev/null 2>&1; then
	echo "local command accepted shell syntax" >&2
	exit 1
fi
fake_sudo="$fake_bin/sudo"
cat > "$fake_sudo" <<EOF
#!/bin/sh
test "\$1 \$2" = "-n /usr/local/sbin/hank-linux-release-local-command"
HANK_LINUX_RELEASE_PROMOTER=/bin/echo "$local_command"
EOF
chmod +x "$fake_sudo"
printf 'archive' | PATH="$fake_bin:$PATH" SSH_ORIGINAL_COMMAND='promote 0.3.1' "$forced" | grep -q '^0.3.1$'
if SSH_ORIGINAL_COMMAND='promote 0.3.1; id' HANK_LINUX_RELEASE_PROMOTER=/bin/echo "$forced" >/dev/null 2>&1; then
	echo "forced command accepted shell syntax" >&2
	exit 1
fi

make_release 0.5.0
printf '0.5.0\n' > "$test_root/agent-version"
apply_count=$(grep -c 'hankagent --system update apply' "$test_root/commands.log")
run_promote 0.5.0
test "$(grep -c 'hankagent --system update apply' "$test_root/commands.log")" -eq "$apply_count"

echo "atomic Linux release promotion tests passed"
