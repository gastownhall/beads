#!/usr/bin/env python3
"""Fail on a skipped test the skip census does not list, or a census line whose test no longer skips.

Usage: check_skip_census.py [--census FILE] (--bep FILE [--testlogs DIR] | --testlogs-dir DIR ...)
                            [--label LABEL ...] [--report-only | --write]
       check_skip_census.py --lint [--census FILE] [--root DIR]

The runtime skip census (C7(a) of the bringup test plan). Every gate on the
conformance tiers is a static source scan (a REFERENCE IS NOT A PASS:
internal/storage/leg_contract_wiring_test.go), check_testcases.py fails only
a test.xml that lists no test, and check_shard_coverage.py only a shard whose
tests all skipped. So a new t.Skip in a served or store-leg case, a contract
wired with a nil hook, or a park left behind after its fix lands passes every
lane. This reads the test.xml of every target (and shard) a `bazel test`
invocation tested, finds every skipped Go test and subtest, classifies its
skip message, and compares that with tools/bazel/skip_census.txt:

- a skip the census does not list is a failure (add the printed line if the
  skip is intended);
- a census line whose test ran and passed, or no longer ran at all in a
  target whose every test.xml is present and failure-free, is a failure:
  the census only shrinks, so unparking a case means deleting its line.

A census line is "<label> <test path> <class>", one per line, sorted, with
whole-line "#" comments. The class comes from the skip message (the last
`file.go:N:` log entry before `--- SKIP: <test>`):

  KNOWN DIVERGENCE <row>       skipKnownDivergence (internal/httpclient) or a
                               storage leg's helper: the first token after the
                               prefix (a ledger row, or a bead)
  KNOWN BDP DIVERGENCE <row>   reserved for C7(b)'s BDP-only parks
  WAITING ON <ref>             a park until <ref> (a PR or slice) lands:
                               t.Skip("WAITING ON #8227 (what it waits for)")
  nil-hook <Hook>              a contract case whose fixture leaves <Hook> nil
                               ("(Hook is nil)", "Hook is nil", "fixture has no
                               Hook", "supplies no ... (Hook)")
  env-gate <VAR>               a test that runs only when <VAR> is set ("set
                               VAR=1 to run ..."): one "<label> * env-gate VAR"
                               line covers every such test of the label
  other                        anything else

Lines for a label the invocation did not test are not judged, so one census
serves every lane; --label (repeatable) narrows the judged labels the way
check_testcases.py's --label does. bazel.yml runs the census through
check_testcases.py --skip-census in every Dolt-tier lane's "Every target and
shard ran tests" step. Where a lane executes on the runner (modes cache and
local), the executor's own environment changes which tests skip, so those
runs pass --skip-census-report-only.

--write regenerates the census from the given test.xml files: the lines of
every label they cover completely are replaced by what they skipped, other
lines are kept (a label with a missing test.xml or a failed test keeps its
old lines plus its new skips; a new skip replaces its test's old line). Only
the header comments (those before the first line) survive --write. Feed it runs that executed on rbe-west only
(modes remote, fork-ro, fork-rw). --testlogs-dir reads a bazel-testlogs tree
without a BEP, such as a lane's "Upload test logs" artifact; it lists a
missing shard (so its target counts as incomplete) but cannot see a missing
unsharded target, so prefer an artifact from a run whose lane failed only on
the census.

--lint checks the census file itself (grammar, order, duplicates) and that
every label is a top-level rule tagged for a Dolt-tier lane (CENSUS_LANE_TAGS,
which scripts' TestSkipCensusLaneTagsAreTheDoltTierTags ties to the lanes'
--test_tag_filters).

Exit status: 0 if the census and the run agree (or with --report-only,
--write, or a clean --lint), 1 otherwise.
"""

import argparse
import os
import re
import sys
import xml.etree.ElementTree as ET

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
from equivalence import read_bep, testlog_xmls  # noqa: E402

DEFAULT_CENSUS = os.path.join(HERE, "skip_census.txt")
# Not os.path.realpath: under Bazel the runfiles tree, not the source tree, holds the BUILD files.
REPO_ROOT = os.path.dirname(os.path.dirname(HERE))

# The --test_tag_filters of the configs bazel.yml's Dolt-tier lanes run
# (scripts TestSkipCensusLaneTagsAreTheDoltTierTags).
CENSUS_LANE_TAGS = frozenset({"embedded", "dolt-server", "dolt-server-proxied", "dolt-server-integration", "dolt-server-cmd"})

