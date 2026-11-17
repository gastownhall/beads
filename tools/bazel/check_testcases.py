#!/usr/bin/env python3
"""Fail if a Bazel test (or any shard of it) ran no Go tests.

Usage: check_testcases.py --bep <build_event_json_file> [--testlogs DIR]
                          [--not-go LABEL ...] [--label LABEL ...]

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
cannot linger. //pkg:all exempts every test in that package (one that holds
only non-Go tests, such as //scripts/repochecks), and must match at least one
tested target.

--label LABEL (repeatable) restricts which tested targets this invocation
judges, by exact label; a requested label absent from the BEP is itself a
failure (a typo or a stale label must not silently narrow the check to
nothing). item 5 A2: bazel-dolt-int's union BEP has both tiers' targets, and
its two "Every target and shard ran tests (<tier>)" steps each pass only
their own tier's labels, so a cmd-dolt zero-testcase shard reaches only
cmd_dolt_result (classify_tier_results.py's --check), not server-storage's,
and vice versa. Omitted (bazel-test and every other lane's own step,
unchanged): every tested target in the BEP, as before.

--skip-census FILE (bazel.yml: every Dolt-tier lane's step, with
tools/bazel/skip_census.txt) also fails on a skipped test or subtest of a
judged target that FILE does not list, and on a FILE line whose test now
runs (check_skip_census.py; --label narrows it like the count).
--skip-census-report-only prints those findings without failing: the lanes
pass it in modes cache and local, which execute on the runner.

Exit status: 0 if every test.xml lists a test (and, with --skip-census, the
census agrees), 1 otherwise (including a BEP with no test results at all, or
a --label naming a target the BEP has no result for).
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


def check(tested, testlogs, not_go=(), labels=None):
    """Return ([(xml path, count)], [problems]) for {label: shard_count}.

    labels, if given (a set), restricts the check to those exact labels; a
    label with no entry in tested is itself a problem (see the module
    docstring's --label section).
    """
    counts, problems = [], []
    if labels is not None:
        for label in sorted(set(labels) - set(tested)):
            problems.append(f"{label}: --label, but the BEP has no result for it")
        tested = {l: n for l, n in tested.items() if l in labels}
    if not tested:
        problems.append("the BEP lists no test results")
    def exempt(label, entry):
        if entry.endswith(":all"):
            return label.split(":", 1)[0] == entry[: -len(":all")]
        return label == entry

    for entry in sorted(set(not_go)):
        if not any(exempt(label, entry) for label in tested):
            problems.append(f"{entry}: --not-go, but the BEP has no result for it")
    for label, shards in sorted(tested.items()):
        if any(exempt(label, entry) for entry in not_go):
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
    ap.add_argument("--label", action="append", default=None, metavar="LABEL",
                    help="restrict to this exact target label (repeatable); default: every tested target in the BEP")
    ap.add_argument("--skip-census", default=None, metavar="FILE",
                    help="also fail on a skip FILE does not list, or a FILE line whose test now runs (check_skip_census.py)")
    ap.add_argument("--skip-census-report-only", action="store_true",
                    help="print --skip-census's findings without failing (modes cache and local)")
    args = ap.parse_args(argv)
    if args.skip_census_report_only and not args.skip_census:
        ap.error("--skip-census-report-only needs --skip-census")

    tested, _, bep_testlogs = read_bep(args.bep)
    testlogs = args.testlogs or bep_testlogs or "bazel-testlogs"
    labels = set(args.label) if args.label else None
    counts, problems = check(tested, testlogs, set(args.not_go), labels=labels)
    for rel, n in counts:
        print(f"{n:5d}  {rel}")
    total = sum(n for _, n in counts)
    print(f"{len(tested)} targets, {len(counts)} test.xml, {total} top-level tests")
    if args.skip_census:
        # Imported only here: lanes without --skip-census (bazel-integration) never load it.
        from check_skip_census import census_problems

        lines, census_failures, notes = census_problems(tested, testlogs, args.skip_census, labels=labels)
        for line in lines:
            print(line)
        for note in notes:
            print(f"note: skip census: {note}")
        if args.skip_census_report_only:
            for p in census_failures:
                print(f"note: skip census (report only): {p}")
        else:
            problems += [f"skip census: {p}" for p in census_failures]
    for p in problems:
        print(f"FAIL: {p}", file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
