// Package dolt provides performance benchmarks for the Dolt storage backend.
// Run with: go test -bench=. -benchmem ./internal/storage/dolt/...
//
// These benchmarks measure:
// - Single and bulk issue operations
// - Search and query performance
// - Dependency operations
// - Concurrent access patterns
// - Version control operations
package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

func benchDatabaseName() string {
	return fmt.Sprintf("beads_test_bench_%d", time.Now().UnixNano())
}

// setupBenchStore creates a store for benchmarks
func setupBenchStore(b *testing.B) (*DoltStore, func()) {
	b.Helper()

	if _, err := os.LookupEnv("DOLT_PATH"); err != false {
		// Check if dolt binary exists
		if _, err := os.Stat("/usr/local/bin/dolt"); os.IsNotExist(err) {
			if _, err := os.Stat("/usr/bin/dolt"); os.IsNotExist(err) {
				b.Skip("Dolt not installed, skipping benchmark")
			}
		}
	}

	ctx := context.Background()
	tmpDir, err := os.MkdirTemp("", "dolt-bench-*")
	if err != nil {
		b.Fatalf("failed to create temp dir: %v", err)
	}

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "bench",
		CommitterEmail:  "bench@example.com",
		Database:        benchDatabaseName(),
		CreateIfMissing: true,
	}

	store, err := New(ctx, cfg)
	if err != nil {
		os.RemoveAll(tmpDir)
		b.Fatalf("failed to create Dolt store: %v", err)
	}

	if err := store.SetConfig(ctx, "issue_prefix", "bench"); err != nil {
		store.Close()
		os.RemoveAll(tmpDir)
		b.Fatalf("failed to set prefix: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

// =============================================================================
// Bootstrap & Connection Benchmarks
// =============================================================================

// BenchmarkBootstrapEmbedded measures store initialization time in embedded mode.
// This is the critical path for CLI commands that open/close the store each time.
func BenchmarkBootstrapEmbedded(b *testing.B) {
	if _, err := os.LookupEnv("DOLT_PATH"); err != false {
		if _, err := os.Stat("/usr/local/bin/dolt"); os.IsNotExist(err) {
			if _, err := os.Stat("/usr/bin/dolt"); os.IsNotExist(err) {
				b.Skip("Dolt not installed, skipping benchmark")
			}
		}
	}

	tmpDir, err := os.MkdirTemp("", "dolt-bootstrap-bench-*")
	if err != nil {
		b.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()

	// Create initial store to set up schema
	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "bench",
		CommitterEmail:  "bench@example.com",
		Database:        benchDatabaseName(),
		CreateIfMissing: true,
	}

	initStore, err := New(ctx, cfg)
	if err != nil {
		b.Fatalf("failed to create initial store: %v", err)
	}
	initStore.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store, err := New(ctx, cfg)
		if err != nil {
			b.Fatalf("failed to create store: %v", err)
		}
		store.Close()
	}
}

// BenchmarkColdStart simulates CLI pattern: open store, read one issue, close.
// This measures the realistic cost of a single bd command.
func BenchmarkColdStart(b *testing.B) {
	// First create a store with data
	store, cleanup := setupBenchStore(b)
	ctx := context.Background()

	// Create a test issue
	issue := &types.Issue{
		ID:          "cold-start-issue",
		Title:       "Cold Start Test Issue",
		Description: "Issue for cold start benchmark",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
		b.Fatalf("failed to create issue: %v", err)
	}

	// Get the path for reopening
	tmpDir := store.dbPath
	dbName := store.database
	store.Close()

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "bench",
		CommitterEmail:  "bench@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Open
		s, err := New(ctx, cfg)
		if err != nil {
			b.Fatalf("failed to open store: %v", err)
		}

		// Read
		_, err = s.GetIssue(ctx, "cold-start-issue")
		if err != nil {
			b.Fatalf("failed to get issue: %v", err)
		}

		// Close
		s.Close()
	}

	// Cleanup is handled by the deferred cleanup from setupBenchStore
	cleanup()
}

// BenchmarkWarmCache measures read performance with warm cache (store already open).
// Contrast with BenchmarkColdStart to see bootstrap overhead.
func BenchmarkWarmCache(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create a test issue
	issue := &types.Issue{
		ID:          "warm-cache-issue",
		Title:       "Warm Cache Test Issue",
		Description: "Issue for warm cache benchmark",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
		b.Fatalf("failed to create issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.GetIssue(ctx, "warm-cache-issue")
		if err != nil {
			b.Fatalf("failed to get issue: %v", err)
		}
	}
}

// BenchmarkCLIWorkflow simulates a typical CLI workflow:
// open -> list ready -> show issue -> close
func BenchmarkCLIWorkflow(b *testing.B) {
	// Setup store with data
	store, cleanup := setupBenchStore(b)
	ctx := context.Background()

	// Create some issues
	for i := 0; i < 20; i++ {
		issue := &types.Issue{
			ID:        fmt.Sprintf("cli-workflow-%d", i),
			Title:     fmt.Sprintf("CLI Workflow Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  (i % 4) + 1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
			b.Fatalf("failed to create issue: %v", err)
		}
	}

	tmpDir := store.dbPath
	dbName := store.database
	store.Close()

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "bench",
		CommitterEmail:  "bench@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate: bd ready && bd show <first>
		s, err := New(ctx, cfg)
		if err != nil {
			b.Fatalf("failed to open store: %v", err)
		}

		ready, err := s.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			b.Fatalf("failed to get ready work: %v", err)
		}

		if len(ready) > 0 {
			_, err = s.GetIssue(ctx, ready[0].ID)
			if err != nil {
				b.Fatalf("failed to get issue: %v", err)
			}
		}

		s.Close()
	}

	cleanup()
}

// =============================================================================
// Single Operation Benchmarks
// =============================================================================

// BenchmarkCreateIssue measures single issue creation performance.
func BenchmarkCreateIssue(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		issue := &types.Issue{
			Title:       fmt.Sprintf("Benchmark Issue %d", i),
			Description: "Benchmark issue for performance testing",
			Status:      types.StatusOpen,
			Priority:    (i % 4) + 1,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
			b.Fatalf("failed to create issue: %v", err)
		}
	}
}

