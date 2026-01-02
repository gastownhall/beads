# Chemistry Patterns

> Adapted from ACF beads skill

Beads uses a chemistry metaphor for work templates. This guide covers when and how to use each phase.

## Phase Transitions

```
┌─────────────────────────────────────────────────────────────┐
│                    PROTO (Solid)                            │
│              Frozen template, reusable pattern              │
│                    .beads/ with template label              │
└─────────────────────────┬───────────────────────────────────┘
                          │
          ┌───────────────┼───────────────┐
          │               │               │
          ▼               │               ▼
┌─────────────────┐       │       ┌─────────────────┐
│   MOL (Liquid)  │       │       │  WISP (Vapor)   │
│   bd pour       │       │       │  bd wisp create │
│                 │       │       │                 │
│  Persistent     │       │       │  Ephemeral      │
│  .beads/        │       │       │  .beads-wisp/   │
│  Git synced     │       │       │  Gitignored     │
└────────┬────────┘       │       └────────┬────────┘
         │                │                │
         │                │        ┌───────┴───────┐
         │                │        │               │
         ▼                │        ▼               ▼
   ┌──────────┐           │   ┌─────────┐    ┌─────────┐
   │  CLOSE   │           │   │ SQUASH  │    │  BURN   │
   │ normally │           │   │ → digest│    │ → gone  │
   └──────────┘           │   └─────────┘    └─────────┘
                          │
                          ▼
                  ┌───────────────┐
                  │   DISTILL     │
                  │ Extract proto │
                  │ from ad-hoc   │
                  │ epic          │
                  └───────────────┘
```

## Decision Tree: Mol vs Wisp

```
Will this work be referenced later?
│
├─ YES → Does it need audit trail / git history?
│        │
│        ├─ YES → MOL (bd pour)
│        │        Examples: Features, bugs, specs
│        │
│        └─ NO  → Could go either way
│                 Consider: Will someone else see this?
│                 │
│                 ├─ YES → MOL
│                 └─ NO  → WISP (then squash if valuable)
│
└─ NO  → WISP (bd wisp create)
         Examples: Grooming, health checks, scratch work
         End state: burn (no value) or squash (capture learnings)
```

## Quick Reference

| Scenario | Use | Command | End State |
|----------|-----|---------|-----------|
| New feature work | Mol | `bd pour spec` | Close normally |
| Bug fix | Mol | `bd pour bug` | Close normally |
| Grooming session | Wisp | `bd wisp create grooming` | Squash → digest |
| Code review | Wisp | `bd wisp create review` | Squash findings |
| Research spike | Wisp | `bd wisp create spike` | Squash or burn |
| Session health check | Wisp | `bd wisp create health` | Burn |
| Agent coordination | Wisp | `bd wisp create coordinator` | Burn |

## Common Patterns

### Pattern 1: Grooming Wisp

Use for periodic backlog maintenance.

```bash
# Start grooming
bd wisp create grooming --var date="2025-01-02"

# Work through checklist (stale, duplicates, verification)
# Track findings in wisp notes

# End: capture summary
bd mol squash <wisp-id>  # Creates digest: "Closed 3, added 5 relationships"
```

**Why wisp?** Grooming is operational—you don't need permanent issues for "reviewed stale items."

### Pattern 2: Code Review Wisp

Use for PR review checklists.

```bash
# Start review
bd wisp create pr-review --var pr="123" --var repo="myproject"

# Track review findings (security, performance, style)
# Each finding is a child issue in the wisp

# End: promote real issues, discard noise
bd mol squash <wisp-id>  # Creates permanent issues for real findings
```

**Why wisp?** Review checklists are ephemeral. Only actual findings become permanent issues.

### Pattern 3: Research Spike Wisp

Use for time-boxed exploration.

```bash
# Start spike (2 hour timebox)
bd wisp create spike --var topic="GraphQL pagination"

# Explore, take notes in wisp issues
# Track sources, findings, dead ends

# End: decide outcome
bd mol squash <wisp-id>  # If valuable → creates research summary issue
# OR
bd mol burn <wisp-id>    # If dead end → no trace
```

**Why wisp?** Research might lead nowhere. Don't pollute the database with abandoned explorations.

## Commands Reference

### Creating Work

```bash
# Persistent mol (solid → liquid)
bd pour <proto>                    # Synced to git
bd pour <proto> --var key=value

# Ephemeral wisp (solid → vapor)
bd wisp create <proto>             # Not synced
bd wisp create <proto> --var key=value
```

### Ending Work

```bash
# Mol: close normally
bd close <mol-id>

# Wisp: squash (condense to digest)
bd mol squash <wisp-id>            # Creates permanent digest issue

# Wisp: burn (evaporate, no trace)
bd mol burn <wisp-id>              # Deletes with no record
```

### Managing

```bash
# List wisps
bd wisp list

# Garbage collect orphaned wisps
bd wisp gc

# View proto/mol structure
bd mol show <id>

# List available protos
bd mol catalog
```

## Storage Locations

| Type | Location | Git Behavior |
|------|----------|--------------|
| Proto | `.beads/` | Synced (template label) |
| Mol | `.beads/` | Synced |
| Wisp | `.beads-wisp/` | Gitignored |

## Anti-Patterns

| Don't | Do Instead |
|-------|------------|
| Create mol for one-time diagnostic | Use wisp, then burn |
| Create wisp for real feature work | Use mol (needs audit trail) |
| Burn wisp with valuable findings | Squash first (captures digest) |
| Let wisps accumulate | Burn or squash at session end |
| Create ad-hoc epics for repeatable patterns | Distill into proto |

## Related Resources

- [MOLECULES.md](MOLECULES.md) — Proto definitions
- [WORKFLOWS.md](WORKFLOWS.md) — General beads workflows
