#!/usr/bin/env python3
"""Fail unless every Bazel shard ran exactly the tests its shard script lists.

Usage: check_shard_coverage.py --bep <build_event_json_file> [--testlogs DIR]
                               --suite LABEL SCRIPT SHARDS [--suite ...]

The manifest-sharded targets (//cmd/bd:bd_embedded_test runs
.github/scripts/embedded-test-shard.sh, ...) discover their tests from
source with grep, but the Bazel test binary holds only the files in the
go_test's srcs. A discovered test the binary lacks matches nothing in the
shard's -test.run selector and passes silently, and check_testcases.py only
rejects a shard with no tests at all.

For each --suite this runs SCRIPT k SHARDS with BEADS_TEST_SHARD_LIST_ONLY=1
(from the repository root, like the CI jobs) for k = 1..SHARDS, and compares
the names it lists with the top-level <testcase> names in the test.xml of
Bazel shard k of LABEL in this invocation's BEP. Any test listed but absent
(or present but not listed), a shard count other than SHARDS, a missing
test.xml or a failing script is an error. It needs test.xml locally and
-test.v in it, as --config=embedded sets.

Exit status: 0 if every shard matches, 1 otherwise.
"""

import argparse
import os
import re
import subprocess
import sys
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from equivalence import read_bep, testlog_xmls  # noqa: E402

LISTED = re.compile(r"^  (Test[A-Za-z0-9_]*)$")


def listed_tests(script, shard, shards):
    """Return the set of tests SCRIPT assigns to shard SHARD of SHARDS."""
    env = dict(os.environ, BEADS_TEST_SHARD_LIST_ONLY="1")
    out = subprocess.run(
        ["bash", script, str(shard), str(shards)],
        env=env, check=True, capture_output=True, text=True,
    ).stdout
    return {m.group(1) for m in map(LISTED.match, out.splitlines()) if m}


def ran_tests(xml_path):
    """Return the set of top-level Go tests in a test.xml."""
    names = set()
    for tc in ET.parse(xml_path).getroot().iter("testcase"):
        name = tc.get("name", "")
        if name.startswith("Test") and "/" not in name:
            names.add(name)
    return names


def check(tested, testlogs, suites, lister=listed_tests):
    """Return ([summary lines], [problems]) for suites of (label, script, shards)."""
    lines, problems = [], []
    for label, script, shards in suites:
        if tested.get(label) != shards:
            problems.append(f"{label}: the BEP has {tested.get(label, 0)} shard(s), want {shards} ({script})")
            continue
        total = 0
        for k, xml_path in enumerate(testlog_xmls(testlogs, label, shards), start=1):
            try:
                want = lister(script, k, shards)
            except (OSError, subprocess.CalledProcessError) as e:
                problems.append(f"{label}: {script} {k} {shards} failed: {e}")
                continue
            rel = os.path.relpath(xml_path, testlogs)
            if not os.path.exists(xml_path):
                problems.append(f"{label}: {rel} missing (was test.xml downloaded?)")
                continue
            try:
                got = ran_tests(xml_path)
            except ET.ParseError as e:
                problems.append(f"{label}: cannot parse {rel}: {e}")
                continue
            total += len(want)
            for name in sorted(want - got):
                problems.append(f"{label} shard {k}/{shards}: {name} is listed by {script} but did not run "
                                f"(not in the target's srcs?)")
            for name in sorted(got - want):
                problems.append(f"{label} shard {k}/{shards}: {name} ran but {script} does not list it there")
        lines.append(f"{label}: {shards} shards, {total} listed tests")
    return lines, problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--bep", required=True, help="--build_event_json_file of the bazel test run")
    ap.add_argument("--testlogs", default=None, help="default: from the BEP, else ./bazel-testlogs")
    ap.add_argument("--suite", nargs=3, action="append", required=True, metavar=("LABEL", "SCRIPT", "SHARDS"),
                    help="a manifest-sharded target, its shard script and shard count (repeatable)")
    args = ap.parse_args(argv)

    suites = []
    for label, script, shards in args.suite:
        if not shards.isdigit() or int(shards) < 1:
            ap.error(f"--suite {label}: SHARDS must be a positive integer, got {shards!r}")
        suites.append((label, script, int(shards)))
    tested, _, bep_testlogs = read_bep(args.bep)
    testlogs = args.testlogs or bep_testlogs or "bazel-testlogs"
    lines, problems = check(tested, testlogs, suites)
    for line in lines:
        print(line)
    for p in problems:
        print(f"FAIL: {p}", file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
