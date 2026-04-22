# Claude Code Integration for Beads

Slash command for converting [Claude Code](https://docs.anthropic.com/en/docs/claude-code) plans to beads tasks.

## Prerequisites

```bash
# Install beads
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash

# Install hooks (auto-injects workflow context on session start)
bd setup claude
```

## Installation

```bash
cp commands/plan-to-beads.md ~/.claude/commands/
```

Optionally add to `~/.claude/settings.json` under `permissions.allow`:

```json
"Bash(bd:*)"
```

## /plan-to-beads

Converts a Claude Code plan file into a beads epic with tasks.

```
/plan-to-beads                    # Convert most recent plan
/plan-to-beads path/to/plan.md    # Convert specific plan
```

**What it does:**
- Parses plan structure (title, summary, phases)
- Creates an epic for the plan
- Creates tasks from each phase
- Sets up sequential dependencies
- Uses Task agent delegation for context efficiency

**Example output:**
```
Created from: peaceful-munching-spark.md

Epic: Standardize ID Generation (bd-abc)
  ├── Add dependency (bd-def) - ready
  ├── Create ID utility (bd-ghi) - blocked by bd-def
  └── Update schema (bd-jkl) - blocked by bd-ghi

Total: 4 tasks
Run `bd ready` to start.
```

## Related

- `bd prime` - Workflow context (auto-injected via hooks)
- `bd setup claude` - Install/manage Claude Code hooks
- `bd ready` - Find unblocked work

## License

Same as beads (see repository root).
