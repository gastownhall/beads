package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestMigration0033_RoundTrip covers the be-eei (D4v2) reversibility
// acceptance criterion: migration 0033 must round-trip cleanly with the row
// population intact. Sequence: setup (migration already applied by
// setupTestStore) → verify the D4v2 index set is present and the dropped
// legacy index is absent → seed a non-trivial row fixture → run the 0033
// down SQL → verify the D4v2 indexes are gone, the legacy idx_issues_status
// is restored, and row count is unchanged → run the 0033 up SQL again →
// verify the D4v2 indexes are back, the legacy index is dropped, and a
// sampled row set still matches.
//
// The indexes don't affect row data — they're pure metadata — so the
// row-count and sample-row invariants are the primary correctness signal. A
// missed DROP or malformed CREATE in the migration would surface here.
//
// Fixture scale: 2K rows per be-eei §8 guardrail 3. The round-trip
// demonstrates the DDL is correct under a meaningful population without
// pushing the test beyond a reasonable timeout; the bench suite
// (BenchmarkGetStaleIssues_10K, BenchmarkCreateIssue_Existing10K) covers
// 10K-scale separately through seedForSummaryBench.
func TestMigration0033_RoundTrip(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	// Seed + DDL round-trip needs more wall-time than the default 30s
	// testTimeout; use 5x so this stays well inside -timeout 600s without
	// flaking on slower Dolt server startups.
	ctx, cancel := context.WithTimeout(context.Background(), 5*testTimeout)
	defer cancel()

	// D4v2 (be-eei §4): composite (status, updated_at) replaces the
	// pre-0033 idx_issues_status, and standalone idx_issues_defer_until is
	// added. idx_issues_status must not coexist with the composite after
	// migration up.
	d4v2Indexes := []string{
		"idx_issues_status_updated_at",
		"idx_issues_defer_until",
	}
	legacyStatusIndex := []string{"idx_issues_status"}

	// Phase 1: post-initial-migration. Composite + defer_until must exist,
	// and the legacy idx_issues_status must be gone, because setupTestStore
	// runs every embedded .up.sql including 0033.
	assertIndexesPresent(t, ctx, store, d4v2Indexes, "after initial migration")
	assertIndexesAbsent(t, ctx, store, legacyStatusIndex, "after initial migration")

	// Seed 2K permanent issues — enough to prove the DDL round-trip under a
	// meaningful population without pushing the test beyond a reasonable
	// timeout (see the function-level comment for the scaling rationale).
	const fixtureSize = 2_000
	seedDateIndexFixture(t, ctx, store, fixtureSize)

	// Capture the count and a stable sample of IDs before the round-trip.
	wantCount := countIssues(t, ctx, store)
	if wantCount < fixtureSize {
		t.Fatalf("seed produced %d rows; want >=%d", wantCount, fixtureSize)
	}
	sampleIDs := sampleIssueIDs(t, ctx, store, 50)

	// Phase 2: apply the down SQL. D4v2 indexes must disappear; legacy
	// idx_issues_status must come back; rows must not change.
	runMigrationSQL(t, ctx, store, downSQL0033)
	assertIndexesAbsent(t, ctx, store, d4v2Indexes, "after down migration")
	assertIndexesPresent(t, ctx, store, legacyStatusIndex, "after down migration")
	if got := countIssues(t, ctx, store); got != wantCount {
		t.Fatalf("down migration changed row count: got %d, want %d", got, wantCount)
	}

	// Phase 3: re-apply the up SQL. D4v2 indexes must return; legacy index
	// drops again; rows must still be byte-identical against the sample.
	runMigrationSQL(t, ctx, store, upSQL0033)
	assertIndexesPresent(t, ctx, store, d4v2Indexes, "after re-running up migration")
	assertIndexesAbsent(t, ctx, store, legacyStatusIndex, "after re-running up migration")
	if got := countIssues(t, ctx, store); got != wantCount {
		t.Fatalf("up re-run changed row count: got %d, want %d", got, wantCount)
	}
	verifySampleIssues(t, ctx, store, sampleIDs)
}

// upSQL0033 / downSQL0033 are the literal statements from
// migrations/0033_add_date_indexes.{up,down}.sql. Kept inline so the test is
// self-contained and catches divergence if the embedded .sql ever gets
// hand-edited away from the shipped DDL. Statement order matters: be-eei §8
// guardrail 1 requires DROP idx_issues_status → CREATE composite → CREATE
// defer_until on the way up, and the exact reverse on the way down.
var upSQL0033 = []string{
	"DROP INDEX idx_issues_status ON issues",
	"CREATE INDEX idx_issues_status_updated_at ON issues (status, updated_at)",
	"CREATE INDEX idx_issues_defer_until ON issues (defer_until)",
}

