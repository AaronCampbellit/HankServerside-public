#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
bootstrap_script="$repo_root/scripts/bootstrap-first-run.sh"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

env_value() {
  local env_file="$1"
  local key="$2"
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$env_file"
}

new_fixture() {
  local fixture_root="$1"
  mkdir -p "$fixture_root/repo/scripts" "$fixture_root/bin"
  cp "$bootstrap_script" "$fixture_root/repo/scripts/bootstrap-first-run.sh"
  ln -s repo "$fixture_root/HankServerside"
  cat >"$fixture_root/repo/scripts/migrate-hank-naming.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "$fixture_root/repo/scripts/migrate-hank-naming.sh"

  cat >"$fixture_root/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
if [[ " $* " == *" web-push-keys "* ]]; then
  if [[ " $* " != *" --no-deps "* ]]; then
    printf 'dependency-started\n' >>"$FAKE_DOCKER_LOG"
  fi
  if [[ -n "${FAKE_WEB_PUSH_OUTPUT:-}" ]]; then
    printf '%s\n' "$FAKE_WEB_PUSH_OUTPUT"
  else
    printf 'HANK_WEB_PUSH_VAPID_PUBLIC_KEY=test-public-key\n'
    printf 'HANK_WEB_PUSH_VAPID_PRIVATE_KEY=test-private-key\n'
  fi
fi
EOF
  chmod +x "$fixture_root/bin/docker"

  cat >"$fixture_root/bin/curl" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "$fixture_root/bin/curl"
}

run_bootstrap() {
  local fixture_root="$1"
  shift
  (
    cd "$fixture_root/repo"
    env \
      PATH="$fixture_root/bin:$PATH" \
      FAKE_DOCKER_LOG="$fixture_root/docker.log" \
      HANK_BOOTSTRAP_NONINTERACTIVE=true \
      HANK_BOOTSTRAP_PUBLIC_BASE_URL=https://hank.example.com \
      HANK_BOOTSTRAP_POSTGRES_PASSWORD=test-db-password \
      HANK_BOOTSTRAP_SECRET_ENCRYPTION_KEY=test-secret-key \
      HANK_BOOTSTRAP_DB_OPS_INTENT_SECRET=test-intent-secret \
      HANK_BOOTSTRAP_DB_OPS_REPO_CIPHER_PASS=test-repo-cipher \
      HANK_NAMING_NEW_ROOT="$fixture_root" \
      "$@" \
      scripts/bootstrap-first-run.sh
  ) >"$fixture_root/output.log" 2>&1
}

test_fresh_bootstrap_generates_web_push_configuration_without_logging_private_key() {
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' RETURN
  new_fixture "$fixture_root"

  run_bootstrap "$fixture_root"

  local env_file="$fixture_root/repo/.env.cloud"
  [[ "$(env_value "$env_file" HANK_PUBLIC_BASE_URL)" == "https://hank.example.com" ]] || fail "public base URL was not persisted"
  grep -q '^HANK_OLLAMA_BASE_URL=$' "$env_file" || fail "fresh bootstrap configured an Ollama service that does not exist"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_PUBLIC_KEY)" == "test-public-key" ]] || fail "VAPID public key was not persisted"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_PRIVATE_KEY)" == "test-private-key" ]] || fail "VAPID private key was not persisted"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_SUBJECT)" == "https://hank.example.com" ]] || fail "VAPID subject was not derived from the public URL"
  if grep -q 'test-private-key' "$fixture_root/output.log"; then
    fail "bootstrap logged the VAPID private key"
  fi
  if grep -q 'dependency-started' "$fixture_root/docker.log"; then
    fail "VAPID generation started Compose dependencies"
  fi
}

write_existing_env() {
  local env_file="$1"
  cat >"$env_file" <<'EOF'
HANK_CLOUD_HOST_BIND=127.0.0.1
HANK_CLOUD_HOST_PORT=18080
POSTGRES_DB=hank
POSTGRES_USER=hank
POSTGRES_PASSWORD=existing-db-password
HANK_PUBLIC_BASE_URL=https://existing.hank.example
HANK_DB_OPS_REPO_CIPHER_PASS=existing-repo-cipher
EOF
  chmod 600 "$env_file"
}

test_existing_web_push_configuration_is_preserved() {
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' RETURN
  new_fixture "$fixture_root"
  write_existing_env "$fixture_root/repo/.env.cloud"
  cat >>"$fixture_root/repo/.env.cloud" <<'EOF'
HANK_WEB_PUSH_VAPID_PUBLIC_KEY=existing-public-key
HANK_WEB_PUSH_VAPID_PRIVATE_KEY=existing-private-key
HANK_WEB_PUSH_VAPID_SUBJECT=https://existing.hank.example
EOF

  run_bootstrap "$fixture_root"

  local env_file="$fixture_root/repo/.env.cloud"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_PUBLIC_KEY)" == "existing-public-key" ]] || fail "existing VAPID public key was replaced"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_PRIVATE_KEY)" == "existing-private-key" ]] || fail "existing VAPID private key was replaced"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_SUBJECT)" == "https://existing.hank.example" ]] || fail "existing VAPID subject was replaced"
  if grep -q 'web-push-keys' "$fixture_root/docker.log"; then
    fail "bootstrap generated a new VAPID pair for a configured server"
  fi
}

