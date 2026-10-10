#!/usr/bin/env python3
"""Regenerate .github/scripts/embedded-cmd-test-shards.txt by cost-balancing.

Discovery must stay in step with embedded-test-shard.sh: both select every
top-level TestEmbedded* function from cmd/bd/*_embedded_test.go.

Two cost models, selected by --weights (default: inits, matching
gen_proxied_shard_manifest.py's convention — see that script's module
docstring for the full rationale this one shares):

  --weights=inits (default): a cheap static proxy for wall-time — the
    function's subtest count (t.Run( occurrences), floored at 1. Unlike the
    proxied generator's bd-init count, embedded cmd tests do not share one
    dominant per-call cost center, so subtest count is the simplest proxy
    that is still monotonic in "more scenarios this function exercises."

  --weights=duration: measured wall-time from
    scripts/ci/embedded_cmd_test_durations.json (see that file's header for
    provenance). A function missing from that file — a test added since it
    was last captured — falls back to its inits cost times the file's
    "seconds_per_init_fallback" ratio. A from-scratch pack models the shard
    process (pack_concurrent): functions without a top-level t.Parallel()
    run one after another, the rest share -test.parallel=4 slots.

This generator's --weights=duration path targets the Bazel-only shard total
(the bazel-embedded job in .github/workflows/bazel.yml). The legacy,
hand-assigned 20-shard block that PR Risk's and main.yml's retired fork/push
jobs used to read has been deleted (those jobs no longer exist; see
engdocs/TESTING.md): the manifest file now holds exactly the one live block.

--write is INCREMENTAL by default (see --repack to force a full rebalance);
--check verifies exact-once NAME coverage only, not shard assignments. Both
behave exactly like gen_proxied_shard_manifest.py's flags of the same name;
see that script's module docstring for the complete explanation (merge-
conflict avoidance, fork-safety, etc.) — it is not repeated here.

Usage: gen_embedded_cmd_shard_manifest.py [total_shards] [--weights=inits|duration]
                                           [--write [--repack] | --check] [--manifest PATH]
"""
import argparse
import glob
import json
import os
import re
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _embedded_shard_manifest_lib import (  # noqa: E402
    check_coverage, incremental_pack, pack, pack_concurrent, read_assignments, write_block,
)

default_manifest_path = '.github/scripts/embedded-cmd-test-shards.txt'
durations_filename = 'embedded_cmd_test_durations.json'
# The Bazel-only block's total (bd_embedded_test + bd_embedded_part2_test's
# shard_counts in cmd/bd/BUILD.bazel must sum to this).
default_total_shards = 100
# .bazelrc's test:embedded --test_arg=-test.parallel=4: how many t.Parallel
# top-level tests one shard's process runs at once (--weights=duration).
test_parallelism = 4

func_re = re.compile(r'^func (TestEmbedded[A-Za-z0-9_]+)\(')


def discover_inits_cost():
    """Return {test_name: subtest count}, floored at 1."""
    costs = {}
    for path in glob.glob('cmd/bd/*_embedded_test.go'):
        with open(path) as fh:
            lines = fh.readlines()
        cur = None
        for ln in lines:
            m = func_re.match(ln)
            if m:
                cur = m.group(1)
                costs.setdefault(cur, 0)
            elif ln.startswith('func '):
                cur = None
            if cur:
                costs[cur] += ln.count('t.Run(')
    for k in costs:
        costs[k] = max(costs[k], 1)
    return costs


def discover_serial():
    """Return the TestEmbedded* functions whose body does not call
    t.Parallel() directly: Go runs them one at a time, before the parallel
    ones (a test that reaches t.Parallel only through a helper counts as
    serial, which overestimates its shard)."""
    serial = set()
    for path in glob.glob('cmd/bd/*_embedded_test.go'):
        with open(path) as fh:
            lines = fh.readlines()
        cur, parallel = None, False
        for ln in lines + ['func \n']:
            if ln.startswith('func '):
                if cur and not parallel:
                    serial.add(cur)
                m = func_re.match(ln)
                cur, parallel = (m.group(1) if m else None), False
            elif cur and ln.rstrip('\n') == '\tt.Parallel()':
                parallel = True
    return serial


def duration_cost(inits):
    """Return {test_name: seconds}, from the committed duration capture,
    falling back to inits * seconds_per_init_fallback for an undated test."""
    durations_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), durations_filename)
    with open(durations_path) as fh:
        data = json.load(fh)
    measured = data['tests']
    fallback_ratio = data['seconds_per_init_fallback']
    stale = sorted(name for name in measured if name not in inits)
    if stale:
        sys.stderr.write(
            f'warning: {durations_filename} has {len(stale)} capture(s) for '
            'tests that no longer exist in cmd/bd/*_embedded_test.go (renamed, split or '
            f'deleted since the last capture): {", ".join(stale)}\n')
    costs = {}
    for name, cost in inits.items():
        costs[name] = measured[name] if name in measured else cost * fallback_ratio
        costs[name] = max(costs[name], 0.001)
    return costs


