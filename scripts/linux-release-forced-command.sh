#!/bin/sh
set -eu

set -- ${SSH_ORIGINAL_COMMAND:-}
if [ "$#" -ne 2 ] || [ "$1" != promote ] || ! printf '%s\n' "$2" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "deployment command rejected" >&2
	exit 64
fi
version=$2

{ printf '%s\n' "$version"; cat; } | exec sudo -n /usr/local/sbin/hank-linux-release-local-command
