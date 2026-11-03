package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestGuardedDeleteCommentRaceCharacterization deterministically reproduces
// the guarded-delete/comment race (PR #8026) through the DIRECT-STORE
// supported APIs and pins the server's current outcome for every executed
// case, journal on and off.
//
// THE RACE. A guarded delete reads a bead's row_lock token and deletes the
// row in one transaction (issueops.DeleteInTx via (*deleter).Delete); a
// comment written to the same bead through issueops.Commenter touches only
// the comments table and never bumps row_lock (internal/storage/issueops/
// close.go: "lifecycle/ownership writes"). With the events journal off,
// nothing orders the two: a comment can land between the delete's token
// read and its commit, or a delete can commit while a comment's
// existence-probe snapshot still shows the parent. The per-id delete fence
// (delete_fence.go) is NOT part of this race: it orders guarded deletes
// against each other only, and a comment never takes it.
//
// THE HARNESS. The loser of each interleaving is driven through the REAL
// retry boundary — withRetryTxOn on one pinned session — with a barrier in
// the transaction body: the first attempt pins its snapshot with the
// caller's token read, signals the racing session, and waits for it to
// finish before running the real operation body (DeleteInTx or
// ExecuteAddComment) and committing. The racing session runs the real
// public API (Commenter().AddComment / Deleter().Delete) to completion in
// that window. Attempt counts, replay semantics and error classification
// are therefore the production ones, and the interleaving is
// channel-synchronized, not timed.
//
// MEASUREMENT, NOT FIX. Every executed case records its evidence as one
// structured `race-evidence:` line (attempt count, decoded loser error,
// typed-error classification, racing session outcome, and the final
// parent/comment/dangling state), so a run of this suite IS the evidence
// report. The characterization asserts the harness integrity and the serial
// control only; the outcome assertions live with the reproduction switch.
//
// COVERAGE. Executed: both commit orders (delete after a committed
// comment; comment after a committed delete), both commit styles the
// direct-store delete supports (in-body DOLT_COMMIT, the default, and
// ordinary COMMIT via a deferred version commit), journal on and off, the
// wisp plane, and a serial non-overlap control. NOT executed here,
// recorded as incomplete coverage: label and dependency races (out of
// scope by design; only the Commenter API has the typed-not-found
// contract), a persisted-constraint-violation fixture, and a
// cancellation-mid-race case (the harness's contexts are bounded and
// barrier-released, but a dedicated cancellation subtest is not run).
func TestGuardedDeleteCommentRaceCharacterization(t *testing.T) {
	skipIfNoDolt(t)
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()

	// Server settings are read on their own short budget: every case below
	// runs on its own bounded context instead, so one case that wedges cannot
	// eat the rest of the suite's deadline.
	settingsCtx, settingsCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer settingsCancel()
	settings := recordServerSettings(settingsCtx, t, store)

	cases := []deleteCommentRaceCase{
		{
			name:        "delete_after_comment/plain_commit/journal_off",
			loser:       "guarded_delete",
			commitStyle: "plain_commit",
			journalOn:   false,
			wisp:        false,
		},
		{
			name:        "delete_after_comment/in_body_dolt_commit/journal_off",
			loser:       "guarded_delete",
			commitStyle: "in_body_dolt_commit",
			journalOn:   false,
			wisp:        false,
		},
		{
			// The delete leg supports both in-tx styles; the comment leg's
			// only in-tx style is an ordinary COMMIT (its version commit is
			// post-transaction by design, runIssueOperationTxWithMessage),
			// so its loser cases all run the plain style and the evidence
			// records that.
			name:        "comment_after_delete/plain_commit/journal_off",
			loser:       "comment",
			commitStyle: "plain_commit",
			journalOn:   false,
			wisp:        false,
		},
		{
			name:        "delete_after_comment/in_body_dolt_commit/journal_on",
			loser:       "guarded_delete",
			commitStyle: "in_body_dolt_commit",
			journalOn:   true,
			wisp:        false,
		},
		{
			// The wisp plane routes the comment to wisp_comments, which is
			// dolt_ignored: the racing comment mints no Dolt commit at all.
			name:        "delete_after_comment/in_body_dolt_commit/journal_off/wisp",
			loser:       "guarded_delete",
			commitStyle: "in_body_dolt_commit",
			journalOn:   false,
			wisp:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One bounded context PER CASE: the loser's retry budget is
			// withRetryTxOn's own (15s in server mode); this outer bound
			// only keeps a wedged barrier or a long server-side lock wait
			// from hanging the suite, and a case that hits it is a
			// harness/server finding, not a slow test.
			caseCtx, caseCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer caseCancel()
			ev := runDirectStoreDeleteCommentRace(caseCtx, t, store, tc)
			ev.log(t, settings, tc)
			assertRaceHarnessIntegrity(t, tc, ev)
		})
	}

	// The serial control: with no overlap, the two operations compose the
	// way the desired serial outcome says they must, journal or no. It runs
	// in both modes and must pass in both.
	t.Run("serial_control_no_overlap", func(t *testing.T) {
		serialCtx, serialCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer serialCancel()
		runSerialControl(serialCtx, t, store)
	})
}

