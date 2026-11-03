package dolt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/testutil/sqlgate"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// raceSettingsProbe is what the gate reads on each racing session at its
// commit point: the settings the characterization depends on, read from the
// session that actually commits rather than from a sibling connection.
const raceSettingsProbe = "SELECT @@foreign_key_checks AS foreign_key_checks, " +
	"@@dolt_force_transaction_commit AS dolt_force_transaction_commit, " +
	"@@autocommit AS autocommit, " +
	"@@transaction_isolation AS transaction_isolation"

// raceOp is one side of a delete/comment race.
type raceOp struct {
	Op         string            `json:"op"`
	CommitKind string            `json:"commit_kind"`
	Settings   map[string]string `json:"settings"`
	Attempts   int               `json:"attempts"`
	Err        string            `json:"err,omitempty"`
	MySQLCode  uint16            `json:"mysql_code,omitempty"`
	SQLState   string            `json:"sqlstate,omitempty"`
	NotFound   bool              `json:"errors_is_not_found"`
	Deleted    int               `json:"deleted,omitempty"`

	err error
}

// raceState is the parent/child state after the race, read on fresh
// transactions with no label, so the gate never holds them.
type raceState struct {
	ParentPresent        bool `json:"parent_present"`
	Comments             int  `json:"comments"`
	LoserCommentPresent  bool `json:"loser_comment_present"`
	WinnerCommentPresent bool `json:"winner_comment_present"`
	OrphanComments       int  `json:"orphan_comments"`
	ConstraintViolations int  `json:"constraint_violations"`
}

// raceEvidence is one executed case's record. It is logged as one JSON line
// so a run's output is the characterization evidence.
type raceEvidence struct {
	Path     string    `json:"path"`
	Case     string    `json:"case"`
	Server   string    `json:"server"`
	Backend  string    `json:"backend"`
	Journal  bool      `json:"journal"`
	Loser    raceOp    `json:"loser"`
	Winner   raceOp    `json:"winner"`
	RolledBk bool      `json:"loser_rolled_back"`
	State    raceState `json:"final_state"`
}

func (e raceEvidence) log(t *testing.T) {
	t.Helper()
	line, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal race evidence: %v", err)
	}
	t.Logf("delete-comment race evidence: %s", line)
}

// decodeRaceError fills op's error fields from err.
func decodeRaceError(op *raceOp, err error) {
	op.err = err
	if err == nil {
		return
	}
	op.Err = err.Error()
	op.NotFound = errors.Is(err, storage.ErrNotFound)
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		op.MySQLCode = me.Number
		op.SQLState = strings.TrimRight(string(me.SQLState[:]), "\x00")
	}
}

// requireRaceSessionSettings fails unless op ran on a session with the
// settings the characterization assumes: constraints on, force-commit off.
// An op that never reached a commit point (a serial comment refused as not
// found) has no committing session to check.
func requireRaceSessionSettings(t *testing.T, who string, op raceOp) {
	t.Helper()
	if op.CommitKind == "" && op.err != nil {
		return
	}
	if op.Settings["foreign_key_checks"] != "1" {
		t.Fatalf("%s session foreign_key_checks = %q, want 1 (constraints enabled)", who, op.Settings["foreign_key_checks"])
	}
	if op.Settings["dolt_force_transaction_commit"] != "0" {
		t.Fatalf("%s session dolt_force_transaction_commit = %q, want 0", who, op.Settings["dolt_force_transaction_commit"])
	}
}

// raceGatedStore points store at a gate over its own DSN and returns the
// gate. The store keeps its real driver, DSN, retry and fence code; only the
// connection pool is the gate's. The pool is wider than
// setupConcurrentTestStore's two because a guarded delete pins one session for
// its fence while the racing comment needs another.
func raceGatedStore(t *testing.T, store *DoltStore) *sqlgate.Connector {
	t.Helper()
	gate := sqlgate.NewConnector(store.db.Driver(), store.connStr)
	gate.ProbeQuery = raceSettingsProbe
	gated := sql.OpenDB(gate)
	gated.SetMaxOpenConns(6)
	original := store.db
	store.db = gated
	t.Cleanup(func() {
		store.db = original
		_ = gated.Close()
	})
	return gate
}

