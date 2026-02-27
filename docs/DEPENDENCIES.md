# Dependencies and Gates

Beads includes a full dependency system for ordering work and a gate system
for bridging external conditions (PR merges, CI runs, timers) into the
dependency graph.

## Adding Dependencies

```bash
# issue-2 depends on issue-1 (issue-1 blocks issue-2)
bd dep add issue-2 issue-1

# Shorthand: issue-1 blocks issue-2
bd dep issue-1 --blocks issue-2

# Alternative flags (equivalent)
bd dep add issue-2 --blocked-by issue-1
bd dep add issue-2 --depends-on issue-1
```

When issue-1 is open, issue-2 won't appear in `bd ready`. Once issue-1
is closed, issue-2 unblocks automatically.

## Removing Dependencies

```bash
bd dep remove issue-2 issue-1
bd dep rm issue-2 issue-1        # alias
```

## Dependency Types

Dependencies have a type that determines whether they block work.

**Blocking types** (affect `bd ready`):

| Type | Meaning | Example |
|------|---------|---------|
| `blocks` (default) | B cannot start until A closes | Task ordering |
| `parent-child` | Children blocked when parent blocked | Epic hierarchies |
| `conditional-blocks` | B runs only if A fails | Error handling paths |
| `waits-for` | B waits for all of A's children | Fanout aggregation |

**Non-blocking types** (graph annotations only):

| Type | Meaning |
|------|---------|
| `related` | Informational link |
| `tracks` | Tracks progress of another issue |
| `discovered-from` | Found during work on another issue |
| `caused-by` | Root cause link |
| `validates` | Test or verification link |
| `supersedes` | Replaces another issue |

Specify with `--type`:

```bash
bd dep add issue-2 issue-1 --type tracks
bd dep add issue-2 issue-1 --type caused-by
```

## Finding Ready Work

`bd ready` shows issues with no open blocking dependencies:

```bash
bd ready
```

Output:
```
📋 Ready work (1 issues with no blockers):

1. [P1] bd-a1b2: Set up database
```

An issue is ready when ALL of its blocking dependencies are closed.

```bash
# Filter ready work
bd ready --priority 1              # By priority
bd ready --label backend           # By label
bd ready --assignee alice          # By assignee
bd ready --unassigned              # Unassigned only
bd ready --type task               # By issue type
bd ready --sort oldest             # Oldest first
```

## Viewing Blocked Issues

```bash
bd blocked
```

Shows every blocked issue and what blocks it. Use after closing an issue
to see what just unblocked.

## Visualizing Dependencies

### Dependency Tree

```bash
bd dep tree issue-id                    # What does this issue depend on?
bd dep tree issue-id --direction=up     # What depends on this issue?
bd dep tree issue-id --direction=both   # Both directions
bd dep tree issue-id --status=open      # Only open issues
bd dep tree issue-id --max-depth=3      # Limit depth
bd dep tree issue-id --format=mermaid   # Mermaid.js output
```

### Dependency Graph

```bash
bd graph issue-id                       # Single issue DAG
bd graph --all                          # All open issues

# Output formats
bd graph --compact issue-id             # One line per issue
bd graph --box issue-id                 # ASCII boxes with layers
bd graph --dot issue-id | dot -Tsvg > graph.svg   # Graphviz
bd graph --html issue-id > graph.html   # Interactive D3.js
```

The graph organizes issues into layers:
- **Layer 0**: No dependencies (can start immediately)
- **Layer 1**: Depends on layer 0
- **Higher layers**: Depend on lower layers
- **Same layer**: Can run in parallel

### Dependency List

```bash
bd dep list issue-id                    # What does this depend on?
bd dep list issue-id --direction=up     # What depends on this?
bd dep list issue-id --type=tracks      # Filter by type
```

### Cycle Detection

```bash
bd dep cycles
```

Beads also rejects cycles at write time — `bd dep add` checks for
cycles before committing.

## Cross-Repo Dependencies

Dependencies can reference issues in other beads rigs:

```bash
bd dep add local-issue external:other-project:remote-issue
```

External dependencies always block. When the remote issue closes,
`bd ready` reflects the change (checked at query time).

## Gates

Gates are special issues that block dependent work until an external
condition is met. They bridge the gap between beads (which tracks work)
and external systems (which track code, CI, or time).

### The Problem Gates Solve

When you use Dolt (server or embedded), issue state is decoupled from
code state. Closing a beads issue means "work is done" but the code
may still be on a feature branch, waiting for PR review:

```
issue-1: closed in beads     (work done)
PR #42:  open on GitHub      (code not yet on main)
issue-2: blocked by issue-1  (should it start?)
```

With file-based storage (JSONL), issue updates land atomically with
code in the same commit. With Dolt, they don't. Gates solve this by
making the dependency wait for the external condition — not just the
beads issue status.

### Gate Types

| Type | Condition | Auto-Resolution |
|------|-----------|-----------------|
| `gh:pr` | PR merged | `gh pr view` returns MERGED |
| `gh:run` | CI passes | `gh run view` returns completed + success |
| `timer` | Time elapsed | Current time exceeds timeout |
| `bead` | Cross-rig issue closed | Remote bead status checked |
| `human` | Manual approval | `bd gate resolve <id>` |

### Creating Gates

```bash
# Wait for PR #42 to merge
bd create --type=gate --title="Wait for PR #42" \
  --await-type=gh:pr --await-id=42

# Wait for CI run
bd create --type=gate --title="Wait for CI" \
  --await-type=gh:run --await-id=12345

# Wait 30 minutes
bd create --type=gate --title="Cooldown" \
  --await-type=timer --await-id=30m

# Wait for a cross-rig bead to close
bd create --type=gate --title="Wait for upstream fix" \
  --await-type=bead --await-id=other-rig:issue-id

# Manual approval gate
bd create --type=gate --title="Deploy approval"
```

### Wiring Gates into Dependencies

A gate is an issue. Wire it into the dependency graph like any other:

```bash
# issue-2 waits for the gate (which waits for PR #42)
bd dep add issue-2 <gate-id>
```

### Checking Gates

`bd gate check` evaluates all open gates and closes resolved ones:

```bash
bd gate check                    # Check all gates
bd gate check --type=gh:pr       # Only PR gates
bd gate check --type=gh:run      # Only CI gates
bd gate check --type=timer       # Only timers
bd gate check --dry-run          # Preview without changes
bd gate check --escalate         # Escalate failed gates
```

Escalation marks gates whose conditions failed (e.g., PR closed without
merge, CI run failed) so they surface for attention.

### Listing and Inspecting Gates

```bash
bd gate list                     # Open gates
bd gate list --all               # Including closed
bd gate show <gate-id>           # Full details
```

### Manual Resolution

For `human` gates or overrides:

```bash
bd gate resolve <gate-id> --reason "Approved by team lead"
```

### Discovering CI Run IDs

When you create a `gh:run` gate before the run starts, `bd gate discover`
matches gates to GitHub Actions runs using heuristics (commit SHA, branch,
timing):

```bash
bd gate discover                 # Auto-match gates to runs
bd gate discover --dry-run       # Preview matches
bd gate discover --branch main   # Filter by branch
```

### Automating Gate Checks

Run `bd gate check` periodically to auto-close resolved gates:

- **CI step**: Add to your GitHub Actions workflow
- **Cron**: `*/5 * * * * cd /path/to/repo && bd gate check`
- **Agent hook**: Run at session start or after PR operations

## Recipes

### PR Merge Gate (Common)

Agent A finishes work, opens PR, creates a gate so Agent B waits for merge:

```bash
# Agent A
bd update issue-1 --status=in_progress
# ... write code, open PR #42 ...
bd create --type=gate --title="Wait for PR #42" \
  --await-type=gh:pr --await-id=42
bd dep add issue-2 <gate-id>
bd close issue-1

# Agent B
bd ready                         # issue-2 not shown (gate open)
# ... PR #42 merges ...
bd gate check                    # gate closes
bd ready                         # issue-2 appears
```

### CI Gate Before Deploy

```bash
bd create --type=gate --title="CI green on main" \
  --await-type=gh:run --await-id=<run-id>
bd dep add deploy-task <gate-id>
```

### Epic with Ordered Phases

```bash
bd create "Auth System" -t epic
bd create "Design" --parent <epic>
bd create "Implement" --parent <epic>
bd create "Test" --parent <epic>

bd dep add <implement> <design>
bd dep add <test> <implement>

bd dep tree <epic>
bd ready                         # Only "Design" is ready
```

## See Also

- [QUICKSTART.md](QUICKSTART.md) — First steps with dependencies
- [MOLECULES.md](MOLECULES.md) — Molecule workflows using gates and dependencies
- [MULTI_REPO_AGENTS.md](MULTI_REPO_AGENTS.md) — Cross-repo dependency patterns
- [DOLT.md](DOLT.md) — Dolt backend configuration
- [CLI_REFERENCE.md](CLI_REFERENCE.md) — Full command reference
