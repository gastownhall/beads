#!/usr/bin/env python3
"""Fail unless every Bazel shard ran exactly the tests its shard script lists.

Usage: check_shard_coverage.py --bep <build_event_json_file> [--testlogs DIR]
                               --suite LABEL SCRIPT SHARDS [--suite ...]
                               [--whole LABEL ...]

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
test.xml or a failing script is an error. So is a shard (or any test.xml of
a --whole LABEL, an unsharded target such as the conformance partitions)
whose top-level tests are all skipped: an injected -test.short,
BEADS_TEST_SKIP or a lost BEADS_TEST_EMBEDDED_DOLT=1 turns the tier into
t.Skip calls, which still list every test. It needs test.xml locally and
-test.v in it, as --config=embedded sets.

Exit status: 0 if every shard matches, 1 otherwise.
"""

import argparse
import concurrent.futures
import os
import re
import subprocess
import sys
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from equivalence import read_bep, testlog_xmls  # noqa: E402

LISTED = re.compile(r"^  (Test[A-Za-z0-9_]*)$")

# Names the shard scripts' `grep '^func Test'` discovery lists that go test
# never runs as a test: TestMain(m *testing.M) is the package's test entry
# point (internal/storage/embeddeddolt/test_fixture_test.go), never a
# <testcase>. The legacy jobs' -test.run selector harmlessly matches nothing
# for it. Filtered here rather than in the shard scripts so the legacy jobs'
# selection stays byte-for-byte what it was. scripts/
# pr_risk_bazel_coverage_test.go runs both real scripts in list-only mode and
# requires every other listed name to be a `func Name(t *testing.T)`.
NOT_TESTS = frozenset({"TestMain"})


def listed_tests(script, shard, shards):
    """Return the set of tests SCRIPT assigns to shard SHARD of SHARDS."""
    env = dict(os.environ, BEADS_TEST_SHARD_LIST_ONLY="1")
    out = subprocess.run(
        ["bash", script, str(shard), str(shards)],
        env=env, check=True, capture_output=True, text=True,
    ).stdout
    return {m.group(1) for m in map(LISTED.match, out.splitlines()) if m} - NOT_TESTS


def ran_tests(xml_path):
    """Return (set of top-level Go tests, set of those skipped) in a test.xml."""
    names, skipped = set(), set()
    for tc in ET.parse(xml_path).getroot().iter("testcase"):
        name = tc.get("name", "")
        if name.startswith("Test") and "/" not in name:
            names.add(name)
            if tc.find("skipped") is not None:
                skipped.add(name)
    return names, skipped


def all_skipped_problem(where, got, skipped):
    if got and got == skipped:
        return f"{where}: every top-level test ({len(got)}) was skipped"
    return None


def check(tested, testlogs, suites, lister=listed_tests, whole=()):
    """Return ([summary lines], [problems]) for suites of (label, script, shards)
    and whole (unsharded or sharded) labels that must not be all-skipped."""
    lines, problems = [], []
    for label in whole:
        if label not in tested:
            problems.append(f"{label}: --whole, but the BEP has no result for it")
            continue
        for xml_path in testlog_xmls(testlogs, label, tested[label]):
            rel = os.path.relpath(xml_path, testlogs)
            if not os.path.exists(xml_path):
                problems.append(f"{label}: {rel} missing (was test.xml downloaded?)")
                continue
            try:
                got, skipped = ran_tests(xml_path)
            except ET.ParseError as e:
                problems.append(f"{label}: cannot parse {rel}: {e}")
                continue
            if not got:
                problems.append(f"{label}: {rel} lists no tests")
            p = all_skipped_problem(f"{label}: {rel}", got, skipped)
            if p:
                problems.append(p)
        lines.append(f"{label}: not all skipped")
    for label, script, shards in suites:
        if tested.get(label) != shards:
            problems.append(f"{label}: the BEP has {tested.get(label, 0)} shard(s), want {shards} ({script})")
            continue
        total = 0
        # The scripts' hash fallback forks per test (seconds per shard for
        # the server suite's ~1200 tests): list the shards concurrently.
        with concurrent.futures.ThreadPoolExecutor(max_workers=min(8, os.cpu_count() or 2)) as pool:
            listings = [pool.submit(lister, script, k, shards) for k in range(1, shards + 1)]
        for k, xml_path in enumerate(testlog_xmls(testlogs, label, shards), start=1):
            try:
                want = listings[k - 1].result()
            except (OSError, subprocess.CalledProcessError) as e:
                problems.append(f"{label}: {script} {k} {shards} failed: {e}")
                continue
            rel = os.path.relpath(xml_path, testlogs)
            if not os.path.exists(xml_path):
                problems.append(f"{label}: {rel} missing (was test.xml downloaded?)")
                continue
            try:
                got, skipped = ran_tests(xml_path)
            except ET.ParseError as e:
                problems.append(f"{label}: cannot parse {rel}: {e}")
                continue
            total += len(want)
            p = all_skipped_problem(f"{label} shard {k}/{shards}", got, skipped)
            if p:
                problems.append(p)
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
    ap.add_argument("--whole", action="append", default=[], metavar="LABEL",
                    help="a target none of whose test.xml may be all skipped (repeatable)")
    args = ap.parse_args(argv)

    suites = []
    for label, script, shards in args.suite:
        if not shards.isdigit() or int(shards) < 1:
            ap.error(f"--suite {label}: SHARDS must be a positive integer, got {shards!r}")
        suites.append((label, script, int(shards)))
    tested, _, bep_testlogs = read_bep(args.bep)
    testlogs = args.testlogs or bep_testlogs or "bazel-testlogs"
    lines, problems = check(tested, testlogs, suites, whole=args.whole)
    for line in lines:
        print(line)
    for p in problems:
        print(f"FAIL: {p}", file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
