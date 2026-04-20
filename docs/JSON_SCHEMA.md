# JSON Output Schema Contract

All `bd` commands that support `--json` output include a `schema_version` field
at the top level. Consumers should check this field to detect format changes.

## Schema Version

Current version: **1**

The `schema_version` field is an integer that increments when:
- Fields are added, renamed, or removed
- Output structure changes (e.g., nesting depth)
- Field types change (e.g., string to integer)

Additive changes (new optional fields) do NOT bump the version.

## Output Formats

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

Commands that return multiple items emit an envelope object:

```json
{
  "schema_version": 1,
  "items": [
    {"id": "beads-abc", "title": "First", ...},
    {"id": "beads-def", "title": "Second", ...}
  ]
}
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

Required fields per item in `items[]`:
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

### bd show --json

Returns a single object (not wrapped in `items`). Same required fields as list
items, plus:
- `description` (string)
- `acceptance_criteria` (string)
- `dependencies` (object[]): Full dependency records
- `comments` (object[]): Comment thread

### bd export --json

Outputs JSONL (one JSON object per line), not wrapped in an envelope.
Each line is a self-contained issue or memory record. `schema_version`
is included per line.

## Consumer Guidelines

1. **Always check `schema_version`** before parsing. If the version is
   higher than expected, log a warning but attempt to parse anyway
   (additive changes are backward-compatible).

2. **For list commands**, read items from the `items` field, not the
   top-level object.

3. **Ignore unknown fields**. New fields may be added without bumping
   the schema version.

4. **Use `--json` flag**, not `--format json`. The `--json` flag is
   the stable contract; `--format` is for human-readable variants.
