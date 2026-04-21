# JSON Output Schema Contract

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

### Updating consumers

```bash
# Before (legacy):
bd list --json | jq '.[0].id'
bd show beads-abc --json | jq '.title'

# After (envelope):
bd list --json | jq '.data[0].id'
bd show beads-abc --json | jq '.data.title'

# Version check:
bd show beads-abc --json | jq '.schema_version'
```

### Timeline

- **Current release**: Legacy format is default. Set `BD_JSON_ENVELOPE=1` to opt in.
  A deprecation notice is printed to stderr when `--json` is used without the env var.
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

### Object commands (show, create, close, update, etc.)

Commands that return a single issue or result emit a JSON object with
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

### List commands (list, ready, search, stale, etc.)

Commands that return multiple items emit a raw JSON array:

```json
[
  {"id": "beads-abc", "title": "First", ...},
  {"id": "beads-def", "title": "Second", ...}
]
```

### Error output (stderr)

Errors with `--json` active emit JSON to stderr:

```json
{
  "schema_version": 1,
  "error": "issue not found: beads-xyz",
  "code": "not_found"
}
```

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
and optional `parent` fields.

### bd blocked --json

Returns issues that are blocked by unresolved dependencies.
Each item includes all standard issue fields plus:
- `blocked_by_count` (number): Number of blocking dependencies
- `blocked_by` (string[]): IDs of blocking issues

### bd show --json

Returns a single object (not wrapped in `items`). Same required fields as list
items, plus:
- `description` (string)
- `acceptance_criteria` (string)
- `dependencies` (object[]): Full dependency records
- `comments` (object[]): Comment thread

### `import --json`

Returns a summary object when `--json` is active:
- `source` (string): File path or "stdin"
- `created` (number): Issues created
- `skipped` (number): Issues skipped (dedup)
- `dedup_skipped` (number): Issues skipped by `--dedup` title match
- `memories` (number): Memory records imported
- `ids` (string[]): IDs of created issues
- `dry_run` (boolean): Whether `--dry-run` was active

### bd export --json

Outputs JSONL (one JSON object per line), not wrapped in an envelope.
Each line is a self-contained issue or memory record. `schema_version`
is included per line.

## Consumer Guidelines

1. **Check `schema_version`** on object output. If the version is
   higher than expected, log a warning but attempt to parse anyway
   (additive changes are backward-compatible).

2. **For list commands**, parse the output as a JSON array directly.

3. **Ignore unknown fields**. New fields may be added without bumping
   the schema version.

4. **Use `--json` flag**, not `--format json`. The `--json` flag is
   the stable contract; `--format` is for human-readable variants.
