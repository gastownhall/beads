//go:build bench

package sqlite

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Benchmark size rationale:
// We only benchmark Large (10K) and XLarge (20K) databases because:
// - Small databases (<1K issues) perform acceptably without optimization
// - Performance issues only manifest at scale (10K+ issues)
// - Smaller benchmarks add code weight without providing optimization insights
// - Target users manage repos with thousands of issues, not hundreds

// runBenchmark sets up a benchmark with consistent configuration and runs the provided test function.
// It handles store setup/cleanup, timer management, and allocation reporting uniformly across all benchmarks.
func runBenchmark(b *testing.B, setupFunc func(*testing.B) (*SQLiteStorage, func()), testFunc func(*SQLiteStorage, context.Context) error) {
	b.Helper()

	store, cleanup := setupFunc(b)
	defer cleanup()

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		if err := testFunc(store, ctx); err != nil {
			b.Fatalf("benchmark failed: %v", err)
		}
	}
}

// BenchmarkGetReadyWork_Large benchmarks GetReadyWork on 10K issue database
func BenchmarkGetReadyWork_Large(b *testing.B) {
	runBenchmark(b, setupLargeBenchDB, func(store *SQLiteStorage, ctx context.Context) error {
		_, err := store.GetReadyWork(ctx, types.WorkFilter{})
		return err
	})
}

// BenchmarkGetReadyWork_XLarge benchmarks GetReadyWork on 20K issue database
func BenchmarkGetReadyWork_XLarge(b *testing.B) {
	runBenchmark(b, setupXLargeBenchDB, func(store *SQLiteStorage, ctx context.Context) error {
		_, err := store.GetReadyWork(ctx, types.WorkFilter{})
		return err
	})
}

// BenchmarkSearchIssues_Large_NoFilter benchmarks searching all open issues
func BenchmarkSearchIssues_Large_NoFilter(b *testing.B) {
	openStatus := types.StatusOpen
	filter := types.IssueFilter{
		Status: &openStatus,
	}

	runBenchmark(b, setupLargeBenchDB, func(store *SQLiteStorage, ctx context.Context) error {
		_, err := store.SearchIssues(ctx, "", filter)
		return err
	})
}

// BenchmarkSearchIssues_Large_ComplexFilter benchmarks complex filtered search
func BenchmarkSearchIssues_Large_ComplexFilter(b *testing.B) {
	openStatus := types.StatusOpen
	filter := types.IssueFilter{
		Status:      &openStatus,
		PriorityMin: intPtr(0),
		PriorityMax: intPtr(2),
	}

	runBenchmark(b, setupLargeBenchDB, func(store *SQLiteStorage, ctx context.Context) error {
		_, err := store.SearchIssues(ctx, "", filter)
		return err
	})
}

// BenchmarkCreateIssue_Large benchmarks issue creation in large database
func BenchmarkCreateIssue_Large(b *testing.B) {
	runBenchmark(b, setupLargeBenchDB, func(store *SQLiteStorage, ctx context.Context) error {
		issue := &types.Issue{
			Title:       "Benchmark issue",
			Description: "Test description",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
		}
		return store.CreateIssue(ctx, issue, "bench")
	})
}

// BenchmarkUpdateIssue_Large benchmarks issue updates in large database
func BenchmarkUpdateIssue_Large(b *testing.B) {
	// Setup phase: get an issue to update (not timed)
	store, cleanup := setupLargeBenchDB(b)
	defer cleanup()
	ctx := context.Background()

	openStatus := types.StatusOpen
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{
		Status: &openStatus,
	})
	if err != nil || len(issues) == 0 {
		b.Fatalf("Failed to get issues for update test: %v", err)
	}
	targetID := issues[0].ID

	// Benchmark phase: measure update operations
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		updates := map[string]interface{}{
			"status": types.StatusInProgress,
		}

		if err := store.UpdateIssue(ctx, targetID, updates, "bench"); err != nil {
			b.Fatalf("UpdateIssue failed: %v", err)
		}

		// reset back to open for next iteration
		updates["status"] = types.StatusOpen
		if err := store.UpdateIssue(ctx, targetID, updates, "bench"); err != nil {
			b.Fatalf("UpdateIssue failed: %v", err)
		}
	}
}

// BenchmarkGetReadyWork_FromJSONL benchmarks ready work on JSONL-imported database
func BenchmarkGetReadyWork_FromJSONL(b *testing.B) {
	runBenchmark(b, setupLargeFromJSONL, func(store *SQLiteStorage, ctx context.Context) error {
		_, err := store.GetReadyWork(ctx, types.WorkFilter{})
		return err
	})
}

// Helper function
func intPtr(i int) *int {
	return &i
}
