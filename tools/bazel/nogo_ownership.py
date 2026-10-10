#!/usr/bin/env python3
"""Dynamic cross-check for the nogo-ownership policy (scripts/nogo_lint_policy_test.go).

The static policy tests (T1-T3, T5-T6) pin .bazelrc/bazel.yml *text*: a
config's flags, and which lane owns its validations. This script instead
asks Bazel itself whether the owner's RunNogo action set for a configuration
really is a superset of every non-owner's RunNogo action set for that same
configuration -- i.e. the owner genuinely analyzes every first-party file a
non-owner compiles, so skipping validation on the non-owner loses no lint
coverage.

Scope (S1): the race configuration group only -- bazel-test (owner, race)
vs. embedded/doltserver/doltserver-proxied/dolt-race (non-owners, which pass
--norun_validations). The integration group (S2) is out of scope until its
lanes gain the same split; see scripts/nogo_lint_policy_test.go's
nogoConfigurations table, which this script's RACE_GROUP mirrors.

Each config's key-affecting flags (the same fingerprint
scripts/nogo_lint_policy_test.go's nogoKeyFlagPattern extracts) are read
straight out of .bazelrc and passed to `aquery`/`query` directly: those are
"build"-family commands and do not see flags defined under a bare `test:X`
.bazelrc stanza, so `--config=X` itself cannot be used here.

The owner (`test:ci`) builds //... whole (T3 pins that: no
--build_tests_only, no --build_tag_filters), so its RunNogo set is queried
directly over //.... Every non-owner here instead passes --build_tests_only
with a --test_tag_filters=<tag[,tag...]> value, which narrows what `bazel
test` actually builds to deps(the tagged tests) -- querying RunNogo over
//... whole for a non-owner would silently compare the *owner's* universe
against itself and always report full coverage (ga-f5s1-nogo-ownership-1:
this was exactly that bug). So each non-owner's target set here is derived
the same way `bazel test --build_tests_only --test_tag_filters=...` derives
it: the deps closure of `tests(//...)` filtered down to targets carrying one
of the lane's tags, read as an *exact* tag (not a substring -- "dolt-server"
is a prefix of both "dolt-server-proxied" and "dolt-server-cmd", so a naive
substring match over `attr(tags, ...)` would wrongly fold those in; tags are
read from `--output=xml` instead, which lists each tag as its own exact
string, to sidestep that).

This is advisory and dynamic (runs real `bazel query`/`aquery`, a few
minutes locally): it is intentionally not part of the required `//scripts`
static test suite, which cannot shell out to Bazel from inside a Bazel
action. Run it with `make nogo-ownership`, locally or on demand in a
slice's own PR.

Exit status: 0 if every non-owner's RunNogo target set is covered by the
owner's, 2 otherwise. Prints the offending targets.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
import xml.etree.ElementTree as ET
from pathlib import Path

# (name, owner config, [non-owner config, ...]) for the race group.
# Keep in sync with scripts/nogo_lint_policy_test.go's nogoConfigurations.
RACE_GROUP = (
    "race",
    "ci",
    ["embedded", "doltserver", "doltserver-proxied", "dolt-race"],
)

# Mirrors scripts/nogo_lint_policy_test.go's nogoKeyFlagPattern.
KEY_FLAG_RE = re.compile(
    r"--@rules_go//go/config:(?:race|pure|tags=\S+)"
    r"|--platforms=\S+"
    r"|//tools/bazel:release_platforms\S*"
)

CONFIG_LINE_RE = re.compile(r"^(build|test):([A-Za-z0-9_-]+)\s+(.*)$")
TEST_TAG_FILTERS_RE = re.compile(r"^(?:build|test) --test_tag_filters=(\S+)$")
BUILD_TESTS_ONLY_RE = re.compile(r"^(?:build|test) --build_tests_only$")


def bazelrc_lines(rc_text: str, name: str) -> list[str]:
    """Every raw ("build"|"test", opt) line for --config=name, in file order."""
    out = []
    for line in rc_text.splitlines():
        line = line.split(" #", 1)[0].rstrip()
        m = CONFIG_LINE_RE.match(line)
        if m and m.group(2) == name:
            out.append(f"{m.group(1)} {m.group(3)}")
    return out


def expand_config(rc_text: str, name: str, seen: set[str]) -> list[str]:
    """Recursively follows nested --config=Y references, as the Go test does."""
    if name in seen:
        return []
    seen.add(name)
    out = []
    for line in bazelrc_lines(rc_text, name):
        for prefix in ("build --config=", "test --config="):
            if line.startswith(prefix):
                out.extend(expand_config(rc_text, line[len(prefix):], seen))
                break
        else:
            out.append(line)
    return out


def fingerprint(rc_text: str, name: str) -> list[str]:
    lines = expand_config(rc_text, name, set())
    flags = set()
    for line in lines:
        flags.update(m.group(0) for m in KEY_FLAG_RE.finditer(line))
    return sorted(flags)


def test_tag_filters(rc_text: str, name: str) -> list[str] | None:
    """The lane's --test_tag_filters value, as a list of exact tags, if any.

    None means the lane sets no --test_tag_filters (the owner: it builds
    //... whole, per T3). A --test_tag_filters value with a leading '-'
    (tag exclusion) is not used by any race-group lane today; this raises
    rather than silently mis-scoping a future lane that added one.
    """
    for line in expand_config(rc_text, name, set()):
        m = TEST_TAG_FILTERS_RE.match(line)
        if not m:
            continue
        tags = m.group(1).split(",")
        for tag in tags:
            if tag.startswith("-"):
                raise NotImplementedError(
                    f"--config={name}: --test_tag_filters={m.group(1)} has an "
                    "exclusion tag; this script only understands inclusion filters"
                )
        return tags
    return None


def lane_builds_tests_only(rc_text: str, name: str) -> bool:
    return any(BUILD_TESTS_ONLY_RE.match(line) for line in expand_config(rc_text, name, set()))


def run_query_xml(bazel: str, query: str) -> ET.Element:
    cmd = [bazel, "--nohome_rc", "query", query, "--output=xml", "--noimplicit_deps"]
    out = subprocess.run(cmd, capture_output=True, text=True, check=True).stdout
    return ET.fromstring(out)


def tagged_test_targets(bazel: str, tags: list[str]) -> set[str]:
    """Labels of tests(//...) carrying at least one of tags, read exactly.

    Tags are read from --output=xml's own <list name="tags"><string .../></list>
    elements rather than matched with attr()'s regex against a stringified
    list: "dolt-server" is a prefix of both "dolt-server-proxied" and
    "dolt-server-cmd" (and attr()'s per-element regex match is a substring
    match, not an exact one), so attr(tags, "dolt-server", //...) also
    returns proxied- and cmd-tagged targets -- the exact bug this script
    exists to avoid reintroducing one level up.
    """
    root = run_query_xml(bazel, "tests(//...)")
    wanted = set(tags)
    labels = set()
    for rule in root.findall("rule"):
        rule_tags = {
            s.get("value")
            for list_el in rule.findall("list[@name='tags']")
            for s in list_el.findall("string")
        }
        if rule_tags & wanted:
            labels.add(rule.get("name"))
    return labels


def lane_universe(bazel: str, targets: set[str] | None) -> str:
    """The query-language target expression this lane builds.

    None (the owner, or any lane with no --build_tests_only): //... whole.
    Otherwise (a lane that builds only its tagged tests and their deps, as
    --build_tests_only + --test_tag_filters does): the deps closure of just
    those targets.
    """
    if targets is None:
        return "//..."
    if not targets:
        raise ValueError("lane_universe: empty tagged target set")
    return "deps(set(" + " ".join(sorted(targets)) + "))"


def run_nogo_targets(bazel: str, flags: list[str], universe: str) -> set[str]:
    """Returns the set of target labels with a RunNogo action under flags."""
    cmd = [bazel, "--nohome_rc", "aquery", f'mnemonic("RunNogo", {universe})', *flags,
           "--output=jsonproto"]
    out = subprocess.run(cmd, capture_output=True, text=True, check=True).stdout
    data = json.loads(out)
    targets_by_id = {t["id"]: t["label"] for t in data.get("targets", [])}
    labels = set()
    for action in data.get("actions", []):
        if action.get("mnemonic") != "RunNogo":
            continue
        label = targets_by_id.get(action.get("targetId"))
        if label and not label.startswith("@"):
            labels.add(label)
    return labels


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bazel", default="bazel", help="bazel/bazelisk binary to invoke")
    parser.add_argument("--bazelrc", default=".bazelrc")
    args = parser.parse_args()

    rc_text = Path(args.bazelrc).read_text()
    name, owner, non_owners = RACE_GROUP

    owner_fp = fingerprint(rc_text, owner)
    if lane_builds_tests_only(rc_text, owner):
        raise NotImplementedError(
            f"--config={owner} now passes --build_tests_only, narrowing its target "
            "set; this script assumes the race owner builds //... whole "
            "(scripts/nogo_lint_policy_test.go's T3). --test_tag_filters alone "
            "(exclusion-only, as today's owner has) only narrows test execution, "
            "not the build, so it is not checked here."
        )
    owner_targets = run_nogo_targets(args.bazel, owner_fp, "//...")

    problems = []
    for non_owner in non_owners:
        non_owner_fp = fingerprint(rc_text, non_owner)
        if non_owner_fp != owner_fp:
            problems.append(
                f"{name}: --config={non_owner}'s fingerprint {non_owner_fp} "
                f"differs from owner --config={owner}'s {owner_fp}; it is no "
                "longer safe to skip its validations"
            )
            continue

        tags = test_tag_filters(rc_text, non_owner)
        if tags is None or not lane_builds_tests_only(rc_text, non_owner):
            # No narrowing this script knows how to replicate: fall back to
            # the owner's own universe, which is always a safe (if coarser)
            # comparison.
            non_owner_universe = "//..."
        else:
            tagged = tagged_test_targets(args.bazel, tags)
            if not tagged:
                problems.append(
                    f"{name}: --config={non_owner}'s --test_tag_filters={','.join(tags)} "
                    "matches no test in //...; its coverage could not be checked"
                )
                continue
            non_owner_universe = lane_universe(args.bazel, tagged)

        non_owner_targets = run_nogo_targets(args.bazel, non_owner_fp, non_owner_universe)
        missing = non_owner_targets - owner_targets
        if missing:
            problems.append(
                f"{name}: --config={non_owner} compiles {len(missing)} target(s) "
                f"with no RunNogo action under the owner --config={owner}: "
                + ", ".join(sorted(missing)[:10])
                + (" ..." if len(missing) > 10 else "")
            )

    if problems:
        print("nogo_ownership: FAIL", file=sys.stderr)
        for p in problems:
            print(f"  {p}", file=sys.stderr)
        return 2

    print(f"nogo_ownership: OK ({name}: --config={owner} covers "
          f"{', '.join(non_owners)})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
