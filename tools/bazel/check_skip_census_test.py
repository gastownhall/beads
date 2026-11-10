#!/usr/bin/env python3
"""Unit tests for check_skip_census.py (the runtime skip census).

Run directly: python3 tools/bazel/check_skip_census_test.py
(scripts' TestCheckSkipCensusUnitTests runs it under //scripts:scripts_test).
"""

import contextlib
import io
import os
import sys
import tempfile
import unittest
import xml.etree.ElementTree as ET

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import check_skip_census as csc  # noqa: E402

SERVED = "//internal/httpclient:httpclient_served_test"
UOW = "//internal/storage/uow:uow_dolt_test"
CMD = "//cmd/bd:bd_dolt_server_test"

HEADER = "# test census\n"


def skip_body(name, message, where="x_test.go:12"):
    indent = "    " * name.count("/")
    return (f"{indent}=== RUN   {name}\n{indent}    {where}: {message}\n"
            f"{indent}--- SKIP: {name} (0.00s)\n")


class Run:
    """A bazel-testlogs tree of synthetic test.xml files."""

    def __init__(self, tc):
        self.tmp = tempfile.TemporaryDirectory()
        tc.addCleanup(self.tmp.cleanup)
        self.root = self.tmp.name
        self.tested = {}

    def xml(self, label, cases, shard=None, shards=1):
        """cases: [(name, status, message)] with status passed|skipped|failed."""
        pkg, name = label[2:].split(":", 1)
        d = os.path.join(self.root, pkg, name)
        if shard is not None:
            d = os.path.join(d, f"shard_{shard}_of_{shards}")
        os.makedirs(d, exist_ok=True)
        suites = ET.Element("testsuites")
        suite = ET.SubElement(suites, "testsuite")
        for case, status, message in cases:
            tc = ET.SubElement(suite, "testcase", name=case)
            if status == "skipped":
                ET.SubElement(tc, "skipped", message="Skipped").text = skip_body(case, message)
            elif status == "failed":
                ET.SubElement(tc, "failure", message="Failed").text = "boom"
        ET.ElementTree(suites).write(os.path.join(d, "test.xml"))
        self.tested[label] = max(self.tested.get(label, 0), shards)

    def census(self, *lines):
        path = os.path.join(self.root, "skip_census.txt")
        with open(path, "w", encoding="utf-8") as f:
            f.write(HEADER + "".join(line + "\n" for line in lines))
        return path

    def judge(self, census, labels=None):
        _, problems, notes = csc.census_problems(self.tested, self.root, census, labels=labels)
        return problems, notes


class SkipMessageTest(unittest.TestCase):
    def test_takes_the_last_log_entry_before_the_skip_line(self):
        body = ("=== RUN   TestX\n    x_test.go:10: KNOWN DIVERGENCE W-Logged.Before (b): not the skip\n"
                "    x_test.go:20: the real reason\n--- SKIP: TestX (0.00s)\n")
        self.assertEqual(csc.skip_message(body, "TestX"), "the real reason")

    def test_joins_continuation_lines(self):
        body = "=== RUN   TestX\n    x_test.go:9: first\n        second\n--- SKIP: TestX (0.00s)\n"
        self.assertEqual(csc.skip_message(body, "TestX"), "first second")

    def test_reads_an_indented_subtest_skip(self):
        name = "TestX/sub"
        self.assertEqual(csc.skip_message(skip_body(name, "why"), name), "why")

    def test_a_go_file_reference_inside_the_message_stays(self):
        name = "TestX"
        msg = "bd doctor requires server mode (doctor.go:188); covered elsewhere"
        self.assertEqual(csc.skip_message(skip_body(name, msg), name), msg)

    def test_skip_now_has_no_message(self):
        self.assertEqual(csc.skip_message("=== RUN   TestX\n--- SKIP: TestX (0.00s)\n", "TestX"), "")