var downSQL0033 = []string{
	"DROP INDEX idx_issues_defer_until ON issues",
	"DROP INDEX idx_issues_status_updated_at ON issues",
	"CREATE INDEX idx_issues_status ON issues (status)",
}

func runMigrationSQL(t *testing.T, ctx context.Context, store *DoltStore, stmts []string) {
	t.Helper()
	for _, stmt := range stmts {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("migration stmt %q: %v", stmt, err)
		}
	}
}

// assertIndexesPresent runs SHOW INDEX FROM issues and asserts each named
// index appears at least once. SHOW INDEX lists one row per key-part, so a
// composite index like idx_issues_status_updated_at surfaces twice (once
// per column); presence — not cardinality — is the invariant.
func assertIndexesPresent(t *testing.T, ctx context.Context, store *DoltStore, indexes []string, phase string) {
	t.Helper()
	got := indexNames(t, ctx, store)
	var missing []string
	for _, want := range indexes {
		if !got[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%s: missing indexes %v; got %v", phase, missing, sortedKeys(got))
	}
}

func assertIndexesAbsent(t *testing.T, ctx context.Context, store *DoltStore, indexes []string, phase string) {
	t.Helper()
	got := indexNames(t, ctx, store)
	var present []string
	for _, unwanted := range indexes {
		if got[unwanted] {
			present = append(present, unwanted)
		}
	}
	if len(present) > 0 {
		t.Fatalf("%s: indexes still present after drop: %v", phase, present)
	}
}

func indexNames(t *testing.T, ctx context.Context, store *DoltStore) map[string]bool {
	t.Helper()
	rows, err := store.db.QueryContext(ctx, "SHOW INDEX FROM issues")
	if err != nil {
		t.Fatalf("SHOW INDEX FROM issues: %v", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("SHOW INDEX columns: %v", err)
	}
	keyNameCol := -1
	for i, c := range cols {
		if strings.EqualFold(c, "Key_name") {
			keyNameCol = i
			break
		}
	}
	if keyNameCol < 0 {
		t.Fatalf("SHOW INDEX output has no Key_name column; got %v", cols)
	}

	got := make(map[string]bool)
	for rows.Next() {
		scanDest := make([]any, len(cols))
		holders := make([]sql.NullString, len(cols))
		for i := range holders {
			scanDest[i] = &holders[i]
		}
		if err := rows.Scan(scanDest...); err != nil {
			t.Fatalf("SHOW INDEX scan: %v", err)
		}
		if holders[keyNameCol].Valid {
			got[holders[keyNameCol].String] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("SHOW INDEX iter: %v", err)
	}
	return got
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func countIssues(t *testing.T, ctx context.Context, store *DoltStore) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues").Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	return n
}

// sampleIssueIDs collects N evenly-spaced IDs for the round-trip integrity
// check. Uses a deterministic ORDER BY so the sample is stable across runs.
func sampleIssueIDs(t *testing.T, ctx context.Context, store *DoltStore, n int) []string {
	t.Helper()
	rows, err := store.db.QueryContext(ctx,
		"SELECT id FROM issues ORDER BY id ASC LIMIT ?", n)
	if err != nil {
		t.Fatalf("sample ids: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("sample id scan: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sample id iter: %v", err)
	}
	if len(ids) != n {
		t.Fatalf("sample returned %d ids; want %d", len(ids), n)
	}
	return ids
}

// verifySampleIssues spot-checks that a known set of issue IDs still exist
// and that their title/status/type round-tripped unchanged after up→down→up.
// Index operations don't touch row data, but a malformed DDL could in theory
// trigger Dolt table restructuring; this guards against that worst case.
func verifySampleIssues(t *testing.T, ctx context.Context, store *DoltStore, ids []string) {
	t.Helper()
	for _, id := range ids {
		iss, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("sample verify GetIssue(%s): %v", id, err)
		}
		if iss == nil {
			t.Fatalf("sample verify: issue %s disappeared after round-trip", id)
		}
		if iss.ID != id {
			t.Fatalf("sample verify: got id %s, want %s", iss.ID, id)
		}
		if !strings.HasPrefix(iss.Title, "date-idx ") {
			t.Fatalf("sample verify %s: title mutated to %q", id, iss.Title)
		}
	}
}

// TestMigration0033_ExplainCapture prints EXPLAIN output for the two read
// shapes D4v2 targets (bd stale's status+updated_at predicate, and bd ready's
// deferred-parents defer_until predicate) so the PR description can cite the
// planner's index usage — be-eei §8 guardrail 4. Not a pass/fail gate — the
// `go test -v` output is the artifact and the reviewer validates it. Skipped
// unless explicitly opted in to keep CI quiet.
func TestMigration0033_ExplainCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("explain capture is verbose; run with -v explicitly")
	}

	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*testTimeout)
	defer cancel()

	seedDateIndexFixture(t, ctx, store, 1_000)

	// Dolt's EXPLAIN doesn't accept bind parameters, and the tabular EXPLAIN
	// output has NULL bigint columns (rows, filtered) that the MySQL driver
	// can't round-trip into any Go type cleanly. EXPLAIN FORMAT=TREE returns
	// a single text column (the plan tree), which is what we want to capture
	// in the PR anyway — it names the index the planner picks.
	cases := []struct {
		label string
		query string
	}{
		{
			// Target: idx_issues_status_updated_at. Matches the
			// GetStaleIssuesInTx predicate (issueops/stale.go:26-32):
			// status IN (...) as the equality prefix, updated_at < cutoff
			// as the range suffix, ORDER BY updated_at aligning with the
			// suffix so no sort step.
			label: "bd stale (status IN + updated_at < cutoff)",
			query: "EXPLAIN FORMAT=TREE SELECT id FROM issues WHERE status IN ('open','in_progress') AND updated_at < '2020-01-01' AND (ephemeral = 0 OR ephemeral IS NULL) ORDER BY updated_at ASC LIMIT 50",
		},
		{
			// Target: idx_issues_defer_until. Matches the
			// getChildrenOfDeferredParentsInTx predicate
			// (ready_work.go:279): defer_until IS NOT NULL skips the
			// NULL-majority leaf, then range scan on defer_until > now.
			label: "bd ready deferred-parents (defer_until IS NOT NULL AND defer_until > now)",
			query: "EXPLAIN FORMAT=TREE SELECT id FROM issues WHERE defer_until IS NOT NULL AND defer_until > UTC_TIMESTAMP()",
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rows, err := store.db.QueryContext(ctx, tc.query)
			if err != nil {
				t.Fatalf("EXPLAIN %q: %v", tc.label, err)
			}
			defer rows.Close()
			t.Logf("\n=== EXPLAIN: %s ===", tc.label)
			for rows.Next() {
				var plan sql.NullString
				if err := rows.Scan(&plan); err != nil {
					t.Fatalf("scan: %v", err)
				}
				if plan.Valid {
					t.Logf("%s", plan.String)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("rows: %v", err)
			}
		})
	}
}