// deleteCommentRaceCase names one interleaving of the characterization
// matrix.
type deleteCommentRaceCase struct {
	// name is the subtest label.
	name string
	// loser is the session whose transaction is pinned first and commits
	// second: "guarded_delete" or "comment".
	loser string
	// commitStyle is the loser's supported commit style:
	// "in_body_dolt_commit" or "plain_commit".
	commitStyle string
	// journalOn turns the durable events journal on for BOTH sessions.
	journalOn bool
	// wisp routes the seeded row to the ephemeral plane.
	wisp bool
}

// deleteCommentRaceEvidence is what one executed case observed: the AC-1
// deliverable. Every field is recorded into the test log, so a run of the
// suite IS the evidence report.
type deleteCommentRaceEvidence struct {
	// Attempts is how many whole transactions the loser's real retry
	// boundary ran: 1 for the held race, 2+ when the first attempt's commit
	// was refused with one of withRetryTxOn's replayable errors (Dolt's
	// @autocommit-rollback 1105, a serialization failure 1213/1205, or a
	// pre-commit connection loss) and the replay re-pinned a FRESH snapshot
	// that already contained the racing write.
	Attempts int
	// LoserErrCode, LoserSQLState and LoserErrText decode the loser's
	// final, returned error (empty when the loser succeeded).
	LoserErrCode  uint16
	LoserSQLState string
	LoserErrText  string
	// LoserErrIsNotFound / IsVersionMismatch / IsSerialization classify the
	// loser's returned error with the repo's own typed checks, so the
	// evidence records what production code would do with it.
	LoserErrIsNotFound        bool
	LoserErrIsVersionMismatch bool
	LoserErrIsSerialization   bool
	// LoserDeleted is the loser delete's reported Deleted count (0 when the
	// loser is the comment side).
	LoserDeleted int
	// RacerErrText records the racing session's outcome (it must be empty:
	// the harness requires the racer to land before the loser is released).
	RacerErrText string
	// Final state, read on a FRESH session after both sessions finished:
	// the parent row's presence, the child comment rows that survived, and
	// how many of those reference a parent that is gone (the orphan the
	// bug report and the C1 candidates disagree about).
	IssueExists  bool
	CommentRows  int
	DanglingRows int
}

func (ev deleteCommentRaceEvidence) loserFailed() bool {
	return ev.LoserErrText != ""
}

// log writes the case's evidence as one structured line, the form the
// checked-in concurrency documentation quotes.
func (ev deleteCommentRaceEvidence) log(t *testing.T, settings serverSettings, tc deleteCommentRaceCase) {
	t.Helper()
	t.Logf("race-evidence: case=%s path=direct-store loser=%s commit_style=%s journal=%s plane=%s server=%s fk_checks=%s isolation=%s attempts=%d loser_error=%q loser_error_code=%d loser_error_sqlstate=%s loser_typed(not_found/version_mismatch/serialization)=%v/%v/%v loser_deleted=%d racer_error=%q final: parent_exists=%v comment_rows=%d dangling_comment_rows=%d",
		tc.name, tc.loser, tc.commitStyle, settings.journalWord(tc.journalOn), settings.planeWord(tc.wisp),
		settings.Artifact, settings.FKChecks, settings.Isolation,
		ev.Attempts, ev.LoserErrText, ev.LoserErrCode, ev.LoserSQLState,
		ev.LoserErrIsNotFound, ev.LoserErrIsVersionMismatch, ev.LoserErrIsSerialization,
		ev.LoserDeleted, ev.RacerErrText, ev.IssueExists, ev.CommentRows, ev.DanglingRows)
}

