# Molecules and Wisps Reference

This reference covers bd's molecular chemistry system for reusable work templates and ephemeral workflows.

## The Chemistry Metaphor

bd v0.34.0 introduces a chemistry-inspired workflow system:

| Phase | Name | Storage | Synced? | Use Case |
|-------|------|---------|---------|----------|
| **Solid** | Proto | `.beads/` | Yes | Reusable template (epic with `template` label) |
| **Liquid** | Mol | `.beads/` | Yes | Persistent instance (real issues from template) |
| **Vapor** | Wisp | `.beads-wisp/` | No | Ephemeral instance (operational work, no audit trail) |

**Phase transitions:**
- `spawn` / `pour`: Solid (proto) → Liquid (mol)
- `wisp create`: Solid (proto) → Vapor (wisp)
- `squash`: Vapor (wisp) → Digest (permanent summary)
- `burn`: Vapor (wisp) → Nothing (deleted, no trace)
- `distill`: Liquid (ad-hoc epic) → Solid (proto)

## When to Use Molecules

### Use Protos/Mols When:
- **Repeatable patterns** - Same workflow structure used multiple times (releases, reviews, onboarding)
- **Team knowledge capture** - Encoding tribal knowledge as executable templates
- **Audit trail matters** - Work that needs to be tracked and reviewed later
- **Cross-session persistence** - Work spanning multiple days/sessions

### Use Wisps When:
- **Operational loops** - Patrol cycles, health checks, routine monitoring
- **One-shot orchestration** - Temporary coordination that shouldn't clutter history
- **Diagnostic runs** - Debugging workflows with no archival value
- **High-frequency ephemeral work** - Would create noise in permanent database

**Key insight:** Wisps prevent database bloat from routine operations while still providing structure during execution.

---

## Proto Management

### Creating a Proto

Protos are epics with the `template` label. Create manually or distill from existing work:

```bash
# Manual creation
bd create "Release Workflow" --type epic --label template
bd create "Run tests for {{component}}" --type task
bd dep add task-id epic-id --type parent-child

# Distill from ad-hoc work (extracts template from existing epic)
bd mol distill bd-abc123 --as "Release Workflow" --var version=1.0.0
```

**Proto naming convention:** Use `mol-` prefix for clarity (e.g., `mol-release`, `mol-patrol`).

### Listing Protos

```bash
bd mol catalog                  # List all protos
bd mol catalog --json           # Machine-readable
```

### Viewing Proto Structure

```bash
bd mol show mol-release         # Show template structure and variables
bd mol show mol-release --json  # Machine-readable
```

---

## Spawning Molecules

### Basic Spawn (Creates Wisp by Default)

```bash
bd mol spawn mol-patrol                    # Creates wisp (ephemeral)
bd mol spawn mol-feature --pour            # Creates mol (persistent)
bd mol spawn mol-release --var version=2.0 # With variable substitution
```

**Chemistry shortcuts:**
```bash
bd pour mol-feature                        # Shortcut for spawn --pour
bd wisp create mol-patrol                  # Explicit wisp creation
```

### Spawn with Immediate Execution

```bash
bd mol run mol-release --var version=2.0
```

`bd mol run` does three things:
1. Spawns the molecule (persistent)
2. Assigns root issue to caller
3. Pins root issue for session recovery

**Use `mol run` when:** Starting durable work that should survive crashes. The pin ensures `bd ready` shows the work after restart.

### Spawn with Attachments

Attach additional protos in a single command:

```bash
bd mol spawn mol-feature --attach mol-testing --var name=auth
# Spawns mol-feature, then spawns mol-testing and bonds them
```

**Attach types:**
- `sequential` (default) - Attached runs after primary completes
- `parallel` - Attached runs alongside primary
- `conditional` - Attached runs only if primary fails

```bash
bd mol spawn mol-deploy --attach mol-rollback --attach-type conditional
```

---

## Bonding Molecules

### Bond Types

```bash
bd mol bond A B                    # Sequential: B runs after A
bd mol bond A B --type parallel    # Parallel: B runs alongside A
bd mol bond A B --type conditional # Conditional: B runs if A fails
```

### Operand Combinations

| A | B | Result |
|---|---|--------|
| proto | proto | Compound proto (reusable template) |
| proto | mol | Spawn proto, attach to molecule |
| mol | proto | Spawn proto, attach to molecule |
| mol | mol | Join into compound molecule |

### Phase Control in Bonds

By default, spawned protos inherit target's phase. Override with flags:

```bash
# Found bug during wisp patrol? Persist it:
bd mol bond mol-critical-bug wisp-patrol --pour

# Need ephemeral diagnostic on persistent feature?
bd mol bond mol-temp-check bd-feature --wisp
```

### Custom Compound Names

```bash
bd mol bond mol-feature mol-deploy --as "Feature with Deploy"
```

---

## Wisp Lifecycle

### Creating Wisps

```bash
bd wisp create mol-patrol                    # From proto
bd mol spawn mol-patrol                      # Same (spawn defaults to wisp)
bd mol spawn mol-check --var target=db       # With variables
```

