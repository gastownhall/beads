# Beads Performance Benchmarks

This document describes the performance benchmarks available in the beads project and how to use them.

## Running Benchmarks

### All Dolt Benchmarks
```bash
go test -tags=bench -bench=. -benchmem ./internal/storage/dolt/...
```

### Specific Benchmark
```bash
go test -tags=bench -bench=BenchmarkGetReadyWork_Large -benchmem ./internal/storage/dolt/...
```

### With CPU Profiling
```bash
go test -tags=bench -bench=BenchmarkGetReadyWork_Large -cpuprofile=cpu.prof ./internal/storage/dolt/...
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
- **BenchmarkGetReadyWork_FromJSONL** - Ready work on imported database

### Search Operations
- **BenchmarkSearchIssues_Large_NoFilter** - Search all open issues (10K dataset)
- **BenchmarkSearchIssues_Large_ComplexFilter** - Search with priority/status filters (10K dataset)
- **BenchmarkPerfSearchTypedLabelFilter_5K** - Label/type search over a 5K issue/label catalog
- **BenchmarkPerfResolvePartialIDInvalidInput_5K** - Invalid partial-ID rejection without a broad fallback scan

### CRUD Operations
- **BenchmarkCreateIssue_Large** - Create new issue in 10K database
- **BenchmarkUpdateIssue_Large** - Update existing issue in 10K database
- **BenchmarkBulkCloseIssues** - Close 100 issues sequentially (NEW)

### Specialized Operations
- **BenchmarkLargeDescription** - Handling 100KB+ issue descriptions (NEW)
- **BenchmarkSyncMerge** - Simulate sync cycle with create/update operations (NEW)

### Recent Perf Regression References

These benchmarks cover the May 2026 Dolt hot-path changes so future perf PRs can run before/after checks against the same fixture shapes:

| PR / change | Benchmark |
|-------------|-----------|
| #3966 `perf(deps): narrow recursive cycle checks` | `BenchmarkPerfAddDependencyCycleCheck_DiamondDAG` |
| #3967 `perf(search): tighten label and partial-id queries` | `BenchmarkPerfSearchTypedLabelFilter_5K`, `BenchmarkPerfResolvePartialIDInvalidInput_5K` |
| #3968 `perf(ready): page blocked checks for limited ready work` | `BenchmarkPerfReadyWorkLimited_LargeBlockedGraph` |
| #4001 `perf(ready): narrow deferred-parent child filtering` | `BenchmarkPerfReadyWorkDeferredParentExclusion_5K` |
| #4002 `perf(ready): restrict blocked dependency scans to active IDs` | `BenchmarkPerfBlockedIssues_ClosedDependencySkew` |
| #4003 `perf(get): query primary issues before wisp fallback` | `BenchmarkPerfGetIssuePrimaryFirst_PermanentWithWisps` |
| #4004 `perf(deps): scan one cycle table for same-storage edges` | No standalone executable perf diff in the landed squash; covered by the cycle-check benchmark above |

Measured with `-benchtime=1x -benchmem -count=1` on the same host, copying this benchmark file onto each before/after ref:

| PR / path | Benchmark | Before | After | Time gain | Alloc gain |
|-----------|-----------|--------|-------|-----------|------------|
| #3967 label/type search | `BenchmarkPerfSearchTypedLabelFilter_5K` | 134.8 ms | 51.8 ms | 61.6% | -0.1% |
| #3967 invalid partial-ID fallback | `BenchmarkPerfResolvePartialIDInvalidInput_5K` | 124.3 ms | 22.5 ms | 81.9% | 43.6% |
| #3966 dependency cycle check | `BenchmarkPerfAddDependencyCycleCheck_DiamondDAG` | 80.0 ms | 25.8 ms | 67.7% | 1.4% |
| #3968 limited ready work | `BenchmarkPerfReadyWorkLimited_LargeBlockedGraph` | 1677.4 ms | 341.7 ms | 79.6% | 85.4% |
| #4001 deferred parent exclusion | `BenchmarkPerfReadyWorkDeferredParentExclusion_5K` | 3257.3 ms | 130.8 ms | 96.0% | 83.1% |
| #4002 active blocked-dep scan | `BenchmarkPerfBlockedIssues_ClosedDependencySkew` | 44.3 ms | 36.2 ms | 18.1% | 96.0% |
| #4003 primary issue lookup | `BenchmarkPerfGetIssuePrimaryFirst_PermanentWithWisps` | 9.0 ms | 6.4 ms | 28.7% | 10.7% |

Run the recent perf reference set with:

```bash
go test -run=^$ -bench='BenchmarkPerf(SearchTypedLabelFilter|ResolvePartialIDInvalidInput|AddDependencyCycleCheck|ReadyWorkLimited|BlockedIssues|ReadyWorkDeferredParentExclusion|GetIssuePrimaryFirst)' -benchtime=1x -benchmem ./internal/storage/dolt
```

For production-shaped CLI timeout and index experiments, use:

```bash
go run ./scripts/repro-dolt-prod-timeouts --bd ./bd --scenario all
go run ./scripts/bench-ready-indexes --dsn 'root@tcp(127.0.0.1:33307)/mc?timeout=30s&readTimeout=30s&writeTimeout=30s'
```

When `repro-dolt-prod-timeouts` targets an existing workspace with
`--workspace`, fixture seeding defaults to `--seed-mode=none`; pass
`--seed-mode=full` or `--seed-mode=dep-only` only when intentionally writing
and committing synthetic fixture rows into that workspace.

`bench-ready-indexes` drops its candidate indexes again before exit by default;
pass `--keep-indexes` only when intentionally leaving the final index set
installed.

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
go test -tags=bench -bench=BenchmarkGetReadyWork_Large -benchmem ./internal/storage/dolt/...

# Make changes...

# Measure improvement
go test -tags=bench -bench=BenchmarkGetReadyWork_Large -benchmem ./internal/storage/dolt/...
```