// BenchmarkGetIssue measures single issue retrieval performance.
func BenchmarkGetIssue(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create a test issue
	issue := &types.Issue{
		ID:          "bench-get-issue",
		Title:       "Get Benchmark Issue",
		Description: "For get benchmark",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
		b.Fatalf("failed to create issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.GetIssue(ctx, "bench-get-issue")
		if err != nil {
			b.Fatalf("failed to get issue: %v", err)
		}
	}
}

// BenchmarkUpdateIssue measures single issue update performance.
func BenchmarkUpdateIssue(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create a test issue
	issue := &types.Issue{
		ID:          "bench-update-issue",
		Title:       "Update Benchmark Issue",
		Description: "For update benchmark",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
		b.Fatalf("failed to create issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		updates := map[string]interface{}{
			"description": fmt.Sprintf("Updated %d times", i),
		}
		if err := store.UpdateIssue(ctx, "bench-update-issue", updates, "bench"); err != nil {
			b.Fatalf("failed to update issue: %v", err)
		}
	}
}

// =============================================================================
// Bulk Operation Benchmarks
// =============================================================================

// BenchmarkBulkCreateIssues measures bulk issue creation performance.
func BenchmarkBulkCreateIssues(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	const batchSize = 100

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		issues := make([]*types.Issue, batchSize)
		for j := 0; j < batchSize; j++ {
			issues[j] = &types.Issue{
				ID:          fmt.Sprintf("bulk-%d-%d", i, j),
				Title:       fmt.Sprintf("Bulk Issue %d-%d", i, j),
				Description: "Bulk created issue",
				Status:      types.StatusOpen,
				Priority:    (j % 4) + 1,
				IssueType:   types.TypeTask,
			}
		}
		if err := store.CreateIssues(ctx, issues, "bench"); err != nil {
			b.Fatalf("failed to create issues: %v", err)
		}
	}
	b.ReportMetric(float64(batchSize), "issues/op")
}

// BenchmarkBulkCreate1000Issues measures creating 1000 issues.
func BenchmarkBulkCreate1000Issues(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	const batchSize = 1000

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		issues := make([]*types.Issue, batchSize)
		for j := 0; j < batchSize; j++ {
			issues[j] = &types.Issue{
				ID:          fmt.Sprintf("bulk1k-%d-%d", i, j),
				Title:       fmt.Sprintf("Bulk 1K Issue %d-%d", i, j),
				Description: "Bulk created issue for 1000 issue benchmark",
				Status:      types.StatusOpen,
				Priority:    (j % 4) + 1,
				IssueType:   types.TypeTask,
			}
		}
		if err := store.CreateIssues(ctx, issues, "bench"); err != nil {
			b.Fatalf("failed to create issues: %v", err)
		}
	}
	b.ReportMetric(float64(batchSize), "issues/op")
}

// =============================================================================
// Search Benchmarks
// =============================================================================

// BenchmarkSearchIssues measures search performance with varying dataset sizes.
func BenchmarkSearchIssues(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create 100 issues with searchable content
	issues := make([]*types.Issue, 100)
	for i := 0; i < 100; i++ {
		issues[i] = &types.Issue{
			ID:          fmt.Sprintf("search-%d", i),
			Title:       fmt.Sprintf("Searchable Issue Number %d", i),
			Description: fmt.Sprintf("This is issue %d with some searchable content about testing", i),
			Status:      types.StatusOpen,
			Priority:    (i % 4) + 1,
			IssueType:   types.TypeTask,
		}
	}
	if err := store.CreateIssues(ctx, issues, "bench"); err != nil {
		b.Fatalf("failed to create issues: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.SearchIssues(ctx, "searchable", types.IssueFilter{})
		if err != nil {
			b.Fatalf("failed to search: %v", err)
		}
	}
}

// BenchmarkSearchWithFilter measures filtered search performance.
func BenchmarkSearchWithFilter(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create issues with different statuses
	for i := 0; i < 100; i++ {
		status := types.StatusOpen
		if i%3 == 0 {
			status = types.StatusInProgress
		} else if i%3 == 1 {
			status = types.StatusClosed
		}

		issue := &types.Issue{
			ID:          fmt.Sprintf("filter-search-%d", i),
			Title:       fmt.Sprintf("Filter Search Issue %d", i),
			Description: "Issue for filtered search benchmark",
			Status:      status,
			Priority:    (i % 4) + 1,
			IssueType:   types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
			b.Fatalf("failed to create issue: %v", err)
		}
	}

	openStatus := types.StatusOpen

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.SearchIssues(ctx, "", types.IssueFilter{Status: &openStatus})
		if err != nil {
			b.Fatalf("failed to search with filter: %v", err)
		}
	}
}

// =============================================================================
// Dependency Benchmarks
// =============================================================================

// BenchmarkAddDependency measures dependency creation performance.
func BenchmarkAddDependency(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create issues to link
	parent := &types.Issue{
		ID:        "dep-parent",
		Title:     "Dependency Parent",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeEpic,
	}
	if err := store.CreateIssue(ctx, parent, "bench"); err != nil {
		b.Fatalf("failed to create parent: %v", err)
	}

	for i := 0; i < b.N; i++ {
		child := &types.Issue{
			ID:        fmt.Sprintf("dep-child-%d", i),
			Title:     fmt.Sprintf("Dependency Child %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, child, "bench"); err != nil {
			b.Fatalf("failed to create child: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dep := &types.Dependency{
			IssueID:     fmt.Sprintf("dep-child-%d", i),
			DependsOnID: "dep-parent",
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "bench"); err != nil {
			b.Fatalf("failed to add dependency: %v", err)
		}
	}
}

// BenchmarkGetDependencies measures dependency retrieval performance.
func BenchmarkGetDependencies(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create a child with multiple dependencies
	child := &types.Issue{
		ID:        "multi-dep-child",
		Title:     "Multi Dependency Child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, child, "bench"); err != nil {
		b.Fatalf("failed to create child: %v", err)
	}

	// Create 10 parents and link them
	for i := 0; i < 10; i++ {
		parent := &types.Issue{
			ID:        fmt.Sprintf("multi-parent-%d", i),
			Title:     fmt.Sprintf("Multi Parent %d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, parent, "bench"); err != nil {
			b.Fatalf("failed to create parent: %v", err)
		}

		dep := &types.Dependency{
			IssueID:     child.ID,
			DependsOnID: parent.ID,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "bench"); err != nil {
			b.Fatalf("failed to add dependency: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.GetDependencies(ctx, child.ID)
		if err != nil {
			b.Fatalf("failed to get dependencies: %v", err)
		}
	}
}

// BenchmarkIsBlocked measures blocking check performance.
func BenchmarkIsBlocked(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create parent and child with blocking relationship
	parent := &types.Issue{
		ID:        "block-parent",
		Title:     "Blocking Parent",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	}
	child := &types.Issue{
		ID:        "block-child",
		Title:     "Blocked Child",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, parent, "bench"); err != nil {
		b.Fatalf("failed to create parent: %v", err)
	}
	if err := store.CreateIssue(ctx, child, "bench"); err != nil {
		b.Fatalf("failed to create child: %v", err)
	}

	dep := &types.Dependency{
		IssueID:     child.ID,
		DependsOnID: parent.ID,
		Type:        types.DepBlocks,
	}
	if err := store.AddDependency(ctx, dep, "bench"); err != nil {
		b.Fatalf("failed to add dependency: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := store.IsBlocked(ctx, child.ID)
		if err != nil {
			b.Fatalf("failed to check if blocked: %v", err)
		}
	}
}

// =============================================================================
// Concurrent Access Benchmarks
// =============================================================================

// BenchmarkConcurrentReads measures concurrent read performance.
func BenchmarkConcurrentReads(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create test issue
	issue := &types.Issue{
		ID:        "concurrent-read",
		Title:     "Concurrent Read Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
		b.Fatalf("failed to create issue: %v", err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := store.GetIssue(ctx, "concurrent-read")
			if err != nil {
				b.Errorf("concurrent read failed: %v", err)
			}
		}
	})
}

// BenchmarkConcurrentWrites measures concurrent write performance.
func BenchmarkConcurrentWrites(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	var counter int64
	var mu sync.Mutex

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			mu.Lock()
			counter++
			id := counter
			mu.Unlock()

			issue := &types.Issue{
				ID:        fmt.Sprintf("concurrent-write-%d", id),
				Title:     fmt.Sprintf("Concurrent Write Issue %d", id),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
				b.Errorf("concurrent write failed: %v", err)
			}
		}
	})
}

// BenchmarkConcurrentMixedWorkload measures mixed read/write workload.
func BenchmarkConcurrentMixedWorkload(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create some initial issues
	for i := 0; i < 50; i++ {
		issue := &types.Issue{
			ID:        fmt.Sprintf("mixed-%d", i),
			Title:     fmt.Sprintf("Mixed Workload Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  (i % 4) + 1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
			b.Fatalf("failed to create issue: %v", err)
		}
	}

	var writeCounter int64
	var mu sync.Mutex

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var localCounter int
		for pb.Next() {
			localCounter++
			if localCounter%5 == 0 {
				// 20% writes
				mu.Lock()
				writeCounter++
				id := writeCounter
				mu.Unlock()

				issue := &types.Issue{
					ID:        fmt.Sprintf("mixed-new-%d", id),
					Title:     fmt.Sprintf("Mixed New Issue %d", id),
					Status:    types.StatusOpen,
					Priority:  2,
					IssueType: types.TypeTask,
				}
				if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
					b.Errorf("write failed: %v", err)
				}
			} else {
				// 80% reads
				_, err := store.GetIssue(ctx, fmt.Sprintf("mixed-%d", localCounter%50))
				if err != nil {
					b.Errorf("read failed: %v", err)
				}
			}
		}
	})
}

