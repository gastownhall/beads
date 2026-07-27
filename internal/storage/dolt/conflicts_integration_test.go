package dolt

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

// Dolt-backed coverage for the `bd conflicts` surface (wy-36ilm F9). Every
// test the feature shipped with was a pure unit test over synthetic column
// maps — which is exactly why its review BLOCKER (the store decorator never
// satisfied ConflictInspector, so `bd conflicts show` printed "No merge
// conflicts" over a wedged merge) was invisible to the suite. These drive a
// REAL two-branch conflict through GetConflictRows / ResolveConflictRows /
// GetMergeBlockers / CommitMergeResolution, plus the decorator chain the CLI
// actually holds.

// conflictTS is one timestamp for base, ours and theirs, so title is the only
// column that diverges and a field assertion reads unambiguously.
const conflictTS = "2026-07-10 11:00:00"

// wedgeIssueConflict leaves a real, UNRESOLVED issues conflict in the working
// set — the state a halted CLI-level pull hands `bd conflicts`. It merges
// without the auto-resolver on purpose: the operator surface exists for what
// the auto path declined.
func wedgeIssueConflict(t *testing.T, issueID string) (*DoltStore, context.Context) {
	t.Helper()
	store, peerBranch := setupIssueMergeConflict(t, issueID,
		"seed", conflictTS, "ours", conflictTS, "theirs", conflictTS, true)

	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	// A session holding live conflicts cannot autocommit without dolt's
	// conflict-tolerance flags — the same pair ResolveConflictRows sets.
	// MaxOpenConns is 1 for these stores, so the session state persists.
	for _, stmt := range []string{
		"SET @@dolt_allow_commit_conflicts = 1",
		"SET @@dolt_force_transaction_commit = 1",
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		// DOLT_MERGE reports the halt through its error on some versions;
		// the conflict assertion below is the real check.
		t.Logf("DOLT_MERGE returned: %v", err)
	}

	rows, err := store.GetConflictRows(ctx, "issues")
	if err != nil {
		t.Fatalf("GetConflictRows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 live issues conflict after the merge, got %d", len(rows))
	}
	return store, ctx
}

// issueTitle reads the working-set title of an issue.
func issueTitle(t *testing.T, ctx context.Context, store *DoltStore, issueID string) string {
	t.Helper()
	var title string
	if err := store.db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issueID).Scan(&title); err != nil {
		t.Fatalf("read title of %s: %v", issueID, err)
	}
	return title
}

// TestConflictsListAndShowSeeALiveMergeConflict is the `bd conflicts
// list`/`show` path end to end: the tables dolt reports, and the per-field
// base/ours/theirs presentation of the conflicted row.
func TestConflictsListAndShowSeeALiveMergeConflict(t *testing.T) {
	const issueID = "conflict-list-1"
	store, ctx := wedgeIssueConflict(t, issueID)

	// `bd conflicts list` counts through GetConflicts.
	tables, err := store.GetConflicts(ctx)
	if err != nil {
		t.Fatalf("GetConflicts: %v", err)
	}
	found := false
	for _, c := range tables {
		if c.Field == "issues" {
			found = true
			if c.Count < 1 {
				t.Errorf("issues conflict count = %d, want >= 1", c.Count)
			}
		}
	}
	if !found {
		t.Fatalf("GetConflicts did not report the issues table: %+v", tables)
	}

	// `bd conflicts show` reads the rows per field.
	rows, err := store.GetConflictRows(ctx, "issues")
	if err != nil {
		t.Fatalf("GetConflictRows: %v", err)
	}
	row := rows[0]
	if row.Key != issueID {
		t.Errorf("conflict row key = %q, want %q", row.Key, issueID)
	}
	if !row.BaseExists || !row.OurExists || !row.TheirExists {
		t.Errorf("modify/modify conflict should exist on all three sides, got base=%v ours=%v theirs=%v",
			row.BaseExists, row.OurExists, row.TheirExists)
	}
	var title *storage.ConflictFieldValue
	for i := range row.Fields {
		if row.Fields[i].Name == "title" {
			title = &row.Fields[i]
		}
		// The conflict must be presented as fields, never as dolt's raw
		// base_/our_/their_ column soup.
		if strings.HasPrefix(row.Fields[i].Name, "our_") || strings.HasPrefix(row.Fields[i].Name, "their_") {
			t.Errorf("field %q still carries a dolt side prefix", row.Fields[i].Name)
		}
	}
	if title == nil {
		t.Fatalf("no title field in the conflict row: %+v", row.Fields)
	}
	if title.Base == nil || *title.Base != "seed" {
		t.Errorf("title base = %v, want \"seed\"", title.Base)
	}
	if title.Ours == nil || *title.Ours != "ours" {
		t.Errorf("title ours = %v, want \"ours\"", title.Ours)
	}
	if title.Theirs == nil || *title.Theirs != "theirs" {
		t.Errorf("title theirs = %v, want \"theirs\"", title.Theirs)
	}
	if !title.Differs() {
		t.Error("title should be reported as a diverged field")
	}
}

