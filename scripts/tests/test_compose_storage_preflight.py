#!/usr/bin/env python3

from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


SCRIPT = Path(__file__).resolve().parents[1] / "compose-storage-preflight.py"
sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location("compose_storage_preflight", SCRIPT)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def volume(source: str, target: str) -> dict:
    return {"type": "volume", "source": source, "target": target}


def valid_config() -> dict:
    return {
        "services": {
            "postgres": {
                "volumes": [
                    volume("live", "/var/lib/postgresql"),
                    volume("repo", "/var/lib/pgbackrest"),
                ]
            },
            "db-ops": {
                "volumes": [
                    volume("live", "/var/lib/postgresql"),
                    volume("restore", "/var/lib/postgresql/restore"),
                    volume("repo", "/var/lib/pgbackrest"),
                    volume("attachments", "/var/lib/hank/note-attachments"),
                    volume("attachments-restore", "/var/lib/hank/note-attachments-restore"),
                ]
            },
            "postgres-restore": {
                "volumes": [
                    volume("restore", "/var/lib/postgresql/restore"),
                    volume("repo", "/var/lib/pgbackrest"),
                ]
            },
            "cloud": {
                "volumes": [volume("attachments", "/var/lib/hank/note-attachments")]
            },
        },
        "volumes": {
            "live": {"name": "site_live"},
            "restore": {"name": "site_restore"},
            "repo": {"name": "site_repo"},
            "attachments": {"name": "site_attachments"},
            "attachments-restore": {"name": "site_attachments_restore"},
        },
    }


class StorageTopologyTest(unittest.TestCase):
    def test_accepts_expected_live_restore_and_repository_pairs(self) -> None:
        self.assertEqual(
            MODULE.validate_storage_topology(valid_config()),
            {
                "postgres_live": "site_live",
                "postgres_restore": "site_restore",
                "pgbackrest_repo": "site_repo",
                "attachments_live": "site_attachments",
                "attachments_restore": "site_attachments_restore",
            },
        )

    def test_rejects_dbops_live_volume_mismatch(self) -> None:
        config = valid_config()
        config["services"]["db-ops"]["volumes"][0]["source"] = "restore"
        with self.assertRaisesRegex(MODULE.PreflightError, "live PostgreSQL"):
            MODULE.validate_storage_topology(config)

    def test_rejects_restore_service_volume_mismatch(self) -> None:
        config = valid_config()
        config["services"]["postgres-restore"]["volumes"][0]["source"] = "live"
        with self.assertRaisesRegex(MODULE.PreflightError, "restore PostgreSQL"):
            MODULE.validate_storage_topology(config)

    def test_rejects_shared_live_and_restore_volumes(self) -> None:
        config = valid_config()
        config["volumes"]["restore"]["name"] = "site_live"
        with self.assertRaisesRegex(MODULE.PreflightError, "must be different"):
            MODULE.validate_storage_topology(config)

    def test_rejects_attachment_and_repository_mismatches(self) -> None:
        config = valid_config()
        config["services"]["cloud"]["volumes"][0]["source"] = "attachments-restore"
        with self.assertRaisesRegex(MODULE.PreflightError, "live attachment"):
            MODULE.validate_storage_topology(config)


if __name__ == "__main__":
    unittest.main()
