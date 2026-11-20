package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/searchcountsequiv"
	"github.com/steveyegge/beads/internal/types"
)

// BenchmarkPerfSearchWithCountsLabelPage times the label-filtered counts
// search a list page runs on the store-backed seam (#7635): one label,
// newest first, a 50-row page plus the probe row (LIMIT 51), over a
// wisp-heavy store, and the same read with no bound. Each shape runs twice:
//
//   - ids-first: DoltStore.SearchIssuesWithCounts, end to end, as shipped.
//   - predicate: searchcountsequiv.PredicateFormSearch, the pre-#7635 read —
//     every leg in the predicate form of sqlbuild.SearchCountsSQL — on the
//     same store, so one run gives the before and the after.
//
// The fixture is the shape the issue measured, scaled by the wisp count:
// molecules of 8 wisps (a root and 7 steps, each step a parent-child child of
// the root and blocked by the previous step), 2–4 labels per row from a pool
// of 100, the target label on about 1 wisp in 19, half the roots under a
// durable issue, one issue per 15 wisps, comments, and a few leases.
//
// It needs a dedicated, throwaway dolt sql-server (BEADS_BENCH_DOLT_PORT; see
// setupBenchStore). Production servers run with table statistics off, and
// that is the condition to measure first; start the server with
//
//	system_variables:
//	  dolt_stats_enabled: 0
//	  dolt_stats_paused: 1
//	  dolt_stats_memory_only: 1
//
// in its --config file. BEADS_BENCH_COUNTS_ANALYZE=1 runs ANALYZE TABLE
// after the seed instead, for a server that keeps statistics.
// BEADS_BENCH_COUNTS_WISPS sets the wisp count (default 2000); 45960 is the
// fixture the issue's numbers come from:
//
//	BEADS_BENCH_DOLT_PORT=<port> BEADS_BENCH_COUNTS_WISPS=45960 \
//	  go test -tags gms_pure_go ./internal/storage/dolt/ -run '^$' \
//	  -bench 'BenchmarkPerfSearchWithCountsLabelPage' -benchtime 20x -v
func BenchmarkPerfSearchWithCountsLabelPage(b *testing.B) {
	store, cleanup := setupBenchStore(b)
	defer cleanup()
	ctx := context.Background()

	wisps := 2000
	if v := os.Getenv("BEADS_BENCH_COUNTS_WISPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < countsBenchMolecule {
			b.Fatalf("BEADS_BENCH_COUNTS_WISPS=%q: want an integer >= %d", v, countsBenchMolecule)
		}
		wisps = n
	}
	seeded := time.Now()
	targets := seedCountsBench(ctx, b, store.db, wisps)
	if os.Getenv("BEADS_BENCH_COUNTS_ANALYZE") == "1" {
		if _, err := store.db.ExecContext(ctx, "ANALYZE TABLE issues, wisps, labels, wisp_labels, dependencies, wisp_dependencies, comments, wisp_comments, leases"); err != nil {
			b.Fatalf("analyze: %v", err)
		}
	}
	var statRows int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_statistics").Scan(&statRows); err != nil {
		b.Logf("dolt_statistics: %v", err)
	}
	b.Logf("fixture: %d wisps (%d carry the label), %d issues, seeded in %s; dolt_statistics rows: %d",
		wisps, targets, wisps/15, time.Since(seeded).Round(time.Millisecond), statRows)

	predicate := func(ctx context.Context, f types.IssueFilter) ([]*types.IssueWithCounts, error) {
		tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback() }()
		return searchcountsequiv.PredicateFormSearch(ctx, tx, "", f)
	}
	idsFirst := func(ctx context.Context, f types.IssueFilter) ([]*types.IssueWithCounts, error) {
		return store.SearchIssuesWithCounts(ctx, "", f)
	}

	for _, page := range []struct {
		name  string
		limit int
	}{{"limit=51", 51}, {"unbounded", 0}} {
		filter := types.IssueFilter{Labels: []string{countsBenchLabel}, SortBy: "created", Limit: page.limit}
		for _, path := range []struct {
			name   string
			search func(context.Context, types.IssueFilter) ([]*types.IssueWithCounts, error)
		}{{"ids-first", idsFirst}, {"predicate", predicate}} {
			b.Run(page.name+"/"+path.name, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					rows, err := path.search(ctx, filter)
					if err != nil {
						b.Fatalf("search: %v", err)
					}
					if len(rows) == 0 {
						b.Fatal("search returned no rows")
					}
				}
			})
		}
	}
}

const (
	countsBenchMolecule  = 8
	countsBenchLabelPool = 100
	countsBenchLabel     = "order-run:target"
	countsBenchBatch     = 500
)