class ClassifyTest(unittest.TestCase):
    def test_classes(self):
        for message, want in [
            ("KNOWN DIVERGENCE W-BatchCreateItem.Issue (ga-6lbny): the wire withholds it", "KNOWN DIVERGENCE W-BatchCreateItem.Issue"),
            ("KNOWN DIVERGENCE be-abc12: storage-leg helper", "KNOWN DIVERGENCE be-abc12"),
            ("KNOWN BDP DIVERGENCE F-unknown-type (bd-x): bdp only", "KNOWN BDP DIVERGENCE F-unknown-type"),
            ("WAITING ON #8227 (guarded deletes through bd serve take the fence)", "WAITING ON #8227"),
            ("WAITING ON donnabox fixes-b B14 (HTTP client journal keeps the actor)", "WAITING ON donnabox fixes-b B14"),
            ("WAITING ON #7987: the RequestContext hook", "WAITING ON #7987"),
            ("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests", "env-gate BEADS_TEST_EMBEDDED_DOLT"),
            ("set BEADS_TEST_MIGRATION_FRONTDOOR=1", "env-gate BEADS_TEST_MIGRATION_FRONTDOOR"),
            ("this backend does not implement expectedRevision CAS (CompareAndSetVersion is nil)", "nil-hook CompareAndSetVersion"),
            ("fixture cannot run the List role: List is nil", "nil-hook List"),
            ("fixture.CountHistory is nil: this backend cannot observe history", "nil-hook fixture.CountHistory"),
            ("fixture has no CountHistory: this backend cannot observe history", "nil-hook CountHistory"),
            ("this leg supplies no import (JournalArrivalHooks.Import)", "nil-hook JournalArrivalHooks.Import"),
            ("LeaseReclaimerFixture.Exec/Claimer/QueryScalar is nil: cannot manufacture a stale lease", "nil-hook LeaseReclaimerFixture.Exec/Claimer/QueryScalar"),
            ("this leg cannot write the stored is_blocked flag directly", "other"),
            ("flaky on CI", "other"),
            ("", "other"),
        ]:
            with self.subTest(message=message):
                self.assertEqual(csc.classify(message), want)
                self.assertRegex(want, csc.CLASS_RE)

    def test_a_park_wins_over_a_waiting_on_in_its_reason(self):
        self.assertEqual(csc.classify("KNOWN DIVERGENCE L-x (b): WAITING ON #1 (later)"), "KNOWN DIVERGENCE L-x")


