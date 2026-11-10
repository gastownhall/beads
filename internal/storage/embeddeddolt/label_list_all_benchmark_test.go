//go:build cgo

package embeddeddolt_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// BenchmarkLabelListAllLabelCounting measures the two ways `bd label list-all`
// can total every label in the database against a real embedded Dolt store.
//
//   - bulk_hydrated is what the command does now: one SearchIssues call, then
//     count from the Issue.Labels that issueops.SearchIssuesInTx already
//     hydrated in bulk.
//   - per_issue_getlabels is what it did before GH#5325: the same SearchIssues
//     call, then Store.GetLabels once per issue. In embedded mode each of
//     those opens a short-lived connector, starts a transaction, runs a wisp
//     probe plus the label SELECT, and closes.
//
// Workload: BEADS_BENCH_ISSUES issues (default 200), each carrying two labels
// drawn from a small shared vocabulary, so both arms return identical counts.
// Set BEADS_TEST_EMBEDDED_DOLT=1 to run.
func BenchmarkLabelListAllLabelCounting(b *testing.B) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		b.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt benchmarks")
	}

	issueCount := 200
	if raw := os.Getenv("BEADS_BENCH_ISSUES"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			b.Fatalf("BEADS_BENCH_ISSUES=%q: want a positive integer", raw)
		}
		issueCount = parsed
	}

	ctx := b.Context()
	beadsDir := filepath.Join(b.TempDir(), ".beads")
	store, err := embeddeddolt.Open(ctx, beadsDir, "bench", "main")
	if err != nil {
		b.Fatalf("open embedded Dolt store: %v", err)
	}
	b.Cleanup(func() {
		if err := store.Close(); err != nil {
			b.Errorf("close embedded Dolt store: %v", err)
		}
	})
	if err := store.SetConfig(ctx, "issue_prefix", "bench"); err != nil {
		b.Fatalf("set issue_prefix: %v", err)
	}

	for i := range issueCount {
		issue := &types.Issue{
			ID:        fmt.Sprintf("bench-%04d", i),
			Title:     fmt.Sprintf("Bench issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
		}
		if err := store.CreateIssue(ctx, issue, "bench"); err != nil {
			b.Fatalf("CreateIssue %s: %v", issue.ID, err)
		}
		for _, label := range []string{fmt.Sprintf("area-%d", i%7), fmt.Sprintf("tier-%d", i%3)} {
			if err := store.AddLabel(ctx, issue.ID, label, "bench"); err != nil {
				b.Fatalf("AddLabel %s/%s: %v", issue.ID, label, err)
			}
		}
	}
	if err := store.Commit(ctx, "bench seed"); err != nil {
		b.Fatalf("commit seed: %v", err)
	}

	wantLabels := min(issueCount, 7) + min(issueCount, 3)

	b.Run("bulk_hydrated", func(b *testing.B) {
		for b.Loop() {
			issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				b.Fatalf("SearchIssues: %v", err)
			}
			counts := make(map[string]int)
			for _, issue := range issues {
				for _, label := range issue.Labels {
					counts[label]++
				}
			}
			if len(counts) != wantLabels {
				b.Fatalf("unique labels = %d, want %d", len(counts), wantLabels)
			}
		}
	})

	b.Run("per_issue_getlabels", func(b *testing.B) {
		for b.Loop() {
			issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
			if err != nil {
				b.Fatalf("SearchIssues: %v", err)
			}
			counts := make(map[string]int)
			for _, issue := range issues {
				labels, err := store.GetLabels(ctx, issue.ID)
				if err != nil {
					b.Fatalf("GetLabels %s: %v", issue.ID, err)
				}
				for _, label := range labels {
					counts[label]++
				}
			}
			if len(counts) != wantLabels {
				b.Fatalf("unique labels = %d, want %d", len(counts), wantLabels)
			}
		}
	})
}
