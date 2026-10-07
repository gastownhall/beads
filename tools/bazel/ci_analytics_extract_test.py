"""Tests for ci_analytics_extract.py.

Three fixture kinds, per the design doc (section 3.5):

  - testdata/ci_analytics/real/*: genuine Bazel 9.2.0 output from an
    isolated scratch workspace (tools/bazel/testdata/ci_analytics/regen.sh),
    with no remote flags and no ambient environment (env -i). The BEP is
    filtered down to only the fields the extractor reads before being
    committed; the exec log is committed decompressed (*.binpb) so these
    tests don't need host `zstd`. Exercises the real BEP/exec-log/profile
    shapes, including a failed test (flaky_test) and, in the post-clean
    pair, a disk_cache_hit majority with one local re-execution.
  - a synthetic remote fixture, built here with a small protobuf encoder,
    for the `remote` and `remote cache hit` runner classes this sandbox has
    no RBE endpoint to produce for real.
  - testdata/ci_analytics/poisoned-bep.json: a BEP file whose every
    extractor-visible field (including a couple of hostile labels) carries
    an embedded secret or leaky value. The summary built from it must
    contain none of them.
"""

from __future__ import annotations

import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import unittest

sys.path.insert(0, os.path.dirname(__file__))
import ci_analytics_extract as extract  # noqa: E402

TESTDATA = os.path.join(os.path.dirname(__file__), "testdata", "ci_analytics")
REAL = os.path.join(TESTDATA, "real")


def _deadline(seconds=30.0):
    return time.monotonic() + seconds


# --- a tiny protobuf encoder, for the synthetic remote fixture and the
# adversarial exec-log tests below ---


def _varint(n: int) -> bytes:
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def _tag(field: int, wire: int) -> bytes:
    return _varint((field << 3) | wire)


def _varint_field(field: int, n: int) -> bytes:
    return _tag(field, 0) + _varint(n)


def _bytes_field(field: int, b: bytes) -> bytes:
    return _tag(field, 2) + _varint(len(b)) + b


def _str_field(field: int, s: str) -> bytes:
    return _bytes_field(field, s.encode("utf-8"))


def _duration_bytes(ms: int) -> bytes:
    seconds, nanos = divmod(ms, 1000)
    return _varint_field(1, seconds) + _varint_field(2, nanos * 1_000_000)


def _spawn_metrics(total_ms=0, queue_ms=0, exec_ms=0, input_bytes=0) -> bytes:
    body = b""
    if total_ms:
        body += _bytes_field(1, _duration_bytes(total_ms))
    if queue_ms:
        body += _bytes_field(5, _duration_bytes(queue_ms))
    if exec_ms:
        body += _bytes_field(8, _duration_bytes(exec_ms))
    if input_bytes:
        body += _varint_field(11, input_bytes)
    return body


def _spawn(target, mnemonic, runner, cache_hit, metrics, args=(), env=()) -> bytes:
    body = b""
    for a in args:  # field 1: args. Never decoded by the extractor.
        body += _str_field(1, a)
    for k, v in env:  # field 2: env_vars. Never decoded by the extractor.
        body += _bytes_field(2, _str_field(1, k) + _str_field(2, v))
    body += _str_field(7, target)
    body += _str_field(8, mnemonic)
    body += _str_field(11, runner)
    body += _varint_field(12, 1 if cache_hit else 0)
    body += _bytes_field(18, metrics)
    return body


def _length_prefixed(entry: bytes) -> bytes:
    return _varint(len(entry)) + entry


def _entry_with_spawn(spawn_body: bytes) -> bytes:
    entry = _bytes_field(7, spawn_body)
    return _length_prefixed(entry)


def _invocation_entry(uuid: str) -> bytes:
    return _length_prefixed(_bytes_field(2, _str_field(4, uuid)))


def synthetic_exec_log(uuid: str) -> bytes:
    """A compact exec log (uncompressed; load_exec_log reads it verbatim
    when it does not start with the zstd magic) with an invocation entry
    then two spawns: a `remote` miss with queue time, and a `remote cache
    hit`."""
    out = _invocation_entry(uuid)
    out += _entry_with_spawn(
        _spawn(
            "//:synth_remote",
            "CppCompile",
            "remote",
            False,
            _spawn_metrics(total_ms=900, queue_ms=250, exec_ms=500),
        )
    )
    out += _entry_with_spawn(
        _spawn(
            "//:synth_cached",
            "CppCompile",
            "remote cache hit",
            True,
            _spawn_metrics(total_ms=20),
        )
    )
    return out


def synthetic_bep(uuid: str) -> str:
    events = [
        {
            "id": {"started": {}},
            "started": {
                "uuid": uuid,
                "startTimeMillis": "1700000000000",
                "buildToolVersion": "9.2.0",
                "command": "build",
            },
        },
        {
            "id": {"buildMetrics": {}},
            "buildMetrics": {
                "timingMetrics": {
                    "cpuTimeInMs": "900",
                    "wallTimeInMs": "950",
                    "criticalPathTime": "0.900s",
                },
                "actionSummary": {
                    "actionsCreated": "2",
                    "actionsExecuted": "2",
                    "runnerCount": [
                        {"name": "remote", "count": "1", "execKind": "Remote"},
                        {"name": "remote cache hit", "count": "1", "execKind": "Remote"},
                    ],
                    "actionData": [
                        {
                            "mnemonic": "CppCompile",
                            "actionsExecuted": "2",
                            "actionsCreated": "2",
                            "userTime": "0.400s",
                            "systemTime": "0.100s",
                        }
                    ],
                },
            },
        },
        {
            "id": {"finished": {}},
            "finished": {"exitCode": {"code": 0, "name": "SUCCESS"}, "finishTimeMillis": "1700000001000"},
        },
    ]
    return "\n".join(json.dumps(e) for e in events) + "\n"