test_partial_web_push_configuration_fails_before_service_changes() {
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' RETURN
  new_fixture "$fixture_root"
  write_existing_env "$fixture_root/repo/.env.cloud"
  printf 'HANK_WEB_PUSH_VAPID_PUBLIC_KEY=orphaned-public-key\n' >>"$fixture_root/repo/.env.cloud"

  if run_bootstrap "$fixture_root"; then
    fail "bootstrap accepted partial VAPID configuration"
  fi
  if grep -Eq ' (build|up|exec|run) ' "$fixture_root/docker.log"; then
    fail "bootstrap changed services before rejecting partial VAPID configuration"
  fi
  grep -q 'public key, private key, and subject together' "$fixture_root/output.log" || fail "partial configuration error was not actionable"
}

test_incomplete_generator_output_does_not_modify_environment() {
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' RETURN
  new_fixture "$fixture_root"

  if run_bootstrap "$fixture_root" FAKE_WEB_PUSH_OUTPUT=HANK_WEB_PUSH_VAPID_PUBLIC_KEY=orphaned-public-key; then
    fail "bootstrap accepted incomplete VAPID generator output"
  fi

  local env_file="$fixture_root/repo/.env.cloud"
  [[ -z "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_PUBLIC_KEY)" ]] || fail "incomplete public key was persisted"
  [[ -z "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_PRIVATE_KEY)" ]] || fail "empty private key entry was persisted"
  [[ -z "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_SUBJECT)" ]] || fail "subject was persisted after generator failure"
}

test_blank_web_push_placeholders_are_replaced_without_duplicates() {
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' RETURN
  new_fixture "$fixture_root"
  write_existing_env "$fixture_root/repo/.env.cloud"
  cat >>"$fixture_root/repo/.env.cloud" <<'EOF'
HANK_WEB_PUSH_VAPID_PUBLIC_KEY=
HANK_WEB_PUSH_VAPID_PRIVATE_KEY=
HANK_WEB_PUSH_VAPID_SUBJECT=
EOF

  run_bootstrap "$fixture_root"

  local env_file="$fixture_root/repo/.env.cloud"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_PUBLIC_KEY)" == "test-public-key" ]] || fail "blank VAPID public-key placeholder was not replaced"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_PRIVATE_KEY)" == "test-private-key" ]] || fail "blank VAPID private-key placeholder was not replaced"
  [[ "$(env_value "$env_file" HANK_WEB_PUSH_VAPID_SUBJECT)" == "https://existing.hank.example" ]] || fail "blank VAPID subject placeholder was not replaced"
  [[ "$(grep -c '^HANK_WEB_PUSH_VAPID_PUBLIC_KEY=' "$env_file")" == "1" ]] || fail "VAPID public key was duplicated"
  [[ "$(grep -c '^HANK_WEB_PUSH_VAPID_PRIVATE_KEY=' "$env_file")" == "1" ]] || fail "VAPID private key was duplicated"
  [[ "$(grep -c '^HANK_WEB_PUSH_VAPID_SUBJECT=' "$env_file")" == "1" ]] || fail "VAPID subject was duplicated"
}

test_invalid_public_url_fails_before_build_or_key_generation() {
  local fixture_root
  fixture_root="$(mktemp -d)"
  trap 'rm -rf "$fixture_root"' RETURN
  new_fixture "$fixture_root"

  if run_bootstrap "$fixture_root" HANK_BOOTSTRAP_PUBLIC_BASE_URL=https://; then
    fail "bootstrap accepted a public URL without a host"
  fi
  if grep -Eq ' (build|up|exec|run) ' "$fixture_root/docker.log"; then
    fail "bootstrap changed services after rejecting the public URL"
  fi
  grep -q 'public HTTPS URL' "$fixture_root/output.log" || fail "invalid public URL error was not actionable"
}

test_fresh_bootstrap_generates_web_push_configuration_without_logging_private_key
test_existing_web_push_configuration_is_preserved
test_partial_web_push_configuration_fails_before_service_changes
test_incomplete_generator_output_does_not_modify_environment
test_blank_web_push_placeholders_are_replaced_without_duplicates
test_invalid_public_url_fails_before_build_or_key_generation
printf 'PASS: bootstrap first-run tests\n'
