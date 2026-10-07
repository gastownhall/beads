#!/usr/bin/env python3
"""Build a redacted `ci-analytics-summary` v1 JSON from a lane's Bazel outputs.

Design: rbe-ci-bep-analytics-design.md, section 3. This file is a byte copy
across gascity and beads (tools/bazel/ci_analytics_extract.py in both):
stdlib Python 3.12+, like critpath.py.

Inputs, per `--invocation STEP BEP [EXEC_LOG|-] [PROFILE|-]` (repeatable, at
most 8):

  - BEP JSON (`--build_event_json_file`): required. Timing, runner counts,
    test results and summaries, network bytes.
  - the compact execution log (`--execution_log_compact_file`, zstd-framed
    protobuf): per-spawn mnemonic, runner, cache hit, target, SpawnMetrics.
    "-" skips it (no per-mnemonic or per-cold data for that invocation).
  - the JSON profile (`--profile`): "Fetching repository" wall time per
    external repository. "-" skips it.

Redaction is by construction (never read, by omission), by validation (every
emitted string is regex-checked against a whitelist and against the tripwire
individually, so one hostile value is dropped rather than losing the whole
document), and by a final tripwire regex over the serialized output as a
backstop. See REDACTION in this file's comments at each site, and the
design's section 3.4.

Every line of BEP or exec log input is adversarial-input tolerant: a
malformed middle BEP line still drops the invocation (unchanged contract),
but a malformed individual event, protobuf entry or field degrades --
skipping just that event/entry/field -- rather than crashing the process.
See ParseTimeout and the try/except boundaries around the exec-log and BEP
passes in build_invocation().

Exit codes: 0 written | 3 no BEP for any invocation (nothing written) |
4 tripwire (nothing written).

Per-invocation `exec_log`/`profile` status values, for the S4 collector:
  - "ok": present, readable, and parsed (possibly with individually
    dropped/degraded entries -- that alone does not change this status).
  - "missing": "-" was passed for that input; it was never read.
  - "unreadable": the path couldn't be opened (missing file, permissions).
  - "corrupt": opened, but the content was unparseable as a whole, or (for
    the profile) a non-finite ts/dur value was seen and dropped.
  - "timeout": PARSE_DEADLINE_S was exceeded partway through parsing; any
    entries already parsed are kept.
  - "too_large": the input exceeded MAX_EXEC_LOG_BYTES after decompression.
"""

from __future__ import annotations

import argparse
import base64
import json
import math
import os
import re
import shutil
import subprocess
import sys
import time

FORMAT = "ci-analytics-summary"
SCHEMA_VERSION = 1
EXTRACTOR_VERSION = "1.0.0"

MAX_INVOCATIONS = 8
MAX_MNEMONICS = 200
MAX_COLD = 100
MAX_REPOS = 10
MAX_TEST_LABELS = 5000
MAX_TEST_RESULTS = 20000  # total across all labels, not per label
MAX_LABEL_LEN = 512
MAX_EXEC_LOG_BYTES = 512 * 1024 * 1024
PARSE_DEADLINE_S = 60.0
DECOMPRESS_CHUNK = 65536

# How often (in entries/lines) the deadline is re-checked in the hot loops.
# Checking time.monotonic() on every iteration would be needlessly slow;
# checking too rarely defeats the point of a deadline.
DEADLINE_CHECK_EVERY = 2000

ZSTD_MAGIC = b"\x28\xb5\x2f\xfd"

# REDACTION: every string this extractor emits is validated against one of
# these, or dropped. No raw path, hostname, URI or free-text field is ever
# written verbatim; the only free text read at all is the two buildToolLogs
# lines named below, and only through these patterns.
LABEL_RE = re.compile(
    r"(?:@@?[A-Za-z0-9_.~+-]{0,100})?//(?:[A-Za-z0-9_.+=,@~-]+(?:/[A-Za-z0-9_.+=,@~-]+)*)?"
    r":(?!/)[A-Za-z0-9_.+=,@~/-]{1,200}"
)
MNEMONIC_RE = re.compile(r"[A-Za-z][A-Za-z0-9_]{0,63}")
REPO_RE = re.compile(r"@@?[A-Za-z0-9_.~+-]{1,200}")
ENUM_RE = re.compile(r"[A-Z_]{1,40}")
# started.command: a Bazel subcommand name ("test", "build", "coverage", ...).
COMMAND_RE = re.compile(r"[a-z][a-z-]{0,31}")
# started.buildToolVersion: a release ("9.2.0") or a dev build string.
VERSION_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}")
# --invocation STEP, --lane, and matrix.lane values (bazel.yml): short,
# shell- and filename-safe identifiers, never free text.
LANE_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,63}")
# started.uuid / the BEP's own invocation id: a canonical (lowercase-or-not)
# UUID, nothing else.
UUID_RE = re.compile(
    r"[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}"
)
# --mode: the rbe job's decide-mode output (design section 2). Checked as an
# exact set, not a regex.
MODE_CHOICES = frozenset({"remote", "fork-ro", "fork-rw", "cache", "local"})

# REDACTION: the tripwire. If this matches the serialized output, the
# extractor writes nothing and exits 4. It is also checked per-string at
# validation time (REDACTION below, in _validate()), so a single hostile
# label or mnemonic is dropped rather than taking down the whole document;
# this document-level check is the backstop for anything that still slips
# through, e.g. a free-text buildToolLogs line. The collector (design
# section 4.3) runs the same regex over the raw artifact text.
TRIPWIRE_RE = re.compile(
    r"(?i)(://|/home/|/tmp/|/root/|/runner/|/etc/|-----BEGIN|\.ops\.gascity\.com|"
    r"\b(?:\d{1,3}\.){3}\d{1,3}\b)"
)

