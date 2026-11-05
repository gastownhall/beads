package versioncontrolops

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

// TestMergeSettleDrainsCallResultSets pins every CALL DOLT_* the merge-settle
// machinery issues, including concludeMerge's stage and commit (GH#7811) and
// the StageAndCommit pair, to schema.DrainCall (QueryContext plus a drain of
// every result set) instead of plain ExecContext (GH#5556). Callers
// pin the session (a *sql.Tx in server mode, a *sql.Conn returned to the pool
// afterwards by MergeWithStrategy), so a CALL that fails before
// go-sql-driver/mysql's own handleOk.discardResults() leaves its result sets
// unread and poisons every later statement on that session ("busy buffer" ->
// "driver: bad connection"); see schema.DrainCall's doc comment.
//
// sqlmock satisfies an ExpectQuery only for a driver Query call, so in ordered
// mode a CALL reverted to ExecContext fails with "call to ExecQuery ... was not
// expected". The recordingConn wrapper also catches reverts on paths that
// swallow the CALL's error (abortMerge, the "up to date" merge swallow,
// StageAndCommit's and concludeMerge's "nothing to commit"): every subtest
// asserts no CALL reached ExecContext.
func TestMergeSettleDrainsCallResultSets(t *testing.T) {
	t.Run("merge_and_settle_metadata", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectWorkingSetClean(mock)
		expectConflictFlags(mock)
		expectDrainedCall(mock, "CALL DOLT_MERGE(?)", "origin/main")
		expectConflicts(mock, "metadata")
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")
		expectDrainedCall(mock, "CALL DOLT_ADD('metadata')")
		expectNoViolations(mock)
		expectMergeResult(mock, "labels")
		expectDrainedCall(mock, "CALL DOLT_COMMIT('-m', ?)", autoResolveCommitMsg)

		if err := MergeAndSettleWithStrategy(context.Background(), rec, "origin/main", ""); err != nil {
			t.Fatalf("MergeAndSettleWithStrategy: %v", err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("merge_with_strategy_clean", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectWorkingSetClean(mock)
		expectConflictFlags(mock)
		expectDrainedCall(mock, "CALL DOLT_MERGE('--author', ?, ?)", testAuthor, "feature")
		expectConflicts(mock)

		conflicts, err := MergeWithStrategy(context.Background(), rec, "feature", testAuthor, "theirs")
		if err != nil || conflicts != nil {
			t.Fatalf("MergeWithStrategy = (%v, %v), want (nil, nil)", conflicts, err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("merge_with_strategy_conflicted", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectStrategyResolved(mock)
		expectDrainedCall(mock, "CALL DOLT_ADD(?)", "issues")
		expectMergeResult(mock, "labels")
		expectDrainedCall(mock, "CALL DOLT_COMMIT('-m', ?, '--author', ?)", strategyCommitMsg, testAuthor)

		conflicts, err := MergeWithStrategy(context.Background(), rec, "feature", testAuthor, "theirs")
		if err != nil || len(conflicts) != 1 {
			t.Fatalf("MergeWithStrategy = (%v, %v), want (one conflict, nil)", conflicts, err)
		}
		rec.assertDrained(t, mock)
	})

	// The unauthored commit form (the proxied-server plane's) drains too.
	t.Run("stage_and_commit_unauthored", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectPendingChanges(mock)
		expectDrainedCall(mock, "CALL DOLT_ADD(?)", "issues")
		expectStagedChanges(mock)
		expectDrainedCall(mock, "CALL DOLT_COMMIT('-m', ?)", "bd: write")

		if err := StageAndCommit(context.Background(), rec, map[string]bool{"issues": true}, "bd: write", ""); err != nil {
			t.Fatalf("StageAndCommit: %v", err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("auto_resolve_config_and_schema_migrations", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectConflicts(mock, "config", "schema_migrations")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT our_key, their_key FROM dolt_conflicts_config")).
			WillReturnRows(sqlmock.NewRows([]string{"our_key", "their_key"}).AddRow("kv.memory.a", "kv.memory.a"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT our_version, their_version, our_content_hash, their_content_hash")).
			WillReturnRows(sqlmock.NewRows([]string{"our_version", "their_version", "our_content_hash", "their_content_hash"}).
				AddRow(7, 7, nil, "h7"))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(our_key, their_key) FROM dolt_conflicts_config")).
			WillReturnRows(sqlmock.NewRows([]string{"key"}))
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'config')")
		expectDrainedCall(mock, "CALL DOLT_ADD('config')")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT our_version, our_content_hash, their_content_hash")).
			WillReturnRows(sqlmock.NewRows([]string{"our_version", "our_content_hash", "their_content_hash"}).
				AddRow(7, nil, "h7"))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE schema_migrations SET content_hash = ? WHERE version = ?")).
			WithArgs("h7", int64(7)).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--ours', 'schema_migrations')")
		expectDrainedCall(mock, "CALL DOLT_ADD('schema_migrations')")

		resolved, err := TryAutoResolveMergeConflicts(context.Background(), rec)
		if err != nil || !resolved {
			t.Fatalf("TryAutoResolveMergeConflicts = (%v, %v), want (true, nil)", resolved, err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("fk_cascade_repair_labels", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectViolations(mock, "labels")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT violation_type, violation_info FROM dolt_constraint_violations_labels")).
			WillReturnRows(sqlmock.NewRows([]string{"violation_type", "violation_info"}).
				AddRow("foreign key", `{"ReferencedTable":"issues"}`))
		mock.ExpectExec(regexp.QuoteMeta(fkCascadeRepairDeletes["labels"])).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dolt_constraint_violations_labels")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDrainedCall(mock, "CALL DOLT_ADD('labels')")
		expectNoViolations(mock)

		repaired, had, err := TryRepairFKCascadeViolations(context.Background(), rec)
		if err != nil || !repaired || !had {
			t.Fatalf("TryRepairFKCascadeViolations = (%v, %v, %v), want (true, true, nil)", repaired, had, err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("settle_merge_strategy_theirs", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectConflicts(mock, "wisps") // the auto-resolver declines wisps
		expectConflicts(mock, "wisps") // GetConflicts
		expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'wisps')")
		expectDrainedCall(mock, "CALL DOLT_ADD(?)", "wisps")
		expectNoViolations(mock)
		expectMergeResult(mock)
		expectDrainedCall(mock, "CALL DOLT_COMMIT('-m', ?)", "Resolve merge conflicts using 'theirs' strategy")

		if err := SettleMerge(context.Background(), rec, nil, true, "theirs"); err != nil {
			t.Fatalf("SettleMerge: %v", err)
		}
		rec.assertDrained(t, mock)
	})

	// A merge whose only artifact is an FK cascade violation the repair
	// clears is concluded by CommitSettledMerge (GH#7811).
	t.Run("settle_merge_fk_repair_only", func(t *testing.T) {
		rec, mock := newDrainMock(t)
		expectConflicts(mock) // the auto-resolver finds no conflict
		expectConflicts(mock) // GetConflicts
		expectViolations(mock, "labels")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT violation_type, violation_info FROM dolt_constraint_violations_labels")).
			WillReturnRows(sqlmock.NewRows([]string{"violation_type", "violation_info"}).
				AddRow("foreign key", `{"ReferencedTable":"issues"}`))
		mock.ExpectExec(regexp.QuoteMeta(fkCascadeRepairDeletes["labels"])).
			WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dolt_constraint_violations_labels")).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDrainedCall(mock, "CALL DOLT_ADD('labels')")
		expectNoViolations(mock)
		expectMergeResult(mock, "config")
		expectDrainedCall(mock, "CALL DOLT_COMMIT('-m', ?)", fkRepairCommitMsg)

		if err := SettleMerge(context.Background(), rec, nil, true, ""); err != nil {
			t.Fatalf("SettleMerge: %v", err)
		}
		rec.assertDrained(t, mock)
	})

	t.Run("abort_merge", func(t *testing.T) {
		abortFailed := errors.New("fatal: There is no merge to abort")
		for _, tc := range []struct {
			name          string
			preMergeClean bool
			abortErr      error
			wantReset     bool
		}{
			{name: "abort succeeds", preMergeClean: true},
			{name: "abort fails on a clean working set", preMergeClean: true, abortErr: abortFailed, wantReset: true},
			{name: "abort fails on a dirty working set", preMergeClean: false, abortErr: abortFailed},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rec, mock := newDrainMock(t)
				if tc.abortErr != nil {
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--abort')")).WillReturnError(tc.abortErr)
				} else {
					expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")
				}
				if tc.wantReset {
					expectDrainedCall(mock, "CALL DOLT_RESET('--hard')")
				}

				abortMerge(context.Background(), rec, tc.preMergeClean)

				rec.assertDrained(t, mock)
				if !tc.wantReset && rec.sentStatement("DOLT_RESET") {
					t.Errorf("abortMerge issued DOLT_RESET; want none (statements: execs %q, queries %q)", rec.execs, rec.queries)
				}
			})
		}
	})

	// A converted CALL's error must reach the caller exactly as before: same
	// wrapper text, and errors.Is still finds the driver's error. The two
	// DOLT_MERGE sites still swallow "up to date", and StageAndCommit still
	// tolerates "nothing to commit".
	t.Run("errors_preserved", func(t *testing.T) {
		ctx := context.Background()
		upToDate := errors.New("Already up to date.")
		nothingToCommit := errors.New("nothing to commit, working tree clean")
		for _, tc := range []struct {
			name  string
			setup func(mock sqlmock.Sqlmock, cause error)
			run   func(db DBConn) error
			// want is the returned error's text; "" means nil.
			want string
		}{
			{
				name: "MergeAndSettle DOLT_MERGE",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectWorkingSetClean(mock)
					expectConflictFlags(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE(?)")).WithArgs("origin/main").WillReturnError(cause)
					expectConflicts(mock)
					expectConflicts(mock)
					expectNoViolations(mock)
					expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")
				},
				run:  func(db DBConn) error { return MergeAndSettleWithStrategy(ctx, db, "origin/main", "") },
				want: "injected",
			},
			{
				name: "MergeAndSettle DOLT_MERGE up to date",
				setup: func(mock sqlmock.Sqlmock, _ error) {
					expectWorkingSetClean(mock)
					expectConflictFlags(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE(?)")).WithArgs("origin/main").WillReturnError(upToDate)
					expectConflicts(mock)
					expectConflicts(mock)
					expectNoViolations(mock)
				},
				run: func(db DBConn) error { return MergeAndSettleWithStrategy(ctx, db, "origin/main", "") },
			},
			{
				name: "MergeWithStrategy DOLT_MERGE",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectWorkingSetClean(mock)
					expectConflictFlags(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--author', ?, ?)")).
						WithArgs(testAuthor, "feature").WillReturnError(cause)
					expectConflicts(mock)
					expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")
				},
				run: func(db DBConn) error {
					_, err := MergeWithStrategy(ctx, db, "feature", testAuthor, "theirs")
					return err
				},
				want: "merge branch feature: injected",
			},
			{
				name: "MergeWithStrategy DOLT_MERGE up to date",
				setup: func(mock sqlmock.Sqlmock, _ error) {
					expectWorkingSetClean(mock)
					expectConflictFlags(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_MERGE('--author', ?, ?)")).
						WithArgs(testAuthor, "feature").WillReturnError(upToDate)
					expectConflicts(mock)
				},
				run: func(db DBConn) error {
					_, err := MergeWithStrategy(ctx, db, "feature", testAuthor, "theirs")
					return err
				},
			},
			{
				name: "MergeWithStrategy DOLT_ADD",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectStrategyResolved(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).WithArgs("issues").WillReturnError(cause)
					expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")
				},
				run: func(db DBConn) error {
					_, err := MergeWithStrategy(ctx, db, "feature", testAuthor, "theirs")
					return err
				},
				want: "conflicts resolved but commit failed: injected",
			},
			{
				name: "MergeWithStrategy DOLT_COMMIT",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectStrategyResolved(mock)
					expectDrainedCall(mock, "CALL DOLT_ADD(?)", "issues")
					expectMergeResult(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
						WithArgs(strategyCommitMsg, testAuthor).WillReturnError(cause)
					expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")
				},
				run: func(db DBConn) error {
					_, err := MergeWithStrategy(ctx, db, "feature", testAuthor, "theirs")
					return err
				},
				want: "conflicts resolved but commit failed: injected",
			},
			{
				name: "StageAndCommit DOLT_COMMIT nothing to commit",
				setup: func(mock sqlmock.Sqlmock, _ error) {
					expectPendingChanges(mock)
					expectDrainedCall(mock, "CALL DOLT_ADD(?)", "issues")
					expectStagedChanges(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?, '--author', ?)")).
						WithArgs("bd: write", testAuthor).WillReturnError(nothingToCommit)
				},
				run: func(db DBConn) error {
					return StageAndCommit(ctx, db, map[string]bool{"issues": true}, "bd: write", testAuthor)
				},
			},
			{
				name: "SettleMerge strategy DOLT_ADD",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectConflicts(mock, "wisps")
					expectConflicts(mock, "wisps")
					expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'wisps')")
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).WithArgs("wisps").WillReturnError(cause)
					expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")
				},
				run:  func(db DBConn) error { return SettleMerge(ctx, db, nil, true, "theirs") },
				want: "stage resolved wisps: injected",
			},
			{
				name: "SettleMerge strategy DOLT_COMMIT",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectConflicts(mock, "wisps")
					expectConflicts(mock, "wisps")
					expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'wisps')")
					expectDrainedCall(mock, "CALL DOLT_ADD(?)", "wisps")
					expectNoViolations(mock)
					expectMergeResult(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
						WithArgs("Resolve merge conflicts using 'theirs' strategy").WillReturnError(cause)
					expectDrainedCall(mock, "CALL DOLT_MERGE('--abort')")
				},
				run:  func(db DBConn) error { return SettleMerge(ctx, db, nil, true, "theirs") },
				want: "conflicts resolved but commit failed: injected",
			},
			{
				name: "auto-resolve config DOLT_CONFLICTS_RESOLVE",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectConflicts(mock, "config")
					mock.ExpectQuery(regexp.QuoteMeta("SELECT our_key, their_key FROM dolt_conflicts_config")).
						WillReturnRows(sqlmock.NewRows([]string{"our_key", "their_key"}).AddRow("kv.memory.a", "kv.memory.a"))
					mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(our_key, their_key) FROM dolt_conflicts_config")).
						WillReturnRows(sqlmock.NewRows([]string{"key"}))
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'config')")).WillReturnError(cause)
				},
				run:  func(db DBConn) error { _, err := TryAutoResolveMergeConflicts(ctx, db); return err },
				want: "failed to resolve config conflicts: injected",
			},
			{
				name: "auto-resolve metadata DOLT_CONFLICTS_RESOLVE",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectConflicts(mock, "metadata")
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")).WillReturnError(cause)
				},
				run:  func(db DBConn) error { _, err := TryAutoResolveMergeConflicts(ctx, db); return err },
				want: "failed to resolve metadata conflicts: injected",
			},
			{
				name: "auto-resolve DOLT_ADD",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectConflicts(mock, "metadata")
					expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'metadata')")
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('metadata')")).WillReturnError(cause)
				},
				run:  func(db DBConn) error { _, err := TryAutoResolveMergeConflicts(ctx, db); return err },
				want: "failed to stage metadata: injected",
			},
			{
				name: "CommitResolvedConflicts DOLT_COMMIT",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectMergeResult(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
						WithArgs(autoResolveCommitMsg).WillReturnError(cause)
				},
				run:  func(db DBConn) error { return CommitResolvedConflicts(ctx, db) },
				want: "failed to commit resolved conflicts: injected",
			},
			{
				name: "concludeMerge stage DOLT_ADD",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectMergeSourceCommit(mock)
					mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).
						WillReturnRows(sqlmock.NewRows([]string{"table_name"}).AddRow("labels"))
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD(?)")).WithArgs("labels").WillReturnError(cause)
				},
				run:  func(db DBConn) error { return CommitResolvedConflicts(ctx, db) },
				want: "failed to commit resolved conflicts: stage merged labels: injected",
			},
			{
				name: "concludeMerge DOLT_COMMIT nothing to commit",
				setup: func(mock sqlmock.Sqlmock, _ error) {
					expectMergeResult(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
						WithArgs(autoResolveCommitMsg).WillReturnError(nothingToCommit)
				},
				run: func(db DBConn) error { return CommitResolvedConflicts(ctx, db) },
			},
			{
				name: "CommitSettledMerge repair DOLT_COMMIT",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectMergeResult(mock)
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_COMMIT('-m', ?)")).
						WithArgs(fkRepairCommitMsg).WillReturnError(cause)
				},
				run:  func(db DBConn) error { return CommitSettledMerge(ctx, db, false, true) },
				want: "failed to commit FK cascade repair: injected",
			},
			{
				name: "schema_migrations DOLT_CONFLICTS_RESOLVE",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					mock.ExpectQuery(regexp.QuoteMeta("SELECT our_version, our_content_hash, their_content_hash")).
						WillReturnRows(sqlmock.NewRows([]string{"our_version", "our_content_hash", "their_content_hash"}))
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_CONFLICTS_RESOLVE('--ours', 'schema_migrations')")).WillReturnError(cause)
				},
				run:  func(db DBConn) error { return resolveSchemaMigrationsVintageConflicts(ctx, db) },
				want: "failed to resolve schema_migrations conflicts: injected",
			},
			{
				name: "FK cascade repair DOLT_ADD",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					expectViolations(mock, "labels")
					mock.ExpectQuery(regexp.QuoteMeta("SELECT violation_type, violation_info FROM dolt_constraint_violations_labels")).
						WillReturnRows(sqlmock.NewRows([]string{"violation_type", "violation_info"}).
							AddRow("foreign key", `{"ReferencedTable":"issues"}`))
					mock.ExpectExec(regexp.QuoteMeta(fkCascadeRepairDeletes["labels"])).
						WillReturnResult(sqlmock.NewResult(0, 2))
					mock.ExpectExec(regexp.QuoteMeta("DELETE FROM dolt_constraint_violations_labels")).
						WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_ADD('labels')")).WillReturnError(cause)
				},
				run:  func(db DBConn) error { _, _, err := TryRepairFKCascadeViolations(ctx, db); return err },
				want: "stage repaired labels: injected",
			},
			{
				name: "ResolveConflicts DOLT_CONFLICTS_RESOLVE",
				setup: func(mock sqlmock.Sqlmock, cause error) {
					mock.ExpectQuery(regexp.QuoteMeta("CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'wisps')")).WillReturnError(cause)
				},
				run:  func(db DBConn) error { return ResolveConflicts(ctx, db, "wisps", "theirs") },
				want: "resolve conflicts: injected",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rec, mock := newDrainMock(t)
				cause := errors.New("injected")
				tc.setup(mock, cause)

				err := tc.run(rec)
				switch {
				case tc.want == "":
					if err != nil {
						t.Errorf("err = %v, want nil", err)
					}
				case err == nil:
					t.Errorf("err = nil, want %q", tc.want)
				default:
					if err.Error() != tc.want {
						t.Errorf("err = %q, want %q", err.Error(), tc.want)
					}
					if !errors.Is(err, cause) {
						t.Errorf("errors.Is(%v, injected cause) = false", err)
					}
				}
				rec.assertDrained(t, mock)
			})
		}
	})
}

