#!/usr/bin/env python3
"""Check that `bazel test --config=ci` ran the same Go tests `go test` would.

The Bazel lane is only a safe replacement for the `go test` lanes in pr.yml
(PR Core runs `go test -race -short -skip '^TestEmbedded' ./...`) if it runs
the same tests. A tag filter, a stray `manual` tag, a package gazelle never
picked up, or a sharding bug that makes a shard run zero tests all shrink the
Bazel set silently: every target Bazel did run is still green. This script
makes that shrinkage a failure.

Two checks always run, a third when `go test -json` output is supplied:

1. Package set. Every package `go list` reports with test files must have a
   go_test target (`bazel query 'kind("go_test rule", //...)'`).
2. Test-name set. For every such package, each top-level `func TestXxx(t
   *testing.T)` in the files `go list` selects (the same -race and
   gms_pure_go build context as PR Core, minus the `^TestEmbedded` skip) must
   appear as a <testcase> in the test.xml of a go_test target that this Bazel
   invocation tested (read from --build_event_json_file). Bazel testcases
   that `go test` would not run are reported too. This check only sees names:
   a test that runs and passes under `go test` but calls t.Skip under Bazel
   (a missing env var, no git checkout, a file that is not declared as data)
   still counts as present here.
3. Skip parity (--go-test-json, nightly). Given the `go test -json` output of
   the PR Core command on the same commit, every top-level test that passed
   under `go test` and was skipped under Bazel is a divergence. The Bazel lane
   alone cannot see this (a skip looks the same whether or not `go test` also
   skips), so the nightly workflow runs PR Core's `go test -json` and passes
   it in. Other status differences (pass/fail either way, skipped under go
   test but run under Bazel) are reported, not failed: a failing test already
   fails its own lane.

Job mode (--job, with --go-test-json): the go test JSON is the whole expected
set instead of `go list ./...`. Use it to prove that a Bazel selection (one or
more --bep runs, e.g. --config=ci plus --config=docker) replaces one specific
CI job: every top-level test the job's command ran must have run under Bazel,
Bazel must run no other test in those packages, and no test the job passed may
be skipped under Bazel. A test seen in several targets (a go_test and its
go_test_variant.sh sh_test) keeps its best status here, since each variant is
a different way of running it and the job needs one run that does what it
does; the default mode keeps the worst. A failure in any target still fails a
job comparison (listed per target), and so does a missing test.xml of any
target in the job's packages, variant or not.

Divergences listed in the allowlist (tools/bazel/equivalence_allowlist.txt)
are expected; anything else fails. The allowlist format is one entry per line:

    <package dir> <TestName or glob>        # why Bazel does not run it
    <package dir> <TestName or glob> skip   # why Bazel skips a test go test runs

The first form (kind "run") covers the name checks; `*` as the test name
matches the whole package (including the package-set check). The `skip` form
covers only check 3. Every entry needs a justification after `#`.

Requires: go (with the module cache the packages need), bazel for the query
unless --no-query, and the BEP JSON plus bazel-testlogs of a `--config=ci`
run (which sets GO_TEST_WRAP_TESTV=1 and downloads test.xml).
"""
import argparse
import fnmatch
import json
import os
import re
import subprocess
import sys
import xml.etree.ElementTree as ET

GO_LIST_ARGS = ["list", "-e", "-race", "-tags", "gms_pure_go", "-json", "./..."]
# pr.yml's go test lanes pass -skip '^TestEmbedded' (as does test:prcore).
SKIP_RE = re.compile(r"^TestEmbedded")
TEST_FUNC_RE = re.compile(r"^func\s+(Test\w*)\s*\(\s*\w+\s+\*testing\.T\s*\)", re.M)
STATUS_RANK = {"skipped": 0, "passed": 1, "failed": 2}
# Job mode: the variant that actually ran a test wins (a failure still fails
# the Bazel run that produced it).
BEST_RANK = {"failed": 0, "skipped": 1, "passed": 2}
GO_TEST_STATUS = {"pass": "passed", "fail": "failed", "skip": "skipped"}
ALLOW_KINDS = ("run", "skip")
DEFAULT_ALLOWLIST = os.path.join(os.path.dirname(os.path.abspath(__file__)), "equivalence_allowlist.txt")