class ExtractorHelperTests(unittest.TestCase):
    def test_varint_roundtrip(self):
        for n in (0, 1, 127, 128, 300, 2**32, 2**40 + 7):
            encoded = _varint(n)
            got, p = extract.varint(encoded, 0)
            self.assertEqual(got, n)
            self.assertEqual(p, len(encoded))

    def test_varint_rejects_truncated_input(self):
        with self.assertRaises(ValueError):
            extract.varint(b"\x80", 0)  # continuation bit set, nothing follows

    def test_varint_rejects_overlong_input(self):
        # No terminator within 64 bits: must raise promptly, not loop while
        # accumulating an ever-larger Python int (the quadratic-blowup DoS
        # a reviewer's adversarial script reproduced against the
        # unbounded version of this function).
        start = time.monotonic()
        with self.assertRaises(ValueError):
            extract.varint(b"\xff" * 160000 + b"\x01", 0)
        self.assertLess(time.monotonic() - start, 1.0)

    def test_valid_label(self):
        self.assertEqual(extract.valid_label("//foo/bar:baz"), "//foo/bar:baz")
        self.assertEqual(extract.valid_label("@@rules_cc+//cc:defs"), "@@rules_cc+//cc:defs")
        self.assertIsNone(extract.valid_label("//foo; rm -rf /"))
        self.assertIsNone(extract.valid_label("http://evil.example/x"))

    def test_valid_label_rejects_trailing_newline(self):
        # fullmatch(), not match() with a trailing "$": "$" matches just
        # before a trailing newline under match(), letting a smuggled "\n"
        # through every validator.
        self.assertIsNone(extract.valid_label("//pkg:name\n"))
        self.assertIsNone(extract.valid_mnemonic("GoCompile\n"))
        self.assertIsNone(extract.valid_enum("PASSED\n"))
        self.assertIsNone(extract.valid_repo("@@foo+\n"))

    def test_valid_label_caps_length(self):
        # The colon-separated target-name component is itself capped at 200
        # chars by LABEL_RE; spread the length over a long package path
        # instead, to isolate MAX_LABEL_LEN (512) as the thing under test.
        short_name = "b" * 190
        under_cap = "//" + ("a" * 300) + ":" + short_name  # len 493 < 512
        over_cap = "//" + ("a" * 330) + ":" + short_name  # len 523 > 512
        self.assertEqual(extract.valid_label(under_cap), under_cap)
        self.assertIsNone(extract.valid_label(over_cap))

    def test_valid_label_drops_tripwire_strings_individually(self):
        self.assertIsNone(extract.valid_label("//pkg:x/tmp/y"))
        self.assertIsNone(extract.valid_label("//pkg:x/etc/y"))
        self.assertIsNone(extract.valid_label("//pkg:x/runner/y"))

    def test_valid_mnemonic_rejects_injected_text(self):
        self.assertEqual(extract.valid_mnemonic("CppCompile"), "CppCompile")
        self.assertIsNone(extract.valid_mnemonic("CppCompile; /home/runner/secret"))

    def test_valid_command_and_version(self):
        self.assertEqual(extract.valid_command("test"), "test")
        self.assertIsNone(extract.valid_command("test\nexport X=1"))
        self.assertEqual(extract.valid_version("9.2.0"), "9.2.0")
        self.assertIsNone(extract.valid_version("9.2.0; token=ghp_abc at http://evil"))

    def test_valid_uuid(self):
        self.assertEqual(
            extract.valid_uuid("11111111-2222-3333-4444-555555555555"),
            "11111111-2222-3333-4444-555555555555",
        )
        self.assertIsNone(extract.valid_uuid("ghp_SECRETSECRETSECRET0123456789abcd"))
        self.assertIsNone(extract.valid_uuid("11111111-2222-3333-4444-555555555555\n"))

    def test_valid_lane(self):
        self.assertEqual(extract.valid_lane("unit"), "unit")
        self.assertEqual(extract.valid_lane("integration-packages"), "integration-packages")
        self.assertIsNone(extract.valid_lane("unit Bearer abc.def"))
        self.assertIsNone(extract.valid_lane(""))

    def test_as_int_handles_overflowing_float(self):
        # A crafted numeric literal (e.g. 1e400) parses as float('inf');
        # int(inf) raises OverflowError, uncaught, before this fix.
        self.assertEqual(extract.as_int(float("inf")), 0)
        self.assertEqual(extract.as_int(float("-inf")), 0)
        self.assertEqual(extract.as_int(float("nan")), 0)

    def test_runner_class(self):
        self.assertEqual(extract.runner_class("remote cache hit"), "remote_cache_hit")
        self.assertEqual(extract.runner_class("disk cache hit"), "disk_cache_hit")
        self.assertEqual(extract.runner_class("remote"), "remote")
        self.assertEqual(extract.runner_class("linux-sandbox"), "local")
        self.assertEqual(extract.runner_class("worker"), "local")
        self.assertEqual(extract.runner_class("something-unexpected"), "other")

    def test_clamp_pct_rejects_non_finite_and_out_of_range(self):
        self.assertEqual(extract._clamp_pct(50.0), 50.0)
        self.assertEqual(extract._clamp_pct(0.0), 0.0)
        self.assertEqual(extract._clamp_pct(100.0), 100.0)
        self.assertEqual(extract._clamp_pct(-5.0), 0.0)
        self.assertEqual(extract._clamp_pct(150.0), 100.0)
        self.assertEqual(extract._clamp_pct(float("inf")), 0.0)
        self.assertEqual(extract._clamp_pct(float("-inf")), 0.0)
        self.assertEqual(extract._clamp_pct(float("nan")), 0.0)


