# Molecular Chemistry in Beads

This document explains beads' molecular chemistry metaphor for template-based workflows.

## The Layer Cake

Beads has a layered architecture where each layer builds on the one below:

```
Formulas (YAML compile-time macros)
    ↓
Protos (template issues, read-only)
    ↓
Molecules (pour, bond, squash, burn)
    ↓
Epics (parent-child, dependencies) ← DATA PLANE
    ↓
Issues (JSONL, git-backed) ← STORAGE
```

**Key insight:** Molecules work without protos. You can create ad-hoc molecules (epics with children) directly. Protos are templates FOR molecules, not the other way around.

## Phase Metaphor

The chemistry metaphor uses phase transitions to describe where work lives:

| Phase | State | Storage | Synced | Use Case |
|-------|-------|---------|--------|----------|
| **Solid** | Proto | `.beads/` | Yes | Reusable templates |
| **Liquid** | Mol | `.beads/` | Yes | Persistent work |
| **Vapor** | Wisp | `.beads-wisp/` | No | Ephemeral operations |

### Phase Transitions

```
Proto (solid) ──pour──→ Mol (liquid)    # Persistent instantiation
Proto (solid) ──wisp──→ Wisp (vapor)    # Ephemeral instantiation
Wisp (vapor) ──squash──→ Digest (solid) # Compress to permanent record
Wisp (vapor) ──burn──→ (nothing)        # Discard without trace
```

## Core Concepts

### Protos (Templates)

A proto is an issue with the `template` label. It defines a reusable pattern of work:

```bash
# List available protos
bd mol catalog

# Show proto structure
bd mol show <proto-id>
```

Protos can contain:
- Template variables using `{{variable}}` syntax
- Hierarchical child issues (subtasks)
- Dependencies between children

### Molecules (Instances)

A molecule is a spawned instance of a proto (or an ad-hoc epic). When you "pour" a proto, you create real issues from the template:

```bash
# Pour: proto → persistent mol (liquid phase)
bd pour <proto-id> --var key=value

# The mol lives in .beads/ and is synced with git
```

### Wisps (Ephemeral Molecules)

Wisps are ephemeral molecules for operational workflows that shouldn't accumulate:

```bash
# Wisp: proto → ephemeral wisp (vapor phase)
bd wisp create <proto-id> --var key=value

# The wisp lives in .beads-wisp/ and is NOT synced
```

Use wisps for:
- Patrol cycles (deacon, witness)
- Health checks and monitoring
- One-shot orchestration runs
- Routine operations with no audit value

### Bonding (Combining Work)

The `bond` command polymorphically combines protos and molecules:

```bash
# proto + proto → compound proto (reusable template)
bd mol bond mol-feature mol-deploy

# proto + mol → spawn proto, attach to molecule
bd mol bond mol-review bd-abc123

# mol + mol → join into compound molecule
bd mol bond bd-abc123 bd-def456
```

Bond types:
- `sequential` (default) - B runs after A completes
- `parallel` - B runs alongside A
- `conditional` - B runs only if A fails

Phase control for bonding:
```bash
# Force spawn as liquid (persistent), even when attaching to wisp
bd mol bond mol-critical-bug wisp-patrol --pour

# Force spawn as vapor (ephemeral), even when attaching to mol
bd mol bond mol-temp-check bd-feature --wisp
```

### Squashing (Compressing)

Squash compresses a wisp's execution into a permanent digest:

```bash
# Squash wisp → permanent digest
bd mol squash <wisp-id>

# With agent-provided summary
bd mol squash <wisp-id> --summary "Brief description of what was done"

# Preview what would be squashed
bd mol squash <wisp-id> --dry-run
```

### Burning (Discarding)

Burn deletes a wisp without creating any digest:

```bash
# Delete wisp with no trace
bd mol burn <wisp-id>

# Preview what would be deleted
bd mol burn <wisp-id> --dry-run
```

## Lifecycle Patterns

### Patrol Cycle (Ephemeral)

```
1. bd wisp create mol-patrol    # Create ephemeral wisp
2. (execute patrol steps)       # Work through children
3. bd mol squash <id>           # Compress to digest
   # or
   bd mol burn <id>             # Discard without trace
```

### Feature Work (Persistent)

```
1. bd pour mol-feature --var name=auth    # Create persistent mol
2. (implement feature)                     # Work through children
3. bd close <children>                     # Complete subtasks
4. bd close <root>                         # Complete the feature
```