// =============================================================================
// Version Control Benchmarks
// =============================================================================

// BenchmarkCommit measures commit performance.
func BenchmarkCommit(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Create an issue
		issue := &types.Issue{
			ID:        fmt.Sprintf("commit-bench-%d", i),
			Title:     fmt.Sprintf("Commit Bench Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
			b.Fatalf("failed to create issue: %v", err)
		}

		// Commit
		if err := store.Commit(ctx, fmt.Sprintf("Benchmark commit %d", i)); err != nil {
			b.Fatalf("failed to commit: %v", err)
		}
	}
}

// BenchmarkLog measures log retrieval performance.
func BenchmarkLog(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create some commits
	for i := 0; i < 20; i++ {
		issue := &types.Issue{
			ID:        fmt.Sprintf("log-bench-%d", i),
			Title:     fmt.Sprintf("Log Bench Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
			b.Fatalf("failed to create issue: %v", err)
		}
		if err := store.Commit(ctx, fmt.Sprintf("Log commit %d", i)); err != nil {
			b.Fatalf("failed to commit: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.Log(ctx, 10)
		if err != nil {
			b.Fatalf("failed to get log: %v", err)
		}
	}
}

// =============================================================================
// Statistics Benchmarks
// =============================================================================

// BenchmarkGetStatistics measures statistics computation performance.
func BenchmarkGetStatistics(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create a mix of issues
	for i := 0; i < 100; i++ {
		status := types.StatusOpen
		if i%3 == 0 {
			status = types.StatusInProgress
		} else if i%3 == 1 {
			status = types.StatusClosed
		}

		issue := &types.Issue{
			ID:        fmt.Sprintf("stats-%d", i),
			Title:     fmt.Sprintf("Stats Issue %d", i),
			Status:    status,
			Priority:  (i % 4) + 1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
			b.Fatalf("failed to create issue: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.GetStatistics(ctx)
		if err != nil {
			b.Fatalf("failed to get statistics: %v", err)
		}
	}
}

// BenchmarkGetReadyWork measures ready work query performance.
func BenchmarkGetReadyWork(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create issues with dependencies
	for i := 0; i < 50; i++ {
		parent := &types.Issue{
			ID:        fmt.Sprintf("ready-parent-%d", i),
			Title:     fmt.Sprintf("Ready Parent %d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, parent, "bench"); err != nil {
			b.Fatalf("failed to create parent: %v", err)
		}

		child := &types.Issue{
			ID:        fmt.Sprintf("ready-child-%d", i),
			Title:     fmt.Sprintf("Ready Child %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, child, "bench"); err != nil {
			b.Fatalf("failed to create child: %v", err)
		}

		if i%2 == 0 {
			// Half are blocked
			dep := &types.Dependency{
				IssueID:     child.ID,
				DependsOnID: parent.ID,
				Type:        types.DepBlocks,
			}
			if err := store.AddDependency(ctx, dep, "bench"); err != nil {
				b.Fatalf("failed to add dependency: %v", err)
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.GetReadyWork(ctx, types.WorkFilter{})
		if err != nil {
			b.Fatalf("failed to get ready work: %v", err)
		}
	}
}

// =============================================================================
// Recent production hot-path regression benchmarks (May 2026 perf stack)
// =============================================================================

func createBenchIssueBatch(b *testing.B, store *DoltStore, issues []*types.Issue) {
	b.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	err := store.withRetryTx(ctx, func(tx *sql.Tx) error {
		issuesByTable := map[string][]*types.Issue{
			"issues": nil,
			"wisps":  nil,
		}
		for _, issue := range issues {
			if issue.Status == "" {
				issue.Status = types.StatusOpen
			}
			if issue.Priority == 0 {
				issue.Priority = 2
			}
			if issue.IssueType == "" {
				issue.IssueType = types.TypeTask
			}
			if issue.CreatedAt.IsZero() {
				issue.CreatedAt = now
			}
			if issue.UpdatedAt.IsZero() {
				issue.UpdatedAt = issue.CreatedAt
			}
			issue.ContentHash = issue.ComputeContentHash()

			issueTable, _ := issueops.TableRouting(issue)
			issuesByTable[issueTable] = append(issuesByTable[issueTable], issue)
		}
		for table, tableIssues := range issuesByTable {
			if err := insertBenchIssues(ctx, tx, table, tableIssues); err != nil {
				return err
			}
			if err := insertBenchLabels(ctx, tx, table, tableIssues); err != nil {
				return err
			}
		}

		if err := insertBenchDependencies(ctx, tx, "dependencies", issuesByTable["issues"], now); err != nil {
			return err
		}
		if err := insertBenchDependencies(ctx, tx, "wisp_dependencies", issuesByTable["wisps"], now); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		b.Fatalf("failed to create %d benchmark issues: %v", len(issues), err)
	}
}

func ptrString(s string) *string {
	return &s
}

func insertBenchIssues(ctx context.Context, tx *sql.Tx, table string, issues []*types.Issue) error {
	const batchSize = 500
	for start := 0; start < len(issues); start += batchSize {
		end := start + batchSize
		if end > len(issues) {
			end = len(issues)
		}
		var placeholders []string
		var args []interface{}
		for _, issue := range issues[start:end] {
			metadata := string(issue.Metadata)
			if metadata == "" {
				metadata = "{}"
			}
			placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args,
				issue.ID,
				issue.ContentHash,
				issue.Title,
				issue.Description,
				issue.Design,
				issue.AcceptanceCriteria,
				issue.Notes,
				issue.Status,
				issue.Priority,
				issue.IssueType,
				issue.Assignee,
				issue.CreatedAt,
				issue.UpdatedAt,
				issue.DeferUntil,
				issue.Ephemeral,
				issue.NoHistory,
				issue.Pinned,
				metadata,
			)
		}
		//nolint:gosec // G201: table is selected from fixed issue table names.
		query := fmt.Sprintf(`
			INSERT INTO %s (
				id, content_hash, title, description, design, acceptance_criteria, notes,
				status, priority, issue_type, assignee, created_at, updated_at, defer_until,
				ephemeral, no_history, pinned, metadata
			) VALUES %s
			ON DUPLICATE KEY UPDATE title = VALUES(title)
		`, table, strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("insert benchmark issues into %s: %w", table, err)
		}
	}
	return nil
}

func insertBenchLabels(ctx context.Context, tx *sql.Tx, issueTable string, issues []*types.Issue) error {
	const batchSize = 500
	labelTable := "labels"
	if issueTable == "wisps" {
		labelTable = "wisp_labels"
	}
	var issueIDs []string
	var labels []string
	for _, issue := range issues {
		for _, label := range issue.Labels {
			issueIDs = append(issueIDs, issue.ID)
			labels = append(labels, label)
		}
	}
	for start := 0; start < len(labels); start += batchSize {
		end := start + batchSize
		if end > len(labels) {
			end = len(labels)
		}
		var placeholders []string
		var args []interface{}
		for i := start; i < end; i++ {
			placeholders = append(placeholders, "(?, ?)")
			args = append(args, issueIDs[i], labels[i])
		}
		//nolint:gosec // G201: labelTable is selected from fixed label table names.
		query := fmt.Sprintf(`
			INSERT INTO %s (issue_id, label)
			VALUES %s
			ON DUPLICATE KEY UPDATE label = label
		`, labelTable, strings.Join(placeholders, ","))
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("insert benchmark labels into %s: %w", labelTable, err)
		}
	}
	return nil
}

func insertBenchDependencies(ctx context.Context, tx *sql.Tx, depTable string, issues []*types.Issue, now time.Time) error {
	type benchDependencyRow struct {
		issueID     string
		dependsOnID string
		depType     types.DependencyType
		createdAt   time.Time
	}

	const batchSize = 500
	rowsByColumn := map[string][]benchDependencyRow{}
	for _, issue := range issues {
		for _, dep := range issue.Dependencies {
			if dep.IssueID == "" {
				dep.IssueID = issue.ID
			}
			createdAt := dep.CreatedAt
			if createdAt.IsZero() {
				createdAt = now
			}
			column := issueops.ClassifyDepTarget(ctx, tx, dep, false).Column()
			rowsByColumn[column] = append(rowsByColumn[column], benchDependencyRow{
				issueID:     dep.IssueID,
				dependsOnID: dep.DependsOnID,
				depType:     dep.Type,
				createdAt:   createdAt,
			})
		}
	}
	for column, rows := range rowsByColumn {
		for start := 0; start < len(rows); start += batchSize {
			end := start + batchSize
			if end > len(rows) {
				end = len(rows)
			}
			var placeholders []string
			var args []interface{}
			for _, row := range rows[start:end] {
				placeholders = append(placeholders, "(?, ?, ?, 'bench', ?, '{}')")
				args = append(args, row.issueID, row.dependsOnID, row.depType, row.createdAt)
			}
			//nolint:gosec // G201: depTable is fixed and column comes from issueops.DepTargetKind.Column.
			query := fmt.Sprintf(`
				INSERT INTO %s (issue_id, %s, type, created_by, created_at, metadata)
				VALUES %s
				ON DUPLICATE KEY UPDATE type = type
			`, depTable, column, strings.Join(placeholders, ","))
			if _, err := tx.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("insert benchmark dependencies into %s: %w", depTable, err)
			}
		}
	}
	return nil
}

func BenchmarkPerfSearchTypedLabelFilter_5K(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	const total = 5000
	issues := make([]*types.Issue, 0, total)
	for i := 0; i < total; i++ {
		labels := []string{fmt.Sprintf("bucket-%03d", i%500)}
		issueType := types.TypeBug
		if i%3 == 0 {
			issueType = types.TypeFeature
		}
		if i%40 == 0 {
			issueType = types.TypeTask
			labels = append(labels, "perf-hot")
		}
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-search-%05d", i),
			Title:     fmt.Sprintf("Search label benchmark issue %05d", i),
			Status:    types.StatusOpen,
			Priority:  (i % 4) + 1,
			IssueType: issueType,
			Labels:    labels,
		})
	}
	createBenchIssueBatch(b, store, issues)

	ctx := context.Background()
	taskType := types.TypeTask
	filter := types.IssueFilter{IssueType: &taskType, Labels: []string{"perf-hot"}, Limit: 200}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			b.Fatalf("SearchIssues label filter: %v", err)
		}
		if len(results) == 0 {
			b.Fatal("SearchIssues label filter returned no issues")
		}
	}
}

func BenchmarkPerfResolvePartialIDInvalidInput_5K(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	const total = 5000
	issues := make([]*types.Issue, 0, total)
	for i := 0; i < total; i++ {
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-partial-%05d", i),
			Title:     fmt.Sprintf("Partial ID benchmark issue %05d", i),
			Status:    types.StatusOpen,
			Priority:  (i % 4) + 1,
			IssueType: types.TypeTask,
		})
	}
	createBenchIssueBatch(b, store, issues)

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := utils.ResolvePartialID(ctx, store, "not/a/valid/id"); err == nil {
			b.Fatal("ResolvePartialID invalid input unexpectedly succeeded")
		}
	}
}

