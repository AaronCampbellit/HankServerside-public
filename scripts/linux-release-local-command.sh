#!/bin/sh
set -eu

IFS= read -r version
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
	echo "deployment version rejected" >&2
	exit 64
fi

promoter=${HANK_LINUX_RELEASE_PROMOTER:-/usr/local/sbin/hank-linux-release-promote}
exec "$promoter" "$version"
