---
title: Hash-based IDs
description: Why beads uses collision-resistant hash IDs like bd-a1b2 so agents and branches never clash
---

Understanding beads' collision-resistant ID system.

## The Problem

Traditional sequential IDs (`#1`, `#2`, `#3`) break when:
- Multiple agents create issues simultaneously
- Different branches have independent numbering
- Forks diverge and later merge

```mermaid
flowchart TD
    subgraph seq["Sequential IDs — collision on merge"]
        a1["agent A creates #7"] --> m1["merge: two different #7s ✗"]
        b1["agent B creates #7"] --> m1
    end
    subgraph hash["Hash IDs — no coordination needed"]
        a2["agent A creates bd-a1b2"] --> m2["merge: both IDs survive ✓"]
        b2["agent B creates bd-f14c"] --> m2
    end
```

## The Solution

Beads uses hash-based IDs:

```
bd-a1b2c3    # Short hash
bd-f14c      # Even shorter
bd-a3f8e9.1  # Hierarchical (child of bd-a3f8e9)
```

**Properties:**
- Globally unique (content-based hash)
- No coordination needed between creators
- Merge-friendly across branches
- Predictable length (configurable)

## How Hashes Work

IDs are generated from:
- Issue title
- Creation timestamp
- Random salt

```bash
# Create issue - ID assigned automatically
bd create "Fix authentication bug"
# Returns: bd-7x2f

# The ID is deterministic for same content+timestamp
```

## Hierarchical IDs

For epics and subtasks:

```bash
# Parent epic
bd create "Auth System" -t epic
# Returns: bd-a3f8e9

# Children auto-increment
bd create "Design UI" --parent bd-a3f8e9    # bd-a3f8e9.1
bd create "Backend" --parent bd-a3f8e9      # bd-a3f8e9.2
bd create "Tests" --parent bd-a3f8e9        # bd-a3f8e9.3
```

Benefits:
- Clear parent-child relationship
- No namespace collision (parent hash is unique)
- Up to 3 levels of nesting

## ID Configuration

Configure ID prefix and length:

```bash
# Set prefix (default: bd)
bd config set id.prefix myproject

# Set hash length (default: 4)
bd config set id.hash_length 6

# New issues use new format
bd create "Test"
# Returns: myproject-a1b2c3
```

## Collision Handling

While rare, collisions are handled automatically:

1. On import, if hash collision detected
2. Beads appends disambiguator
3. Both issues preserved

```bash
# Check for collisions
bd info --schema --json | jq '.collision_count'
```

## Working with IDs

```bash
# Partial ID matching
bd show a1b2     # Finds bd-a1b2...
bd show auth     # Fuzzy match by title

# Full ID required for ambiguous cases
bd show bd-a1b2c3d4

# List with full IDs
bd list --full-ids
```

### `bd delete` requires the full id

Every command above resolves an abbreviation the same way, with one deliberate
exception: `bd delete` accepts only a full, exact id, never an abbreviation. An
abbreviation that happens to be a leading-prefix match for the wrong issue would
otherwise land the deletion there, and a deletion cannot be undone.

The wrong match is easy to reach by accident. The id of an issue that is
already gone is still the start of other ids — its child's (`bd-a3f8e9.1`
after `bd-a3f8e9` is deleted), or a longer hash that happens to begin with it —
so retrying a delete or re-running a `--from-file` list would otherwise reach an
issue nobody named. The refusal says delete needs the full id and names the ids
the input matches.

Reads and every other mutation (`show`, `close`, `update`, …) keep accepting
abbreviations as documented above.

```bash
bd show a1b2              # OK — abbreviation resolves for reads
bd close a1b2              # OK — abbreviation resolves for other writes
bd delete a1b2             # refused — deletes require the full id
bd delete bd-a1b2c3d4      # OK — previews the deletion; add --force to delete
```

## Migration from Sequential IDs

If migrating from a system with sequential IDs:

```bash
# Bootstrap from a JSONL export (preserves original IDs in metadata)
bd init --from-jsonl old-issues.jsonl

# View original ID
bd show bd-new --json | jq '.original_id'
```

## Best Practices

1. **Use short references** - `bd-a1b2` is usually unique enough, except on
   `bd delete`, which requires the full id (see
   [Working with IDs](#working-with-ids) above)
2. **Use `--json` for scripts** - Parse full ID programmatically
3. **Reference by hash in commits** - `Fixed bd-a1b2` in commit messages
4. **Let hierarchies form naturally** - Create epics, add children as needed