// BenchmarkPerfIDProjectionVsHydration_5K isolates the value of the narrow
// id-only projection (SearchIssueIDs) against the two hydration alternatives on
// an identical, large result set. All three arms share the same WHERE clause
// via issueops.searchInTx, so the only variable is projection + row hydration:
//
//   - SearchIssueIDs          — projects only `id`; no row structs, no labels.
//   - SearchIssues+SkipLabels — full IssueSelectColumns scan into *types.Issue
//     (description/design/notes/metadata/...), but skips the labels JOIN.
//   - SearchIssues (full)     — full column scan plus label hydration.
//
// The narrow-vs-SkipLabels delta is the figure that justifies the projection
// over simply reusing upstream's SkipLabels: SkipLabels suppresses labels but
// still materializes every heavy TEXT column, which this fixture populates.
func BenchmarkPerfIDProjectionVsHydration_5K(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	const total = 5000
	// Heavy TEXT payloads approximate real issues. The narrow projection never
	// scans these columns; SkipLabels does not help with them.
	bigText := strings.Repeat("lorem ipsum dolor sit amet consectetur ", 24) // ~936 B
	bigJSON := json.RawMessage(`{"k":"` + strings.Repeat("v", 256) + `"}`)

	issues := make([]*types.Issue, 0, total)
	for i := 0; i < total; i++ {
		issues = append(issues, &types.Issue{
			ID:                 fmt.Sprintf("narrowbench-%05d", i),
			Title:              fmt.Sprintf("narrowbench issue %05d", i),
			Description:        bigText,
			Design:             bigText,
			AcceptanceCriteria: bigText,
			Notes:              bigText,
			Metadata:           bigJSON,
			Status:             types.StatusOpen,
			Priority:           (i % 4) + 1,
			IssueType:          types.TypeTask,
			Labels:             []string{"area-narrow", fmt.Sprintf("bucket-%03d", i%100)},
		})
	}
	createBenchIssueBatch(b, store, issues)

	ctx := context.Background()
	// "narrowbench" appears in every id and title, so id/title LIKE matches the
	// full set — maximizing per-row hydration cost, the dimension under test.
	const query = "narrowbench"

	// Guard: all three arms must see the same cardinality, else the comparison
	// would be measuring row count rather than projection.
	if ids, err := store.SearchIssueIDs(ctx, query, types.IssueFilter{}); err != nil {
		b.Fatalf("setup SearchIssueIDs: %v", err)
	} else if len(ids) != total {
		b.Fatalf("fixture: expected %d matches, got %d", total, len(ids))
	}

	b.Run("SearchIssueIDs_narrow", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got, err := store.SearchIssueIDs(ctx, query, types.IssueFilter{}); err != nil {
				b.Fatalf("SearchIssueIDs: %v", err)
			} else if len(got) != total {
				b.Fatalf("SearchIssueIDs: got %d want %d", len(got), total)
			}
		}
	})

	b.Run("SearchIssues_SkipLabels", func(b *testing.B) {
		b.ReportAllocs()
		filter := types.IssueFilter{SkipLabels: true}
		for i := 0; i < b.N; i++ {
			if got, err := store.SearchIssues(ctx, query, filter); err != nil {
				b.Fatalf("SearchIssues SkipLabels: %v", err)
			} else if len(got) != total {
				b.Fatalf("SearchIssues SkipLabels: got %d want %d", len(got), total)
			}
		}
	})

	b.Run("SearchIssues_full", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got, err := store.SearchIssues(ctx, query, types.IssueFilter{}); err != nil {
				b.Fatalf("SearchIssues full: %v", err)
			} else if len(got) != total {
				b.Fatalf("SearchIssues full: got %d want %d", len(got), total)
			}
		}
	})
}

