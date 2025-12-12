---
description: Show the AI-supervised issue workflow guide
---

Display the beads workflow for AI agents and developers.

# Beads Workflow

Beads is an issue tracker designed for AI-supervised coding workflows. Here's how to use it effectively:

## 1. Find Ready Work
Use `/beads:ready` or the `ready` MCP tool to see tasks with no blockers.

## 2. Claim Your Task
Update the issue status to `in_progress`:
- Via command: `/beads:update <id> in_progress`
- Via MCP tool: `update` with `status: "in_progress"`

## 3. Work on It
Implement, test, and document the feature or fix.

## 4. Discover New Work
As you work, you'll often find bugs, TODOs, or related work:
- Create issues: `/beads:create` or `create` MCP tool
- Link them: Use `dep` MCP tool with `type: "discovered-from"`
- This maintains context and work history

## 5. Complete the Task
Close the issue when done:
- Via command: `/beads:close <id> "Completed: <summary>"`
- Via MCP tool: `close` with reason

## 6. Check What's Unblocked
After closing, check if other work became ready:
- Use `/beads:ready` to see newly unblocked tasks
- Start the cycle again

## Tips
- **Priority levels**: 0=critical, 1=high, 2=medium, 3=low, 4=backlog
- **Issue types**: bug, feature, task, epic, chore
- **Dependencies**: Use `blocks` for hard dependencies, `related` for soft links
- **Auto-sync**: Changes automatically export to `.beads/issues.jsonl` (5-second debounce)
- **Git workflow**: After `git pull`, JSONL auto-imports if newer than DB

## Available Commands
- `/beads:ready` - Find unblocked work
- `/beads:create` - Create new issue
- `/beads:show` - Show issue details
- `/beads:update` - Update issue
- `/beads:close` - Close issue
- `/beads:workflow` - Show this guide (you are here!)

## MCP Tools Available
Use these via the beads MCP server:
- `ready`, `list`, `show`, `create`, `update`, `close`
- `dep` (manage dependencies), `blocked`, `stats`
- `init` (initialize bd in a project)

For more details, see the beads README at: https://github.com/steveyegge/beads
