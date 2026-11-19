//go:build cgo

// Real-store contract tests for the batch external-ref history capability
// (bd-vcpyl7, GH#6605): PreviousExternalRefs must agree with the single-id
// oracle issueops.PreviousExternalRefInTx on every id, presence included —
// a historical NULL is a present empty string and an issue deleted before
// the cutoff still resolves from its committed history. Both Dolt backends
// run the same fixture matrix: a relink (NULL then set), a multi-value ref,
// a deletion, merged ancestry, an issue created after the cutoff, an
// uncommitted-only issue, a never-existing id, a cutoff before all history,
// and a cutoff exactly on a commit date.

package tracker

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// previousRefsOracle resolves every id through the single-id oracle
// issueops.PreviousExternalRefInTx, the reference implementation the batch
// must match (AC-3).
func previousRefsOracle(ctx context.Context, t *testing.T, db *sql.DB, ids []string, asOf time.Time) map[string]string {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	refs := make(map[string]string, len(ids))
	for _, id := range ids {
		ref, found, err := issueops.PreviousExternalRefInTx(ctx, tx, id, asOf)
		if err != nil {
			t.Fatalf("PreviousExternalRefInTx(%s): %v", id, err)
		}
		if found {
			refs[id] = ref
		}
	}
	return refs
}

// assertPreviousRefsEqual fails unless batch agrees with oracle on presence
// and value for every id.
func assertPreviousRefsEqual(t *testing.T, cutoff time.Time, batch, oracle map[string]string) {
	t.Helper()
	for id, want := range oracle {
		got, ok := batch[id]
		if !ok {
			t.Errorf("cutoff %s: batch is missing %s, which the oracle resolves to %q", cutoff.UTC(), id, want)
			continue
		}
		if got != want {
			t.Errorf("cutoff %s: batch[%s] = %q, want the oracle's %q", cutoff.UTC(), id, got, want)
		}
	}
	for id := range batch {
		if _, ok := oracle[id]; !ok {
			t.Errorf("cutoff %s: batch holds %s -> %q, which the oracle does not resolve at all", cutoff.UTC(), id, batch[id])
		}
	}
}

// assertPreviousRefs pins a few known answers on top of the oracle
// comparison, so a wrong fixture cannot make both sides wrong identically.
func assertPreviousRefs(t *testing.T, cutoff time.Time, refs map[string]string, want map[string]string) {
	t.Helper()
	for id, wantRef := range want {
		gotRef, ok := refs[id]
		if !ok {
			t.Errorf("cutoff %s: %s missing, want %q", cutoff.UTC(), id, wantRef)
			continue
		}
		if gotRef != wantRef {
			t.Errorf("cutoff %s: refs[%s] = %q, want %q", cutoff.UTC(), id, gotRef, wantRef)
		}
	}
	for id := range refs {
		if _, ok := want[id]; !ok {
			t.Errorf("cutoff %s: refs holds %s -> %q, want it absent", cutoff.UTC(), id, refs[id])
		}
	}
}

// previousRefsFixtureIDs are the ids every real-store fixture asks about.
var previousRefsFixtureIDs = []string{
	"zz-unchanged", "zz-relink", "zz-nullref", "zz-multivalue", "zz-delete",
	"zz-after", "zz-uncommitted", "zz-never",
}

// previousRefsFixtureIssue builds a fixture issue with the given external
// ref; nil means a NULL column.
func previousRefsFixtureIssue(id string, externalRef *string) *types.Issue {
	return &types.Issue{
		ID:          id,
		Title:       "fixture " + id,
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: externalRef,
	}
}

func refValue(s string) *string { return &s }

