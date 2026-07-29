---
description: Import issues and memories from JSONL
argument-hint: "[file|-] [--input file] [--dry-run] [--dedup] [--allow-stale]"
---

`bd import` imports newline-delimited JSON into an existing Beads database.
Issues with new IDs are created and existing IDs use guarded upsert semantics.
The command is not supported in proxied-server mode.

## Usage

```bash
bd import                         # Read .beads/<import.path>
bd import backup.jsonl            # Read a specific file
bd import --input backup.jsonl    # Same as -i backup.jsonl
bd import -                       # Read JSONL from stdin
bd export --include-memories | bd import - # Round-trip issues and memories
bd import --dry-run backup.jsonl  # Parse and report without writing
bd import --json backup.jsonl     # Return a structured result
```

With no file argument, the source is the `import.path` file under `.beads/`;
`import.path` defaults to `issues.jsonl`. Use either a positional file or
`--input`/`-i`, not both. The special path `-` reads stdin. With `--global` and
no file, the source is `global-issues.jsonl` in the active Beads directory.

Each non-empty line must be a JSON object. Issue records accept the schema
emitted by `bd export`; `title` is the only required issue field. Records with
`"_type":"memory"` and non-empty `key` and `value` fields are imported as
persistent memories. Optional export header records are ignored, and tombstone
rows are skipped. `bd export` omits memories by default; add
`--include-memories` or `--all` when they should be carried into an import.

## Merge and Safety Semantics

For records that supply `updated_at`, an incoming row rewrites an existing
issue only when its timestamp is strictly newer. Older records are skipped
wholesale, including their labels, comments, and dependencies. When timestamps
are equal, the local issue row wins, while those auxiliary records still merge.
The stale check is repeated inside the upsert so a concurrent newer local update
is not overwritten.

If `updated_at` is omitted, the importer fills it with the current time during
the write. A record naming an existing ID can therefore overwrite local state;
include the exported timestamp when stale protection matters.

Use `--allow-stale` only when deliberately restoring an older snapshot: it
disables that guard and can overwrite newer local issue state. Imports are
upserts, and labels, comments, and dependencies are deduplicated. Re-running
canonical export data or other input with stable issue IDs and preserved
`updated_at` values is safe and converges. Without a preserved timestamp, each
run can appear newer and rewrite the row. An issue record without an ID is
assigned one; re-importing it can create another issue instead of targeting the
first.

Large issue imports use bounded SQL transaction chunks. If one fails, the
command exits nonzero and keeps the already committed prefix in the Dolt working
set; no final import-summary Dolt commit is created. Re-run stable-ID input to
resume only when its `updated_at` values are also preserved.

After a successful non-dry import with accepted issue or memory records, the
command calls `store.Commit` with an import summary regardless of the
`dolt.auto-commit` policy. That commit can include unrelated pending working-set
changes; in direct server mode, config (including memories) is excluded from
this commit. When commit isolation matters, run `bd doctor` before importing and
proceed only when its Dolt Status check reports a clean working set.

The database must already exist; initialize or bootstrap it before importing.
Use `--dry-run` to parse the input, apply optional title deduplication, and
report issue and memory counts without writing. It does not run write-time field
validation or predict stale skips and updates.

## Flags

- `--input`, `-i <file>`: read a specific file instead of a positional path.
- `--dry-run`: report parsed record counts without writing.
- `--dedup`: skip an input issue whose title exactly matches, ignoring case, an
  existing non-closed issue. Input rows are not compared with one another; if
  the existing-issue search fails, title filtering is skipped.
- `--allow-stale`: import older rows too, allowing newer local state to be
  overwritten.
- `--json`: use global JSON-output mode for structured counts and IDs. The
  `created` count is accepted issue rows, including existing rows, not only new
  inserts.
