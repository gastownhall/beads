# Molecules: Work Graphs in Beads

This doc explains how beads structures and executes work. Start here if you're building workflows.

## TL;DR

1. **Work = issues with dependencies.** That's it. No special types needed.
2. **Dependencies control execution.** `blocks` = sequential. No dep = parallel.
3. **Molecules are just epics.** Any epic with children is a molecule. Templates are optional.
4. **Bonding = adding dependencies.** `bd mol bond A B` creates a dependency between work graphs.
5. **Agents execute until blocked.** When all ready work is done, the workflow is complete.

## The Execution Model

### How Work Flows

An agent picks up a molecule (epic with children). They execute ready children in parallel until everything closes:

```
epic-root (assigned to agent)
├── child.1 (no deps → ready)      ← execute in parallel
├── child.2 (no deps → ready)      ← execute in parallel
├── child.3 (needs child.1) → blocked until child.1 closes
└── child.4 (needs child.2, child.3) → blocked until both close
```

**Ready work:** `bd ready` shows issues with no open blockers.
**Blocked work:** `bd blocked` shows what's waiting.

### Dependency Types That Block

| Type | Semantics | Use Case |
|------|-----------|----------|
| `blocks` | B can't start until A closes | Sequencing work |
| `parent-child` | If parent blocked, children blocked | Hierarchy (children parallel by default) |
| `conditional-blocks` | B runs only if A fails | Error handling paths |
| `waits-for` | B waits for all of A's children | Fanout gates |

**Non-blocking types:** `related`, `discovered-from`, `replies-to` - these link issues without affecting execution.

### Default Parallelism

**Children are parallel by default.** Only explicit dependencies create sequence:

```bash
# These three tasks run in PARALLEL (no deps between them)
bd create "Task A" -t task
bd create "Task B" -t task
bd create "Task C" -t task

# Add dependency to make B wait for A
bd dep add <B-id> <A-id>   # B depends on A (B needs A)
```

### Multi-Day Execution

An agent works through a molecule by:
1. Getting ready work (`bd ready`)
2. Claiming it (`bd update <id> --status in_progress`)
3. Doing the work
4. Closing it (`bd close <id>`)
5. Repeat until molecule is done

If the molecule is blocked by another molecule:
- Agent either waits, or
- Agent continues into the blocking molecule (compound execution)

**Bonding enables compound execution:** When you bond molecule A to molecule B, the agent can traverse both as one logical unit of work.

## Molecules vs Epics

**They're the same thing.** A molecule is just an epic (parent + children) with workflow semantics.

| Term | Meaning | When to Use |
|------|---------|-------------|
| **Epic** | Parent issue with children | General term for hierarchical work |
| **Molecule** | Epic with execution intent | When discussing workflow traversal |
| **Proto** | Epic with `template` label | Reusable pattern (optional) |

You can create molecules without protos - just create an epic and add children:

```bash
bd create "Feature X" -t epic
bd create "Design" -t task --parent <epic-id>
bd create "Implement" -t task --parent <epic-id>
bd create "Test" -t task --parent <epic-id>
bd dep add <implement-id> <design-id>   # implement needs design
bd dep add <test-id> <implement-id>      # test needs implement
```

## Bonding: Connecting Work Graphs

**Bond = create a dependency between two work graphs.**

```bash
bd mol bond A B                    # B depends on A (sequential by default)
bd mol bond A B --type parallel    # Organizational link, no blocking
bd mol bond A B --type conditional # B runs only if A fails
```

### What Bonding Does

| Operands | What Happens |
|----------|--------------|
| epic + epic | Creates dependency edge between them |
| proto + epic | Spawns proto as new issues, attaches to epic |
| proto + proto | Creates compound template |

### The Key Insight

**Bonding lets agents traverse compound workflows.** When A blocks B:
- Completing A unblocks B
- Agent can continue from A into B seamlessly
- The compound work graph can span days

This is how orchestrators run autonomous workflows - agents follow the dependency graph, handing off between sessions, until all work closes.

## Phase Metaphor (Templates)

For reusable workflows, beads uses a chemistry metaphor:

| Phase | Name | Storage | Synced | Purpose |
|-------|------|---------|--------|---------|
| **Solid** | Proto | `.beads/` | Yes | Frozen template |
| **Liquid** | Mol | `.beads/` | Yes | Active persistent work |
| **Vapor** | Wisp | `.beads/` (Wisp=true) | No | Ephemeral operations |

### Phase Commands