class JudgeTest(unittest.TestCase):
    def test_listed_skips_pass(self):
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "skipped", "KNOWN DIVERGENCE W-A (b): r"), ("TestServedB", "passed", "")])
        problems, _ = run.judge(run.census(f"{SERVED} TestServedA KNOWN DIVERGENCE W-A"))
        self.assertEqual(problems, [])

    def test_a_bare_skip_in_a_served_case_fails(self):
        # Acceptance (1): a bare t.Skip in a served or store-leg case fails that lane's census.
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "skipped", "KNOWN DIVERGENCE W-A (b): r"), ("TestServedB", "skipped", "flaky")])
        problems, _ = run.judge(run.census(f"{SERVED} TestServedA KNOWN DIVERGENCE W-A"))
        self.assertEqual(len(problems), 1, problems)
        self.assertIn("TestServedB skipped as 'other'", problems[0])

    def test_a_new_subtest_skip_in_a_store_leg_fails(self):
        run = Run(self)
        run.xml(UOW, [("TestContract", "passed", ""), ("TestContract/Case", "skipped", "the hook (Hook is nil)")])
        problems, _ = run.judge(run.census())
        self.assertTrue(any("TestContract/Case skipped as 'nil-hook Hook'" in p for p in problems), problems)

    def test_unparking_without_deleting_the_line_fails_as_stale(self):
        # Acceptance (2): unparking a KNOWN DIVERGENCE case without deleting its line fails.
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "passed", "")])
        problems, _ = run.judge(run.census(f"{SERVED} TestServedA KNOWN DIVERGENCE W-A"))
        self.assertEqual(len(problems), 1, problems)
        self.assertIn("TestServedA ran and passed", problems[0])

    def test_a_listed_test_that_fails_is_only_a_note(self):
        # The failure already fails the lane; telling the author to delete the
        # park line would make the next (passing-flake) run fail as unlisted.
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "failed", "")])
        problems, notes = run.judge(run.census(f"{SERVED} TestServedA KNOWN DIVERGENCE W-A"))
        self.assertEqual(problems, [])
        self.assertTrue(any("TestServedA failed instead of skipping" in n for n in notes), notes)
        out, _, _ = csc.census_problems(run.tested, run.root, run.census(f"{SERVED} TestServedA KNOWN DIVERGENCE W-A"))
        self.assertFalse(any("delete from" in line for line in out), out)

    def test_a_changed_row_is_one_problem(self):
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "skipped", "KNOWN DIVERGENCE W-New (b): r")])
        problems, _ = run.judge(run.census(f"{SERVED} TestServedA KNOWN DIVERGENCE W-Old"))
        self.assertEqual(len(problems), 1, problems)
        self.assertIn("now skips as 'KNOWN DIVERGENCE W-New'", problems[0])

    def test_a_listed_test_that_no_longer_runs_fails_in_a_complete_target(self):
        run = Run(self)
        run.xml(SERVED, [("TestServedB", "passed", "")])
        problems, _ = run.judge(run.census(f"{SERVED} TestServedGone other"))
        self.assertTrue(any("did not run" in p for p in problems), problems)

    def test_a_listed_test_missing_from_a_failed_target_is_only_a_note(self):
        run = Run(self)
        run.xml(UOW, [("TestContract", "failed", "")])
        problems, notes = run.judge(run.census(f"{UOW} TestContract/Case nil-hook Hook"))
        self.assertEqual(problems, [])
        self.assertTrue(any("TestContract failed" in n for n in notes), notes)

    def test_a_missing_shard_makes_the_target_incomplete(self):
        run = Run(self)
        run.xml(UOW, [("TestA", "passed", "")], shard=1, shards=2)
        problems, notes = run.judge(run.census(f"{UOW} TestB other"))
        self.assertEqual(problems, [])
        self.assertTrue(any("missing" in n for n in notes), notes)

    def test_shards_are_judged_together(self):
        run = Run(self)
        run.xml(UOW, [("TestA", "skipped", "x")], shard=1, shards=2)
        run.xml(UOW, [("TestB", "skipped", "y")], shard=2, shards=2)
        problems, _ = run.judge(run.census(f"{UOW} TestA other", f"{UOW} TestB other"))
        self.assertEqual(problems, [])

    def test_lines_for_targets_this_run_did_not_test_are_not_judged(self):
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "passed", "")])
        problems, _ = run.judge(run.census(f"{UOW} TestGone other"))
        self.assertEqual(problems, [])

    def test_labels_narrow_the_judged_targets(self):
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "skipped", "unlisted")])
        run.xml(UOW, [("TestA", "passed", "")])
        problems, _ = run.judge(run.census(), labels={UOW})
        self.assertEqual(problems, [])

    def test_an_env_gate_line_covers_its_target(self):
        run = Run(self)
        run.xml(CMD, [("TestEmbeddedA", "skipped", "set BEADS_TEST_EMBEDDED_DOLT=1 to run a"),
                      ("TestEmbeddedB", "skipped", "set BEADS_TEST_EMBEDDED_DOLT=1 to run b")])
        problems, _ = run.judge(run.census(f"{CMD} * env-gate BEADS_TEST_EMBEDDED_DOLT"))
        self.assertEqual(problems, [])

    def test_an_env_gate_skip_without_its_line_fails(self):
        run = Run(self)
        run.xml(CMD, [("TestProxiedA", "skipped", "set BEADS_TEST_PROXIED_SERVER=1 to run a"),
                      ("TestProxiedB", "skipped", "set BEADS_TEST_PROXIED_SERVER=1 to run b"),
                      ("TestProxiedC", "skipped", "set BEADS_TEST_PROXIED_SERVER=1 to run c")])
        census = run.census(f"{CMD} * env-gate BEADS_TEST_EMBEDDED_DOLT")
        problems, _ = run.judge(census)
        # One problem for the new gate (not one per test), and one for the stale glob.
        self.assertEqual(len(problems), 2, problems)
        self.assertTrue(any("3 tests (first TestProxiedA) skip as 'env-gate BEADS_TEST_PROXIED_SERVER'" in p
                            for p in problems), problems)
        out, _, _ = csc.census_problems(run.tested, run.root, census)
        self.assertEqual(out.count(f"  {CMD} * env-gate BEADS_TEST_PROXIED_SERVER"), 1, out)

    def test_an_env_gate_line_nothing_matches_is_stale(self):
        run = Run(self)
        run.xml(CMD, [("TestA", "passed", "")])
        problems, _ = run.judge(run.census(f"{CMD} * env-gate BEADS_TEST_EMBEDDED_DOLT"))
        self.assertTrue(any("skips that way" in p for p in problems), problems)

    def test_census_errors_are_problems(self):
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "passed", "")])
        problems, _ = run.judge(run.census(f"{SERVED} TestServedA not-a-class"))
        self.assertTrue(any("is not a census class" in p for p in problems), problems)