CRITICAL_PATH_RE = re.compile(
    r"Critical Path: ([0-9.]+)s, Remote \(([0-9.]+)% of the time\): \[(.*)\]"
)
# Each bracketed component ("parse: 0.00%", "queue: 0.00%", ...); matched
# generically and then filtered to the keys this schema keeps.
CRITICAL_PATH_PCT_RE = re.compile(r"([A-Za-z]+): ([0-9.]+)%")
# The per-test critical-path line; the shard clause is optional so unsharded
# tests ("action 'Testing //pkg:t'", no "(shard N of M)") match too.
CRITICAL_PATH_TEST_RE = re.compile(
    r"\s+([0-9.]+)s action 'Testing (//[^ ']+)(?: \(shard ([0-9]+) of ([0-9]+)\))?'"
)
DURATION_RE = re.compile(r"(-?[0-9]+(?:\.[0-9]+)?)s")

# SpawnMetrics Durations (spawn.proto @ 9.2.0). Field 11 (input_bytes) is a
# varint, handled separately.
SPAWN_METRIC = {
    1: "total_ms",
    3: "network_ms",
    4: "fetch_ms",
    5: "queue_ms",
    6: "setup_ms",
    7: "upload_ms",
    8: "exec_ms",
    9: "outputs_ms",
}

# Runner classes (design section 3.3). The raw runner name is never emitted.
_RUNNER_EXACT = {
    "remote cache hit": "remote_cache_hit",
    "disk cache hit": "disk_cache_hit",
    "remote": "remote",
}
_RUNNER_LOCAL = {
    "linux-sandbox",
    "processwrapper-sandbox",
    "darwin-sandbox",
    "local",
    "worker",
    "standalone",
    "sandboxed",
    "windows-sandbox",
    "docker",
    "dynamic",
}


def runner_class(name: str) -> str:
    if name in _RUNNER_EXACT:
        return _RUNNER_EXACT[name]
    if name in _RUNNER_LOCAL:
        return "local"
    return "other"


class Tripwire(Exception):
    """Raised when the tripwire regex matches; caught at top level."""


class ParseTimeout(Exception):
    """Raised by read_bep()/spawns() when PARSE_DEADLINE_S is exceeded."""


def _reject_constant(constant):  # json.loads(parse_constant=...)
    raise ValueError("non-finite JSON constant %r" % (constant,))


def as_int(value, default=0):
    """Decodes a proto3-JSON integer: Bazel writes int64 as a string and
    int32 as a number; both decode here. Guards against OverflowError from
    a huge/infinite float (e.g. a crafted 1e400 literal, or float('inf'))."""
    if value is None:
        return default
    if isinstance(value, bool):
        return int(value)
    if isinstance(value, int):
        return value
    if isinstance(value, float):
        try:
            return int(value)
        except (OverflowError, ValueError):
            return default
    if isinstance(value, str):
        try:
            return int(value)
        except (ValueError, OverflowError):
            return default
    return default


def as_duration_ms(value):
    """Decodes a proto3 Duration JSON string ("3.140856289s") to int ms."""
    if not isinstance(value, str):
        return None
    m = DURATION_RE.fullmatch(value)
    if not m:
        return None
    try:
        return round(float(m.group(1)) * 1000)
    except (ValueError, OverflowError):
        return None


def _safe_float(s, default=0.0):
    try:
        return float(s)
    except (ValueError, OverflowError, TypeError):
        return default


def _clamp_pct(value):
    """Clamps a critical-path percentage to a finite [0, 100]. The regex
    that extracts these values only ever captures digits and a single dot,
    so float() on it can't itself produce inf/nan -- but this is cheap
    insurance against a future, looser regex or a _safe_float() call site
    that doesn't exist yet, and it's what keeps every *_pct value safe to
    pass straight to json.dumps(..., allow_nan=False)."""
    if not math.isfinite(value):
        return 0.0
    return max(0.0, min(100.0, value))


def _validate(s, pattern, max_len=None):
    """REDACTION: the shared validator every valid_*() below calls. Checks
    type, an optional length cap, the whitelist pattern (fullmatch: a
    pattern ending in a bare `$` can still be satisfied by a string with a
    trailing "\\n" under .match(), since `$` matches just before a trailing
    newline -- fullmatch closes that gap), and the tripwire, individually.
    A value that fails any of these is dropped (returns None); the caller
    simply omits that one field/row rather than the whole document."""
    if not isinstance(s, str):
        return None
    if max_len is not None and len(s) > max_len:
        return None
    if not pattern.fullmatch(s):
        return None
    if TRIPWIRE_RE.search(s):
        return None
    return s


def valid_label(s):
    return _validate(s, LABEL_RE, MAX_LABEL_LEN)


def valid_mnemonic(s):
    return _validate(s, MNEMONIC_RE)


def valid_repo(s):
    return _validate(s, REPO_RE)


def valid_enum(s):
    return _validate(s, ENUM_RE)


def valid_command(s):
    return _validate(s, COMMAND_RE)


def valid_version(s):
    return _validate(s, VERSION_RE)


def valid_lane(s):
    return _validate(s, LANE_RE)


def valid_uuid(s):
    return _validate(s, UUID_RE)


# --- exec log: field-number protobuf decoding (spawn.proto @ 9.2.0) ---
#
# REDACTION: Spawn fields 7 target, 8 mnemonic, 9 exit code, 11 runner, 12
# cache hit and 18 metrics are decoded; Invocation.id (field 4) is read only
# for the cross-check. Fields 1 (args), 2 (env_vars) and 3 (platform) are
# skipped by length, never decoded: this is the only place the exec log's
# argv and environment values could leak, and this code never looks at them.
#
# Every function here assumes nothing about its input's shape beyond what
# the length-prefixed wire format itself guarantees; a value that isn't the
# type a field is supposed to have (e.g. a string field re-encoded as a
# varint by a hostile or corrupt producer) is skipped, not force-cast. What
# this layer can't degrade from on its own (a truncated varint, an
# unsupported wire type, an absurd nesting) is left to raise; the caller
# (build_mnemonics_and_cold, from build_invocation) wraps the whole pass in
# try/except and degrades the invocation's exec_log status to "corrupt" or
# "timeout" instead of crashing the process.