```bash
bd mol pour <proto>              # Proto → Mol (persistent instance)
bd mol wisp <proto>              # Proto → Wisp (ephemeral instance)
bd mol squash <id>               # Mol/Wisp → Digest (permanent record)
bd mol burn <id>                 # Wisp → nothing (discard)
```

### When to Use Each Phase

| Use Case | Phase | Why |
|----------|-------|-----|
| Feature work | Mol (pour) | Persists across sessions, audit trail |
| Patrol cycles | Wisp | Routine, no audit value |
| One-shot ops | Wisp | Scaffolding, not the work itself |
| Important discovery during wisp | Mol (--pour) | "This matters, save it" |

## Common Patterns

### Sequential Pipeline

```bash
bd create "Pipeline" -t epic
bd create "Step 1" -t task --parent <pipeline>
bd create "Step 2" -t task --parent <pipeline>
bd create "Step 3" -t task --parent <pipeline>
bd dep add <step2> <step1>
bd dep add <step3> <step2>
```

### Parallel Fanout with Gate

```bash
bd create "Process files" -t epic
bd create "File A" -t task --parent <epic>
bd create "File B" -t task --parent <epic>
bd create "File C" -t task --parent <epic>
bd create "Aggregate" -t task --parent <epic>
# Aggregate needs all three (waits-for gate)
bd dep add <aggregate> <fileA> --type waits-for
```

### Dynamic Bonding (Christmas Ornament)

When the number of children isn't known until runtime:

```bash
# In a survey step, discover polecats and bond arms dynamically
for polecat in $(gt polecat list); do
  bd mol bond mol-polecat-arm $PATROL_ID --ref arm-$polecat --var name=$polecat
done
```

Creates:
```
patrol-x7k (wisp)
├── preflight
├── survey-workers
│   ├── patrol-x7k.arm-ace (dynamically bonded)
│   ├── patrol-x7k.arm-nux (dynamically bonded)
│   └── patrol-x7k.arm-toast (dynamically bonded)
└── aggregate (waits for all arms)
```

## Agent Pitfalls

### 1. Temporal Language Inverts Dependencies

**Wrong:** "Phase 1 comes before Phase 2" → `bd dep add phase1 phase2`
**Right:** "Phase 2 needs Phase 1" → `bd dep add phase2 phase1`

Use requirement language. Verify with `bd blocked`.

### 2. Assuming Order = Sequence

Numbered steps don't create sequence. Dependencies do:

```bash
# These run in PARALLEL despite names
bd create "Step 1" ...
bd create "Step 2" ...
bd create "Step 3" ...

# Add deps to sequence them
bd dep add step2 step1
bd dep add step3 step2
```

### 3. Forgetting to Close Work

Blocked issues stay blocked forever if their blockers aren't closed. Always close completed work:

```bash
bd close <id> --reason "Done"
```

### 4. Orphaned Wisps

Wisps accumulate if not squashed/burned:

```bash
bd mol wisp list        # Check for orphans
bd mol squash <id>      # Create digest
bd mol burn <id>        # Or discard
bd mol wisp gc          # Garbage collect old wisps
```

## Layer Cake Architecture

For reference, here's how the layers stack:

```
Formulas (JSON compile-time macros)      ← optional, for complex composition
    ↓
Protos (template issues)                  ← optional, for reusable patterns
    ↓
Molecules (bond, squash, burn)            ← workflow operations
    ↓
Epics (parent-child, dependencies)        ← DATA PLANE (the core)
    ↓
Issues (JSONL, git-backed)                ← STORAGE
```

**Most users only need the bottom two layers.** Protos and formulas are for reusable patterns and complex composition.

## Commands Quick Reference

### Execution

```bash
bd ready                         # What's ready to work
bd blocked                       # What's blocked
bd update <id> --status in_progress
bd close <id>
```

### Dependencies

```bash
bd dep add <issue> <depends-on>  # issue needs depends-on
bd dep tree <id>                 # Show dependency tree
```

### Molecules

```bash
bd mol pour <proto> --var k=v    # Template → persistent mol
bd mol wisp <proto>              # Template → ephemeral wisp
bd mol bond A B                  # Connect work graphs
bd mol squash <id>               # Compress to digest
bd mol burn <id>                 # Discard without record
```

## Related Docs

- [CLI_REFERENCE.md](CLI_REFERENCE.md) - Full command reference
- [ARCHITECTURE.md](ARCHITECTURE.md) - System internals
- [../CLAUDE.md](../CLAUDE.md) - Quick agent reference