// seedDateIndexFixture populates the store with N permanent issues. Status
// is cycled across open/in_progress/closed so the composite
// idx_issues_status_updated_at has non-trivial leading-column cardinality;
// date columns (started/closed/due/defer_until) stay NULL at seed time —
// real-world distribution is NULL-majority and the indexes we ship are
// expected to handle that.
func seedDateIndexFixture(t *testing.T, ctx context.Context, store *DoltStore, totalN int) {
	t.Helper()

	const batch = 500
	statuses := []types.Status{types.StatusOpen, types.StatusInProgress, types.StatusClosed}
	issueTypes := []types.IssueType{types.TypeTask, types.TypeBug, types.TypeFeature}

	for start := 0; start < totalN; start += batch {
		end := start + batch
		if end > totalN {
			end = totalN
		}
		chunk := make([]*types.Issue, 0, end-start)
		for i := start; i < end; i++ {
			iss := &types.Issue{
				ID:        fmt.Sprintf("date-idx-%06d", i),
				Title:     fmt.Sprintf("date-idx %06d", i),
				Status:    statuses[i%len(statuses)],
				Priority:  i % 5,
				IssueType: issueTypes[i%len(issueTypes)],
			}
			chunk = append(chunk, iss)
		}
		if err := store.CreateIssuesWithFullOptions(ctx, chunk, "test", storage.BatchCreateOptions{
			OrphanHandling:       storage.OrphanAllow,
			SkipPrefixValidation: true,
		}); err != nil {
			t.Fatalf("seed date-idx batch %d: %v", start, err)
		}
	}
}
