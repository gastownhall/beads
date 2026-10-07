#!/usr/bin/env python3
"""Unit tests for shard_budget.py.

Run directly: python3 tools/bazel/shard_budget_test.py
(or: python3 -m unittest tools.bazel.shard_budget_test, from the repo root)
"""

import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import shard_budget  # noqa: E402


def write_bep(events):
    f = tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False, encoding="utf-8")
    for ev in events:
        f.write(json.dumps(ev) + "\n")
    f.close()
    return f.name


def test_result_event(label, shard, ms, cached=False, cached_remotely=False):
    return {
        "id": {"testResult": {"label": label, "shard": shard}},
        "testResult": {
            "testAttemptDurationMillis": str(ms),
            "cachedLocally": cached,
            "executionInfo": {"cachedRemotely": cached_remotely, "strategy": "remote"},
        },
    }


class ReadShardDurationsTest(unittest.TestCase):
    def test_collects_executed_durations_per_shard(self):
        path = write_bep([
            test_result_event("//x:t", 1, 10_000),
            test_result_event("//x:t", 2, 20_000),
        ])
        try:
            got = shard_budget.read_shard_durations(path)
        finally:
            os.unlink(path)
        self.assertEqual(got, {"//x:t": {1: 10_000, 2: 20_000}})

    def test_excludes_cached_results(self):
        path = write_bep([
            test_result_event("//x:t", 1, 10_000),
            test_result_event("//x:t", 2, 999_000, cached_remotely=True),
        ])
        try:
            got = shard_budget.read_shard_durations(path)
        finally:
            os.unlink(path)
        # Shard 2 was a cache hit: it contributes no duration.
        self.assertEqual(got, {"//x:t": {1: 10_000}})

    def test_unsharded_defaults_to_shard_1(self):
        path = write_bep([test_result_event("//x:t", None, 5_000)])
        try:
            got = shard_budget.read_shard_durations(path)
        finally:
            os.unlink(path)
        self.assertEqual(got, {"//x:t": {1: 5_000}})

    def test_ignores_non_test_result_lines(self):
        path = write_bep([{"id": {"progress": {}}, "progress": {}}])
        try:
            got = shard_budget.read_shard_durations(path)
        finally:
            os.unlink(path)
        self.assertEqual(got, {})

    def test_malformed_line_is_skipped(self):
        f = tempfile.NamedTemporaryFile(mode="w", suffix=".json", delete=False, encoding="utf-8")
        f.write("not json\n")
        f.write(json.dumps(test_result_event("//x:t", 1, 1_000)) + "\n")
        f.close()
        try:
            got = shard_budget.read_shard_durations(f.name)
        finally:
            os.unlink(f.name)
        self.assertEqual(got, {"//x:t": {1: 1_000}})


class EvaluateTest(unittest.TestCase):
    def test_balanced_shards_do_not_warn(self):
        # gascity #7241-shaped data: every shard within a few seconds of the
        # others never crosses max(150s, 2x median).
        results = shard_budget.evaluate({"//x:t": {i: 60_000 + i * 1000 for i in range(1, 11)}})
        self.assertEqual(len(results), 1)
        r = results[0]
        self.assertFalse(r.warn)
        self.assertFalse(r.fail)

    def test_single_long_pole_warns(self):
        # gascity acceptance shard 4 shape: one shard far above the rest.
        shards = {i: 80_000 for i in range(1, 10)}
        shards[4] = 725_000
        results = shard_budget.evaluate({"//acceptance:t": shards})
        r = results[0]
        self.assertTrue(r.warn)
        self.assertEqual(r.slowest_shard, 4)
        self.assertAlmostEqual(r.slowest_ms, 725_000)

    def test_fail_threshold_is_stricter_than_warn(self):
        # beads server-Dolt storage tier shape: a straggler 4.3-4.5 min
        # behind an otherwise ~80s median. Over the warn floor (150s, 2x
        # median) but under the fail floor (300s, 3x median) until it grows
        # further.
        shards = {i: 80_000 for i in range(1, 16)}
        shards[16] = 250_000
        r = shard_budget.evaluate({"//storage/dolt:t": shards})[0]
        self.assertTrue(r.warn)
        self.assertFalse(r.fail)

        shards[16] = 400_000
        r = shard_budget.evaluate({"//storage/dolt:t": shards})[0]
        self.assertTrue(r.warn)
        self.assertTrue(r.fail)

    def test_floor_applies_below_tiny_medians(self):
        # A fast, lopsided split (two 5s shards, one 200s shard) must still
        # warn even though 2x the 5s median is nowhere near the slowest
        # shard; the 150s floor catches it, not the ratio.
        r = shard_budget.evaluate({"//x:t": {1: 5_000, 2: 5_000, 3: 200_000}})[0]
        self.assertTrue(r.warn)

    def test_fewer_than_two_shards_is_skipped(self):
        self.assertEqual(shard_budget.evaluate({"//x:t": {1: 10_000}}), [])
        self.assertEqual(shard_budget.evaluate({"//x:t": {}}), [])

    def test_sorted_slowest_first(self):
        results = shard_budget.evaluate({
            "//a:t": {1: 10_000, 2: 20_000},
            "//b:t": {1: 10_000, 2: 500_000},
        })
        self.assertEqual([r.label for r in results], ["//b:t", "//a:t"])


class MainTest(unittest.TestCase):
    def test_warn_only_exits_zero(self):
        shards = {i: 80_000 for i in range(1, 10)}
        shards[4] = 900_000
        events = [test_result_event("//x:t", i, ms) for i, ms in shards.items()]
        path = write_bep(events)
        try:
            rc = shard_budget.main(["--bep", path])
        finally:
            os.unlink(path)
        self.assertEqual(rc, 0)

    def test_fail_on_imbalance_exits_nonzero_past_fail_threshold(self):
        shards = {i: 80_000 for i in range(1, 10)}
        shards[4] = 900_000
        events = [test_result_event("//x:t", i, ms) for i, ms in shards.items()]
        path = write_bep(events)
        try:
            rc = shard_budget.main(["--bep", path, "--fail-on-imbalance"])
        finally:
            os.unlink(path)
        self.assertEqual(rc, 1)

    def test_fail_on_imbalance_still_zero_under_fail_threshold(self):
        shards = {i: 80_000 for i in range(1, 10)}
        shards[4] = 200_000
        events = [test_result_event("//x:t", i, ms) for i, ms in shards.items()]
        path = write_bep(events)
        try:
            rc = shard_budget.main(["--bep", path, "--fail-on-imbalance"])
        finally:
            os.unlink(path)
        self.assertEqual(rc, 0)


if __name__ == "__main__":
    unittest.main()
