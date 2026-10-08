#!/usr/bin/env python3
"""Exercise the credential writer in the pinned Prometheus image with fake input."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

repo = Path(__file__).resolve().parents[2]
docker = shutil.which("docker")
if not docker:
    raise SystemExit("docker is required for the credential helper test")

with tempfile.TemporaryDirectory(prefix="hank-monitoring-setup-test-") as tmp:
    root = Path(tmp)
    (root / "scripts").mkdir()
    (root / "bin").mkdir()
    shutil.copy(repo / "scripts/configure-monitoring.sh", root / "scripts/configure-monitoring.sh")
    for name in ("prometheus/prometheus.yml", "prometheus/alerts.yml", "alertmanager/alertmanager.yml"):
        destination = root / "ops" / name
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy(repo / "ops" / name, destination)
        destination.chmod(0o600)
    fake = root / "bin/docker"
    fake.write_text('''#!/usr/bin/env bash
if [[ "$1" == compose ]]; then
  if [[ "${!#}" == HANK_ALERTMANAGER_WEBHOOK_TOKEN ]]; then
    printf "%s" "$FIXTURE_INBOX_TOKEN"
  else
    printf "%s" "$FIXTURE_TOKEN"
  fi
  exit "${FIXTURE_SOURCE_EXIT:-0}"
fi
exec "$REAL_DOCKER" "$@"
''')
    fake.chmod(0o755)
    env = dict(os.environ, PATH=str(root / "bin") + ":" + os.environ["PATH"],
               REAL_DOCKER=docker, FIXTURE_TOKEN="fixture-only-$(touch-do-not-execute)")

    def run(*args):
        return subprocess.run(["bash", "-x", str(root / "scripts/configure-monitoring.sh"), *args],
                              env=env, capture_output=True, text=True, timeout=60)

    def inspect(directory="prometheus", name="metrics-token"):
        return subprocess.check_output([
            docker, "run", "--rm", "--network", "none", "--user", "0:0", "--entrypoint", "sh",
            "--mount", f"type=bind,src={root}/ops/{directory}/secrets,dst=/secrets,readonly",
            "prom/prometheus:v3.5.0", "-c",
            'stat -c "%a %u %g" "/secrets/$1"; cat "/secrets/$1"', 'sh', name,
        ], text=True, timeout=30)

    result = run()
    assert result.returncode == 0, "credential helper failed"
    assert env["FIXTURE_TOKEN"] not in result.stdout + result.stderr, "credential leaked"
    before = inspect()
    assert before == "400 65534 65534\n" + env["FIXTURE_TOKEN"], "wrong bytes or permissions"
    for name in ("prometheus/prometheus.yml", "prometheus/alerts.yml", "alertmanager/alertmanager.yml"):
        st = (root / "ops" / name).stat()
        assert st.st_uid == os.getuid() and st.st_gid == 65534 and st.st_mode & 0o777 == 0o640
    env["FIXTURE_INBOX_TOKEN"] = "inbox-fixture-$(do-not-execute)"
    result = run("--inbox")
    assert result.returncode == 0, result.stderr
    assert env["FIXTURE_INBOX_TOKEN"] not in result.stdout + result.stderr
    inbox_before = inspect("alertmanager", "hank-inbox-token")
    assert inbox_before == "400 65534 65534\n" + env["FIXTURE_INBOX_TOKEN"]
    env["FIXTURE_INBOX_TOKEN"] = env["FIXTURE_TOKEN"]
    assert run("--inbox").returncode != 0, "shared read/write credential accepted"
    assert inspect("alertmanager", "hank-inbox-token") == inbox_before
    env["FIXTURE_TOKEN"] = ""
    assert run().returncode != 0, "empty credential accepted"
    assert inspect() == before, "empty source replaced existing credential"
    env.update(FIXTURE_TOKEN="partial-fixture-value", FIXTURE_SOURCE_EXIT="1")
    assert run().returncode != 0, "failed source accepted"
    assert inspect() == before, "failed source replaced existing credential"

print("Credential helper: opaque bytes, permissions, redaction, and failure preservation passed.")
