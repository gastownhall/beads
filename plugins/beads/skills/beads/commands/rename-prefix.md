---
description: Rename the issue prefix for all issues
argument-hint: <new-prefix> [--dry-run]
---

Rename the issue prefix for all issues in the database.

Updates all issue IDs and all text references across all fields.

## Prefix Rules

- Max length: 8 characters
- Allowed: lowercase letters, numbers, hyphens
- Must start with a letter
- Must end with a hyphen (e.g., 'kw-', 'work-')

## Usage

- **Preview**: `bd rename-prefix kw- --dry-run`
- **Apply**: `bd rename-prefix kw-`

Example: Rename from 'knowledge-work-' to 'kw-'

All dependencies and text references are automatically updated.