func BenchmarkPerfAddDependencyCycleCheck_DiamondDAG(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	const layers = 10
	issues := make([]*types.Issue, 0, 2*layers+1)
	issues = append(issues, &types.Issue{
		ID:        "bench-perf-cycle-source",
		Title:     "Cycle source",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	})
	for layer := 0; layer < layers; layer++ {
		for _, suffix := range []string{"a", "b"} {
			issue := &types.Issue{
				ID:        fmt.Sprintf("bench-perf-cycle-l%02d-%s", layer, suffix),
				Title:     fmt.Sprintf("Cycle diamond layer %02d %s", layer, suffix),
				Status:    types.StatusOpen,
				Priority:  2,
				IssueType: types.TypeTask,
			}
			if layer < layers-1 {
				issue.Dependencies = []*types.Dependency{
					{DependsOnID: fmt.Sprintf("bench-perf-cycle-l%02d-a", layer+1), Type: types.DepBlocks},
					{DependsOnID: fmt.Sprintf("bench-perf-cycle-l%02d-b", layer+1), Type: types.DepBlocks},
				}
			}
			issues = append(issues, issue)
		}
	}
	createBenchIssueBatch(b, store, issues)

	ctx := context.Background()
	dep := &types.Dependency{
		IssueID:     "bench-perf-cycle-source",
		DependsOnID: "bench-perf-cycle-l00-a",
		Type:        types.DepBlocks,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.AddDependency(ctx, dep, "bench"); err != nil {
			b.Fatalf("AddDependency cycle check: %v", err)
		}
		b.StopTimer()
		if err := store.RemoveDependency(ctx, dep.IssueID, dep.DependsOnID, "bench"); err != nil {
			b.Fatalf("RemoveDependency cycle check fixture reset: %v", err)
		}
		if i < b.N-1 {
			b.StartTimer()
		}
	}
}