def render(total, shards, weights):
    out = []
    out.append('# Embedded-Dolt cmd/bd test shard manifest.')
    out.append('#')
    out.append('# Format: <total_shards> <shard_number> <top_level_test_name>')
    out.append('#')
    out.append(f'# {total}-shard split for the Bazel-only embedded-Dolt cmd tier')
    out.append('# (bazel-embedded in .github/workflows/bazel.yml), bin-packed')
    out.append('# longest-first by measured wall-time from')
    out.append(f'# scripts/ci/{durations_filename} (see that file for provenance')
    out.append('# and its "relative weight, not absolute SLA" caveat) onto the shard')
    out.append('# whose estimated wall time grows least: serial tests (no top-level')
    out.append(f'# t.Parallel) add up, parallel ones share -test.parallel={test_parallelism}.')
    out.append(f'# Regenerate with scripts/ci/gen_embedded_cmd_shard_manifest.py {total}')
    out.append('# --weights=duration --write after adding, splitting or removing')
    out.append('# TestEmbedded* functions in cmd/bd/*_embedded_test.go. --write is')
    out.append('# incremental: it keeps every already-assigned test on its current shard')
    out.append('# and only places newly-discovered names, onto whichever shard is')
    out.append('# currently lightest, so two unrelated PRs that each add one test do not')
    out.append('# textually conflict or silently invalidate each other\'s packing. Pass')
    out.append('# --repack to force a full from-scratch rebalance instead (a deliberate,')
    out.append('# separate change — expect a large diff). --check only verifies every')
    out.append('# discovered name is listed here exactly once (no stale or duplicate')
    out.append('# entries); it does NOT verify the packing is still well-balanced.')
    out.append('#')
    out.append('# This file is generated: notes added here are erased by the next')
    out.append('# regeneration. Add them to the generator or the durations file instead.')
    out.append('')
    for i in range(total):
        for name in sorted(shards[i]):
            out.append(f'{total} {i + 1} {name}')
        out.append('')
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__.split('\n\n')[0])
    ap.add_argument('total_shards', nargs='?', type=int, default=None)
    ap.add_argument('--weights', choices=['inits', 'duration'], default='inits')
    ap.add_argument('--manifest', default=default_manifest_path,
                     help=f'manifest file to read/write (default: {default_manifest_path})')
    mode = ap.add_mutually_exclusive_group()
    mode.add_argument('--write', action='store_true',
                       help='rewrite only this total_shards block in --manifest in place '
                            '(incremental by default; see --repack)')
    mode.add_argument('--check', action='store_true',
                       help="exit non-zero unless --manifest's block for this total_shards has "
                            'exact-once coverage of the discovered test set')
    ap.add_argument('--repack', action='store_true',
                     help='ignore any existing block for this total_shards and do a full '
                          'from-scratch LPT pack')
    args = ap.parse_args()
    if args.total_shards is None:
        args.total_shards = default_total_shards

    inits = discover_inits_cost()
    costs = inits if args.weights == 'inits' else duration_cost(inits)

    existing = None if args.repack else read_assignments(args.manifest, args.total_shards)
    if existing:
        shards, loads = incremental_pack(costs, args.total_shards, existing)
    elif args.weights == 'duration':
        shards, loads = pack_concurrent(costs, args.total_shards, discover_serial(), test_parallelism)
    else:
        shards, loads = pack(costs, args.total_shards)
    out = render(args.total_shards, shards, args.weights)

    unit = 'inits' if args.weights == 'inits' else 's'
    fmt = (lambda v: str(v)) if args.weights == 'inits' else (lambda v: f'{v:.1f}')
    mode_label = 'repack' if (args.repack or not existing) else 'incremental'
    sys.stderr.write(f'shard loads ({mode_label}, est. {unit}, weights={args.weights}): ' +
                      ', '.join(f'{i + 1}:{fmt(loads[i])}' for i in range(args.total_shards)) + '\n')
    sys.stderr.write(f'heaviest shard: {fmt(max(loads))} {unit}\n')

    if args.check:
        err = check_coverage(args.manifest, args.total_shards, costs.keys(),
                              'TestEmbedded*', sys.argv[0])
        if err:
            sys.stderr.write('error: ' + err + '\n')
            sys.exit(1)
        return
    if args.write:
        write_block(args.manifest, args.total_shards, out)
        sys.stderr.write(f'wrote {args.manifest}\'s {args.total_shards}-shard block '
                          f'({mode_label})\n')
        return
    print('\n'.join(out).rstrip() + '\n', end='')


if __name__ == '__main__':
    main()
