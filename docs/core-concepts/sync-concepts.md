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