def is_go_test_name(name):
    # cmd/go: "TestXxx" where Xxx does not start with a lower-case letter.
    return name == "Test" or not name[4].islower()


def go_expected(root, go_json_path=None):
    """Return ({pkg_dir: set(test names)} for packages with test files,
    {import path: pkg_dir} for every package)."""
    if go_json_path:
        with open(go_json_path, encoding="utf-8") as f:
            raw = f.read()
    else:
        raw = subprocess.run(["go", *GO_LIST_ARGS], cwd=root, check=True, capture_output=True, text=True).stdout
    decoder = json.JSONDecoder()
    pkgs, idx = [], 0
    while idx < len(raw):
        while idx < len(raw) and raw[idx].isspace():
            idx += 1
        if idx >= len(raw):
            break
        obj, idx = decoder.raw_decode(raw, idx)
        pkgs.append(obj)

    expected, dirs = {}, {}
    for p in pkgs:
        rel = os.path.relpath(p["Dir"], root)
        rel = "" if rel == "." else rel.replace(os.sep, "/")
        if p.get("ImportPath"):
            dirs[p["ImportPath"]] = rel
        files = (p.get("TestGoFiles") or []) + (p.get("XTestGoFiles") or [])
        if not files:
            continue
        names = set()
        for name in files:
            with open(os.path.join(p["Dir"], name), encoding="utf-8", errors="replace") as f:
                for m in TEST_FUNC_RE.finditer(f.read()):
                    t = m.group(1)
                    if is_go_test_name(t) and not SKIP_RE.search(t):
                        names.add(t)
        expected[rel] = names
    return expected, dirs


def go_test_statuses(path, dirs):
    """Return {pkg_dir: {test: status}} for top-level tests in `go test -json`
    output. Packages go list does not know are skipped."""
    out = {}
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            if not line.startswith("{"):
                continue
            try:
                ev = json.loads(line)
            except ValueError:
                continue
            test, action = ev.get("Test"), ev.get("Action")
            if not test or "/" in test or action not in GO_TEST_STATUS:
                continue
            pkg = dirs.get(ev.get("Package", ""))
            if pkg is None:
                continue
            out.setdefault(pkg, {})[test] = GO_TEST_STATUS[action]
    return out


def label_pkg(label):
    # "//cmd/bd:bd_test" -> "cmd/bd", "@@//:x" -> ""
    label = label.lstrip("@")
    return label[2:].split(":", 1)[0] if label.startswith("//") else label.split(":", 1)[0]


def query_go_test_pkgs(root, bazel):
    out = subprocess.run(
        [bazel, "query", 'kind("go_test rule", //...)', "--output=label"],
        cwd=root, check=True, capture_output=True, text=True,
    ).stdout
    return {label_pkg(l) for l in out.split()}


# go_test_variant.sh variants are sh_tests that run a go_test binary, which
# writes the same test.xml.
TEST_KINDS = ("go_test rule", "sh_test rule")