// TestConflictsResolveOursCommitsTheMerge drives `bd conflicts resolve <id>
// --ours` against a real conflict: our value survives, the conflict clears,
// and the merge concludes through CommitMergeResolution.
func TestConflictsResolveOursCommitsTheMerge(t *testing.T) {
	const issueID = "conflict-ours-1"
	store, ctx := wedgeIssueConflict(t, issueID)

	// The merge is open and blocked on the row conflict, not on schema
	// conflicts or constraint violations (wy-36ilm F12).
	blockers, err := store.GetMergeBlockers(ctx)
	if err != nil {
		t.Fatalf("GetMergeBlockers: %v", err)
	}
	if !blockers.Merging {
		t.Error("GetMergeBlockers reported no merge in progress while a merge is wedged")
	}
	if blockers.Blocked() {
		t.Errorf("unexpected non-row merge blockers: %+v", blockers)
	}

	n, err := store.ResolveConflictRows(ctx, "issues", []string{issueID}, "ours")
	if err != nil {
		t.Fatalf("ResolveConflictRows --ours: %v", err)
	}
	if n != 1 {
		t.Fatalf("resolved %d rows, want 1", n)
	}

	rows, err := store.GetConflictRows(ctx, "issues")
	if err != nil {
		t.Fatalf("GetConflictRows after resolve: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no live conflicts after resolve, got %d", len(rows))
	}
	if got := issueTitle(t, ctx, store, issueID); got != "ours" {
		t.Errorf("title after --ours = %q, want %q", got, "ours")
	}

	if err := store.CommitMergeResolution(ctx, "Resolve 1 merge conflict(s) using ours strategy"); err != nil {
		t.Fatalf("CommitMergeResolution: %v", err)
	}
	after, err := store.GetConflicts(ctx)
	if err != nil {
		t.Fatalf("GetConflicts after commit: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("conflicts still reported after the merge commit: %+v", after)
	}
	blockers, err = store.GetMergeBlockers(ctx)
	if err != nil {
		t.Fatalf("GetMergeBlockers after commit: %v", err)
	}
	if blockers.Merging {
		t.Error("merge still reported as in progress after CommitMergeResolution")
	}
	if got := issueTitle(t, ctx, store, issueID); got != "ours" {
		t.Errorf("title after the merge commit = %q, want %q", got, "ours")
	}
}

// TestConflictsResolveTheirsTakesTheirValue is the --theirs half: their values
// are written over ours before the conflict row is cleared.
func TestConflictsResolveTheirsTakesTheirValue(t *testing.T) {
	const issueID = "conflict-theirs-1"
	store, ctx := wedgeIssueConflict(t, issueID)

	n, err := store.ResolveConflictRows(ctx, "issues", []string{issueID}, "theirs")
	if err != nil {
		t.Fatalf("ResolveConflictRows --theirs: %v", err)
	}
	if n != 1 {
		t.Fatalf("resolved %d rows, want 1", n)
	}
	if got := issueTitle(t, ctx, store, issueID); got != "theirs" {
		t.Errorf("title after --theirs = %q, want %q", got, "theirs")
	}
	rows, err := store.GetConflictRows(ctx, "issues")
	if err != nil {
		t.Fatalf("GetConflictRows after resolve: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no live conflicts after resolve, got %d", len(rows))
	}
	if err := store.CommitMergeResolution(ctx, "Resolve 1 merge conflict(s) using theirs strategy"); err != nil {
		t.Fatalf("CommitMergeResolution: %v", err)
	}
	if got := issueTitle(t, ctx, store, issueID); got != "theirs" {
		t.Errorf("title after the merge commit = %q, want %q", got, "theirs")
	}
}

// TestConflictsResolveRefusesAnUnknownIssue proves a miskeyed resolve fails
// loudly instead of silently clearing someone else's conflict.
func TestConflictsResolveRefusesAnUnknownIssue(t *testing.T) {
	const issueID = "conflict-unknown-1"
	store, ctx := wedgeIssueConflict(t, issueID)

	if _, err := store.ResolveConflictRows(ctx, "issues", []string{"no-such-issue"}, "ours"); err == nil {
		t.Fatal("resolving an unconflicted ID should fail")
	} else if !strings.Contains(err.Error(), "no live conflict") {
		t.Errorf("unexpected error for an unconflicted ID: %v", err)
	}
	rows, err := store.GetConflictRows(ctx, "issues")
	if err != nil {
		t.Fatalf("GetConflictRows: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("the real conflict should survive a failed resolve, got %d rows", len(rows))
	}
}

// TestConflictInspectorSurvivesTheStoreDecoratorChain is the BEHAVIORAL half
// of the wy-jpd3.5 review BLOCKER guard: the CLI never holds the concrete
// store, it holds decorators, and the conflict surface reached through
// storage.UnwrapStore must see and resolve a REAL wedged merge — not merely
// type-assert. The type-level half (which chains UnwrapStore peels, and why a
// naked assertion is not enough) is conflicts_unwrap_test.go, which needs no
// dolt and so runs everywhere this test skips (wy-wrq9o F6).
//
// This test deliberately makes no claim about what the decorator does or does
// not satisfy on its own: teaching HookFiringStore to forward the optional
// interfaces would be a strict improvement, and must not turn this red
// (wy-wrq9o F7).
func TestConflictInspectorSurvivesTheStoreDecoratorChain(t *testing.T) {
	const issueID = "conflict-decorator-1"
	store, ctx := wedgeIssueConflict(t, issueID)

	// Two decorators deep, exactly as cmd/bd's chain can be.
	var decorated storage.DoltStorage = storage.NewHookFiringStore(store, nil)
	decorated = storage.NewHookFiringStore(decorated, nil)

	inspector, ok := storage.UnwrapStore(decorated).(storage.ConflictInspector)
	if !ok {
		t.Fatal("UnwrapStore(decorated) does not satisfy ConflictInspector: bd conflicts would report 'No merge conflicts' over a wedged merge (the wy-jpd3.5 review blocker)")
	}
	rows, err := inspector.GetConflictRows(ctx, "issues")
	if err != nil {
		t.Fatalf("GetConflictRows through the decorator chain: %v", err)
	}
	if len(rows) != 1 || rows[0].Key != issueID {
		t.Fatalf("conflict invisible through the decorator chain: %+v", rows)
	}

	blockers, ok := storage.UnwrapStore(decorated).(storage.MergeBlockerInspector)
	if !ok {
		t.Fatal("UnwrapStore(decorated) does not satisfy MergeBlockerInspector: schema conflicts and constraint violations would be invisible to bd conflicts")
	}
	b, err := blockers.GetMergeBlockers(ctx)
	if err != nil {
		t.Fatalf("GetMergeBlockers through the decorator chain: %v", err)
	}
	if !b.Merging {
		t.Error("merge status invisible through the decorator chain")
	}

	// And the resolution path reaches the concrete store too.
	n, err := inspector.ResolveConflictRows(ctx, "issues", []string{issueID}, "ours")
	if err != nil {
		t.Fatalf("ResolveConflictRows through the decorator chain: %v", err)
	}
	if n != 1 {
		t.Errorf("resolved %d rows through the decorator chain, want 1", n)
	}
}

// ── The BLOCKING merge state (wy-36ilm F12), dolt-backed ────────────────
//
// GetMergeBlockers reads two dolt system tables whose column names it cannot
// see at compile time: dolt_schema_conflicts.table_name, and
// dolt_constraint_violations.(`table`, num_violations). wy-wrq9o F5: those
// reads had no test at all — a column rename, a dropped `WHERE num_violations
// > 0`, or a Blocked() inversion would have landed green. These reproduce
// both blocker classes for real and assert the storage half of the gate:
// blockers are visible and Blocked() is true. The command-level commit hold
// and remedies are covered in cmd/bd/conflicts_conclude*_test.go.

// wedgeConstraintViolation leaves a real FK constraint violation in the
// working set: our branch deleted an issue, the peer added a child row
// referencing it, and the merge ran WITHOUT the settle pass that repairs the
// cascade (mergesettle.go) — exactly the state a halted CLI-level pull hands
// `bd conflicts`.
func wedgeConstraintViolation(t *testing.T, tc fkCascadeCase) (*DoltStore, context.Context) {
	t.Helper()
	store, cleanup := setupTestStore(t)
	t.Cleanup(cleanup)

	ctx, cancel := testContext(t)
	t.Cleanup(cancel)

	peerBranch := seedFKCascadeDivergence(ctx, t, store.db, tc)

	// The same conflict-tolerance flags the resolution path sets: without
	// them the violating merge rolls back and leaves nothing to inspect.
	// MaxOpenConns is 1 for these stores, so the session state persists.
	for _, stmt := range []string{
		"SET @@dolt_allow_commit_conflicts = 1",
		"SET @@dolt_force_transaction_commit = 1",
	} {
		if _, err := store.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		// Some dolt versions report the violation through the CALL's error;
		// the assertions below are the real check.
		t.Logf("DOLT_MERGE returned: %v", err)
	}
	return store, ctx
}

// TestGetMergeBlockersSeesAConstraintViolation drives the constraint-violation
// half of GetMergeBlockers against real dolt: the table name and the count
// come out of dolt_constraint_violations' own columns, so this fails if
// either is renamed.
func TestGetMergeBlockersSeesAConstraintViolation(t *testing.T) {
	for _, tc := range fkCascadeCases {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := wedgeConstraintViolation(t, tc)

			// The violation is NOT a row conflict: dolt_conflicts is empty,
			// which is precisely why "0 conflicts remain" used to be a lie.
			rows, err := store.GetConflictRows(ctx, "issues")
			if err != nil {
				t.Fatalf("GetConflictRows: %v", err)
			}
			if len(rows) != 0 {
				t.Errorf("expected no ROW conflicts alongside the violation, got %d", len(rows))
			}

			blockers, err := store.GetMergeBlockers(ctx)
			if err != nil {
				t.Fatalf("GetMergeBlockers: %v", err)
			}
			if !blockers.Blocked() {
				t.Fatalf("a live constraint violation must block the merge commit, got %+v", blockers)
			}
			if len(blockers.SchemaConflictTables) != 0 {
				t.Errorf("no schema conflict was staged, got %v", blockers.SchemaConflictTables)
			}
			found := false
			for _, v := range blockers.ConstraintViolations {
				if v.Table == tc.name {
					found = true
					if v.Count < 1 {
						t.Errorf("violation count for %s = %d, want >= 1 (is `WHERE num_violations > 0` still filtering, and is num_violations still the column?)", v.Table, v.Count)
					}
				}
			}
			if !found {
				t.Errorf("GetMergeBlockers did not report the %s violations: %+v", tc.name, blockers.ConstraintViolations)
			}
		})
	}
}

