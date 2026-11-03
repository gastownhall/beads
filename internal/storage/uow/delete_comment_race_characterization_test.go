package uow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestUOWDeleteCommentRaceCharacterization is the guarded-delete/comment
// race characterization (bd-ecggzy / PR #8026) on the UNIT-OF-WORK path —
// the plumbing every proxied CLI command and bd serve write reaches. It is
// the uow twin of internal/storage/dolt's
// TestGuardedDeleteCommentRaceCharacterization: the same interleavings, the
// same evidence contract, through the second supported server commit
// path.
//
// THE RACE, in this plumbing's terms. A guarded delete runs deleteInUOW in
// one unit of work whose commit is CALL DOLT_COMMIT('-Am') (or, in batch
// mode, a plain SQL COMMIT); a comment through NewCommenter resolves the
// plane and inserts a comments row in one unit of work and never bumps
// row_lock. Nothing orders the two, so a comment can land between the
// delete's guard read and its commit, or a delete can commit while a
// comment's resolve still sees the parent. The per-id delete fence is not
// part of this race: it orders guarded deletes against each other, and a
// comment never takes it.
//
// THE HARNESS. The loser runs through the REAL retry boundary — RunTxResult
// replays the whole unit of work on a serialization failure — with the
// barrier inside the work function: the first attempt pins its snapshot
// with the caller's row_lock read, signals the racing session, and waits
// for it to finish before running the real operation body
// (deleteInUOW / the commenter body) and returning the real commit message.
// The racing session runs the real public API (NewCommenter().AddComment /
// NewDeleter().Delete) to completion in that window. A REPLAY (attempt 2+)
// re-races nothing: RunTxResult only replays a commit that was refused with
// a guaranteed server-side rollback, so the racing write is already
// committed and visible in the replay's FRESH snapshot — the production
// replay semantics.
//
// MEASUREMENT, NOT FIX. Every executed case records its evidence as one
// structured `race-evidence:` line, so a run of this suite IS the evidence
// report; the outcome assertions live with the reproduction switch. See the
// direct-store twin for the full defect narrative and
// engdocs/design/dolt-concurrency.md, "Guarded delete vs. concurrent
// comment", for the recorded dependency contract.
func TestUOWDeleteCommentRaceCharacterization(t *testing.T) {
	const prefix = "rdcr"
	rootCtx := context.Background()
	provider := newUOWRoleFixtureProvider(t, rootCtx, prefix)
	kit := newUOWRoleFixtureKit(provider, prefix)

	// The journal control goes through the production activation surface:
	// cmd/bd type-asserts the proxied provider to storage.EventsJournalConfigurer.
	configurer, ok := provider.(storage.EventsJournalConfigurer)
	if !ok {
		t.Fatalf("uow provider does not implement storage.EventsJournalConfigurer; the events journal cannot be controlled for the characterization")
	}

	settingsCtx, settingsCancel := context.WithTimeout(rootCtx, 30*time.Second)
	defer settingsCancel()
	settings := recordRaceServerSettings(settingsCtx, t, kit)

	cases := []deleteCommentRaceCase{
		{
			// The proxied delete's default commit: CALL DOLT_COMMIT('-Am') as
			// the transaction's commit statement — this plumbing's in-body
			// Dolt commit.
			name:        "delete_after_comment/in_body_dolt_commit/journal_off",
			loser:       "guarded_delete",
			commitStyle: "in_body_dolt_commit",
			journalOn:   false,
			wisp:        false,
		},
		{
			// Batch mode (GH#4995): the deferred version commit blanks the
			// message, so the same unit of work ends in an ordinary COMMIT.
			name:        "delete_after_comment/plain_commit/journal_off",
			loser:       "guarded_delete",
			commitStyle: "plain_commit",
			journalOn:   false,
			wisp:        false,
		},
		{
			// The comment leg's default commit in this plumbing is also the
			// '-Am' statement; the plain style below is its batch-mode form.
			name:        "comment_after_delete/in_body_dolt_commit/journal_off",
			loser:       "comment",
			commitStyle: "in_body_dolt_commit",
			journalOn:   false,
			wisp:        false,
		},
		{
			name:        "comment_after_delete/plain_commit/journal_off",
			loser:       "comment",
			commitStyle: "plain_commit",
			journalOn:   false,
			wisp:        false,
		},
		{
			// The durable events journal is the one configuration whose
			// write-write conflict serializes the race on its own: the
			// direct-store twin measures it, and this case records whether
			// the uow path inherits the same replay serialization.
			name:        "delete_after_comment/in_body_dolt_commit/journal_on",
			loser:       "guarded_delete",
			commitStyle: "in_body_dolt_commit",
			journalOn:   true,
			wisp:        false,
		},
		{
			// The wisp plane routes the comment to wisp_comments (dolt_ignored),
			// the racing comment mints no Dolt commit at all.
			name:        "delete_after_comment/in_body_dolt_commit/journal_off/wisp",
			loser:       "guarded_delete",
			commitStyle: "in_body_dolt_commit",
			journalOn:   false,
			wisp:        true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// One bounded context PER CASE: RunTxResult's replay budget is
			// its own (15s); this bound only keeps a wedged barrier or a
			// long server-side lock wait from hanging the suite.
			caseCtx, caseCancel := context.WithTimeout(context.WithoutCancel(rootCtx), 2*time.Minute)
			defer caseCancel()
			ev := runUOWDeleteCommentRace(caseCtx, t, provider, configurer, kit, tc)
			ev.log(t, settings, tc)
			assertRaceHarnessIntegrity(t, tc, ev)
		})
	}

	// The serial control: with no overlap the two operations compose the way
	// the desired serial outcome says they must. It runs in both modes and
	// must pass in both.
	t.Run("serial_control_no_overlap", func(t *testing.T) {
		serialCtx, serialCancel := context.WithTimeout(context.WithoutCancel(rootCtx), 2*time.Minute)
		defer serialCancel()
		runUOWSerialControl(serialCtx, t, provider, kit)
	})
}

