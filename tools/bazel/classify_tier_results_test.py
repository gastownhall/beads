#!/usr/bin/env python3
"""Unit tests for classify_tier_results.py.

Run directly: python3 tools/bazel/classify_tier_results_test.py
(or: python3 -m unittest tools.bazel.classify_tier_results_test, from the repo root)
"""

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import classify_tier_results as ctr  # noqa: E402

SS = "//internal/storage/dolt:dolt_server_full_test"
SS2 = "//internal/storage/dolt:dolt_server_conformance_test"
CD = "//cmd/bd:bd_dolt_server_test"
CD2 = "//tests/regression:regression_test"
UNKNOWN = "//internal/storage/dolt:dolt_server_new_test"

TIERS = {"server-storage": [SS, SS2], "cmd-dolt": [CD, CD2]}


def write_bep(events):
    f = tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False, encoding="utf-8")
    for ev in events:
        f.write(json.dumps(ev) + "\n")
    f.close()
    return f.name


def result_event(label, status):
    return {"id": {"testResult": {"label": label}}, "testResult": {"status": status}}


def result_event_without_status(label):
    # proto3 JSON omits a NO_STATUS (0) field entirely rather than writing
    # "status": "NO_STATUS", so a real BEP line for an unattempted/aborted
    # test action looks like this, not like result_event(label, "NO_STATUS").
    # testResult still carries other fields (cachedLocally et al), so it
    # stays truthy; only "status" is missing.
    return {"id": {"testResult": {"label": label}}, "testResult": {"cachedLocally": False}}


def all_passed_bep(extra=()):
    return write_bep([result_event(l, "PASSED") for l in (SS, SS2, CD, CD2)] + list(extra))


class TierResultTest(unittest.TestCase):
    def test_all_passed_is_success(self):
        statuses = {SS: {"PASSED"}, SS2: {"PASSED"}}
        self.assertEqual(ctr.tier_result(statuses, [SS, SS2]), "success")

    def test_any_failed_label_is_failure(self):
        statuses = {SS: {"PASSED"}, SS2: {"FAILED"}}
        self.assertEqual(ctr.tier_result(statuses, [SS, SS2]), "failure")

    def test_missing_label_is_failure(self):
        statuses = {SS: {"PASSED"}}
        self.assertEqual(ctr.tier_result(statuses, [SS, SS2]), "failure")

    def test_flaky_status_is_failure(self):
        # These lanes run flaky=False: a FLAKY-looking status is still a
        # failure, never silently treated as a pass.
        statuses = {SS: {"PASSED", "FLAKY"}}
        self.assertEqual(ctr.tier_result(statuses, [SS]), "failure")

    def test_omitted_status_is_failure(self):
        # proto3 JSON omits a NO_STATUS (0) field entirely; read_test_statuses
        # falls back to the literal string "NO_STATUS", which is not PASSED.
        statuses = {SS: {"NO_STATUS"}}
        self.assertEqual(ctr.tier_result(statuses, [SS]), "failure")


class ReadTestStatusesTest(unittest.TestCase):
    def test_bep_testresult_with_omitted_status_field_defaults_to_no_status(self):
        # PR #7535 review nit (fail-closed by code, uncovered by test): a
        # testResult event whose body omits "status" entirely (the real
        # proto3-JSON shape for NO_STATUS, e.g. a shard that never ran) must
        # not default to PASSED, or an unattempted test would read as a pass.
        path = write_bep([result_event_without_status(SS)])
        try:
            statuses = ctr.read_test_statuses(path)
        finally:
            os.unlink(path)
        self.assertEqual(statuses, {SS: {"NO_STATUS"}})

    def test_classify_fails_the_tier_on_an_omitted_status_result(self):
        # End-to-end: an omitted-status event for a --tier label fails that
        # tier (and, as the gate tier, the job), exactly as an explicit
        # non-PASSED status would.
        path = write_bep([
            result_event_without_status(SS),
            result_event(SS2, "PASSED"),
            result_event(CD, "PASSED"),
            result_event(CD2, "PASSED"),
        ])
        try:
            results, status, reason = ctr.classify(path, 3, TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "success"})
        self.assertEqual(status, 1)
        self.assertIsNotNone(reason)