LOG_RE = re.compile(r"^\s*\S+\.go:\d+: ?(.*)$")
KNOWN_RES = (
    ("KNOWN BDP DIVERGENCE", re.compile(r"\bKNOWN BDP DIVERGENCE\s+(\S+)")),
    ("KNOWN DIVERGENCE", re.compile(r"\bKNOWN DIVERGENCE\s+(\S+)")),
)
WAITING_RE = re.compile(r"\bWAITING ON\s+(.+?)\s*(?:\(|:\s|;|$)")
ENV_GATE_RE = re.compile(r"^set ([A-Z][A-Z0-9_]*)=")
NIL_HOOK_RES = (
    re.compile(r"\(((?:[a-z_]\w*\.)?[A-Z][\w./]*) is nil\)"),
    re.compile(r"\b((?:[a-z_]\w*\.)?[A-Z][\w./]*) is nil\b"),
    re.compile(r"^fixture has no ([A-Z]\w*)"),
    re.compile(r"\bsupplies (?:no|neither)\b.*\(([A-Z][\w.]*)\)\s*$"),
)
TRAILING = ":;,.)"

LABEL_RE = re.compile(r"^//[A-Za-z0-9_./+-]*:[A-Za-z0-9_.+-]+$")
TEST_RE = re.compile(r"^Test\S*$")
CLASS_RE = re.compile(
    r"^(?:KNOWN (?:BDP )?DIVERGENCE \S+|WAITING ON \S.*|nil-hook [A-Za-z_][\w./]*|env-gate [A-Z][A-Z0-9_]*|other)$"
)
SHARD_DIR_RE = re.compile(r"^shard_(\d+)_of_(\d+)$")
RULE_START_RE = re.compile(r"^[A-Za-z_][\w.]*\($")
MESSAGE_LIMIT = 200


# --- classifying a skip -----------------------------------------------------

def skip_message(body, name):
    """The skip message in a <skipped> body: the last `file.go:N:` log entry
    before `--- SKIP: name`, with its continuation lines joined; "" if none."""
    lines = (body or "").splitlines()
    end = len(lines)
    marker = "--- SKIP: " + name
    for i, line in enumerate(lines):
        stripped = line.strip()
        if stripped == marker or stripped.startswith(marker + " "):
            end = i
    for j in range(end - 1, -1, -1):
        m = LOG_RE.match(lines[j])
        if m:
            parts = [m.group(1)] + [line.strip() for line in lines[j + 1:end]]
            return " ".join(p for p in parts if p).strip()
        if lines[j].startswith("=== "):
            break
    return ""


def classify(message):
    """Return the census class of a skip message (see the module docstring)."""
    for prefix, rx in KNOWN_RES:
        m = rx.search(message)
        if m and m.group(1).rstrip(TRAILING):
            return f"{prefix} {m.group(1).rstrip(TRAILING)}"
    m = WAITING_RE.search(message)
    if m:
        ref = " ".join(m.group(1).split()).rstrip(TRAILING)
        if ref:
            return f"WAITING ON {ref}"
    m = ENV_GATE_RE.match(message)
    if m:
        return f"env-gate {m.group(1)}"
    for rx in NIL_HOOK_RES:
        m = rx.search(message)
        if m:
            return f"nil-hook {m.group(1)}"
    return "other"


def is_env_gate(cls):
    return cls.startswith("env-gate ")


def census_line(label, test, cls):
    return f"{label} {'*' if is_env_gate(cls) else test} {cls}"


def line_key(text):
    fields = text.split()
    return fields[0], fields[1], " ".join(fields[2:])


# --- the census file ---------------------------------------------------------

def read_census(path):
    """Return (header lines, entries, errors). The header is the comment and
    blank lines before the first entry; an entry is a dict with label, test,
    cls, line (number), text and used."""
    header, entries, errors = [], [], []
    with open(path, encoding="utf-8") as f:
        for n, raw in enumerate(f, 1):
            line = raw.rstrip("\n")
            if not line.strip() or line.lstrip().startswith("#"):
                if not entries:
                    header.append(line)
                continue
            fields = line.split()
            if len(fields) < 3:
                errors.append(f"{path}:{n}: want '<label> <test path> <class>', got {line!r}")
                continue
            label, test, cls = fields[0], fields[1], " ".join(fields[2:])
            if not LABEL_RE.match(label):
                errors.append(f"{path}:{n}: {label!r} is not a //package:target label")
            if not CLASS_RE.match(cls):
                errors.append(f"{path}:{n}: {cls!r} is not a census class")
            if is_env_gate(cls) and test != "*":
                errors.append(f"{path}:{n}: an env-gate line covers its whole label: its test path must be *")
            if not is_env_gate(cls) and not TEST_RE.match(test):
                errors.append(f"{path}:{n}: {test!r} is not a Go test path (Test...)")
            entries.append({"label": label, "test": test, "cls": cls, "line": n,
                            "text": f"{label} {test} {cls}", "used": False})
    keys = [(e["label"], e["test"], e["cls"]) for e in entries]
    if keys != sorted(keys):
        errors.append(f"{path}: lines are not sorted (by label, then test path, then class)")
    if len(set(keys)) != len(keys):
        errors.append(f"{path}: duplicate lines")
    seen = {}
    for e in entries:
        if e["test"] == "*":
            continue
        other = seen.setdefault((e["label"], e["test"]), e)
        if other is not e:
            errors.append(f"{path}:{e['line']}: {e['label']} {e['test']} is already listed on line {other['line']}")
    return header, entries, errors


