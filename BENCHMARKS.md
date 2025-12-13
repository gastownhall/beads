# Beads Performance Benchmarks

This document describes the performance benchmarks available in the beads project and how to use them.

## Running Benchmarks

### All SQLite Benchmarks
```bash
go test -tags=bench -bench=. -benchmem ./internal/storage/sqlite/...
```

### Specific Benchmark
```bash
go test -tags=bench -bench=BenchmarkGetReadyWork_Large -benchmem ./internal/storage/sqlite/...
```

### With CPU Profiling
```bash
go test -tags=bench -bench=BenchmarkGetReadyWork_Large -cpuprofile=cpu.prof ./internal/storage/sqlite/...
go tool pprof -http=:8080 cpu.prof
```

## Benchmark Categories

### Compaction Operations
- **BenchmarkGetTier1Candidates** - Identify L1 compaction candidates
- **BenchmarkGetTier2Candidates** - Identify L2 compaction candidates
- **BenchmarkCheckEligibility** - Check if issue is eligible for compaction

### Cycle Detection
Tests on graphs with different topologies (linear chains, trees, dense graphs):
- **BenchmarkCycleDetection_Linear_100/1000/5000** - Linear dependency chains
- **BenchmarkCycleDetection_Tree_100/1000** - Tree-structured dependencies
- **BenchmarkCycleDetection_Dense_100/1000** - Dense graphs

### Ready Work / Filtering
- **BenchmarkGetReadyWork_Large** - Filter unblocked issues (10K dataset)
- **BenchmarkGetReadyWork_XLarge** - Filter unblocked issues (20K dataset)
- **BenchmarkGetReadyWork_FromJSONL** - Ready work on JSONL-imported database

### Search Operations
- **BenchmarkSearchIssues_Large_NoFilter** - Search all open issues (10K dataset)
- **BenchmarkSearchIssues_Large_ComplexFilter** - Search with priority/status filters (10K dataset)

### CRUD Operations
- **BenchmarkCreateIssue_Large** - Create new issue in 10K database
- **BenchmarkUpdateIssue_Large** - Update existing issue in 10K database
- **BenchmarkBulkCloseIssues** - Close 100 issues sequentially (NEW)

### Specialized Operations
- **BenchmarkLargeDescription** - Handling 100KB+ issue descriptions (NEW)
- **BenchmarkSyncMerge** - Simulate sync cycle with create/update operations (NEW)

## Performance Targets

### Typical Results (M2 Pro)

| Operation | Time | Memory | Notes |
|-----------|------|--------|-------|
| GetReadyWork (10K) | 30ms | 16.8MB | Filters ~200 open issues |
| Search (10K, no filter) | 12.5ms | 6.3MB | Returns all open issues |
| Cycle Detection (5000 linear) | 70ms | 15KB | Detects transitive deps |
| Create Issue (10K db) | 2.5ms | 8.9KB | Insert into index |
| Update Issue (10K db) | 18ms | 17KB | Status change |
| **Large Description (100KB)** | **3.3ms** | **874KB** | String handling overhead |
| **Bulk Close (100 issues)** | **1.9s** | **1.2MB** | 100 sequential writes |
| **Sync Merge (20 ops)** | **29ms** | **198KB** | Create 10 + update 10 |

## Dataset Caching

Benchmark datasets are cached in `/tmp/beads-bench-cache/`:
- `large.db` - 10,000 issues (16.6 MB)
- `xlarge.db` - 20,000 issues (generated on demand)
- `large-jsonl.db` - 10K issues via JSONL import

Cached databases are reused across runs. To regenerate:
```bash
rm /tmp/beads-bench-cache/*.db
```

## Adding New Benchmarks

Follow the pattern in `sqlite_bench_test.go`:

```go
// BenchmarkMyTest benchmarks a specific operation
func BenchmarkMyTest(b *testing.B) {
	runBenchmark(b, setupLargeBenchDB, func(store *SQLiteStorage, ctx context.Context) error {
		// Your test code here
		return err
	})
}
```

Or for custom setup:

```go
func BenchmarkMyTest(b *testing.B) {
	store, cleanup := setupLargeBenchDB(b)
	defer cleanup()
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Your test code here
	}
}
```

## CPU Profiling

The benchmark suite automatically enables CPU profiling on the first benchmark run:

```
CPU profiling enabled: bench-cpu-2025-12-07-174417.prof
View flamegraph: go tool pprof -http=:8080 bench-cpu-2025-12-07-174417.prof
```

This generates a flamegraph showing where time is spent across all benchmarks.

## Performance Optimization Strategy

1. **Identify bottleneck** - Run benchmarks to find slow operations
2. **Profile** - Use CPU profiling to see which functions consume time
3. **Measure** - Run baseline benchmark before optimization
4. **Optimize** - Make targeted changes
5. **Verify** - Re-run benchmark to measure improvement

Example:
```bash
# Baseline
go test -tags=bench -bench=BenchmarkGetReadyWork_Large -benchmem ./internal/storage/sqlite/...

# Make changes...

# Measure improvement
go test -tags=bench -bench=BenchmarkGetReadyWork_Large -benchmem ./internal/storage/sqlite/...
```