func BenchmarkPerfReadyWorkLimited_LargeBlockedGraph(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	const (
		readyCount        = 100
		blockedPairCount  = 2500
		estimatedRowCount = readyCount + 2*blockedPairCount
	)
	issues := make([]*types.Issue, 0, estimatedRowCount)
	for i := 0; i < readyCount; i++ {
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-ready-clear-%04d", i),
			Title:     fmt.Sprintf("Ready issue %04d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		})
	}
	for i := 0; i < blockedPairCount; i++ {
		blockerID := fmt.Sprintf("bench-perf-ready-blocker-%05d", i)
		issues = append(issues, &types.Issue{
			ID:        blockerID,
			Title:     fmt.Sprintf("Ready blocker %05d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		})
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-ready-blocked-%05d", i),
			Title:     fmt.Sprintf("Blocked ready issue %05d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: blockerID, Type: types.DepBlocks},
			},
		})
	}
	createBenchIssueBatch(b, store, issues)

	ctx := context.Background()
	filter := types.WorkFilter{Limit: 50, SortPolicy: types.SortPolicyPriority}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := store.GetReadyWork(ctx, filter)
		if err != nil {
			b.Fatalf("GetReadyWork limited blocked graph: %v", err)
		}
		if len(results) != filter.Limit {
			b.Fatalf("GetReadyWork limited blocked graph returned %d issues, want %d", len(results), filter.Limit)
		}
	}
}

func BenchmarkPerfReadyWorkLimited_ExampleOrgWispHeavy(b *testing.B) {
	const (
		wispCount    = 8000
		readyLimit   = 20
		gcAssignee   = "example-org/workflows.codex-min-11"
		gcRoute      = "example-org/workflows.codex-min-11"
		routeJSON    = `{"route.routed_to":"example-org/workflows.codex-min-11"}`
		emptyJSON    = `{}`
		closedStatus = types.StatusClosed
	)
	future := time.Now().UTC().Add(24 * time.Hour)

	makeWisp := func(id string, priority int, assignee string, metadata string, deferUntil *time.Time) *types.Issue {
		return &types.Issue{
			ID:         id,
			Title:      id,
			Status:     types.StatusOpen,
			Priority:   priority,
			IssueType:  types.TypeTask,
			Assignee:   assignee,
			Ephemeral:  true,
			Metadata:   []byte(metadata),
			DeferUntil: deferUntil,
		}
	}

	cases := []struct {
		name   string
		filter types.WorkFilter
		issues func() []*types.Issue
	}{
		{
			name: "AssignedDenseLimit20Of8000",
			filter: types.WorkFilter{
				Assignee:         ptrString(gcAssignee),
				IncludeEphemeral: true,
				Limit:            readyLimit,
				SortPolicy:       types.SortPolicyPriority,
			},
			issues: func() []*types.Issue {
				issues := make([]*types.Issue, 0, wispCount)
				for i := 0; i < wispCount; i++ {
					issues = append(issues, makeWisp(
						fmt.Sprintf("bench-perf-gc-assigned-wisp-%05d", i),
						(i%4)+1,
						gcAssignee,
						emptyJSON,
						nil,
					))
				}
				return issues
			},
		},
		{
			name: "MetadataRouteDenseLimit20Of8000",
			filter: types.WorkFilter{
				Unassigned:       true,
				MetadataFields:   map[string]string{"route.routed_to": gcRoute},
				IncludeEphemeral: true,
				Limit:            readyLimit,
				SortPolicy:       types.SortPolicyPriority,
			},
			issues: func() []*types.Issue {
				issues := make([]*types.Issue, 0, wispCount)
				for i := 0; i < wispCount; i++ {
					issues = append(issues, makeWisp(
						fmt.Sprintf("bench-perf-gc-routed-wisp-%05d", i),
						(i%4)+1,
						"",
						routeJSON,
						nil,
					))
				}
				return issues
			},
		},
		{
			name: "AssignedSparseAfterDeferredLimit20Of8000",
			filter: types.WorkFilter{
				Assignee:         ptrString(gcAssignee),
				IncludeEphemeral: true,
				Limit:            readyLimit,
				SortPolicy:       types.SortPolicyPriority,
			},
			issues: func() []*types.Issue {
				issues := make([]*types.Issue, 0, wispCount)
				for i := 0; i < wispCount-readyLimit; i++ {
					issues = append(issues, makeWisp(
						fmt.Sprintf("bench-perf-gc-deferred-wisp-%05d", i),
						1,
						gcAssignee,
						emptyJSON,
						&future,
					))
				}
				for i := wispCount - readyLimit; i < wispCount; i++ {
					issues = append(issues, makeWisp(
						fmt.Sprintf("bench-perf-gc-ready-tail-wisp-%05d", i),
						4,
						gcAssignee,
						emptyJSON,
						nil,
					))
				}
				return issues
			},
		},
		{
			name: "AssignedClosedNoiseLimit20Of8000",
			filter: types.WorkFilter{
				Assignee:         ptrString(gcAssignee),
				IncludeEphemeral: true,
				Limit:            readyLimit,
				SortPolicy:       types.SortPolicyPriority,
			},
			issues: func() []*types.Issue {
				issues := make([]*types.Issue, 0, wispCount)
				for i := 0; i < wispCount-readyLimit; i++ {
					wisp := makeWisp(
						fmt.Sprintf("bench-perf-gc-closed-wisp-%05d", i),
						1,
						gcAssignee,
						emptyJSON,
						nil,
					)
					wisp.Status = closedStatus
					issues = append(issues, wisp)
				}
				for i := wispCount - readyLimit; i < wispCount; i++ {
					issues = append(issues, makeWisp(
						fmt.Sprintf("bench-perf-gc-ready-closed-noise-wisp-%05d", i),
						4,
						gcAssignee,
						emptyJSON,
						nil,
					))
				}
				return issues
			},
		},
	}

	ctx := context.Background()
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			store, cleanup := setupBenchStore(b)
			defer cleanup()
			createBenchIssueBatch(b, store, tc.issues())
			b.ReportAllocs()
			b.ReportMetric(float64(wispCount), "wisps")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				results, err := store.GetReadyWork(ctx, tc.filter)
				if err != nil {
					b.Fatalf("GetReadyWork example-org wisps: %v", err)
				}
				if len(results) != readyLimit {
					b.Fatalf("GetReadyWork example-org wisps returned %d issues, want %d", len(results), readyLimit)
				}
			}
		})
	}
}

func BenchmarkPerfBlockedIssues_ClosedDependencySkew(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	const (
		closedPairCount = 2500
		activePairCount = 10
	)
	issues := make([]*types.Issue, 0, 2*(closedPairCount+activePairCount))
	for i := 0; i < closedPairCount; i++ {
		blockerID := fmt.Sprintf("bench-perf-closed-blocker-%05d", i)
		issues = append(issues, &types.Issue{
			ID:        blockerID,
			Title:     fmt.Sprintf("Closed blocker %05d", i),
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
		})
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-closed-blocked-%05d", i),
			Title:     fmt.Sprintf("Closed blocked issue %05d", i),
			Status:    types.StatusClosed,
			Priority:  2,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: blockerID, Type: types.DepBlocks},
			},
		})
	}
	for i := 0; i < activePairCount; i++ {
		blockerID := fmt.Sprintf("bench-perf-active-blocker-%02d", i)
		issues = append(issues, &types.Issue{
			ID:        blockerID,
			Title:     fmt.Sprintf("Active blocker %02d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		})
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-active-blocked-%02d", i),
			Title:     fmt.Sprintf("Active blocked issue %02d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: blockerID, Type: types.DepBlocks},
			},
		})
	}
	createBenchIssueBatch(b, store, issues)

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := store.GetBlockedIssues(ctx, types.WorkFilter{})
		if err != nil {
			b.Fatalf("GetBlockedIssues closed skew: %v", err)
		}
		if len(results) != activePairCount {
			b.Fatalf("GetBlockedIssues closed skew returned %d issues, want %d", len(results), activePairCount)
		}
	}
}