class RealFixtureTests(unittest.TestCase):
    """testdata/ci_analytics/real/*: genuine Bazel 9.2.0 output."""

    @staticmethod
    def _fixture_critical_path_time():
        with open(os.path.join(REAL, "cold-bep.json"), encoding="utf-8") as f:
            for line in f:
                event = json.loads(line)
                bm = event.get("buildMetrics")
                if isinstance(bm, dict):
                    tm = bm.get("timingMetrics")
                    if isinstance(tm, dict) and "criticalPathTime" in tm:
                        return tm["criticalPathTime"]
        return None

    def test_cold_invocation_has_a_failed_test_and_local_spawns(self):
        rec = extract.build_invocation(
            "test",
            os.path.join(REAL, "cold-bep.json"),
            os.path.join(REAL, "cold-exec.binpb"),
            os.path.join(REAL, "cold-profile.json"),
            _deadline(),
        )
        self.assertIsNotNone(rec)
        self.assertEqual(rec["command"], "test")
        self.assertEqual(rec["bazel_version"], "9.2.0")
        self.assertEqual(rec["exec_log"], "ok")
        self.assertEqual(rec["profile"], "ok")
        self.assertGreater(rec["timing"]["wall_ms"], 0)
        # criticalPathTime is a Duration string (e.g. "0.212852922s" in the
        # currently-regenerated fixture), not criticalPathTimeInMs: as_int()
        # on that string alone always silently produced 0 before this fix.
        # Read the fixture's own value instead of hardcoding one, since
        # regen.sh is nondeterministic run-to-run.
        self.assertGreater(rec["timing"]["critical_path_ms"], 0)
        self.assertEqual(
            rec["timing"]["critical_path_ms"],
            extract.as_duration_ms(self._fixture_critical_path_time()),
        )

        by_mnemonic = {m["mnemonic"]: m for m in rec["mnemonics"]}
        self.assertIn("TestRunner", by_mnemonic)
        self.assertIn("CppCompile", by_mnemonic)
        # No RBE in this sandbox: every spawn in the cold run is local.
        self.assertEqual(by_mnemonic["TestRunner"]["remote"], 0)
        self.assertEqual(by_mnemonic["TestRunner"]["local"], by_mnemonic["TestRunner"]["spawns"])

        labels = {t["label"]: t for t in rec["tests"]}
        self.assertEqual(labels["//:flaky_test"]["overall"], "FAILED")
        self.assertEqual(labels["//:pass_test"]["overall"], "PASSED")
        self.assertEqual(labels["//:sharded_test"]["shards"], 3)

        # Per-test timing lives at testResult.executionInfo.timingBreakdown,
        # not a top-level testResult.timingBreakdown (which doesn't exist):
        # confirmed non-zero exec_ms for at least one real test result.
        all_results = [r for t in rec["tests"] for r in t["results"]]
        self.assertTrue(any(r["exec_ms"] > 0 for r in all_results))

        # The *_pct fields are always present. They're each 0.0 on this
        # fixture (a sandbox with no RBE endpoint: every percentage in the
        # real "Critical Path: ...% of the time: [...]" breakdown is
        # genuinely 0.00%, not just unparsed) -- the real assertion here is
        # that build_timing() parsed the breakdown text at all (a test_*
        # key below), not that any one percentage is non-zero.
        for key in (
            "remote_pct",
            "queue_pct",
            "fetch_pct",
            "process_pct",
            "upload_pct",
            "setup_pct",
            "network_pct",
        ):
            self.assertIn(key, rec["critical_path"])
        self.assertNotEqual(rec["critical_path"]["test_label"], "")

    def test_postclean_invocation_is_mostly_disk_cache_hits(self):
        rec = extract.build_invocation(
            "test",
            os.path.join(REAL, "postclean-bep.json"),
            os.path.join(REAL, "postclean-exec.binpb"),
            None,
            _deadline(),
        )
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "ok")
        self.assertEqual(rec["profile"], "missing")

        by_mnemonic = {m["mnemonic"]: m for m in rec["mnemonics"]}
        self.assertGreater(by_mnemonic["CppCompile"]["disk_cache_hit"], 0)
        self.assertEqual(by_mnemonic["CppCompile"]["local"], 0)
        # flaky_test's TestRunner spawn is never a disk-cache hit: Bazel
        # does not cache a failed test, so it reruns (a cold row).
        self.assertGreater(by_mnemonic["TestRunner"]["local"], 0)
        cold_targets = {c["target"] for c in rec["cold"]}
        self.assertIn("//:flaky_test", cold_targets)

    def test_repo_fetch_is_read_from_the_profile(self):
        rec = extract.build_invocation(
            "test",
            os.path.join(REAL, "cold-bep.json"),
            None,
            os.path.join(REAL, "cold-profile.json"),
            _deadline(),
        )
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "missing")
        self.assertEqual(rec["profile"], "ok")
        # @@bazel_tools is always fetched (it's bootstrapped, not from a
        # registry cache that might already be warm), so repo_fetch must
        # be non-empty here: this is the must-fix regression test for
        # trim_profile's single-line-document bug, where iter_repo_fetch()
        # silently saw zero "Fetching repository" events no matter what was
        # actually in the profile, and this assertion would have passed
        # vacuously against an empty list.
        self.assertTrue(rec["repo_fetch"])
        for row in rec["repo_fetch"]:
            self.assertIn("repo", row)
            self.assertIn("ms", row)
            self.assertIsNotNone(extract.valid_repo(row["repo"]))

    def test_unreadable_profile_path_is_reported_accurately(self):
        rec = extract.build_invocation(
            "test",
            os.path.join(REAL, "cold-bep.json"),
            None,
            os.path.join(REAL, "does-not-exist-profile.json"),
            _deadline(),
        )
        self.assertIsNotNone(rec)
        self.assertEqual(rec["profile"], "unreadable")

    # Event kinds the extractor itself ever reads out of a BEP event body
    # (plus "id", which is on every event). Anything else showing up in a
    # fixture is, by definition, something the extractor doesn't need --
    # and therefore something regen.sh's filter_bep() should have dropped.
    _EVENT_KIND_ALLOW = {
        "id", "started", "testResult", "testSummary", "buildMetrics", "finished", "buildToolLogs",
    }
    _STARTED_KEY_ALLOW = {"uuid", "command", "buildToolVersion", "startTimeMillis"}
    # Keys that, wherever they appear in the structure, are specifically the
    # ones known to carry operator/host/filesystem detail in real BEP output
    # (test case URIs, output file URIs, exec hostnames, raw status/failure
    # detail protos with command lines and paths baked in).
    _FORBIDDEN_KEYS_ANY_DEPTH = {"uri", "testActionOutput", "hostname", "statusDetails", "failureDetail"}

    # client_env= (not CLIENT_ENVIRONMENT_VARIABLE, a legitimate Bazel
    # skyfunction name that otherwise false-positives on "client_env").
    # Generic enough to catch leaks this fixture's own operator wouldn't
    # produce (a different machine, a different token format, ...), not
    # just the ones this particular sandbox happens to be able to leak.
    _LEAK_PATTERN = extract.re.compile(
        r"client_env=|\"host\"|\"user\"|BUILD_HOST|/Users/|/home/|ghp_|sk-|token|cherry|ubuntu",
        extract.re.IGNORECASE,
    )

    def _walk_json_leak_check(self, obj, path, event_kinds_seen):
        """Recursively asserts structural + string-content leak guards over
        a decoded BEP event's JSON. event_kinds_seen collects top-level
        event-kind keys seen (id/started/testResult/...) for the caller to
        check against _EVENT_KIND_ALLOW."""
        if isinstance(obj, dict):
            for key, value in obj.items():
                self.assertNotIn(
                    key, self._FORBIDDEN_KEYS_ANY_DEPTH, f"{path}.{key} should have been stripped by filter_bep()"
                )
                self._walk_json_leak_check(value, f"{path}.{key}", event_kinds_seen)
        elif isinstance(obj, list):
            for i, value in enumerate(obj):
                self._walk_json_leak_check(value, f"{path}[{i}]", event_kinds_seen)
        elif isinstance(obj, str):
            self.assertIsNone(extract.TRIPWIRE_RE.search(obj), f"{path} matches the document tripwire: {obj!r}")
            self.assertIsNone(self._LEAK_PATTERN.search(obj), f"{path} matches the operator-leak pattern: {obj!r}")

    def test_fixtures_do_not_leak_the_operator_environment(self):
        """The must-fix: a real fixture reviewer-readable on disk, not just
        what the extractor happens to read, must carry nothing from the
        operator's own environment, host, or session.

        Structural, not a raw-text denylist: a denylist tuned to one
        operator misses things like --client_env=GH_TOKEN=ghp_..., a
        different home directory, or an sk-... key. Here we walk the
        *decoded* JSON of every BEP event, assert the event-kind and
        started-key sets are each a subset of what the extractor actually
        reads, assert none of the known leak-prone keys (uri,
        testActionOutput, hostname, statusDetails, failureDetail) appear
        at any nesting depth, and only then fall back to running the
        denylist/tripwire over every decoded string value."""
        for name in ("cold-bep.json", "postclean-bep.json"):
            path = os.path.join(REAL, name)
            with open(path, encoding="utf-8") as f:
                for lineno, line in enumerate(f, start=1):
                    line = line.strip()
                    if not line:
                        continue
                    event = json.loads(line)
                    self.assertIsInstance(event, dict, f"{name}:{lineno} is not an object")
                    kinds = set(event.keys())
                    self.assertTrue(
                        kinds <= self._EVENT_KIND_ALLOW,
                        f"{name}:{lineno} has event kind(s) {kinds - self._EVENT_KIND_ALLOW} outside the allowlist",
                    )
                    if "started" in event and isinstance(event["started"], dict):
                        started_keys = set(event["started"].keys())
                        self.assertTrue(
                            started_keys <= self._STARTED_KEY_ALLOW,
                            f"{name}:{lineno} started has key(s) "
                            f"{started_keys - self._STARTED_KEY_ALLOW} outside the allowlist",
                        )
                    self._walk_json_leak_check(event, f"{name}:{lineno}", kinds)

        # cold-profile.json is Bazel's own line-per-event trace format, not
        # one-JSON-object-per-BEP-line; the leak checks on its string
        # content still apply, but there's no BEP event-kind structure to
        # validate.
        with open(os.path.join(REAL, "cold-profile.json"), encoding="utf-8") as f:
            text = f.read()
        self.assertIsNone(extract.TRIPWIRE_RE.search(text), "cold-profile.json matches the document tripwire")
        self.assertIsNone(
            self._LEAK_PATTERN.search(text), "cold-profile.json matches the operator-leak pattern"
        )

        # The exec logs are zstd-compressed protobuf, not JSON -- decode as
        # best-effort text (the same way `strings` would) and run the same
        # leak pattern over whatever falls out, including non-UTF-8 noise.
        for name in ("cold-exec.binpb", "postclean-exec.binpb"):
            with open(os.path.join(REAL, name), "rb") as f:
                data = f.read()
            self.assertIsNone(
                self._LEAK_PATTERN.search(data.decode("utf-8", "replace")),
                f"{name} matches the operator-leak pattern",
            )


