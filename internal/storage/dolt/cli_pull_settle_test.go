package dolt

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
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

// mockMergeState is one readCLIMergeState read as sqlmock serves it.
type mockMergeState struct {
	failRead  bool // the first query fails: the read errors out
	merging   bool
	source    string
	target    string
	conflicts []string
	schema    []string
}

const ownedSource, ownedTarget = "refs/remotes/origin/main", "refs/heads/main"

var (
	clean         = mockMergeState{}
	ownedConflict = mockMergeState{merging: true, source: ownedSource, target: ownedTarget, conflicts: []string{"issues"}}
	readFails     = mockMergeState{failRead: true}
)

func expectMergeState(mock sqlmock.Sqlmock, st mockMergeState) {
	q := regexp.QuoteMeta
	if st.failRead {
		mock.ExpectQuery(q("SELECT is_merging, source, target FROM dolt_merge_status")).
			WillReturnError(errors.New("merge status read failed"))
		return
	}
	status := sqlmock.NewRows([]string{"is_merging", "source", "target"})
	if st.merging {
		status.AddRow(true, st.source, st.target)
	} else {
		status.AddRow(false, nil, nil)
	}
	mock.ExpectQuery(q("SELECT is_merging, source, target FROM dolt_merge_status")).WillReturnRows(status)
	conflicts := sqlmock.NewRows([]string{"table", "num_conflicts"})
	for _, tbl := range st.conflicts {
		conflicts.AddRow(tbl, 1)
	}
	mock.ExpectQuery(q("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(conflicts)
	mock.ExpectQuery(q("SELECT is_merging FROM dolt_merge_status")).
		WillReturnRows(sqlmock.NewRows([]string{"is_merging"}).AddRow(st.merging))
	schemaRows := sqlmock.NewRows([]string{"table_name"})
	for _, tbl := range st.schema {
		schemaRows.AddRow(tbl)
	}
	mock.ExpectQuery(q("SELECT table_name FROM dolt_schema_conflicts")).WillReturnRows(schemaRows)
	mock.ExpectQuery(q("FROM dolt_constraint_violations")).
		WillReturnRows(sqlmock.NewRows([]string{"table", "num_violations"}))
}

// expectResolverFails scripts autoResolveConflictsAfterCLIPull failing on its
// first conflicts read, an ordinary (not indeterminate) resolver error.
func expectResolverFails(mock sqlmock.Sqlmock) {
	q := regexp.QuoteMeta
	mock.ExpectExec(q("SET @@dolt_allow_commit_conflicts = 1")).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectQuery(q("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnError(errors.New("resolver read failed"))
	mock.ExpectRollback()
	mock.ExpectExec(q("SET @@dolt_allow_commit_conflicts = 0")).WillReturnResult(sqlmock.NewResult(0, 0))
}

func expectAbort(mock sqlmock.Sqlmock, err error) {
	e := mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')"))
	if err != nil {
		e.WillReturnError(err)
		return
	}
	e.WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(0))
}

// TestFinishCLIPullSettlement pins AC-5 of GH#7839: settlement aborts only a
// merge it can attribute to the pull it just ran (source the fetched
// refs/remotes/<remote>/<branch>, target the store's branch, nothing open
// before the transport) and claims restoration only after re-reading the
// branch. Every other outcome is an actionable *pullMergeStateError that
// never says the working set was restored, issues no reset of any kind, and
// stays an error through federation's PullFrom.
func TestFinishCLIPullSettlement(t *testing.T) {
	pullErr := errors.New("dolt pull failed: CONFLICT (content): Merge conflict in issues")
	cases := []struct {
		name   string
		script func(sqlmock.Sqlmock)
		want   string
	}{
		{"a merge of another source is not the pull's", func(m sqlmock.Sqlmock) {
			expectMergeState(m, clean)
			expectMergeState(m, mockMergeState{merging: true, source: "refs/remotes/other/main", target: ownedTarget, conflicts: []string{"issues"}})
		}, "cannot attribute"},
		{"a merge into another branch is not the pull's", func(m sqlmock.Sqlmock) {
			expectMergeState(m, clean)
			expectMergeState(m, mockMergeState{merging: true, source: ownedSource, target: "refs/heads/feature", conflicts: []string{"issues"}})
		}, "cannot attribute"},
		{"an unreadable pre-pull state leaves the merge unattributed", func(m sqlmock.Sqlmock) {
			expectMergeState(m, readFails)
			expectMergeState(m, ownedConflict)
		}, "cannot attribute"},
		{"an unreadable post-pull state is reported as unknown", func(m sqlmock.Sqlmock) {
			expectMergeState(m, clean)
			expectMergeState(m, readFails)
		}, "could not read"},
		{"a failed abort is not a restoration", func(m sqlmock.Sqlmock) {
			expectMergeState(m, clean)
			expectMergeState(m, ownedConflict)
			expectResolverFails(m)
			expectMergeState(m, ownedConflict)
			expectMergeState(m, ownedConflict)
			expectAbort(m, errors.New("abort refused"))
		}, "could not confirm"},
		{"an abort that leaves the merge open is not a restoration", func(m sqlmock.Sqlmock) {
			expectMergeState(m, clean)
			expectMergeState(m, ownedConflict)
			expectResolverFails(m)
			expectMergeState(m, ownedConflict)
			expectMergeState(m, ownedConflict)
			expectAbort(m, nil)
			expectMergeState(m, ownedConflict)
		}, "could not confirm"},
		{"an unverifiable abort is not a restoration", func(m sqlmock.Sqlmock) {
			expectMergeState(m, clean)
			expectMergeState(m, ownedConflict)
			expectResolverFails(m)
			expectMergeState(m, ownedConflict)
			expectMergeState(m, ownedConflict)
			expectAbort(m, nil)
			expectMergeState(m, readFails)
		}, "could not confirm"},
		{"blockers without a merge are diagnosed, not resolved", func(m sqlmock.Sqlmock) {
			expectMergeState(m, clean)
			expectMergeState(m, mockMergeState{schema: []string{"issues"}})
		}, "no merge in progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()
			tc.script(mock)
			store := &DoltStore{db: db, branch: "main"}

			got := store.runCLIPull(context.Background(), "origin", func() error { return pullErr })
			var pmse *pullMergeStateError
			if !errors.As(got, &pmse) {
				t.Fatalf("runCLIPull = %v (%T); want a *pullMergeStateError", got, got)
			}
			msg := got.Error()
			for _, want := range []string{tc.want, "bd conflicts list", `bd sql "CALL DOLT_MERGE('--abort')"`} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not mention %q", msg, want)
				}
			}
			for _, claim := range []string{"working set restored", "merge aborted"} {
				if strings.Contains(msg, claim) {
					t.Errorf("error %q claims %q", msg, claim)
				}
			}
			if !errors.Is(got, pullErr) {
				t.Errorf("error %q does not wrap the dolt pull error", msg)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("settlement did not run exactly the scripted reads (no resolve, abort or reset beyond them): %v", err)
			}
		})
	}

	t.Run("a read-only store surfaces the transport result untouched", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		store := &DoltStore{db: db, branch: "main", readOnly: true}
		if got := store.runCLIPull(context.Background(), "origin", func() error { return pullErr }); got != pullErr {
			t.Errorf("read-only runCLIPull = %v; want the transport error as-is", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("read-only settlement touched the database: %v", err)
		}
	})

	t.Run("federation keeps a settlement failure an error", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		// The live merge is still there, which is exactly why it must not be
		// read back as conflicts-as-data.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).
			WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}).AddRow("issues", 1))
		store := &DoltStore{db: db, branch: "main"}
		settleErr := &pullMergeStateError{remote: "origin", state: cliMergeState{merging: true, source: "refs/remotes/other/main", conflicts: []storage.Conflict{{Field: "issues", Count: 1}}}}
		var conflicts []storage.Conflict
		got := store.peerPullOutcome(context.Background(), "origin", settleErr, &conflicts)
		var pmse *pullMergeStateError
		if !errors.As(got, &pmse) {
			t.Errorf("peerPullOutcome = %v; want the settlement failure kept as an error", got)
		}
		if len(conflicts) != 0 {
			t.Errorf("peerPullOutcome returned conflicts %+v as data for a merge settlement left open", conflicts)
		}
	})

	t.Run("a real merge from another source is preserved", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		got := store.runCLIPull(ctx, "origin", func() error {
			persistConflictedMerge(t, ctx, store)
			return pullErr
		})
		var pmse *pullMergeStateError
		if !errors.As(got, &pmse) || !strings.Contains(got.Error(), "cannot attribute") {
			t.Errorf("runCLIPull over a merge of a local branch = %v; want it reported as unattributed", got)
		}
		st := freshMergeState(t, ctx, store)
		if !st.merging || len(st.conflicts) != 1 || st.conflicts[0].Field != "issues" {
			t.Errorf("after settlement is_merging=%v conflicts=%+v; want the unattributed merge left exactly as it was", st.merging, st.conflicts)
		}
	})
}