func BenchmarkPerfReadyWorkDeferredParentExclusion_5K(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	future := time.Now().UTC().Add(24 * time.Hour)
	const (
		readyCount          = 100
		deferredParentCount = 5000
		deferredChildCount  = 50
	)
	issues := make([]*types.Issue, 0, readyCount+deferredParentCount+deferredChildCount)
	for i := 0; i < readyCount; i++ {
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-deferred-ready-%03d", i),
			Title:     fmt.Sprintf("Ready non-deferred issue %03d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
		})
	}
	for i := 0; i < deferredParentCount; i++ {
		issues = append(issues, &types.Issue{
			ID:         fmt.Sprintf("bench-perf-deferred-parent-%05d", i),
			Title:      fmt.Sprintf("Future deferred parent %05d", i),
			Status:     types.StatusOpen,
			Priority:   2,
			IssueType:  types.TypeTask,
			DeferUntil: &future,
		})
	}
	for i := 0; i < deferredChildCount; i++ {
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-deferred-child-%03d", i),
			Title:     fmt.Sprintf("Child of deferred parent %03d", i),
			Status:    types.StatusOpen,
			Priority:  1,
			IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{
				{DependsOnID: fmt.Sprintf("bench-perf-deferred-parent-%05d", i), Type: types.DepParentChild},
			},
		})
	}
	createBenchIssueBatch(b, store, issues)

	ctx := context.Background()
	filter := types.WorkFilter{Limit: 50, SortPolicy: types.SortPolicyPriority}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := store.GetReadyWork(ctx, filter)
		if err != nil {
			b.Fatalf("GetReadyWork deferred parent exclusion: %v", err)
		}
		if len(results) != filter.Limit {
			b.Fatalf("GetReadyWork deferred parent exclusion returned %d issues, want %d", len(results), filter.Limit)
		}
	}
}

func BenchmarkPerfGetIssuePrimaryFirst_PermanentWithWisps(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	const wispCount = 5000
	issues := make([]*types.Issue, 0, wispCount+1)
	issues = append(issues, &types.Issue{
		ID:        "bench-perf-get-primary",
		Title:     "Permanent issue fetched from primary table",
		Status:    types.StatusOpen,
		Priority:  1,
		IssueType: types.TypeTask,
	})
	for i := 0; i < wispCount; i++ {
		issues = append(issues, &types.Issue{
			ID:        fmt.Sprintf("bench-perf-get-wisp-%04d", i),
			Title:     fmt.Sprintf("Wisp noise issue %04d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		})
	}
	createBenchIssueBatch(b, store, issues)

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		issue, err := store.GetIssue(ctx, "bench-perf-get-primary")
		if err != nil {
			b.Fatalf("GetIssue permanent with wisps: %v", err)
		}
		if issue == nil || issue.ID != "bench-perf-get-primary" {
			b.Fatalf("GetIssue permanent with wisps returned %v", issue)
		}
	}
}

// =============================================================================
// Label Benchmarks
// =============================================================================

// BenchmarkAddLabel measures label addition performance.
func BenchmarkAddLabel(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create test issue
	issue := &types.Issue{
		ID:        "label-bench",
		Title:     "Label Bench Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
		b.Fatalf("failed to create issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.AddLabel(ctx, issue.ID, fmt.Sprintf("label-%d", i), "bench"); err != nil {
			b.Fatalf("failed to add label: %v", err)
		}
	}
}

// BenchmarkGetLabels measures label retrieval performance.
func BenchmarkGetLabels(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ctx := context.Background()

	// Create issue with multiple labels
	issue := &types.Issue{
		ID:        "labels-bench",
		Title:     "Labels Bench Issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
		b.Fatalf("failed to create issue: %v", err)
	}

	for i := 0; i < 20; i++ {
		if err := store.AddLabel(ctx, issue.ID, fmt.Sprintf("label-%d", i), "bench"); err != nil {
			b.Fatalf("failed to add label: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.GetLabels(ctx, issue.ID)
		if err != nil {
			b.Fatalf("failed to get labels: %v", err)
		}
	}
}

// =============================================================================
// WispIDSet mixed-ID routing benchmarks (be-nu4.2.1 / D2)
// =============================================================================

// seedMixedForWispSetBench populates the store with N issues at the requested
// wisp share. IDs are returned in the order created (perms first, then wisps)
// so benchmarks can shuffle if needed. Callers are responsible for cleanup.
func seedMixedForWispSetBench(b *testing.B, store *DoltStore, totalN int, wispShare float64) []string {
	b.Helper()
	ctx := context.Background()
	numWisps := int(float64(totalN) * wispShare)
	numPerms := totalN - numWisps

	ids := make([]string, 0, totalN)
	for i := 0; i < numPerms; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("ws-perm-%d", i),
			Title:     "perm",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "bench"); err != nil {
			b.Fatalf("create perm %d: %v", i, err)
		}
		ids = append(ids, iss.ID)
	}
	for i := 0; i < numWisps; i++ {
		iss := &types.Issue{
			Title:     "wisp",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := store.CreateIssue(ctx, iss, "bench"); err != nil {
			b.Fatalf("create wisp %d: %v", i, err)
		}
		ids = append(ids, iss.ID)
	}
	return ids
}

func benchmarkGetLabelsForIssuesMixed(b *testing.B, totalN int) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ids := seedMixedForWispSetBench(b, store, totalN, 0.25)

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.GetLabelsForIssues(ctx, ids); err != nil {
			b.Fatalf("GetLabelsForIssues: %v", err)
		}
	}
}

func BenchmarkGetLabelsForIssues_Mixed1K(b *testing.B)  { benchmarkGetLabelsForIssuesMixed(b, 1000) }
func BenchmarkGetLabelsForIssues_Mixed10K(b *testing.B) { benchmarkGetLabelsForIssuesMixed(b, 10000) }
func BenchmarkGetLabelsForIssues_Mixed50K(b *testing.B) { benchmarkGetLabelsForIssuesMixed(b, 50000) }

func benchmarkGetIssuesByIDsMixed(b *testing.B, totalN int) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	ids := seedMixedForWispSetBench(b, store, totalN, 0.25)

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.GetIssuesByIDs(ctx, ids); err != nil {
			b.Fatalf("GetIssuesByIDs: %v", err)
		}
	}
}

func BenchmarkGetIssuesByIDs_Mixed1K(b *testing.B)  { benchmarkGetIssuesByIDsMixed(b, 1000) }
func BenchmarkGetIssuesByIDs_Mixed10K(b *testing.B) { benchmarkGetIssuesByIDsMixed(b, 10000) }
func BenchmarkGetIssuesByIDs_Mixed50K(b *testing.B) { benchmarkGetIssuesByIDsMixed(b, 50000) }

