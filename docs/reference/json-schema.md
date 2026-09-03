---
title: JSON Output Schema Contract
description: The stable JSON output contract for bd --json commands, covering the schema_version envelope, per-command fields, and consumer guidelines.
---

Last reviewed: 2026-08-07

Freshness source: `cmd/bd/output.go`, `cmd/bd/errors.go`, and
`cmd/bd/protocol/json_contract_test.go`.

All `bd` commands that support `--json` output can wrap their response in
a uniform envelope by setting `BD_JSON_ENVELOPE=1`. This will become the
default format in v2.0.

## Migration Guide

### Opt in to the envelope format

```bash
export BD_JSON_ENVELOPE=1
```

### Envelope format (BD_JSON_ENVELOPE=1, default in v2.0)

Every `--json` command wraps output as:

```json
{"schema_version": 1, "data": <original-payload>}
```

The original payload is untouched inside `.data` — no type corruption,
no field injection. Works identically for objects, arrays, and maps.

When a `--limit`-truncated listing runs in envelope mode (currently wired
for `bd ready`), the envelope also carries a `pagination` key:

```json
{"schema_version": 1, "data": [...], "pagination": {"returned": 10, "total": 42, "truncated": true}}
```

`total` is omitted when unknown; the whole `pagination` key is absent when
the result was not truncated. Legacy mode keeps the stderr text hint instead.

### Updating consumers

```bash
# Before (legacy):
bd list --json | jq '.[0].id'
bd show beads-abc --json | jq '.[0].title'

# After (envelope):
bd list --json | jq '.data[0].id'
bd show beads-abc --json | jq '.data[0].title'

# Version check (object commands, e.g. create):
bd create "Example" --json | jq '.schema_version'
```

### Timeline

- **Current release**: Legacy format is default. Set `BD_JSON_ENVELOPE=1` to opt in.
  A deprecation notice is printed to stderr when `--json` is used without the env
  var — but only when stderr is a terminal, and at most once per invocation, so
  scripts capturing stderr will not see it.
- **v2.0**: Envelope becomes the default. `BD_JSON_ENVELOPE=0` available as
  temporary escape hatch for one release cycle.

## Schema Version

Current version: **1**

The `schema_version` field is an integer that increments when:
- Fields are added, renamed, or removed
- Output structure changes (e.g., nesting depth)
- Field types change (e.g., string to integer)

Additive changes (new optional fields) do NOT bump the version.

## Output Formats

### Envelope mode (BD_JSON_ENVELOPE=1)

All commands emit a uniform envelope:

```json
{
  "schema_version": 1,
  "data": {
    "id": "beads-abc",
    "title": "Example issue",
    "status": "open"
  }
}
```

Arrays are wrapped the same way:

```json
{
  "schema_version": 1,
  "data": [
    {"id": "beads-abc", "title": "First"},
    {"id": "beads-def", "title": "Second"}
  ]
}
```

### Legacy mode (default, until v2.0)

### Object commands (create, ping, etc.)

Commands that return a single result emit a JSON object with
`schema_version` as a top-level field alongside the data:

```json
{
  "schema_version": 1,
  "id": "beads-abc",
  "title": "Example issue",
  "status": "open",
  "priority": 1,
  "issue_type": "task",
  "created_at": "2026-04-20T12:00:00Z"
}
```

### List commands (list, ready, search, stale, show, close, update, etc.)

Commands that return one or more issues emit a raw JSON array — including
`show`, `close`, and `update`, which return one element per requested ID.
Array output carries no top-level `schema_version` field:

```json
[
  {"id": "beads-abc", "title": "First", ...},
  {"id": "beads-def", "title": "Second", ...}
]
```

### Error output

Errors with `--json` active emit JSON — most error paths write it to stderr,
though some command-result error paths emit the same shape to stdout:

```json
{
  "schema_version": 1,
  "error": "issue not found: beads-xyz",
  "code": "not_found"
}
```

`code` and `hint` (a remediation suggestion) are both optional — only
`error` and `schema_version` are always present. In envelope mode
(`BD_JSON_ENVELOPE=1`) the error payload moves inside the envelope:
`{"schema_version": 1, "data": {"error": ..., "code": ..., "hint": ...}}`.
JSON-mode errors exit with code 1.

## Field Contracts by Command