def varint(b, p):
    s = r = 0
    n = len(b)
    while True:
        if p >= n:
            raise ValueError("truncated varint")
        c = b[p]
        p += 1
        r |= (c & 0x7F) << s
        s += 7
        if not c & 0x80:
            return r, p
        if s > 63:
            # Bounds the loop (a crafted run of 0xFF bytes with no
            # terminator would otherwise grow `r` into an arbitrarily large
            # Python int, making every subsequent shift/OR progressively
            # more expensive -- an O(n^2) DoS, not just an eventual
            # IndexError) and rejects anything wider than a 64-bit value.
            raise ValueError("varint exceeds 64 bits")


def fields(b, deadline=None):
    """Yields (field, wiretype, value); length-delimited values are slices,
    never decoded here.

    A single field-rich submessage (e.g. a 40 MB Spawn made of millions of
    tiny 2-byte unknown fields) can take many seconds to merely iterate,
    even though spawns()'s own per-entry deadline check never sees it: that
    check only fires once per ExecLogEntry, not once per field inside one.
    deadline, when given, is re-checked every DEADLINE_CHECK_EVERY fields
    here too, so a single oversized submessage at any nesting level can't
    run past PARSE_DEADLINE_S on its own."""
    p = 0
    n = len(b)
    count = 0
    while p < n:
        count += 1
        if deadline is not None and count % DEADLINE_CHECK_EVERY == 0 and time.monotonic() > deadline:
            raise ParseTimeout("protobuf field parse deadline exceeded")
        k, p = varint(b, p)
        f, t = k >> 3, k & 7
        if t == 0:
            v, p = varint(b, p)
        elif t == 2:
            ln, p = varint(b, p)
            v = b[p : p + ln]
            p += ln
        elif t == 1:
            v = b[p : p + 8]
            p += 8
        elif t == 5:
            v = b[p : p + 4]
            p += 4
        else:
            raise ValueError("wire type %d" % t)
        yield f, t, v


def dur_ms(v, deadline=None):
    """Decodes a Duration submessage's bytes (fields 1 seconds, 2 nanos)."""
    seconds = nanos = 0
    for f, t, val in fields(v, deadline):
        if t != 0:
            continue
        if f == 1:
            seconds = val
        elif f == 2:
            nanos = val
    return seconds * 1000 + nanos // 1_000_000


def spawns(raw, deadline):
    """Yields (invocation_id, spawn-dict) for every ExecLogEntry.spawn in the
    decompressed exec log stream. ExecLogEntry: 1 id, 2 invocation, 7 spawn;
    the rest skipped. Every sub-field is type-checked before use: a hostile
    or corrupt encoder can put any wire type on any field number, and a
    string/submessage field that arrives as a varint (int, not bytes) is
    simply skipped rather than passed to .decode()/fields()."""
    p = 0
    n = len(raw)
    inv = None
    count = 0
    while p < n:
        count += 1
        if count % DEADLINE_CHECK_EVERY == 0 and time.monotonic() > deadline:
            raise ParseTimeout("exec log parse deadline exceeded")
        ln, p = varint(raw, p)
        entry = raw[p : p + ln]
        p += ln
        for f, _, v in fields(entry, deadline):
            if f == 2 and isinstance(v, bytes):
                inv = next(
                    (
                        sv.decode("utf-8", "replace")
                        for sf, _, sv in fields(v, deadline)
                        if sf == 4 and isinstance(sv, bytes)
                    ),
                    inv,
                )
            elif f == 7 and isinstance(v, bytes):
                s = {"target": "", "mnemonic": "", "runner": "", "cache_hit": False, "m": {}}
                for sf, st, sv in fields(v, deadline):  # 1 args, 2 env_vars, 3 platform: never decoded
                    if sf == 7 and isinstance(sv, bytes):
                        s["target"] = sv.decode("utf-8", "replace")
                    elif sf == 8 and isinstance(sv, bytes):
                        s["mnemonic"] = sv.decode("utf-8", "replace")
                    elif sf == 11 and isinstance(sv, bytes):
                        s["runner"] = sv.decode("utf-8", "replace")
                    elif sf == 12:
                        s["cache_hit"] = bool(sv)
                    elif sf == 18 and isinstance(sv, bytes):
                        for mf, mt, mv in fields(sv, deadline):
                            if mf in SPAWN_METRIC and mt == 2 and isinstance(mv, bytes):
                                s["m"][SPAWN_METRIC[mf]] = dur_ms(mv, deadline)
                            elif mf == 11 and mt == 0:
                                s["m"]["input_bytes"] = mv
                yield inv, s