// =============================================================================
// WispIDSet scoped-query benchmarks (be-rgm / small-N against large-W)
// =============================================================================
//
// These benchmarks target the case maphew flagged on PR #3453:
// a small hydration batch (N input IDs) against a wisps table of W rows.
// The pre-be-rgm unscoped `SELECT id FROM wisps` scaled O(W); the
// scoped `SELECT id FROM wisps WHERE id IN (?…)` should scale O(N·log W)
// instead, so a small N against a large W should be materially cheaper
// than the full-scan implementation.
//
// Seed shape: a large wisps table (W rows) plus a handful of permanent
// issues (≈ inputN/2, just enough to build a mixed-input batch). The
// input batch is inputN/2 perms + inputN/2 sampled wisps, so only a
// tiny fraction of the wisp table is actually hydrated each call.

// seedSmallNLargeW seeds permCount permanent issues and wispCount active
// wisps and returns (permIDs, wispIDs) in creation order.
func seedSmallNLargeW(b *testing.B, store *DoltStore, permCount, wispCount int) (permIDs, wispIDs []string) {
	b.Helper()
	ctx := context.Background()

	permIDs = make([]string, 0, permCount)
	for i := 0; i < permCount; i++ {
		iss := &types.Issue{
			ID:        fmt.Sprintf("smN-perm-%d", i),
			Title:     "perm",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, iss, "bench"); err != nil {
			b.Fatalf("create perm %d: %v", i, err)
		}
		permIDs = append(permIDs, iss.ID)
	}
	wispIDs = make([]string, 0, wispCount)
	for i := 0; i < wispCount; i++ {
		iss := &types.Issue{
			Title:     "wisp",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Ephemeral: true,
		}
		if err := store.CreateIssue(ctx, iss, "bench"); err != nil {
			b.Fatalf("create wisp %d: %v", i, err)
		}
		wispIDs = append(wispIDs, iss.ID)
	}
	return permIDs, wispIDs
}

// mixedInputSmallN returns a mixed input slice of size inputN: half
// permanents (from permIDs) + half wisps (from wispIDs). The callers
// want half/half so both the perm and wisp branches of the partition
// exercise the IN-clause.
func mixedInputSmallN(permIDs, wispIDs []string, inputN int) []string {
	input := make([]string, 0, inputN)
	half := inputN / 2
	for i := 0; i < half && i < len(permIDs); i++ {
		input = append(input, permIDs[i])
	}
	for i := 0; i < inputN-len(input) && i < len(wispIDs); i++ {
		input = append(input, wispIDs[i])
	}
	return input
}

// benchmarkGetLabelsForIssuesSmallN runs GetLabelsForIssues against a
// seeded store where wispW wisps exist but only inputN input IDs are
// hydrated. Half the input is permanent and half is wisps.
func benchmarkGetLabelsForIssuesSmallN(b *testing.B, wispW, inputN int) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	permIDs, wispIDs := seedSmallNLargeW(b, store, inputN/2+1, wispW)
	input := mixedInputSmallN(permIDs, wispIDs, inputN)

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.GetLabelsForIssues(ctx, input); err != nil {
			b.Fatalf("GetLabelsForIssues: %v", err)
		}
	}
}

// BenchmarkGetLabelsForIssues_SmallNLargeW_10_5K measures the canonical
// regression case: 10 input IDs against 5 000 wisps in the table.
func BenchmarkGetLabelsForIssues_SmallNLargeW_10_5K(b *testing.B) {
	benchmarkGetLabelsForIssuesSmallN(b, 5000, 10)
}

// BenchmarkGetLabelsForIssues_SmallNLargeW_10_10K stresses the same
// shape against a bigger wisp table so the O(W) full-scan regression
// would be obvious in wall time.
func BenchmarkGetLabelsForIssues_SmallNLargeW_10_10K(b *testing.B) {
	benchmarkGetLabelsForIssuesSmallN(b, 10000, 10)
}

// BenchmarkGetLabelsForIssues_SmallNLargeW_100_5K checks a slightly
// larger N to confirm the scaled-N·log(W) path still beats full scan.
func BenchmarkGetLabelsForIssues_SmallNLargeW_100_5K(b *testing.B) {
	benchmarkGetLabelsForIssuesSmallN(b, 5000, 100)
}

// benchmarkGetIssuesByIDsSmallN mirrors benchmarkGetLabelsForIssuesSmallN
// for the issue-hydration caller.
func benchmarkGetIssuesByIDsSmallN(b *testing.B, wispW, inputN int) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()

	permIDs, wispIDs := seedSmallNLargeW(b, store, inputN/2+1, wispW)
	input := mixedInputSmallN(permIDs, wispIDs, inputN)

	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.GetIssuesByIDs(ctx, input); err != nil {
			b.Fatalf("GetIssuesByIDs: %v", err)
		}
	}
}

func BenchmarkGetIssuesByIDs_SmallNLargeW_10_5K(b *testing.B) {
	benchmarkGetIssuesByIDsSmallN(b, 5000, 10)
}
func BenchmarkGetIssuesByIDs_SmallNLargeW_10_10K(b *testing.B) {
	benchmarkGetIssuesByIDsSmallN(b, 10000, 10)
}
func BenchmarkGetIssuesByIDs_SmallNLargeW_100_5K(b *testing.B) {
	benchmarkGetIssuesByIDsSmallN(b, 5000, 100)
}

// =============================================================================
// Schema Probe Benchmarks
// =============================================================================

// BenchmarkContentHashColumnProbe compares the retired INFORMATION_SCHEMA.COLUMNS
// existence probe against the SHOW COLUMNS probe that replaced it.
// schema.MigrateUp's migrationWorkNeeded runs this probe twice on every
// connection. INFORMATION_SCHEMA.COLUMNS forces Dolt to materialize the full
// column catalog because the predicate is not pushed down; SHOW COLUMNS reads a
// single table's schema directly. The gap measured here understates production:
// on a multi-database deployment the old probe was measured at 19-60 ms/conn
// versus ~0 ms on the new one.
func BenchmarkContentHashColumnProbe(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()
	ctx := context.Background()

	b.Run("InformationSchema", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var count int
			if err := store.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'content_hash'`,
				"schema_migrations").Scan(&count); err != nil {
				b.Fatalf("information_schema probe: %v", err)
			}
		}
	})

	b.Run("ShowColumns", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			rows, err := store.db.QueryContext(ctx, "SHOW COLUMNS FROM schema_migrations LIKE 'content_hash'")
			if err != nil {
				b.Fatalf("show columns probe: %v", err)
			}
			for rows.Next() { //nolint:revive // draining the result set is the point
			}
			_ = rows.Close()
		}
	})
}
