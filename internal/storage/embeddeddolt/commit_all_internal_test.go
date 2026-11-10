//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

func TestDoltCommitResponseLossIsIndeterminate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}

	responseLoss := errors.New("connection lost before DOLT_COMMIT response")
	mock.ExpectExec("CALL DOLT_COMMIT('-Am', ?)").
		WithArgs("test: publish working set").
		WillReturnError(responseLoss)

	committed, err := commitAllInTx(t.Context(), tx, "test: publish working set", true)
	if committed {
		t.Fatal("commitAllInTx() committed = true after response loss, want false")
	}
	if !errors.Is(err, responseLoss) {
		t.Fatalf("commitAllInTx() error = %v, want cause %v", err, responseLoss)
	}
	if !errors.Is(err, storage.ErrCommitIndeterminate) {
		t.Fatalf("commitAllInTx() error = %v, want ErrCommitIndeterminate", err)
	}

	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// mockVerificationTx returns a sqlmock handle plus a live *sql.Tx, for
// driving the commit-verification seam in isolation.
func mockVerificationTx(t *testing.T) (sqlmock.Sqlmock, *sql.Tx) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	mock.ExpectBegin()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	// The transaction is never committed; sqlmock needs no rollback
	// expectation and database/sql tolerates Close with the tx open.
	t.Cleanup(func() { _ = db.Close() })
	return mock, tx
}

// swapCommitAllPolicyLoader replaces the verification's policy seam for the
// test and restores it on cleanup.
func swapCommitAllPolicyLoader(t *testing.T, loader LoadOwnedIgnorePolicyFunc) {
	t.Helper()
	prev := commitAllPolicyLoader
	commitAllPolicyLoader = loader
	t.Cleanup(func() { commitAllPolicyLoader = prev })
}