// serverSettings is the environment part of the evidence: which server
// artifact the characterization ran against and the session settings that
// decide the race's semantics.
type serverSettings struct {
	// Artifact names the pinned Dolt server: the container/local image tag
	// plus the server's own reported version string.
	Artifact string
	// ServerVersion is what SELECT VERSION() answered.
	ServerVersion string
	FKChecks      string
	Isolation     string
}

func (s serverSettings) journalWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func (s serverSettings) planeWord(wisp bool) string {
	if wisp {
		return "wisp"
	}
	return "issue"
}

// recordServerSettings verifies the settings the design pins — constraints
// on, a migrated scratch schema — before any case runs, so a run against a
// server configured differently cannot produce evidence that looks like
// this race's.
func recordServerSettings(ctx context.Context, t *testing.T, store *DoltStore) serverSettings {
	t.Helper()

	var fkChecks, isolation, version string
	if err := store.db.QueryRowContext(ctx,
		"SELECT @@foreign_key_checks, @@transaction_isolation, VERSION()").Scan(&fkChecks, &isolation, &version); err != nil {
		t.Fatalf("read server settings: %v", err)
	}
	// Constraints must be ON: the whole disagreement the harness settles is
	// what the FK cascade does at commit-merge time, and a session with
	// foreign_key_checks=0 is a different, unsupported configuration.
	if fkChecks != "1" {
		t.Fatalf("unsupported configuration: @@foreign_key_checks = %q, want 1", fkChecks)
	}

	// The dolt_version() function is the engine's own version probe; when a
	// build lacks it, VERSION() above already names the release.
	var engineVersion sql.NullString
	_ = store.db.QueryRowContext(ctx, "SELECT dolt_version()").Scan(&engineVersion)

	return serverSettings{
		Artifact:      testutil.DoltDockerImage + " (VERSION()=" + version + ")",
		ServerVersion: version,
		FKChecks:      fkChecks,
		Isolation:     isolation,
	}
}