func raceDoltVersion(ctx context.Context, t *testing.T, db *sql.DB) string {
	t.Helper()
	var version string
	if err := db.QueryRowContext(ctx, "SELECT dolt_version()").Scan(&version); err != nil {
		t.Fatalf("read dolt_version(): %v", err)
	}
	return "dolt " + version
}

func raceBackend() string {
	if b := os.Getenv("BEADS_TEST_DOLT_SERVER"); b != "" {
		return b
	}
	return "default"
}

// readRaceState reads the final state for issueID through the store's pool.
func readRaceState(ctx context.Context, t *testing.T, db *sql.DB, issueID, loserText, winnerText string) raceState {
	t.Helper()
	var st raceState
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", issueID).Scan(&n); err != nil {
		t.Fatalf("read parent: %v", err)
	}
	st.ParentPresent = n == 1
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", issueID).Scan(&st.Comments); err != nil {
		t.Fatalf("read comments: %v", err)
	}
	for text, present := range map[string]*bool{loserText: &st.LoserCommentPresent, winnerText: &st.WinnerCommentPresent} {
		if text == "" {
			continue
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM comments WHERE issue_id = ? AND text = ?", issueID, text).Scan(&n); err != nil {
			t.Fatalf("read comment %q: %v", text, err)
		}
		*present = n > 0
	}
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM comments c LEFT JOIN issues i ON i.id = c.issue_id WHERE i.id IS NULL").Scan(&st.OrphanComments); err != nil {
		t.Fatalf("count orphan comments: %v", err)
	}
	// Engine-side constraint validation, test-only: production never reads
	// this table.
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_constraint_violations").Scan(&st.ConstraintViolations); err != nil {
		t.Fatalf("read dolt_constraint_violations: %v", err)
	}
	return st
}

const (
	raceDeleteLoses  = "delete-loses"
	raceCommentLoses = "comment-loses"
)

// raceCase is one executed race: which side is held, the commit style, and
// whether the events journal is on.
type raceCase struct {
	name     string
	loser    string
	deferred bool
	journal  bool
	serial   bool
}

// raceCases is both orders under both supported commit styles with the
// journal off, plus two controls in the default commit style: both orders
// with the journal on (there the loser is retried and ends with the serial
// outcome), and both orders run one after the other with no overlap (the
// serial outcome by construction).
//
// The styles: by default a guarded write mints its version commit with
// DOLT_COMMIT inside its transaction ("in-body-dolt-commit"); under
// dolt.auto-commit batch/off it commits with a plain SQL COMMIT and the
// version commit is deferred ("sql-commit").
func raceCases() []raceCase {
	var cases []raceCase
	for _, style := range []struct {
		name     string
		deferred bool
	}{{name: "in-body-dolt-commit"}, {name: "sql-commit", deferred: true}} {
		for _, loser := range []string{raceDeleteLoses, raceCommentLoses} {
			cases = append(cases, raceCase{name: loser + "/" + style.name, loser: loser, deferred: style.deferred})
		}
	}
	for _, loser := range []string{raceDeleteLoses, raceCommentLoses} {
		cases = append(cases, raceCase{name: "journal-on-control/" + loser, loser: loser, journal: true})
	}
	for _, loser := range []string{raceDeleteLoses, raceCommentLoses} {
		cases = append(cases, raceCase{name: "serial-control/" + loser, loser: loser, serial: true})
	}
	return cases
}

// raceLoserRolledBack reports, from application reads, whether the held
// operation's write is absent afterwards.
func raceLoserRolledBack(loser string, st raceState) bool {
	if loser == raceDeleteLoses {
		return st.ParentPresent
	}
	return !st.LoserCommentPresent
}

