# Adaptive ID Length

**Feature:** bd-ea2a13  
**Status:** Implemented (v0.21+)

## Overview

Beads uses adaptive hash ID lengths that automatically scale based on database size, optimizing for readability in small databases while preventing collisions as databases grow.

## Motivation

- **Small databases** (0-500 issues): Very short, readable IDs like `bd-a3f2` (4 chars)
- **Medium databases** (500-1500 issues): Slightly longer IDs like `bd-7f3a8` (5 chars)
- **Large databases** (1500+ issues): Standard IDs like `bd-7f3a86` (6 chars)

Users who actively archive old issues can keep their IDs shorter over time.

## How It Works

### Birthday Paradox Math

The collision probability is calculated using:

```
P(collision) ≈ 1 - e^(-n²/2N)
```

Where:
- `n` = number of issues in database
- `N` = total possible IDs (36^length for lowercase alphanumeric)

### Default Thresholds (25% max collision)

| Database Size | ID Length | Collision Probability |
|--------------|-----------|----------------------|
| 0-500        | 4 chars   | ~7% at 500           |
| 501-1500     | 5 chars   | ~2% at 1500          |
| 1501+        | 6 chars   | continues scaling    |

### Collision Resolution

If a collision occurs (rare), the algorithm automatically tries:
1. Base length (e.g., 4 chars)
2. Base + 1 (e.g., 5 chars)
3. Base + 2 (e.g., 6 chars)

With 10 nonces per length, giving 30 attempts total.

## Configuration

Adaptive ID length is automatically enabled when using `id_mode=hash`. You can customize the behavior:

### Max Collision Probability

Default: 25% (0.25)

```bash
# More lenient (allow up to 50% collision probability)
bd config set max_collision_prob "0.50"

# Stricter (only allow 1% collision probability)
bd config set max_collision_prob "0.01"
```

### Minimum Hash Length

Default: 4 chars

```bash
# Start with 5-char IDs minimum
bd config set min_hash_length "5"

# Very short IDs (use with caution)
bd config set min_hash_length "3"
```

### Maximum Hash Length

Default: 8 chars

```bash
# Allow even longer IDs for huge databases
bd config set max_hash_length "10"
```

## Examples

### Default Configuration

```bash
# Initialize with hash IDs
bd init --id-mode hash --prefix myproject

# First 500 issues get 4-char IDs
bd create "Fix bug" -p 1
# → myproject-a3f2

# After 1000 issues, switches to 5-char IDs
bd create "Add feature" -p 1
# → myproject-7f3a8c

# At 10,000 issues, uses 6-char IDs
bd create "Refactor" -p 1
# → myproject-b9d1e4
```

### Custom Configuration

```bash
# Very strict collision tolerance
bd config set max_collision_prob "0.01"

# With 1% threshold and 100 issues, uses 4-char IDs
# (collision probability is ~0.3% with 4 chars)

# Force minimum 5-char IDs for consistency
bd config set min_hash_length "5"

# All IDs will be at least 5 chars now
bd create "Task" -p 1
# → myproject-7f3a8
```

## Collision Probability Table

Use `scripts/collision-calculator.go` to explore collision probabilities:

```bash
go run scripts/collision-calculator.go
```

Output shows:
- Collision probabilities for different database sizes and ID lengths
- Recommended ID lengths for different thresholds
- Expected number of collisions
- Adaptive scaling strategy

## Implementation Details

### Location

- Algorithm: `internal/storage/dolt/adaptive_length.go`
- ID generation: `internal/storage/dolt/dolt.go` (`generateHashID`)
- Tests: `internal/storage/dolt/adaptive_length_test.go`
- E2E tests: `internal/storage/dolt/adaptive_e2e_test.go`

### Database Schema

Configuration is stored in the `config` table:

```sql
INSERT INTO config (key, value) VALUES ('max_collision_prob', '0.25');
INSERT INTO config (key, value) VALUES ('min_hash_length', '4');
INSERT INTO config (key, value) VALUES ('max_hash_length', '8');
```

### Performance

- Collision probability calculation: ~10ns per call
- ID generation with adaptive length: ~300ns (same as before)
- Database query to count issues: ~100μs

## Migration

### Existing Databases

Existing databases with 6-char IDs will:
1. Continue using 6-char IDs by default
2. Can opt into adaptive mode by setting config (new IDs will use adaptive length)
3. Old IDs remain unchanged

### Sequential to Hash Migration

When migrating from sequential IDs to hash IDs with `bd migrate --to-hash-ids`:
- Uses adaptive length algorithm for new IDs
- Preserves existing sequential IDs
- References are automatically updated

## Best Practices

1. **Default is good**: The 25% threshold works well for most use cases
2. **Active archival**: Delete closed issues to keep database small and IDs short
3. **Consistency**: Set `min_hash_length` if you want all IDs to be same length
4. **Monitoring**: Run collision calculator periodically to check health

## Future Enhancements

Potential improvements (not yet implemented):

- **Automatic scaling notifications**: Warn when approaching threshold
- **Per-workspace thresholds**: Different configs for different projects
- **Dynamic adjustment**: Auto-adjust threshold based on observed collision rate
- **Compaction-aware**: Don't count compacted issues in collision calculation

## Alternative: Sequential Counter IDs

Adaptive hash IDs are the default, but beads also supports sequential integer IDs
(`bd-1`, `bd-2`, ...) for projects that prefer human-readable numbering.

Counter mode is controlled by the `issue_id_mode` config key:

```bash
# Switch to sequential IDs
bd config set issue_id_mode counter

# Revert to hash IDs (default)
bd config set issue_id_mode hash
```

**Tradeoff:**

- **Hash IDs** (this document): Collision-free across parallel branches and agents; IDs are less predictable but always unique.
- **Counter IDs**: Human-friendly and sequential; require care in multi-branch workflows where counters can diverge.

See [CONFIG.md](CONFIG.md) for full documentation on `issue_id_mode=counter`, including migration
guidance and per-prefix counter isolation.

## Related

- [Migration Guide](../README.md#migration) - Converting from sequential to hash IDs
- [Configuration](CONFIG.md) - All configuration options
