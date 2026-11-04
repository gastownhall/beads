#!/usr/bin/env bash
# Regenerates real/*.json and real/*.binpb from a disposable scratch Bazel
# workspace. Run manually (not part of any test or CI job); the fixtures it
# produces are committed. This documents how they were made, so a reviewer
# can tell they are real Bazel 9 output and not hand-written JSON, and so
# they can be regenerated against a newer Bazel without re-deriving the
# scratch workspace from scratch.
#
# Isolation matters here as much as in bazel.yml itself, and more: the host
# that runs this script is a live operator workstation, not a disposable CI
# runner, so Bazel's own BEP `started` event and --client_env echo the whole
# ambient environment (hostname, user, SSH_CLIENT, auth tokens, gateway key
# paths, ...) straight into the fixture unless we scrub it. We do that in two
# layers:
#   1. Run Bazel under `env -i` with a minimal, scratch-local environment, so
#      there is nothing secret in the environment to begin with.
#   2. Pass every raw BEP file through filter_bep() below, which keeps only
#      the events and fields tools/bazel/ci_analytics_extract.py actually
#      reads, before anything is copied into $out. This is defense in depth:
#      even a future Bazel version that adds new ambient fields to `started`
#      or a new event kind gets dropped by default, not included by default.
#
# --nohome_rc --nosystem_rc keep the home .bazelrc (which may carry a real
# --remote_executor) out of the invocation, and --output_user_root puts the
# repo-contents cache outside the scratch workspace. No remote flags are
# passed at all: these fixtures are deliberately local-only (disk_cache_hit,
# not remote_cache_hit). A synthetic remote-runner fixture is built in
# ci_analytics_extract_test.py instead, from its own varint encoder, since
# this repo's sandbox has no RBE endpoint to run against.
set -euo pipefail

real_bazel=$(command -v bazel)

scratch=$(mktemp -d)
userroot=$(mktemp -d)
diskcache=$(mktemp -d)
home="$scratch/home"
mkdir -p "$home"
trap 'rm -rf "$scratch" "$userroot" "$diskcache"' EXIT

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)
cp "$repo_root/.bazelversion" "$scratch/.bazelversion"

cat >"$scratch/MODULE.bazel" <<'EOF'
module(name = "s2scratch", version = "0.0.0")
bazel_dep(name = "rules_cc", version = "0.0.17")
EOF

cat >"$scratch/BUILD.bazel" <<'EOF'
load("@rules_cc//cc:defs.bzl", "cc_test")

cc_test(
    name = "pass_test",
    srcs = ["main_pass.c"],
)

cc_test(
    name = "sharded_test",
    srcs = ["main_shard.c"],
    shard_count = 3,
)

cc_test(
    name = "flaky_test",
    srcs = ["main_fail.c"],
)

genrule(
    name = "gen_out",
    outs = ["gen_out.txt"],
    cmd = "echo hello > $@",
)
EOF

cat >"$scratch/main_pass.c" <<'EOF'
int main(void) { return 0; }
EOF

cat >"$scratch/main_fail.c" <<'EOF'
int main(void) { return 1; }
EOF

cat >"$scratch/main_shard.c" <<'EOF'
#include <stdio.h>
#include <stdlib.h>

int main(void) {
  const char *status_file = getenv("TEST_SHARD_STATUS_FILE");
  if (status_file != NULL) {
    FILE *f = fopen(status_file, "w");
    if (f != NULL) {
      fclose(f);
    }
  }
  return 0;
}
EOF

cd "$scratch"

# A fully scrubbed environment: no inherited secrets, SSH state, gateway
# addresses or tokens. PATH is just enough for a C toolchain and bazel's own
# exec needs; HOME is scratch-local so nothing in it (no ~/.bazelrc either)
# can leak in, and so Bazel's own "user"/"host" fields come from this
# sandboxed identity rather than the operator's.
run_bazel() {
  env -i \
    HOME="$home" \
    PATH=/usr/bin:/bin \
    USER=fixture \
    LOGNAME=fixture \
    TMPDIR=/tmp \
    "$real_bazel" --nohome_rc --nosystem_rc "--output_user_root=$userroot" "$@"
}

out="$1"  # destination dir, e.g. tools/bazel/testdata/ci_analytics/real
mkdir -p "$out"

# filter_bep keeps only the BEP events and fields
# tools/bazel/ci_analytics_extract.py reads (plus buildToolLogs' "name" and
# "contents", with "uri" stripped, since those are local filesystem paths and
# not needed to recompute anything the extractor reports). Everything else —
# unstructuredCommandLine, structuredCommandLine, optionsParsed,
# workspaceStatus, buildMetadata, progress, and the leaky parts of started —
# is dropped entirely rather than merely left unused, so the fixture itself
# carries nothing a reviewer reading the raw JSON could call a leak.
filter_bep() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