def load_exec_log(path, deadline):
    """Returns (decompressed bytes or None, status) where status is one of
    "ok", "unreadable", "too_large"; None data means no per-spawn data for
    this invocation (BEP-level data is still emitted).

    The compressed file's size is stat-capped before it is ever opened, and
    decompression itself runs under a Popen whose stdout is read in bounded
    chunks: at most MAX_EXEC_LOG_BYTES + 1 bytes are ever read before the
    child is killed, so a zstd decompression bomb can't grow this process's
    memory past that bound no matter how small the compressed input is.
    --long=31 (which lets zstd negotiate up to a 2 GiB window) is
    deliberately not passed, for the same reason.
    """
    try:
        st = os.stat(path)
    except OSError:
        return None, "unreadable"
    if st.st_size > MAX_EXEC_LOG_BYTES:
        return None, "too_large"
    try:
        with open(path, "rb") as fh:
            magic = fh.read(4)
    except OSError:
        return None, "unreadable"
    if magic != ZSTD_MAGIC:
        try:
            with open(path, "rb") as fh:
                data = fh.read()
        except OSError:
            return None, "unreadable"
        if len(data) > MAX_EXEC_LOG_BYTES:
            return None, "too_large"
        return data, "ok"

    zstd = shutil.which("zstd")
    if zstd is None:
        return None, "unreadable"
    try:
        in_fh = open(path, "rb")
    except OSError:
        return None, "unreadable"
    try:
        proc = subprocess.Popen(
            [zstd, "-dc"],
            stdin=in_fh,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        )
    except OSError:
        return None, "unreadable"
    finally:
        in_fh.close()  # Popen dup'd it; closing our copy doesn't affect the child.

    chunks = []
    total = 0
    too_large = False
    try:
        try:
            while True:
                if time.monotonic() > deadline:
                    proc.kill()
                    proc.wait()
                    return None, "unreadable"
                chunk = proc.stdout.read(DECOMPRESS_CHUNK)
                if not chunk:
                    break
                total += len(chunk)
                if total > MAX_EXEC_LOG_BYTES:
                    too_large = True
                    proc.kill()
                    break
                chunks.append(chunk)
            proc.wait(timeout=max(1.0, deadline - time.monotonic()))
        except (OSError, subprocess.SubprocessError):
            try:
                proc.kill()
            except OSError:
                pass
            return None, "unreadable"
    finally:
        proc.stdout.close()

    if too_large:
        return None, "too_large"
    if proc.returncode != 0:
        return None, "unreadable"
    return b"".join(chunks), "ok"


# --- BEP: newline-delimited JSON ---


def read_bep(path, deadline):
    """Reads the BEP JSON stream. Returns (events, truncated). A malformed
    last line sets truncated; a malformed middle line raises ValueError (the
    whole invocation is dropped, the same contract as gascity bep.go)."""
    events = []
    pending_err = None
    count = 0
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        for line in fh:
            count += 1
            if count % DEADLINE_CHECK_EVERY == 0 and time.monotonic() > deadline:
                raise ParseTimeout("BEP parse deadline exceeded")
            line = line.strip()
            if not line:
                continue
            if pending_err is not None:
                raise ValueError(pending_err)
            try:
                events.append(json.loads(line, parse_constant=_reject_constant))
            except ValueError as exc:
                pending_err = str(exc)
    return events, pending_err is not None


def iter_repo_fetch(profile_path, deadline):
    """Streams the JSON profile line by line; only lines mentioning
    "Fetching repository" are parsed (design section 3.5's cap). Computes
    the wall-clock union of each name's intervals (not just a min-start/
    max-end span: two disjoint fetches of the same repo should not count the
    gap between them). Returns (rows, status), status one of "ok",
    "unreadable", "corrupt" (a non-finite ts/dur -- Infinity, NaN, or a sum
    that overflows to inf -- was seen and dropped; any other valid rows
    parsed are still returned)."""
    intervals = {}  # name -> sorted list of disjoint, merged [start, end)
    corrupt = False
    try:
        fh = open(profile_path, "r", encoding="utf-8", errors="replace")
    except OSError:
        return [], "unreadable"
    with fh:
        for line in fh:
            if time.monotonic() > deadline:
                break
            if "Fetching repository" not in line:
                continue
            stripped = line.rstrip(",\n")
            try:
                # Default parse_constant (not _reject_constant): a crafted
                # "ts": Infinity/NaN literal must reach the isfinite()
                # check below and flip this profile's status to "corrupt",
                # not raise out of json.loads() and get silently skipped.
                ev = json.loads(stripped)
            except ValueError:
                continue
            if not isinstance(ev, dict) or ev.get("cat") != "Fetching repository":
                continue
            name = ev.get("name")
            ts = ev.get("ts")
            dur = ev.get("dur")
            if (
                not isinstance(name, str)
                or not isinstance(ts, (int, float))
                or not isinstance(dur, (int, float))
            ):
                continue
            # A crafted ts/dur (1e999 parses as inf via _reject_constant's
            # sibling path below; ts=1e308 + dur=1e308 overflows a finite
            # sum to inf without raising) must degrade this invocation's
            # profile status to "corrupt", not crash or silently produce an
            # infinite/NaN interval that a naive sum() would then also
            # produce as a reported "ms" value.
            try:
                start, end = ts, ts + dur
                if not (math.isfinite(start) and math.isfinite(end)):
                    corrupt = True
                    continue
            except (OverflowError, ValueError, TypeError):
                corrupt = True
                continue
            merged = intervals.setdefault(name, [])
            _merge_interval(merged, start, end)
    out = []
    for name, merged in intervals.items():
        repo = valid_repo(name)
        if repo is None:
            continue
        total_us = sum(e - s for s, e in merged)
        out.append({"repo": repo, "ms": round(total_us / 1000.0)})
    out.sort(key=lambda r: r["ms"], reverse=True)
    return out[:MAX_REPOS], ("corrupt" if corrupt else "ok")


def _merge_interval(merged, start, end):
    """Inserts [start, end) into `merged`, a list of disjoint intervals kept
    sorted by start, merging with any overlapping/adjacent neighbors so the
    list always represents the true union."""
    out = []
    placed = False
    for s, e in merged:
        if end < s:
            if not placed:
                out.append((start, end))
                placed = True
            out.append((s, e))
        elif start > e:
            out.append((s, e))
        else:
            start, end = min(start, s), max(end, e)
    if not placed:
        out.append((start, end))
    out.sort()
    merged[:] = out


# --- per-invocation BEP extraction ---


def _empty_critical_path():
    return {
        "remote_pct": 0.0,
        "queue_pct": 0.0,
        "fetch_pct": 0.0,
        "process_pct": 0.0,
        "upload_pct": 0.0,
        "setup_pct": 0.0,
        "network_pct": 0.0,
        # Always present (the S4 contract: a stable, always-complete key
        # set), even when the critical path text has no per-test line at
        # all (a pure-build invocation, or a critpath_bad_float-style
        # degrade): "" / 0, never an absent key the collector has to guess
        # the type of.
        "test_label": "",
        "test_shard": 0,
        "test_ms": 0,
    }


