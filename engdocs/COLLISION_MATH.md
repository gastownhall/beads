# Hash ID Collision Mathematics

This document explains the collision probability calculations for beads' adaptive hash-based IDs and the thresholds used for automatic length scaling.

## Birthday Paradox Formula

The collision probability for hash IDs is calculated using the birthday paradox:

```
P(collision) ≈ 1 - e^(-n²/2N)
```

Where:
- **n** = number of issues in database
- **N** = total possible IDs = 36^length (lowercase alphanumeric: `[a-z0-9]`)

## Collision Probability Table

| DB Size | 4-char | 5-char | 6-char | 7-char | 8-char |
|---------|--------|--------|--------|--------|--------|
| 50      | 0.07%  | 0.00%  | 0.00%  | 0.00%  | 0.00%  |
| 100     | 0.30%  | 0.01%  | 0.00%  | 0.00%  | 0.00%  |
| 200     | 1.18%  | 0.03%  | 0.00%  | 0.00%  | 0.00%  |
| 500     | 7.17%  | 0.21%  | 0.01%  | 0.00%  | 0.00%  |
| 1,000   | 25.75% | 0.82%  | 0.02%  | 0.00%  | 0.00%  |
| 2,000   | 69.60% | 3.25%  | 0.09%  | 0.00%  | 0.00%  |
| 5,000   | 99.94% | 18.68% | 0.57%  | 0.02%  | 0.00%  |
| 10,000  | 100%   | 56.26% | 2.27%  | 0.06%  | 0.00%  |

### Key Insights

- **4-char IDs** are safe up to ~500 issues (7% collision risk)
- **5-char IDs** are safe up to ~1,500 issues (2% collision risk)
- **6-char IDs** are safe up to ~10,000 issues (2% collision risk)
- **7-char IDs** support 100,000+ issues with negligible collision risk
- **8-char IDs** support millions of issues

## Expected Number of Collisions

This shows the average number of actual hash collisions you'll encounter:

| DB Size | 4-char | 5-char | 6-char | 7-char | 8-char |
|---------|--------|--------|--------|--------|--------|
| 100     | 0.00   | 0.00   | 0.00   | 0.00   | 0.00   |
| 500     | 0.07   | 0.00   | 0.00   | 0.00   | 0.00   |
| 1,000   | 0.30   | 0.01   | 0.00   | 0.00   | 0.00   |
| 2,000   | 1.19   | 0.03   | 0.00   | 0.00   | 0.00   |
| 5,000   | 7.44   | 0.21   | 0.01   | 0.00   | 0.00   |
| 10,000  | 29.77  | 0.83   | 0.02   | 0.00   | 0.00   |

**Example:** With 5,000 issues using 4-char IDs, you'll likely see ~7 hash collisions (automatically retried with +1 nonce).

## Adaptive Scaling Strategy

Beads automatically increases ID length when the collision probability exceeds **25%** (configurable via `max_collision_prob`).

### Default Thresholds (25% max collision)

| Database Size | ID Length | Collision Probability at Max |
|---------------|-----------|------------------------------|
| 0-500         | 4 chars   | 7.17% at 500 issues          |
| 501-1,500     | 5 chars   | 1.84% at 1,500 issues        |
| 1,501-5,000   | 5 chars   | 18.68% at 5,000 issues       |
| 5,001-15,000  | 6 chars   | 5.04% at 15,000 issues       |
| 15,001+       | continues scaling as needed   |

### Why 25%?

The 25% threshold balances:
- **Readability:** Keep IDs short for small databases
- **Safety:** Avoid frequent collision retries
- **Scalability:** Grow gracefully as database expands

Even at 25% collision *probability*, the *expected number* of actual collisions is low (< 1 collision per 1,000 issues created).

## Alternative Thresholds

You can customize the threshold with `bd config set max_collision_prob <value>`:

### Conservative (10% threshold)

| DB Size | ID Length |
|---------|-----------|
| 0-200   | 4 chars   |
| 201-1,000 | 5 chars |
| 1,001-5,000 | 6 chars |
| 5,001+ | continues scaling |

### Aggressive (50% threshold)

| DB Size | ID Length |
|---------|-----------|
| 0-500   | 4 chars   |
| 501-2,000 | 5 chars |
| 2,001-10,000 | 6 chars |
| 10,001+ | continues scaling |

## Collision Resolution

When a hash collision occurs (same ID generated twice), beads automatically:

1. Tries base length with different nonce (10 attempts)
2. Tries base+1 length with different nonce (10 attempts)
3. Tries base+2 length with different nonce (10 attempts)

**Total: 30 attempts** before failing (astronomically unlikely).

Example with 4-char base:
- `bd-a3f2` (nonce 0) - collision!
- `bd-a3f2` (nonce 1) - collision again!
- `bd-b7d4` (nonce 2) - success! ✓

## Mathematical Properties

### ID Space Size

| Length | Possible IDs | Notation |
|--------|--------------|----------|
| 3 chars | 46,656      | 36³      |
| 4 chars | 1,679,616   | 36⁴ ≈ 1.7M |
| 5 chars | 60,466,176  | 36⁵ ≈ 60M  |
| 6 chars | 2,176,782,336 | 36⁶ ≈ 2.2B |
| 7 chars | 78,364,164,096 | 36⁷ ≈ 78B |
| 8 chars | 2,821,109,907,456 | 36⁸ ≈ 2.8T |

### Why Alphanumeric?

Using `[a-z0-9]` (36 characters) instead of hex (16 characters):
- **4-char alphanumeric** ≈ **6-char hex** in capacity
- More readable: `bd-a3f2` vs `bd-a3f2e1`
- Easier to type and communicate

## Verification

Run the collision calculator yourself:

```bash
go run scripts/collision-calculator.go
```

This generates the tables above and shows adaptive scaling strategy for any threshold.

## Related Documentation

- [ADAPTIVE_IDS.md](ADAPTIVE_IDS.md) - Configuration and usage guide
- [CONFIG.md](CONFIG.md) - All configuration options