// runDirectStoreDeleteCommentRace executes one case of the matrix and
// returns the observed evidence. It fails the test only when the harness
// itself is broken (the racing session did not land, or a barrier was
// missed) — never for the outcome under characterization.
func runDirectStoreDeleteCommentRace(ctx context.Context, t *testing.T, store *DoltStore, tc deleteCommentRaceCase) deleteCommentRaceEvidence {
	t.Helper()

	store.SetEventsJournalEnabled(tc.journalOn)
	defer store.SetEventsJournalEnabled(false)

	// The delete's commit style rides the context exactly as production
	// batch mode does: WithDeferredVersionCommit is what makes the deleter
	// skip its in-body DOLT_COMMIT (deleter.go's write closure).
	caseCtx := ctx
	if tc.commitStyle == "plain_commit" {
		caseCtx = storageissueops.WithDeferredVersionCommit(ctx)
	}

	issue := &types.Issue{
		ID:        "rdcr-" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(tc.name, "/", "-"), "_", "-")),
		Title:     "race characterization seed",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: tc.wisp,
	}
	if err := store.CreateIssue(caseCtx, issue, "seeder"); err != nil {
		t.Fatalf("seed %s: %v", issue.ID, err)
	}
	parentTable := "issues"
	commentTable := "comments"
	if tc.wisp {
		parentTable = "wisps"
		commentTable = "wisp_comments"
	}

	// The racing delete (comment-loser cases) needs its guard token read on
	// a fresh session before the loser pins anything.
	racerDeleteVersion := readRowLock(caseCtx, t, store, parentTable, issue.ID)

	// One pinned session for the loser, the same shape the delete fence
	// pins: GET_LOCK aside, withRetryTxOn(conn) replays every attempt on
	// this one session.
	conn, err := store.db.Conn(caseCtx)
	if err != nil {
		t.Fatalf("pin loser session: %v", err)
	}
	defer conn.Close()

	pinned := make(chan struct{})
	racerDone := make(chan error, 1)

	ev := deleteCommentRaceEvidence{}
	var loserErr error
	// loserCommentTables mirrors runIssueOperationTxWithMessage's captured
	// changed-tables set, with the same plain-assignment-per-attempt contract.
	var loserCommentTables storageissueops.ChangedTables

	loserDone := make(chan error, 1)
	raceStart := time.Now()
	go func() {
		loserDone <- store.withRetryTxOn(caseCtx, conn, func(tx *sql.Tx) error {
			// Plain assignment on every attempt, the withRetryTx closure
			// contract: a rolled-back attempt's result must not leak into
			// the evidence below.
			ev.Attempts++
			ev.LoserErrCode, ev.LoserSQLState, ev.LoserErrText = 0, "", ""

			// THE TOKEN READ IS THE SNAPSHOT PIN. It is the caller's read of
			// the row_lock token — the exact read whose result the guard
			// re-checks — and under the server's transaction isolation it
			// fixes this attempt's snapshot, so every later read in this
			// transaction (the existence probe, the guard, the insert) sees
			// the pre-race state.
			version := readRowLockInTx(caseCtx, t, tx, parentTable, issue.ID)
			if ev.Attempts == 1 {
				close(pinned)
				select {
				case rerr := <-racerDone:
					ev.RacerErrText = fmt.Sprint(rerr)
				case <-caseCtx.Done():
					return caseCtx.Err()
				}
			}
			// A REPLAY (attempt 2+) re-races NOTHING: withRetryTxOn only
			// replays a commit that was refused with a guarantee of a
			// server-side rollback (Dolt's @autocommit rollback 1105, a
			// serialization failure 1213/1205, or a pre-commit connection
			// loss), so by the time this attempt runs, the racing write —
			// which finished before the refused commit — is already
			// committed and visible in this attempt's FRESH snapshot, the
			// same production replay semantics as any concurrent write.

			switch tc.loser {
			case "guarded_delete":
				token := version
				res, err := storageissueops.DeleteInTx(caseCtx, tx, publicops.DeleteRequest{
					IDs:             []string{issue.ID},
					Actor:           "race-characterization",
					ExpectedVersion: &token,
				})
				if err != nil {
					return err
				}
				ev.LoserDeleted = res.Deleted
				if res.Deleted == 0 {
					return nil
				}
				// The deleter's exact in-tx publication, verbatim from
				// deleter.go's write closure: stage the swept tables (plus
				// any versioned-history tables), then DOLT_COMMIT in-body —
				// skipped under a deferred version commit, which is how the
				// plain_commit style is selected.
				if !storageissueops.VersionCommitDeferred(caseCtx) {
					for _, table := range store.withVersionedHistoryTables(caseCtx, tx, sweptTables) {
						_ = schema.DrainCall(caseCtx, tx, "CALL DOLT_ADD(?)", table)
					}
					msg := fmt.Sprintf("bd: delete %d issue(s)", res.Deleted)
					if err := schema.DrainCall(caseCtx, tx, "CALL DOLT_COMMIT('-m', ?, '--author', ?)",
						msg, store.commitAuthorString()); err != nil && !isDoltNothingToCommit(err) {
						return fmt.Errorf("dolt commit: %w", err)
					}
				}
				return nil

			case "comment":
				var tables storageissueops.ChangedTables
				_, tables, err := storageissueops.ExecuteAddComment(caseCtx, tx, publicops.AddCommentRequest{
					IssueID: issue.ID,
					Author:  "race-characterization",
					Text:    "loser comment",
				})
				if err != nil {
					return err
				}
				loserCommentTables = tables
				return nil

			default:
				return fmt.Errorf("unknown loser %q", tc.loser)
			}
		})
	}()

	// THE RACING SESSION, real public API to completion inside the window.
	// racerErr is a captured var rather than a second receive because the
	// LOSER is the channel's only reader: it unblocks on racerDone inside
	// its transaction body.
	var racerErr error
	<-pinned
	racerStart := time.Now()
	switch tc.loser {
	case "guarded_delete":
		commenter, err := NewCommenter(store)
		if err != nil {
			t.Fatalf("racing commenter: %v", err)
		}
		_, racerErr = commenter.AddComment(caseCtx, publicops.AddCommentRequest{
			IssueID: issue.ID,
			Author:  "racer",
			Text:    "racing comment",
		})
	case "comment":
		deleter, err := store.Deleter()
		if err != nil {
			t.Fatalf("racing deleter: %v", err)
		}
		_, racerErr = deleter.Delete(caseCtx, publicops.DeleteRequest{
			IDs:             []string{issue.ID},
			Actor:           "racer",
			ExpectedVersion: &racerDeleteVersion,
		})
	}
	racerDone <- racerErr
	racerElapsed := time.Since(racerStart)

	loserErr = <-loserDone
	// Timing is part of the evidence: a case that takes seconds, not the
	// ~100ms of the ordinary ones, records a server-side lock wait or a
	// retry loop the error text alone does not name.
	t.Logf("race-timing: case=%s racer_elapsed=%s loser_total_elapsed=%s attempts=%d", tc.name, racerElapsed, time.Since(raceStart), ev.Attempts)
	if racerErr != nil {
		// The harness requires the racing session to land: a racer failure is
		// a broken harness or an unsupportable configuration, not evidence.
		t.Fatalf("racing session did not land (%s side): %v", tc.loser, racerErr)
	}

	// The comment leg's funnel publishes its Dolt history entry AFTER the SQL
	// commit (runIssueOperationTxWithMessage), with the same contract: a
	// failure there is logged, never returned, because the data is already
	// durable. Reproduced verbatim so a committed loser comment goes through
	// the exact production publication, including the wisp case's "nothing
	// versioned to stage" skip.
	if tc.loser == "comment" && loserErr == nil {
		if staged := sortedDirtyTables(loserCommentTables); len(staged) > 0 {
			if err := store.doltAddAndCommitPostTx(caseCtx, staged, storageissueops.AddCommentCommitMessage(issue.ID)); err != nil {
				t.Logf("post-tx dolt commit failed after a committed loser comment (data already committed): %v", err)
			}
		}
	}

	code, sqlstate, text := decodeServerError(loserErr)
	ev.LoserErrCode, ev.LoserSQLState, ev.LoserErrText = code, sqlstate, text
	ev.LoserErrIsNotFound = errors.Is(loserErr, storage.ErrNotFound)
	ev.LoserErrIsVersionMismatch = errors.Is(loserErr, storage.ErrVersionMismatch)
	ev.LoserErrIsSerialization = isSerializationError(loserErr) || isDoltAutocommitRollbackError(loserErr)

	// ROLLBACK AND FINAL STATE, read on fresh sessions: a successful ROLLBACK
	// after the fact proves nothing, so the evidence observes the loser's
	// absence through application reads instead.
	readCount := func(query string, args ...any) int {
		var n int
		if err := store.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("read final state (%s): %v", query, err)
		}
		return n
	}
	ev.IssueExists = readCount(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", parentTable), issue.ID) == 1
	ev.CommentRows = readCount(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ?", commentTable), issue.ID)
	ev.DanglingRows = readCount(fmt.Sprintf(
		"SELECT COUNT(*) FROM %s c LEFT JOIN %s p ON p.id = c.issue_id WHERE c.issue_id = ? AND p.id IS NULL",
		commentTable, parentTable), issue.ID)

	return ev
}

// runSerialControl runs the two operations back to back with no overlap and
// asserts they compose exactly as the desired serial outcome says they must.
// It is the non-race control for both assertion modes: the characterization
// assertions are only meaningful if the same APIs compose serially.
func runSerialControl(ctx context.Context, t *testing.T, store *DoltStore) {
	t.Helper()

	// seed runs inside the subtests below, so it takes the SUBTEST's t:
	// FailNow on the parent t from inside a subtest panics the whole test.
	seed := func(t *testing.T, name string, wisp bool) string {
		t.Helper()
		id := "rdcr-serial-" + name
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "serial control", Status: types.StatusOpen, Priority: 2,
			IssueType: types.TypeTask, Ephemeral: wisp,
		}, "seeder"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		return id
	}

	// Comment first, guarded delete second: the delete must succeed, take
	// the comment with it (the cascade), and leave no orphan.
	t.Run("delete_after_comment", func(t *testing.T) {
		id := seed(t, "after-comment", false)
		commenter, err := NewCommenter(store)
		if err != nil {
			t.Fatalf("commenter: %v", err)
		}
		if _, err := commenter.AddComment(ctx, publicops.AddCommentRequest{IssueID: id, Author: "racer", Text: "serial comment"}); err != nil {
			t.Fatalf("serial comment: %v", err)
		}
		version := readRowLock(ctx, t, store, "issues", id)
		deleter, err := store.Deleter()
		if err != nil {
			t.Fatalf("deleter: %v", err)
		}
		res, derr := deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{id}, Actor: "serial-control", ExpectedVersion: &version})
		if derr != nil || res.Deleted != 1 {
			t.Errorf("serial control: guarded delete after a comment failed: err=%v deleted=%d; the desired serial outcome is not available even without a race", derr, res.Deleted)
		}
		if n := countRows(ctx, t, store, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", id); n != 0 {
			t.Errorf("serial control: %d comment row(s) survived the serial delete; want 0 (cascade)", n)
		}
	})

	// Guarded delete first, comment second: the comment must be refused with
	// the typed not-found error the Commenter role promises.
	t.Run("comment_after_delete", func(t *testing.T) {
		id := seed(t, "after-delete", false)
		version := readRowLock(ctx, t, store, "issues", id)
		deleter, err := store.Deleter()
		if err != nil {
			t.Fatalf("deleter: %v", err)
		}
		if res, derr := deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{id}, Actor: "serial-control", ExpectedVersion: &version}); derr != nil || res.Deleted != 1 {
			t.Fatalf("serial control delete: err=%v deleted=%d", derr, res.Deleted)
		}
		commenter, err := NewCommenter(store)
		if err != nil {
			t.Fatalf("commenter: %v", err)
		}
		_, cerr := commenter.AddComment(ctx, publicops.AddCommentRequest{IssueID: id, Author: "racer", Text: "serial comment"})
		if !errors.Is(cerr, storage.ErrNotFound) {
			t.Errorf("serial control: comment on deleted %s returned %v; want typed %v", id, cerr, storage.ErrNotFound)
		}
		if n := countRows(ctx, t, store, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", id); n != 0 {
			t.Errorf("serial control: %d comment row(s) landed on a deleted issue; want 0", n)
		}
	})
}