const testAuthor = "Tester <tester@example.com>"

// autoResolveCommitMsg is the message CommitResolvedConflicts concludes a
// merge under, bound to concludeMerge's CALL DOLT_COMMIT('-m', ?).
const autoResolveCommitMsg = "auto-resolve merge conflicts: metadata, dependencies, schema_migrations, config, issues (field-level three-way merge), labels/comments/events (union)"

// fkRepairCommitMsg is the message CommitSettledMerge concludes a merge the
// FK cascade repair alone settled under.
const fkRepairCommitMsg = "auto-repair merge FK cascade violations: deleted rows referencing issues deleted on the other side"

// mergeSourceCommit is the open merge's source commit the stageMergeResult
// reads answer with: a valid Dolt commit hash, so the merged-tables query runs.
const mergeSourceCommit = "0123456789abcdefghijklmnopqrstuv"

// mergedTablesQueryPrefix starts stageMergeResult's merged-but-unstaged
// tables query.
const mergedTablesQueryPrefix = "SELECT DISTINCT CASE WHEN s.table_name = CONCAT(d.from_table_name, ' -> ', d.to_table_name)"

// strategyCommitMsg is the message MergeWithStrategy("feature", "theirs")
// commits a resolved merge under.
const strategyCommitMsg = "Resolve merge conflicts from feature using theirs strategy"

