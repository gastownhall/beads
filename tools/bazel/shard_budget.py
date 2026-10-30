#!/usr/bin/env python3
"""Warn (or fail) when a sharded test's slowest shard is far above its median.

Usage: shard_budget.py --bep <build_event_json_file> [--fail-on-imbalance]

Rationale (rbe-ci-cost-latency-study.md, recommendation 4): an unbalanced
shard split quietly grows the critical path of whichever lane runs it. gascity
#7233/#7241 showed the gain of re-splitting a hot shard; this script keeps
that gain from regressing and catches the tier this repository has today:
//internal/storage/dolt:dolt_server_full_test's last shard has trailed the
rest by 4.3-4.5 min in 2 of 8 runs (study Section 3.4).

Reads one `bazel test` invocation's --build_event_json_file (the same file
tools/bazel/check_testcases.py and check_shard_coverage.py already read) and,
for every sharded test target (shard_count > 1) that this invocation actually
executed at least two shards of, computes:

    median  = the median wall time of the shards that executed (not a cache
              hit; a cached shard's duration says nothing about balance)
    slowest = the maximum of those

A shard with no executed result (every attempt was a cache hit) is left out
of both: it ran nowhere this invocation. A target with fewer than two shards
left is skipped (not enough evidence this run).

Thresholds (study Section 6.4, picked from its shard data):

    warn when slowest > max(150s, 2 x median)
    fail when slowest > max(300s, 3 x median)   -- only with --fail-on-imbalance

Day one: this prints `::warning` and always exits 0 (CI must not block on
it yet). Pass --fail-on-imbalance (a workflow step can gate this behind a
repository variable) to exit 1 and print `::error` instead, once the budget
has run clean for a while.

--label (repeatable) restricts which sharded targets this invocation judges,
by exact label. A union job's BEP has every member's shards in it; pass each
label whose owning lane's job this script runs in (item 5 A2:
bazel-dolt-int's step passes only server-storage's
//internal/storage/dolt:dolt_server_full_test, so it keeps judging that tier
alone and not cmd-dolt's bd_dolt_server_test / regression_test shards, which
no job budgeted before the union and which this script has never judged).
Omitted (bazel-server-storage's own step, unchanged): every sharded target in
the BEP, as before.

A --label naming a target with no testResult event in the BEP at all (cached
or executed) is a workflow authoring error (a typo, or a target renamed out
from under a pinned step), not a budget outcome: the step prints ::error and
exits 1 rather than silently reporting "no sharded test target executed two
or more shards" (PR #7535 review, should-fix 3, carried across both rounds).
A label that is in the BEP but had every shard served from cache this run --
so it has zero executed shards, same as a label with only one -- is not an
error: a fully cache-hit invocation is a legitimate outcome evaluate()
already reports as "nothing to judge" for that target, same as before.
"""

import argparse
import json
import statistics
import sys

WARN_FLOOR_S = 150.0
WARN_FACTOR = 2.0
FAIL_FLOOR_S = 300.0
FAIL_FACTOR = 3.0