// TestEmbeddedPreviousExternalRefsMatchSingleIDOracle runs the fixture
// matrix on the embedded backend (AC-3). Commit dates come from
// @@dolt_committer_date on a pinned raw connection while the store is
// closed — the embedded engine admits one holder at a time — so the fixture
// needs no wall-clock sleeps. The batch and the oracle are queried through
// separate opens, which is also the reopen-per-transaction shape the
// embedded store really runs with.
func TestEmbeddedPreviousExternalRefsMatchSingleIDOracle(t *testing.T) {
	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	const database = "zz6605"
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	// Commit instants, all in the past relative to the wall clock so the
	// store's own later commit (dated at the wall clock) sorts after them.
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	t0 := base.Add(-10 * time.Minute) // before all history
	t1 := base                        // exact-cutoff inclusion
	t2 := base.Add(10 * time.Minute)
	mid := base.Add(15 * time.Minute)
	t3 := base.Add(20 * time.Minute)
	t5 := base.Add(30 * time.Minute)
	t6 := base.Add(40 * time.Minute)

	// Phase 1: the store creates the issues, uncommitted.
	store, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("Open embedded store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "zz"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	for _, issue := range []*types.Issue{
		previousRefsFixtureIssue("zz-unchanged", refValue("EXT-U")),
		previousRefsFixtureIssue("zz-multivalue", refValue("A")),
		previousRefsFixtureIssue("zz-delete", refValue("EXT-D")),
		previousRefsFixtureIssue("zz-relink", nil),
		previousRefsFixtureIssue("zz-nullref", nil),
	} {
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close store before raw fixture commits: %v", err)
	}

	// Phase 2: a raw connection dates and commits the fixture, merges a
	// side branch back in, and computes the oracle for the fixture cutoffs.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("db.Conn: %v", err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("fixture exec %q: %v", query, err)
		}
	}
	setDate := func(at time.Time) {
		t.Helper()
		value := ""
		if !at.IsZero() {
			value = at.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		}
		exec("SET @@dolt_committer_date = ?", value)
	}
	setDate(t1)
	exec("CALL DOLT_COMMIT('-Am', 'fixture: t1')")
	exec("UPDATE issues SET external_ref = ? WHERE id = ?", "EXT-R", "zz-relink")
	exec("UPDATE issues SET external_ref = ? WHERE id = ?", "B", "zz-multivalue")
	setDate(t2)
	exec("CALL DOLT_COMMIT('-Am', 'fixture: t2')")
	exec("UPDATE issues SET external_ref = ? WHERE id = ?", "C", "zz-multivalue")
	exec("DELETE FROM issues WHERE id = ?", "zz-delete")
	setDate(t3)
	exec("CALL DOLT_COMMIT('-Am', 'fixture: t3')")
	// Merged ancestry: zz-unchanged changes on a side branch and merges
	// back into main.
	setDate(t5)
	exec("CALL DOLT_CHECKOUT('-b', 'zzother')")
	exec("UPDATE issues SET external_ref = ? WHERE id = ?", "MERGED", "zz-unchanged")
	exec("CALL DOLT_COMMIT('-Am', 'fixture: t5 other')")
	exec("CALL DOLT_CHECKOUT('main')")
	setDate(t6)
	exec("CALL DOLT_MERGE('zzother', '--no-ff', '-m', 'fixture: merge')")
	exec("CALL DOLT_BRANCH('-d', 'zzother')")
	setDate(time.Time{})

	oracle := make(map[time.Time]map[string]string)
	for _, cutoff := range []time.Time{t0, t1, mid, t3} {
		oracle[cutoff] = previousRefsOracle(ctx, t, db, previousRefsFixtureIDs, cutoff)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close fixture connection: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup raw handle: %v", err)
	}

	// Phase 3: the store commits one issue after every fixture date (dated
	// at the wall clock, so after t6) and leaves one uncommitted.
	store, err = embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("reopen embedded store: %v", err)
	}
	if err := store.CreateIssue(ctx, previousRefsFixtureIssue("zz-after", refValue("AFTER")), "tester"); err != nil {
		t.Fatalf("CreateIssue(zz-after): %v", err)
	}
	if err := store.Commit(ctx, "fixture: after"); err != nil {
		t.Fatalf("Commit(after): %v", err)
	}
	if err := store.CreateIssue(ctx, previousRefsFixtureIssue("zz-uncommitted", refValue("UNC")), "tester"); err != nil {
		t.Fatalf("CreateIssue(zz-uncommitted): %v", err)
	}
	late := time.Now().UTC().Add(time.Minute) // after the wall-clock commit

	for _, cutoff := range []time.Time{t0, t1, mid, t3} {
		refs, err := store.PreviousExternalRefs(ctx, previousRefsFixtureIDs, cutoff)
		if err != nil {
			t.Fatalf("PreviousExternalRefs at %s: %v", cutoff.UTC(), err)
		}
		assertPreviousRefsEqual(t, cutoff, refs, oracle[cutoff])
	}
	lateRefs, err := store.PreviousExternalRefs(ctx, previousRefsFixtureIDs, late)
	if err != nil {
		t.Fatalf("PreviousExternalRefs at %s: %v", late.UTC(), err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}

	// Phase 4: the late-cutoff oracle, on a fresh raw handle.
	db, cleanup, err = embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
	if err != nil {
		t.Fatalf("OpenSQL for the late oracle: %v", err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Fatalf("cleanup raw handle: %v", err)
		}
	}()
	assertPreviousRefsEqual(t, late, lateRefs, previousRefsOracle(ctx, t, db, previousRefsFixtureIDs, late))

	// Known answers, so a broken fixture cannot pass by being wrong on
	// both sides identically.
	assertPreviousRefs(t, t0, oracle[t0], map[string]string{})
	assertPreviousRefs(t, t1, oracle[t1], map[string]string{
		"zz-unchanged":  "EXT-U",
		"zz-relink":     "", // a NULL column is a present empty string
		"zz-nullref":    "",
		"zz-multivalue": "A",
		"zz-delete":     "EXT-D",
	})
	assertPreviousRefs(t, mid, oracle[mid], map[string]string{
		"zz-unchanged":  "EXT-U",
		"zz-relink":     "EXT-R",
		"zz-nullref":    "",
		"zz-multivalue": "B",
		"zz-delete":     "EXT-D",
	})
	assertPreviousRefs(t, t3, oracle[t3], map[string]string{
		"zz-unchanged":  "EXT-U",
		"zz-relink":     "EXT-R",
		"zz-nullref":    "",
		"zz-multivalue": "C",
		"zz-delete":     "EXT-D", // deleted at t3, its committed history remains
	})
	assertPreviousRefs(t, late, lateRefs, map[string]string{
		"zz-unchanged":  "MERGED", // through the merged side branch
		"zz-relink":     "EXT-R",
		"zz-nullref":    "",
		"zz-multivalue": "C",
		"zz-delete":     "EXT-D",
		"zz-after":      "AFTER",
	})
}

