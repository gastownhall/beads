package uow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/testutil/sqlgate"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// expectSerialDeleteCommentEnv is internal/storage/dolt's reproduction
// switch, read here too: with it set to 1 the journal-off cases assert the
// serial outcome and fail on the characterized server. See
// engdocs/design/dolt-concurrency.md, "Guarded delete racing a comment".
const expectSerialDeleteCommentEnv = "BEADS_TEST_EXPECT_SERIAL_DELETE_COMMENT"

// uowRaceSettingsProbe is what the gate reads on each racing session at its
// commit point.
const uowRaceSettingsProbe = "SELECT @@foreign_key_checks AS foreign_key_checks, " +
	"@@dolt_force_transaction_commit AS dolt_force_transaction_commit, " +
	"@@autocommit AS autocommit, " +
	"@@transaction_isolation AS transaction_isolation"

// uowRaceOp is one side of a delete/comment race; the JSON shape matches
// internal/storage/dolt's so the two paths' evidence lines compare directly.
type uowRaceOp struct {
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

type uowRaceState struct {
	ParentPresent        bool `json:"parent_present"`
	Comments             int  `json:"comments"`
	LoserCommentPresent  bool `json:"loser_comment_present"`
	WinnerCommentPresent bool `json:"winner_comment_present"`
	OrphanComments       int  `json:"orphan_comments"`
	ConstraintViolations int  `json:"constraint_violations"`
}

type uowRaceEvidence struct {
	Path     string       `json:"path"`
	Case     string       `json:"case"`
	Server   string       `json:"server"`
	Backend  string       `json:"backend"`
	Journal  bool         `json:"journal"`
	Loser    uowRaceOp    `json:"loser"`
	Winner   uowRaceOp    `json:"winner"`
	RolledBk bool         `json:"loser_rolled_back"`
	State    uowRaceState `json:"final_state"`
}

const (
	uowRaceDeleteLoses  = "delete-loses"
	uowRaceCommentLoses = "comment-loses"
)

type uowRaceCase struct {
	name     string
	loser    string
	deferred bool
	journal  bool
	serial   bool
}

// uowRaceCases is both orders under both commit styles with the journal off,
// plus two controls in the default style: both orders with the journal on,
// and both orders run one after the other with no overlap. Unlike
// the direct store, both uow roles commit in-body by default
// (CALL DOLT_COMMIT('-Am')), so every order × style cell is executable here.
func uowRaceCases() []uowRaceCase {
	var cases []uowRaceCase
	for _, style := range []struct {
		name     string
		deferred bool
	}{{name: "in-body-dolt-commit"}, {name: "sql-commit", deferred: true}} {
		for _, loser := range []string{uowRaceDeleteLoses, uowRaceCommentLoses} {
			cases = append(cases, uowRaceCase{name: loser + "/" + style.name, loser: loser, deferred: style.deferred})
		}
	}
	for _, loser := range []string{uowRaceDeleteLoses, uowRaceCommentLoses} {
		cases = append(cases, uowRaceCase{name: "journal-on-control/" + loser, loser: loser, journal: true})
	}
	for _, loser := range []string{uowRaceDeleteLoses, uowRaceCommentLoses} {
		cases = append(cases, uowRaceCase{name: "serial-control/" + loser, loser: loser, serial: true})
	}
	return cases
}

// TestUOWDeleteCommentRaceCharacterization is
// internal/storage/dolt's TestGuardedDeleteCommentRaceCharacterization for
// the unit-of-work path (proxied mode, bd serve): the guarded Deleter under
// its per-id delete fence races the Commenter on one issue, on a real Dolt
// sql-server. The loser is held at its commit point while the winner runs to
// completion, then released.
func TestUOWDeleteCommentRaceCharacterization(t *testing.T) {
	server := testutil.StartIsolatedDoltContainerHandle(t)
	port, err := strconv.Atoi(server.Port)
	if err != nil {
		t.Fatal(err)
	}
	ep := proxy.Endpoint{Host: "127.0.0.1", Port: port}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const database = "delete_comment_race"
	opened, err := openAndInitSchema(ctx, "tcp", ep, "", database, "root", "", "", false, "", providerOptions{})
	if err != nil {
		t.Fatalf("openAndInitSchema: %v", err)
	}
	t.Cleanup(func() { _ = opened.Close(context.Background()) })
	provider := opened.(*doltSQLProvider)

	dsn := buildDSN("tcp", ep, database, "root", "", "")
	gate := sqlgate.NewConnector(provider.db.Driver(), dsn)
	gate.ProbeQuery = uowRaceSettingsProbe
	original := provider.db
	provider.db = sql.OpenDB(gate)
	_ = original.Close()

	reader, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	var serverVersion string
	if err := reader.QueryRowContext(ctx, "SELECT dolt_version()").Scan(&serverVersion); err != nil {
		t.Fatalf("read dolt_version(): %v", err)
	}
	backend := os.Getenv("BEADS_TEST_DOLT_SERVER")
	if backend == "" {
		backend = "default"
	}

	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "bd: set issue prefix", uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", "race")
	}); err != nil {
		t.Fatalf("set issue_prefix: %v", err)
	}

	deleter, err := provider.Deleter()
	if err != nil {
		t.Fatalf("Deleter: %v", err)
	}
	commenter, err := provider.Commenter()
	if err != nil {
		t.Fatalf("Commenter: %v", err)
	}

	seed := func(t *testing.T, id string) int64 {
		t.Helper()
		issue := &types.Issue{ID: id, Title: "race " + id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: issue, ExplicitID: id, CreateOnly: true}, "seeder")
			return "seed " + id, err
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		rows, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) ([]*types.Issue, error) {
			return uw.IssueUseCase().GetIssuesByIDs(ctx, []string{id})
		})
		if err != nil || len(rows) != 1 {
			t.Fatalf("read seeded %s: %d rows, %v", id, len(rows), err)
		}
		return rows[0].RowVersion
	}
	deleteOp := func(id string, version int64) func(context.Context) uowRaceOp {
		return func(ctx context.Context) uowRaceOp {
			res, err := deleter.Delete(ctx, publicops.DeleteRequest{Actor: "racer", IDs: []string{id}, ExpectedVersion: &version})
			op := uowRaceOp{Op: "guarded-delete", Deleted: res.Deleted}
			decodeUOWRaceError(&op, err)
			return op
		}
	}
	commentOp := func(id, text string) func(context.Context) uowRaceOp {
		return func(ctx context.Context) uowRaceOp {
			_, err := commenter.AddComment(ctx, publicops.AddCommentRequest{Author: "racer", IssueID: id, Text: text})
			op := uowRaceOp{Op: "add-comment"}
			decodeUOWRaceError(&op, err)
			return op
		}
	}

	var executed []string
	for _, rc := range uowRaceCases() {
		t.Run(rc.name, func(t *testing.T) {
			provider.SetEventsJournalEnabled(rc.journal)
			defer provider.SetEventsJournalEnabled(false)
			id := "race-" + strings.ReplaceAll(rc.name, "/", "-")
			version := seed(t, id)
			loserText, winnerText := "", ""
			var loserFn, winnerFn func(context.Context) uowRaceOp
			if rc.loser == uowRaceDeleteLoses {
				winnerText = "winner comment"
				loserFn, winnerFn = deleteOp(id, version), commentOp(id, winnerText)
			} else {
				loserText = "loser comment"
				loserFn, winnerFn = commentOp(id, loserText), deleteOp(id, version)
			}
			base := ctx
			if rc.deferred {
				base = issueops.WithDeferredVersionCommit(base)
			}
			var lres, wres uowRaceOp
			if rc.serial {
				lres, wres = runUOWSerialRace(base, t, gate, t.Name()+"/loser", t.Name()+"/winner", loserFn, winnerFn)
			} else {
				lres, wres = runUOWHeldRace(base, t, gate, t.Name()+"/loser", t.Name()+"/winner", loserFn, winnerFn)
			}

			ev := uowRaceEvidence{
				Path: "uow", Case: rc.name, Server: "dolt " + serverVersion, Backend: backend, Journal: rc.journal,
				Loser: lres, Winner: wres,
				State: readUOWRaceState(ctx, t, reader, id, loserText, winnerText),
			}
			if rc.loser == uowRaceDeleteLoses {
				ev.RolledBk = ev.State.ParentPresent
			} else {
				ev.RolledBk = !ev.State.LoserCommentPresent
			}
			line, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal race evidence: %v", err)
			}
			t.Logf("delete-comment race evidence: %s", line)
			executed = append(executed, rc.name)

			for who, op := range map[string]uowRaceOp{"loser": ev.Loser, "winner": ev.Winner} {
				if op.CommitKind == "" && op.err != nil {
					continue // never reached a commit point: no committing session
				}
				if op.Settings["foreign_key_checks"] != "1" || op.Settings["dolt_force_transaction_commit"] != "0" {
					t.Fatalf("%s session settings %v, want foreign_key_checks=1 dolt_force_transaction_commit=0", who, op.Settings)
				}
			}
			requireUOWRaceIntegrity(t, rc, ev)
			switch {
			case rc.serial:
				assertUOWSerialDeleteCommentRace(t, rc.loser, ev)
			case rc.journal:
				if ev.Loser.Attempts < 2 {
					t.Errorf("journal-on loser attempts = %d, want a retry", ev.Loser.Attempts)
				}
				assertUOWSerialDeleteCommentRace(t, rc.loser, ev)
			case os.Getenv(expectSerialDeleteCommentEnv) == "1":
				assertUOWSerialDeleteCommentRace(t, rc.loser, ev)
			default:
				assertUOWCurrentDeleteCommentRace(t, rc.loser, ev)
			}
		})
	}
	t.Logf("delete-comment race coverage (uow): executed %v; "+
		"not covered by this item: labels, dependencies, the wisp plane, cancellation of a held loser, "+
		"persisted (non-sequencing) constraint violations", executed)
}

