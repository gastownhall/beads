"""Shared bin-packing/manifest-file machinery for the embedded-Dolt tier's
Bazel-only shard generators (gen_embedded_cmd_shard_manifest.py,
gen_embedded_storage_shard_manifest.py).

This is a near-verbatim extraction of scripts/ci/gen_proxied_shard_manifest.py
(F2)'s LPT packing, incremental --write, and multi-block manifest-file
machinery, generalized so both embedded generators (and any future one) share
one implementation instead of two independently-maintained copies. The
per-suite scripts own discovery (the test-name regex/glob), the duration-file
path and its "seconds_per_init_fallback" semantics, and the manifest header
text; this module owns everything suite-agnostic: pack(), incremental_pack(),
split_blocks()/write_block(), and check_coverage().

See gen_proxied_shard_manifest.py's module docstring for the full rationale
behind incremental --write (merge-conflict avoidance across unrelated PRs)
and --check's exact-once-coverage-only contract (fork-safe: it does not fail
when a fresh repack would reassign shards differently).
"""
import os
import re


def pack(costs, total):
    """LPT bin-pack costs into `total` shards. Returns (shards, loads)."""
    order = sorted(costs, key=lambda k: (-costs[k], k))
    shards = [[] for _ in range(total)]
    loads = [0.0] * total
    for name in order:
        i = loads.index(min(loads))
        shards[i].append(name)
        loads[i] += costs[name]
    return shards, loads


def split_blocks(text):
    """Split a manifest file's text into its per-total_shards blocks.

    A block is a run of leading '#' comment (and blank spacer) lines,
    immediately followed by a run of lines that do not start with '#' (shard
    assignment lines and the blank lines render() puts between shards) that
    ends at the next '#' line or end of file. Returns a list of dicts with
    'header' and 'body' line lists and the block's 'total' (parsed from its
    first assignment line; None for a block with no assignment lines yet).
    """
    lines = text.split('\n')
    blocks = []
    i, n = 0, len(lines)
    while i < n:
        header = []
        while i < n and lines[i].startswith('#'):
            header.append(lines[i])
            i += 1
        while i < n and lines[i] == '' and not (i + 1 < n and lines[i + 1].startswith('#')):
            header.append(lines[i])
            i += 1
        body = []
        while i < n and not lines[i].startswith('#'):
            body.append(lines[i])
            i += 1
        if not header and not body:
            continue
        total = None
        for bl in body:
            m = re.match(r'^(\d+) ', bl)
            if m:
                total = int(m.group(1))
                break
        blocks.append({'header': header, 'body': body, 'total': total})
    return blocks


def read_assignments(path, total):
    """Return {name: shard_number} for path's block matching total_shards ==
    total, or None if path or that block does not exist yet."""
    if not os.path.exists(path):
        return None
    for b in split_blocks(open(path).read()):
        if b['total'] == total:
            assignments = {}
            for ln in b['body']:
                m = re.match(r'^(\d+) (\d+) (\S+)$', ln)
                if m and int(m.group(1)) == total:
                    assignments[m.group(3)] = int(m.group(2))
            return assignments
    return None


def incremental_pack(costs, total, existing):
    """Keep every name in `existing` that is still in `costs` on its current
    shard; drop names no longer in `costs` (renamed/split/deleted); LPT-place
    every name in `costs` not already in `existing` onto whichever shard is
    currently lightest. Never moves an already-assigned test. Returns
    (shards, loads), like pack()."""
    shards = [[] for _ in range(total)]
    loads = [0.0] * total
    for name, shard_num in existing.items():
        if name not in costs or not (1 <= shard_num <= total):
            continue  # stale, or out of range for a changed total: re-place below
        shards[shard_num - 1].append(name)
        loads[shard_num - 1] += costs[name]
    placed = {n for s in shards for n in s}
    new_names = sorted((n for n in costs if n not in placed), key=lambda k: (-costs[k], k))
    for name in new_names:
        i = loads.index(min(loads))
        shards[i].append(name)
        loads[i] += costs[name]
    return shards, loads


def write_block(path, total, out):
    """Replace path's block for total_shards == total with out (a render()
    list), preserving every other block and its position. Appends out as a
    new trailing block if path has none for this total yet."""
    text = open(path).read() if os.path.exists(path) else ''
    blocks = split_blocks(text) if text else []
    result = []
    replaced = False
    for b in blocks:
        if b['total'] == total:
            result.extend(out)
            replaced = True
        else:
            result.extend(b['header'])
            result.extend(b['body'])
    if not replaced:
        if result and result[-1] != '':
            result.append('')
        result.extend(out)
    with open(path, 'w') as fh:
        fh.write('\n'.join(result).rstrip('\n') + '\n')


def check_coverage(path, total, universe, test_name_desc, script_name):
    """Return None if path's committed block for total_shards == total lists
    every name in `universe` (the currently-discovered test set) exactly
    once, with no stale or duplicate names; otherwise an actionable error
    string. Does NOT check the block's shard *assignments* — see this
    module's docstring for why (incremental --write)."""
    if not os.path.exists(path):
        return f'{path} does not exist'
    for b in split_blocks(open(path).read()):
        if b['total'] == total:
            names = []
            for ln in b['body']:
                m = re.match(r'^(\d+) (\d+) (\S+)$', ln)
                if m and int(m.group(1)) == total:
                    names.append(m.group(3))
            universe_set = set(universe)
            counts = {}
            for n in names:
                counts[n] = counts.get(n, 0) + 1
            dupes = sorted(n for n, c in counts.items() if c > 1)
            stale = sorted(n for n in counts if n not in universe_set)
            missing = sorted(n for n in universe_set if n not in counts)
            if not dupes and not stale and not missing:
                return None
            weights_guess = 'duration' if any('duration' in h for h in b['header']) else 'inits'
            msg = [f"{path}'s {total}-shard block does not have exact-once "
                   f'coverage of the currently-discovered {test_name_desc} test set.']
            if missing:
                msg.append(f'missing ({len(missing)}): {", ".join(missing)}')
            if stale:
                msg.append(f'stale/unknown ({len(stale)}): {", ".join(stale)}')
            if dupes:
                msg.append(f'duplicated ({len(dupes)}): {", ".join(dupes)}')
            msg.append(f'Run: python3 {script_name} {total} --weights={weights_guess} --write')
            return '\n'.join(msg)
    return f'{path} has no block for total_shards={total}'
