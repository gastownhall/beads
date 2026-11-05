#!/usr/bin/env python3
"""Unit tests for nogo_ownership.py, with a fake `bazel` (no Bazel needed).

Run directly: python3 tools/bazel/nogo_ownership_test.py
(or: python3 -m unittest tools.bazel.nogo_ownership_test, from the repo root)
"""

import json
import os
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import nogo_ownership  # noqa: E402

RC = """\
test:prcore --@rules_go//go/config:race
test:prcore --test_tag_filters=-embedded,-manual
test:ci --config=prcore
test:embedded --@rules_go//go/config:race
test:embedded --test_tag_filters=embedded
test:embedded --build_tests_only
test:embedded --norun_validations
"""

# tests(//...) and //... as `bazel query --output=xml` lists them: one
# embedded-tagged test, one embedded-tagged test that is also manual (so
# `bazel test //...` never builds it), a library and the package gates' bd.
QUERY_XML = """<?xml version="1.1" encoding="UTF-8"?>
<query version="2">
  <rule class="go_test" name="//a:a_test">
    <list name="tags"><string value="embedded"/></list>
  </rule>
  <rule class="go_test" name="//a:manual_test">
    <list name="tags"><string value="embedded"/><string value="manual"/></list>
  </rule>
  <rule class="go_library" name="//b:lib">
    <list name="tags"/>
  </rule>
  <rule class="go_binary" name="//cmd/bd:bd_for_tests">
    <list name="tags"/>
  </rule>
</query>
"""


def aquery_json(actions):
    """actions: [(mnemonic, action key, label)] as aquery --output=jsonproto."""
    labels = sorted({label for _, _, label in actions})
    target_ids = {label: i + 1 for i, label in enumerate(labels)}
    return json.dumps({
        "targets": [{"id": target_ids[l], "label": l} for l in labels],
        "actions": [
            {"targetId": target_ids[label], "actionKey": key, "mnemonic": mnemonic}
            for mnemonic, key, label in actions
        ],
    })


class FakeBazel:
    """Answers `bazel query` with QUERY_XML and `bazel aquery` with owner()
    for the owner's expression (no deps(...)) and non_owner() otherwise."""

    def __init__(self, owner, non_owner):
        self.owner = owner
        self.non_owner = non_owner
        self.aqueries = []

    def __call__(self, cmd, **kwargs):
        verb = cmd[2]
        if verb == "query":
            out = QUERY_XML
        elif verb == "aquery":
            expr = cmd[3]
            self.aqueries.append(expr)
            out = aquery_json(self.non_owner(expr) if "deps(" in expr else self.owner(expr))
        else:
            raise AssertionError(f"unexpected bazel command {cmd}")
        return subprocess.CompletedProcess(cmd, 0, stdout=out, stderr="")