// deleteCommentRaceCase names one interleaving of the characterization
// matrix. Identical to the direct-store harness's case, so the two evidence
// reports are comparable line by line.
type deleteCommentRaceCase struct {
	// name is the subtest label.
	name string
	// loser is the session whose unit of work pins first and commits second:
	// "guarded_delete" or "comment".
	loser string
	// commitStyle is the loser's commit statement:
	// "in_body_dolt_commit" (CALL DOLT_COMMIT('-Am')) or "plain_commit"
	// (ordinary COMMIT, via a deferred version commit).
	commitStyle string
	// journalOn turns the durable events journal on for BOTH sessions.
	journalOn bool
	// wisp routes the seeded row to the ephemeral plane.
	wisp bool
}

// deleteCommentRaceEvidence is what one executed case observed. Every field
// is recorded into the test log, so a run of the suite IS the evidence
// report.
type deleteCommentRaceEvidence struct {
	// Attempts is how many whole units of work the loser's real retry
	// boundary ran: 1 for the held race, 2+ when the first attempt's commit
	// was refused with a serialization failure and the replay re-pinned a
	// FRESH snapshot that already contained the racing write.
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
	// loser is the comment side), as reported from inside the transaction
	// before the commit was refused.
	LoserDeleted int
	// RacerErrText records the racing session's outcome (it must be empty:
	// the harness requires the racer to land before the loser is released).
	RacerErrText string
	// Final state, read on a FRESH unit of work after both sessions
	// finished: the parent row's presence, the child comment rows that
	// survived, and how many of those reference a parent that is gone.
	IssueExists  bool
	CommentRows  int
	DanglingRows int
}

func (ev deleteCommentRaceEvidence) loserFailed() bool {
	return ev.LoserErrText != ""
}