class ClassifyTest(unittest.TestCase):
    def test_rc_0_both_tiers_succeed(self):
        path = all_passed_bep()
        try:
            results, status, reason = ctr.classify(path, 0, TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "success", "cmd-dolt": "success"})
        self.assertEqual(status, 0)
        self.assertIsNone(reason)

    def test_server_storage_fail_only_fails_the_job(self):
        path = write_bep([
            result_event(SS, "FAILED"),
            result_event(SS2, "PASSED"),
            result_event(CD, "PASSED"),
            result_event(CD2, "PASSED"),
        ])
        try:
            results, status, reason = ctr.classify(path, 3, TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "success"})
        self.assertEqual(status, 1)
        self.assertIsNotNone(reason)

    def test_cmd_dolt_fail_only_leaves_the_job_green(self):
        path = write_bep([
            result_event(SS, "PASSED"),
            result_event(SS2, "PASSED"),
            result_event(CD, "FAILED"),
            result_event(CD2, "PASSED"),
        ])
        try:
            results, status, reason = ctr.classify(path, 3, TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "success", "cmd-dolt": "failure"})
        # A cmd-dolt-only test failure must not fail the job: it stays
        # advisory, exactly as bazel-cmd-dolt's own continue-on-error outside
        # mode remote.
        self.assertEqual(status, 0)
        self.assertIsNone(reason)

    def test_both_tiers_fail(self):
        path = write_bep([
            result_event(SS, "FAILED"),
            result_event(SS2, "PASSED"),
            result_event(CD, "TIMEOUT"),
            result_event(CD2, "PASSED"),
        ])
        try:
            results, status, reason = ctr.classify(path, 3, TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "failure"})
        self.assertEqual(status, 1)

    def test_build_failure_fails_both_tiers_and_the_job(self):
        # rc=1 (BUILD_FAILURE): not attributable to one tier, even though the
        # BEP happens to show every known label passing (e.g. an unrelated
        # target failed to build under --keep_going).
        path = all_passed_bep()
        try:
            results, status, reason = ctr.classify(path, 1, TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "failure"})
        self.assertEqual(status, 1)

    def test_missing_rc_fails_both_tiers_and_the_job(self):
        # should-fix 2 (bee-ghosttrack's review): a timed-out or cancelled
        # `bazel test` step never writes $TEST_RC, so bazel.yml passes
        # --rc "" rather than omitting the flag; this must take the "not 0
        # or 3" branch, not crash.
        path = all_passed_bep()
        try:
            results, status, reason = ctr.classify(path, "", TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "failure"})
        self.assertEqual(status, 1)
        self.assertIn("missing or not an integer", reason)

    def test_non_integer_rc_fails_both_tiers_and_the_job(self):
        path = all_passed_bep()
        try:
            results, status, reason = ctr.classify(path, "abc", TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "failure"})
        self.assertEqual(status, 1)
        self.assertIn("missing or not an integer", reason)

    def test_gate_tier_selects_which_tier_fails_the_job(self):
        # Flip which tier gates: a cmd-dolt failure now fails the job, a
        # server-storage failure does not. (Not bazel-dolt-int's own wiring,
        # which always gates on server-storage; this just proves the script
        # is not hardcoded to one tier name.)
        path = write_bep([
            result_event(SS, "FAILED"),
            result_event(SS2, "PASSED"),
            result_event(CD, "PASSED"),
            result_event(CD2, "PASSED"),
        ])
        try:
            results, status, reason = ctr.classify(path, 3, TIERS, "cmd-dolt")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "success"})
        self.assertEqual(status, 0)

    def test_unknown_failing_label_fails_every_tier_and_the_job(self):
        # must-fix 1: a target tagged dolt-server-integration/dolt-server-cmd
        # added without updating the --tier lists (bazel.yml's classify step
        # args, or the --tier arg dict built here) must not read as success
        # just because no --tier list claims its label.
        path = all_passed_bep(extra=[result_event(UNKNOWN, "FAILED")])
        try:
            results, status, reason = ctr.classify(path, 3, TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "failure"})
        self.assertEqual(status, 1)
        self.assertIn(UNKNOWN, reason)

    def test_unknown_passing_label_does_not_affect_classification(self):
        # A target outside every --tier that passed is not this script's
        # business (it is not one of the two tiers bazel-dolt-int reports);
        # only a non-PASSED result outside the tiers triggers the fail-closed
        # path.
        path = all_passed_bep(extra=[result_event(UNKNOWN, "PASSED")])
        try:
            results, status, reason = ctr.classify(path, 0, TIERS, "server-storage")
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "success", "cmd-dolt": "success"})
        self.assertEqual(status, 0)

    def test_check_failure_forces_that_tier_failed_and_gates_the_job(self):
        # should-fix 2: a per-tier check_testcases.py step's own outcome
        # (bazel.yml's split "Every target and shard ran tests (<tier>)"
        # steps) folds into that tier's result even though the BEP alone
        # would call it a success (e.g. a zero-testcase shard).
        path = all_passed_bep()
        try:
            results, status, reason = ctr.classify(
                path, 0, TIERS, "server-storage", checks={"server-storage": "failure"}
            )
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "failure", "cmd-dolt": "success"})
        self.assertEqual(status, 1)

    def test_check_failure_on_non_gate_tier_leaves_the_job_green(self):
        path = all_passed_bep()
        try:
            results, status, reason = ctr.classify(
                path, 0, TIERS, "server-storage", checks={"cmd-dolt": "failure"}
            )
        finally:
            os.unlink(path)
        self.assertEqual(results, {"server-storage": "success", "cmd-dolt": "failure"})
        self.assertEqual(status, 0)

    def test_check_cancelled_also_forces_failure(self):
        path = all_passed_bep()
        try:
            results, _, _ = ctr.classify(
                path, 0, TIERS, "server-storage", checks={"cmd-dolt": "cancelled"}
            )
        finally:
            os.unlink(path)
        self.assertEqual(results["cmd-dolt"], "failure")

    def test_check_success_leaves_bep_result_alone(self):
        path = write_bep([
            result_event(SS, "FAILED"),
            result_event(SS2, "PASSED"),
            result_event(CD, "PASSED"),
            result_event(CD2, "PASSED"),
        ])
        try:
            results, status, _ = ctr.classify(
                path, 3, TIERS, "server-storage", checks={"server-storage": "success"}
            )
        finally:
            os.unlink(path)
        self.assertEqual(results["server-storage"], "failure")
        self.assertEqual(status, 1)


