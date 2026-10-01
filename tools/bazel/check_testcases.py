#!/usr/bin/env python3
"""Fail if a Bazel test (or any shard of it) ran no Go tests.

Usage: check_testcases.py --bep <build_event_json_file> [--testlogs DIR]
                          [--not-go LABEL ...]

A test target whose Go binary matches no test still exits 0: a shard script
whose manifest assigns a shard nothing ("no tests assigned"), a -test.run
selector that matches nothing, a manifest missing from runfiles. This reads
the tests a `bazel test` invocation ran from its BEP and requires every
target's test.xml, and every shard's, to list at least one top-level
<testcase> (a Test* name without "/"). It needs the test.xml files locally
(--remote_download_regex=.*/test\\.xml$) and -test.v in them
(GO_TEST_WRAP_TESTV=1), as --config=ci, --config=embedded and
--config=integration set.

--not-go LABEL exempts a test that is not a Go binary (a plain sh_test such
as //tools/bazel:dolt_version_test, whose test.xml Bazel writes with no Go
testcases) from the count. The label must still be in the BEP: an
exemption for a target the run did not test is an error, so a stale one
cannot linger.

Exit status: 0 if every test.xml lists a test, 1 otherwise (including a BEP
with no test results at all).
"""

import argparse
import os
import sys
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from equivalence import read_bep, testlog_xmls  # noqa: E402


def count_testcases(xml_path):
    """Return the number of top-level Go tests in a test.xml."""
    n = 0
    for tc in ET.parse(xml_path).getroot().iter("testcase"):
        name = tc.get("name", "")
        if name.startswith("Test") and "/" not in name:
            n += 1
    return n


def check(tested, testlogs, not_go=()):
    """Return ([(xml path, count)], [problems]) for {label: shard_count}."""
    counts, problems = [], []
    if not tested:
        problems.append("the BEP lists no test results")
    for label in sorted(set(not_go) - set(tested)):
        problems.append(f"{label}: --not-go, but the BEP has no result for it")
    for label, shards in sorted(tested.items()):
        if label in not_go:
            continue
        for xml_path in testlog_xmls(testlogs, label, shards):
            rel = os.path.relpath(xml_path, testlogs)
            if not os.path.exists(xml_path):
                problems.append(f"{label}: {rel} missing (was test.xml downloaded?)")
                continue
            try:
                n = count_testcases(xml_path)
            except ET.ParseError as e:
                problems.append(f"{label}: cannot parse {rel}: {e}")
                continue
            counts.append((rel, n))
            if n == 0:
                problems.append(f"{label}: {rel} lists no tests")
    return counts, problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--bep", required=True, help="--build_event_json_file of the bazel test run")
    ap.add_argument("--testlogs", default=None, help="default: from the BEP, else ./bazel-testlogs")
    ap.add_argument("--not-go", action="append", default=[], metavar="LABEL",
                    help="a tested non-Go target to leave out of the count (repeatable)")
    args = ap.parse_args(argv)

    tested, _, bep_testlogs = read_bep(args.bep)
    testlogs = args.testlogs or bep_testlogs or "bazel-testlogs"
    counts, problems = check(tested, testlogs, set(args.not_go))
    for rel, n in counts:
        print(f"{n:5d}  {rel}")
    total = sum(n for _, n in counts)
    print(f"{len(tested)} targets, {len(counts)} test.xml, {total} top-level tests")
    for p in problems:
        print(f"FAIL: {p}", file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
