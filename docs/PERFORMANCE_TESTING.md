# Performance Testing Guide

This guide covers beads' performance testing framework, including running benchmarks, profiling, and diagnosing performance issues.

## Overview

The beads performance testing framework provides:

- **Benchmarks**: Measure operation speed on 10K-20K issue databases
- **CPU Profiling**: Automatic profiling during benchmarks with flamegraph support
- **User Diagnostics**: `bd doctor --perf` for end-user performance analysis
- **Database Caching**: One-time generation of test databases, reused across runs

Performance issues typically only manifest at scale (10K+ issues), so benchmarks focus on large databases.

## Running Benchmarks

### Full Benchmark Suite

```bash
make bench
```

This runs all benchmarks with:
- 1 second per benchmark (`-benchtime=1s`)
- 10K and 20K issue databases
- Automatic CPU profiling
- 30 minute timeout

Output includes:
- `ns/op` - Nanoseconds per operation
- `allocs/op` - Memory allocations per operation
- Profile file path

### Quick Benchmarks

For faster iteration during development:

```bash
make bench-quick
```

Uses shorter benchmark time (100ms) for quicker feedback.

### Running Specific Benchmarks

```bash
# Run only GetReadyWork benchmarks
go test -bench=BenchmarkGetReadyWork -benchtime=1s -tags=bench -run=^$ ./internal/storage/dolt/

# Run only Large (10K) benchmarks
go test -bench=Large -benchtime=1s -tags=bench -run=^$ ./internal/storage/dolt/
```

### Understanding Benchmark Output

```
BenchmarkGetReadyWork_Large-8    	    1234	    812345 ns/op	   12345 B/op	     123 allocs/op
```

| Field | Meaning |
|-------|---------|
| `-8` | Number of CPU cores used |
| `1234` | Number of iterations run |
| `812345 ns/op` | ~0.8ms per operation |
| `12345 B/op` | Bytes allocated per operation |
| `123 allocs/op` | Heap allocations per operation |

**Performance Targets:**
- `GetReadyWork`: < 50ms on 20K database
- `SearchIssues`: < 100ms on 20K database
- `CreateIssue`: < 10ms

## Profiling and Analysis

### CPU Profiling

Benchmarks automatically generate CPU profiles:

```bash
# Run benchmarks (generates profile)
make bench

# View profile in browser (flamegraph)
cd internal/storage/sqlite
go tool pprof -http=:8080 bench-cpu-*.prof
```

This opens an interactive web UI at `http://localhost:8080` with:
- **Flamegraph**: Visual call stack (wider = more time)
- **Graph**: Call graph with time percentages
- **Top**: Functions by CPU time

### Reading Flamegraphs

1. **Width = Time**: Wider bars consume more CPU time
2. **Stacks grow upward**: Callees above callers
3. **Look for wide bars**: These are optimization targets
4. **Click to zoom**: Focus on specific call stacks

Common hotspots in database operations:
- SQL query execution
- JSON encoding/decoding
- Memory allocations

### Memory Profiling

For memory-focused analysis:

```bash
# Generate memory profile
go test -bench=BenchmarkGetReadyWork_Large -benchtime=1s -tags=bench -run=^$ \
    -memprofile=mem.prof ./internal/storage/dolt/

# View memory profile
go tool pprof -http=:8080 mem.prof
```

Look for:
- Large allocation sizes
- Frequent small allocations
- Retained memory that should be freed

## User Diagnostics

### Using `bd doctor --perf`

End users can run performance diagnostics:

```bash
bd doctor --perf
```

This:
1. Measures time for common operations
2. Generates a CPU profile
3. Reports any performance issues
4. Provides the profile file path for sharing

### Sharing Profiles with Bug Reports

When reporting performance issues:

1. Run `bd doctor --perf`
2. Note the profile file path from output
3. Attach the `.prof` file to the bug report
4. Include the diagnostic output

### Understanding the Report

```
Performance Diagnostics
=======================
Database size: 15,234 issues
GetReadyWork:  45ms  [OK]
SearchIssues:  78ms  [OK]
CreateIssue:   8ms   [OK]

CPU profile saved: beads-perf-2024-01-15-143022.prof
```

Status indicators:
- `[OK]` - Within acceptable range
- `[SLOW]` - Slower than expected, may need investigation
- `[CRITICAL]` - Significantly degraded, likely a bug

## Comparing Performance

### Using benchstat

Install benchstat:

```bash
go install golang.org/x/perf/cmd/benchstat@latest
```

Compare before/after:

```bash
# Save baseline
go test -bench=. -count=5 -tags=bench -run=^$ ./internal/storage/dolt/ > old.txt

# Make changes, then run again
go test -bench=. -count=5 -tags=bench -run=^$ ./internal/storage/dolt/ > new.txt

# Compare
benchstat old.txt new.txt
```

Output shows:
- Performance change percentage
- Statistical significance (p-value)
- Confidence that change is real

Example:
```
name                    old time/op  new time/op  delta
GetReadyWork_Large-8    812µs ± 2%   654µs ± 1%  -19.46%  (p=0.000 n=5+5)
```

### Detecting Regressions

A change is significant if:
- Delta is > 5%
- p-value is < 0.05
- Consistent across multiple runs (`-count=5` or more)

## Tips for Optimization

### When to Profile vs Benchmark

| Use Case | Tool |
|----------|------|
| "Is this fast enough?" | Benchmark |
| "Why is this slow?" | Profile |
| "Did my change help?" | benchstat |
| "User reports slowness" | `bd doctor --perf` |

### Common Optimization Patterns

1. **Reduce allocations**: Reuse buffers, avoid `append` in loops
2. **Batch database operations**: One query instead of N
3. **Use indexes**: Ensure queries hit database indexes
4. **Avoid N+1 queries**: Fetch related data in single query
5. **Cache computed values**: Store results that don't change

### Optimization Workflow

1. **Measure first**: Get baseline numbers
2. **Profile**: Identify the actual hotspot
3. **Optimize**: Make targeted changes
4. **Verify**: Confirm improvement with benchstat
5. **Test**: Ensure correctness wasn't sacrificed

### Database-Specific Tips

- Check `EXPLAIN QUERY PLAN` for slow queries
- Ensure indexes exist for filtered columns
- Consider `PRAGMA optimize` after large imports
- Watch for table scans on large tables

## Cleaning Up

Remove benchmark artifacts:

```bash
make clean
```

This removes:
- CPU profile files (`bench-cpu-*.prof`)
- User diagnostic profiles (`beads-perf-*.prof`)
- Cached benchmark databases are in `/tmp/beads-bench-cache/`

To clear cached databases:

```bash
rm -rf /tmp/beads-bench-cache/
```

## See Also

- [TESTING.md](TESTING.md) - General testing guide
- [ARCHITECTURE.md](ARCHITECTURE.md) - System architecture
- [INTERNALS.md](INTERNALS.md) - Implementation details