// TestDoltServerPreviousExternalRefsMatchSingleIDOracle runs the same
// fixture matrix on the Dolt server backend (AC-3). Commit dates come from
// @@dolt_committer_date on the store's pooled session, the way the
// compaction gate test drives them.
func TestDoltServerPreviousExternalRefsMatchSingleIDOracle(t *testing.T) {
	store := newTestStore(t) // skips if the shared test Dolt server is absent
	defer store.Close()
	ctx := context.Background()
	db := store.DB()

	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	t0 := base.Add(-10 * time.Minute)
	t1 := base
	t2 := base.Add(10 * time.Minute)
	mid := base.Add(15 * time.Minute)
	t3 := base.Add(20 * time.Minute)
	t4 := base.Add(25 * time.Minute)
	t5 := base.Add(30 * time.Minute)
	t6 := base.Add(40 * time.Minute)
	late := base.Add(50 * time.Minute)

	setDate := func(at time.Time) {
		t.Helper()
		value := ""
		if !at.IsZero() {
			value = at.UTC().Format("2006-01-02T15:04:05.000Z07:00")
		}
		if _, err := db.ExecContext(ctx, "SET @@dolt_committer_date = ?", value); err != nil {
			t.Fatalf("set committer date: %v", err)
		}
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("fixture exec %q: %v", query, err)
		}
	}
	// The server store auto-commits every write (doltAddAndCommit), and each
	// DOLT_COMMIT consumes @@dolt_committer_date, so every dated fixture
	// write sets the date for its own commit. store.Commit would be a no-op
	// here — the working set is always clean — and would only leave the
	// date for the next write.
	updateRefAt := func(at time.Time, id, ref string) {
		t.Helper()
		setDate(at)
		if err := store.UpdateIssue(ctx, id, map[string]interface{}{"external_ref": ref}, "tester"); err != nil {
			t.Fatalf("UpdateIssue(%s): %v", id, err)
		}
	}
	createAt := func(at time.Time, issue *types.Issue) {
		t.Helper()
		setDate(at)
		if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
			t.Fatalf("CreateIssue(%s): %v", issue.ID, err)
		}
	}

	for _, issue := range []*types.Issue{
		previousRefsFixtureIssue("zz-unchanged", refValue("EXT-U")),
		previousRefsFixtureIssue("zz-multivalue", refValue("A")),
		previousRefsFixtureIssue("zz-delete", refValue("EXT-D")),
		previousRefsFixtureIssue("zz-relink", nil),
		previousRefsFixtureIssue("zz-nullref", nil),
	} {
		createAt(t1, issue)
	}
	updateRefAt(t2, "zz-relink", "EXT-R")
	updateRefAt(t2, "zz-multivalue", "B")
	updateRefAt(t3, "zz-multivalue", "C")
	setDate(t3)
	if err := store.DeleteIssue(ctx, "zz-delete"); err != nil {
		t.Fatalf("DeleteIssue(zz-delete): %v", err)
	}
	createAt(t4, previousRefsFixtureIssue("zz-after", refValue("AFTER")))

	// Merged ancestry: zz-unchanged changes on a side branch of this test's
	// isolated branch and merges back in.
	var activeBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&activeBranch); err != nil {
		t.Fatalf("read active branch: %v", err)
	}
	setDate(t5)
	exec("CALL DOLT_CHECKOUT('-b', 'zzother')")
	exec("UPDATE issues SET external_ref = ? WHERE id = ?", "MERGED", "zz-unchanged")
	exec("CALL DOLT_COMMIT('-Am', 'fixture: t5 other')")
	exec("CALL DOLT_CHECKOUT(?)", activeBranch)
	setDate(t6)
	exec("CALL DOLT_MERGE('zzother', '--no-ff', '-m', 'fixture: merge')")
	exec("CALL DOLT_BRANCH('-d', 'zzother')")
	setDate(time.Time{})

	// After-every-cutoff: created at the wall clock (the session date is
	// reset), past `late`, so no cutoff ever resolves it.
	if err := store.CreateIssue(ctx, previousRefsFixtureIssue("zz-uncommitted", refValue("UNC")), "tester"); err != nil {
		t.Fatalf("CreateIssue(zz-uncommitted): %v", err)
	}

	for _, cutoff := range []time.Time{t0, t1, mid, t3, late} {
		refs, err := store.PreviousExternalRefs(ctx, previousRefsFixtureIDs, cutoff)
		if err != nil {
			t.Fatalf("PreviousExternalRefs at %s: %v", cutoff.UTC(), err)
		}
		oracle := previousRefsOracle(ctx, t, db, previousRefsFixtureIDs, cutoff)
		assertPreviousRefsEqual(t, cutoff, refs, oracle)

		var want map[string]string
		switch cutoff {
		case t0:
			want = map[string]string{}
		case t1:
			want = map[string]string{
				"zz-unchanged":  "EXT-U",
				"zz-relink":     "", // a NULL column is a present empty string
				"zz-nullref":    "",
				"zz-multivalue": "A",
				"zz-delete":     "EXT-D",
			}
		case mid:
			want = map[string]string{
				"zz-unchanged":  "EXT-U",
				"zz-relink":     "EXT-R",
				"zz-nullref":    "",
				"zz-multivalue": "B",
				"zz-delete":     "EXT-D",
			}
		case t3:
			want = map[string]string{
				"zz-unchanged":  "EXT-U",
				"zz-relink":     "EXT-R",
				"zz-nullref":    "",
				"zz-multivalue": "C",
				"zz-delete":     "EXT-D", // deleted at t3, history remains
			}
		case late:
			want = map[string]string{
				"zz-unchanged":  "MERGED", // through the merged side branch
				"zz-relink":     "EXT-R",
				"zz-nullref":    "",
				"zz-multivalue": "C",
				"zz-delete":     "EXT-D",
				"zz-after":      "AFTER",
			}
		}
		assertPreviousRefs(t, cutoff, refs, want)
		if refs == nil && len(want) > 0 {
			t.Fatalf("cutoff %s: PreviousExternalRefs returned a nil map", cutoff.UTC())
		}
	}

	// The batch call with no ids opens no history query and answers empty.
	refs, err := store.PreviousExternalRefs(ctx, nil, late)
	if err != nil {
		t.Fatalf("PreviousExternalRefs(no ids): %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("PreviousExternalRefs(no ids) = %v, want empty", refs)
	}
}