// TestCommitVerificationPreservesTransactionOutcome pins the verification
// seam (GH#5433 AC-5): the required reads are the dolt_status read and the
// owned-policy load; a failure of either is collected — never returned from
// the withConn callback, where it would roll back a published commit — with
// the operation that failed and DOLT_COMMIT's own answer, and the optional
// pattern diagnostic degrades alone: a residual finding survives its own
// failed diagnostic read.
func TestCommitVerificationPreservesTransactionOutcome(t *testing.T) {
	ctx := t.Context()
	statusQuery := "SELECT table_name, status FROM dolt_status"

	queryErr := errors.New("dolt_status query failed")
	iterErr := errors.New("iteration failed partway")
	policyErr := errors.New("policy read failed")

	t.Run("required status read failures are collected with the operation", func(t *testing.T) {
		for _, committed := range []bool{true, false} {
			for name, expect := range map[string]func(sqlmock.Sqlmock){
				"query": func(mock sqlmock.Sqlmock) {
					mock.ExpectQuery(statusQuery).WillReturnError(queryErr)
				},
				// No dedicated scan-failure fixture: the two destinations are
				// *string, and database/sql converts every legal driver.Value
				// (int64, float64, bool, []byte, time.Time, nil) into a
				// string on Scan, while sqlmock refuses AddRow of anything
				// else. A mid-row failure is therefore indistinguishable
				// from the iteration error covered below; both surface as
				// rows.Err()/Scan errors the check already collects.
				"iteration": func(mock sqlmock.Sqlmock) {
					rows := sqlmock.NewRows([]string{"table_name", "status"}).AddRow("issues", "modified")
					rows.RowError(0, iterErr)
					mock.ExpectQuery(statusQuery).WillReturnRows(rows)
				},
			} {
				t.Run(fmt.Sprintf("%s/committed=%v", name, committed), func(t *testing.T) {
					mock, tx := mockVerificationTx(t)
					expect(mock)
					v := verifyCommitAllInTx(ctx, tx, committed)
					if v.readFailed == nil {
						t.Fatal("verifyCommitAllInTx collected no read failure")
					}
					if v.readFailedOperation != "read dolt_status after the commit" {
						t.Errorf("readFailedOperation = %q, want the status read", v.readFailedOperation)
					}
					if v.committed != committed {
						t.Errorf("committed = %v, want DOLT_COMMIT's own answer %v preserved", v.committed, committed)
					}
					if len(v.residual) != 0 {
						t.Errorf("residual = %v, want none: the read failed before any finding", v.residual)
					}
					switch name {
					case "query":
						if !errors.Is(v.readFailed, queryErr) {
							t.Errorf("readFailed = %v, want the query cause", v.readFailed)
						}
					case "iteration":
						if !errors.Is(v.readFailed, iterErr) {
							t.Errorf("readFailed = %v, want the iteration cause", v.readFailed)
						}
					}
					if err := mock.ExpectationsWereMet(); err != nil {
						t.Fatalf("unmet sqlmock expectations: %v", err)
					}
				})
			}
		}
	})

	t.Run("required policy load failure is collected with the operation", func(t *testing.T) {
		mock, tx := mockVerificationTx(t)
		mock.ExpectQuery(statusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "status"}))
		swapCommitAllPolicyLoader(t, func(context.Context, schema.DBConn) (schema.OwnedIgnorePolicy, error) {
			return schema.OwnedIgnorePolicy{}, policyErr
		})

		v := verifyCommitAllInTx(ctx, tx, false)
		if !errors.Is(v.readFailed, policyErr) {
			t.Fatalf("readFailed = %v, want the policy-load cause", v.readFailed)
		}
		if v.readFailedOperation != "load the Beads-owned dolt_ignore policy" {
			t.Errorf("readFailedOperation = %q, want the policy load", v.readFailedOperation)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("clean status collects nothing", func(t *testing.T) {
		mock, tx := mockVerificationTx(t)
		mock.ExpectQuery(statusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "status"}))
		swapCommitAllPolicyLoader(t, func(context.Context, schema.DBConn) (schema.OwnedIgnorePolicy, error) {
			return schema.OwnedIgnorePolicy{}, nil
		})

		v := verifyCommitAllInTx(ctx, tx, true)
		if v.readFailed != nil {
			t.Fatalf("readFailed = %v, want nil on a clean store", v.readFailed)
		}
		if len(v.residual) != 0 {
			t.Fatalf("residual = %v, want none on a clean store", v.residual)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("dirty tables collect as residual, committed answer preserved", func(t *testing.T) {
		mock, tx := mockVerificationTx(t)
		mock.ExpectQuery(statusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "status"}).AddRow("issues", "modified"))
		swapCommitAllPolicyLoader(t, func(context.Context, schema.DBConn) (schema.OwnedIgnorePolicy, error) {
			return schema.OwnedIgnorePolicy{}, nil
		})

		v := verifyCommitAllInTx(ctx, tx, true)
		if v.readFailed != nil {
			t.Fatalf("readFailed = %v, want nil", v.readFailed)
		}
		if len(v.residual) != 1 || v.residual[0] != "issues" {
			t.Fatalf("residual = %v, want [issues]", v.residual)
		}
		if !v.committed {
			t.Error("committed = false, want DOLT_COMMIT's own true answer preserved")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})

	t.Run("optional pattern read failure does not weaken the finding", func(t *testing.T) {
		mock, tx := mockVerificationTx(t)
		mock.ExpectQuery(statusQuery).
			WillReturnRows(sqlmock.NewRows([]string{"table_name", "status"}).AddRow("issues", "modified"))
		swapCommitAllPolicyLoader(t, func(context.Context, schema.DBConn) (schema.OwnedIgnorePolicy, error) {
			return schema.OwnedIgnorePolicy{}, nil
		})
		mock.ExpectQuery("SELECT pattern FROM dolt_ignore WHERE ignored = 1").
			WillReturnError(errors.New("dolt_ignore read failed"))

		v := verifyCommitAllInTx(ctx, tx, false)
		if v.readFailed != nil {
			t.Fatalf("readFailed = %v, want nil: the failed read is the optional diagnostic, not a required one", v.readFailed)
		}
		if len(v.residual) != 1 || v.residual[0] != "issues" {
			t.Fatalf("residual = %v, want [issues] to survive its own failed diagnostic", v.residual)
		}
		if len(v.patterns) != 0 {
			t.Errorf("patterns = %v, want none after the diagnostic read failed", v.patterns)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet sqlmock expectations: %v", err)
		}
	})
}

// TestCommitAllReportsVerificationFailureWhenRequiredReadFails is the
// GH#5433 AC-5 wiring test on a real engine: when the required owned-policy
// read fails after the DOLT_COMMIT, commitAll surfaces the typed
// CommitVerificationError instead of a clean/success claim —
// outcome-sensitively, with DOLT_COMMIT's own answer preserved in the
// committed bool and never claiming the mutation failed or prescribing
// replay. The dirty case also pins publication precedence: the commit the
// diagnostic followed has already landed, and the finding must not roll it
// back or misreport it.
func TestCommitAllReportsVerificationFailureWhenRequiredReadFails(t *testing.T) {
	ctx := t.Context()

	for _, tc := range []struct {
		name       string
		dirty      bool
		wantCommit bool
		outcome    storage.CommitVerificationOutcome
		wantLine   string
	}{
		{
			name:       "commit_succeeded",
			dirty:      true,
			wantCommit: true,
			outcome:    storage.CommitSucceeded,
			wantLine:   "DOLT_COMMIT reported success; the commit may have landed, but completeness could not be verified",
		},
		{
			name:       "nothing_to_commit",
			dirty:      false,
			wantCommit: false,
			outcome:    storage.CommitNothingToCommit,
			wantLine:   "DOLT_COMMIT reported nothing to commit; working-set cleanliness could not be verified, so the completeness outcome is unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := filepath.Join(t.TempDir(), ".beads")
			store, err := Open(ctx, beadsDir, "verfail", "main")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.SetConfig(ctx, "issue_prefix", "vf"); err != nil {
				t.Fatalf("SetConfig(issue_prefix): %v", err)
			}
			if err := store.Commit(ctx, "bd init"); err != nil {
				t.Fatalf("Commit(bd init): %v", err)
			}

			headBefore, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit: %v", err)
			}

			if tc.dirty {
				issue := &types.Issue{
					ID:        "vf-" + tc.name,
					Title:     "pending change",
					Status:    types.StatusOpen,
					Priority:  2,
					IssueType: types.TypeTask,
				}
				if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
					t.Fatalf("CreateIssue: %v", err)
				}
			}

			policyReadErr := errors.New("owned policy cursor unreadable")
			swapCommitAllPolicyLoader(t, func(context.Context, schema.DBConn) (schema.OwnedIgnorePolicy, error) {
				return schema.OwnedIgnorePolicy{}, policyReadErr
			})

			committed, commitErr := store.CommitAll(ctx, "test: verification failure")
			var verification *storage.CommitVerificationError
			if !errors.As(commitErr, &verification) {
				t.Fatalf("CommitAll error = %v, want *storage.CommitVerificationError", commitErr)
			}
			if committed != tc.wantCommit {
				t.Errorf("committed = %v, want DOLT_COMMIT's own answer %v preserved through the failed read", committed, tc.wantCommit)
			}
			if verification.Outcome != tc.outcome {
				t.Errorf("Outcome = %q, want %q", verification.Outcome, tc.outcome)
			}
			if verification.Operation != "load the Beads-owned dolt_ignore policy" {
				t.Errorf("Operation = %q, want the failed policy load", verification.Operation)
			}
			if !errors.Is(commitErr, policyReadErr) {
				t.Errorf("errors.Is(commitErr, cause) = false, want the underlying read error through Unwrap: %v", commitErr)
			}
			if !errors.Is(commitErr, storage.ErrCommitVerificationFailed) {
				t.Errorf("errors.Is(commitErr, ErrCommitVerificationFailed) = false, want the sentinel through any wrapper: %v", commitErr)
			}
			if msg := verification.Error(); !strings.Contains(msg, tc.wantLine) {
				t.Errorf("message lacks the outcome-sensitive line %q:\n%s", tc.wantLine, msg)
			}
			if msg := verification.Error(); !strings.Contains(msg, "do not replay the writes") {
				t.Errorf("message lacks the no-replay advice:\n%s", msg)
			}
			if issueops.IsNothingToCommitError(commitErr) {
				t.Error("IsNothingToCommitError classified the verification failure as a healthy no-op")
			}

			headAfter, err := store.GetCurrentCommit(ctx)
			if err != nil {
				t.Fatalf("GetCurrentCommit after: %v", err)
			}
			if tc.dirty {
				// Publication precedence: the commit DOLT_COMMIT made before
				// the failed read landed and stays landed.
				if headAfter == headBefore {
					t.Error("HEAD did not advance: the published commit was lost to the failed diagnostic")
				}
			} else if headAfter != headBefore {
				t.Errorf("HEAD moved across an unverified nothing-to-commit: %s -> %s", headBefore, headAfter)
			}
		})
	}
}