src, dst = sys.argv[1], sys.argv[2]

# Positive allowlists only, one per event kind: every key below is one
# ci_analytics_extract.py actually reads (cross-checked against
# build_timing/build_tests/build_action_data). Nothing else survives,
# including keys a future Bazel version might add -- a denylist (drop just
# the known-leaky keys) was tried first and missed testSummary.passed/
# failed[].uri (a file://<scratch-path> local path) entirely; an allowlist
# can't miss a key like that by construction, it would just never be kept.
STARTED_KEEP = {"uuid", "command", "buildToolVersion", "startTimeMillis"}
TESTRESULT_KEEP = {"status", "cachedLocally", "testAttemptDuration", "testAttemptDurationMillis"}
EXECINFO_KEEP = {"strategy", "timingBreakdown"}
TIMING_CHILD_KEEP = {"name", "time"}
TESTSUMMARY_KEEP = {"overallStatus", "shardCount", "attemptCount", "totalNumCached"}
FINISHED_KEEP = {"finishTimeMillis"}
EXITCODE_KEEP = {"code", "name"}
LOG_ENTRY_KEEP = {"name", "contents"}


def pick(d, keys):
    return {k: v for k, v in d.items() if k in keys} if isinstance(d, dict) else {}


def filter_test_result(tr):
    if not isinstance(tr, dict):
        return {}
    out = pick(tr, TESTRESULT_KEEP)
    ei = tr.get("executionInfo")
    if isinstance(ei, dict):
        filtered_ei = pick(ei, EXECINFO_KEEP)
        tbd = ei.get("timingBreakdown")
        if isinstance(tbd, dict) and isinstance(tbd.get("child"), list):
            filtered_ei["timingBreakdown"] = {
                "child": [pick(c, TIMING_CHILD_KEEP) for c in tbd["child"] if isinstance(c, dict)]
            }
        out["executionInfo"] = filtered_ei
    return out


def filter_build_metrics(bm):
    if not isinstance(bm, dict):
        return {}
    out = {}
    tm = bm.get("timingMetrics")
    if isinstance(tm, dict):
        out["timingMetrics"] = pick(
            tm,
            {
                "cpuTimeInMs",
                "wallTimeInMs",
                "analysisPhaseTimeInMs",
                "executionPhaseTimeInMs",
                "actionsExecutionStartInMs",
                "criticalPathTime",
                "criticalPathTimeInMs",
            },
        )
    asum = bm.get("actionSummary")
    if isinstance(asum, dict):
        filtered_asum = pick(asum, {"actionsCreated", "actionsExecuted"})
        if isinstance(asum.get("runnerCount"), list):
            filtered_asum["runnerCount"] = [
                pick(rc, {"name", "count", "execKind"}) for rc in asum["runnerCount"] if isinstance(rc, dict)
            ]
        acs = asum.get("actionCacheStatistics")
        if isinstance(acs, dict):
            filtered_asum["actionCacheStatistics"] = pick(acs, {"hits", "misses"})
        if isinstance(asum.get("actionData"), list):
            filtered_asum["actionData"] = [
                pick(ad, {"mnemonic", "actionsExecuted", "actionsCreated", "userTime", "systemTime"})
                for ad in asum["actionData"]
                if isinstance(ad, dict)
            ]
        out["actionSummary"] = filtered_asum
    pm = bm.get("packageMetrics")
    if isinstance(pm, dict):
        out["packageMetrics"] = pick(pm, {"packagesLoaded"})
    tgm = bm.get("targetMetrics")
    if isinstance(tgm, dict):
        out["targetMetrics"] = pick(tgm, {"targetsConfigured"})
    nmw = bm.get("networkMetrics")
    nm = nmw.get("systemNetworkStats") if isinstance(nmw, dict) else None
    if isinstance(nm, dict):
        out["networkMetrics"] = {"systemNetworkStats": pick(nm, {"bytesRecv", "bytesSent"})}
    return out


