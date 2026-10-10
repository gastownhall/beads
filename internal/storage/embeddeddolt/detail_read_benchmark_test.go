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
	"github.com/steveyegge/beads/issueops"
)

// BenchmarkEmbeddedDetailReads measures detail reads and the short-lived engine
// lifecycle against a real embedded Dolt store.
//
//   - lifecycle_only opens OpenSQL and runs its cleanup without an application
//     query: one connector/engine open-close cycle, including OpenSQL's ping,
//     database selection and branch setup, but no transaction.
//   - get_issue reads one issue: one store call and one open-close cycle.
//   - reader_get/N=1, N=10 and N=50 perform N default Reader.Get calls. Each Get
//     makes six store calls (GetIssue, GetLabels, GetDependenciesWithMetadata,
//     CountDependents, CountDependencies, CountIssueComments), hence six
//     connector/engine open-close cycles, or 6N cycles per operation.
//   - list_page/N=50 returns one default Reader.List page: three configuration
//     store calls plus SearchIssuesWithCounts, hence four open-close cycles.
//
// Lifecycle counts are derived from source, not measured by this benchmark.
// Workload: BEADS_BENCH_ISSUES issues (default 200), each carrying two labels;
// about 30% carry one or two blocks dependencies on earlier issues. One batch
// create and one seed commit run outside timing. Read/page sizes are clamped
// to the fixture size. Set BEADS_TEST_EMBEDDED_DOLT=1 to run.
func BenchmarkEmbeddedDetailReads(b *testing.B) {
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

	issues := make([]*types.Issue, issueCount)
	ids := make([]string, issueCount)
	for i := range issueCount {
		ids[i] = fmt.Sprintf("bench-%04d", i)
		issue := &types.Issue{
			ID:        ids[i],
			Title:     fmt.Sprintf("Bench issue %d", i),
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Labels:    []string{fmt.Sprintf("area-%d", i%7), fmt.Sprintf("tier-%d", i%3)},
		}
		if i%10 >= 7 {
			for j := range 1 + i%2 {
				issue.Dependencies = append(issue.Dependencies, &types.Dependency{
					IssueID:     issue.ID,
					DependsOnID: ids[i-1-j],
					Type:        types.DepBlocks,
				})
			}
		}
		issues[i] = issue
	}
	if err := store.CreateIssues(ctx, issues, "bench"); err != nil {
		b.Fatalf("CreateIssues: %v", err)
	}
	if err := store.Commit(ctx, "bench seed"); err != nil {
		b.Fatalf("commit seed: %v", err)
	}
	reader, err := store.IssueReader()
	if err != nil {
		b.Fatalf("IssueReader: %v", err)
	}

	// Open derives its engine data directory beneath the .beads root.
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	b.Run("lifecycle_only", func(b *testing.B) {
		for b.Loop() {
			_, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "bench", "main")
			if err != nil {
				b.Fatalf("OpenSQL: %v", err)
			}
			if err := cleanup(); err != nil {
				b.Fatalf("close OpenSQL: %v", err)
			}
		}
		b.ReportMetric(1, "opens/op")
		b.ReportMetric(b.Elapsed().Seconds()*1000/float64(b.N), "ms/open")
	})

	b.Run("get_issue", func(b *testing.B) {
		next := 0
		for b.Loop() {
			id := ids[next]
			next = (next + 1) % issueCount
			issue, err := store.GetIssue(ctx, id)
			if err != nil {
				b.Fatalf("GetIssue %s: %v", id, err)
			}
			if issue == nil || issue.ID != id {
				b.Fatalf("GetIssue %s: wrong or missing issue", id)
			}
		}
		b.ReportMetric(1, "reads/op")
		b.ReportMetric(b.Elapsed().Seconds()*1000/float64(b.N), "ms/read")
	})

	for _, size := range []int{1, 10, 50} {
		n := min(size, issueCount)
		b.Run(fmt.Sprintf("reader_get/N=%d", size), func(b *testing.B) {
			next := 0
			for b.Loop() {
				for range n {
					id := ids[next]
					next = (next + 1) % issueCount
					issue, err := reader.Get(ctx, issueops.GetRequest{ID: id})
					if err != nil {
						b.Fatalf("Reader.Get %s: %v", id, err)
					}
					if issue == nil || issue.ID != id {
						b.Fatalf("Reader.Get %s: wrong or missing issue", id)
					}
				}
			}
			b.ReportMetric(float64(n), "reads/op")
			b.ReportMetric(b.Elapsed().Seconds()*1000/(float64(b.N)*float64(n)), "ms/read")
		})
	}

	b.Run("list_page/N=50", func(b *testing.B) {
		n := min(50, issueCount)
		for b.Loop() {
			page, err := reader.List(ctx, issueops.ListRequest{Limit: &n})
			if err != nil {
				b.Fatalf("Reader.List: %v", err)
			}
			if len(page.Items) != n {
				b.Fatalf("Reader.List rows = %d, want %d", len(page.Items), n)
			}
		}
		b.ReportMetric(float64(n), "rows/op")
		b.ReportMetric(b.Elapsed().Seconds()*1000/(float64(b.N)*float64(n)), "ms/row")
	})
}