// stallFixtureStore returns a real store in the GH#5433 stall: an enabled
// operator pattern over the tracked issues table, its latest write stranded
// in the working set — so a tolerant full commit would answer the typed
// residual finding.
func stallFixtureStore(t *testing.T, database string) *EmbeddedDoltStore {
	t.Helper()
	ctx := t.Context()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	store, err := Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "fpp"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit(bd init): %v", err)
	}
	issue := &types.Issue{
		ID:        "fpp-1",
		Title:     "tracked at head",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
	if err := store.CreateIssue(ctx, issue, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := store.Commit(ctx, "test: track the issue"); err != nil {
		t.Fatalf("Commit(track the issue): %v", err)
	}
	// Raw-SQL writes go through short-lived handles that close before the
	// next store operation: a handle kept open across store.Commit holds
	// the engine's database lock and wedges the commit path's own open in
	// its lock-retry loop (the pattern every te.exec fixture follows).
	execStallSQL := func(query string) {
		t.Helper()
		db, cleanup, err := OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), database, "main")
		if err != nil {
			t.Fatalf("OpenSQL: %v", err)
		}
		defer func() { _ = cleanup() }()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatalf("exec %q: %v", query, err)
		}
	}
	execStallSQL("INSERT INTO dolt_ignore (pattern, ignored) VALUES ('issues', true)")
	if err := store.Commit(ctx, "test: register ignore pattern"); err != nil {
		t.Fatalf("Commit(register ignore pattern): %v", err)
	}
	execStallSQL("UPDATE issues SET title = 'stranded' WHERE id = 'fpp-1'")
	return store
}