class SyntheticRemoteFixtureTests(unittest.TestCase):
    """A synthetic exec log + BEP for the `remote` and `remote cache hit`
    runner classes, which this sandbox cannot produce against a real RBE
    endpoint (design doc section 3.5)."""

    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        self.uuid = "00000000-0000-0000-0000-000000000001"
        self.bep_path = os.path.join(self.tmpdir.name, "bep.json")
        self.exec_log_path = os.path.join(self.tmpdir.name, "exec.log")
        with open(self.bep_path, "w", encoding="utf-8") as f:
            f.write(synthetic_bep(self.uuid))
        with open(self.exec_log_path, "wb") as f:
            f.write(synthetic_exec_log(self.uuid))

    def test_remote_and_remote_cache_hit_spawns_are_classified(self):
        rec = extract.build_invocation("build", self.bep_path, self.exec_log_path, None, _deadline())
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "ok")
        self.assertEqual(rec["invocation_id"], self.uuid)
        # "0.900s" decodes via as_duration_ms to 900ms exactly.
        self.assertEqual(rec["timing"]["critical_path_ms"], 900)

        by_mnemonic = {m["mnemonic"]: m for m in rec["mnemonics"]}
        cpp = by_mnemonic["CppCompile"]
        self.assertEqual(cpp["remote"], 1)
        self.assertEqual(cpp["remote_cache_hit"], 1)
        self.assertEqual(cpp["remote_queue_ms"], 250)
        self.assertEqual(cpp["remote_queue_max_ms"], 250)
        self.assertEqual(cpp["remote_exec_ms"], 500)

        cold_targets = {c["target"]: c for c in rec["cold"]}
        self.assertIn("//:synth_remote", cold_targets)
        self.assertNotIn("//:synth_cached", cold_targets)  # a hit, not cold


SECRET_MARKERS = (
    "ghp_SECRETTOKEN1234567890",
    "evil.example.com",
    "attacker.example",
    "AWS_SECRET_ACCESS_KEY",
    "grpcs://rbe-west-internal.ops.gascity.com",
    "/etc/secrets/rbe-client.key",
    "DEPLOY_TOKEN",
    "s3cr3t-deploy-token-0001",
    "/root/.ssh/id_rsa",
    "10.0.0.5",
    "ghp_abc123",
    "BEGIN PRIVATE KEY",
    "/home/runner/secret",
    "leak.example.com",
    "hunter2",
)


class PoisonedBEPTests(unittest.TestCase):
    """testdata/ci_analytics/poisoned-bep.json: every field the extractor
    reads carries a secret or a leaky value, including two hostile labels
    (one tripwire-triggering, one with injected text in its status/
    runnerCount fields). The summary must contain none of them, whether
    through whitelist validation or the tripwire -- and a hostile label
    must only drop that one row, not the whole document."""

    def test_poisoned_bep_leaks_nothing(self):
        rec = extract.build_invocation(
            "test", os.path.join(TESTDATA, "poisoned-bep.json"), None, None, _deadline()
        )
        self.assertIsNotNone(rec)
        text = json.dumps(rec, sort_keys=True)
        for marker in SECRET_MARKERS:
            self.assertNotIn(marker, text, f"leaked {marker!r} into the summary")
        self.assertIsNone(extract.TRIPWIRE_RE.search(text))
        # The poisoned fields were dropped, not merely escaped.
        self.assertEqual(rec["command"], "unknown")
        self.assertEqual(rec["bazel_version"], "unknown")
        self.assertEqual(rec["exit_name"], "UNKNOWN")
        # The hostile /etc/-labeled testSummary was dropped entirely, but
        # the rest of the document (e.g. //:pass_test) is still present:
        # one poisoned label does not kill the whole document.
        labels = {t["label"] for t in rec["tests"]}
        self.assertNotIn("//pkg:x/etc/evil_test", labels)
        self.assertIn("//:pass_test", labels)

    def test_full_summary_build_rejects_nothing_silently_wrong(self):
        # The end-to-end path (build_summary -> the tripwire over the
        # serialized output) also sees no leak and still writes a summary
        # (poisoning a field must not suppress the whole invocation).
        summary = extract.build_summary(
            "unit",
            "local",
            "123",
            "0",
            [("test", os.path.join(TESTDATA, "poisoned-bep.json"), None, None)],
            _deadline(),
        )
        self.assertIsNotNone(summary)
        text = json.dumps(summary, sort_keys=True)
        for marker in SECRET_MARKERS:
            self.assertNotIn(marker, text, f"leaked {marker!r} into the summary")

    def test_poisoned_exec_log_leaks_nothing(self):
        """Secrets in a spawn's args/env, next to a hostile mnemonic and a
        hostile target, built with the test-side varint encoder: args/env
        (fields 1/2) are never decoded at all, and the hostile
        mnemonic/target are dropped by valid_mnemonic()/valid_label()."""
        uuid = "00000000-0000-0000-0000-00000000bad1"
        exec_log = _invocation_entry(uuid) + _entry_with_spawn(
            _spawn(
                "//pkg:x/etc/evil",  # hostile target: tripwire substring
                "Evil; /home/runner/secret",  # hostile mnemonic: invalid charset
                "local",
                False,
                _spawn_metrics(total_ms=10, exec_ms=10),
                # A label-shaped secret: if a future regression ever
                # decoded args (field 1) into the label/target used
                # anywhere downstream (e.g. mistaking an argv[] entry for
                # the target), this would surface as a leaked
                # //:hunter2_secret row rather than silently passing.
                args=("--token=hunter2", "//:hunter2_secret", "AKIAIOSFODNN7EXAMPLE"),
                env=(("DEPLOY_TOKEN", "s3cr3t-deploy-token-0001"),),
            )
        )
        with tempfile.TemporaryDirectory() as tmp:
            bep_path = os.path.join(tmp, "bep.json")
            exec_path = os.path.join(tmp, "exec.log")
            with open(bep_path, "w", encoding="utf-8") as f:
                f.write(synthetic_bep(uuid))
            with open(exec_path, "wb") as f:
                f.write(exec_log)
            rec = extract.build_invocation("test", bep_path, exec_path, None, _deadline())
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "ok")
        self.assertEqual(rec["mnemonics"], [])  # the hostile mnemonic was dropped
        self.assertEqual(rec["cold"], [])  # and so is the only spawn carrying it
        text = json.dumps(rec, sort_keys=True)
        for marker in (
            "hunter2",
            "hunter2_secret",
            "AKIAIOSFODNN7EXAMPLE",
            "DEPLOY_TOKEN",
            "s3cr3t-deploy-token-0001",
        ):
            self.assertNotIn(marker, text)

    def test_poisoned_profile_leaks_nothing(self):
        # One event per line -- {"traceEvents":[\n{...},\n{...}\n]} -- the
        # real shape Bazel's own --profile output is in, and the shape
        # iter_repo_fetch() actually reads line by line: a single
        # json.dump() here (one line for the whole document) would make
        # every "Fetching repository" entry live on the one line that also
        # contains the "traceEvents" key, whose top-level dict has no "cat"
        # of its own, so it never reaches ev.get("cat") == "Fetching
        # repository" at all. repo_fetch would always come back empty and
        # this test would pass vacuously, without ever exercising
        # valid_repo() against the hostile name.
        hostile = {
            "cat": "Fetching repository",
            "name": "@@evil+; token=hunter2 /etc/passwd",
            "ts": 0,
            "dur": 1000,
        }
        valid = {"cat": "Fetching repository", "name": "@@rules_cc+", "ts": 2000, "dur": 500}
        with tempfile.TemporaryDirectory() as tmp:
            bep_path = os.path.join(tmp, "bep.json")
            profile_path = os.path.join(tmp, "profile.json")
            with open(bep_path, "w", encoding="utf-8") as f:
                f.write(synthetic_bep("00000000-0000-0000-0000-00000000bad2"))
            with open(profile_path, "w", encoding="utf-8") as f:
                f.write('{"traceEvents":[\n')
                f.write(",\n".join(json.dumps(e) for e in (hostile, valid)))
                f.write("\n]}\n")
            rec = extract.build_invocation("test", bep_path, None, profile_path, _deadline())
        self.assertIsNotNone(rec)
        self.assertEqual(rec["profile"], "ok")
        repos = {r["repo"] for r in rec["repo_fetch"]}
        self.assertNotIn("@@evil+; token=hunter2 /etc/passwd", repos)  # the hostile repo name was dropped
        self.assertIn("@@rules_cc+", repos)  # its valid sibling survives
        text = json.dumps(rec, sort_keys=True)
        self.assertNotIn("hunter2", text)

    def test_one_tripwire_label_drops_only_that_row(self):
        lines = [
            json.dumps(
                {
                    "started": {
                        "uuid": "11111111-2222-3333-4444-555555555555",
                        "command": "test",
                        "buildToolVersion": "9.2.0",
                    }
                }
            ),
            json.dumps(
                {
                    "id": {"testSummary": {"label": "//internal/runner/tmp:x/etc/y_test"}},
                    "testSummary": {"overallStatus": "PASSED"},
                }
            ),
            json.dumps(
                {
                    "id": {"testSummary": {"label": "//:fine_test"}},
                    "testSummary": {"overallStatus": "PASSED"},
                }
            ),
        ]
        with tempfile.TemporaryDirectory() as tmp:
            bep_path = os.path.join(tmp, "t.bep.json")
            out_path = os.path.join(tmp, "t.out.json")
            with open(bep_path, "w", encoding="utf-8") as f:
                f.write("\n".join(lines) + "\n")
            rc = extract.main(
                [
                    "--lane", "unit", "--mode", "local", "--check-run-id", "1", "--pr-hint", "0",
                    "--invocation", "test", bep_path, "--out", out_path,
                ]
            )
            self.assertEqual(rc, 0)
            with open(out_path, encoding="utf-8") as f:
                summary = json.load(f)
        labels = {t["label"] for t in summary["invocations"][0]["tests"]}
        self.assertNotIn("//internal/runner/tmp:x/etc/y_test", labels)
        self.assertIn("//:fine_test", labels)


