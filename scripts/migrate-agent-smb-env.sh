#!/usr/bin/env bash
set -euo pipefail

env_file="${1:-.env.agent}"

if [ ! -f "$env_file" ]; then
	echo "agent env file not found: $env_file" >&2
	exit 1
fi

python3 - "$env_file" <<'PY'
from pathlib import Path
import json
import shlex
import shutil
import sys

path = Path(sys.argv[1])
legacy_keys = {
    "HANK_SMB_HOST",
    "HANK_SMB_SHARE",
    "HANK_SMB_USERNAME",
    "HANK_SMB_PASSWORD",
    "HANK_SMB_DOMAIN",
}
json_key = "HANK_SMB_SHARES_JSON"

def parse_value(raw: str) -> str:
    raw = raw.strip()
    if not raw:
        return ""
    try:
        parsed = shlex.split(raw, comments=False, posix=True)
    except ValueError:
        return raw.strip("'\"")
    return parsed[0] if parsed else ""

lines = path.read_text().splitlines()
values = {}
kept = []
existing_json = ""

for line in lines:
    stripped = line.strip()
    if not stripped or stripped.startswith("#") or "=" not in stripped:
        kept.append(line)
        continue
    key, raw = stripped.split("=", 1)
    if key in legacy_keys:
        values[key] = parse_value(raw)
        continue
    if key == json_key:
        existing_json = parse_value(raw)
        continue
    kept.append(line)

shares_json = existing_json
if not shares_json:
    host = values.get("HANK_SMB_HOST", "")
    share = values.get("HANK_SMB_SHARE", "")
    if host or share:
        shares_json = json.dumps([{
            "id": "smb",
            "name": share or "SMB",
            "host": host,
            "share": share,
            "username": values.get("HANK_SMB_USERNAME", ""),
            "password": values.get("HANK_SMB_PASSWORD", ""),
            "domain": values.get("HANK_SMB_DOMAIN", ""),
        }], separators=(",", ":"))

backup = path.with_suffix(path.suffix + ".legacy-smb.bak")
shutil.copy2(path, backup)

while kept and kept[-1].strip() == "":
    kept.pop()
if shares_json:
    kept.extend(["", f"{json_key}={shares_json}"])
path.write_text("\n".join(kept) + "\n")
print(f"wrote {path}; backup is {backup}")
PY

chmod 600 "$env_file"