def build_timing(events):
    timing = {
        "elapsed_ms": 0,
        "wall_ms": 0,
        "cpu_ms": 0,
        "analysis_ms": 0,
        "execution_ms": 0,
        "actions_start_ms": 0,
        "critical_path_ms": 0,
    }
    started = None
    finished = None
    # Always a full key set (design/S4 contract): a lane with no critical
    # path text still reports every *_pct at 0.0, rather than an empty dict.
    critical_path = _empty_critical_path()
    actions = {
        "created": 0,
        "executed": 0,
        "remote_cache_hit": 0,
        "disk_cache_hit": 0,
        "remote": 0,
        "local": 0,
        "internal": 0,
        "action_cache_hits": 0,
        "action_cache_misses": 0,
    }
    graph = {"packages_loaded": 0, "targets_configured": 0}
    net = {"bytes_recv": 0, "bytes_sent": 0}

    for ev in events:
        # Tolerates a BEP line that decoded to a list, a string, or any
        # other non-dict JSON value (malformed middle lines already raise
        # in read_bep(); this is for individually-wrong-shaped lines that
        # parsed fine as JSON but aren't the object BEP lines always are).
        if not isinstance(ev, dict):
            continue
        try:
            if "started" in ev:
                s = ev["started"]
                if isinstance(s, dict):
                    started = {
                        "invocation_id": valid_uuid(s.get("uuid", "")) or "",
                        "command": valid_command(s.get("command", "")) or "unknown",
                        "bazel_version": valid_version(s.get("buildToolVersion", "")) or "unknown",
                        "started_ms": as_int(s.get("startTimeMillis")),
                    }
            if "finished" in ev:
                fin = ev["finished"]
                if isinstance(fin, dict):
                    exit_code = fin.get("exitCode")
                    if not isinstance(exit_code, dict):
                        exit_code = {}
                    finished = {
                        "exit_code": as_int(exit_code.get("code")),
                        "exit_name": exit_code.get("name", ""),
                        "finished_ms": as_int(fin.get("finishTimeMillis")),
                    }
            if "buildToolLogs" in ev:
                btl = ev["buildToolLogs"]
                logs = btl.get("log", []) if isinstance(btl, dict) else []
                for log in logs or []:
                    if not isinstance(log, dict):
                        continue
                    name = log.get("name")
                    contents = log.get("contents")
                    if name == "elapsed time" and contents:
                        try:
                            timing["elapsed_ms"] = round(float(base64.b64decode(contents)) * 1000)
                        except (ValueError, TypeError, OverflowError):
                            pass
                    elif name == "critical path" and contents:
                        try:
                            text = base64.b64decode(contents).decode("utf-8", "replace")
                        except (ValueError, TypeError):
                            text = ""
                        first_line = text.splitlines()[0] if text else ""
                        m = CRITICAL_PATH_RE.fullmatch(first_line)
                        if m:
                            cp = dict(critical_path)
                            cp["remote_pct"] = _clamp_pct(_safe_float(m.group(2)))
                            for key, val in CRITICAL_PATH_PCT_RE.findall(m.group(3)):
                                field = "%s_pct" % key
                                if field in cp:
                                    cp[field] = _clamp_pct(_safe_float(val))
                            critical_path = cp
                            for line in text.splitlines()[1:]:
                                tm = CRITICAL_PATH_TEST_RE.fullmatch(line)
                                if tm:
                                    label = valid_label(tm.group(2))
                                    if label is not None:
                                        critical_path["test_label"] = label
                                        critical_path["test_shard"] = (
                                            as_int(tm.group(3)) if tm.group(3) else 0
                                        )
                                        critical_path["test_ms"] = round(
                                            _safe_float(tm.group(1)) * 1000
                                        )
                                    break
            if "buildMetrics" in ev:
                bm = ev["buildMetrics"]
                if not isinstance(bm, dict):
                    continue
                tm = bm.get("timingMetrics")
                if not isinstance(tm, dict):
                    tm = {}
                for key, dst in (
                    ("cpuTimeInMs", "cpu_ms"),
                    ("wallTimeInMs", "wall_ms"),
                    ("analysisPhaseTimeInMs", "analysis_ms"),
                    ("executionPhaseTimeInMs", "execution_ms"),
                    ("actionsExecutionStartInMs", "actions_start_ms"),
                ):
                    if key in tm:
                        timing[dst] = as_int(tm.get(key))
                # Bazel 9.2.0 sends criticalPathTime as a Duration string
                # ("0.203047257s"), not criticalPathTimeInMs: as_int() on
                # that string always returned the default 0, silently.
                # Parse the Duration first; fall back to a legacy/future
                # criticalPathTimeInMs integer field if present instead.
                cpt_ms = as_duration_ms(tm.get("criticalPathTime"))
                if cpt_ms is None and "criticalPathTimeInMs" in tm:
                    cpt_ms = as_int(tm.get("criticalPathTimeInMs"))
                if cpt_ms is not None:
                    timing["critical_path_ms"] = cpt_ms

                asum = bm.get("actionSummary")
                if not isinstance(asum, dict):
                    asum = {}
                if "actionsCreated" in asum:
                    actions["created"] = as_int(asum.get("actionsCreated"))
                if "actionsExecuted" in asum:
                    actions["executed"] = as_int(asum.get("actionsExecuted"))
                local_count = 0
                for rc in asum.get("runnerCount", []) or []:
                    if not isinstance(rc, dict):
                        continue
                    name = rc.get("name", "")
                    count = as_int(rc.get("count"))
                    if rc.get("execKind") == "Local":
                        local_count += count
                        continue
                    if name == "remote cache hit":
                        actions["remote_cache_hit"] += count
                    elif name == "disk cache hit":
                        actions["disk_cache_hit"] += count
                    elif name == "remote":
                        actions["remote"] += count
                    elif name == "internal":
                        actions["internal"] += count
                actions["local"] = local_count
                acs = asum.get("actionCacheStatistics")
                if isinstance(acs, dict):
                    actions["action_cache_hits"] = as_int(acs.get("hits"))
                    actions["action_cache_misses"] = as_int(acs.get("misses"))

                pm = bm.get("packageMetrics")
                if isinstance(pm, dict):
                    graph["packages_loaded"] = as_int(pm.get("packagesLoaded"))
                tgm = bm.get("targetMetrics")
                if isinstance(tgm, dict):
                    graph["targets_configured"] = as_int(tgm.get("targetsConfigured"))
                nmw = bm.get("networkMetrics")
                nm = nmw.get("systemNetworkStats") if isinstance(nmw, dict) else None
                if isinstance(nm, dict):
                    net["bytes_recv"] = as_int(nm.get("bytesRecv"))
                    net["bytes_sent"] = as_int(nm.get("bytesSent"))
        except Exception:
            # Degrade to a partial BEP: one malformed event (e.g. "started"
            # or "testResult" present but holding a non-dict value) is
            # skipped; every other event in this stream is still processed.
            continue

    return started, finished, timing, critical_path, actions, graph, net