// TestEmbeddedPreviousExternalRefsSeeHistoryCommittedAfterAnEarlierCall pins
// freshness (AC-5): the batch answers must come from the store's reachable
// history at call time, never from a per-cutoff memo of an earlier answer.
// The same id and the same cutoff are asked twice, with a second writer
// committing a new historical row (dated before the cutoff) in between; the
// second answer must see it. On the embedded backend the second writer is a
// raw connection while the store is closed — the one-holder engine — dated
// with @@dolt_committer_date, no wall-clock sleeps.
func TestEmbeddedPreviousExternalRefsSeeHistoryCommittedAfterAnEarlierCall(t *testing.T) {
	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	const database = "zz6605"
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	cutoff := time.Now().UTC().Add(time.Minute) // after every fixture commit

	// The store creates the issue; a raw connection commits it at a past
	// date below the cutoff.
	store, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("Open embedded store: %v", err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", "zz"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.CreateIssue(ctx, previousRefsFixtureIssue("zz-fresh", refValue("OLD")), "tester"); err != nil {
		t.Fatalf("CreateIssue(zz-fresh): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close store: %v", err)
	}
	commitRawAt := func(at time.Time, updates map[string]string) {
		t.Helper()
		db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() {
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup raw handle: %v", err)
			}
		}()
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("db.Conn: %v", err)
		}
		if _, err := conn.ExecContext(ctx, "SET @@dolt_committer_date = ?",
			at.UTC().Format("2006-01-02T15:04:05.000Z07:00")); err != nil {
			t.Fatalf("set committer date: %v", err)
		}
		for id, ref := range updates {
			if _, err := conn.ExecContext(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", ref, id); err != nil {
				t.Fatalf("update %s: %v", id, err)
			}
		}
		if _, err := conn.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'fixture: fresh history')"); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := conn.Close(); err != nil {
			t.Fatalf("close raw connection: %v", err)
		}
	}
	commitRawAt(time.Now().UTC().Add(-20*time.Minute), nil)

	ask := func() string {
		t.Helper()
		store, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
		if err != nil {
			t.Fatalf("reopen embedded store: %v", err)
		}
		defer func() { _ = store.Close() }()
		refs, err := store.PreviousExternalRefs(ctx, []string{"zz-fresh"}, cutoff)
		if err != nil {
			t.Fatalf("PreviousExternalRefs: %v", err)
		}
		return refs["zz-fresh"]
	}

	if got := ask(); got != "OLD" {
		t.Fatalf("first batch call = %q, want OLD: the fixture must hold the baseline", got)
	}
	// A second writer commits a newer historical row, still below the
	// cutoff.
	commitRawAt(time.Now().UTC().Add(-10*time.Minute), map[string]string{"zz-fresh": "NEW"})
	if got := ask(); got != "NEW" {
		t.Fatalf("second batch call = %q, want NEW: the batch must read the history committed after the first call, not a memoized answer", got)
	}
}

