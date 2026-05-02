---
description: Epic management commands
argument-hint: [command]
---

Manage epics (large features composed of multiple issues).

## Available Commands

- **status**: Show epic completion status
  - Shows progress for each epic
  - Lists child issues and their states
  - Calculates completion percentage

- **close-eligible**: Close epics where all children are complete
  - Automatically closes epics when all child issues are done
  - Useful for bulk epic cleanup

## Epic Workflow

1. Create epic: `bd create "Large Feature" -t epic -p 1`
2. Link subtasks: `bd dep add bd-20 bd-10 --type parent-child` (task bd-20 is child of epic bd-10)
   - Or at creation: `bd create "Subtask title" -t task --parent bd-10`
3. Track progress: `bd epic status`
4. Auto-close when done: `bd epic close-eligible`

Epics use parent-child dependencies to track subtasks.