// log writes the case's evidence as one structured line, the form the
// checked-in concurrency documentation quotes.
func (ev deleteCommentRaceEvidence) log(t *testing.T, settings raceServerSettings, tc deleteCommentRaceCase) {
	t.Helper()
	t.Logf("race-evidence: case=%s path=uow loser=%s commit_style=%s journal=%s plane=%s server=%s fk_checks=%s isolation=%s attempts=%d loser_error=%q loser_error_code=%d loser_error_sqlstate=%s loser_typed(not_found/version_mismatch/serialization)=%v/%v/%v loser_deleted=%d racer_error=%q final: parent_exists=%v comment_rows=%d dangling_comment_rows=%d",
		tc.name, tc.loser, tc.commitStyle, settings.journalWord(tc.journalOn), settings.planeWord(tc.wisp),
		settings.Artifact, settings.FKChecks, settings.Isolation,
		ev.Attempts, ev.LoserErrText, ev.LoserErrCode, ev.LoserSQLState,
		ev.LoserErrIsNotFound, ev.LoserErrIsVersionMismatch, ev.LoserErrIsSerialization,
		ev.LoserDeleted, ev.RacerErrText, ev.IssueExists, ev.CommentRows, ev.DanglingRows)
}

// raceServerSettings is the environment part of the evidence: which server
// artifact the characterization ran against and the session settings that
// decide the race's semantics.
type raceServerSettings struct {
	// Artifact names the pinned Dolt server: the local server's own
	// reported version string.
	Artifact string
	FKChecks string
	// Isolation is what @@transaction_isolation answered.
	Isolation string
}