// TestDoltServerPreviousExternalRefsSeeHistoryCommittedAfterAnEarlierCall is
// the server backend's half of the freshness pin (AC-5): the same id and
// cutoff asked twice, with a second commit landing between the calls through
// the store's pooled session, must answer the new history the second time.
func TestDoltServerPreviousExternalRefsSeeHistoryCommittedAfterAnEarlierCall(t *testing.T) {
	store := newTestStore(t) // skips if the shared test Dolt server is absent
	defer store.Close()
	ctx := context.Background()
	db := store.DB()

	if err := store.CreateIssue(ctx, previousRefsFixtureIssue("zz-fresh", refValue("OLD")), "tester"); err != nil {
		t.Fatalf("CreateIssue(zz-fresh): %v", err)
	}
	// The create auto-commits at the wall clock, so the second commit must
	// be dated after it and still below the cutoff.
	created := time.Now().UTC()
	cutoff := created.Add(time.Minute)
	ask := func() string {
		t.Helper()
		refs, err := store.PreviousExternalRefs(ctx, []string{"zz-fresh"}, cutoff)
		if err != nil {
			t.Fatalf("PreviousExternalRefs: %v", err)
		}
		return refs["zz-fresh"]
	}
	if got := ask(); got != "OLD" {
		t.Fatalf("first batch call = %q, want OLD: the fixture must hold the baseline", got)
	}

	// A second commit lands between the calls, dated after the create and
	// below the cutoff.
	if _, err := db.ExecContext(ctx, "SET @@dolt_committer_date = ?",
		created.Add(30*time.Second).UTC().Format("2006-01-02T15:04:05.000Z07:00")); err != nil {
		t.Fatalf("set committer date: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE issues SET external_ref = ? WHERE id = ?", "NEW", "zz-fresh"); err != nil {
		t.Fatalf("update zz-fresh: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'fixture: fresh history')"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := db.ExecContext(ctx, "SET @@dolt_committer_date = ?", ""); err != nil {
		t.Fatalf("reset committer date: %v", err)
	}

	if got := ask(); got != "NEW" {
		t.Fatalf("second batch call = %q, want NEW: the batch must read the history committed after the first call, not a memoized answer", got)
	}
}