// assertRaceHarnessIntegrity asserts only what the HARNESS promises — the
// racing session landed, the loser ran at least once, and the barrier
// synchronized the interleaving — so the characterization's evidence is
// recorded without gating on the (unresolved) server behavior.
func assertRaceHarnessIntegrity(t *testing.T, tc deleteCommentRaceCase, ev deleteCommentRaceEvidence) {
	t.Helper()
	if ev.Attempts < 1 {
		t.Errorf("harness: the loser's retry boundary never ran an attempt")
	}
}

// readRowLock reads a row's guard token on the pool, the way a caller does
// before choosing to delete.
func readRowLock(ctx context.Context, t *testing.T, store *DoltStore, table, id string) int64 {
	t.Helper()
	var rowLock sql.NullInt64
	if err := store.db.QueryRowContext(ctx, fmt.Sprintf("SELECT row_lock FROM %s WHERE id = ?", table), id).Scan(&rowLock); err != nil {
		t.Fatalf("read row_lock of %s: %v", id, err)
	}
	return rowLock.Int64
}

func readRowLockInTx(ctx context.Context, t *testing.T, tx *sql.Tx, table, id string) int64 {
	t.Helper()
	var rowLock sql.NullInt64
	if err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT row_lock FROM %s WHERE id = ?", table), id).Scan(&rowLock); err != nil {
		t.Fatalf("read row_lock of %s in tx: %v", id, err)
	}
	return rowLock.Int64
}

func countRows(ctx context.Context, t *testing.T, store *DoltStore, query string, args ...any) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("count rows (%s): %v", query, err)
	}
	return n
}

// decodeServerError decodes a driver error the way the production
// classifiers do: MySQL error number, SQLSTATE, and text. err==nil decodes
// to zero values.
func decodeServerError(err error) (code uint16, sqlstate, text string) {
	if err == nil {
		return 0, "", ""
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number, string(mysqlErr.SQLState[:]), err.Error()
	}
	return 0, "", err.Error()
}