// recordingConn logs every statement sent through ExecContext and QueryContext
// before passing it to the wrapped sqlmock connection. sqlmock's
// ExpectationsWereMet does not flag an unexpected extra statement, and the
// best-effort abortMerge discards the error such a statement gets, so the log
// is what proves no CALL took the exec path and no stray DOLT_RESET ran.
type recordingConn struct {
	DBConn
	execs   []string
	queries []string
}

func (c *recordingConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.execs = append(c.execs, query)
	return c.DBConn.ExecContext(ctx, query, args...)
}

func (c *recordingConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.queries = append(c.queries, query)
	return c.DBConn.QueryContext(ctx, query, args...)
}

// assertDrained fails the test when any expectation is unmet or any CALL was
// sent through ExecContext.
func (c *recordingConn) assertDrained(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
	for _, q := range c.execs {
		if strings.HasPrefix(q, "CALL ") {
			t.Errorf("%q went through ExecContext; CALLs must go through schema.DrainCall", q)
		}
	}
}

// sentStatement reports whether any statement containing substr was sent
// through either path.
func (c *recordingConn) sentStatement(substr string) bool {
	for _, q := range append(append([]string(nil), c.execs...), c.queries...) {
		if strings.Contains(q, substr) {
			return true
		}
	}
	return false
}