def build_action_data(events):
    """BEP buildMetrics.actionSummary.actionData[]: per-mnemonic executed,
    created, userTime, systemTime. Top 20 only (Bazel's own cap)."""
    data = {}
    for ev in events:
        if not isinstance(ev, dict):
            continue
        try:
            bm = ev.get("buildMetrics")
            if not isinstance(bm, dict):
                continue
            asum = bm.get("actionSummary")
            if not isinstance(asum, dict):
                continue
            for ad in asum.get("actionData", []) or []:
                if not isinstance(ad, dict):
                    continue
                mnemonic = valid_mnemonic(ad.get("mnemonic", ""))
                if mnemonic is None:
                    continue
                d = data.setdefault(
                    mnemonic, {"bep_executed": 0, "bep_created": 0, "user_ms": 0, "system_ms": 0}
                )
                d["bep_executed"] += as_int(ad.get("actionsExecuted"))
                d["bep_created"] += as_int(ad.get("actionsCreated"))
                d["user_ms"] += as_duration_ms(ad.get("userTime")) or 0
                d["system_ms"] += as_duration_ms(ad.get("systemTime")) or 0
        except Exception:
            continue
    return data


def build_tests(events):
    """testResult + testSummary, grouped by label. MAX_TEST_RESULTS is a
    total across every label (the S4 contract), not a per-label cap."""
    tests = {}  # label -> {summary, results: []}
    truncated = 0
    total_results = 0
    for ev in events:
        if not isinstance(ev, dict):
            continue
        try:
            ident = ev.get("id")
            if not isinstance(ident, dict):
                ident = {}
            tr = ev.get("testResult")
            if tr is not None and isinstance(tr, dict):
                tid = ident.get("testResult")
                if not isinstance(tid, dict):
                    tid = {}
                label = valid_label(tid.get("label", ""))
                if label is None:
                    continue
                entry = tests.setdefault(label, {"summary": None, "results": []})
                if total_results >= MAX_TEST_RESULTS:
                    truncated += 1
                    continue
                status = valid_enum(tr.get("status", "")) or "UNKNOWN"
                execinfo = tr.get("executionInfo")
                if not isinstance(execinfo, dict):
                    execinfo = {}
                strategy = execinfo.get("strategy", "")
                cached_locally = bool(tr.get("cachedLocally"))
                runner = "local_cache" if cached_locally else runner_class(strategy)
                duration_ms = as_duration_ms(tr.get("testAttemptDuration"))
                if duration_ms is None:
                    duration_ms = as_int(tr.get("testAttemptDurationMillis"))
                queue_ms = fetch_ms = exec_ms = 0
                # Per-test timing lives under testResult.executionInfo.
                # timingBreakdown, not a top-level testResult.timingBreakdown
                # (which doesn't exist at all) -- confirmed against a real
                # Bazel 9.2 fixture.
                tbd = execinfo.get("timingBreakdown")
                children = tbd.get("child", []) if isinstance(tbd, dict) else []
                for child in children or []:
                    if not isinstance(child, dict):
                        continue
                    name = child.get("name")
                    ms = as_duration_ms(child.get("time"))
                    if ms is None:
                        continue
                    if name == "queueTime":
                        queue_ms = ms
                    elif name == "fetchTime":
                        fetch_ms = ms
                    elif name == "executionWallTime":
                        exec_ms = ms
                entry["results"].append(
                    {
                        "shard": as_int(tid.get("shard")),
                        "run": as_int(tid.get("run")),
                        "attempt": as_int(tid.get("attempt")),
                        "status": status,
                        "runner": runner,
                        "duration_ms": duration_ms,
                        "queue_ms": queue_ms,
                        "fetch_ms": fetch_ms,
                        "exec_ms": exec_ms,
                    }
                )
                total_results += 1
            ts = ev.get("testSummary")
            if ts is not None and isinstance(ts, dict):
                tid = ident.get("testSummary")
                if not isinstance(tid, dict):
                    tid = {}
                label = valid_label(tid.get("label", ""))
                if label is None:
                    continue
                entry = tests.setdefault(label, {"summary": None, "results": []})
                entry["summary"] = {
                    "overall": valid_enum(ts.get("overallStatus", "")) or "UNKNOWN",
                    "shards": as_int(ts.get("shardCount")) or 1,
                    "attempts": as_int(ts.get("attemptCount")) or 1,
                    "cached": as_int(ts.get("totalNumCached")),
                }
        except Exception:
            continue

    labels = sorted(tests.keys())
    tests_truncated = 0
    if len(labels) > MAX_TEST_LABELS:
        tests_truncated = len(labels) - MAX_TEST_LABELS
        labels = labels[:MAX_TEST_LABELS]
    out = []
    for label in labels:
        entry = tests[label]
        summary = entry["summary"] or {
            "overall": "UNKNOWN",
            "shards": 1,
            "attempts": 1,
            "cached": 0,
        }
        out.append(
            {
                "label": label,
                "overall": summary["overall"],
                "shards": summary["shards"],
                "attempts": summary["attempts"],
                "cached": summary["cached"],
                "results": entry["results"],
            }
        )
    return out, tests_truncated + truncated


