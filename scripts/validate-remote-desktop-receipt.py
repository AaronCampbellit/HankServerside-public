#!/usr/bin/env python3
"""Strict metadata-only receipt validation for native Remote Desktop gates."""

import argparse
import json
import sys


RESERVED = {"status", "native_evidence", "platform", "requirement", "scenario"}


class ReceiptError(ValueError):
    pass


def reject_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ReceiptError(f"duplicate field: {key}")
        result[key] = value
    return result


def reject_nested_reserved(value, root=True):
    if isinstance(value, dict):
        if not root:
            conflict = RESERVED.intersection(value)
            if conflict:
                raise ReceiptError(f"reserved field must be top-level: {sorted(conflict)[0]}")
        for child in value.values():
            reject_nested_reserved(child, False)
    elif isinstance(value, list):
        for child in value:
            reject_nested_reserved(child, False)


def validate(raw, kind, name, platform):
    try:
        value = json.loads(raw, object_pairs_hook=reject_duplicate_keys)
    except (json.JSONDecodeError, ReceiptError) as error:
        raise ReceiptError(f"invalid JSON: {error}") from error
    if not isinstance(value, dict):
        raise ReceiptError("receipt root must be an object")
    reject_nested_reserved(value)
    other = "scenario" if kind == "requirement" else "requirement"
    if other in value:
        raise ReceiptError(f"contradictory field: {other}")
    if value.get(kind) != name or not isinstance(value.get(kind), str):
        raise ReceiptError(f"{kind} mismatch")
    if value.get("status") != "pass" or not isinstance(value.get("status"), str):
        raise ReceiptError("status is not pass")
    if value.get("native_evidence") is not True:
        raise ReceiptError("native_evidence must be boolean true")
    actual_platform = value.get("platform")
    if not isinstance(actual_platform, str):
        raise ReceiptError("platform must be a string")
    if platform == "any-native":
        if actual_platform not in {"windows", "macos"}:
            raise ReceiptError("platform must be windows or macos")
    elif actual_platform != platform:
        raise ReceiptError("platform mismatch")
    return value


def self_test():
    valid = '{"status":"pass","native_evidence":true,"platform":"windows","scenario":"slow_consumer"}'
    validate(valid, "scenario", "slow_consumer", "windows")
    spoofs = [
        '{"status":"fail","status":"pass","native_evidence":true,"platform":"windows","scenario":"slow_consumer"}',
        '{"status":"pass","native_evidence":false,"native_evidence":true,"platform":"windows","scenario":"slow_consumer"}',
        '{"status":"pass","native_evidence":true,"platform":"macos","platform":"windows","scenario":"slow_consumer"}',
        '{"status":"pass","native_evidence":true,"platform":"windows","scenario":"wrong","scenario":"slow_consumer"}',
        '{"status":"pass","native_evidence":true,"platform":"windows","scenario":"slow_consumer","requirement":"slow_consumer"}',
        '{"status":"pass","native_evidence":"true","platform":"windows","scenario":"slow_consumer"}',
        '{"status":"pass","native_evidence":true,"platform":"windows","scenario":"slow_consumer","details":{"status":"fail"}}',
        valid + ' {"status":"fail"}',
    ]
    for spoof in spoofs:
        try:
            validate(spoof, "scenario", "slow_consumer", "windows")
        except ReceiptError:
            continue
        raise ReceiptError(f"spoof accepted: {spoof}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--kind", choices=("requirement", "scenario"))
    parser.add_argument("--name")
    parser.add_argument("--platform")
    args = parser.parse_args()
    if args.self_test:
        self_test()
        return
    if not args.kind or not args.name or not args.platform:
        parser.error("--kind, --name, and --platform are required")
    try:
        value = validate(sys.stdin.read(), args.kind, args.name, args.platform)
    except ReceiptError as error:
        print(f"invalid native receipt: {error}", file=sys.stderr)
        raise SystemExit(1)
    print(json.dumps(value, separators=(",", ":"), sort_keys=True))


if __name__ == "__main__":
    main()