class MainTest(unittest.TestCase):
    def run_main(self, bep, rc, gate_tier="server-storage", extra_args=()):
        out = tempfile.NamedTemporaryFile(mode="w", delete=False)
        out.close()
        try:
            status = ctr.main([
                "--bep", bep,
                "--rc", str(rc),
                "--tier", f"server-storage={SS},{SS2}",
                "--tier", f"cmd-dolt={CD},{CD2}",
                "--gate-tier", gate_tier,
                "--github-output", out.name,
                *extra_args,
            ])
            with open(out.name, encoding="utf-8") as f:
                lines = set(f.read().splitlines())
        finally:
            os.unlink(out.name)
        return status, lines

    def test_writes_github_output_and_exits_1_on_gate_tier_failure(self):
        bep = write_bep([
            result_event(SS, "FAILED"),
            result_event(SS2, "PASSED"),
            result_event(CD, "PASSED"),
            result_event(CD2, "PASSED"),
        ])
        try:
            status, lines = self.run_main(bep, 3)
        finally:
            os.unlink(bep)
        self.assertEqual(status, 1)
        self.assertEqual(lines, {"server_storage_result=failure", "cmd_dolt_result=success"})

    def test_empty_rc_exits_1_with_outputs_written_not_an_argparse_crash(self):
        # should-fix 2: bazel.yml always passes --rc "$TEST_RC", so a timed
        # out or cancelled test step reaches main() with --rc "" rather than
        # omitting the flag; this must write both failure outputs and exit
        # 1, not crash argparse with nothing written at all.
        bep = all_passed_bep()
        try:
            status, lines = self.run_main(bep, "")
        finally:
            os.unlink(bep)
        self.assertEqual(status, 1)
        self.assertEqual(lines, {"server_storage_result=failure", "cmd_dolt_result=failure"})

    def test_exits_0_when_only_the_non_gate_tier_fails(self):
        bep = write_bep([
            result_event(SS, "PASSED"),
            result_event(SS2, "PASSED"),
            result_event(CD, "FAILED"),
            result_event(CD2, "PASSED"),
        ])
        try:
            status, _ = self.run_main(bep, 3)
        finally:
            os.unlink(bep)
        self.assertEqual(status, 0)

    def test_unknown_failing_label_exits_1_through_main(self):
        bep = all_passed_bep(extra=[result_event(UNKNOWN, "FAILED")])
        try:
            status, lines = self.run_main(bep, 3)
        finally:
            os.unlink(bep)
        self.assertEqual(status, 1)
        self.assertEqual(lines, {"server_storage_result=failure", "cmd_dolt_result=failure"})

    def test_check_flag_folds_into_tier_output(self):
        bep = all_passed_bep()
        try:
            status, lines = self.run_main(bep, 0, extra_args=["--check", "cmd-dolt=failure"])
        finally:
            os.unlink(bep)
        self.assertEqual(status, 0)
        self.assertEqual(lines, {"server_storage_result=success", "cmd_dolt_result=failure"})

    def test_repeated_tier_name_is_rejected(self):
        bep = all_passed_bep()
        out = tempfile.NamedTemporaryFile(mode="w", delete=False)
        out.close()
        try:
            with self.assertRaises(SystemExit):
                ctr.main([
                    "--bep", bep,
                    "--rc", "0",
                    "--tier", f"server-storage={SS},{SS2}",
                    "--tier", f"cmd-dolt={CD},{CD2}",
                    "--tier", "cmd-dolt=//other:t",
                    "--gate-tier", "server-storage",
                    "--github-output", out.name,
                ])
        finally:
            os.unlink(bep)
            os.unlink(out.name)

    def test_repeated_gate_tier_is_rejected(self):
        bep = all_passed_bep()
        out = tempfile.NamedTemporaryFile(mode="w", delete=False)
        out.close()
        try:
            with self.assertRaises(SystemExit):
                ctr.main([
                    "--bep", bep,
                    "--rc", "0",
                    "--tier", f"server-storage={SS},{SS2}",
                    "--tier", f"cmd-dolt={CD},{CD2}",
                    "--gate-tier", "server-storage",
                    "--gate-tier", "cmd-dolt",
                    "--github-output", out.name,
                ])
        finally:
            os.unlink(bep)
            os.unlink(out.name)

    def test_repeated_check_name_is_rejected(self):
        bep = all_passed_bep()
        out = tempfile.NamedTemporaryFile(mode="w", delete=False)
        out.close()
        try:
            with self.assertRaises(SystemExit):
                ctr.main([
                    "--bep", bep,
                    "--rc", "0",
                    "--tier", f"server-storage={SS},{SS2}",
                    "--tier", f"cmd-dolt={CD},{CD2}",
                    "--gate-tier", "server-storage",
                    "--github-output", out.name,
                    "--check", "cmd-dolt=failure",
                    "--check", "cmd-dolt=success",
                ])
        finally:
            os.unlink(bep)
            os.unlink(out.name)

    def test_unknown_flag_is_rejected(self):
        bep = all_passed_bep()
        out = tempfile.NamedTemporaryFile(mode="w", delete=False)
        out.close()
        try:
            with self.assertRaises(SystemExit):
                ctr.main([
                    "--bep", bep,
                    "--rc", "0",
                    "--tier", f"server-storage={SS},{SS2}",
                    "--gate-tier", "server-storage",
                    "--github-output", out.name,
                    "--gate-tier-extra", "server-storage",
                ])
        finally:
            os.unlink(bep)
            os.unlink(out.name)

    def test_check_for_unknown_tier_name_is_rejected(self):
        bep = all_passed_bep()
        out = tempfile.NamedTemporaryFile(mode="w", delete=False)
        out.close()
        try:
            with self.assertRaises(SystemExit):
                ctr.main([
                    "--bep", bep,
                    "--rc", "0",
                    "--tier", f"server-storage={SS},{SS2}",
                    "--gate-tier", "server-storage",
                    "--github-output", out.name,
                    "--check", "not-a-tier=failure",
                ])
        finally:
            os.unlink(bep)
            os.unlink(out.name)


if __name__ == "__main__":
    unittest.main()
