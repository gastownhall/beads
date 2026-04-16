---
description: Show the AI-supervised issue workflow guide
---

Display the beads workflow for AI agents and developers.

# Beads Workflow

Beads is an issue tracker designed for AI-supervised coding workflows. Here's how to use it effectively:

## 1. Find Ready Work
Use `bd ready --json` to see tasks with no blockers.

## 2. Claim Your Task
Claim the issue atomically (assignee + `in_progress` in one step):
Use `bd update <id> --claim --json`.

## 3. Work on It
Implement, test, and document the feature or fix.

## 4. Discover New Work
As you work, you'll often find bugs, TODOs, or related work:
- Create issues with `bd create ... --json`
- Link them with `bd dep add <issue> <discovered-from-id> --type discovered-from --json`
- This maintains context and work history

## 5. Complete the Task
Close the issue when done:
Use `bd close <id> --reason "Completed: <summary>" --json`.

## 6. Check What's Unblocked
After closing, check if other work became ready:
- Use `bd ready --json` to see newly unblocked tasks
- Start the cycle again

## Tips
- **Priority levels**: 0=critical, 1=high, 2=medium, 3=low, 4=backlog
- **Issue types**: bug, feature, task, epic, chore
- **Dependencies**: Use `blocks` for hard dependencies, `related` for soft links
- **Auto-sync**: Changes are stored in Dolt and synced via `bd dolt push` / `bd dolt pull`

## Available Commands
- `bd ready` - Find unblocked work
- `bd create` - Create new issue
- `bd show` - Show issue details
- `bd update` - Update issue
- `bd close` - Close issue
- `bd dep` - Manage dependencies

For more details, see the beads README at: https://github.com/gastownhall/beads