### bd list --json

Required fields per item:
- `id` (string): Issue ID (e.g., "beads-abc")
- `title` (string): Issue title
- `status` (string): open, in_progress, closed, deferred
- `priority` (number): 0-4
- `issue_type` (string): bug, feature, task, epic, chore
- `created_at` (string): RFC3339 timestamp

Optional fields:
- `description`, `owner`, `updated_at`, `closed_at`
- `labels` (string[]): Attached labels
- `dependencies` (object[]): Dependency records
- `dependency_count`, `dependent_count`, `comment_count` (number)
- `parent` (string|null): Parent issue ID

### bd ready --json

Same schema as `bd list --json`. Items are filtered to unblocked issues only.
Each item includes `dependency_count`, `dependent_count`, `comment_count`,
and optional `parent` fields. In envelope mode a `--limit`-truncated result
adds the envelope-level `pagination` key (see the envelope section above).

### bd blocked --json

Returns issues that are blocked by unresolved dependencies.
Each item includes all standard issue fields plus:
- `blocked_by_count` (number): Number of blocking dependencies
- `blocked_by` (string[]): IDs of blocking issues

### bd show --json

Returns a top-level JSON array with one element per requested ID; items do
not carry `schema_version` (this shape is pinned by a contract test — a
change here is a breaking wire change). Same required fields as list
items, plus:
- `description` (string)
- `acceptance_criteria` (string)
- `revision` (number): guarded-write optimistic-concurrency token; always
  present, including a legacy `0`
- `dependencies` (object[]): Full dependency records
- `comments` (object[]): Comment thread — present only with `--include-comments`;
  the default response returns `comment_count` only (count-only, be-ijck6q)
- `comments_omitted` (boolean, optional): `true` only when `comment_count` is
  nonzero and `comments` was left out of the response (no `--include-comments`).
  Absent when comments were included or when there are none to omit (ga-clgh)

### `import --json`

Returns a summary object when `--json` is active:
- `source` (string): File path or "stdin"
- `created` (number): Issues created
- `updated` (number): Existing issues updated
- `unchanged` (number, optional): Rows identical to local state, untouched
- `skipped` (number): Issues skipped (stale rows + dedup)
- `dedup_skipped` (number): Issues skipped by `--dedup` title match
- `memories` (number): Memory records imported
- `ids` (string[]): IDs of created issues
- `updated_issues` (object[]): Per-issue summary of what an update changed
- `tie_kept_local_ids` (string[]): Equal-`updated_at` rows where local state won
- `stale_skipped_ids` (string[]): Rows older than the local issue, skipped
- `skipped_dependencies` (string[]): Dependency edges whose target id was absent
- `dry_run` (boolean): Whether `--dry-run` was active

### bd export --json

Outputs JSONL (one JSON object per line), not wrapped in an envelope.
Each line is a self-contained issue or memory record, discriminated by
`_type` (`"issue"` / `"memory"`). Export lines do **not** carry
`schema_version` — that field belongs to the `--json` command envelope,
not to the interchange stream. The interchange's own version marker is the
optional `_schema` header record (`{"_schema":"beads-jsonl/1"}`), which
readers skip.

Issue records carry an optional `wisp_plane` boolean: the explicit
wisps-plane marker. Export stamps it on rows that live in the wisps table
when the row flags alone cannot prove the plane (a `no_history: true` record
is otherwise ambiguous — an unpromoted no-history wisp and a promoted one
look identical). Import routes the storage plane by this marker, never by
`no_history`: marker absent means the durable issues table. The marker is a
fresh key rather than a reuse of the legacy `wisp` boolean so that older
readers, which do not know it, degrade to flag routing instead of importing
marked rows as ephemeral (purge-eligible and export-excluded). The v0.35–
v0.37 `wisp` key — those streams' spelling of `ephemeral` — is still honored
as a read-side legacy alias.

## Consumer Guidelines

1. **Check `schema_version`** on object output. If the version is
   higher than expected, log a warning but attempt to parse anyway
   (additive changes are backward-compatible).

2. **For list commands**, parse the output as a JSON array directly.

3. **Ignore unknown fields**. New fields may be added without bumping
   the schema version.

4. **Use `--json` flag**, not `--format json`. The `--json` flag is
   the stable contract; `--format` is for human-readable variants.
