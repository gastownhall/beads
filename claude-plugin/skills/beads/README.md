# Beads Skill for Claude Code

A comprehensive skill for using [beads](https://github.com/steveyegge/beads) (bd) issue tracking with Claude Code.

## What This Skill Does

This skill teaches Claude Code how to use bd effectively for:
- **Multi-session work tracking** - Persistent memory across conversation compactions
- **Dependency management** - Graph-based issue relationships
- **Session handoff** - Writing notes that survive context resets
- **Molecules and wisps** (v0.34.0+) - Reusable work templates and ephemeral workflows

## Installation

Copy the `beads/` directory to your Claude Code skills location:

```bash
# Global installation
cp -r beads ~/.claude/skills/

# Or project-local
cp -r beads .claude/skills/
```

## When Claude Uses This Skill

The skill activates when conversations involve:
- "multi-session", "complex dependencies", "resume after weeks"
- "project memory", "persistent context", "side quest tracking"
- Work that spans multiple days or compaction cycles
- Tasks too complex for simple TodoWrite lists

## File Structure

```
beads/
├── SKILL.md                 # Main skill file (Claude reads this first)
├── CLAUDE.md                # Maintenance guide for updating the skill
├── README.md                # This file (for humans)
├── adr/                     # Architectural Decision Records
│   └── 0001-bd-prime-as-source-of-truth.md
└── resources/               # Detailed documentation (loaded on demand)
    ├── BOUNDARIES.md        # When to use bd vs TodoWrite
    ├── CLI_REFERENCE.md     # CLI command reference
    ├── DEPENDENCIES.md      # Dependency semantics (A blocks B vs B blocks A)
    ├── INTEGRATION_PATTERNS.md # TodoWrite and other tool integration
    ├── ISSUE_CREATION.md    # When and how to create issues
    ├── MOLECULES.md         # Protos, mols, wisps (v0.34.0+)
    ├── PATTERNS.md          # Common usage patterns
    ├── RESUMABILITY.md      # Writing notes for post-compaction recovery
    ├── STATIC_DATA.md       # Using bd for reference databases
    ├── TROUBLESHOOTING.md   # Common issues and fixes
    ├── WORKFLOWS.md         # Step-by-step workflow guides
    ├── AGENTS.md            # Agent bead tracking (v0.40+)
    ├── ASYNC_GATES.md       # Human-in-the-loop gates
    ├── CHEMISTRY_PATTERNS.md # Mol vs Wisp decision tree
    └── WORKTREES.md         # Parallel development patterns
```

## Key Concepts

### bd vs TodoWrite

| Use bd when... | Use TodoWrite when... |
|----------------|----------------------|
| Work spans multiple sessions | Single-session tasks |
| Complex dependencies exist | Linear step-by-step work |
| Need to resume after weeks | Just need a quick checklist |
| Knowledge work with fuzzy boundaries | Clear, immediate tasks |

### The Dependency Direction Trap

`bd dep add A B` means **"A depends on B"** (B must complete before A can start).

```bash
# Want: "Setup must complete before Implementation"
bd dep add implementation setup  # ✓ CORRECT
# NOT: bd dep add setup implementation  # ✗ WRONG
```

### Surviving Compaction

When Claude's context gets compacted, conversation history is lost but bd state survives. Write notes as if explaining to a future Claude with zero context:

```bash
bd update issue-123 --notes "COMPLETED: JWT auth with RS256
KEY DECISION: RS256 over HS256 for key rotation
IN PROGRESS: Password reset flow
NEXT: Implement rate limiting"
```

## Requirements

- [bd CLI](https://github.com/steveyegge/beads) installed (`brew install beads`)
- A git repository (bd requires git for sync)
- Initialized database (`bd init` in project root)

## Version Compatibility

| Version | Features |
|---------|----------|
| v0.47.0+ | Pull-first sync, resolve-conflicts, dry-run create, gate auto-discovery |
| v0.43.0+ | Full support: agents, gates, worktrees, chemistry patterns |
| v0.40.0+ | Agent beads, async gates, worktree management |
| v0.34.0+ | Molecules, wisps, cross-project dependencies |
| v0.15.0+ | Core: dependencies, notes, status tracking |
| Earlier | Basic functionality, some features missing |

## Contributing

This skill is maintained at [github.com/steveyegge/beads](https://github.com/steveyegge/beads) in the `claude-plugin/skills/beads/` directory.

Issues and PRs welcome for:
- Documentation improvements
- New workflow patterns
- Bug fixes in examples
- Additional troubleshooting scenarios

## License

MIT (same as beads)