class CensusFileTest(unittest.TestCase):
    def read(self, *lines):
        with tempfile.NamedTemporaryFile("w", suffix=".txt", delete=False, encoding="utf-8") as f:
            f.write("".join(line + "\n" for line in lines))
        self.addCleanup(os.unlink, f.name)
        return csc.read_census(f.name)

    def test_errors(self):
        for lines, want in [
            ([f"{UOW} TestB other", f"{UOW} TestA other"], "not sorted"),
            ([f"{UOW} TestA other", f"{UOW} TestA other"], "duplicate"),
            ([f"{UOW} TestA nil-hook X", f"{UOW} TestA other"], "already listed"),
            ([f"uow TestA other"], "label"),
            ([f"{UOW} TestA"], "want '<label>"),
            ([f"{CMD} TestA env-gate BEADS_X"], "must be *"),
            ([f"{CMD} * other"], "not a Go test path"),
        ]:
            with self.subTest(lines=lines):
                _, _, errors = self.read(*lines)
                self.assertTrue(any(want in e for e in errors), errors)

    def test_comments_blank_lines_and_the_header(self):
        header, entries, errors = self.read("# head", "", f"{UOW} TestA WAITING ON donnabox fixes-b B14", "# later", f"{UOW} TestB other")
        self.assertEqual(errors, [])
        self.assertEqual(header, ["# head", ""])
        self.assertEqual([e["cls"] for e in entries], ["WAITING ON donnabox fixes-b B14", "other"])

    def test_rule_tags(self):
        build = ('sh_test(\n    name = "a_test",\n    srcs = ["x.sh"],\n    tags = ["embedded"],\n)\n\n'
                 'sh_test(\n    name = "b_test",\n    tags = [\n        "dolt-server",\n        "manual",\n    ],\n)\n')
        self.assertEqual(csc.rule_tags(build, "a_test"), {"embedded"})
        self.assertEqual(csc.rule_tags(build, "b_test"), {"dolt-server", "manual"})
        self.assertIsNone(csc.rule_tags(build, "c_test"))

    def test_lint_labels(self):
        with tempfile.TemporaryDirectory() as root:
            os.makedirs(os.path.join(root, "p"))
            with open(os.path.join(root, "p", "BUILD.bazel"), "w") as f:
                f.write('sh_test(\n    name = "ok_test",\n    tags = ["embedded"],\n)\n'
                        'go_test(\n    name = "unit_test",\n)\n'
                        'sh_test(\n    name = "manual_test",\n    tags = ["embedded", "manual"],\n)\n')
            entries = [{"label": f"//p:{n}"} for n in ("ok_test", "unit_test", "manual_test", "gone_test")] + [{"label": "//q:x"}]
            errors = csc.lint_labels(entries, root)
            self.assertEqual(len(errors), 4, errors)
            self.assertFalse(any("ok_test" in e for e in errors))