with open(src) as fin, open(dst, "w") as fout:
    for line in fin:
        line = line.strip()
        if not line:
            continue
        event = json.loads(line)
        kept = {}
        # "id" carries the BEP identifier for this event (e.g.
        # id.testResult.label / id.testSummary.label), which
        # ci_analytics_extract.py reads to group test rows by label. It is
        # not leaky on its own (a label, run/shard/attempt numbers), so it
        # is kept verbatim alongside whichever event body below is kept.
        if "id" in event:
            kept["id"] = event["id"]
        if "started" in event and isinstance(event["started"], dict):
            kept["started"] = pick(event["started"], STARTED_KEEP)
        elif "testResult" in event:
            kept["testResult"] = filter_test_result(event["testResult"])
        elif "testSummary" in event and isinstance(event["testSummary"], dict):
            # passed/failed (TestFile{uri, ...} lists) are never read by
            # the extractor at all: dropped entirely, not merely
            # uri-stripped, along with anything else not in the allowlist.
            kept["testSummary"] = pick(event["testSummary"], TESTSUMMARY_KEEP)
        elif "buildMetrics" in event:
            kept["buildMetrics"] = filter_build_metrics(event["buildMetrics"])
        elif "finished" in event and isinstance(event["finished"], dict):
            fin_ev = pick(event["finished"], FINISHED_KEEP)
            ec = event["finished"].get("exitCode")
            if isinstance(ec, dict):
                fin_ev["exitCode"] = pick(ec, EXITCODE_KEEP)
            kept["finished"] = fin_ev
        elif "buildToolLogs" in event:
            btl = event["buildToolLogs"]
            logs = btl.get("log") if isinstance(btl, dict) else None
            kept["buildToolLogs"] = {
                "log": [pick(entry, LOG_ENTRY_KEEP) for entry in (logs or []) if isinstance(entry, dict)]
            }
        else:
            kept.pop("id", None)
            continue
        fout.write(json.dumps(kept) + "\n")
PY
}

# trim_profile keeps only the "Fetching repository" category entries the
# extractor's iter_repo_fetch() reads, dropping the (much larger, and
# otherwise unredacted) rest of the Chrome trace. Written one event per
# line -- {"traceEvents":[\n{...},\n{...}\n]} -- matching the real,
# streamable shape Bazel itself writes a JSON --profile in (as opposed to
# json.dump()'s single line): iter_repo_fetch() reads the profile line by
# line specifically so it never has to hold the whole (potentially huge)
# trace in memory, and a single-line file defeats that path entirely
# (every "Fetching repository" entry, if there's more than one, ends up on
# the one line containing the literal "traceEvents" key instead, which
# doesn't have a "cat" of its own and is skipped -- repo_fetch would always
# be empty, and the test for it would pass vacuously).
trim_profile() {
  python3 - "$1" "$2" <<'PY'
import json
import sys

src, dst = sys.argv[1], sys.argv[2]
with open(src) as f:
    doc = json.load(f)
events = [
    e for e in doc.get("traceEvents", []) if e.get("cat") == "Fetching repository"
]
with open(dst, "w") as f:
    f.write('{"traceEvents":[\n')
    f.write(",\n".join(json.dumps(e) for e in events))
    f.write("\n]}\n")
PY
}

# Cold: no disk cache. pass_test and sharded_test pass; flaky_test fails
# (deliberately, so the fixture has a FAILED testResult too).
run_bazel test //... \
  --keep_going \
  --build_event_json_file="$scratch/cold-bep.raw.json" \
  --execution_log_compact_file="$scratch/cold-exec.log" \
  --profile="$scratch/cold-profile.raw.json" || true
filter_bep "$scratch/cold-bep.raw.json" "$out/cold-bep.json"
zstd -dc --long=0 "$scratch/cold-exec.log" >"$out/cold-exec.binpb"
trim_profile "$scratch/cold-profile.raw.json" "$out/cold-profile.json"

# Warm: first run with --disk_cache set populates it (its own actions are
# still local execs; the disk cache is empty going in). Not copied to $out
# on its own; it only sets up the disk cache for the post-clean run below.
run_bazel test //... \
  --keep_going \
  "--disk_cache=$diskcache" \
  --build_event_json_file="$scratch/warm1-bep.json" \
  --execution_log_compact_file="$scratch/warm1-exec.log" || true

# Post-clean, with the disk cache warm1 just populated: every action is a
# disk_cache_hit except flaky_test's TestRunner spawn (Bazel never caches a
# failed test's result, so it reruns and misses).
run_bazel clean
run_bazel test //... \
  --keep_going \
  "--disk_cache=$diskcache" \
  --build_event_json_file="$scratch/postclean-bep.raw.json" \
  --execution_log_compact_file="$scratch/postclean-exec.log" || true
filter_bep "$scratch/postclean-bep.raw.json" "$out/postclean-bep.json"
zstd -dc --long=0 "$scratch/postclean-exec.log" >"$out/postclean-exec.binpb"

echo "wrote fixtures to $out" >&2