def rule_tags(build_text, name):
    """The tags of the top-level rule called name in a buildifier-formatted
    BUILD file, or None if it has no such rule."""
    block = None
    for line in build_text.splitlines():
        if block is None:
            if RULE_START_RE.match(line):
                block = [line]
            continue
        block.append(line)
        if line != ")":
            continue
        text = "\n".join(block)
        block = None
        if re.search(r'^\s+name = "%s",$' % re.escape(name), text, re.M):
            m = re.search(r"^\s+tags = \[(.*?)\],?$", text, re.M | re.S)
            return set(re.findall(r'"([^"]+)"', m.group(1))) if m else set()
    return None


def lint_labels(entries, root):
    """Errors for census labels that are not a top-level rule of their
    package's BUILD.bazel tagged for a Dolt-tier lane (and not manual)."""
    errors, builds = [], {}
    for label in sorted({e["label"] for e in entries}):
        pkg, name = label[2:].split(":", 1)
        path = os.path.join(root, pkg, "BUILD.bazel")
        if path not in builds:
            try:
                with open(path, encoding="utf-8") as f:
                    builds[path] = f.read()
            except OSError:
                builds[path] = None
        if builds[path] is None:
            errors.append(f"{label}: {pkg}/BUILD.bazel is missing")
            continue
        tags = rule_tags(builds[path], name)
        if tags is None:
            errors.append(f"{label}: {pkg}/BUILD.bazel has no top-level rule named {name!r}")
        elif "manual" in tags or not tags & CENSUS_LANE_TAGS:
            errors.append(f"{label}: tagged {sorted(tags)}, so no Dolt-tier lane runs it (want one of "
                          f"{sorted(CENSUS_LANE_TAGS)}, not manual)")
    return errors


# --- observing a run -----------------------------------------------------------

class Observation:
    """What a run's test.xml files say, per (label, test path)."""

    def __init__(self):
        self.labels = set()
        self.skips = {}       # (label, test) -> (class, message)
        self.status = {}      # (label, test) -> "passed" | "skipped" | "failed"
        self.incomplete = {}  # label -> why stale "did not run" checks cannot be trusted


def observe(label_xmls, obs=None):
    """Fold {label: [test.xml path]} into an Observation."""
    obs = obs or Observation()
    for label, xmls in sorted(label_xmls.items()):
        obs.labels.add(label)
        for path in xmls:
            if not os.path.exists(path):
                obs.incomplete.setdefault(label, f"{path} is missing")
                continue
            try:
                root = ET.parse(path).getroot()
            except ET.ParseError as e:
                obs.incomplete.setdefault(label, f"cannot parse {path}: {e}")
                continue
            tests = 0
            for tc in root.iter("testcase"):
                name = tc.get("name", "")
                if not name.startswith("Test"):
                    continue
                tests += 1
                if tc.find("failure") is not None or tc.find("error") is not None:
                    obs.status[(label, name)] = "failed"
                    obs.incomplete.setdefault(label, f"{name} failed")
                    continue
                skipped = tc.find("skipped")
                if skipped is None:
                    obs.status.setdefault((label, name), "passed")
                    continue
                message = skip_message(skipped.text, name)
                obs.skips[(label, name)] = (classify(message), message)
                obs.status[(label, name)] = "skipped"
            if tests == 0:
                obs.incomplete.setdefault(label, f"{path} lists no Go test")
    return obs