// seedCountsBench writes the fixture with multi-row INSERTs in one
// transaction and returns how many wisps carry the target label.
func seedCountsBench(ctx context.Context, tb testing.TB, db *sql.DB, wisps int) int {
	tb.Helper()
	rng := rand.New(rand.NewSource(1)) //nolint:gosec // deterministic fixture
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	insert := func(table, cols, row string, rows [][]any) {
		for start := 0; start < len(rows); start += countsBenchBatch {
			end := min(start+countsBenchBatch, len(rows))
			var args []any
			for _, r := range rows[start:end] {
				args = append(args, r...)
			}
			q := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s", table, cols,
				strings.TrimSuffix(strings.Repeat(row+", ", end-start), ", "))
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				tb.Fatalf("seed %s: %v", table, err)
			}
		}
	}
	status := func(created time.Time) (string, any) {
		switch r := rng.Intn(100); {
		case r < 80:
			return "closed", created.Add(time.Duration(1+rng.Intn(600)) * time.Second)
		case r < 95:
			return "open", nil
		default:
			return "in_progress", nil
		}
	}
	labels := func(id string, out [][]any) [][]any {
		for _, p := range rng.Perm(countsBenchLabelPool)[:2+rng.Intn(3)] {
			out = append(out, []any{id, fmt.Sprintf("pool-%02d", p)})
		}
		return out
	}

	issues := wisps / 15
	issueID := func(i int) string { return fmt.Sprintf("bi-%06d", i) }
	var rows, labelRows, depRows [][]any
	for i := 0; i < issues; i++ {
		created := base.Add(time.Duration(i) * time.Minute)
		st, closed := status(created)
		rows = append(rows, []any{issueID(i), "issue " + issueID(i), st, rng.Intn(5), created, created, closed})
		labelRows = labels(issueID(i), labelRows)
		if rng.Intn(125) == 0 {
			labelRows = append(labelRows, []any{issueID(i), countsBenchLabel})
		}
		if i%10 != 0 {
			depRows = append(depRows, []any{issueID(i), issueID(i - i%10), "parent-child", created})
		}
	}
	insert("issues", "id, title, description, design, acceptance_criteria, notes, status, priority, issue_type, created_at, updated_at, closed_at, created_by",
		"(?, ?, '', '', '', '', ?, ?, 'task', ?, ?, ?, 'bench')", rows)
	insert("labels", "issue_id, label", "(?, ?)", labelRows)
	insert("dependencies", "id, issue_id, depends_on_issue_id, type, created_at, created_by", "(UUID(), ?, ?, ?, ?, 'bench')", depRows)

	wispID := func(i int) string { return fmt.Sprintf("bw-%06d", i) }
	wbase := base.Add(time.Duration(issues) * time.Minute)
	rows, labelRows, depRows = nil, nil, nil
	var toIssue, comments, leases [][]any
	targets := 0
	for i := 0; i < wisps; i++ {
		created := wbase.Add(time.Duration(i) * time.Second)
		st, closed := status(created)
		rows = append(rows, []any{wispID(i), "wisp " + wispID(i), st, rng.Intn(5), created, created, closed})
		labelRows = labels(wispID(i), labelRows)
		if rng.Intn(19) == 0 {
			labelRows = append(labelRows, []any{wispID(i), countsBenchLabel})
			targets++
		}
		switch pos := i % countsBenchMolecule; {
		case pos == 0:
			if issues > 0 && rng.Intn(2) == 0 {
				toIssue = append(toIssue, []any{wispID(i), issueID(rng.Intn(issues)), "parent-child", created})
			}
		default:
			depRows = append(depRows, []any{wispID(i), wispID(i - pos), "parent-child", created})
			if pos >= 2 {
				depRows = append(depRows, []any{wispID(i), wispID(i - 1), "blocks", created})
			}
		}
		if i%15 == 0 {
			comments = append(comments, []any{wispID(rng.Intn(wisps)), created})
		}
		if i%400 == 0 {
			leases = append(leases, []any{wispID(i), created, created.Add(time.Hour), created})
		}
	}
	insert("wisps", "id, title, status, priority, issue_type, created_at, updated_at, closed_at, ephemeral, created_by",
		"(?, ?, ?, ?, 'task', ?, ?, ?, 1, 'bench')", rows)
	insert("wisp_labels", "issue_id, label", "(?, ?)", labelRows)
	insert("wisp_dependencies", "id, issue_id, depends_on_wisp_id, type, created_at, created_by", "(UUID(), ?, ?, ?, ?, 'bench')", depRows)
	insert("wisp_dependencies", "id, issue_id, depends_on_issue_id, type, created_at, created_by", "(UUID(), ?, ?, ?, ?, 'bench')", toIssue)
	insert("wisp_comments", "id, issue_id, author, text, created_at", "(UUID(), ?, 'bench', 'a wisp comment', ?)", comments)
	insert("leases", "issue_id, holder, granted_at, lease_expires_at, heartbeat_at", "(?, 'bench', ?, ?, ?)", leases)
	if err := tx.Commit(); err != nil {
		tb.Fatalf("seed commit: %v", err)
	}
	return targets
}