// TestGetMergeBlockersSeesASchemaConflict is the other class: two branches
// that added the SAME column with different types. Dolt keeps schema
// conflicts out of dolt_conflicts entirely, so this is the state
// `bd conflicts list` used to render as "No merge conflicts."
func TestGetMergeBlockersSeesASchemaConflict(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	const probeTable = "schema_conflict_probe"

	var currentBranch string
	if err := db.QueryRowContext(ctx, "SELECT active_branch()").Scan(&currentBranch); err != nil {
		t.Fatalf("get current branch: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CREATE TABLE "+probeTable+" (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed probe table')"); err != nil {
		t.Fatalf("commit probe table: %v", err)
	}

	peerBranch := currentBranch + "_schema_peer"
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH(?, 'HEAD')", peerBranch); err != nil {
		t.Fatalf("create peer branch: %v", err)
	}
	defer func() {
		_, _ = db.ExecContext(context.Background(), "CALL DOLT_MERGE('--abort')")
		_, _ = db.ExecContext(context.Background(), "CALL DOLT_CHECKOUT(?)", currentBranch)
		_, _ = db.ExecContext(context.Background(), "CALL DOLT_BRANCH('-Df', ?)", peerBranch)
	}()

	// Divergent ALTER TABLE: same column name, incompatible types.
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+probeTable+" ADD COLUMN note VARCHAR(32)"); err != nil {
		t.Fatalf("alter on current: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'our column')"); err != nil {
		t.Fatalf("commit our column: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", peerBranch); err != nil {
		t.Fatalf("checkout peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+probeTable+" ADD COLUMN note BIGINT"); err != nil {
		t.Fatalf("alter on peer: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'their column')"); err != nil {
		t.Fatalf("commit their column: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT(?)", currentBranch); err != nil {
		t.Fatalf("checkout current: %v", err)
	}

	for _, stmt := range []string{
		"SET @@dolt_allow_commit_conflicts = 1",
		"SET @@dolt_force_transaction_commit = 1",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_MERGE(?)", peerBranch); err != nil {
		t.Logf("DOLT_MERGE returned: %v", err)
	}

	blockers, err := store.GetMergeBlockers(ctx)
	if err != nil {
		t.Fatalf("GetMergeBlockers: %v", err)
	}
	if !blockers.Blocked() {
		t.Fatalf("a live schema conflict must block the merge commit, got %+v", blockers)
	}
	found := false
	for _, tbl := range blockers.SchemaConflictTables {
		if tbl == probeTable {
			found = true
		}
	}
	if !found {
		t.Errorf("GetMergeBlockers did not report the %s schema conflict (is dolt_schema_conflicts.table_name still the column?): %+v",
			probeTable, blockers)
	}
}