def build_mnemonics_and_cold(started_uuid, exec_log_data, deadline):
    """Groups exec-log spawns by mnemonic (mnemonics[]) and finds the
    cache-missing ones by (mnemonic, target) (cold[])."""
    by_mnemonic = {}
    cold = {}
    matched_invocation = False
    if exec_log_data is None:
        return {}, {}, False
    for inv, spawn in spawns(exec_log_data, deadline):
        if inv is not None:
            if started_uuid and inv != started_uuid:
                continue
            matched_invocation = True
        mnemonic = valid_mnemonic(spawn["mnemonic"])
        if mnemonic is None:
            continue
        cls = runner_class(spawn["runner"])
        m = spawn["m"]
        d = by_mnemonic.setdefault(
            mnemonic,
            {
                "spawns": 0,
                "remote_cache_hit": 0,
                "disk_cache_hit": 0,
                "remote": 0,
                "local": 0,
                "other": 0,
                "total_ms": 0,
                "remote_exec_ms": 0,
                "remote_queue_ms": 0,
                "remote_queue_max_ms": 0,
                "remote_setup_ms": 0,
                "local_exec_ms": 0,
                "fetch_ms": 0,
                "upload_ms": 0,
                "input_bytes": 0,
            },
        )
        d["spawns"] += 1
        if cls in ("remote_cache_hit", "disk_cache_hit", "remote", "local"):
            d[cls] += 1
        else:
            d["other"] += 1
        d["total_ms"] += m.get("total_ms", 0)
        d["fetch_ms"] += m.get("fetch_ms", 0)
        d["upload_ms"] += m.get("upload_ms", 0)
        d["input_bytes"] += m.get("input_bytes", 0)
        if cls == "remote":
            d["remote_exec_ms"] += m.get("exec_ms", 0)
            d["remote_queue_ms"] += m.get("queue_ms", 0)
            d["remote_queue_max_ms"] = max(d["remote_queue_max_ms"], m.get("queue_ms", 0))
            d["remote_setup_ms"] += m.get("setup_ms", 0)
        elif cls == "local":
            d["local_exec_ms"] += m.get("exec_ms", 0)

        if cls in ("remote", "local"):  # cache misses
            target = valid_label(spawn["target"])
            if target is not None:
                key = (mnemonic, target, cls)
                cd = cold.setdefault(
                    key, {"mnemonic": mnemonic, "target": target, "runner": cls, "spawns": 0, "total_ms": 0, "exec_ms": 0, "queue_ms": 0}
                )
                cd["spawns"] += 1
                cd["total_ms"] += m.get("total_ms", 0)
                cd["exec_ms"] += m.get("exec_ms", 0)
                cd["queue_ms"] += m.get("queue_ms", 0)
    return by_mnemonic, cold, matched_invocation


def finalize_mnemonics(by_mnemonic, action_data):
    truncated = 0
    names = sorted(by_mnemonic.keys())
    if len(names) > MAX_MNEMONICS:
        truncated = len(names) - MAX_MNEMONICS
        names = sorted(by_mnemonic.keys(), key=lambda n: by_mnemonic[n]["total_ms"], reverse=True)[:MAX_MNEMONICS]
        names.sort()
    out = []
    for name in names:
        d = dict(by_mnemonic[name])
        d["mnemonic"] = name
        extra = action_data.get(name)
        if extra:
            d.update(extra)
        else:
            d.setdefault("bep_executed", 0)
            d.setdefault("bep_created", 0)
            d.setdefault("user_ms", 0)
            d.setdefault("system_ms", 0)
        out.append(d)
    return out, truncated


def finalize_cold(cold):
    rows = list(cold.values())
    rows.sort(key=lambda r: r["total_ms"], reverse=True)
    truncated = max(0, len(rows) - MAX_COLD)
    return rows[:MAX_COLD], truncated


