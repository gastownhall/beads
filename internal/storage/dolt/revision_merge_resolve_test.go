package dolt

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"testing"
)

// colVal is a single column assignment in a branch edit.
type colVal struct{ col, val string }

// revMergeEdit is one branch's edit in a disjoint-column merge-conflict setup: it
// sets each of `sets` plus a fresh revision nonce, and (when updatedAt != "") pins
// updated_at so the two branches can be made to tie or diverge on it deliberately.
// When deleted is true the branch instead deletes the row (a modify/delete setup).
type revMergeEdit struct {
	sets      []colVal
	nonce     int64
	updatedAt string // pinned updated_at literal; "" lets ON UPDATE bump it
	deleted   bool
}

// setupIssuesRevisionMergeConflict seeds one issue (the shared ancestor), then
// forks a peer branch and applies `ours` on the current branch and `theirs` on the
// peer. Each edit stamps its own revision nonce, so merging the peer produces a
// whole-row conflict — the shape TestProbeDoltDisjointCellMergeRevision locked and
// the issues merge resolver settles. It returns the store, the peer branch, and
// the base revision the ancestor carried, leaving the merge for the caller.
func setupIssuesRevisionMergeConflict(t *testing.T, id string, ours, theirs revMergeEdit) (*DoltStore, string, int64) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)
	ctx, cancel := testContext(t)
	t.Cleanup(cancel)
	db := store.db

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}

	// CreateIssue auto-commits the row, so HEAD already carries the base issue —
	// the shared ancestor both branches fork from. No explicit seed commit.
	makeCASIssue(t, ctx, store, id)
	baseRev := revOf(t, ctx, store, id)

	peerBranch := currentBranch + "_peer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	t.Cleanup(func() {
		db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch)
		db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', ?)", peerBranch)
	})

	applyEdit := func(e revMergeEdit) {
		t.Helper()
		if e.deleted {
			if _, err := db.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", id); err != nil {
				t.Fatalf("delete edit: %v", err)
			}
			return
		}
		var setClauses []string
		var args []any
		for _, s := range e.sets {
			setClauses = append(setClauses, s.col+" = ?")
			args = append(args, s.val)
		}
		setClauses = append(setClauses, "revision = ?")
		args = append(args, e.nonce)
		if e.updatedAt != "" {
			setClauses = append(setClauses, "updated_at = ?")
			args = append(args, e.updatedAt)
		}
		args = append(args, id)
		//nolint:gosec // column names are test-controlled.
		if _, err := db.ExecContext(ctx,
			"UPDATE issues SET "+strings.Join(setClauses, ", ")+" WHERE id = ?", args...); err != nil {
			t.Fatalf("edit %v: %v", e.sets, err)
		}
	}

	applyEdit(ours)
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'ours edit')"); err != nil {
		t.Fatalf("commit ours: %v", err)
	}

	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	applyEdit(theirs)
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'theirs edit')"); err != nil {
		t.Fatalf("commit theirs: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}
	return store, peerBranch, baseRev
}

// mergeAndResolveIssues merges peerBranch on the current branch inside a tx with
// commit-conflicts allowed, runs the auto-resolver, and reports whether it
// resolved. A declined resolve rolls the tx back (restoring the pre-merge state),
// mirroring mergeAndResolveSchemaMigrations.
func mergeAndResolveIssues(t *testing.T, store *DoltStore, peerBranch string) bool {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		// Some Dolt versions report the conflict as a merge error; the resolver
		// inspects dolt_conflicts regardless.
		t.Logf("merge returned: %v", err)
	}

	resolved, err := store.tryAutoResolveMergeConflicts(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("resolver error: %v", err)
	}
	if !resolved {
		_ = tx.Rollback()
		return false
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after resolve: %v", err)
	}
	return true
}

func readIssueString(t *testing.T, store *DoltStore, id, col string) string {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	var v sql.NullString
	//nolint:gosec // col is a test-controlled column name.
	if err := store.db.QueryRowContext(ctx, "SELECT "+col+" FROM issues WHERE id = ?", id).Scan(&v); err != nil {
		t.Fatalf("read issues.%s for %s: %v", col, id, err)
	}
	return v.String
}

