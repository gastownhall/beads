//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// BenchmarkDetailBatchReads compares a loop of Reader.Get with one GetBatch.
// Fixtures and parity checks run outside the measured sub-benchmarks. The hub
// requests one epic's 1,000 parent-child dependents, not 1,000 subject IDs.
func BenchmarkDetailBatchReads(b *testing.B) {
	b.StopTimer()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		b.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt benchmarks")
	}
	store, err := embeddeddolt.Open(b.Context(), filepath.Join(b.TempDir(), ".beads"), "bench", "main")
	if err != nil {
		b.Fatalf("open embedded Dolt store: %v", err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Errorf("close embedded Dolt store: %v", err)
		}
	})
	if err := store.SetConfig(b.Context(), "issue_prefix", "bench"); err != nil {
		b.Fatalf("set issue_prefix: %v", err)
	}
	ctx := b.Context()
	ids := make([]string, 201)
	issues := make([]*types.Issue, 0, 1202)
	for i := range ids {
		ids[i] = fmt.Sprintf("bench-read-%04d", i)
		issues = append(issues, &types.Issue{
			ID: ids[i], Title: "Detail read subject", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
		})
	}
	const epicID = "bench-hub"
	issues = append(issues, &types.Issue{
		ID: epicID, Title: "Detail read hub", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeEpic,
	})
	for i := range 1000 {
		id := fmt.Sprintf("bench-child-%04d", i)
		issues = append(issues, &types.Issue{
			ID: id, Title: "Hub child", Status: types.StatusOpen,
			Priority: 2, IssueType: types.TypeTask,
			Dependencies: []*types.Dependency{{
				IssueID: id, DependsOnID: epicID, Type: types.DepParentChild,
			}},
		})
	}
	if err := store.CreateIssues(ctx, issues, "bench"); err != nil {
		b.Fatalf("seed detail reads: %v", err)
	}
	if err := store.Commit(ctx, "bench seed"); err != nil {
		b.Fatalf("commit seed: %v", err)
	}
	reader, err := store.IssueReader()
	if err != nil {
		b.Fatalf("IssueReader: %v", err)
	}
	batchReader, err := store.DetailBatchReader()
	if err != nil {
		b.Fatalf("DetailBatchReader: %v", err)
	}
	runCase := func(b *testing.B, request issueops.DetailBatchRequest) {
		b.Helper()
		b.StopTimer()
		getLoop := func(b *testing.B) []*issueops.IssueDetails {
			details := make([]*issueops.IssueDetails, 0, len(request.IDs))
			for _, id := range request.IDs {
				detail, err := reader.Get(ctx, issueops.GetRequest{
					ID: id, IncludeDependents: request.IncludeDependents,
				})
				if err != nil {
					b.Fatalf("Get %s: %v", id, err)
				}
				details = append(details, detail)
			}
			return details
		}
		want := getLoop(b)
		got, err := batchReader.GetBatch(ctx, request)
		if err != nil {
			b.Fatalf("GetBatch: %v", err)
		}
		if len(got.Items) != len(want) {
			b.Fatalf("batch items = %d, want %d", len(got.Items), len(want))
		}
		for i, item := range got.Items {
			if want[i] == nil || !item.Found || item.Issue == nil ||
				item.ID != request.IDs[i] || item.Issue.ID != want[i].ID || want[i].ID != request.IDs[i] {
				b.Fatalf("item %d does not match requested loop ID %s", i, request.IDs[i])
			}
			if request.IncludeDependents {
				if len(want[i].Dependents) != 1000 || len(item.Issue.Dependents) != 1000 {
					b.Fatal("hub must return 1,000 dependents in both arms")
				}
				for j, dependent := range item.Issue.Dependents {
					if dependent.ID != want[i].Dependents[j].ID {
						b.Fatalf("hub dependent %d differs", j)
					}
				}
			}
		}
		b.Run("loop", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = getLoop(b)
			}
		})
		b.Run("batch", func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := batchReader.GetBatch(ctx, request); err != nil {
					b.Fatalf("GetBatch: %v", err)
				}
			}
		})
	}
	for _, n := range []int{1, 10, 50, 201} {
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			runCase(b, issueops.DetailBatchRequest{IDs: ids[:n]})
		})
	}
	b.Run("hub", func(b *testing.B) {
		runCase(b, issueops.DetailBatchRequest{IDs: []string{epicID}, IncludeDependents: true})
	})
}
