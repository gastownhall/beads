---
title: Sync Concepts
description: Why Dolt is the source of truth for sync and how the JSONL export differs from bd dolt push and pull
---

Beads issue data lives in Dolt. The local Dolt database is the source of truth
for `bd list`, `bd show`, `bd ready`, and every write command.

## The Wire Format

Cross-machine sync uses Dolt remotes:

```bash
bd dolt push
bd dolt pull
```

For normal git-hosted projects, the Dolt remote can be the same `origin` URL
used for source code. Dolt stores issue history under `refs/dolt/data`, separate
from source branches such as `refs/heads/main`.

On new projects, `bd init` auto-detects `git remote get-url origin` and
configures a Dolt remote named `origin`. The first `bd dolt push` publishes
`refs/dolt/data`. Fresh clones should run `bd bootstrap` to clone that Dolt
history. When bootstrap finds `refs/dolt/data` on git origin, it also wires
that origin as the Dolt remote for future `bd dolt push` and `bd dolt pull`.

## What JSONL Is For

`.beads/issues.jsonl` is an export. It exists for viewers, interchange,
migration, and backup. It is not the canonical cross-machine sync channel.

Do not use routine `bd import .beads/issues.jsonl` as a replacement for
`bd dolt pull`. JSONL import is upsert-only; it cannot infer that records absent
from an export were deleted, pruned, or simply never exported.

### Importing an export in parts

For each source row it writes, one `bd import` resolves a dependency edge
against targets already in the database or anywhere in the same input,
whatever order its lines come in. An edge whose target is in neither is
skipped, and so is an edge refused by validation (a cycle or an invalid
hierarchy). Text output prints
`Skipped dependency: <from> -> <to>: <reason>` on stderr for each one; under
`--json` the edges are listed in `skipped_dependencies` instead. Either way the import exits 0.

So an export restored in several parts skips each edge whose target only a
later part carries. Prefer one combined import when it fits. Otherwise import
every part, correct any edge validation refused, then import again each part
that reported skipped edges: its rows upsert and each valid edge is added
once. A re-run adds an edge only when it writes the edge's source row. A row
skipped as stale (listed in `stale_skipped_ids`, because the local issue
changed after the export) keeps the local state, and its edges are neither
added nor reported; the same holds for a row `--dedup` skips as a duplicate
title. Do not pass `--allow-stale` to force stale rows in: it overwrites the
newer local rows.

Verify a restore by comparing the dependency edges the database holds against
the export, not by row counts alone: a split restore can land every issue,
comment and label and still miss edges. A script can gate each part on the
JSON result; `(.data // .)` reads it whether or not `BD_JSON_ENVELOPE=1` nests
it under `data` (see the [JSON envelope](/reference/json-schema)):

```bash
set -o pipefail
bd import --json part-2.jsonl | jq -e '(.data // .) | (.skipped_dependencies // []) | length == 0'
```

This checks skipped dependency edges only, not rows skipped for other reasons
(`stale_skipped_ids`, `dedup_skipped`), and is not an audit of the whole graph.

## Hooks

The pre-commit hook refreshes `.beads/issues.jsonl` when `export.auto=true`.
That keeps the export current for tools, but it does not push Dolt history.

The post-merge and post-checkout hooks skip JSONL import when `sync.remote` is
configured. For old projects with no Dolt remote, they may import JSONL as a
compatibility fallback and print a warning that this is not durable sync.

## Repair

For projects initialized before automatic git-origin remote wiring, pick the
machine with the authoritative local Dolt database first. Then run:

```bash
bd dolt remote list
bd export -o .beads/issues.pre-remote.jsonl   # optional issue audit export
bd dolt remote add origin <git-origin-url>
bd dolt push
```

Use the Dolt-compatible git URL form when needed. For example,
`git+ssh://git@github.com/org/repo.git` or
`git+https://github.com/org/repo.git`. `bd dolt remote add origin ...`
persists `sync.remote` into `.beads/config.yaml`; commit and push that config
change so fresh clones can run `bd bootstrap`.

Other machines should then run:

```bash
bd dolt pull
# or, if the local database is stale or missing:
bd bootstrap
```
