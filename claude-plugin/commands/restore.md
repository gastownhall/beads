---
description: Restore full history of compacted issue from git
argument-hint: <issue-id>
---

Restore full history of a compacted issue from git version control.

When an issue is compacted, the git commit hash is saved. This command:

1. Reads the compacted_at_commit from the database
2. Retrieves the full issue from Dolt history at that point
3. Displays the full issue history (description, events, etc.)
4. Returns to the current state

## Usage

`bd restore bd-42`

This is **read-only** - it does not modify the database or git state.

Useful for:
- Reviewing old issues after compaction
- Recovering forgotten context
- Audit trails
- Historical research

Requires git repository with issue history.