### Listing Wisps

```bash
bd wisp list                    # List all wisps
bd wisp list --json             # Machine-readable
```

### Ending Wisps

**Option 1: Squash (compress to digest)**
```bash
bd mol squash wisp-abc123                              # Auto-generate summary
bd mol squash wisp-abc123 --summary "Completed patrol" # Agent-provided summary
bd mol squash wisp-abc123 --keep-children              # Keep children, just create digest
bd mol squash wisp-abc123 --dry-run                    # Preview
```

Squash creates a permanent digest issue summarizing the wisp's work, then deletes the wisp children.

**Option 2: Burn (delete without trace)**
```bash
bd mol burn wisp-abc123                    # Delete wisp, no digest
```

Use burn for routine work with no archival value.

### Garbage Collection

```bash
bd wisp gc                      # Clean up orphaned wisps
```

---

## Distilling Protos

Extract a reusable template from ad-hoc work:

```bash
bd mol distill bd-o5xe --as "Release Workflow"
bd mol distill bd-abc --var feature_name=auth-refactor --var version=1.0.0
```

**What distill does:**
1. Loads existing epic and all children
2. Clones structure as new proto (adds `template` label)
3. Replaces concrete values with `{{variable}}` placeholders

**Variable syntax (both work):**
```bash
--var branch=feature-auth      # variable=value (recommended)
--var feature-auth=branch      # value=variable (auto-detected)
```

**Use cases:**
- Team develops good workflow organically, wants to reuse it
- Capture tribal knowledge as executable templates
- Create starting point for similar future work

---

## Cross-Project Dependencies

### Concept

Projects can depend on capabilities shipped by other projects:

```bash
# Project A ships a capability
bd ship auth-api                # Marks capability as available

# Project B depends on it
bd dep add bd-123 external:project-a:auth-api
```

### Shipping Capabilities

```bash
bd ship <capability>            # Ship capability (requires closed issue)
bd ship <capability> --force    # Ship even if issue not closed
bd ship <capability> --dry-run  # Preview
```

**How it works:**
1. Find issue with `export:<capability>` label
2. Validate issue is closed
3. Add `provides:<capability>` label

### Depending on External Capabilities

```bash
bd dep add <issue> external:<project>:<capability>
```

The dependency is satisfied when the external project has a closed issue with `provides:<capability>` label.

**`bd ready` respects external deps:** Issues blocked by unsatisfied external dependencies won't appear in ready list.

---

## Common Patterns

### Pattern: Weekly Review Proto

```bash
# Create proto
bd create "Weekly Review" --type epic --label template
bd create "Review open issues" --type task
bd create "Update priorities" --type task
bd create "Archive stale work" --type task
# Link as children...

# Use each week
bd mol spawn mol-weekly-review --pour
```

### Pattern: Ephemeral Patrol Cycle

```bash
# Patrol proto exists
bd wisp create mol-patrol

# Execute patrol work...

# End patrol
bd mol squash wisp-abc123 --summary "Patrol complete: 3 issues found, 2 resolved"
```

### Pattern: Feature with Rollback

```bash
bd mol spawn mol-deploy --attach mol-rollback --attach-type conditional
# If deploy fails, rollback automatically becomes unblocked
```

### Pattern: Capture Tribal Knowledge

```bash
# After completing a good workflow organically
bd mol distill bd-release-epic --as "Release Process" --var version=X.Y.Z
# Now team can: bd mol spawn mol-release-process --var version=2.0.0
```

---

## CLI Quick Reference

| Command | Purpose |
|---------|---------|
| `bd mol catalog` | List available protos |
| `bd mol show <id>` | Show proto/mol structure |
| `bd mol spawn <proto>` | Create wisp from proto (default) |
| `bd mol spawn <proto> --pour` | Create persistent mol from proto |
| `bd mol run <proto>` | Spawn + assign + pin (durable execution) |
| `bd mol bond <A> <B>` | Combine protos or molecules |
| `bd mol distill <epic>` | Extract proto from ad-hoc work |
| `bd mol squash <mol>` | Compress wisp children to digest |
| `bd mol burn <wisp>` | Delete wisp without trace |
| `bd pour <proto>` | Shortcut for `spawn --pour` |
| `bd wisp create <proto>` | Create ephemeral wisp |
| `bd wisp list` | List all wisps |
| `bd wisp gc` | Garbage collect orphaned wisps |
| `bd ship <capability>` | Publish capability for cross-project deps |

---

## Troubleshooting

**"Proto not found"**
- Check `bd mol catalog` for available protos
- Protos need `template` label on the epic

**"Variable not substituted"**
- Use `--var key=value` syntax
- Check proto for `{{key}}` placeholders with `bd mol show`

**"Wisp commands fail"**
- Wisps stored in `.beads-wisp/` (separate from `.beads/`)
- Check `bd wisp list` for active wisps

**"External dependency not satisfied"**
- Target project must have closed issue with `provides:<capability>` label
- Use `bd ship <capability>` in target project first
