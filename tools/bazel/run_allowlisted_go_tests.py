#!/usr/bin/env python3
"""Run, under `go test`, the Go tests the Bazel PR-core lane does not run.

tools/bazel/equivalence_allowlist.txt lists every Go test that PR Core's
`go test` runs and `bazel test //... --config=ci` does not run (kind "run")
or skips (kind "skip"), each with a reason. Where pr.yml's legacy PR Core job
stands down for the Bazel lane (D2 step 3), nothing else would run those
tests before merge, so this runs exactly them, with PR Core's test flags
(-short, -skip '^TestEmbedded', gms_pure_go; not -race: the build is shared
with the job's other non-race `go test` steps), and requires each entry to
have matched at least one top-level test and every matched test to have
passed: a test that now skips, fails or no longer exists fails the step
instead of passing silently.

A package glob is refused (`go test` needs real package paths); a test name
of `*` runs the whole package. Call it through
scripts/ci/allowlisted-go-tests.sh, which gives the tests PR Core's
environment (scripts/ci/lib/test-env.sh).

Usage: run_allowlisted_go_tests.py [--allowlist FILE] [--go GO] [--dry-run]
"""
import argparse
import json
import os
import re
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from equivalence import DEFAULT_ALLOWLIST, load_allowlist  # noqa: E402

# The build tag(s) PR Core's test flags apply (tags come from .buildflags'
# GOFLAGS too). Factored out so --compile-only's `go test -c` command below
# can reuse it directly instead of slicing GO_TEST_FLAGS (F7b review fix N1:
# `GO_TEST_FLAGS[-2:]` silently assumed these were always the last two
# elements of GO_TEST_FLAGS; any future reordering or addition there would
# have fed `-c` the wrong flags with no error, just a tag-less compile).
BUILD_FLAGS = ["-tags", "gms_pure_go"]
# PR Core's test flags (scripts/ci/pr-core.sh), less -race, -p/-parallel and
# -timeout's package count.
GO_TEST_FLAGS = ["-short", "-count=1", "-timeout=30m", "-skip", "^TestEmbedded", *BUILD_FLAGS]
# Tests an allowlisted test needs in the same process, run alongside it:
# TestZZStdioNotLeaked compares against the streams TestAAAStdioBaseline
# recorded (and skips without it), which is why Bazel's sharding skips it.
COMPANIONS = {("cmd/bd", "TestZZStdioNotLeaked"): ["TestAAAStdioBaseline"]}
NAME_RE = re.compile(r"^[A-Za-z0-9_*]+$")
PKG_RE = re.compile(r"^[A-Za-z0-9_./-]+$")


def plan(entries):
    """{pkg: [entry, ...]} in allowlist order, or raise ValueError."""
    out = {}
    for e in entries:
        if not PKG_RE.match(e["pkg"]) or e["pkg"].startswith(("/", "..")) or "*" in e["pkg"]:
            raise ValueError(f"allowlist line {e['line']}: package {e['pkg']!r} is not a literal package directory")
        if not NAME_RE.match(e["test"]):
            raise ValueError(f"allowlist line {e['line']}: test {e['test']!r} is not a Test name or glob")
        out.setdefault(e["pkg"], []).append(e)
    return out


def glob_re(name):
    return re.compile("^" + ".*".join(re.escape(p) for p in name.split("*")) + "$")


def run_regex(group):
    """-run selector for a package's entries, or None for the whole package."""
    if any(e["test"] == "*" for e in group):
        return None
    names = []
    for e in group:
        names += COMPANIONS.get((e["pkg"], e["test"]), []) + [e["test"]]
    return "^(" + "|".join(".*".join(re.escape(p) for p in n.split("*")) for n in names) + ")$"


def top_level_results(output):
    """{test: final action} for top-level tests in `go test -json` output."""
    res = {}
    for line in output.splitlines():
        if not line.startswith("{"):
            continue
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        test, action = ev.get("Test"), ev.get("Action")
        if test and "/" not in test and action in ("pass", "fail", "skip"):
            res[test] = action
    return res


def check(group, results):
    """Problems for one package's entries given its top-level results."""
    problems = []
    for e in group:
        matched = sorted(t for t in results if glob_re(e["test"]).match(t))
        if not matched:
            problems.append(f"{e['pkg']} {e['test']} (allowlist line {e['line']}): matched no test that ran")
        for t in matched:
            if results[t] != "pass":
                problems.append(f"{e['pkg']} {t} (allowlist line {e['line']}): {results[t]}, want pass")
    for t, action in sorted(results.items()):
        if action == "fail":
            problems.append(f"{group[0]['pkg']} {t}: fail")
    return problems


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--allowlist", default=DEFAULT_ALLOWLIST)
    ap.add_argument("--go", default="go")
    ap.add_argument("--dry-run", action="store_true", help="print the go test commands only")
    ap.add_argument(
        "--compile-only",
        action="store_true",
        help=(
            "compile each allowlisted package's test binary (go test -c -o "
            "/dev/null) instead of running it; used by main.yml's "
            "blacksmith-go-build-cache seed to warm the non-race GOCACHE for "
            "exactly the packages this script's real run needs, with zero "
            "risk of the package list drifting from the allowlist (F7b)."
        ),
    )
    args = ap.parse_args(argv)

    if not os.path.exists(args.allowlist):
        print(f"{args.allowlist}: no such allowlist", file=sys.stderr)
        return 1
    entries, errors = load_allowlist(args.allowlist)
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1
    try:
        pkgs = plan(entries)
    except ValueError as err:
        print(err, file=sys.stderr)
        return 1

    if args.compile_only:
        problems = []
        for pkg in pkgs:
            cmd = [args.go, "test", "-c", *BUILD_FLAGS, "-o", os.devnull, "./" + pkg]
            print("+ " + " ".join(cmd), flush=True)
            if args.dry_run:
                continue
            proc = subprocess.run(cmd)
            if proc.returncode != 0:
                problems.append(f"{pkg}: go test -c exited {proc.returncode}")
        if args.dry_run:
            return 0
        if problems:
            print("\nFAIL: compiling allowlisted packages' test binaries:", file=sys.stderr)
            print("\n".join("  " + p for p in problems), file=sys.stderr)
            return 1
        print(f"\nok: compiled {len(pkgs)} allowlisted package test binaries")
        return 0

    problems, ran = [], 0
    for pkg, group in pkgs.items():
        cmd = [args.go, "test", "-json", *GO_TEST_FLAGS]
        sel = run_regex(group)
        if sel is not None:
            cmd += ["-run", sel]
        cmd.append("./" + pkg)
        print("+ " + " ".join(cmd), flush=True)
        if args.dry_run:
            continue
        proc = subprocess.run(cmd, capture_output=True, text=True)
        for line in proc.stdout.splitlines():
            try:
                out = json.loads(line).get("Output")
            except ValueError:
                out = line + "\n"
            if out:
                sys.stdout.write(out)
        sys.stdout.write(proc.stderr)
        results = top_level_results(proc.stdout)
        ran += len(results)
        problems += check(group, results)
        if proc.returncode != 0:
            problems.append(f"{pkg}: go test exited {proc.returncode}")
    if args.dry_run:
        return 0
    if problems:
        print("\nFAIL: the Go tests the Bazel PR-core lane does not run did not all pass here:", file=sys.stderr)
        print("\n".join("  " + p for p in problems), file=sys.stderr)
        return 1
    print(f"\nok: {len(entries)} allowlist entries, {ran} top-level tests passed under go test")
    return 0


if __name__ == "__main__":
    sys.exit(main())
