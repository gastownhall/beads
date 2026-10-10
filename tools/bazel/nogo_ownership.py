#!/usr/bin/env python3
"""Dynamic cross-check for the nogo-ownership policy (scripts/nogo_lint_policy_test.go).

The static policy tests (T1-T3, T5) pin .bazelrc/bazel.yml *text*: a config's
flags, and which lane owns its validations. This script instead asks Bazel
itself, via `aquery`, whether the owner's RunNogo action set for a
configuration really is a superset of every non-owner's RunNogo action set
for that same configuration -- i.e. the owner genuinely analyzes every
first-party file a non-owner compiles, so skipping validation on the
non-owner loses no lint coverage.

Scope (S1): the race configuration group only -- bazel-test (owner, race)
vs. embedded/doltserver/doltserver-proxied/dolt-race (non-owners, which pass
--norun_validations). The integration group (S2) is out of scope until its
lanes gain the same split; see scripts/nogo_lint_policy_test.go's
nogoConfigurations table, which this script's RACE_GROUP mirrors.

Each config's key-affecting flags (the same fingerprint
scripts/nogo_lint_policy_test.go's nogoKeyFlagPattern extracts) are read
straight out of .bazelrc and passed to `aquery` directly: aquery is a
"build"-family command and does not see flags defined under a bare `test:X`
.bazelrc stanza, so `--config=X` itself cannot be used here.

This is advisory and dynamic (runs real `bazel aquery`, about 2 min
locally): it is intentionally not part of the required `//scripts` static
test suite, which cannot shell out to Bazel from inside a Bazel action. Run
it with `make nogo-ownership`, locally or on demand in a slice's own PR.

Exit status: 0 if every non-owner's RunNogo target set is covered by the
owner's, 2 otherwise. Prints the offending targets.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

# (name, owner config, [non-owner config, ...]) for the race group.
# Keep in sync with scripts/nogo_lint_policy_test.go's nogoConfigurations.
RACE_GROUP = (
    "race",
    "ci",
    ["embedded", "doltserver", "doltserver-proxied", "dolt-race"],
)

# Mirrors scripts/nogo_lint_policy_test.go's nogoKeyFlagPattern.
KEY_FLAG_RE = re.compile(
    r"--@rules_go//go/config:(?:race|pure|tags=\S+)"
    r"|--platforms=\S+"
    r"|//tools/bazel:release_platforms\S*"
)

CONFIG_LINE_RE = re.compile(r"^(build|test):([A-Za-z0-9_-]+)\s+(.*)$")


def bazelrc_lines(rc_text: str, name: str) -> list[str]:
    """Every raw ("build"|"test", opt) line for --config=name, in file order."""
    out = []
    for line in rc_text.splitlines():
        line = line.split(" #", 1)[0].rstrip()
        m = CONFIG_LINE_RE.match(line)
        if m and m.group(2) == name:
            out.append(f"{m.group(1)} {m.group(3)}")
    return out


def expand_config(rc_text: str, name: str, seen: set[str]) -> list[str]:
    """Recursively follows nested --config=Y references, as the Go test does."""
    if name in seen:
        return []
    seen.add(name)
    out = []
    for line in bazelrc_lines(rc_text, name):
        for prefix in ("build --config=", "test --config="):
            if line.startswith(prefix):
                out.extend(expand_config(rc_text, line[len(prefix):], seen))
                break
        else:
            out.append(line)
    return out


def fingerprint(rc_text: str, name: str) -> list[str]:
    lines = expand_config(rc_text, name, set())
    flags = set()
    for line in lines:
        flags.update(m.group(0) for m in KEY_FLAG_RE.finditer(line))
    return sorted(flags)


def run_nogo_targets(bazel: str, flags: list[str]) -> set[str]:
    """Returns the set of target labels with a RunNogo action under flags."""
    cmd = [bazel, "--nohome_rc", "aquery", 'mnemonic("RunNogo", //...)', *flags,
           "--output=jsonproto"]
    out = subprocess.run(cmd, capture_output=True, text=True, check=True).stdout
    data = json.loads(out)
    targets_by_id = {t["id"]: t["label"] for t in data.get("targets", [])}
    labels = set()
    for action in data.get("actions", []):
        if action.get("mnemonic") != "RunNogo":
            continue
        label = targets_by_id.get(action.get("targetId"))
        if label and not label.startswith("@"):
            labels.add(label)
    return labels


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bazel", default="bazel", help="bazel/bazelisk binary to invoke")
    parser.add_argument("--bazelrc", default=".bazelrc")
    args = parser.parse_args()

    rc_text = Path(args.bazelrc).read_text()
    name, owner, non_owners = RACE_GROUP

    owner_fp = fingerprint(rc_text, owner)
    owner_targets = run_nogo_targets(args.bazel, owner_fp)

    problems = []
    for non_owner in non_owners:
        non_owner_fp = fingerprint(rc_text, non_owner)
        if non_owner_fp != owner_fp:
            problems.append(
                f"{name}: --config={non_owner}'s fingerprint {non_owner_fp} "
                f"differs from owner --config={owner}'s {owner_fp}; it is no "
                "longer safe to skip its validations"
            )
            continue
        non_owner_targets = run_nogo_targets(args.bazel, non_owner_fp)
        missing = non_owner_targets - owner_targets
        if missing:
            problems.append(
                f"{name}: --config={non_owner} compiles {len(missing)} target(s) "
                f"with no RunNogo action under the owner --config={owner}: "
                + ", ".join(sorted(missing)[:10])
                + (" ..." if len(missing) > 10 else "")
            )

    if problems:
        print("nogo_ownership: FAIL", file=sys.stderr)
        for p in problems:
            print(f"  {p}", file=sys.stderr)
        return 2

    print(f"nogo_ownership: OK ({name}: --config={owner} covers "
          f"{', '.join(non_owners)})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
