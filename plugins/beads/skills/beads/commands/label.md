---
description: Manage issue labels
argument-hint: [command] [issue-id] [label]
---

Manage labels on beads issues. Labels provide flexible cross-cutting metadata beyond structured fields (status, priority, type).

## Available Commands

- **add**: Add a label to an issue
  - $1: "add"
  - $2: Issue ID
  - $3: Label name

- **remove**: Remove a label from an issue
  - $1: "remove"
  - $2: Issue ID
  - $3: Label name

- **list**: List labels on a specific issue
  - $1: "list"
  - $2: Issue ID

- **list-all**: Show all labels used across all issues

## Common Label Use Cases

- Technical scope: `backend`, `frontend`, `api`, `database`
- Quality gates: `needs-review`, `needs-tests`, `security-review`
- Effort sizing: `quick-win`, `complex`, `spike`
- Context: `technical-debt`, `documentation`, `performance`

Use `bd label add <issue-id> <label>` to tag issues with contextual metadata.