// headAndDirtyRows reads HEAD and the dolt_status row count for issues on a
// fresh, live context.
func headAndDirtyRows(t *testing.T, store *EmbeddedDoltStore) (head string, dirtyRows int) {
	t.Helper()
	ctx := t.Context()
	head, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatalf("GetCurrentCommit: %v", err)
	}
	db, cleanup, err := OpenSQL(ctx, store.dataDir, store.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_status WHERE table_name = 'issues'").Scan(&dirtyRows); err != nil {
		t.Fatalf("read dolt_status: %v", err)
	}
	return head, dirtyRows
}

// TestCommitAllKeepsOperationalFailurePrecedenceAndNonTolerantBehavior pins
// GH#5433 AC-6: the residual finding may only ever attach to a commit whose
// SQL publication was confirmed. A publication/operation failure keeps its
// own error and its indeterminate classification — never the typed finding,
// never a claimed success — and the non-tolerant merge-resolution path keeps
// its pre-existing refusal rather than growing the tolerant classification.
func TestCommitAllKeepsOperationalFailurePrecedenceAndNonTolerantBehavior(t *testing.T) {
	issueopsStillRefuses := func(t *testing.T, err error) {
		t.Helper()
		var residual *storage.UncommittedChangesError
		if errors.As(err, &residual) {
			t.Fatalf("error = typed residual finding %v, want the operational failure to retain precedence", err)
		}
		var verification *storage.CommitVerificationError
		if errors.As(err, &verification) {
			t.Fatalf("error = typed verification finding %v, want the operational failure to retain precedence", err)
		}
		if errors.Is(err, storage.ErrCommitVerificationFailed) {
			t.Fatalf("error = verification sentinel %v, want the operational failure", err)
		}
	}

	t.Run("canceled context keeps the operational failure, no replay", func(t *testing.T) {
		store := stallFixtureStore(t, "fppcancel")
		headBefore, dirtyBefore := headAndDirtyRows(t, store)
		if dirtyBefore == 0 {
			t.Fatal("fixture did not strand the issues write")
		}

		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		committed, commitErr := store.CommitAll(canceled, "test: canceled commit")
		issueopsStillRefuses(t, commitErr)
		if committed {
			t.Fatal("CommitAll claimed committed=true on a canceled context")
		}
		if !errors.Is(commitErr, context.Canceled) {
			t.Fatalf("CommitAll error = %v, want the canceled-context operational failure", commitErr)
		}

		// Nothing moved and nothing was replayed: the stranded write is
		// still exactly the working set it was.
		headAfter, dirtyAfter := headAndDirtyRows(t, store)
		if headAfter != headBefore {
			t.Fatalf("HEAD moved across the failed commit: %s -> %s", headBefore, headAfter)
		}
		if dirtyAfter != dirtyBefore {
			t.Fatalf("issues dolt_status rows changed across the failed commit: %d -> %d", dirtyBefore, dirtyAfter)
		}
	})

	t.Run("non-tolerant merge resolution keeps the Dolt refusal", func(t *testing.T) {
		store := stallFixtureStore(t, "fppmerge")
		headBefore, dirtyBefore := headAndDirtyRows(t, store)
		if dirtyBefore == 0 {
			t.Fatal("fixture did not strand the issues write")
		}

		err := store.CommitMergeResolution(t.Context(), "test: merge resolution")
		issueopsStillRefuses(t, err)
		// The non-tolerant contract: DOLT_COMMIT's nothing-to-commit refusal
		// is returned as the failure it is, not reclassified as a tolerant
		// no-op and not decorated with the tolerant paths' typed finding.
		if !issueops.IsNothingToCommitError(err) {
			t.Fatalf("CommitMergeResolution error = %v, want the preserved non-tolerant nothing-to-commit refusal", err)
		}
		headAfter, dirtyAfter := headAndDirtyRows(t, store)
		if headAfter != headBefore {
			t.Fatalf("HEAD moved across the refused merge resolution: %s -> %s", headBefore, headAfter)
		}
		if dirtyAfter != dirtyBefore {
			t.Fatalf("issues dolt_status rows changed across the refused merge resolution: %d -> %d", dirtyBefore, dirtyAfter)
		}
	})
}
