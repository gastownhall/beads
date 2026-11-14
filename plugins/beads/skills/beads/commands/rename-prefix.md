---
description: Rename the issue prefix for all issues
argument-hint: "<new-prefix> [--dry-run] [--repair | --mint-only]"
---

Rename the issue prefix for all issues in the database.

Updates all issue IDs and all text references across all fields.

## Prefix Rules

- Allowed: lowercase letters, numbers, hyphens
- Must start with a letter
- Must end with a hyphen (e.g., 'kw-', 'work-')

## Usage

- **Preview**: `bd rename-prefix kw- --dry-run`
- **Apply**: `bd rename-prefix kw-`
- **Consolidate several prefixes**: `bd rename-prefix kb- --repair` keeps each
  issue's suffix (`taxdb-u6ch` -> `kb-u6ch`), leaves `kb-*` issues alone, and
  moves a whole family to a fresh `kb-<8 hex>` root only when its suffix is
  already taken; every such move is reported. Add a hyphenated source prefix
  to `allowed_prefixes` to split it exactly (`cre-db-wxyz` -> `kb-wxyz`).
- **New issues only**: `bd rename-prefix kb- --mint-only` makes new issues use
  `kb-` and keeps every existing ID; the old prefix stays valid, so children of
  existing issues can still be created. Cannot be combined with `--repair`.
  A dependency between an old-prefix and a new-prefix issue does not block
  (`bd ready` lists the dependent) until a later `--repair` consolidates the
  prefixes.

Example: Rename from 'knowledge-work-' to 'kw-'

All dependencies and text references are automatically updated.
