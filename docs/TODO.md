# TODO Command

The `bd todo` command provides a lightweight interface for managing TODO items as task-type issues.

## Philosophy

TODOs in bd are not a separate tracking system - they are regular task-type issues with convenient shortcuts. This means:

- **No parallel systems**: TODOs use the same storage and sync as all other issues
- **Promotable**: Easy to convert a TODO to a bug/feature when needed
- **Full featured**: TODOs support all bd features (dependencies, labels, routing)
- **Simple interface**: Quick commands for common TODO workflows

## Quick Start

```bash
# Add a TODO
bd todo add "Fix the login bug" -p 1

# List TODOs
bd todo

# Mark TODO as done
bd todo done <id>
```

## Commands

### `bd todo` (or `bd todo list`)

List all open task-type issues.

```bash
bd todo                  # List open TODOs
bd todo list            # Same as above
bd todo list --all      # Show completed TODOs too
bd todo list --json     # JSON output
```

**Output:**
```
  ○ test-yxg  Fix the login bug                         ● P1  open
  ○ test-ryl  Update documentation                      ● P3  open

Total: 2 TODOs
```

### `bd todo add <title>`

Create a new TODO item (task-type issue).

```bash
bd todo add "Fix the login bug"                                # Default P2
bd todo add "Update docs" -p 3 -d "Add examples"              # With priority and description
bd todo add "Critical fix" --priority 0 --description "ASAP"  # P0 task
```

**Flags:**
- `-p, --priority <0-4>`: Priority (default: 2)
- `-d, --description <text>`: Description

### `bd todo done <id> [<id>...]`

Mark one or more TODOs as complete.

```bash
bd todo done test-abc              # Close one TODO
bd todo done test-abc test-def     # Close multiple
bd todo done test-abc --reason "Fixed in PR #42"  # With reason
```

**Flags:**
- `--reason <text>`: Reason for closing (default: "Completed")

## Converting TODOs

TODOs are regular task issues, so you can convert them:

```bash
# Promote TODO to bug
bd update test-abc --type bug --priority 0

# Add dependencies
bd dep add test-abc test-def

# Add labels
bd update test-abc --labels "urgent,frontend"
```

## Viewing TODO Details

Use regular bd commands:

```bash
bd show test-abc        # View TODO details
bd list --type task     # List all tasks (including TODOs)
bd ready               # See ready TODOs in work queue
```

## Examples

### Daily TODO workflow

```bash
# Morning: add your tasks
bd todo add "Review PRs"
bd todo add "Fix CI pipeline" -p 1
bd todo add "Update changelog" -p 3

# Check what's on your plate
bd todo

# Complete work
bd todo done <id>
bd todo done <id>

# End of day: see what's left
bd todo
```

### Converting TODO to full issue

```bash
# Start with a quick TODO
bd todo add "Login is broken"

# Later, realize it's more serious
bd update <id> --type bug --priority 0 --description "Users can't login, multiple reports"
bd update <id> --acceptance "Login works for all user types"

# Now it's a full-fledged bug with proper tracking
bd show <id>
```

## FAQ

**Q: Are TODOs different from tasks?**
A: No, TODOs are just task-type issues. The `bd todo` command provides shortcuts for common task operations.

**Q: Can TODOs have dependencies?**
A: Yes! Use `bd dep add <todo-id> <blocks-id>` like any other issue.

**Q: Do TODOs sync with git?**
A: Yes, they're exported to `.beads/issues.jsonl` like all other issues.

**Q: Can I use TODOs with bd ready?**
A: Yes! `bd ready` shows all unblocked issues, including task-type TODOs.

**Q: Should I use TODOs or regular tasks?**
A: Use `bd todo` for quick, informal tasks. Use `bd create -t task` for tasks that need more context or are part of larger planning.

## Design Rationale

The TODO command follows beads' philosophy of **minimal surface area**:

1. **No new types**: TODOs are task-type issues
2. **No special storage**: Same database and JSONL as everything else
3. **Convenience layer**: Just shortcuts for common operations
4. **Fully compatible**: Works with all bd features and commands

This ensures:
- No duplicate tracking systems
- No migration needed between TODOs and tasks
- Works with all existing bd tooling (federation, compaction, routing)
- Simple to understand and maintain
