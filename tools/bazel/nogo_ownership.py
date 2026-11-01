#!/usr/bin/env python3
"""Dynamic cross-check for the nogo-ownership policy (scripts/nogo_lint_policy_test.go).

The static policy tests (T1-T3, T5-T6) pin .bazelrc/bazel.yml *text*: a
config's flags, and which lane owns its validations. This script instead
asks Bazel itself whether the owner's nogo action set (RunNogo and
ValidateNogo) for a configuration really is a superset of every non-owner's
-- i.e. the owner genuinely runs every nogo action a non-owner would, so
skipping validation on the non-owner loses no lint coverage. Actions are
compared by Bazel action key, not by target label: one label compiled in two
configurations (a transition, or a lane whose flags differ) is two different
nogo actions, and only the owner's own keys are actually validated.

Scope (S1): the race configuration group only -- bazel-test (owner, race)
vs. embedded/doltserver/doltserver-proxied/dolt-race (non-owners, which pass
--norun_validations); see scripts/nogo_lint_policy_test.go's
nogoConfigurations table, which this script's RACE_GROUP mirrors. The
integration group (S2) has since gained its own validating lane -- a narrow
`bazel build --output_groups=nogo_fix` step over the integration-only
go_library targets, not a full validating test lane -- checked statically by
nogo_lint_policy_test.go's checkNogoOnlyStepRow and
nogo_integration_scan_test.go (independent review of #7486, latent item);
this script's dynamic aquery/query-based action-key cross-check is still S1
(RACE_GROUP) only, since S2's owner has no non-owner test lane building the
same configuration to compare actions against -- there is nothing dynamic
left to cross-check for S2 the way there is for S1.

Each config's key-affecting flags (the same fingerprint
scripts/nogo_lint_policy_test.go's nogoKeyFlags parses) are read
straight out of .bazelrc and passed to `aquery`/`query` directly: those are
"build"-family commands and do not see flags defined under a bare `test:X`
.bazelrc stanza, so `--config=X` itself cannot be used here.

The owner (`test:ci`) builds //... whole (T3 pins that: no
--build_tests_only, no --build_tag_filters), so its nogo set is queried over
//... as written: aquery expands a bare //... the way `bazel test //...` does,
skipping `manual` targets as top-level targets (listing one explicitly, e.g.
`//... except set(<manual>)`, would make it top-level and analyze it -- for
//tools/bazel:release_cross that fails outright). Every non-owner here instead passes --build_tests_only
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
string, to sidestep that), minus `manual` targets, plus
//cmd/bd:bd_for_tests and the package gates' targets (the package gates
build exactly PACKAGE_GATE_TARGETS in the race configuration with
--norun_validations, so they are non-owners too).

This is advisory and dynamic (runs real `bazel query`/`aquery`, a few
minutes locally): it is intentionally not part of the required `//scripts`
static test suite, which cannot shell out to Bazel from inside a Bazel
action. Run it with `make nogo-ownership`, locally or on demand in a
slice's own PR.

Exit status: 0 if every non-owner's nogo action keys are covered by the
owner's, 2 otherwise. Prints the offending actions' targets.
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

# What the package gates build (bazel.yml package-mcp/package-npm: `bazel
# build --@rules_go//go/config:race //cmd/bd:bd_for_tests --norun_validations`).
# Every non-owner universe includes them; //cmd/bd:bd_for_tests is also the
# subprocess bd every race-group test lane builds.
PACKAGE_GATE_TARGETS = ["//cmd/bd:bd_for_tests"]

# The nogo actions rules_go registers per Go compile: RunNogo (the analysis)
# and ValidateNogo (the validation action --norun_validations skips).
NOGO_MNEMONICS = ("RunNogo", "ValidateNogo")

# Mirrors scripts/nogo_lint_policy_test.go's nogoKeyFlags: whole tokens,
# never prefixes (--@rules_go//go/config:race=false is not the race build).
BOOL_KEY_FLAGS = ("--@rules_go//go/config:race", "--@rules_go//go/config:pure")
VALUE_KEY_FLAGS = ("--@rules_go//go/config:tags", "--platforms", "--//tools/bazel:release_platforms")


def key_flags(line: str) -> list[str]:
    out = []
    for tok in line.split():
        name, has_value, value = tok.partition("=")
        if name in BOOL_KEY_FLAGS:
            out.append(f"{name}={value if has_value else 'true'}")
        elif name.startswith("--no") and "--" + name[len("--no"):] in BOOL_KEY_FLAGS and not has_value:
            out.append(f"--{name[len('--no'):]}=false")
        elif name in VALUE_KEY_FLAGS:
            out.append(tok)
    return out

CONFIG_LINE_RE = re.compile(r"^(common|build|test):([A-Za-z0-9_-]+)\s+(.*)$")
TEST_TAG_FILTERS_RE = re.compile(r"^(?:common|build|test) --test_tag_filters=(\S+)$")
BUILD_TESTS_ONLY_RE = re.compile(r"^(?:common|build|test) --build_tests_only$")


def bazelrc_lines(rc_text: str, name: str) -> list[str]:
    """Every raw ("common"|"build"|"test", opt) line for --config=name, in
    file order. "common" applies to build and test alike, exactly as a
    common:X line reached through --config=X does in the real .bazelrc
    (independent review of #7486, must-fix 5 / #7482 should-fix 1)."""
    out = []
    for line in rc_text.splitlines():
        line = line.split(" #", 1)[0].rstrip()
        m = CONFIG_LINE_RE.match(line)
        if m and m.group(2) == name:
            out.append(f"{m.group(1)} {m.group(3)}")
    return out


def expand_config(rc_text: str, name: str, seen: set[str]) -> list[str]:
    """Recursively follows nested --config=Y references, as the Go test does.

    A --config= reference is recognized wherever it appears among a line's
    options -- on a common/build/test line, and not only as the line's
    first (or only) option. An earlier version matched only a whole line
    beginning "build --config=" / "test --config=", so a --config=
    reference on a common: line, or not in first position, silently
    expanded to nothing (independent review of #7486, must-fix 5, mirroring
    #7482 should-fix 1's fix to scripts/nogo_lint_policy_test.go's Go
    version of this function).
    """
    if name in seen:
        return []
    seen.add(name)
    out = []
    for line in bazelrc_lines(rc_text, name):
        out.append(line)
        for tok in line.split():
            if tok.startswith("--config="):
                out.extend(expand_config(rc_text, tok[len("--config="):], seen))
    return out


def fingerprint(rc_text: str, name: str) -> list[str]:
    lines = expand_config(rc_text, name, set())
    flags = set()
    for line in lines:
        flags.update(key_flags(line))
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
    wanted = set(tags)
    return {
        label
        for label, rule_tags in rule_tags_by_label(bazel, "tests(//...)").items()
        if rule_tags & wanted and "manual" not in rule_tags
    }


def rule_tags_by_label(bazel: str, query: str) -> dict[str, set[str]]:
    """Each rule query returns, with its exact tags."""
    root = run_query_xml(bazel, query)
    return {
        rule.get("name"): {
            s.get("value")
            for list_el in rule.findall("list[@name='tags']")
            for s in list_el.findall("string")
        }
        for rule in root.findall("rule")
    }



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
    return "deps(set(" + " ".join(sorted(set(targets) | set(PACKAGE_GATE_TARGETS))) + "))"



def run_nogo_actions(bazel: str, flags: list[str], universe: str) -> dict[str, str]:
    """Returns {action key: "label (mnemonic)"} for every first-party nogo
    action (NOGO_MNEMONICS) in universe under flags.

    Keyed by Bazel's action key, which covers the action's configuration,
    inputs and command line: the same label built in another configuration
    is a different key, so a label match alone is not coverage.
    """
    pattern = "|".join(NOGO_MNEMONICS)
    cmd = [bazel, "--nohome_rc", "aquery", f'mnemonic("{pattern}", {universe})', *flags,
           "--output=jsonproto"]
    out = subprocess.run(cmd, capture_output=True, text=True, check=True).stdout
    data = json.loads(out)
    targets_by_id = {t["id"]: t["label"] for t in data.get("targets", [])}
    actions = {}
    for action in data.get("actions", []):
        mnemonic = action.get("mnemonic")
        if mnemonic not in NOGO_MNEMONICS:
            continue
        label = targets_by_id.get(action.get("targetId"))
        if label and not label.startswith("@"):
            actions[action["actionKey"]] = f"{label} ({mnemonic})"
    return actions


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
    owner_actions = run_nogo_actions(args.bazel, owner_fp, "//...")
    if not owner_actions:
        # A set-difference check below (non_owner_actions.keys() -
        # owner_actions.keys()) is vacuously empty when both sides are
        # empty, which would otherwise report full coverage for an owner
        # query that silently matched nothing (a mnemonic rename, a broken
        # --config, aquery returning no actions at all) -- the exact
        # failure mode this script exists to catch, not pass through
        # (review of #7482, should-fix 4).
        print(
            f"nogo_ownership: FAIL\n  {name}: owner --config={owner} query "
            "returned zero RunNogo/ValidateNogo actions over //...; cannot "
            "verify anything covers the non-owners",
            file=sys.stderr,
        )
        return 2

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

        non_owner_actions = run_nogo_actions(args.bazel, non_owner_fp, non_owner_universe)
        if not non_owner_actions:
            # Mirrors the owner-empty guard above (review of #7482, should-fix
            # 4): a non-owner query that silently matched nothing (a bad
            # universe expression, an aquery mnemonic rename local to this
            # config) makes the set-difference below vacuously empty too,
            # reporting full coverage for a non-owner this script never
            # actually queried. The owner side is already known non-empty
            # here, so an empty non-owner is the same failure mode, not a
            # legitimately nogo-free lane (independent review of #7486,
            # should-fix).
            problems.append(
                f"{name}: --config={non_owner} query over {non_owner_universe!r} "
                "returned zero RunNogo/ValidateNogo actions even though the "
                f"owner --config={owner} has {len(owner_actions)}; cannot verify "
                "this non-owner is actually covered"
            )
            continue
        missing = sorted(non_owner_actions[k] for k in non_owner_actions.keys() - owner_actions.keys())
        if missing:
            problems.append(
                f"{name}: --config={non_owner} has {len(missing)} nogo action(s) "
                f"whose action key the owner --config={owner} never runs: "
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
