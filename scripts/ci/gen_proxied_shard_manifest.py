#!/usr/bin/env python3
"""Regenerate .github/scripts/proxied-cmd-test-shards.txt by cost-balancing.

Cost proxy for each top-level TestProxiedServer* function is its bd-init count
(newSharedProxiedProject occurrences) — inits dominate proxied wall-time. Tests
are packed into TOTAL shards longest-processing-time-first so the heaviest shard
is minimized. Prints the manifest to stdout.

Usage: gen_proxied_shard_manifest.py [total_shards]
"""
import glob
import re
import sys

TOTAL = int(sys.argv[1]) if len(sys.argv) > 1 else 15
func_re = re.compile(r'^func (TestProxiedServer[A-Za-z0-9_]+)\(')

costs = {}
for path in glob.glob('cmd/bd/*_test.go'):
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
            costs[cur] += ln.count('newSharedProxiedProject')

# Every discovered test costs at least 1 (setup/teardown) even with no inits.
for k in costs:
    costs[k] = max(costs[k], 1)

# LPT bin-packing: heaviest first onto the currently-lightest shard.
order = sorted(costs, key=lambda k: (-costs[k], k))
shards = [[] for _ in range(TOTAL)]
loads = [0] * TOTAL
for name in order:
    i = loads.index(min(loads))
    shards[i].append(name)
    loads[i] += costs[name]

out = []
out.append('# Proxied-server cmd test shard manifest.')
out.append('#')
out.append('# Format: <total_shards> <shard_number> <top_level_test_name>')
out.append('#')
out.append(f'# {TOTAL}-shard split, bin-packed longest-processing-time-first by')
out.append('# estimated cost (bd-init count; per-init migration chains dominate).')
out.append('# Regenerate with scripts/ci/gen_proxied_shard_manifest.py after adding,')
out.append('# splitting, or reweighting TestProxiedServer* functions. Newly-added')
out.append('# tests not listed here hash-distribute via proxied-test-shard.sh.')
out.append('')
for i in range(TOTAL):
    for name in sorted(shards[i]):
        out.append(f'{TOTAL} {i + 1} {name}')
    out.append('')

sys.stderr.write('shard loads (est. inits): ' +
                 ', '.join(f'{i + 1}:{loads[i]}' for i in range(TOTAL)) + '\n')
sys.stderr.write(f'heaviest shard: {max(loads)} inits\n')
print('\n'.join(out).rstrip() + '\n', end='')
