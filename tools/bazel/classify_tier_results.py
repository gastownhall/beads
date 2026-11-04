#!/usr/bin/env python3
"""Classify a union `bazel test` run's result by tier, from its BEP.

Usage: classify_tier_results.py --bep <build_event_json_file> --rc <bazel exit code>
                                 --tier NAME=LABEL[,LABEL...] [--tier NAME=LABEL...]
                                 --gate-tier NAME --github-output <path>
                                 [--check NAME=success|failure|cancelled ...]

item 5 A2's bazel-dolt-int job runs two tiers (server-storage, cmd-dolt) in
one `bazel test --config=dolt-int` invocation. The job carries no job-level
continue-on-error: on push, nightly and dispatch, nothing reads
BAZEL_SERVER_STORAGE off a `workflow_call` output, so a job-level
continue-on-error (reading job.status as success regardless of the `bazel
test` step's own outcome) would let a server-storage failure end the run
green there, where it must stay red exactly as it would on main today. The
`bazel test` step instead carries continue-on-error, and this script -- an
always() step of its own -- computes each tier's result independently from
the BEP and decides whether the job itself must fail.

Bazel's own exit code governs first. 0 (ran, all passed) and 3 (ran, some
test failed) are the only codes this script trusts the BEP for: a build
failure, an infra error or a crash (any other code) cannot be safely
attributed to one tier's tests, so both tiers are marked "failure" and the
script always exits 1 for it (no silent cmd-dolt-only downgrade of a
not-actually-a-test-failure). A missing or non-integer --rc (bee-ghosttrack's
review, PR #7535 should-fix 2: the `bazel test` step's 25-minute timeout or a
cancellation before it can `echo "rc=$rc"` leaves bazel.yml's $TEST_RC empty)
takes this same branch rather than crashing argparse with no outputs
written: it is treated exactly like an exit code outside {0, 3}.

Otherwise each --tier is "failure" iff a BEP testResult for one of its
labels has a non-PASSED status (FAILED, TIMEOUT, REMOTE_FAILURE, INCOMPLETE,
FLAKY all count: these lanes run flaky=False, so a flaky-looking status is
still a failure), or if the BEP has no result at all for one of its labels
(an attempt that never ran cannot be presumed to have passed; a genuine
coverage gap already fails the job through check_testcases.py's own step).

Fail-closed on an unknown label (coordinator review, PR #7535 must-fix 1):
if the BEP has a non-PASSED result for any label that is not one of the
--tier lists (a newly `dolt-server-integration`/`dolt-server-cmd`-tagged
target added without updating the step's --tier args, i.e. bazel.yml and
this script's lists drifting from the BUILD files' tag sets -- see
TestBazelDoltIntTierLabelsMatchTheTags), every tier is marked "failure" and
the script exits 1: an un-enumerated failing target must not read as success
just because no --tier list claims it.

--check NAME=OUTCOME (repeatable) folds a per-tier shell check's own GitHub
Actions step outcome into that tier's result: "success" leaves the BEP-only
result alone, anything else (bazel.yml passes the step's own
`steps.<id>.outcome`: "failure" or "cancelled") forces that tier to
"failure" regardless of the BEP. This is how bazel.yml's two
"Every target and shard ran tests (<tier>)" steps (the shared
check_testcases.py split per tier, PR #7535 should-fix 2) report a zero-
testcase shard without going through job.status: cmd-dolt's check runs with
its own continue-on-error so it cannot fail the job directly, but its result
must still reach cmd_dolt_result.

Exit status is 1 iff --gate-tier's tier is "failure" (or the exit-code or
unknown-label branches above applied); a failure confined to another tier
leaves this script's own exit 0, so this step stays green, mirroring that
tier's own advisory, continue-on-error nature outside mode remote. (Where no
caller gate reads that tier's result -- outside pr.yml's call -- bazel.yml's
dolt-int job fails itself on it in a later step.) Every tier's result, win or lose, is appended to
--github-output as "<name>_result=success|failure" (NAME lowercased, "-"
turned into "_"), so the job can expose one output per tier.

Every flag may be given at most once, except --tier and --check (one per
distinct NAME: a repeated NAME, or an unrecognized flag, is a hard error --
coordinator review, PR #7535 must-fix 3 -- so a future edit cannot silently
widen or narrow the gate by appending an extra --tier/--gate-tier/--check
that argparse would otherwise just let the last value win).
"""

import argparse
import json
import sys

PASS_STATUS = "PASSED"


class _StoreOnce(argparse.Action):
    """Like the default "store" action, but errors if given more than once."""

    def __call__(self, parser, namespace, values, option_string=None):
        if getattr(namespace, self.dest, None) is not None:
            parser.error(f"{option_string} may only be given once")
        setattr(namespace, self.dest, values)


