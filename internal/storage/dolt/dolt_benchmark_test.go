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
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

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
		Database:        "benchdb",
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
		Database:        "benchdb",
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
	store.Close()

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "bench",
		CommitterEmail:  "bench@example.com",
		Database:        "benchdb",
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
	store.Close()

	cfg := &Config{
		Path:            tmpDir,
		CommitterName:   "bench",
		CommitterEmail:  "bench@example.com",
		Database:        "benchdb",
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
