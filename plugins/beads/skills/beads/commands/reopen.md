---
description: Reopen closed issues
argument-hint: "[issue-ids...] [--reason]"
---

Reopen one or more closed issues.

Sets status to 'open' and clears the closed_at timestamp, together with the current `close_reason` and `closed_by_session`. Emits a Reopened event; a `--reason` is also appended to the issue's comment thread (readable via `bd comments`, `bd show` and issue export) and carried on the reopened audit event. A previously committed close reason remains readable with `bd history <id>`.

## Usage

- **Reopen single**: `bd reopen bd-42`
- **Reopen multiple**: `bd reopen bd-42 bd-43 bd-44`
- **With reason**: `bd reopen bd-42 --reason "Found regression"` — the reason lands in the comment thread, attributed to you

More explicit than `bd update --status open` - specifically designed for reopening workflow.

Common reasons for reopening:
- Regression found
- Requirements changed
- Incomplete implementation
- New information discovered
