#!/usr/bin/env bash
set -Eeuo pipefail

required_major=18
version_num="${1:-}"

if [[ ! "$version_num" =~ ^[0-9]{5,6}$ ]]; then
  printf 'invalid PostgreSQL server_version_num: expected 5 or 6 decimal digits\n' >&2
  exit 2
fi

numeric_version=$((10#$version_num))
actual_major=$((numeric_version / 10000))
patch_version=$((numeric_version % 10000))

if [ "$actual_major" -ne "$required_major" ]; then
  printf 'Hank requires PostgreSQL major version %d; server reports major version %d\n' "$required_major" "$actual_major" >&2
  exit 1
fi

printf 'PostgreSQL %d.%d satisfies Hank required major version %d\n' "$actual_major" "$patch_version" "$required_major"