func newDrainMock(t *testing.T) (*recordingConn, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &recordingConn{DBConn: db}, mock
}

// expectDrainedCall expects call as a query answering an empty result set, the
// shape schema.DrainCall consumes.
func expectDrainedCall(mock sqlmock.Sqlmock, call string, args ...driver.Value) *sqlmock.ExpectedQuery {
	q := mock.ExpectQuery(regexp.QuoteMeta(call))
	if len(args) > 0 {
		q = q.WithArgs(args...)
	}
	return q.WillReturnRows(sqlmock.NewRows([]string{"status"}))
}

func expectWorkingSetClean(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM dolt_status LIMIT 1")).
		WillReturnRows(sqlmock.NewRows([]string{"1"}))
}

func expectConflictFlags(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_allow_commit_conflicts = 1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("SET @@dolt_force_transaction_commit = 1")).
		WillReturnResult(sqlmock.NewResult(0, 0))
}

// expectConflicts answers one dolt_conflicts read with one conflict per table.
func expectConflicts(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table", "num_conflicts"})
	for _, table := range tables {
		rows.AddRow(table, 1)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table`, num_conflicts FROM dolt_conflicts")).WillReturnRows(rows)
}

func expectNoViolations(mock sqlmock.Sqlmock) {
	expectViolations(mock)
}

// expectStrategyResolved expects MergeWithStrategy("feature", "theirs") up to
// its conclusion: the merge leaves one issues conflict, which is resolved
// --theirs with no constraint violation behind it.
func expectStrategyResolved(mock sqlmock.Sqlmock) {
	expectWorkingSetClean(mock)
	expectConflictFlags(mock)
	expectDrainedCall(mock, "CALL DOLT_MERGE('--author', ?, ?)", testAuthor, "feature")
	expectConflicts(mock, "issues")
	expectDrainedCall(mock, "CALL DOLT_CONFLICTS_RESOLVE('--theirs', 'issues')")
	expectNoViolations(mock)
}

// expectPendingChanges answers StageAndCommit's working-set read
// (issueops.HasPendingChanges) with one pending change.
func expectPendingChanges(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status s")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
}

// expectStagedChanges answers StageAndCommit's post-DOLT_ADD read
// (issueops.HasStagedChanges) with one staged change.
func expectStagedChanges(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*) FROM dolt_status WHERE staged = 1")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
}

// expectMergeSourceCommit answers stageMergeResult's read of the open merge's
// source commit.
func expectMergeSourceCommit(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT source_commit FROM dolt_merge_status WHERE is_merging")).
		WillReturnRows(sqlmock.NewRows([]string{"source_commit"}).AddRow(mergeSourceCommit))
}

// expectMergeResult answers concludeMerge's stageMergeResult (GH#7811): the
// open merge's source commit, then the merged-but-unstaged tables, each
// staged with a drained DOLT_ADD.
func expectMergeResult(mock sqlmock.Sqlmock, tables ...string) {
	expectMergeSourceCommit(mock)
	rows := sqlmock.NewRows([]string{"table_name"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta(mergedTablesQueryPrefix)).WillReturnRows(rows)
	for _, table := range tables {
		expectDrainedCall(mock, "CALL DOLT_ADD(?)", table)
	}
}

// expectViolations answers one dolt_constraint_violations read with tables.
func expectViolations(mock sqlmock.Sqlmock, tables ...string) {
	rows := sqlmock.NewRows([]string{"table"})
	for _, table := range tables {
		rows.AddRow(table)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0")).
		WillReturnRows(rows)
}