def read_shard_durations(path):
    """Return {label: {shard_index: executed_duration_ms}} from a BEP file.

    Only testResult events are read (as equivalence.py's read_bep does); a
    shard's duration is the longest executed (non-cached) attempt reported
    for it. shard defaults to 1 (an unsharded test), matching Bazel's BEP,
    which omits the field for shard_count == 1.
    """
    shards = {}
    with open(path, encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            tr_id = ev.get("id", {}).get("testResult")
            tr = ev.get("testResult")
            if not tr_id or not tr:
                continue
            cached = bool(tr.get("cachedLocally")) or bool(
                (tr.get("executionInfo") or {}).get("cachedRemotely")
            )
            if cached:
                continue
            label = tr_id["label"]
            shard = int(tr_id.get("shard") or 1)
            ms = int(tr.get("testAttemptDurationMillis") or 0)
            per_label = shards.setdefault(label, {})
            per_label[shard] = max(per_label.get(shard, 0), ms)
    return shards


def read_tested_labels(path):
    """Return the set of every label with at least one testResult event in
    the BEP, executed or cached.

    Separate from read_shard_durations, which only counts executed (non-
    cached) attempts: a target whose every shard was a cache hit this
    invocation has no entry in read_shard_durations at all, which is a
    legitimate "nothing to judge" outcome (evaluate() already skips it),
    not a --label typo. This set is what --label's fail-closed check
    (main()) tests against instead, so a fully-cached run does not falsely
    report an unknown label.
    """
    labels = set()
    with open(path, encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            tr_id = ev.get("id", {}).get("testResult")
            tr = ev.get("testResult")
            if not tr_id or not tr:
                continue
            labels.add(tr_id["label"])
    return labels


class Imbalance:
    """One sharded test target's balance verdict."""

    def __init__(self, label, shards, median_ms, slowest_ms, slowest_shard):
        self.label = label
        self.shards = shards
        self.median_ms = median_ms
        self.slowest_ms = slowest_ms
        self.slowest_shard = slowest_shard

    @property
    def warn_threshold_ms(self):
        return max(WARN_FLOOR_S * 1000, WARN_FACTOR * self.median_ms)

    @property
    def fail_threshold_ms(self):
        return max(FAIL_FLOOR_S * 1000, FAIL_FACTOR * self.median_ms)

    @property
    def warn(self):
        return self.slowest_ms > self.warn_threshold_ms

    @property
    def fail(self):
        return self.slowest_ms > self.fail_threshold_ms

    def message(self):
        return (
            f"{self.label}: shard {self.slowest_shard} of {self.shards} took "
            f"{self.slowest_ms / 1000:.0f}s against a median of {self.median_ms / 1000:.0f}s "
            f"(warn over {self.warn_threshold_ms / 1000:.0f}s, fail over {self.fail_threshold_ms / 1000:.0f}s)"
        )


def evaluate(shard_durations, labels=None):
    """Return a sorted list of Imbalance for every target with >= 2 executed
    shards this invocation, whether or not it crosses the warn threshold.

    labels, if given (a set), restricts evaluation to those exact labels: a
    union job's BEP covers every member's shards, and a caller that only
    owns one member's shard budget passes its labels so the other member's
    shards are never judged here.
    """
    out = []
    for label, per_shard in sorted(shard_durations.items()):
        if labels is not None and label not in labels:
            continue
        if len(per_shard) < 2:
            continue
        durations = list(per_shard.values())
        median_ms = statistics.median(durations)
        slowest_ms = max(durations)
        slowest_shard = max(per_shard, key=per_shard.get)
        out.append(Imbalance(label, len(per_shard), median_ms, slowest_ms, slowest_shard))
    out.sort(key=lambda i: i.slowest_ms, reverse=True)
    return out


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--bep", required=True, help="--build_event_json_file of the bazel test run")
    ap.add_argument(
        "--fail-on-imbalance",
        action="store_true",
        help="exit 1 (and print ::error) past the fail threshold; default is warn-only",
    )
    ap.add_argument(
        "--label",
        action="append",
        default=None,
        help="restrict to this exact target label (repeatable); default: every sharded target in the BEP",
    )
    args = ap.parse_args(argv)

    shard_durations = read_shard_durations(args.bep)

    failed = False
    if args.label:
        tested = read_tested_labels(args.bep)
        for label in sorted(set(args.label) - tested):
            failed = True
            print(f"::error title=shard budget::{label}: --label, but the BEP has no result for it")

    results = evaluate(shard_durations, labels=set(args.label) if args.label else None)

    for r in results:
        if r.fail and args.fail_on_imbalance:
            failed = True
            print(f"::error title=shard budget::{r.message()}")
        elif r.warn:
            print(f"::warning title=shard budget::{r.message()}")
        else:
            print(f"ok: {r.message()}")

    if not results:
        print("shard budget: no sharded test target executed two or more shards in this invocation")

    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