class WriteTest(unittest.TestCase):
    def test_write_replaces_complete_targets_and_keeps_the_rest(self):
        run = Run(self)
        run.xml(SERVED, [("TestServedA", "skipped", "KNOWN DIVERGENCE W-A (b): r"), ("TestServedOld", "passed", "")])
        run.xml(UOW, [("TestContract", "failed", "")])
        run.xml(CMD, [("TestEmbeddedA", "skipped", "set BEADS_TEST_EMBEDDED_DOLT=1 to run a")])
        path = run.census(f"//other:t TestKept other", f"{SERVED} TestServedOld other", f"{UOW} TestContract/Case nil-hook Hook")
        header, entries, _ = csc.read_census(path)
        obs = csc.observe({l: [os.path.join(run.root, l[2:].replace(":", "/"), "test.xml")] for l in run.tested})
        csc.write_census(path, header, entries, obs)
        header, entries, errors = csc.read_census(path)
        self.assertEqual(errors, [])
        self.assertEqual(header, ["# test census"])
        self.assertEqual([e["text"] for e in entries], [
            f"{CMD} * env-gate BEADS_TEST_EMBEDDED_DOLT",
            f"{SERVED} TestServedA KNOWN DIVERGENCE W-A",
            f"{UOW} TestContract/Case nil-hook Hook",
            "//other:t TestKept other",
        ])


    def test_write_replaces_a_changed_class_in_an_incomplete_target(self):
        run = Run(self)
        run.xml(UOW, [("TestContract", "failed", ""), ("TestOther", "skipped", "KNOWN DIVERGENCE W-New (b): r")])
        path = run.census(f"{UOW} TestContract/Case nil-hook Hook", f"{UOW} TestOther KNOWN DIVERGENCE W-Old")
        header, entries, _ = csc.read_census(path)
        obs = csc.observe({UOW: [os.path.join(run.root, UOW[2:].replace(":", "/"), "test.xml")]})
        self.assertIn(UOW, obs.incomplete)
        csc.write_census(path, header, entries, obs)
        _, entries, errors = csc.read_census(path)
        self.assertEqual(errors, [])
        self.assertEqual([e["text"] for e in entries], [
            f"{UOW} TestContract/Case nil-hook Hook",
            f"{UOW} TestOther KNOWN DIVERGENCE W-New",
        ])


class MainTest(unittest.TestCase):
    def test_a_missing_census_file_fails_except_under_write(self):
        with tempfile.TemporaryDirectory() as d, \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            missing = os.path.join(d, "typo.txt")
            self.assertEqual(csc.main(["--lint", "--census", missing]), 1)
            run = Run(self)
            run.xml(SERVED, [("TestServedA", "passed", "")])
            self.assertEqual(csc.main(["--census", missing, "--testlogs-dir", run.root]), 1)
            self.assertEqual(csc.main(["--write", "--census", missing, "--testlogs-dir", run.root]), 0)
            self.assertTrue(os.path.exists(missing))


class ScanTestlogsDirTest(unittest.TestCase):
    def test_labels_and_a_missing_shard(self):
        run = Run(self)
        run.xml(UOW, [("TestA", "passed", "")], shard=1, shards=2)
        run.xml(SERVED, [("TestServedA", "passed", "")])
        got = csc.scan_testlogs_dir(run.root)
        self.assertEqual(sorted(got), [SERVED, UOW])
        self.assertEqual(len(got[UOW]), 2)
        obs = csc.observe(got)
        self.assertIn(UOW, obs.incomplete)
        self.assertNotIn(SERVED, obs.incomplete)


class CheckedInCensusTest(unittest.TestCase):
    def test_the_checked_in_census_lints_clean(self):
        _, entries, errors = csc.read_census(csc.DEFAULT_CENSUS)
        errors += csc.lint_labels(entries, csc.REPO_ROOT)
        self.assertEqual(errors, [])
        self.assertGreater(len(entries), 0)


if __name__ == "__main__":
    unittest.main()