func decodeUOWRaceError(op *uowRaceOp, err error) {
	op.err = err
	if err == nil {
		return
	}
	op.Err = err.Error()
	op.NotFound = errors.Is(err, publicops.ErrNotFound)
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		op.MySQLCode = me.Number
		op.SQLState = strings.TrimRight(string(me.SQLState[:]), "\x00")
	}
}

// runUOWHeldRace runs loser until the gate holds it at its commit point, runs
// winner to completion, then releases loser.
func runUOWHeldRace(ctx context.Context, t *testing.T, gate *sqlgate.Connector, loserLabel, winnerLabel string,
	loser, winner func(context.Context) uowRaceOp) (uowRaceOp, uowRaceOp) {
	t.Helper()
	hold := gate.Arm(loserLabel)
	defer hold.Release()
	done := make(chan uowRaceOp, 1)
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
	var lres uowRaceOp
	select {
	case lres = <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("loser did not finish after release")
	}
	fillUOWRaceOp(t, gate, &lres, loserLabel)
	fillUOWRaceOp(t, gate, &wres, winnerLabel)
	return lres, wres
}

// runUOWSerialRace runs winner to completion, then loser, with no hold: the
// non-overlap control.
func runUOWSerialRace(ctx context.Context, t *testing.T, gate *sqlgate.Connector, loserLabel, winnerLabel string,
	loser, winner func(context.Context) uowRaceOp) (uowRaceOp, uowRaceOp) {
	t.Helper()
	wres := winner(sqlgate.WithLabel(ctx, winnerLabel))
	lres := loser(sqlgate.WithLabel(ctx, loserLabel))
	fillUOWRaceOp(t, gate, &lres, loserLabel)
	fillUOWRaceOp(t, gate, &wres, winnerLabel)
	return lres, wres
}

