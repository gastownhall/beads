//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// seedCounterDivergence seeds parent issues with child_counters rows at 70,
// commits that as the merge base, then diverges: main runs ours and the
// returned peer branch runs theirs. Leaves main checked out on conn.
func seedCounterDivergence(t *testing.T, ctx context.Context, conn *sql.Conn, parents []string, ours, theirs func(t *testing.T)) (peerBranch string) {
	t.Helper()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, p := range parents {
		exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'epic')", p, p)
		exec("INSERT INTO child_counters (parent_id, last_child) VALUES (?, 70)", p)
	}
	exec("CALL DOLT_COMMIT('-Am', 'seed counters')")
	peerBranch = "ccpeer"
	exec("CALL DOLT_BRANCH(?, 'HEAD')", peerBranch)
	ours(t)
	exec("CALL DOLT_COMMIT('-Am', 'ours')")
	exec("CALL DOLT_CHECKOUT(?)", peerBranch)
	theirs(t)
	exec("CALL DOLT_COMMIT('-Am', 'theirs')")
	exec("CALL DOLT_CHECKOUT('main')")
	return peerBranch
}

func counterValue(t *testing.T, ctx context.Context, conn *sql.Conn, parent string) (int, bool) {
	t.Helper()
	var n int
	err := conn.QueryRowContext(ctx, "SELECT last_child FROM child_counters WHERE parent_id = ?", parent).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		t.Fatalf("read counter %s: %v", parent, err)
	}
	return n, true
}

// TestEmbeddedMergeAndSettleChildCountersMax is the embedded-engine half of
// bd-28sb25 AC-1: two clones minting children under the same parents leave
// both-changed child_counters rows; settlement takes each row's maximum
// (whichever side holds it) and concludes the merge.
func TestEmbeddedMergeAndSettleChildCountersMax(t *testing.T) {
	te := newTestEnv(t, "ccmax")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	set := func(p string, n int) func(*testing.T) {
		return func(t *testing.T) {
			if _, err := conn.ExecContext(ctx, "UPDATE child_counters SET last_child = ? WHERE parent_id = ?", n, p); err != nil {
				t.Fatalf("set counter: %v", err)
			}
		}
	}
	peer := seedCounterDivergence(t, ctx, conn, []string{"cc-a", "cc-b"},
		func(t *testing.T) { set("cc-a", 73)(t); set("cc-b", 71)(t) },
		func(t *testing.T) { set("cc-a", 72)(t); set("cc-b", 75)(t) })

	if err := versioncontrolops.MergeAndSettle(ctx, conn, peer); err != nil {
		t.Fatalf("MergeAndSettle: %v", err)
	}
	for p, want := range map[string]int{"cc-a": 73, "cc-b": 75} {
		if got, ok := counterValue(t, ctx, conn, p); !ok || got != want {
			t.Errorf("counter %s = %d (present %v), want %d", p, got, ok, want)
		}
	}
	var merging bool
	if err := conn.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read merge status: %v", err)
	}
	if merging {
		t.Error("merge left open after counter settlement")
	}
	var dirty int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if dirty != 0 {
		t.Errorf("working set has %d dirty tables after settlement, want a committed merge", dirty)
	}
}

// TestEmbeddedMergeAndSettleChildCounterDeletionDeclines is bd-28sb25 AC-9
// on the embedded engine: a counter row one side deleted while the other
// advanced it is not a high-water-mark conflict (deletion is not zero), so
// settlement declines with a MergeConflictsError naming child_counters and
// MergeAndSettle restores the pre-merge working set: no counter resolution
// lands, and the other parent's valid conflict is not resolved either.
func TestEmbeddedMergeAndSettleChildCounterDeletionDeclines(t *testing.T) {
	te := newTestEnv(t, "ccdecl")
	ctx := t.Context()
	conn := openSettleConn(t, ctx, te)
	exec := func(t *testing.T, q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	peer := seedCounterDivergence(t, ctx, conn, []string{"cd-a", "cd-b"},
		func(t *testing.T) {
			exec(t, "UPDATE child_counters SET last_child = 73 WHERE parent_id = 'cd-a'")
			exec(t, "UPDATE child_counters SET last_child = 71 WHERE parent_id = 'cd-b'")
		},
		func(t *testing.T) {
			exec(t, "DELETE FROM child_counters WHERE parent_id = 'cd-a'")
			exec(t, "UPDATE child_counters SET last_child = 75 WHERE parent_id = 'cd-b'")
		})
	var headBefore string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&headBefore); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}

	err := versioncontrolops.MergeAndSettle(ctx, conn, peer)
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(err, &mce) {
		t.Fatalf("MergeAndSettle = %v, want a MergeConflictsError", err)
	}
	named := false
	for _, c := range mce.Conflicts {
		named = named || c.Field == "child_counters"
	}
	if !named {
		t.Errorf("conflicts %+v do not name child_counters", mce.Conflicts)
	}
	for p, want := range map[string]int{"cd-a": 73, "cd-b": 71} {
		if got, ok := counterValue(t, ctx, conn, p); !ok || got != want {
			t.Errorf("counter %s = %d (present %v), want the pre-merge %d", p, got, ok, want)
		}
	}
	var headAfter string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&headAfter); err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if headAfter != headBefore {
		t.Errorf("HEAD moved from %s to %s on a declined merge", headBefore, headAfter)
	}
	var merging bool
	if err := conn.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read merge status: %v", err)
	}
	var dirty int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status").Scan(&dirty); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if merging || dirty != 0 {
		t.Errorf("after decline: merging=%v dirty tables=%d, want the pre-merge working set restored", merging, dirty)
	}
}