def read_test_statuses(bep_path):
    """Return {label: set(status seen across every testResult event)}."""
    statuses = {}
    with open(bep_path, encoding="utf-8") as f:
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
            label = tr_id["label"]
            statuses.setdefault(label, set()).add(tr.get("status", "NO_STATUS"))
    return statuses


def tier_result(statuses, labels):
    """Return "success" or "failure" for one tier's labels."""
    for label in labels:
        seen = statuses.get(label)
        if not seen or seen - {PASS_STATUS}:
            return "failure"
    return "success"


def output_key(name):
    return name.lower().replace("-", "_") + "_result"


def classify(bep_path, rc, tiers, gate_tier, checks=None):
    """Return ({tier: "success"|"failure"}, exit_status, reason|None).

    checks, if given, is {tier name: GitHub Actions step outcome}: any value
    other than "success" forces that tier to "failure" (see the module
    docstring's --check section). reason is None iff exit_status is 0.
    """
    checks = checks or {}
    try:
        rc = int(rc)
    except (TypeError, ValueError):
        reason = (
            f"bazel exit code {rc!r} is missing or not an integer: the test "
            "step likely timed out or was cancelled before it could record one"
        )
        return {name: "failure" for name in tiers}, 1, reason
    if rc not in (0, 3):
        reason = f"bazel exit {rc} (not 0 or 3): a build or infra failure, not attributable to one tier"
        return {name: "failure" for name in tiers}, 1, reason

    statuses = read_test_statuses(bep_path)

    known_labels = set()
    for labels in tiers.values():
        known_labels.update(labels)
    unknown_failed = sorted(
        label for label, seen in statuses.items()
        if label not in known_labels and (seen - {PASS_STATUS})
    )
    if unknown_failed:
        # A real test failure Bazel reported (rc 3) that no --tier list
        # claims: the tier/tag lists have drifted from what actually ran.
        # Fail everything shut rather than silently reading it as success.
        reason = f"non-PASSED result for a label outside every --tier: {', '.join(unknown_failed)}"
        return {name: "failure" for name in tiers}, 1, reason

    results = {}
    for name, labels in tiers.items():
        result = tier_result(statuses, labels)
        if checks.get(name, "success") != "success":
            result = "failure"
        results[name] = result
    if results.get(gate_tier) == "failure":
        return results, 1, f"{gate_tier} tier failed"
    return results, 0, None


def parse_tier_arg(spec):
    name, _, labels = spec.partition("=")
    if not name or not labels:
        raise argparse.ArgumentTypeError(f"--tier {spec!r}: want NAME=LABEL[,LABEL...]")
    return name, [l for l in labels.split(",") if l]


def parse_check_arg(spec):
    name, sep, outcome = spec.partition("=")
    if not name or not sep or not outcome:
        raise argparse.ArgumentTypeError(f"--check {spec!r}: want NAME=OUTCOME")
    return name, outcome


def _dict_no_duplicates(parser, pairs, flag):
    out = {}
    for name, value in pairs:
        if name in out:
            parser.error(f"{flag} {name!r} given more than once")
        out[name] = value
    return out


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--bep", required=True, action=_StoreOnce)
    # Not type=int: an empty or non-integer --rc (the test step's own
    # $TEST_RC left unset by a timeout or cancellation) must reach
    # classify()'s own handling below, not crash argparse with no outputs
    # written (should-fix 2).
    p.add_argument("--rc", required=True, action=_StoreOnce)
    p.add_argument("--tier", action="append", type=parse_tier_arg, default=[], dest="tiers", metavar="NAME=LABEL[,LABEL...]")
    p.add_argument("--gate-tier", required=True, action=_StoreOnce)
    p.add_argument("--github-output", required=True, action=_StoreOnce)
    p.add_argument("--check", action="append", type=parse_check_arg, default=[], dest="checks", metavar="NAME=OUTCOME")
    args = p.parse_args(argv)

    tiers = _dict_no_duplicates(p, args.tiers, "--tier")
    if not tiers:
        p.error("at least one --tier is required")
    if args.gate_tier not in tiers:
        p.error(f"--gate-tier {args.gate_tier!r} is not one of --tier's names {sorted(tiers)}")
    checks = _dict_no_duplicates(p, args.checks, "--check")
    for name in checks:
        if name not in tiers:
            p.error(f"--check {name!r} is not one of --tier's names {sorted(tiers)}")

    results, status, reason = classify(args.bep, args.rc, tiers, args.gate_tier, checks)

    with open(args.github_output, "a", encoding="utf-8") as f:
        for name, result in results.items():
            line = f"{output_key(name)}={result}"
            print(line)
            f.write(line + "\n")

    if status:
        print(f"::error::classify_tier_results: {reason}", file=sys.stderr)
    return status


if __name__ == "__main__":
    sys.exit(main())