func fillUOWRaceOp(t *testing.T, gate *sqlgate.Connector, op *uowRaceOp, label string) {
	t.Helper()
	op.CommitKind = gate.CommitKind(label)
	op.Attempts = gate.Attempts(label)
	settings, err := gate.Settings(label)
	if err != nil {
		t.Fatalf("%s settings: %v", label, err)
	}
	op.Settings = settings
}

// readUOWRaceState reads the final state on an ungated pool.
func readUOWRaceState(ctx context.Context, t *testing.T, db *sql.DB, issueID, loserText, winnerText string) uowRaceState {
	t.Helper()
	var st uowRaceState
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
	// Engine-side constraint validation, test-only.
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_constraint_violations").Scan(&st.ConstraintViolations); err != nil {
		t.Fatalf("read dolt_constraint_violations: %v", err)
	}
	return st
}

// requireUOWRaceIntegrity fails on an orphan, a leftover violation, a loser
// that failed with its write visible, or a commit kind other than the case's.
func requireUOWRaceIntegrity(t *testing.T, rc uowRaceCase, ev uowRaceEvidence) {
	t.Helper()
	if ev.State.OrphanComments != 0 {
		t.Errorf("%d orphan comment(s) after the race", ev.State.OrphanComments)
	}
	if ev.State.ConstraintViolations != 0 {
		t.Errorf("%d dolt_constraint_violations row(s) after the race", ev.State.ConstraintViolations)
	}
	if ev.Loser.err != nil && !ev.RolledBk {
		t.Errorf("loser failed (%v) but its write is visible: an ambiguous outcome", ev.Loser.err)
	}
	if rc.serial {
		return // a refused serial comment never reaches a commit point
	}
	want := "CALL DOLT_COMMIT"
	if rc.deferred {
		want = "COMMIT"
	}
	for who, op := range map[string]uowRaceOp{"loser": ev.Loser, "winner": ev.Winner} {
		if op.CommitKind != want {
			t.Errorf("%s commit kind = %q, want %q", who, op.CommitKind, want)
		}
	}
}

