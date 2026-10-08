#!/usr/bin/env sh
set -eu

REPO_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
export PATH="$REPO_DIR/bin:$PATH"

echo "Local docker shims enabled for this shell."
echo "PATH starts with: $REPO_DIR/bin"
echo "docker -> $(command -v docker)"
echo "docker-compose -> $(command -v docker-compose)"
