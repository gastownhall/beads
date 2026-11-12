package dolt

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// persistConflictedMerge leaves a conflicted merge open on the store's working
// set, as a CLI `dolt pull` did before GH#7839: issue ow-1's title is edited
// on a side branch and on the store's branch, and the side branch is merged in
// a transaction that is allowed to commit conflicts. It returns the store's
// active branch (setupTestStore's per-test branch) and the merged branch.
func persistConflictedMerge(t *testing.T, ctx context.Context, store *DoltStore) (string, string) {
	t.Helper()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var branch string
	if err := conn.QueryRowContext(ctx, "SELECT active_branch()").Scan(&branch); err != nil {
		t.Fatalf("active_branch: %v", err)
	}
	side := branch + "-peer"

	exec("INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) VALUES ('ow-1', 'base', '', '', '', '', 'open', 2, 'task')")
	exec("CALL DOLT_COMMIT('-Am', 'add ow-1')")
	exec("CALL DOLT_BRANCH(?)", side)
	exec("CALL DOLT_CHECKOUT(?)", side)
	exec("UPDATE issues SET title = 'peer title' WHERE id = 'ow-1'")
	exec("CALL DOLT_COMMIT('-Am', 'peer title')")
	exec("CALL DOLT_CHECKOUT(?)", branch)
	exec("UPDATE issues SET title = 'local title' WHERE id = 'ow-1'")
	exec("CALL DOLT_COMMIT('-Am', 'local title')")

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin merge tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	_, _ = tx.ExecContext(ctx, "CALL DOLT_MERGE(?)", side)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit conflicted merge: %v", err)
	}
	// The test pool may hold a single connection: release the pin first.
	_ = conn.Close()
	st := freshMergeState(t, ctx, store)
	if !st.merging || len(st.conflicts) == 0 {
		t.Fatalf("fixture left no conflicted merge: is_merging=%v conflicts=%+v", st.merging, st.conflicts)
	}
	return branch, side
}

// freshMergeState reads the merge state on a new pooled session.
func freshMergeState(t *testing.T, ctx context.Context, store *DoltStore) cliMergeState {
	t.Helper()
	st, err := store.readStoreMergeState(ctx)
	if err != nil {
		t.Fatalf("read merge state: %v", err)
	}
	return st
}

// assertOpenMergeRefusal checks a pull's refusal names the open merge and both
// the inspect and the abort remedy.
func assertOpenMergeRefusal(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s over an open conflicted merge returned nil; want a refusal", what)
	}
	for _, want := range []string{"already open", "issues", "bd conflicts list", `bd sql "CALL DOLT_MERGE('--abort')"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s refusal %q does not mention %q", what, err.Error(), want)
		}
	}
}

// TestPullRefusesOverConflictedWorkingSet pins AC-4 of GH#7839: a pull that
// finds a conflicted merge already open (left by an older bd, or by an
// operator) reports it with the inspect and abort remedy before it stages
// anything or starts a transport, and leaves that merge and its conflicts
// exactly as they were: it is not the pull's merge to abort.
func TestPullRefusesOverConflictedWorkingSet(t *testing.T) {
	t.Run("normal and peer pulls refuse before staging", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		persistConflictedMerge(t, ctx, store)

		assertOpenMergeRefusal(t, "Pull", store.Pull(ctx))
		assertOpenMergeRefusal(t, "PullRemote", store.PullRemote(ctx, "origin"))
		_, peerErr := store.PullFrom(ctx, "nonexistent")
		assertOpenMergeRefusal(t, "PullFrom", peerErr)

		st := freshMergeState(t, ctx, store)
		if !st.merging || len(st.conflicts) != 1 || st.conflicts[0].Field != "issues" {
			t.Errorf("after the refused pulls is_merging=%v conflicts=%+v; want the open issues conflict preserved", st.merging, st.conflicts)
		}
	})

	t.Run("CLI route refuses before transport", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		persistConflictedMerge(t, ctx, store)

		called := false
		err := store.runCLIPull(ctx, "origin", func() error { called = true; return nil })
		if called {
			t.Error("runCLIPull ran the dolt pull transport over an already-open merge")
		}
		assertOpenMergeRefusal(t, "runCLIPull", err)
		if st := freshMergeState(t, ctx, store); !st.merging || len(st.conflicts) == 0 {
			t.Errorf("after the refused CLI pull is_merging=%v conflicts=%+v; want the merge preserved", st.merging, st.conflicts)
		}
	})

	t.Run("a resolved but uncommitted merge is not refused", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		persistConflictedMerge(t, ctx, store)
		resolveOpenMergeOurs(t, ctx, store)

		// Only conflicts and blockers are refused: a merge the operator has
		// resolved is theirs to commit, and the pre-pull commit is unchanged.
		if err := store.commitBeforePull(ctx, "auto-commit before pull"); err != nil && !isDoltNothingToCommit(err) {
			t.Fatalf("commitBeforePull over a resolved merge: %v", err)
		}
	})
}

// resolveOpenMergeOurs resolves the open merge's issues conflicts with ours and
// persists the resolved, still-unconcluded merge.
func resolveOpenMergeOurs(t *testing.T, ctx context.Context, store *DoltStore) {
	t.Helper()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatalf("begin resolve tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "SET @@dolt_allow_commit_conflicts = 1"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("allow commit conflicts: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "CALL DOLT_CONFLICTS_RESOLVE('--ours', 'issues')"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("resolve ours: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit resolution: %v", err)
	}
	_ = conn.Close()
	st := freshMergeState(t, ctx, store)
	if !st.merging || len(st.conflicts) != 0 {
		t.Fatalf("resolution fixture: is_merging=%v conflicts=%+v; want a merge with no conflicts", st.merging, st.conflicts)
	}
}
