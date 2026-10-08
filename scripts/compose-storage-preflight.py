#!/usr/bin/env python3
"""Validate and report Hank's effective Compose storage topology."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


SAFE_VOLUME_NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_.-]*$")


class PreflightError(ValueError):
    pass


def _mount_source(config: dict, service_name: str, target: str) -> str:
    services = config.get("services")
    if not isinstance(services, dict) or service_name not in services:
        raise PreflightError(f"required Compose service {service_name!r} is missing")
    service = services[service_name]
    matches = [
        mount
        for mount in service.get("volumes", [])
        if isinstance(mount, dict) and mount.get("target") == target
    ]
    if len(matches) != 1:
        raise PreflightError(
            f"service {service_name!r} must have exactly one mount at {target!r}"
        )
    mount = matches[0]
    if mount.get("type") != "volume":
        raise PreflightError(
            f"service {service_name!r} mount {target!r} must be a named volume"
        )
    logical_name = mount.get("source")
    if not isinstance(logical_name, str) or not logical_name:
        raise PreflightError(
            f"service {service_name!r} mount {target!r} has no volume source"
        )
    volume = config.get("volumes", {}).get(logical_name, {})
    actual_name = volume.get("name", logical_name) if isinstance(volume, dict) else logical_name
    if not isinstance(actual_name, str) or not SAFE_VOLUME_NAME.fullmatch(actual_name):
        raise PreflightError(
            f"service {service_name!r} mount {target!r} resolves to an unsafe volume name"
        )
    return actual_name


def validate_storage_topology(config: dict) -> dict[str, str]:
    postgres_live = _mount_source(config, "postgres", "/var/lib/postgresql")
    dbops_live = _mount_source(config, "db-ops", "/var/lib/postgresql")
    dbops_restore = _mount_source(config, "db-ops", "/var/lib/postgresql/restore")
    postgres_restore = _mount_source(
        config, "postgres-restore", "/var/lib/postgresql/restore"
    )

    postgres_repo = _mount_source(config, "postgres", "/var/lib/pgbackrest")
    dbops_repo = _mount_source(config, "db-ops", "/var/lib/pgbackrest")
    restore_repo = _mount_source(config, "postgres-restore", "/var/lib/pgbackrest")

    cloud_attachments = _mount_source(
        config, "cloud", "/var/lib/hank/note-attachments"
    )
    dbops_attachments = _mount_source(
        config, "db-ops", "/var/lib/hank/note-attachments"
    )
    attachment_restore = _mount_source(
        config, "db-ops", "/var/lib/hank/note-attachments-restore"
    )

    if postgres_live != dbops_live:
        raise PreflightError("postgres and db-ops do not share the live PostgreSQL volume")
    if dbops_restore != postgres_restore:
        raise PreflightError(
            "db-ops and postgres-restore do not share the restore PostgreSQL volume"
        )
    if postgres_live == postgres_restore:
        raise PreflightError("live and restore PostgreSQL volumes must be different")
    if len({postgres_repo, dbops_repo, restore_repo}) != 1:
        raise PreflightError(
            "postgres, db-ops, and postgres-restore do not share the pgBackRest repository"
        )
    if cloud_attachments != dbops_attachments:
        raise PreflightError("cloud and db-ops do not share the live attachment volume")
    if cloud_attachments == attachment_restore:
        raise PreflightError("live and restore attachment volumes must be different")

    return {
        "postgres_live": postgres_live,
        "postgres_restore": postgres_restore,
        "pgbackrest_repo": postgres_repo,
        "attachments_live": cloud_attachments,
        "attachments_restore": attachment_restore,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("config", type=Path, help="docker compose config --format json output")
    parser.add_argument(
        "--values", action="store_true", help="print validated volume names in stable order"
    )
    args = parser.parse_args()
    try:
        config = json.loads(args.config.read_text(encoding="utf-8"))
        topology = validate_storage_topology(config)
    except (OSError, json.JSONDecodeError, PreflightError) as exc:
        print(f"storage topology preflight failed: {exc}", file=sys.stderr)
        return 1
    if args.values:
        for key in (
            "postgres_live",
            "postgres_restore",
            "pgbackrest_repo",
            "attachments_live",
            "attachments_restore",
        ):
            print(topology[key])
    else:
        print(json.dumps(topology, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