def read_bep(path):
    """Return ({label: shard_count} for tested go_test targets and sh_test
    variants, set of go_test labels configured, this invocation's testlogs dir
    or None)."""
    go_tests, tests, shards = set(), set(), {}
    exec_root, testlogs_rel = None, None
    with open(path, encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            ev = json.loads(line)
            eid = ev.get("id", {})
            if "workspace" in eid:
                exec_root = ev.get("workspaceInfo", {}).get("localExecRoot")
            elif "convenienceSymlinksIdentified" in eid:
                for link in ev.get("convenienceSymlinksIdentified", {}).get("convenienceSymlinks", []):
                    if link.get("path", "").endswith("testlogs") and link.get("target"):
                        testlogs_rel = link["target"]
            elif "targetConfigured" in eid:
                kind = ev.get("configured", {}).get("targetKind")
                if kind in TEST_KINDS:
                    tests.add(eid["targetConfigured"]["label"])
                if kind == "go_test rule":
                    go_tests.add(eid["targetConfigured"]["label"])
            elif "testResult" in eid:
                tr = eid["testResult"]
                shards[tr["label"]] = max(shards.get(tr["label"], 0), int(tr.get("shard", 1) or 1))
    tested = {l: n for l, n in shards.items() if l in tests}
    testlogs = None
    if exec_root and testlogs_rel:
        # The symlink target is relative to the output base, two levels above
        # the exec root (<output_base>/execroot/<workspace>).
        testlogs = os.path.join(os.path.dirname(os.path.dirname(exec_root)), testlogs_rel)
    return tested, go_tests, testlogs


def testlog_xmls(testlogs, label, shard_count):
    pkg = label_pkg(label)
    name = label.split(":", 1)[1]
    base = os.path.join(testlogs, pkg, name)
    if shard_count <= 1:
        return [os.path.join(base, "test.xml")]
    return [os.path.join(base, f"shard_{i}_of_{shard_count}", "test.xml") for i in range(1, shard_count + 1)]


def bazel_observed(tested, testlogs, go_tests, best=False, observed=None, failures=None, no_xml=None):
    """Return ({pkg: {test: status}}, [problems]), merged into observed.

    Job mode (best) also collects (pkg, "Test (label)") for every failed test
    in failures and (pkg, label) for every missing test.xml in no_xml."""
    observed, problems = ({} if observed is None else observed), []
    for label, n in sorted(tested.items()):
        pkg = label_pkg(label)
        per = observed.setdefault(pkg, {})
        for xml_path in testlog_xmls(testlogs, label, n):
            if not os.path.exists(xml_path):
                if best and no_xml is not None:
                    # Bazel writes a test.xml for every test, so a variant's
                    # is missing only if it was never downloaded.
                    no_xml.append((pkg, f"{label}: {os.path.relpath(xml_path, testlogs)}"))
                    continue
                if label not in go_tests:
                    continue  # an sh_test that is not a variant may write none
                problems.append(f"{label}: {xml_path} missing (was test.xml downloaded? run with --config=ci)")
                continue
            try:
                tree = ET.parse(xml_path)
            except ET.ParseError as e:
                problems.append(f"{label}: cannot parse {xml_path}: {e}")
                continue
            cases = tree.getroot().iter("testcase")
            count = 0
            for tc in cases:
                name = tc.get("name", "")
                if "/" in name or not name.startswith("Test"):
                    continue
                count += 1
                if tc.find("failure") is not None or tc.find("error") is not None:
                    status = "failed"
                elif tc.find("skipped") is not None:
                    status = "skipped"
                else:
                    status = "passed"
                if status == "failed" and failures is not None:
                    failures.append((pkg, f"{name} ({label})"))
                # A test seen in several targets keeps its worst status (best in
                # job mode).
                rank = BEST_RANK if best else STATUS_RANK
                if name not in per or rank[status] > rank[per[name]]:
                    per[name] = status
            if count == 0 and label in go_tests and os.path.getsize(xml_path) < 64:
                # An empty <testsuites/> means the binary ran no tests at all
                # (or GO_TEST_WRAP_TESTV was off). The test-name diff reports
                # which tests are missing; note it here for the log.
                problems.append(f"{label}: {os.path.relpath(xml_path, testlogs)} lists no testcases")
    return observed, problems


def load_allowlist(path):
    entries, errors = [], []
    if not path or not os.path.exists(path):
        return entries, errors
    with open(path, encoding="utf-8") as f:
        for n, line in enumerate(f, 1):
            body, _, why = line.partition("#")
            body = body.strip()
            if not body:
                continue
            fields = body.split()
            kind = fields[2] if len(fields) == 3 else "run"
            if len(fields) not in (2, 3) or kind not in ALLOW_KINDS or not why.strip():
                errors.append(
                    f"{path}:{n}: want '<package dir> <test glob> [skip]  # justification', got {line.strip()!r}"
                )
                continue
            entries.append({"pkg": fields[0], "test": fields[1], "kind": kind, "why": why.strip(), "line": n, "used": False})
    return entries, errors


def allowed(entries, pkg, test, kind="run"):
    for e in entries:
        if e["kind"] == kind and fnmatch.fnmatchcase(pkg, e["pkg"]) and fnmatch.fnmatchcase(test, e["test"]):
            e["used"] = True
            return True
    return False


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument(
        "--bep", required=True, action="append",
        help="--build_event_json_file of a bazel test run (repeatable: results are merged)",
    )
    ap.add_argument("--root", default=os.getcwd(), help="workspace root (default: cwd)")
    ap.add_argument("--testlogs", default=None, help="default: from the BEP, else <root>/bazel-testlogs")
    ap.add_argument("--allowlist", default=DEFAULT_ALLOWLIST)
    ap.add_argument("--go-list-json", default=None, help="precomputed `go list -json` output (tests)")
    ap.add_argument(
        "--go-test-json", default=None,
        help="`go test -json` output of the PR Core command on the same commit; enables the skip-parity check",
    )
    ap.add_argument("--bazel", default=os.environ.get("BAZEL", "bazel"))
    ap.add_argument("--no-query", action="store_true", help="skip the bazel query package-set check")
    ap.add_argument("--details", action="store_true", help="list every divergence (nightly)")
    ap.add_argument("--summary", default=None, help="append Markdown here (default: $GITHUB_STEP_SUMMARY)")
    ap.add_argument(
        "--job", action="store_true",
        help="compare with --go-test-json as one CI job's complete test set (see Job mode above)",
    )
    args = ap.parse_args(argv)
    if args.job and not args.go_test_json:
        ap.error("--job needs --go-test-json")

    root = os.path.abspath(args.root)
    entries, allow_errors = load_allowlist(args.allowlist)

    expected, dirs = go_expected(root, args.go_list_json)
    observed, problems, configured, tested = {}, [], set(), {}
    failures, no_xml = [], []
    for bep in args.bep:
        bep_tested, bep_configured, bep_testlogs = read_bep(bep)
        configured |= bep_configured
        tested.update(bep_tested)
        # Prefer the BEP's own testlogs dir: the bazel-testlogs symlink follows
        # whatever bazel command ran last, possibly in another output base.
        testlogs = args.testlogs or bep_testlogs or os.path.join(root, "bazel-testlogs")
        _, bep_problems = bazel_observed(
            bep_tested, testlogs, bep_configured, args.job, observed,
            failures if args.job else None, no_xml if args.job else None)
        problems += bep_problems
    target_pkgs = None if (args.no_query or args.job) else query_go_test_pkgs(root, args.bazel)
    go_status = go_test_statuses(args.go_test_json, dirs) if args.go_test_json else None
    if args.job:
        # The job's own results are the expected set; other packages Bazel
        # ran are out of scope.
        expected = {pkg: set(ts) for pkg, ts in go_status.items()}
        observed = {pkg: seen for pkg, seen in observed.items() if pkg in expected}
        failures = sorted(set(f for f in failures if f[0] in expected))
        no_xml = sorted(set(m for m in no_xml if m[0] in expected))

    no_target, missing, extra, allowlisted = [], [], [], []
    if target_pkgs is not None:
        for pkg in sorted(set(expected) - target_pkgs):
            (allowlisted if allowed(entries, pkg, "*") else no_target).append((pkg, "*"))
    for pkg, names in sorted(expected.items()):
        seen = observed.get(pkg, {})
        for t in sorted(names - set(seen)):
            (allowlisted if allowed(entries, pkg, t) else missing).append((pkg, t))
    for pkg, seen in sorted(observed.items()):
        for t in sorted(set(seen) - expected.get(pkg, set())):
            if SKIP_RE.search(t):
                continue
            (allowlisted if allowed(entries, pkg, t) else extra).append((pkg, t))

    # Skip parity: passed under go test, skipped under Bazel.
    skip_div, skip_allowed, parity_notes = [], [], []
    if go_status is not None:
        for pkg, seen in sorted(observed.items()):
            gs = go_status.get(pkg, {})
            for t, bs in sorted(seen.items()):
                g = gs.get(t)
                if g is None or g == bs:
                    continue
                if g == "passed" and bs == "skipped":
                    (skip_allowed if allowed(entries, pkg, t, "skip") else skip_div).append((pkg, t))
                else:
                    parity_notes.append((pkg, f"{t} (go test {g}, bazel {bs})"))
    allowlisted += skip_allowed

    # Dedupe allowlisted pairs (a package-level entry also covers its tests).
    allowlisted = sorted(set(allowlisted))
    # Skip entries can only be exercised when go test statuses are known.
    # (The allowlist describes the --config=ci lane; a job comparison uses only
    # the entries that apply.)
    unused = [] if args.job else [e for e in entries if not e["used"] and (e["kind"] == "run" or go_status is not None)]

    statuses = {"passed": 0, "skipped": 0, "failed": 0}
    for seen in observed.values():
        for s in seen.values():
            statuses[s] += 1
    n_expected = sum(len(v) for v in expected.values())
    ok = not (no_target or missing or extra or skip_div or failures or no_xml or allow_errors)

    limit = None if args.details else 40

    def listing(title, pairs):
        out = [f"{title} ({len(pairs)}):"]
        shown = pairs if limit is None else pairs[:limit]
        out += [f"  {p or '.'}  {t}" for p, t in shown]
        if limit is not None and len(pairs) > limit:
            out.append(f"  ... {len(pairs) - limit} more (run with --details)")
        return out

    lines = [
        f"go test:  {len(expected)} packages with tests, {n_expected} top-level tests (excluding ^TestEmbedded)",
        f"bazel:    {len(configured)} go_test targets configured, {len(tested)} tested; "
        f"{sum(len(v) for v in observed.values())} top-level tests seen "
        f"({statuses['passed']} passed, {statuses['skipped']} skipped, {statuses['failed']} failed)",
    ]
    if target_pkgs is not None:
        lines.append(f"query:    {len(target_pkgs)} packages with a go_test target")
    if go_status is None:
        lines.append("skips:    not compared (no --go-test-json; the nightly run checks skip parity)")
    else:
        n_go = sum(len(v) for v in go_status.values())
        lines.append(
            f"skips:    compared with {n_go} go test results: {len(skip_div) + len(skip_allowed)} passed under "
            f"go test but skipped under Bazel ({len(skip_allowed)} allowlisted), {len(parity_notes)} other differences"
        )
    lines.append(f"allowlisted divergences: {len(allowlisted)}")
    for title, pairs in (
        ("packages with Go tests but no go_test target", no_target),
        ("tests go test runs that Bazel did not", missing),
        ("tests Bazel ran that go test would not", extra),
        ("tests go test passed that Bazel skipped", skip_div),
        ("tests that failed in a Bazel target", failures),
        ("Bazel targets without test.xml", no_xml),
    ):
        if pairs:
            lines += listing(title, pairs)
    if parity_notes:
        lines += listing("other status differences (reported, not failed)", parity_notes)
    if args.details and allowlisted:
        lines += listing("allowlisted", allowlisted)
    for e in unused:
        lines.append(f"note: allowlist line {e['line']} ({e['pkg']} {e['test']} {e['kind']}) matched nothing; remove it")
    for p in problems:
        lines.append(f"note: {p}")
    lines += allow_errors
    verdict = "EQUIVALENCE: ok" if ok else "EQUIVALENCE: FAIL (unexplained divergence; fix the BUILD tags/targets or add a justified allowlist entry)"
    lines.append(verdict)
    print("\n".join(lines))

    summary = args.summary or os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        head = (
            f"**Equivalence**: {'ok' if ok else 'FAIL'}; go test {n_expected} tests in {len(expected)} packages, "
            f"Bazel saw {sum(len(v) for v in observed.values())} "
            f"(missing {len(missing)}, extra {len(extra)}, packages without target {len(no_target)}, "
            f"skipped only under Bazel {len(skip_div) if go_status is not None else 'not compared'}, "
            f"allowlisted {len(allowlisted)})\n\n"
        )
        with open(summary, "a", encoding="utf-8") as f:
            f.write(head)
            f.write("<details><summary>Equivalence detail</summary>\n\n```\n" + "\n".join(lines) + "\n```\n</details>\n\n")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
