#!/usr/bin/env python3
"""Unit tests for check_testcases.py, in particular its --label flag.

item 5 A2: bazel-dolt-int's union BEP has both tiers' targets, so its two
"Every target and shard ran tests (<tier>)" steps each need --label to scope
the check to their own tier's labels only (should-fix 2, PR #7535 review).
This covers check()'s --label behaviour directly; main()'s argument wiring is
exercised indirectly by bazel_dolt_int_test.go's step-content assertions and
by the manual BEP runs in that PR's review thread.
"""

import os
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from check_testcases import check  # noqa: E402


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


if __name__ == "__main__":
    unittest.main()