// requireRaceIntegrity fails on a partial or orphaned outcome, whatever the
// loser's error was: no comment without its issue, no violation left behind.
func requireRaceIntegrity(t *testing.T, ev raceEvidence) {
	t.Helper()
	if ev.State.OrphanComments != 0 {
		t.Errorf("%d orphan comment(s) after the race", ev.State.OrphanComments)
	}
	if ev.State.ConstraintViolations != 0 {
		t.Errorf("%d dolt_constraint_violations row(s) after the race", ev.State.ConstraintViolations)
	}
	if ev.Loser.err == nil && !ev.RolledBk {
		return
	}
	if ev.Loser.err != nil && !ev.RolledBk {
		t.Errorf("loser failed (%v) but its write is visible: an ambiguous outcome", ev.Loser.err)
	}
	if ev.Winner.err != nil {
		t.Errorf("winner failed: %v", ev.Winner.err)
	}
}

// TestGuardedDeleteCommentRaceCharacterization races the direct store's
// guarded Deleter against its Commenter on one issue, on a real Dolt
// sql-server, with the events journal off (plus a journal-on control).
//
// The loser's transaction is held at its commit point (it has read and
// written everything), the winner runs to completion, and the loser is then
// released. Both orders run, under both supported commit styles. The direct
// store's Commenter commits with a plain SQL COMMIT and mints its version
// commit after it, whatever the style, so "comment loses under an in-body
// DOLT_COMMIT" is not executable on this path: the case still runs, and its
// evidence records the commit kind the commenter really used.
//
// Every case logs one evidence line, then checks the session settings, the
// commit kinds and that no orphan or ambiguous state is left.
func TestGuardedDeleteCommentRaceCharacterization(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	gate := raceGatedStore(t, store)

	ctx, cancel := testContext(t)
	defer cancel()
	server := raceDoltVersion(ctx, t, store.db)

	deleter, err := store.Deleter()
	if err != nil {
		t.Fatalf("Deleter: %v", err)
	}
	commenter, err := store.Commenter()
	if err != nil {
		t.Fatalf("Commenter: %v", err)
	}

	seed := func(t *testing.T, id string) int64 {
		t.Helper()
		issue := &types.Issue{ID: id, Title: "race " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, issue, "seeder"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		got, err := store.GetIssue(ctx, id)
		if err != nil {
			t.Fatalf("read seeded %s: %v", id, err)
		}
		return got.RowVersion
	}
	deleteOp := func(id string, version int64) func(context.Context) raceOp {
		return func(ctx context.Context) raceOp {
			res, err := deleter.Delete(ctx, publicops.DeleteRequest{Actor: "racer", IDs: []string{id}, ExpectedVersion: &version})
			op := raceOp{Op: "guarded-delete", Deleted: res.Deleted}
			decodeRaceError(&op, err)
			return op
		}
	}
	commentOp := func(id, text string) func(context.Context) raceOp {
		return func(ctx context.Context) raceOp {
			_, err := commenter.AddComment(ctx, publicops.AddCommentRequest{Author: "racer", IssueID: id, Text: text})
			op := raceOp{Op: "add-comment"}
			decodeRaceError(&op, err)
			return op
		}
	}

	var executed []string
	for _, rc := range raceCases() {
		t.Run(rc.name, func(t *testing.T) {
			store.SetEventsJournalEnabled(rc.journal)
			defer store.SetEventsJournalEnabled(false)
			id := "race-" + strings.ReplaceAll(rc.name, "/", "-")
			version := seed(t, id)
			loserText, winnerText := "", ""
			var loserFn, winnerFn func(context.Context) raceOp
			if rc.loser == raceDeleteLoses {
				winnerText = "winner comment"
				loserFn, winnerFn = deleteOp(id, version), commentOp(id, winnerText)
			} else {
				loserText = "loser comment"
				loserFn, winnerFn = commentOp(id, loserText), deleteOp(id, version)
			}
			base := ctx
			if rc.deferred {
				base = storeops.WithDeferredVersionCommit(base)
			}
			loserLabel, winnerLabel := t.Name()+"/loser", t.Name()+"/winner"
			var lres, wres raceOp
			if rc.serial {
				lres, wres = runSerialRace(base, t, gate, loserLabel, winnerLabel, loserFn, winnerFn)
			} else {
				lres, wres = runHeldRace(base, t, gate, loserLabel, winnerLabel, loserFn, winnerFn)
			}

			ev := raceEvidence{
				Path: "dolt-store", Case: rc.name, Server: server, Backend: raceBackend(), Journal: rc.journal,
				Loser: lres, Winner: wres,
				State: readRaceState(ctx, t, store.db, id, loserText, winnerText),
			}
			ev.RolledBk = raceLoserRolledBack(rc.loser, ev.State)
			ev.log(t)
			executed = append(executed, rc.name)

			requireRaceSessionSettings(t, "loser", ev.Loser)
			requireRaceSessionSettings(t, "winner", ev.Winner)
			requireRaceIntegrity(t, ev)
			if !rc.serial {
				requireRaceCommitKinds(t, rc, ev)
			}
		})
	}
	t.Logf("delete-comment race coverage (dolt-store): executed %v; not executable on this path: "+
		"comment loser with an in-body DOLT_COMMIT (the Commenter always SQL-COMMITs first); "+
		"not covered by this item: labels, dependencies, the wisp plane, cancellation of a held loser, "+
		"persisted (non-sequencing) constraint violations", executed)
}

// runHeldRace runs loser until the gate holds it at its commit point, runs
// winner to completion, then releases loser and returns both results with the
// gate's per-label evidence.
func runHeldRace(ctx context.Context, t *testing.T, gate *sqlgate.Connector, loserLabel, winnerLabel string,
	loser, winner func(context.Context) raceOp) (raceOp, raceOp) {
	t.Helper()
	hold := gate.Arm(loserLabel)
	defer hold.Release()
	done := make(chan raceOp, 1)
	go func() { done <- loser(sqlgate.WithLabel(ctx, loserLabel)) }()
	select {
	case <-hold.Reached():
	case res := <-done:
		t.Fatalf("loser finished before reaching its commit point: %+v", res)
	case <-time.After(30 * time.Second):
		t.Fatal("loser never reached its commit point")
	}
	wres := winner(sqlgate.WithLabel(ctx, winnerLabel))
	hold.Release()
	var lres raceOp
	select {
	case lres = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("loser did not finish after release")
	}
	fillRaceOp(t, gate, &lres, loserLabel)
	fillRaceOp(t, gate, &wres, winnerLabel)
	return lres, wres
}

// runSerialRace runs winner to completion, then loser, with no hold: the
// non-overlap control.
func runSerialRace(ctx context.Context, t *testing.T, gate *sqlgate.Connector, loserLabel, winnerLabel string,
	loser, winner func(context.Context) raceOp) (raceOp, raceOp) {
	t.Helper()
	wres := winner(sqlgate.WithLabel(ctx, winnerLabel))
	lres := loser(sqlgate.WithLabel(ctx, loserLabel))
	fillRaceOp(t, gate, &lres, loserLabel)
	fillRaceOp(t, gate, &wres, winnerLabel)
	return lres, wres
}

// fillRaceOp copies the gate's per-label evidence into op.
func fillRaceOp(t *testing.T, gate *sqlgate.Connector, op *raceOp, label string) {
	t.Helper()
	op.CommitKind = gate.CommitKind(label)
	op.Attempts = gate.Attempts(label)
	settings, err := gate.Settings(label)
	if err != nil {
		t.Fatalf("%s settings: %v", label, err)
	}
	op.Settings = settings
}

// requireRaceCommitKinds fails unless each side committed the way its case
// says: a case named for an in-body DOLT_COMMIT that really SQL-COMMITted
// would be evidence for the wrong path. The direct store's Commenter always
// commits with Tx.Commit first.
func requireRaceCommitKinds(t *testing.T, rc raceCase, ev raceEvidence) {
	t.Helper()
	wantDelete := "CALL DOLT_ADD"
	if rc.deferred {
		wantDelete = "Tx.Commit"
	}
	del, com := ev.Loser, ev.Winner
	if rc.loser == raceCommentLoses {
		del, com = ev.Winner, ev.Loser
	}
	if del.CommitKind != wantDelete {
		t.Errorf("delete commit kind = %q, want %q", del.CommitKind, wantDelete)
	}
	if com.CommitKind != "Tx.Commit" {
		t.Errorf("comment commit kind = %q, want Tx.Commit", com.CommitKind)
	}
}