class CLITests(unittest.TestCase):
    def test_end_to_end_writes_a_schema_v1_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "nested", "summary.json")
            rc = extract.main(
                [
                    "--lane",
                    "unit",
                    "--mode",
                    "local",
                    "--check-run-id",
                    "42",
                    "--pr-hint",
                    "7",
                    "--invocation",
                    "test",
                    os.path.join(REAL, "cold-bep.json"),
                    os.path.join(REAL, "cold-exec.binpb"),
                    os.path.join(REAL, "cold-profile.json"),
                    "--out",
                    out,
                ]
            )
            self.assertEqual(rc, 0)
            with open(out, encoding="utf-8") as f:
                summary = json.load(f)
        self.assertEqual(summary["format"], "ci-analytics-summary")
        self.assertEqual(summary["version"], 1)
        self.assertEqual(summary["lane"], "unit")
        self.assertEqual(summary["check_run_id"], 42)
        self.assertEqual(len(summary["invocations"]), 1)

    def test_no_bep_anywhere_exits_3_and_writes_nothing(self):
        with tempfile.TemporaryDirectory() as tmp:
            missing = os.path.join(tmp, "nope.json")
            out = os.path.join(tmp, "summary.json")
            rc = extract.main(
                [
                    "--lane",
                    "unit",
                    "--mode",
                    "local",
                    "--check-run-id",
                    "1",
                    "--pr-hint",
                    "0",
                    "--invocation",
                    "test",
                    missing,
                    "--out",
                    out,
                ]
            )
            self.assertEqual(rc, 3)
            self.assertFalse(os.path.exists(out))

    def test_too_many_invocations_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "summary.json")
            argv = ["--lane", "unit", "--mode", "local", "--check-run-id", "1", "--pr-hint", "0", "--out", out]
            for i in range(extract.MAX_INVOCATIONS + 1):
                argv += ["--invocation", f"step{i}", os.path.join(REAL, "cold-bep.json")]
            with self.assertRaises(SystemExit):
                extract.parse_args(argv)

    def test_hostile_lane_mode_step_and_uuid_are_rejected_or_dropped(self):
        with tempfile.TemporaryDirectory() as tmp:
            bep_path = os.path.join(tmp, "u.bep.json")
            out = os.path.join(tmp, "u.out.json")
            hostile_uuid = "ghp_SECRETSECRETSECRET0123456789abcd"
            with open(bep_path, "w", encoding="utf-8") as f:
                f.write(
                    json.dumps(
                        {"started": {"uuid": hostile_uuid, "command": "test", "buildToolVersion": "9.2.0"}}
                    )
                    + "\n"
                )
            # --lane, --mode and STEP with injected auth-header-shaped text:
            # rejected at argument-parsing time, before any file is read.
            with self.assertRaises(SystemExit):
                extract.main(
                    [
                        "--lane", "unit Bearer abc.def",
                        "--mode", "remote token=hunter2",
                        "--check-run-id", "1", "--pr-hint", "0",
                        "--invocation", "step with spaces AKIAIOSFODNN7EXAMPLE", bep_path,
                        "--out", out,
                    ]
                )
            # A valid --lane/--mode/STEP, but a hostile started.uuid: the
            # CLI still succeeds, but the hostile uuid never reaches the
            # output (dropped to "", not validated-and-passed-through).
            rc = extract.main(
                [
                    "--lane", "unit", "--mode", "remote", "--check-run-id", "1", "--pr-hint", "0",
                    "--invocation", "test", bep_path, "--out", out,
                ]
            )
            self.assertEqual(rc, 0)
            with open(out, encoding="utf-8") as f:
                text = f.read()
            self.assertNotIn(hostile_uuid, text)

    def test_mode_enum_is_enforced(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = os.path.join(tmp, "summary.json")
            with self.assertRaises(SystemExit):
                extract.parse_args(
                    [
                        "--lane", "unit", "--mode", "not-a-real-mode", "--check-run-id", "1",
                        "--pr-hint", "0", "--invocation", "test", os.path.join(REAL, "cold-bep.json"),
                        "--out", out,
                    ]
                )


class ElapsedTimeFieldTests(unittest.TestCase):
    """buildToolLogs' base64-encoded "elapsed time" / "critical path" logs
    are read; make sure a benign one decodes correctly (the malicious case
    is covered by PoisonedBEPTests)."""

    def test_elapsed_and_critical_path_decode(self):
        text = (
            "Critical Path: 2.50s, Remote (10.0% of the time): [queue: 1.50%, process: 2.25%]\n"
            "  2.50s action 'Testing //:pass_test (shard 0 of 1)'\n"
        )
        events = [
            {"id": {"started": {}}, "started": {"uuid": "u", "command": "test", "buildToolVersion": "9.2.0"}},
            {
                "id": {"buildToolLogs": {}},
                "buildToolLogs": {
                    "log": [
                        {"name": "elapsed time", "contents": base64.b64encode(b"2.5").decode()},
                        {"name": "critical path", "contents": base64.b64encode(text.encode()).decode()},
                    ]
                },
            },
        ]
        started, finished, timing, critical_path, actions, graph, net = extract.build_timing(events)
        self.assertEqual(timing["elapsed_ms"], 2500)
        self.assertEqual(critical_path["remote_pct"], 10.0)
        self.assertEqual(critical_path["queue_pct"], 1.50)
        self.assertEqual(critical_path["process_pct"], 2.25)
        self.assertEqual(critical_path["test_label"], "//:pass_test")
        self.assertEqual(critical_path["test_shard"], 0)
        self.assertEqual(critical_path["test_ms"], 2500)

    def test_critical_path_test_line_matches_unsharded_tests_too(self):
        text = (
            "Critical Path: 1.00s, Remote (0.0% of the time): []\n"
            "  1.00s action 'Testing //:unsharded_test'\n"
        )
        events = [
            {"id": {"started": {}}, "started": {"uuid": "u", "command": "test", "buildToolVersion": "9.2.0"}},
            {
                "id": {"buildToolLogs": {}},
                "buildToolLogs": {
                    "log": [{"name": "critical path", "contents": base64.b64encode(text.encode()).decode()}]
                },
            },
        ]
        _, _, _, critical_path, _, _, _ = extract.build_timing(events)
        self.assertEqual(critical_path["test_label"], "//:unsharded_test")
        self.assertEqual(critical_path["test_shard"], 0)

    def test_critical_path_pct_keys_always_present_even_with_no_match(self):
        events = [
            {"id": {"started": {}}, "started": {"uuid": "u", "command": "test", "buildToolVersion": "9.2.0"}},
        ]
        _, _, _, critical_path, _, _, _ = extract.build_timing(events)
        for key in (
            "remote_pct",
            "queue_pct",
            "fetch_pct",
            "process_pct",
            "upload_pct",
            "setup_pct",
            "network_pct",
        ):
            self.assertEqual(critical_path[key], 0.0)
        # Always present (the S4 contract), even with no critical-path text
        # at all: "" / 0 defaults, not an absent key.
        self.assertEqual(critical_path["test_label"], "")
        self.assertEqual(critical_path["test_shard"], 0)
        self.assertEqual(critical_path["test_ms"], 0)


class RobustnessTests(unittest.TestCase):
    """Every crash a reviewer's adversarial script reproduced against this
    extractor must degrade (a status string, or a smaller/partial result),
    never raise out of build_invocation()."""

    def _bep(self, uuid="11111111-2222-3333-4444-555555555555", extra_started=None, lines_after=()):
        st = {"uuid": uuid, "command": "test", "buildToolVersion": "9.2.0", "startTimeMillis": "1"}
        if extra_started:
            st.update(extra_started)
        lines = [json.dumps({"id": {"started": {}}, "started": st})] + list(lines_after)
        return "\n".join(lines) + "\n"

    def _run(self, bep_text, exec_bytes, deadline=None):
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            with open(bp, "w", encoding="utf-8") as f:
                f.write(bep_text)
            ep = None
            if exec_bytes is not None:
                ep = os.path.join(tmp, "x.exec")
                with open(ep, "wb") as f:
                    f.write(exec_bytes)
            return extract.build_invocation("test", bp, ep, None, deadline if deadline is not None else _deadline())

    def test_truncated_varint_tail_degrades_to_corrupt(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        inv_entry = _invocation_entry(uuid)
        rec = self._run(self._bep(uuid), inv_entry + b"\x80")
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "corrupt")

    def test_wiretype_3_in_entry_degrades_to_corrupt(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        inv_entry = _invocation_entry(uuid)
        rec = self._run(self._bep(uuid), inv_entry + _length_prefixed(b"\x3b"))  # field 7, wire 3
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "corrupt")

    def test_wiretype_6_in_entry_degrades_to_corrupt(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        inv_entry = _invocation_entry(uuid)
        rec = self._run(self._bep(uuid), inv_entry + _length_prefixed(b"\x3e"))  # field 7, wire 6
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "corrupt")

    def test_spawn_target_as_varint_is_skipped_not_crashed(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        inv_entry = _invocation_entry(uuid)
        # Spawn.target_label (field 7) re-encoded as a varint instead of a
        # length-delimited string.
        entry = _length_prefixed(b"\x3a" + _length_prefixed(b"\x38\x01"))
        rec = self._run(self._bep(uuid), inv_entry + entry)
        self.assertIsNotNone(rec)
        self.assertIn(rec["exec_log"], ("ok", "corrupt"))

    def test_metrics_input_bytes_as_bytes_is_skipped_not_crashed(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        inv_entry = _invocation_entry(uuid)
        metrics = b"\x42" + _length_prefixed(b"\x5a" + _length_prefixed(b"xx"))
        entry = _length_prefixed(b"\x3a" + _length_prefixed(metrics))
        rec = self._run(self._bep(uuid), inv_entry + entry)
        self.assertIsNotNone(rec)
        self.assertIn(rec["exec_log"], ("ok", "corrupt"))

    def test_invocation_submessage_as_varint_degrades(self):
        rec = self._run(self._bep(), _length_prefixed(b"\x10\x05"))
        self.assertIsNotNone(rec)
        self.assertIn(rec["exec_log"], ("ok", "mismatch", "corrupt"))

    def test_huge_length_prefix_does_not_crash(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        inv_entry = _invocation_entry(uuid)
        rec = self._run(self._bep(uuid), inv_entry + _varint(2**62) + b"abc")
        self.assertIsNotNone(rec)

    def test_deadline_is_checked_inside_a_single_oversized_submessage(self):
        """spawns()'s own deadline check fires once per ExecLogEntry -- but
        a single entry can itself be made arbitrarily large (a reviewer's
        adversarial script built one 40 MB Spawn out of millions of tiny
        2-byte fields and measured 15.8s against a 5s deadline, since
        nothing inside fields() ever looked at the clock). Build one
        Spawn's SpawnMetrics submessage (field 18) out of many tiny unknown
        fields -- small enough to construct quickly in a test, but still
        many times DEADLINE_CHECK_EVERY -- and confirm an already-elapsed
        deadline is caught well before the whole submessage is decoded."""
        uuid = "11111111-2222-3333-4444-555555555555"
        # field 1 (total_ms, wire 0): 2 bytes/field (tag + a 1-byte value).
        huge_metrics = b"\x08\x00" * (extract.DEADLINE_CHECK_EVERY * 5)
        spawn_body = _spawn("//:huge", "CppCompile", "local", False, huge_metrics)
        exec_log = _invocation_entry(uuid) + _entry_with_spawn(spawn_body)
        start = time.monotonic()
        rec = self._run(self._bep(uuid), exec_log, deadline=time.monotonic() - 1)
        elapsed = time.monotonic() - start
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "timeout")
        self.assertLess(elapsed, 5.0)

    def test_bep_line_is_a_list(self):
        rec = self._run(self._bep(lines_after=["[1,2,3]", '{"finished":{}}']), None)
        self.assertIsNotNone(rec)

    def test_bep_line_is_a_bare_string(self):
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            with open(bp, "w", encoding="utf-8") as f:
                f.write('"started here"\n{"x":1}\n')
            rec = extract.build_invocation("test", bp, None, None, _deadline())
        self.assertIsNone(rec)  # no valid started event anywhere: dropped, not crashed

    def test_bep_inf_number_in_started(self):
        text = self._bep(extra_started={"startTimeMillis": 1e999}).replace("Infinity", "1e999")
        rec = self._run(text, None)
        self.assertIsNotNone(rec)
        self.assertEqual(rec["started_ms"], 0)  # as_int() degraded the overflow to the default

    def test_bep_started_not_a_dict(self):
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            with open(bp, "w", encoding="utf-8") as f:
                f.write('{"started": 5}\n')
            rec = extract.build_invocation("test", bp, None, None, _deadline())
        self.assertIsNone(rec)  # started never became valid: dropped, not crashed

    def test_critpath_bad_float_does_not_crash(self):
        cp = "Critical Path: 1.2.3s, Remote (0.0% of the time): []\n"
        rec = self._run(
            self._bep(
                lines_after=[
                    json.dumps(
                        {
                            "buildToolLogs": {
                                "log": [
                                    {
                                        "name": "critical path",
                                        "contents": base64.b64encode(cp.encode()).decode(),
                                    }
                                ]
                            }
                        }
                    )
                ]
            ),
            None,
        )
        self.assertIsNotNone(rec)

    def test_testresult_not_a_dict(self):
        rec = self._run(
            self._bep(
                lines_after=[
                    json.dumps({"id": {"testResult": {"label": "//:a"}}, "testResult": "PASSED"})
                ]
            ),
            None,
        )
        self.assertIsNotNone(rec)
        self.assertEqual(rec["tests"], [])  # the malformed event was skipped

    def test_runnercount_count_overflow_does_not_crash(self):
        rec = self._run(
            self._bep(
                lines_after=[
                    '{"buildMetrics":{"actionSummary":{"runnerCount":[{"name":"remote","count":1e999}]}}}'
                ]
            ),
            None,
        )
        self.assertIsNotNone(rec)
        self.assertEqual(rec["actions"]["remote"], 0)  # the overflow degraded to the default

    def _profile_with_event(self, event_line):
        return '{"traceEvents":[\n' + event_line + "\n]}\n"

    def test_repo_fetch_infinite_ts_does_not_crash(self):
        # 1e999 parses as float('inf') via json's default Infinity literal
        # (iter_repo_fetch does not reject non-finite constants the way
        # read_bep() does -- it must instead catch them with isfinite()).
        line = json.dumps({"cat": "Fetching repository", "name": "@@evil", "ts": 1e999, "dur": 1}).replace(
            "Infinity", "1e999"
        )
        with tempfile.TemporaryDirectory() as tmp:
            pp = os.path.join(tmp, "p.json")
            with open(pp, "w", encoding="utf-8") as f:
                f.write(self._profile_with_event(line))
            rows, status = extract.iter_repo_fetch(pp, _deadline())
        self.assertEqual(rows, [])
        self.assertEqual(status, "corrupt")

    def test_repo_fetch_nan_ts_does_not_crash(self):
        line = json.dumps({"cat": "Fetching repository", "name": "@@evil", "ts": float("nan"), "dur": 1})
        with tempfile.TemporaryDirectory() as tmp:
            pp = os.path.join(tmp, "p.json")
            with open(pp, "w", encoding="utf-8") as f:
                f.write(self._profile_with_event(line))
            rows, status = extract.iter_repo_fetch(pp, _deadline())
        self.assertEqual(rows, [])
        self.assertEqual(status, "corrupt")

    def test_repo_fetch_overflowing_sum_does_not_crash(self):
        # ts and dur are each individually finite (1e308 is a valid
        # float), but their sum overflows a double to inf: isfinite(start)
        # alone would miss this, only isfinite(end) catches it.
        line = json.dumps({"cat": "Fetching repository", "name": "@@evil", "ts": 1e308, "dur": 1e308})
        with tempfile.TemporaryDirectory() as tmp:
            pp = os.path.join(tmp, "p.json")
            with open(pp, "w", encoding="utf-8") as f:
                f.write(self._profile_with_event(line))
            rows, status = extract.iter_repo_fetch(pp, _deadline())
        self.assertEqual(rows, [])
        self.assertEqual(status, "corrupt")

    def test_repo_fetch_corrupt_entry_does_not_hide_valid_siblings(self):
        bad = json.dumps({"cat": "Fetching repository", "name": "@@evil", "ts": float("nan"), "dur": 1})
        good = json.dumps({"cat": "Fetching repository", "name": "@@rules_cc+", "ts": 0, "dur": 500})
        with tempfile.TemporaryDirectory() as tmp:
            pp = os.path.join(tmp, "p.json")
            with open(pp, "w", encoding="utf-8") as f:
                f.write('{"traceEvents":[\n' + ",\n".join((bad, good)) + "\n]}\n")
            rows, status = extract.iter_repo_fetch(pp, _deadline())
        self.assertEqual(status, "corrupt")
        self.assertEqual([r["repo"] for r in rows], ["@@rules_cc+"])


class BoundsTests(unittest.TestCase):
    def test_exec_log_parse_timeout_is_reported(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        exec_log = _invocation_entry(uuid)
        # DEADLINE_CHECK_EVERY (2000) + a margin of spawn entries, so the
        # deadline check inside spawns() fires at least once. The BEP
        # itself is a single line, so read_bep()'s own (much coarser)
        # check never fires even with an already-elapsed deadline: the
        # timeout this test exercises is specifically the exec-log pass'.
        for i in range(extract.DEADLINE_CHECK_EVERY + 50):
            exec_log += _entry_with_spawn(
                _spawn(f"//:t{i}", "CppCompile", "local", False, _spawn_metrics(total_ms=1))
            )
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            ep = os.path.join(tmp, "x.exec")
            with open(bp, "w", encoding="utf-8") as f:
                f.write(
                    json.dumps(
                        {
                            "id": {"started": {}},
                            "started": {
                                "uuid": uuid,
                                "command": "test",
                                "buildToolVersion": "9.2.0",
                                "startTimeMillis": "1",
                            },
                        }
                    )
                    + "\n"
                )
            with open(ep, "wb") as f:
                f.write(exec_log)
            # An already-elapsed deadline: once the spawn loop's entry
            # count crosses DEADLINE_CHECK_EVERY, it must raise
            # ParseTimeout, which build_invocation turns into an
            # "exec_log": "timeout" status rather than propagating.
            rec = extract.build_invocation("test", bp, ep, None, time.monotonic() - 1)
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "timeout")

    @unittest.skipUnless(shutil.which("zstd"), "zstd not on PATH")
    def test_zstd_round_trip(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        raw = synthetic_exec_log(uuid)
        with tempfile.TemporaryDirectory() as tmp:
            compressed = os.path.join(tmp, "exec.log.zst")
            proc = subprocess.run(
                ["zstd", "-q", "-o", compressed],
                input=raw,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=True,
            )
            del proc
            data, status = extract.load_exec_log(compressed, _deadline())
        self.assertEqual(status, "ok")
        self.assertEqual(data, raw)

    @unittest.skipUnless(shutil.which("zstd"), "zstd not on PATH")
    def test_zstd_decompression_bomb_is_bounded(self):
        # Patch MAX_EXEC_LOG_BYTES down instead of actually decompressing
        # past the real 512 MiB cap: this test only needs to prove
        # load_exec_log() stops reading once the decompressed stream
        # crosses the cap, not that the cap is specifically 512 MiB, and a
        # 600 MiB bomb was slow to build and (briefly) held 512 MiB+ in
        # this process regardless of the outcome.
        old_cap = extract.MAX_EXEC_LOG_BYTES
        extract.MAX_EXEC_LOG_BYTES = 8 * 1024 * 1024  # 8 MiB
        try:
            with tempfile.TemporaryDirectory() as tmp:
                bomb = os.path.join(tmp, "bomb.zst")
                # 64 MiB of zeros, well past the patched 8 MiB cap,
                # compresses to a tiny file almost instantly.
                dd = subprocess.Popen(
                    ["dd", "if=/dev/zero", "bs=1M", "count=64"], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL
                )
                with open(bomb, "wb") as out:
                    zstd = subprocess.Popen(["zstd", "-q", "-1"], stdin=dd.stdout, stdout=out)
                    dd.stdout.close()
                    zstd.communicate(timeout=60)
                dd.wait(timeout=60)
                start = time.monotonic()
                data, status = extract.load_exec_log(bomb, time.monotonic() + 30)
                elapsed = time.monotonic() - start
        finally:
            extract.MAX_EXEC_LOG_BYTES = old_cap
        self.assertIsNone(data)
        self.assertEqual(status, "too_large")
        self.assertLess(elapsed, 30.0)


class ContractTests(unittest.TestCase):
    """The S4 contract: a stable, always-complete key set and status
    vocabulary, so the collector never has to guess at a missing field."""

    def test_max_test_results_is_a_global_total_not_per_label(self):
        lines = [
            json.dumps(
                {
                    "id": {"started": {}},
                    "started": {
                        "uuid": "u",
                        "command": "test",
                        "buildToolVersion": "9.2.0",
                        "startTimeMillis": "1",
                    },
                }
            )
        ]
        # Two labels, each with more rows than half of a small total cap:
        # if the cap were still enforced per-label, both labels would fit
        # under it individually; enforced globally, the combined total must
        # be clipped.
        old_cap = extract.MAX_TEST_RESULTS
        extract.MAX_TEST_RESULTS = 10
        try:
            for label_i in range(2):
                for run in range(8):
                    lines.append(
                        json.dumps(
                            {
                                "id": {
                                    "testResult": {
                                        "label": f"//:label{label_i}",
                                        "run": run,
                                        "shard": 0,
                                        "attempt": 1,
                                    }
                                },
                                "testResult": {"status": "PASSED", "testAttemptDurationMillis": "1"},
                            }
                        )
                    )
            with tempfile.TemporaryDirectory() as tmp:
                bp = os.path.join(tmp, "x.bep.json")
                with open(bp, "w", encoding="utf-8") as f:
                    f.write("\n".join(lines) + "\n")
                rec = extract.build_invocation("test", bp, None, None, _deadline())
        finally:
            extract.MAX_TEST_RESULTS = old_cap
        self.assertIsNotNone(rec)
        total = sum(len(t["results"]) for t in rec["tests"])
        self.assertLessEqual(total, 10)
        self.assertGreater(rec["tests_truncated"], 0)

    def test_exit_code_is_minus_one_without_a_finished_event(self):
        rec = self._run_minimal_bep()
        self.assertEqual(rec["exit_code"], -1)
        self.assertEqual(rec["exit_name"], "UNKNOWN")

    def test_profile_missing_vs_unreadable_vs_ok(self):
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            with open(bp, "w", encoding="utf-8") as f:
                f.write(self._minimal_bep_text())
            rec_missing = extract.build_invocation("test", bp, None, None, _deadline())
            rec_unreadable = extract.build_invocation(
                "test", bp, None, os.path.join(tmp, "nope.json"), _deadline()
            )
            profile_path = os.path.join(tmp, "profile.json")
            with open(profile_path, "w", encoding="utf-8") as f:
                json.dump({"traceEvents": []}, f)
            rec_ok = extract.build_invocation("test", bp, None, profile_path, _deadline())
        self.assertEqual(rec_missing["profile"], "missing")
        self.assertEqual(rec_unreadable["profile"], "unreadable")
        self.assertEqual(rec_ok["profile"], "ok")

    def test_exec_log_missing_vs_unreadable(self):
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            with open(bp, "w", encoding="utf-8") as f:
                f.write(self._minimal_bep_text())
            rec_missing = extract.build_invocation("test", bp, None, None, _deadline())
            rec_unreadable = extract.build_invocation(
                "test", bp, os.path.join(tmp, "nope.exec"), None, _deadline()
            )
        self.assertEqual(rec_missing["exec_log"], "missing")
        self.assertEqual(rec_unreadable["exec_log"], "unreadable")

    @staticmethod
    def _minimal_bep_text():
        return (
            json.dumps(
                {
                    "id": {"started": {}},
                    "started": {
                        "uuid": "11111111-2222-3333-4444-555555555555",
                        "command": "test",
                        "buildToolVersion": "9.2.0",
                        "startTimeMillis": "1",
                    },
                }
            )
            + "\n"
        )

    def _run_minimal_bep(self):
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            with open(bp, "w", encoding="utf-8") as f:
                f.write(self._minimal_bep_text())
            return extract.build_invocation("test", bp, None, None, _deadline())


class BEPReadContractTests(unittest.TestCase):
    """The malformed-middle-line vs. truncated-last-line BEP contract
    (shared with gascity's bep.go / beads' extractor), plus the ported
    beads invocation-id mismatch fixture."""

    def _started_line(self, uuid):
        return json.dumps(
            {
                "id": {"started": {}},
                "started": {
                    "uuid": uuid,
                    "command": "test",
                    "buildToolVersion": "9.2.0",
                    "startTimeMillis": "1",
                },
            }
        )

    def test_malformed_middle_line_drops_the_invocation(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        text = self._started_line(uuid) + "\n{not json\n" + '{"finished":{}}\n'
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            with open(bp, "w", encoding="utf-8") as f:
                f.write(text)
            rec = extract.build_invocation("test", bp, None, None, _deadline())
        self.assertIsNone(rec)

    def test_truncated_last_line_sets_bep_truncated(self):
        uuid = "11111111-2222-3333-4444-555555555555"
        text = self._started_line(uuid) + "\n" + '{"finished": {"exitCode": {"cod'  # cut off mid-line
        with tempfile.TemporaryDirectory() as tmp:
            bp = os.path.join(tmp, "x.bep.json")
            with open(bp, "w", encoding="utf-8") as f:
                f.write(text)
            rec = extract.build_invocation("test", bp, None, None, _deadline())
        self.assertIsNotNone(rec)
        self.assertTrue(rec["bep_truncated"])

    def test_beads_mismatch_fixture_reports_mismatch(self):
        """Ported from beads' tools/bazel/testdata/ci_analytics/
        mismatch.bep.jsonl: a BEP whose started.uuid does not match the
        exec log's embedded invocation id."""
        bep_path = os.path.join(TESTDATA, "mismatch.bep.jsonl")
        with open(bep_path, encoding="utf-8") as f:
            text = f.read()
        self.assertIn("99999999-0000-1111-2222-333333333333", text)
        exec_log = synthetic_exec_log("00000000-aaaa-bbbb-cccc-000000000000")
        with tempfile.TemporaryDirectory() as tmp:
            ep = os.path.join(tmp, "mismatch.exec")
            with open(ep, "wb") as f:
                f.write(exec_log)
            rec = extract.build_invocation("test", bep_path, ep, None, _deadline())
        self.assertIsNotNone(rec)
        self.assertEqual(rec["exec_log"], "mismatch")
        self.assertEqual(rec["mnemonics"], [])


if __name__ == "__main__":
    unittest.main()
