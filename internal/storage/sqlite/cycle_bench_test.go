//go:build bench

package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// BenchmarkCycleDetection benchmarks the cycle detection performance
// on various graph sizes and structures
//
// Benchmark Results (Apple M4 Max, 2025-10-16):
//
// Linear chains (sparse):
//   100 issues:   ~3.4ms per AddDependency (with cycle check)
//   1000 issues:  ~3.7ms per AddDependency (with cycle check)
//
// Tree structure (branching factor 3):
//   100 issues:   ~3.3ms per AddDependency
//   1000 issues:  ~3.5ms per AddDependency
//
// Dense graphs (each issue depends on 3-5 previous):
//   100 issues:   Times out (>120s for setup + benchmarking)
//   1000 issues:  Times out
//
// Conclusion:
// - Cycle detection adds ~3-4ms overhead per AddDependency call
// - Performance is acceptable for typical use cases (linear chains, trees)
// - Dense graphs with many dependencies can be slow, but are rare in practice
// - No optimization needed for normal workflows

// BenchmarkCycleDetection_Linear_100 tests linear chain (sparse): bd-1 → bd-2 → bd-3 ... → bd-100
func BenchmarkCycleDetection_Linear_100(b *testing.B) {
	benchmarkCycleDetectionLinear(b, 100)
}

// BenchmarkCycleDetection_Linear_1000 tests linear chain (sparse): bd-1 → bd-2 → ... → bd-1000
func BenchmarkCycleDetection_Linear_1000(b *testing.B) {
	benchmarkCycleDetectionLinear(b, 1000)
}

// BenchmarkCycleDetection_Linear_5000 tests linear chain (sparse): bd-1 → bd-2 → ... → bd-5000
func BenchmarkCycleDetection_Linear_5000(b *testing.B) {
	benchmarkCycleDetectionLinear(b, 5000)
}

// BenchmarkCycleDetection_Dense_100 tests dense graph: each issue depends on 3-5 previous issues
func BenchmarkCycleDetection_Dense_100(b *testing.B) {
	b.Skip("Dense graph setup slow (creates 5*n deps). AddDependency CTE is O(n), not affected by DetectCycles fix.")
	benchmarkCycleDetectionDense(b, 100)
}

// BenchmarkCycleDetection_Dense_1000 tests dense graph with 1000 issues
func BenchmarkCycleDetection_Dense_1000(b *testing.B) {
	b.Skip("Dense graph setup slow (creates 5*n deps). AddDependency CTE is O(n), not affected by DetectCycles fix.")
	benchmarkCycleDetectionDense(b, 1000)
}

// BenchmarkCycleDetection_Tree_100 tests tree structure (branching factor 3)
func BenchmarkCycleDetection_Tree_100(b *testing.B) {
	benchmarkCycleDetectionTree(b, 100)
}

// BenchmarkCycleDetection_Tree_1000 tests tree structure with 1000 issues
func BenchmarkCycleDetection_Tree_1000(b *testing.B) {
	benchmarkCycleDetectionTree(b, 1000)
}

// Helper: Create linear dependency chain
func benchmarkCycleDetectionLinear(b *testing.B, n int) {
	store, cleanup := setupBenchDB(b)
	defer cleanup()
	ctx := context.Background()

	// Create n issues
	issues := make([]*types.Issue, n)
	for i := 0; i < n; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
		issues[i] = issue
	}

	// Create linear chain: each issue depends on the previous one
	for i := 1; i < n; i++ {
		dep := &types.Dependency{
			IssueID:     issues[i].ID,
			DependsOnID: issues[i-1].ID,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "benchmark"); err != nil {
			b.Fatalf("Failed to add dependency: %v", err)
		}
	}

	// Now benchmark adding a dependency that would NOT create a cycle
	// (from the last issue to a new unconnected issue)
	newIssue := &types.Issue{
		Title:     "New issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, newIssue, "benchmark"); err != nil {
		b.Fatalf("Failed to create new issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Add dependency from first issue to new issue (safe, no cycle)
		dep := &types.Dependency{
			IssueID:     issues[0].ID,
			DependsOnID: newIssue.ID,
			Type:        types.DepBlocks,
		}
		// This will run cycle detection on a chain of length n
		_ = store.AddDependency(ctx, dep, "benchmark")
		// Clean up for next iteration
		_ = store.RemoveDependency(ctx, issues[0].ID, newIssue.ID, "benchmark")
	}
}

// Helper: Create dense dependency graph
func benchmarkCycleDetectionDense(b *testing.B, n int) {
	store, cleanup := setupBenchDB(b)
	defer cleanup()
	ctx := context.Background()

	// Create n issues
	issues := make([]*types.Issue, n)
	for i := 0; i < n; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
		issues[i] = issue
	}

	// Create dense graph: each issue (after 5) depends on 3-5 previous issues
	for i := 5; i < n; i++ {
		for j := 1; j <= 5 && i-j >= 0; j++ {
			dep := &types.Dependency{
				IssueID:     issues[i].ID,
				DependsOnID: issues[i-j].ID,
				Type:        types.DepBlocks,
			}
			if err := store.AddDependency(ctx, dep, "benchmark"); err != nil {
				b.Fatalf("Failed to add dependency: %v", err)
			}
		}
	}

	// Benchmark adding a dependency
	newIssue := &types.Issue{
		Title:     "New issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, newIssue, "benchmark"); err != nil {
		b.Fatalf("Failed to create new issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dep := &types.Dependency{
			IssueID:     issues[n/2].ID, // Middle issue
			DependsOnID: newIssue.ID,
			Type:        types.DepBlocks,
		}
		_ = store.AddDependency(ctx, dep, "benchmark")
		_ = store.RemoveDependency(ctx, issues[n/2].ID, newIssue.ID, "benchmark")
	}
}

