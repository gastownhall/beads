#!/usr/bin/env python3
"""Critical-path report and CI budget check for a `bazel --profile` JSON.

    gap = elapsed - critical_path

The critical path is the SUM of the chained "critical path component" events
Bazel records, not the single longest one: a cold race build is a long chain
of compile/link actions, and reporting only its longest member understates the
path and turns the difference into a phantom "scheduling gap".

Decision rule:
  gap < 15% of elapsed   the chain itself is the bottleneck. Attack its
                         longest member (shard/speed up a test, split or slim
                         a build action, warm the CAS if input upload
                         dominates).
  gap >= 15% of elapsed  actions waited for capacity: raise --jobs, grow the
                         executor pool, restore the runner disk cache.

Measurement tiers (caching makes single numbers lie):
  T1  steady: warm remote cache + restored runner disk cache (the PR loop).
  T2  cold client: fresh runner disk cache, warm remote cache.
  T3  cold farm: after worker recycle / CAS eviction. Tracked, never gated.
  local  no remote execution (fork PRs). Reported, never gated.

Usage:
  critpath.py PROFILE [--tier T2] [--budget SECONDS] [--enforce]
                      [--log BAZEL_OUTPUT_LOG] [--top N] [--summary FILE]

Without --enforce an over-budget run prints a GitHub warning annotation and
exits 0 (report-only phase); with --enforce it exits 1.
"""
import argparse
import json
import os
import re
import sys

CP_CAT = "critical path component"
DECISION_GAP_PCT = 15.0


def load_events(path):
    with open(path, encoding="utf-8") as f:
        prof = json.load(f)
    if isinstance(prof, list):
        return prof
    return prof.get("traceEvents", [])


def short_name(name, width=90):
    # "action 'GoLink cmd/bd/bd_test_/bd_test'" -> "GoLink cmd/bd/bd_test_/bd_test"
    m = re.match(r"action '(.*)'$", name)
    if m:
        name = m.group(1)
    return name if len(name) <= width else name[: width - 1] + "…"


def processes_line(log_path):
    """Return Bazel's final "N processes: ..." line from its console log."""
    if not log_path or not os.path.exists(log_path):
        return None
    found = None
    with open(log_path, encoding="utf-8", errors="replace") as f:
        for line in f:
            m = re.search(r"(\d[\d,]* processes?: .*)$", line.strip())
            if m:
                found = m.group(1)
    return found


def analyze(events):
    elapsed = max((e.get("dur", 0) for e in events if e.get("name") == "buildTargets"), default=0) / 1e6
    stamps = [e["ts"] for e in events if e.get("ts")]
    wall = (max(stamps) - min(stamps)) / 1e6 if stamps else 0.0
    chain = [e for e in events if e.get("cat") == CP_CAT]
    cp = sum(e.get("dur", 0) for e in chain) / 1e6
    longest = sorted(chain, key=lambda e: e.get("dur", 0), reverse=True)
    return {
        "elapsed": elapsed,
        "wall": wall,
        "cp": cp,
        "chain_len": len(chain),
        "longest": longest,
    }


def next_step(res):
    elapsed, cp = res["elapsed"], res["cp"]
    gap = max(elapsed - cp, 0.0)
    pct = gap / elapsed * 100 if elapsed else 0.0
    if not res["longest"]:
        return pct, "NEXT: no critical path recorded (nothing executed, or a fully cached run)"
    if pct < DECISION_GAP_PCT:
        top = res["longest"][0]
        name = short_name(top["name"], 200)
        secs = top.get("dur", 0) / 1e6
        m = re.search(r"Testing (\S+)", name)
        if m:
            return pct, f"NEXT: shard or speed up {m.group(1)} ({secs:.0f}s)"
        if "upload" in name.lower():
            return pct, "NEXT: input upload dominates; warm the remote CAS or slim the declared inputs"
        return pct, f"NEXT: attack {short_name(name, 70)} ({secs:.0f}s)"
    return pct, "NEXT: scheduling gap; raise --jobs, grow the executor pool, or restore the runner disk cache"


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("profile")
    ap.add_argument("--tier", default="T2")
    ap.add_argument("--budget", type=float, default=None, help="seconds; compared with max(elapsed, wall)")
    ap.add_argument("--enforce", action="store_true", help="exit 1 when over budget")
    ap.add_argument("--log", help="bazel console log, for the 'processes:' cache line")
    ap.add_argument("--top", type=int, default=8, help="longest chain members to list")
    ap.add_argument("--summary", default=None, help="append Markdown here (default: $GITHUB_STEP_SUMMARY)")
    args = ap.parse_args(argv)

    if not os.path.exists(args.profile):
        print(f"critpath: profile {args.profile} not found (bazel did not start?)", file=sys.stderr)
        return 1 if args.enforce else 0

    res = analyze(load_events(args.profile))
    pct, advice = next_step(res)
    gap = max(res["elapsed"] - res["cp"], 0.0)
    procs = processes_line(args.log)
    tier = args.tier

    lines = [
        f"[{tier}] wall clock     {res['wall']:7.1f}s   (server start to last event)",
        f"[{tier}] elapsed        {res['elapsed']:7.1f}s   (build graph)",
        f"[{tier}] critical path  {res['cp']:7.1f}s   (sum of {res['chain_len']} chained components)",
        f"[{tier}] gap            {gap:7.1f}s   ({pct:.0f}% of elapsed)",
    ]
    if procs:
        lines.append(f"[{tier}] processes      {procs}")
    top = [e for e in res["longest"][: args.top] if e.get("dur", 0) > 0]
    if top:
        lines.append("")
        lines.append(f"longest critical-path members (top {len(top)}):")
        for e in top:
            lines.append(f"  {e['dur'] / 1e6:7.1f}s  {short_name(e['name'])}")
    lines.append("")
    lines.append(advice)

    verdict = None
    over = False
    if args.budget is not None:
        gated = max(res["elapsed"], res["wall"])
        over = gated > args.budget
        mode = "enforcing" if args.enforce else "report-only"
        if over:
            verdict = f"BUDGET ({mode}): over, {gated:.1f}s > {args.budget:.0f}s ({tier})"
        else:
            verdict = f"BUDGET ({mode}): within, {gated:.1f}s <= {args.budget:.0f}s ({tier})"
        lines.append("")
        lines.append(verdict)

    print("\n".join(lines))
    if over and not args.enforce and os.environ.get("GITHUB_ACTIONS") == "true":
        print(f"::warning title=Bazel critical-path budget::{verdict}")

    summary = args.summary or os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as f:
            f.write(
                f"**Tier {tier}**: elapsed {res['elapsed']:.0f}s, wall {res['wall']:.0f}s, "
                f"critical path {res['cp']:.0f}s ({res['chain_len']} components), gap {pct:.0f}%"
            )
            f.write(f"; {verdict}\n\n" if verdict else "\n\n")
            f.write("<details><summary>Critical path</summary>\n\n```\n" + "\n".join(lines) + "\n```\n</details>\n\n")

    return 1 if (over and args.enforce) else 0


if __name__ == "__main__":
    sys.exit(main())