class NogoOwnershipTest(unittest.TestCase):
    def run_main(self, fake, rc=RC):
        with tempfile.NamedTemporaryFile("w", suffix=".bazelrc", delete=False) as f:
            f.write(rc)
        self.addCleanup(os.unlink, f.name)
        argv = ["nogo_ownership.py", "--bazel", "bazel", "--bazelrc", f.name]
        with mock.patch.object(nogo_ownership, "RACE_GROUP", ("race", "ci", ["embedded"])), \
                mock.patch.object(nogo_ownership.subprocess, "run", fake), \
                mock.patch.object(sys, "argv", argv), \
                mock.patch("sys.stdout"), mock.patch("sys.stderr"):
            return nogo_ownership.main()

    def test_same_action_keys_pass(self):
        acts = [("RunNogo", "k-lib", "//b:lib"), ("ValidateNogo", "v-lib", "//b:lib")]
        self.assertEqual(self.run_main(FakeBazel(lambda e: acts, lambda e: acts)), 0)

    def test_same_label_different_action_key_is_a_gap(self):
        # The non-owner compiles //b:lib in a configuration the owner does not
        # (e.g. through a transition): same label, a different RunNogo action.
        owner = [("RunNogo", "k-lib-race", "//b:lib")]
        non_owner = [("RunNogo", "k-lib-other", "//b:lib")]
        self.assertEqual(self.run_main(FakeBazel(lambda e: owner, lambda e: non_owner)), 2)

    def test_validate_nogo_actions_are_compared(self):
        owner = [("RunNogo", "k-lib", "//b:lib")]
        non_owner = [("RunNogo", "k-lib", "//b:lib"), ("ValidateNogo", "v-lib", "//b:lib")]
        self.assertEqual(self.run_main(FakeBazel(lambda e: owner, lambda e: non_owner)), 2)

    def test_owner_universe_is_bare_wildcard(self):
        # A bare //... skips manual targets as top-level targets, as `bazel
        # test //...` does; naming one (even in an except clause) would make
        # aquery analyze it as top-level.
        acts = [("RunNogo", "k-lib", "//b:lib")]
        fake = FakeBazel(lambda e: acts, lambda e: acts)
        self.assertEqual(self.run_main(fake), 0)
        owner = [e for e in fake.aqueries if "deps(" not in e]
        self.assertEqual(owner, ['mnemonic("RunNogo|ValidateNogo", //...)'])

    def test_non_owner_universe_is_tagged_tests_minus_manual_plus_bd_for_tests(self):
        acts = [("RunNogo", "k-lib", "//b:lib")]
        fake = FakeBazel(lambda e: acts, lambda e: acts)
        self.assertEqual(self.run_main(fake), 0)
        non_owner = [e for e in fake.aqueries if "deps(" in e]
        self.assertTrue(non_owner, fake.aqueries)
        for expr in non_owner:
            self.assertIn("//a:a_test", expr)
            self.assertIn("//cmd/bd:bd_for_tests", expr)
            self.assertNotIn("//a:manual_test", expr)

    def test_empty_owner_and_non_owner_action_sets_is_a_fail_not_a_vacuous_pass(self):
        # review of #7482, should-fix 4: non_owner_actions.keys() -
        # owner_actions.keys() is the empty set whether both sides have
        # findings in common or neither side has any actions at all (a
        # mnemonic rename, a broken --config, aquery matching nothing) --
        # the latter must fail loudly, not report full coverage.
        self.assertEqual(self.run_main(FakeBazel(lambda e: [], lambda e: [])), 2)

    def test_fingerprint_parses_whole_tokens(self):
        # race=false is not the race configuration: it must not fingerprint
        # (and so be queried) as the owner's race build.
        rc = RC.replace("test:embedded --@rules_go//go/config:race\n",
                        "test:embedded --@rules_go//go/config:race=false\n")
        acts = [("RunNogo", "k-lib", "//b:lib")]
        self.assertEqual(self.run_main(FakeBazel(lambda e: acts, lambda e: acts), rc), 2)

    def test_non_owner_empty_action_set_is_a_fail_not_a_vacuous_pass(self):
        # independent review of #7486, should-fix: a non-owner query that
        # matches nothing (bad universe expression, a renamed mnemonic local
        # to that lane) must not report full coverage just because the
        # (then-empty) missing set is vacuously empty too. Mirrors
        # test_empty_owner_and_non_owner_action_sets_is_a_fail_not_a_vacuous_pass,
        # but with a non-empty owner side, so only the should-fix's new
        # non-owner guard (not the pre-existing owner-empty guard) can catch
        # it.
        owner = [("RunNogo", "k-lib", "//b:lib")]
        self.assertEqual(self.run_main(FakeBazel(lambda e: owner, lambda e: [])), 2)

    def test_expand_config_follows_config_ref_on_a_common_line(self):
        # independent review of #7486, must-fix 5 (mirrors #7482 should-fix
        # 1's fix to the Go test's expandBazelrcConfig): a --config=Y
        # reference reached only through a common: line must still be
        # followed, not silently dropped.
        rc = RC + "common:embedded --config=ci\n"
        got = nogo_ownership.expand_config(rc, "embedded", set())
        self.assertIn("common --config=ci", got)
        # The reference is actually followed, not just recorded: ci's own
        # (and, transitively, prcore's) lines -- reached only through this
        # common: --config=ci link -- show up too.
        self.assertIn("test --config=prcore", got)
        self.assertIn("test --@rules_go//go/config:race", got)

    def test_expand_config_follows_config_ref_not_in_first_position(self):
        # independent review of #7486, must-fix 5: a --config=Y reference
        # elsewhere on the line (not the line's first option) must still be
        # followed.
        rc = RC + "test:embedded --keep_going --config=prcore\n"
        got = nogo_ownership.expand_config(rc, "embedded", set())
        self.assertIn("test --test_tag_filters=-embedded,-manual", got)


if __name__ == "__main__":
    unittest.main()