def scan_testlogs_dir(root):
    """{label: [test.xml path]} for a bazel-testlogs tree
    (<pkg>/<target>/[shard_K_of_N/]test.xml). A shard the tree lacks is listed
    by the path it would have, so observe() marks its target incomplete."""
    out, shards = {}, {}
    for dirpath, _, files in os.walk(root):
        if "test.xml" not in files:
            continue
        parts = os.path.relpath(dirpath, root).split(os.sep)
        m = SHARD_DIR_RE.match(parts[-1]) if parts else None
        if m:
            parts = parts[:-1]
        if not parts or parts == ["."]:
            continue
        label = "//" + "/".join(parts[:-1]) + ":" + parts[-1]
        out.setdefault(label, []).append(os.path.join(dirpath, "test.xml"))
        if m:
            shards[label] = (os.path.dirname(dirpath), int(m.group(2)))
    for label, (base, total) in shards.items():
        want = [os.path.join(base, f"shard_{k}_of_{total}", "test.xml") for k in range(1, total + 1)]
        out[label] += [p for p in want if p not in out[label]]
    return out


# --- judging -------------------------------------------------------------------

def _short(message):
    return message if len(message) <= MESSAGE_LIMIT else message[:MESSAGE_LIMIT - 3] + "..."


def judge(obs, entries):
    """Return (problems, notes, add, delete): add and delete are census lines."""
    problems, notes, add, delete = [], [], [], []
    exact = {(e["label"], e["test"]): e for e in entries if e["test"] != "*"}
    globs = {(e["label"], e["cls"]): e for e in entries if e["test"] == "*"}
    ungated = {}  # (label, env-gate class) -> [skip count, first test]: one problem per gate, not per test
    for (label, test), (cls, message) in sorted(obs.skips.items()):
        e = exact.get((label, test))
        if e is None and is_env_gate(cls):
            e = globs.get((label, cls))
            if e is None:
                ungated.setdefault((label, cls), [0, test])[0] += 1
                continue
        if e is not None and e["cls"] == cls:
            e["used"] = True
            continue
        want = census_line(label, test, cls)
        if e is not None:
            e["used"] = True
            problems.append(f"{label}: {test} now skips as '{cls}' ({_short(message)!r}), "
                            f"but census line {e['line']} says '{e['cls']}'")
            delete.append(e["text"])
        else:
            problems.append(f"{label}: {test} skipped as '{cls}' ({_short(message)!r}) and the census does not list it")
        if want not in add:
            add.append(want)
    for (label, cls), (count, first) in sorted(ungated.items()):
        problems.append(f"{label}: {count} test{'s' if count != 1 else ''} (first {first}) skip as '{cls}' "
                        f"and the census has no \"{label} * {cls}\" line")
        add.append(census_line(label, first, cls))
    for e in entries:
        if e["used"] or e["label"] not in obs.labels:
            continue
        label, test = e["label"], e["test"]
        if test == "*":
            if label in obs.incomplete:
                notes.append(f"census line {e['line']} ({e['text']}) matched no skip, but {obs.incomplete[label]}")
                continue
            problems.append(f"census line {e['line']} ({e['text']}): no test of {label} skips that way any more")
        elif obs.status.get((label, test)) == "failed":
            # The lane is already red on the failure; a flaky park must keep
            # its line, so this is not a census problem and not a delete.
            notes.append(f"census line {e['line']} ({e['text']}): {test} failed instead of skipping")
            continue
        elif (label, test) in obs.status:
            problems.append(f"census line {e['line']} ({e['text']}): {test} ran and {obs.status[(label, test)]} "
                            f"(the census only shrinks: delete the line when a case is unparked)")
        elif label in obs.incomplete:
            notes.append(f"census line {e['line']} ({e['text']}): {test} did not run, but {obs.incomplete[label]}")
            continue
        else:
            problems.append(f"census line {e['line']} ({e['text']}): {test} did not run in {label} (renamed or removed?)")
        delete.append(e["text"])
    return problems, notes, add, delete


def summary(obs, entries):
    used = sum(1 for e in entries if e["used"])
    elsewhere = sum(1 for e in entries if e["label"] not in obs.labels)
    return (f"skip census: {len(obs.skips)} skipped tests in {len(obs.labels)} targets; "
            f"{used} census lines matched, {elsewhere} name targets this run did not test")


def census_problems(tested, testlogs, census_path, labels=None):
    """check_testcases.py --skip-census: (output lines, problems, notes) for
    {label: shard_count} tested by a BEP, restricted to labels if given."""
    _, entries, errors = read_census(census_path)
    label_xmls = {label: testlog_xmls(testlogs, label, shards) for label, shards in tested.items()
                  if labels is None or label in labels}
    obs = observe(label_xmls)
    problems, notes, add, delete = judge(obs, entries)
    out = [summary(obs, entries)] + action_lines(census_path, add, delete)
    return out, errors + problems, notes