func (s raceServerSettings) journalWord(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func (s raceServerSettings) planeWord(wisp bool) string {
	if wisp {
		return "wisp"
	}
	return "issue"
}

// recordRaceServerSettings verifies the settings the design pins —
// constraints on, a migrated scratch schema — before any case runs, so a run
// against a server configured differently cannot produce evidence that looks
// like this race's.
func recordRaceServerSettings(ctx context.Context, t *testing.T, kit roleFixtureKit) raceServerSettings {
	t.Helper()

	var fkChecks, isolation, version string
	if err := kit.QueryScalar(ctx, "SELECT @@foreign_key_checks, @@transaction_isolation, VERSION()", nil, &fkChecks, &isolation, &version); err != nil {
		t.Fatalf("read server settings: %v", err)
	}
	// Constraints must be ON: the whole disagreement the harness settles is
	// what the FK cascade does at commit-merge time.
	if fkChecks != "1" {
		t.Fatalf("unsupported configuration: @@foreign_key_checks = %q, want 1", fkChecks)
	}
	return raceServerSettings{
		Artifact:  "local dolt sql-server (VERSION()=" + version + ")",
		FKChecks:  fkChecks,
		Isolation: isolation,
	}
}

// runUOWDeleteCommentRace executes one case of the matrix and returns the
// observed evidence. It fails the test only when the harness itself is
// broken (the racing session did not land, or a barrier was missed) — never
// for the outcome under characterization.
func runUOWDeleteCommentRace(
	ctx context.Context,
	t *testing.T,
	provider UnitOfWorkProvider,
	configurer storage.EventsJournalConfigurer,
	kit roleFixtureKit,
	tc deleteCommentRaceCase,
) deleteCommentRaceEvidence {
	t.Helper()

	configurer.SetEventsJournalEnabled(tc.journalOn)
	defer configurer.SetEventsJournalEnabled(false)

	// The loser's commit style rides the context exactly as production
	// batch mode does: WithDeferredVersionCommit is what blanks the commit
	// message inside commitStatement, turning the unit of work's
	// DOLT_COMMIT('-Am') into an ordinary COMMIT.
	caseCtx := ctx
	if tc.commitStyle == "plain_commit" {
		caseCtx = storageissueops.WithDeferredVersionCommit(ctx)
	}

	id := "rdcr-" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(tc.name, "/", "-"), "_", "-"))
	seed := kit.CreateIssue
	if tc.wisp {
		seed = kit.CreateWisp
	}
	if err := seed(caseCtx, &types.Issue{
		ID: id, Title: "race characterization seed", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, Ephemeral: tc.wisp,
	}, "seeder"); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	parentTable, commentTable := "issues", "comments"
	if tc.wisp {
		parentTable, commentTable = "wisps", "wisp_comments"
	}

	// The racing delete (comment-loser cases) needs its guard token read on
	// a fresh unit of work before the loser pins anything.
	racerDeleteVersion := raceReadRowLock(caseCtx, t, kit, parentTable, id)

	pinned := make(chan struct{})
	racerDone := make(chan error, 1)

	ev := deleteCommentRaceEvidence{}
	// THE LOSER, through the real retry boundary: RunTxResult replays the
	// whole unit of work on a serialization failure, on a FRESH unit of
	// work with a fresh snapshot — the production replay semantics.
	loserDone := make(chan error, 1)
	go func() {
		_, err := RunTxResult(caseCtx, provider, func(ctx context.Context, uw UnitOfWork) (publicops.DeleteResult, string, error) {
			// Plain assignment on every attempt, the retried closure
			// contract: a rolled-back attempt's result must not leak into
			// the evidence below.
			ev.Attempts++
			ev.LoserErrCode, ev.LoserSQLState, ev.LoserErrText = 0, "", ""

			// THE TOKEN READ IS THE SNAPSHOT PIN. It is the caller's read of
			// the row_lock token, and under the server's transaction
			// isolation it fixes this attempt's snapshot, so every later
			// read in this unit of work (the existence probe, the guard,
			// the insert) sees the pre-race state.
			version := raceReadRowLockInUOW(ctx, t, uw, parentTable, id)
			if ev.Attempts == 1 {
				close(pinned)
				select {
				case rerr := <-racerDone:
					ev.RacerErrText = fmt.Sprint(rerr)
				case <-caseCtx.Done():
					return publicops.DeleteResult{}, "", caseCtx.Err()
				}
			}
			// A REPLAY (attempt 2+) re-races NOTHING: RunTxResult only
			// replays a commit that was refused with a serialization
			// failure, which guarantees a server-side rollback, so by the
			// time this attempt runs the racing write — which finished
			// before the refused commit — is already committed and visible
			// in this attempt's FRESH snapshot.

			switch tc.loser {
			case "guarded_delete":
				token := version
				res, err := deleteInUOW(ctx, uw, publicops.DeleteRequest{
					IDs:             []string{id},
					Actor:           "race-characterization",
					ExpectedVersion: &token,
				})
				if err != nil {
					return res, "", err
				}
				ev.LoserDeleted = res.Deleted
				if res.Deleted == 0 {
					return res, "", nil
				}
				// The deleter's production commit message, verbatim from
				// deleter.go's write closure.
				return res, fmt.Sprintf("bd: delete %d issue(s)", res.Deleted), nil

			case "comment":
				// The commenter's production body, verbatim from
				// commenter.go's AddComment work function.
				issue, isWisp, err := workapi.GetIssueOrWisp(ctx, workapi.NewUOWDetailSource(uw), id)
				if err != nil {
					return publicops.DeleteResult{}, "", err
				}
				if isWisp {
					_, err = uw.CommentUseCase().AddCommentToWisp(ctx, issue.ID, "race-characterization", "loser comment")
				} else {
					_, err = uw.CommentUseCase().AddCommentToIssue(ctx, issue.ID, "race-characterization", "loser comment")
				}
				if err != nil {
					return publicops.DeleteResult{}, "", err
				}
				return publicops.DeleteResult{}, storageissueops.AddCommentCommitMessage(issue.ID), nil

			default:
				return publicops.DeleteResult{}, "", fmt.Errorf("unknown loser %q", tc.loser)
			}
		})
		loserDone <- err
	}()

	// THE RACING SESSION, real public API to completion inside the window,
	// on the UNDEFERRED context: the style under test is the loser's commit
	// statement, and the racing session is the ordinary production session
	// every non-batch caller is.
	var racerErr error
	<-pinned
	racerStart := time.Now()
	switch tc.loser {
	case "guarded_delete":
		commenter, err := NewCommenter(provider)
		if err != nil {
			t.Fatalf("racing commenter: %v", err)
		}
		_, racerErr = commenter.AddComment(ctx, publicops.AddCommentRequest{
			IssueID: id,
			Author:  "racer",
			Text:    "racing comment",
		})
	case "comment":
		deleter, err := NewDeleter(provider)
		if err != nil {
			t.Fatalf("racing deleter: %v", err)
		}
		_, racerErr = deleter.Delete(ctx, publicops.DeleteRequest{
			IDs:             []string{id},
			Actor:           "racer",
			ExpectedVersion: &racerDeleteVersion,
		})
	}
	racerDone <- racerErr
	racerElapsed := time.Since(racerStart)

	loserErr := <-loserDone
	t.Logf("race-timing: case=%s racer_elapsed=%s loser_total_elapsed=%s attempts=%d", tc.name, racerElapsed, time.Since(racerStart), ev.Attempts)
	if racerErr != nil {
		// The harness requires the racing session to land: a racer failure is
		// a broken harness or an unsupportable configuration, not evidence.
		t.Fatalf("racing session did not land (%s side): %v", tc.loser, racerErr)
	}

	code, sqlstate, text := raceDecodeServerError(loserErr)
	ev.LoserErrCode, ev.LoserSQLState, ev.LoserErrText = code, sqlstate, text
	ev.LoserErrIsNotFound = errors.Is(loserErr, storage.ErrNotFound)
	ev.LoserErrIsVersionMismatch = errors.Is(loserErr, storage.ErrVersionMismatch)
	ev.LoserErrIsSerialization = isSerializationError(loserErr) || isDoltAutocommitRollbackError(loserErr)

	// FINAL STATE, read on a fresh unit of work: the parent row's presence,
	// the child comment rows that survived, and any child whose parent is
	// gone (the orphan the bug report and the fix candidates disagree about).
	count := func(query string, args ...any) int {
		var n int
		if err := kit.QueryScalar(ctx, query, args, &n); err != nil {
			t.Fatalf("read final state (%s): %v", query, err)
		}
		return n
	}
	ev.IssueExists = count(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE id = ?", parentTable), id) == 1
	ev.CommentRows = count(fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE issue_id = ?", commentTable), id)
	ev.DanglingRows = count(fmt.Sprintf(
		"SELECT COUNT(*) FROM %s c LEFT JOIN %s p ON p.id = c.issue_id WHERE c.issue_id = ? AND p.id IS NULL",
		commentTable, parentTable), id)

	return ev
}

// runUOWSerialControl runs the two operations back to back with no overlap
// and asserts they compose exactly as the desired serial outcome says they
// must. It is the non-race control for both assertion modes.
func runUOWSerialControl(ctx context.Context, t *testing.T, provider UnitOfWorkProvider, kit roleFixtureKit) {
	t.Helper()

	// Comment first, guarded delete second: the delete must succeed, take the
	// comment with it (the cascade), and leave no orphan.
	t.Run("delete_after_comment", func(t *testing.T) {
		id := "rdcr-serial-after-comment"
		if err := kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "serial control", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seeder"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		commenter, err := NewCommenter(provider)
		if err != nil {
			t.Fatalf("commenter: %v", err)
		}
		if _, err := commenter.AddComment(ctx, publicops.AddCommentRequest{IssueID: id, Author: "racer", Text: "serial comment"}); err != nil {
			t.Fatalf("serial comment: %v", err)
		}
		version := raceReadRowLock(ctx, t, kit, "issues", id)
		deleter, err := NewDeleter(provider)
		if err != nil {
			t.Fatalf("deleter: %v", err)
		}
		res, derr := deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{id}, Actor: "serial-control", ExpectedVersion: &version})
		if derr != nil || res.Deleted != 1 {
			t.Errorf("serial control: guarded delete after a comment failed: err=%v deleted=%d; the desired serial outcome is not available even without a race", derr, res.Deleted)
		}
		if n := raceCountRows(ctx, t, kit, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", id); n != 0 {
			t.Errorf("serial control: %d comment row(s) survived the serial delete; want 0 (cascade)", n)
		}
	})

	// Guarded delete first, comment second: the comment must be refused with
	// the typed not-found error the Commenter role promises.
	t.Run("comment_after_delete", func(t *testing.T) {
		id := "rdcr-serial-after-delete"
		if err := kit.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "serial control", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}, "seeder"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		version := raceReadRowLock(ctx, t, kit, "issues", id)
		deleter, err := NewDeleter(provider)
		if err != nil {
			t.Fatalf("deleter: %v", err)
		}
		if res, derr := deleter.Delete(ctx, publicops.DeleteRequest{IDs: []string{id}, Actor: "serial-control", ExpectedVersion: &version}); derr != nil || res.Deleted != 1 {
			t.Fatalf("serial control delete: err=%v deleted=%d", derr, res.Deleted)
		}
		commenter, err := NewCommenter(provider)
		if err != nil {
			t.Fatalf("commenter: %v", err)
		}
		_, cerr := commenter.AddComment(ctx, publicops.AddCommentRequest{IssueID: id, Author: "racer", Text: "serial comment"})
		if !errors.Is(cerr, storage.ErrNotFound) {
			t.Errorf("serial control: comment on deleted %s returned %v; want typed %v", id, cerr, storage.ErrNotFound)
		}
		if n := raceCountRows(ctx, t, kit, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", id); n != 0 {
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

// raceReadRowLock reads a row's guard token on a fresh read-only unit of
// work, the way a caller does before choosing to delete.
func raceReadRowLock(ctx context.Context, t *testing.T, kit roleFixtureKit, table, id string) int64 {
	t.Helper()
	var rowLock int64
	if err := kit.QueryScalar(ctx, fmt.Sprintf("SELECT row_lock FROM %s WHERE id = ?", table), []any{id}, &rowLock); err != nil {
		t.Fatalf("read row_lock of %s: %v", id, err)
	}
	return rowLock
}

// raceReadRowLockInUOW is raceReadRowLock inside the loser's pinned unit of
// work: the read that fixes the attempt's snapshot.
func raceReadRowLockInUOW(ctx context.Context, t *testing.T, uw UnitOfWork, table, id string) int64 {
	t.Helper()
	result, err := uw.RawSQLUseCase().Query(ctx, fmt.Sprintf("SELECT row_lock FROM %s WHERE id = ?", table), id)
	if err != nil {
		t.Fatalf("read row_lock of %s in uow: %v", id, err)
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
		t.Fatalf("read row_lock of %s in uow: %d row(s), want 1", id, len(result.Rows))
	}
	var rowLock int64
	if err := scanRawSQLValue(&rowLock, result.Rows[0][0]); err != nil {
		t.Fatalf("scan row_lock of %s: %v", id, err)
	}
	return rowLock
}

func raceCountRows(ctx context.Context, t *testing.T, kit roleFixtureKit, query string, args ...any) int {
	t.Helper()
	var n int
	if err := kit.QueryScalar(ctx, query, args, &n); err != nil {
		t.Fatalf("count rows (%s): %v", query, err)
	}
	return n
}

// raceDecodeServerError decodes a driver error the way the production
// classifiers do: MySQL error number, SQLSTATE, and text. err==nil decodes
// to zero values.
func raceDecodeServerError(err error) (code uint16, sqlstate, text string) {
	if err == nil {
		return 0, "", ""
	}
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number, string(mysqlErr.SQLState[:]), err.Error()
	}
	return 0, "", err.Error()
}
