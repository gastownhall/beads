//go:build bench

package sqlite

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/testutil/fixtures"
)

var (
	profileOnce   sync.Once
	profileFile   *os.File
	benchCacheDir = "/tmp/beads-bench-cache"
)

// startBenchmarkProfiling starts CPU profiling for the entire benchmark run.
// Uses sync.Once to ensure it only runs once per test process.
// The profile is saved to bench-cpu-<timestamp>.prof in the current directory.
func startBenchmarkProfiling(b *testing.B) {
	b.Helper()
	profileOnce.Do(func() {
		profilePath := fmt.Sprintf("bench-cpu-%s.prof", time.Now().Format("2006-01-02-150405"))
		f, err := os.Create(profilePath)
		if err != nil {
			b.Logf("Warning: failed to create CPU profile: %v", err)
			return
		}
		profileFile = f

		if err := pprof.StartCPUProfile(f); err != nil {
			b.Logf("Warning: failed to start CPU profiling: %v", err)
			f.Close()
			return
		}

		b.Logf("CPU profiling enabled: %s", profilePath)

		// Register cleanup to stop profiling when all benchmarks complete
		b.Cleanup(func() {
			pprof.StopCPUProfile()
			if profileFile != nil {
				profileFile.Close()
				b.Logf("CPU profile saved: %s", profilePath)
				b.Logf("View flamegraph: go tool pprof -http=:8080 %s", profilePath)
			}
		})
	})
}

// Benchmark setup rationale:
// We only provide Large (10K) and XLarge (20K) setup functions because
// small databases don't exhibit the performance characteristics we need to optimize.
// See sqlite_bench_test.go for full rationale.
//
// Dataset caching:
// Datasets are cached in /tmp/beads-bench-cache/ to avoid regenerating 10K-20K
// issues on every benchmark run. Cached databases are ~10-30MB and reused across runs.

// getCachedOrGenerateDB returns a cached database or generates it if missing.
// cacheKey should be unique per dataset type (e.g., "large", "xlarge").
// generateFn is called only if the cached database doesn't exist.
func getCachedOrGenerateDB(b *testing.B, cacheKey string, generateFn func(context.Context, storage.Storage) error) string {
	b.Helper()

	// Ensure cache directory exists
	if err := os.MkdirAll(benchCacheDir, 0755); err != nil {
		b.Fatalf("Failed to create benchmark cache directory: %v", err)
	}

	dbPath := fmt.Sprintf("%s/%s.db", benchCacheDir, cacheKey)

	// Check if cached database exists
	if stat, err := os.Stat(dbPath); err == nil {
		sizeMB := float64(stat.Size()) / (1024 * 1024)
		b.Logf("Using cached benchmark database: %s (%.1f MB)", dbPath, sizeMB)
		return dbPath
	}

	// Generate new database
	b.Logf("===== Generating benchmark database: %s =====", dbPath)
	b.Logf("This is a one-time operation that will be cached for future runs...")
	b.Logf("Expected time: ~1-3 minutes for 10K issues, ~2-6 minutes for 20K issues")

	ctx := context.Background()

	store, err := New(ctx, dbPath)
	if err != nil {
		b.Fatalf("Failed to create storage: %v", err)
	}

	// Initialize database with prefix
	if err := store.SetConfig(ctx, "issue_prefix", "bd-"); err != nil {
		store.Close()
		b.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Generate dataset using provided function
	if err := generateFn(ctx, store); err != nil {
		store.Close()
		os.Remove(dbPath) // cleanup partial database
		b.Fatalf("Failed to generate dataset: %v", err)
	}

	store.Close()

	// Log completion with final size
	if stat, err := os.Stat(dbPath); err == nil {
		sizeMB := float64(stat.Size()) / (1024 * 1024)
		b.Logf("===== Database generation complete: %s (%.1f MB) =====", dbPath, sizeMB)
	}

	return dbPath
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Sync()
}

// setupLargeBenchDB creates or reuses a cached 10K issue database.
// Returns configured storage instance and cleanup function.
// Uses //go:build bench tag to avoid running in normal tests.
// Automatically enables CPU profiling on first call.
//
// Note: Copies the cached database to a temp location for each benchmark
// to prevent mutations from affecting subsequent runs.
func setupLargeBenchDB(b *testing.B) (*SQLiteStorage, func()) {
	b.Helper()

	// Start CPU profiling (only happens once per test run)
	startBenchmarkProfiling(b)

	// Get or generate cached database
	cachedPath := getCachedOrGenerateDB(b, "large", fixtures.LargeSQLite)

	// Copy to temp location to prevent mutations
	tmpPath := b.TempDir() + "/large.db"
	if err := copyFile(cachedPath, tmpPath); err != nil {
		b.Fatalf("Failed to copy cached database: %v", err)
	}

	// Open the temporary copy
	ctx := context.Background()

	store, err := New(ctx, tmpPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}

	return store, func() {
		store.Close()
	}
}

// setupXLargeBenchDB creates or reuses a cached 20K issue database.
// Returns configured storage instance and cleanup function.
// Uses //go:build bench tag to avoid running in normal tests.
// Automatically enables CPU profiling on first call.
//
// Note: Copies the cached database to a temp location for each benchmark
// to prevent mutations from affecting subsequent runs.
func setupXLargeBenchDB(b *testing.B) (*SQLiteStorage, func()) {
	b.Helper()

	// Start CPU profiling (only happens once per test run)
	startBenchmarkProfiling(b)

	// Get or generate cached database
	cachedPath := getCachedOrGenerateDB(b, "xlarge", fixtures.XLargeSQLite)

	// Copy to temp location to prevent mutations
	tmpPath := b.TempDir() + "/xlarge.db"
	if err := copyFile(cachedPath, tmpPath); err != nil {
		b.Fatalf("Failed to copy cached database: %v", err)
	}

	// Open the temporary copy
	ctx := context.Background()

	store, err := New(ctx, tmpPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}

	return store, func() {
		store.Close()
	}
}

// setupLargeFromJSONL creates or reuses a cached 10K issue database via JSONL import path.
// Returns configured storage instance and cleanup function.
// Uses //go:build bench tag to avoid running in normal tests.
// Automatically enables CPU profiling on first call.
//
// Note: Copies the cached database to a temp location for each benchmark
// to prevent mutations from affecting subsequent runs.
func setupLargeFromJSONL(b *testing.B) (*SQLiteStorage, func()) {
	b.Helper()

	// Start CPU profiling (only happens once per test run)
	startBenchmarkProfiling(b)

	// Get or generate cached database with JSONL import path
	cachedPath := getCachedOrGenerateDB(b, "large-jsonl", func(ctx context.Context, store storage.Storage) error {
		tempDir := b.TempDir()
		return fixtures.LargeFromJSONL(ctx, store, tempDir)
	})

	// Copy to temp location to prevent mutations
	tmpPath := b.TempDir() + "/large-jsonl.db"
	if err := copyFile(cachedPath, tmpPath); err != nil {
		b.Fatalf("Failed to copy cached database: %v", err)
	}

	// Open the temporary copy
	ctx := context.Background()

	store, err := New(ctx, tmpPath)
	if err != nil {
		b.Fatalf("Failed to open database: %v", err)
	}

	return store, func() {
		store.Close()
	}
}