def build_invocation(step, bep_path, exec_log_path, profile_path, deadline):
    try:
        events, bep_truncated = read_bep(bep_path, deadline)
    except OSError:
        return None
    except ValueError:
        # A malformed non-final line: drop this invocation entirely.
        return None
    except ParseTimeout:
        return None

    started, finished, timing, critical_path, actions, graph, net = build_timing(events)
    if started is None:
        return None

    # "-" (or omitting the argument) means "missing": there was never a file
    # to read. That's different from "unreadable" (a path was given but
    # couldn't be opened/decoded) or "corrupt"/"timeout" (opened, but the
    # contents didn't decode, or took too long).
    exec_log_status = "missing"
    exec_log_data = None
    if exec_log_path is not None:
        exec_log_data, exec_log_status = load_exec_log(exec_log_path, deadline)

    mnemonics_raw, cold_raw = {}, {}
    if exec_log_data is not None:
        try:
            mnemonics_raw, cold_raw, matched = build_mnemonics_and_cold(
                started.get("invocation_id", ""), exec_log_data, deadline
            )
        except ParseTimeout:
            exec_log_status = "timeout"
        except Exception:
            # Every crash this layer can't itself degrade from (a truncated
            # varint, an unsupported wire type, a sub-message that's the
            # wrong shape in a way isinstance() checks upstream didn't
            # anticipate) lands here: the invocation still gets its BEP-only
            # data, just no per-mnemonic/cold rows.
            exec_log_status = "corrupt"
        else:
            if started.get("invocation_id") and not matched:
                exec_log_status = "mismatch"
                mnemonics_raw, cold_raw = {}, {}

    action_data = build_action_data(events)
    mnemonics, mnemonics_truncated = finalize_mnemonics(mnemonics_raw, action_data)
    cold, cold_truncated = finalize_cold(cold_raw)

    repo_fetch = []
    profile_status = "missing"
    if profile_path is not None:
        repo_fetch, profile_status = iter_repo_fetch(profile_path, deadline)

    tests, tests_truncated = build_tests(events)

    record = {
        "step": step,
        "invocation_id": started["invocation_id"],
        "command": started["command"],
        "bazel_version": started["bazel_version"],
        "started_ms": started["started_ms"],
        "finished_ms": finished["finished_ms"] if finished else 0,
        # No finished event at all (the lane crashed/was killed mid-build):
        # -1, never the misleadingly-successful-looking default of 0.
        "exit_code": finished["exit_code"] if finished else -1,
        "exit_name": valid_enum(finished["exit_name"]) if finished else None,
        "bep_truncated": bep_truncated,
        "exec_log": exec_log_status,
        "profile": profile_status,
        "timing": timing,
        "critical_path": critical_path,
        "actions": actions,
        "graph": graph,
        "net": net,
        "mnemonics": mnemonics,
        "mnemonics_truncated": mnemonics_truncated,
        "cold": cold,
        "cold_truncated": cold_truncated,
        "repo_fetch": repo_fetch,
        "tests": tests,
        "tests_truncated": tests_truncated,
    }
    if record["exit_name"] is None:
        record["exit_name"] = "UNKNOWN"
    return record


def build_summary(lane, mode, check_run_id, pr_hint, invocation_args, deadline):
    invocations = []
    for step, bep, exec_log, profile in invocation_args:
        record = build_invocation(step, bep, exec_log, profile, deadline)
        if record is not None:
            invocations.append(record)
    if not invocations:
        return None
    return {
        "format": FORMAT,
        "version": SCHEMA_VERSION,
        "extractor": EXTRACTOR_VERSION,
        "lane": lane,
        "mode": mode,
        "check_run_id": as_int(check_run_id),
        "pr_hint": as_int(pr_hint),
        "invocations": invocations,
    }


def parse_args(argv):
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--lane", required=True)
    ap.add_argument("--mode", required=True)
    ap.add_argument("--check-run-id", required=True)
    ap.add_argument("--pr-hint", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument(
        "--invocation",
        nargs="+",
        action="append",
        default=[],
        metavar=("STEP BEP EXEC_LOG PROFILE"),
    )
    args = ap.parse_args(argv)

    # REDACTION: --lane, --mode and every --invocation STEP are written
    # straight into the output JSON (lane, mode, invocations[].step). They
    # come from workflow env/matrix values, not free text, but nothing
    # enforced that before this CLI parsed them -- a hostile or just wrong
    # caller could put an auth-header-shaped string in --lane and have it
    # land in the artifact verbatim. Validate eagerly, before any file I/O.
    if valid_lane(args.lane) is None:
        ap.error("--lane must match %s" % LANE_RE.pattern)
    if args.mode not in MODE_CHOICES:
        ap.error("--mode must be one of: %s" % ", ".join(sorted(MODE_CHOICES)))

    invocations = []
    for inv in args.invocation:
        if len(inv) < 2 or len(inv) > 4:
            ap.error("--invocation takes STEP BEP [EXEC_LOG|-] [PROFILE|-]")
        step = inv[0]
        if valid_lane(step) is None:
            ap.error("--invocation STEP must match %s" % LANE_RE.pattern)
        bep = inv[1]
        exec_log = inv[2] if len(inv) > 2 and inv[2] != "-" else None
        profile = inv[3] if len(inv) > 3 and inv[3] != "-" else None
        invocations.append((step, bep, exec_log, profile))
    if len(invocations) > MAX_INVOCATIONS:
        ap.error("at most %d --invocation entries" % MAX_INVOCATIONS)
    if not invocations:
        ap.error("at least one --invocation is required")
    args.invocations = invocations
    return args


def main(argv=None):
    args = parse_args(argv if argv is not None else sys.argv[1:])
    deadline = time.monotonic() + PARSE_DEADLINE_S
    try:
        summary = build_summary(
            args.lane, args.mode, args.check_run_id, args.pr_hint, args.invocations, deadline
        )
    except Tripwire:
        sys.stderr.write("::error::ci_analytics_extract: tripwire matched; nothing written\n")
        return 4

    if summary is None:
        sys.stderr.write("::error::ci_analytics_extract: no BEP for any invocation; nothing written\n")
        return 3

    try:
        # allow_nan=False: a backstop against ever writing invalid JSON
        # (bare Infinity/-Infinity/NaN) should a non-finite number reach
        # this point despite as_int()/as_duration_ms()/_clamp_pct() -- every
        # numeric field this extractor emits is already supposed to be
        # finite by the time it gets here.
        text = json.dumps(summary, sort_keys=True, separators=(",", ":"), allow_nan=False)
    except ValueError:
        sys.stderr.write(
            "::error::ci_analytics_extract: a non-finite number reached serialization; nothing written\n"
        )
        return 4
    if TRIPWIRE_RE.search(text):
        sys.stderr.write("::error::ci_analytics_extract: tripwire matched serialized output; nothing written\n")
        return 4

    out_dir = os.path.dirname(args.out)
    if out_dir:
        os.makedirs(out_dir, exist_ok=True)
    tmp_path = args.out + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.replace(tmp_path, args.out)
    return 0


if __name__ == "__main__":
    sys.exit(main())
