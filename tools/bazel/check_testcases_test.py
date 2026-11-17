#!/usr/bin/env python3
"""Unit tests for check_testcases.py, in particular its --label flag.

item 5 A2: bazel-dolt-int's union BEP has both tiers' targets, so its two
"Every target and shard ran tests (<tier>)" steps each need --label to scope
the check to their own tier's labels only (should-fix 2, PR #7535 review).
This covers check()'s --label behaviour directly; main()'s argument wiring is
exercised indirectly by bazel_dolt_int_test.go's step-content assertions and
by the manual BEP runs in that PR's review thread.
"""

import json
import os
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from check_testcases import check, main  # noqa: E402


def write_testxml(path, testcase_names):
    root = ET.Element("testsuite")
    for name in testcase_names:
        ET.SubElement(root, "testcase", name=name)
    ET.ElementTree(root).write(path)


class CheckTestcasesTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.testlogs = self.tmp.name

    def tearDown(self):
        self.tmp.cleanup()

    def write(self, label, names, shard=None):
        rel_dir = label.lstrip("/").replace(":", "/")
        d = os.path.join(self.testlogs, rel_dir)
        if shard is not None:
            d = os.path.join(d, "shard_%d_of_1" % shard)
        os.makedirs(d, exist_ok=True)
        write_testxml(os.path.join(d, "test.xml"), names)

    def test_no_label_checks_every_tested_target(self):
        self.write("//a:x", ["TestFoo"])
        self.write("//b:y", ["TestBar"])
        counts, problems = check({"//a:x": 1, "//b:y": 1}, self.testlogs)
        self.assertEqual(problems, [])
        self.assertEqual(len(counts), 2)

    def test_label_restricts_to_named_targets(self):
        self.write("//a:x", ["TestFoo"])
        self.write("//b:y", [])  # would fail if checked
        counts, problems = check(
            {"//a:x": 1, "//b:y": 1}, self.testlogs, labels={"//a:x"}
        )
        self.assertEqual(problems, [])
        self.assertEqual(len(counts), 1)

    def test_label_not_in_bep_is_a_failure(self):
        self.write("//a:x", ["TestFoo"])
        counts, problems = check(
            {"//a:x": 1}, self.testlogs, labels={"//a:x", "//missing:z"}
        )
        self.assertTrue(
            any("//missing:z" in p and "--label" in p for p in problems),
            problems,
        )

    def test_label_does_not_mask_a_zero_testcase_shard_in_its_own_tier(self):
        self.write("//a:x", [])
        counts, problems = check({"//a:x": 1}, self.testlogs, labels={"//a:x"})
        self.assertTrue(any("//a:x" in p and "lists no tests" in p for p in problems))

    def test_label_scoping_is_independent_of_other_tiers_zero_testcase_shard(self):
        # The exact bazel-dolt-int scenario: cmd-dolt's zero-testcase shard
        # must not surface when --label scopes the check to server-storage.
        self.write("//server:ss", ["TestServer"])
        self.write("//cmd:cd", [])
        counts, problems = check(
            {"//server:ss": 1, "//cmd:cd": 1},
            self.testlogs,
            labels={"//server:ss"},
        )
        self.assertEqual(problems, [])
        self.assertEqual(len(counts), 1)

    def test_empty_label_set_with_no_tested_targets_is_a_failure(self):
        counts, problems = check({}, self.testlogs, labels={"//a:x"})
        self.assertTrue(any("//a:x" in p for p in problems))

    def test_not_go_still_applies_within_a_labeled_subset(self):
        self.write("//a:x", ["TestFoo"])
        self.write("//a:shtest", [])  # exempt via --not-go
        counts, problems = check(
            {"//a:x": 1, "//a:shtest": 1},
            self.testlogs,
            not_go=("//a:shtest",),
            labels={"//a:x", "//a:shtest"},
        )
        self.assertEqual(problems, [])


class SkipCensusFlagTest(unittest.TestCase):
    """--skip-census runs check_skip_census.py's census over the same targets."""

    LABEL = "//a:x"

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        d = os.path.join(self.tmp.name, "a", "x")
        os.makedirs(d)
        root = ET.Element("testsuites")
        suite = ET.SubElement(root, "testsuite")
        ET.SubElement(suite, "testcase", name="TestRuns")
        skipped = ET.SubElement(suite, "testcase", name="TestSkips")
        ET.SubElement(skipped, "skipped", message="Skipped").text = (
            "=== RUN   TestSkips\n    x_test.go:3: KNOWN DIVERGENCE W-X (b): r\n--- SKIP: TestSkips (0.00s)\n")
        ET.ElementTree(root).write(os.path.join(d, "test.xml"))
        self.bep = os.path.join(self.tmp.name, "bep.json")
        with open(self.bep, "w", encoding="utf-8") as f:
            for ev in (
                {"id": {"targetConfigured": {"label": self.LABEL}}, "configured": {"targetKind": "go_test rule"}},
                {"id": {"testResult": {"label": self.LABEL, "shard": 1}}, "testResult": {"status": "PASSED"}},
            ):
                f.write(json.dumps(ev) + "\n")

    def census(self, *lines):
        path = os.path.join(self.tmp.name, "skip_census.txt")
        with open(path, "w", encoding="utf-8") as f:
            f.write("".join(line + "\n" for line in lines))
        return path

    def run_main(self, *extra):
        return main(["--bep", self.bep, "--testlogs", self.tmp.name, *extra])

    def test_a_listed_skip_passes(self):
        self.assertEqual(self.run_main("--skip-census", self.census(f"{self.LABEL} TestSkips KNOWN DIVERGENCE W-X")), 0)

    def test_an_unlisted_skip_fails(self):
        self.assertEqual(self.run_main("--skip-census", self.census()), 1)

    def test_a_stale_line_fails(self):
        census = self.census(f"{self.LABEL} TestRuns other", f"{self.LABEL} TestSkips KNOWN DIVERGENCE W-X")
        self.assertEqual(self.run_main("--skip-census", census), 1)

    def test_report_only_never_fails_on_the_census(self):
        self.assertEqual(self.run_main("--skip-census", self.census(), "--skip-census-report-only"), 0)

    def test_report_only_needs_the_census(self):
        with self.assertRaises(SystemExit):
            self.run_main("--skip-census-report-only")

    def test_label_narrows_the_census(self):
        census = self.census()
        self.assertEqual(self.run_main("--label", self.LABEL, "--skip-census", census), 1)


if __name__ == "__main__":
    unittest.main()
