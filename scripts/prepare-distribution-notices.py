#!/usr/bin/env python3
"""Carry reviewed notices and exact MPL-covered source with Go distributions."""

from pathlib import Path
import argparse
import shutil

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("output", type=Path)
args = parser.parse_args()
output = args.output.resolve()
if output == root or root.is_relative_to(output):
    parser.error("output must be a distribution directory, not the source root or its parent")
output.mkdir(parents=True, exist_ok=True)
for name in ("LICENSE", "THIRD_PARTY_NOTICES.md"):
    shutil.copy2(root / name, output / name)
for name in ("third-party-licenses", "third-party-source"):
    shutil.copytree(root / name, output / name, dirs_exist_ok=True)
(output / "THIRD_PARTY_SOURCE.txt").write_text(
    "Hank Go distribution — third-party source access\n\n"
    "github.com/hashicorp/go-uuid v1.0.3 is covered by MPL-2.0. Its exact, "
    "unmodified source and license accompany this distribution at "
    "third-party-source/go/github.com__hashicorp__go-uuid/v1.0.3/.\n"
    "Upstream: https://github.com/hashicorp/go-uuid/tree/v1.0.3\n"
    "Separate original Hank material remains rights reserved. This does not "
    "restrict recipients' MPL rights to the covered files.\n"
    "See THIRD_PARTY_NOTICES.md and third-party-licenses/ for additional "
    "dependency terms and the bounded inventory scope.\n",
    encoding="utf-8",
)