// TestTryAutoResolveMergeConflicts_IssuesRevisionOnly is the core assertion: two
// clones edit DISJOINT columns of the same bead (title vs status), each stamping
// its own revision nonce, and pin updated_at to the same value so revision is the
// SOLE both-sides conflict. The resolver rebuilds the row cell-wise so BOTH edits
// survive and revision becomes a fresh third value — a bare --ours would have
// dropped the peer's status edit and kept our nonce.
func TestTryAutoResolveMergeConflicts_IssuesRevisionOnly(t *testing.T) {
	const id = "rev-merge-1"
	const nonceA, nonceB = int64(111111111), int64(222222222)
	const sharedUpdatedAt = "2020-01-01 00:00:00"

	store, peerBranch, baseRev := setupIssuesRevisionMergeConflict(t, id,
		revMergeEdit{sets: []colVal{{"title", "titleA"}}, nonce: nonceA, updatedAt: sharedUpdatedAt},
		revMergeEdit{sets: []colVal{{"status", "in_progress"}}, nonce: nonceB, updatedAt: sharedUpdatedAt})

	if !mergeAndResolveIssues(t, store, peerBranch) {
		t.Fatal("expected a revision-only disjoint-column conflict to be auto-resolved")
	}

	if got := readIssueString(t, store, id, "title"); got != "titleA" {
		t.Errorf("title after resolve = %q, want ours %q", got, "titleA")
	}
	if got := readIssueString(t, store, id, "status"); got != "in_progress" {
		t.Errorf("status after resolve = %q, want theirs %q", got, "in_progress")
	}
	rev := revOf(t, context.Background(), store, id)
	if rev == nonceA || rev == nonceB || rev == baseRev || rev == 0 {
		t.Errorf("revision after resolve = %d, want a fresh value distinct from base(%d)/ours(%d)/theirs(%d)/0",
			rev, baseRev, nonceA, nonceB)
	}
}

// TestTryAutoResolveMergeConflicts_IssuesUpdatedAtWedges verifies the documented
// scope boundary: when the two disjoint edits happen at different times, updated_at
// (ON UPDATE CURRENT_TIMESTAMP) genuinely conflicts on both sides, so the resolver
// declines and leaves the merge for the operator — the nonce does not widen the
// auto-resolvable set beyond what updated_at already wedges.
func TestTryAutoResolveMergeConflicts_IssuesUpdatedAtWedges(t *testing.T) {
	const id = "rev-merge-2"
	store, peerBranch, _ := setupIssuesRevisionMergeConflict(t, id,
		revMergeEdit{sets: []colVal{{"title", "titleA"}}, nonce: 111111111, updatedAt: "2020-01-01 00:00:00"},
		revMergeEdit{sets: []colVal{{"status", "in_progress"}}, nonce: 222222222, updatedAt: "2020-06-06 06:06:06"})

	if mergeAndResolveIssues(t, store, peerBranch) {
		t.Fatal("a both-sides updated_at divergence must NOT be auto-resolved")
	}
}

// TestTryAutoResolveMergeConflicts_IssuesRealContentConflictLeftAlone verifies that
// two clones editing the SAME column to different values — a real edit collision —
// is left for the operator even though revision also conflicts.
func TestTryAutoResolveMergeConflicts_IssuesRealContentConflictLeftAlone(t *testing.T) {
	const id = "rev-merge-3"
	const sharedUpdatedAt = "2020-01-01 00:00:00"
	store, peerBranch, _ := setupIssuesRevisionMergeConflict(t, id,
		revMergeEdit{sets: []colVal{{"title", "titleFromOurs"}}, nonce: 111111111, updatedAt: sharedUpdatedAt},
		revMergeEdit{sets: []colVal{{"title", "titleFromTheirs"}}, nonce: 222222222, updatedAt: sharedUpdatedAt})

	if mergeAndResolveIssues(t, store, peerBranch) {
		t.Fatal("a genuine both-sides title conflict must NOT be auto-resolved")
	}
}