def action_lines(census_path, add, delete):
    rel = census_path
    out = []
    if delete:
        out.append(f"skip census: delete from {rel}:")
        out += ["  " + line for line in sorted(set(delete), key=line_key)]
    if add:
        out.append(f"skip census: add to {rel} (sorted; only if each skip is intended):")
        out += ["  " + line for line in sorted(set(add), key=line_key)]
    return out


# --- regenerating ----------------------------------------------------------------

def write_census(path, header, entries, obs):
    """Rewrite path (see --write in the module docstring); return the line count."""
    # An incomplete label keeps its old lines, except one whose test this run
    # saw skip: the new observation replaces it (else --write would leave two
    # lines for one test, which --lint rejects).
    lines = {e["text"] for e in entries
             if e["label"] not in obs.labels
             or (e["label"] in obs.incomplete and (e["label"], e["test"]) not in obs.skips)}
    for (label, test), (cls, _) in obs.skips.items():
        lines.add(census_line(label, test, cls))
    body = sorted(lines, key=line_key)
    with open(path, "w", encoding="utf-8") as f:
        for line in header:
            f.write(line + "\n")
        for line in body:
            f.write(line + "\n")
    return len(body)


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--census", default=DEFAULT_CENSUS, help="default: tools/bazel/skip_census.txt")
    ap.add_argument("--bep", help="--build_event_json_file of the bazel test run")
    ap.add_argument("--testlogs", help="with --bep; default: from the BEP, else ./bazel-testlogs")
    ap.add_argument("--testlogs-dir", action="append", default=[], metavar="DIR",
                    help="a bazel-testlogs tree to read without a BEP (repeatable), e.g. an 'Upload test logs' artifact")
    ap.add_argument("--label", action="append", default=None, metavar="LABEL",
                    help="judge only this target (repeatable)")
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument("--report-only", action="store_true", help="print what disagrees, exit 0")
    mode.add_argument("--write", action="store_true", help="regenerate the census from the given runs")
    mode.add_argument("--lint", action="store_true", help="check the census file and its labels' BUILD tags")
    ap.add_argument("--root", default=REPO_ROOT, help="with --lint: the workspace root (default: this checkout)")
    args = ap.parse_args(argv)

    if os.path.exists(args.census):
        header, entries, errors = read_census(args.census)
    elif args.write:
        header, entries, errors = [], [], []
    else:
        print(f"FAIL: {args.census}: no such census file", file=sys.stderr)
        return 1
    if args.lint:
        errors += lint_labels(entries, args.root)
        for e in errors:
            print(f"FAIL: {e}", file=sys.stderr)
        if not errors:
            print(f"skip census ok: {len(entries)} lines, {len({e['label'] for e in entries})} targets")
        return 1 if errors else 0
    if not args.bep and not args.testlogs_dir:
        ap.error("want --bep or --testlogs-dir (or --lint)")

    label_xmls = {}
    for d in args.testlogs_dir:
        for label, xmls in scan_testlogs_dir(d).items():
            label_xmls.setdefault(label, []).extend(xmls)
    if args.bep:
        tested, _, bep_testlogs = read_bep(args.bep)
        testlogs = args.testlogs or bep_testlogs or "bazel-testlogs"
        for label, shards in tested.items():
            label_xmls.setdefault(label, []).extend(testlog_xmls(testlogs, label, shards))
    if args.label is not None:
        missing = sorted(set(args.label) - set(label_xmls))
        for label in missing:
            errors.append(f"{label}: --label, but no test.xml was given for it")
        label_xmls = {label: xmls for label, xmls in label_xmls.items() if label in set(args.label)}
    obs = observe(label_xmls)

    if args.write:
        n = write_census(args.census, header, entries, obs)
        print(f"wrote {n} lines to {args.census}")
        for label, why in sorted(obs.incomplete.items()):
            print(f"note: {label} kept its old lines too: {why}")
        return 0

    problems, notes, add, delete = judge(obs, entries)
    problems = errors + problems
    print(summary(obs, entries))
    for line in action_lines(args.census, add, delete):
        print(line)
    for note in notes:
        print(f"note: {note}")
    for p in problems:
        print(f"{'note' if args.report_only else 'FAIL'}: skip census: {p}", file=sys.stderr)
    return 1 if problems and not args.report_only else 0


if __name__ == "__main__":
    sys.exit(main())