// Helper: Create tree structure (branching)
func benchmarkCycleDetectionTree(b *testing.B, n int) {
	store, cleanup := setupBenchDB(b)
	defer cleanup()
	ctx := context.Background()

	// Create n issues
	issues := make([]*types.Issue, n)
	for i := 0; i < n; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
		issues[i] = issue
	}

	// Create tree: each issue (after root) depends on parent (branching factor ~3)
	for i := 1; i < n; i++ {
		parent := (i - 1) / 3
		dep := &types.Dependency{
			IssueID:     issues[i].ID,
			DependsOnID: issues[parent].ID,
			Type:        types.DepBlocks,
		}
		if err := store.AddDependency(ctx, dep, "benchmark"); err != nil {
			b.Fatalf("Failed to add dependency: %v", err)
		}
	}

	// Benchmark adding a dependency
	newIssue := &types.Issue{
		Title:     "New issue",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, newIssue, "benchmark"); err != nil {
		b.Fatalf("Failed to create new issue: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dep := &types.Dependency{
			IssueID:     issues[n-1].ID, // Leaf node
			DependsOnID: newIssue.ID,
			Type:        types.DepBlocks,
		}
		_ = store.AddDependency(ctx, dep, "benchmark")
		_ = store.RemoveDependency(ctx, issues[n-1].ID, newIssue.ID, "benchmark")
	}
}

// ============================================================================
// DetectCycles Benchmarks
// These benchmark the DetectCycles function directly (not AddDependency).
// The Go DFS fix changed DetectCycles from O(2^n) to O(V+E).
// ============================================================================

// BenchmarkDetectCycles_Linear_1000 benchmarks DetectCycles on a linear chain
func BenchmarkDetectCycles_Linear_1000(b *testing.B) {
	store, cleanup := setupBenchDB(b)
	defer cleanup()
	ctx := context.Background()

	createLinearGraph(b, store, ctx, 1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.DetectCycles(ctx)
	}
}

// BenchmarkDetectCycles_Dense_500 benchmarks DetectCycles on dense graph
// This was O(2^n) before the fix, now O(V+E)
func BenchmarkDetectCycles_Dense_500(b *testing.B) {
	store, cleanup := setupBenchDB(b)
	defer cleanup()
	ctx := context.Background()

	createDenseGraphDirect(b, store, ctx, 500)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.DetectCycles(ctx)
	}
}

// BenchmarkDetectCycles_Tree_1000 benchmarks DetectCycles on tree structure
func BenchmarkDetectCycles_Tree_1000(b *testing.B) {
	store, cleanup := setupBenchDB(b)
	defer cleanup()
	ctx := context.Background()

	createTreeGraph(b, store, ctx, 1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.DetectCycles(ctx)
	}
}

// createLinearGraph creates n issues with linear chain dependencies
func createLinearGraph(b *testing.B, store *SQLiteStorage, ctx context.Context, n int) []*types.Issue {
	issues := make([]*types.Issue, n)
	for i := 0; i < n; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
		issues[i] = issue
	}

	// Create linear chain using direct SQL (faster than AddDependency)
	for i := 1; i < n; i++ {
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by)
			VALUES (?, ?, 'blocks', datetime('now'), 'bench')
		`, issues[i].ID, issues[i-1].ID)
		if err != nil {
			b.Fatalf("Failed to add dependency: %v", err)
		}
	}

	return issues
}

// createDenseGraphDirect creates n issues with dense deps using direct SQL
// Each issue (after 5) depends on the 5 previous issues
// Uses direct SQL to bypass AddDependency's cycle check (O(n) vs O(n²) setup)
func createDenseGraphDirect(b *testing.B, store *SQLiteStorage, ctx context.Context, n int) []*types.Issue {
	issues := make([]*types.Issue, n)
	for i := 0; i < n; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
		issues[i] = issue
	}

	// Create dense graph using direct SQL (bypasses cycle check during setup)
	for i := 5; i < n; i++ {
		for j := 1; j <= 5 && i-j >= 0; j++ {
			_, err := store.db.ExecContext(ctx, `
				INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by)
				VALUES (?, ?, 'blocks', datetime('now'), 'bench')
			`, issues[i].ID, issues[i-j].ID)
			if err != nil {
				b.Fatalf("Failed to add dependency: %v", err)
			}
		}
	}

	return issues
}

// createTreeGraph creates n issues in tree structure (branching factor 3)
func createTreeGraph(b *testing.B, store *SQLiteStorage, ctx context.Context, n int) []*types.Issue {
	issues := make([]*types.Issue, n)
	for i := 0; i < n; i++ {
		issue := &types.Issue{
			Title:     fmt.Sprintf("Issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "benchmark"); err != nil {
			b.Fatalf("Failed to create issue: %v", err)
		}
		issues[i] = issue
	}

	// Create tree using direct SQL
	for i := 1; i < n; i++ {
		parent := (i - 1) / 3
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO dependencies (issue_id, depends_on_id, type, created_at, created_by)
			VALUES (?, ?, 'blocks', datetime('now'), 'bench')
		`, issues[i].ID, issues[parent].ID)
		if err != nil {
			b.Fatalf("Failed to add dependency: %v", err)
		}
	}

	return issues
}
