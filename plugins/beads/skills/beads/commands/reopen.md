---
description: Reopen closed issues
argument-hint: "[issue-ids...] [--reason]"
---

Reopen one or more closed issues.

Sets status to 'open' and clears the close fields (closed_at, close_reason, closed_by_session). Emits a Reopened event.

A `--reason` is added as a comment on the issue, authored by the reopening actor, so `bd comments`, `bd show` and `bd export` show it; it is also kept on the local reopened audit event (`bd history <id> --events`). The cleared close reason stays in retained committed history: plain `bd history <id>` prints it for each snapshot taken while the issue was closed.

## Usage

- **Reopen single**: `bd reopen bd-42`
- **Reopen multiple**: `bd reopen bd-42 bd-43 bd-44`
- **With reason**: `bd reopen bd-42 --reason "Found regression"`

More explicit than `bd update --status open` - specifically designed for reopening workflow.

Common reasons for reopening:
- Regression found
- Requirements changed
- Incomplete implementation
- New information discovered