### Dynamic Bonding (Christmas Ornament Pattern)

Attach work dynamically using custom IDs:

```bash
# Spawn per-worker arms on a patrol
bd mol bond mol-polecat-arm bd-patrol --ref arm-{{name}} --var name=ace
# Creates: bd-patrol.arm-ace (and children like bd-patrol.arm-ace.capture)
```

## Progress Tracking

Molecule progress is **computed, not stored**:

- `progress.Completed` = count of closed children
- `progress.Total` = count of all children

This means:
- No state to get out of sync
- Dynamic fanout works automatically (new children increase Total)
- Closing children increases Completed

## Parallelism Model

**Default: Parallel.** Issues without `depends_on` relationships run in parallel.

**Opt-in Sequential:** Add blocking dependencies to create sequence:

```bash
# phase-2 depends on phase-1 (phase-1 must complete first)
bd dep add phase-2 phase-1
```

**Cognitive trap:** Temporal language ("Phase 1 comes before Phase 2") inverts dependencies. Think requirements: "Phase 2 **needs** Phase 1."

## Common Agent Pitfalls

### 1. Temporal Language Inverting Dependencies

**Wrong thinking:** "Phase 1 comes before Phase 2" → `bd dep add phase1 phase2`

**Right thinking:** "Phase 2 needs Phase 1" → `bd dep add phase2 phase1`

**Solution:** Use requirement language, not temporal language. Verify with `bd blocked`.

### 2. Confusing Protos with Molecules

- **Proto** = Template (has `template` label, read-only pattern)
- **Molecule** = Instance (real work, created by pour/wisp)

You can create molecules without protos (ad-hoc epics). Protos are just reusable patterns.

### 3. Forgetting to Squash/Burn Wisps

Wisps accumulate in `.beads-wisp/` if not cleaned up. At session end:

```bash
bd wisp list                    # Check for orphaned wisps
bd mol squash <id> --summary "" # Compress to digest
# or
bd mol burn <id>                # Discard if not needed
# or
bd wisp gc                      # Garbage collect old wisps
```

### 4. Thinking Phases Imply Sequence

Phases, steps, or numbered items in a plan do NOT create sequence. Only dependencies do.

```bash
# These tasks run in PARALLEL (no dependencies)
bd create "Step 1: Do X" ...
bd create "Step 2: Do Y" ...
bd create "Step 3: Do Z" ...

# Add dependencies to create sequence
bd dep add step2 step1    # step2 needs step1
bd dep add step3 step2    # step3 needs step2
```

## Orphan vs Stale Matrix

A molecule can be in one of four states based on two dimensions:

| | Assigned | Unassigned |
|---|---|---|
| **Blocking** | Active work | Orphaned (needs pickup) |
| **Not blocking** | In progress | Stale (complete but unclosed) |

- **Orphaned:** Complete-but-unclosed molecules blocking other assigned work
- **Stale:** Complete-but-unclosed molecules not blocking anything

Graph pressure (blocking other work) determines urgency, not time.

## Commands Reference

### Proto/Template Commands

```bash
bd mol catalog                  # List available protos
bd mol show <id>                # Show proto/molecule structure
bd mol distill <epic-id>        # Extract proto from ad-hoc epic
```

### Phase Transition Commands

```bash
bd pour <proto> --var k=v       # Proto → Mol (liquid)
bd wisp create <proto> --var k=v # Proto → Wisp (vapor)
bd mol squash <id>              # Wisp → Digest (solid)
bd mol burn <id>                # Wisp → nothing
```

### Bonding Commands

```bash
bd mol bond <A> <B>             # Polymorphic combine
bd mol bond <A> <B> --type parallel
bd mol bond <A> <B> --pour      # Force persistent
bd mol bond <A> <B> --wisp      # Force ephemeral
bd mol bond <A> <B> --ref <ref> # Dynamic child ID
```

### Wisp Management

```bash
bd wisp list                    # List all wisps
bd wisp list --all              # Include closed
bd wisp gc                      # Garbage collect orphaned wisps
bd wisp gc --age 24h            # Custom age threshold
```

## Related Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) - Overall bd architecture
- [CLI_REFERENCE.md](CLI_REFERENCE.md) - Complete command reference
- [../CLAUDE.md](../CLAUDE.md) - Quick reference for agents