// TestTryAutoResolveMergeConflicts_IssuesMultiColumnPeerEdit exercises the rebuild
// loop when a single row needs MULTIPLE columns copied from theirs: ours edits
// title; theirs edits status AND priority (both disjoint from ours). It proves the
// sequential per-column their_<col> reads stay stable as the working row is mutated
// — every one of ours' and theirs' edits must survive.
func TestTryAutoResolveMergeConflicts_IssuesMultiColumnPeerEdit(t *testing.T) {
	const id = "rev-merge-multi"
	const nonceA, nonceB = int64(111111111), int64(222222222)
	const sharedUpdatedAt = "2020-01-01 00:00:00"

	store, peerBranch, baseRev := setupIssuesRevisionMergeConflict(t, id,
		revMergeEdit{sets: []colVal{{"title", "titleA"}}, nonce: nonceA, updatedAt: sharedUpdatedAt},
		revMergeEdit{sets: []colVal{{"status", "in_progress"}, {"priority", "0"}}, nonce: nonceB, updatedAt: sharedUpdatedAt})

	if !mergeAndResolveIssues(t, store, peerBranch) {
		t.Fatal("expected a revision-only multi-column disjoint conflict to be auto-resolved")
	}

	if got := readIssueString(t, store, id, "title"); got != "titleA" {
		t.Errorf("title after resolve = %q, want ours %q", got, "titleA")
	}
	if got := readIssueString(t, store, id, "status"); got != "in_progress" {
		t.Errorf("status after resolve = %q, want theirs %q", got, "in_progress")
	}
	if got := readIssueString(t, store, id, "priority"); got != "0" {
		t.Errorf("priority after resolve = %q, want theirs %q", got, "0")
	}
	rev := revOf(t, context.Background(), store, id)
	if rev == nonceA || rev == nonceB || rev == baseRev || rev == 0 {
		t.Errorf("revision after resolve = %d, want a fresh distinct value", rev)
	}
}

// TestTryAutoResolveMergeConflicts_IssuesModifyDeleteLeftAlone verifies that a
// modify-on-ours vs delete-on-theirs conflict is never auto-resolved: taking ours
// would silently un-delete the bead the peer removed. The discriminator rejects it
// on the NULL id side.
func TestTryAutoResolveMergeConflicts_IssuesModifyDeleteLeftAlone(t *testing.T) {
	const id = "rev-merge-del"
	store, peerBranch, _ := setupIssuesRevisionMergeConflict(t, id,
		revMergeEdit{sets: []colVal{{"title", "titleA"}}, nonce: 111111111, updatedAt: "2020-01-01 00:00:00"},
		revMergeEdit{deleted: true})

	if mergeAndResolveIssues(t, store, peerBranch) {
		t.Fatal("a modify-vs-delete conflict must NOT be auto-resolved")
	}
}

// TestIssuesConflictTableExposesEveryColumn is the schema-parity guard: it pins the
// Dolt behavior the dynamic resolver relies on — that dolt_conflicts_issues exposes
// a full base_/our_/their_ triple for EVERY physical issues column. If a future
// column (or Dolt version) breaks that, the resolver could silently skip a column
// and drop a real edit; this test fails first.
func TestIssuesConflictTableExposesEveryColumn(t *testing.T) {
	const id = "rev-merge-parity"
	const sharedUpdatedAt = "2020-01-01 00:00:00"
	store, peerBranch, _ := setupIssuesRevisionMergeConflict(t, id,
		revMergeEdit{sets: []colVal{{"title", "titleA"}}, nonce: 111111111, updatedAt: sharedUpdatedAt},
		revMergeEdit{sets: []colVal{{"status", "in_progress"}}, nonce: 222222222, updatedAt: sharedUpdatedAt})

	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	if _, err := db.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		t.Fatalf("allow commit conflicts: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("merge returned: %v", err)
	}

	// Physical issues columns — the exact set the resolver enumerates from.
	physical := physicalIssuesColumns(t, ctx, db)
	if len(physical) == 0 {
		t.Fatal("information_schema returned no issues columns")
	}

	// dolt_conflicts_issues column names.
	have := map[string]bool{}
	rows, err := db.QueryContext(ctx, "SHOW COLUMNS FROM dolt_conflicts_issues")
	if err != nil {
		t.Fatalf("show columns dolt_conflicts_issues: %v", err)
	}
	for rows.Next() {
		var field, typ string
		var null, key, def, extra sql.NullString
		if err := rows.Scan(&field, &typ, &null, &key, &def, &extra); err != nil {
			rows.Close()
			t.Fatalf("scan show columns: %v", err)
		}
		have[field] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate show columns: %v", err)
	}

	var missing []string
	for _, col := range physical {
		for _, side := range []string{"base_", "our_", "their_"} {
			if !have[side+col] {
				missing = append(missing, side+col)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("dolt_conflicts_issues is missing %d expected conflict columns; the resolver would silently skip them:\n%s",
			len(missing), strings.Join(missing, "\n"))
	}
}

func physicalIssuesColumns(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		"SELECT column_name FROM information_schema.columns WHERE table_name = 'issues' AND table_schema = DATABASE() ORDER BY ordinal_position")
	if err != nil {
		t.Fatalf("information_schema issues columns: %v", err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan column_name: %v", err)
		}
		cols = append(cols, c)
	}
	return cols
}