// assertUOWCurrentDeleteCommentRace asserts the characterized outcome, the
// same one the direct store gives: the released loser is rolled back with
// error 1105 (HY000), not retried, and no orphan survives.
func assertUOWCurrentDeleteCommentRace(t *testing.T, loser string, ev uowRaceEvidence) {
	t.Helper()
	if ev.Winner.err != nil {
		t.Fatalf("winner: %v", ev.Winner.err)
	}
	if ev.Loser.err == nil {
		t.Fatalf("loser succeeded; the characterized outcome is a 1105 rollback (evidence above)")
	}
	if ev.Loser.MySQLCode != 1105 || ev.Loser.SQLState != "HY000" {
		t.Errorf("loser error = %d (%s), want 1105 (HY000): %v", ev.Loser.MySQLCode, ev.Loser.SQLState, ev.Loser.err)
	}
	for _, want := range []string{"constraint violations", "transaction rolled back"} {
		if !strings.Contains(ev.Loser.Err, want) {
			t.Errorf("loser error %q does not mention %q", ev.Loser.Err, want)
		}
	}
	if ev.Loser.NotFound {
		t.Errorf("loser error unexpectedly matches ErrNotFound: %v", ev.Loser.err)
	}
	if ev.Loser.Attempts != 1 {
		t.Errorf("loser attempts = %d, want 1 (1105 is not retried)", ev.Loser.Attempts)
	}
	switch loser {
	case uowRaceDeleteLoses:
		if !ev.State.ParentPresent || ev.State.Comments != 1 || !ev.State.WinnerCommentPresent {
			t.Errorf("final state %+v, want the issue kept with only the winner's comment", ev.State)
		}
	case uowRaceCommentLoses:
		if ev.State.ParentPresent || ev.State.Comments != 0 {
			t.Errorf("final state %+v, want the issue deleted with no comments", ev.State)
		}
	}
}

// assertUOWSerialDeleteCommentRace asserts the winner-then-loser serial
// outcome: a delete after a comment cascades it away; a comment after a
// delete is refused as not found.
func assertUOWSerialDeleteCommentRace(t *testing.T, loser string, ev uowRaceEvidence) {
	t.Helper()
	switch loser {
	case uowRaceDeleteLoses:
		if ev.Loser.err != nil || ev.Loser.Deleted != 1 {
			t.Errorf("serial outcome: the delete after a comment succeeds and deletes 1 issue; got deleted=%d, err: %v",
				ev.Loser.Deleted, ev.Loser.err)
		}
		if ev.State.ParentPresent || ev.State.Comments != 0 {
			t.Errorf("serial outcome: issue and its comments deleted; got %+v", ev.State)
		}
	case uowRaceCommentLoses:
		if !errors.Is(ev.Loser.err, publicops.ErrNotFound) {
			t.Errorf("serial outcome: the comment after a delete is refused with ErrNotFound; got: %v", ev.Loser.err)
		}
		if ev.State.ParentPresent {
			t.Errorf("serial outcome: issue deleted; got %+v", ev.State)
		}
	}
}
