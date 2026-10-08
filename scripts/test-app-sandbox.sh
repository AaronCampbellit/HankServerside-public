#!/usr/bin/env bash
set -euo pipefail

# Disposable local security acceptance only. These outer container permissions
# allow nested confinement tests; they are never a production deployment profile.
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
sandbox_test_dir="$(mktemp -d)"
sandbox_test_suffix="$(basename "$sandbox_test_dir" | tr '[:upper:]' '[:lower:]')"
sandbox_runtime_image="hank-app-runtime-test:${sandbox_test_suffix}"
sandbox_test_image="hank-app-sandbox-test:${sandbox_test_suffix}"
cleanup() {
  if [ -f "$sandbox_test_dir/test" ]; then unlink "$sandbox_test_dir/test"; fi
  rmdir "$sandbox_test_dir"
  docker image rm "$sandbox_test_image" "$sandbox_runtime_image" >/dev/null 2>&1 || true
}
trap cleanup EXIT

CGO_ENABLED=0 GOOS=linux go test -c -o "$sandbox_test_dir/test" ./internal/agent/apps
docker build --target app-runtime -f Dockerfile.server -t "$sandbox_runtime_image" .
docker build -f ops/tests/Dockerfile.app-sandbox \
  --build-arg "APP_RUNTIME_IMAGE=$sandbox_runtime_image" -t "$sandbox_test_image" .
docker run --rm --network none --memory 1g --pids-limit 180 --cpus 2 \
  --cap-add SYS_ADMIN --security-opt apparmor=unconfined \
  --security-opt seccomp=unconfined --cgroupns private \
  -e HANK_TEST_APP_SANDBOX=1 \
  -v "$sandbox_test_dir/test:/test:ro" \
  -v "$repo_root/examples/restricted-app:/example:ro" \
  "$sandbox_test_image" sh -ceu '
    # This is the disposable container’s private cgroup subtree, bounded by
    # Docker above. No host cgroup or host filesystem is bind-mounted writable.
    mount -o remount,rw /sys/fs/cgroup
    mkdir /sys/fs/cgroup/runner
    echo $$ > /sys/fs/cgroup/runner/cgroup.procs
    echo +cpu +memory +pids > /sys/fs/cgroup/cgroup.subtree_control
    mkdir /sys/fs/cgroup/hank-apps
    echo +cpu +memory +pids > /sys/fs/cgroup/hank-apps/cgroup.subtree_control
    exec /test -test.run "TestSandbox|TestManagerLoadIsolates" -test.v
  '
