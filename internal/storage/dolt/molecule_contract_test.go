package dolt

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestMoleculeContract runs the molecule contract (in-transaction auto-close on
// Lifecycle.Close and BatchCloser.CloseBatch, and the MoleculeStepper) against
// the server-backed store. One store, sequential subtests, ids namespaced by
// the fixture prefix.
func TestMoleculeContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltMoleculeFixture(t, "mol")
	defer cleanup()

	for _, test := range []struct {
		name string
		run  func(*testing.T, context.Context, conformance.MoleculeFixture)
	}{
		{"CloseAutoClosesTheCompletedRoot", conformance.RunMoleculeCloseAutoClosesTheCompletedRoot},
		{"AutoCloseIsOptInAndRuleBound", conformance.RunMoleculeAutoCloseIsOptInAndRuleBound},
		{"RecloseHealsAStrandedRoot", conformance.RunMoleculeRecloseHealsAStrandedRoot},
		{"BatchCloseAutoClosesTheRootOnce", conformance.RunMoleculeBatchCloseAutoClosesTheRootOnce},
		{"RefusedRootKeepsTheStepClose", conformance.RunMoleculeRefusedRootKeepsTheStepClose},
		{"GuardedRootKeepsTheStepClose", conformance.RunMoleculeGuardedRootKeepsTheStepClose},
		{"StepperClaimsTheNextReadyStepForTheActor", conformance.RunMoleculeStepperClaimsTheNextReadyStepForTheActor},
		{"StepperNeverTakesAStepAnotherActorHolds", conformance.RunMoleculeStepperNeverTakesAStepAnotherActorHolds},
		{"StepperAnswersCompleteAndOutsideAMolecule", conformance.RunMoleculeStepperAnswersCompleteAndOutsideAMolecule},
		{"StepperClaimsAWispStep", conformance.RunMoleculeStepperClaimsAWispStep},
	} {
		t.Run(test.name, func(t *testing.T) { test.run(t, ctx, fixture) })
	}
}

func newDoltMoleculeFixture(t *testing.T, prefix string) (conformance.MoleculeFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	cleanup := func() {
		cancel()
		storeCleanup()
	}
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		cleanup()
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	closer, err := store.BatchCloser()
	if err != nil {
		cleanup()
		t.Fatalf("BatchCloser(): %v", err)
	}
	stepper, err := store.MoleculeStepper()
	if err != nil {
		cleanup()
		t.Fatalf("MoleculeStepper(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.MoleculeFixture{
		IssuePrefix:   kit.IssuePrefix,
		Lifecycle:     lifecycle,
		BatchCloser:   closer,
		Stepper:       stepper,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		QueryScalar:   kit.QueryScalar,
	}, ctx, cleanup
}

// seedRacingMolecule seeds an auto-closing molecule root with n open steps
// and answers the steps.
func seedRacingMolecule(t *testing.T, ctx context.Context, store *DoltStore, root string, n int) []string {
	t.Helper()
	if err := store.CreateIssue(ctx, &types.Issue{ID: root, Title: "molecule " + root, IssueType: types.TypeMolecule, Status: types.StatusOpen, Priority: 2}, "tester"); err != nil {
		t.Fatalf("create %s: %v", root, err)
	}
	steps := make([]string, n)
	for i := range steps {
		steps[i] = fmt.Sprintf("%s.%d", root, i+1)
		createPerm(t, ctx, store, steps[i])
		addDependency(t, ctx, store, steps[i], root, types.DepParentChild)
	}
	return steps
}

// readStatus reads id's status from the issues table.
func readStatus(t *testing.T, ctx context.Context, store *DoltStore, id string) string {
	t.Helper()
	var status string
	if err := store.db.QueryRowContext(ctx, "SELECT status FROM issues WHERE id = ?", id).Scan(&status); err != nil {
		t.Fatalf("read %s status: %v", id, err)
	}
	return status
}

// closeTwoStepsInRacingTransactions closes steps[0] and steps[1], each with
// the molecule auto-close, in two transactions that both begin before either
// closes, so each reads the other's step open: the race's interleaving, made
// deterministic. Neither may auto-close the root. It commits both and answers
// the two commit errors.
func closeTwoStepsInRacingTransactions(t *testing.T, ctx context.Context, store *DoltStore, steps []string) [2]error {
	t.Helper()
	var txs [2]*sql.Tx
	for i := range txs {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin transaction %d: %v", i, err)
		}
		defer tx.Rollback() // no-op after Commit; preserves cleanup on early Fatal.
		txs[i] = tx
	}
	for i, tx := range txs {
		res, _, err := issueops.ExecuteClose(ctx, tx, publicops.CloseRequest{
			Actor: "tester", IssueID: steps[i], Reason: "done", AutoCloseMolecule: true,
		})
		if err != nil {
			t.Fatalf("close %s: %v", steps[i], err)
		}
		if res.AutoClosedMolecule != nil {
			t.Fatalf("close %s auto-closed %s, but in its own transaction %s is still open", steps[i], res.AutoClosedMolecule.ID, steps[1-i])
		}
	}
	return [2]error{txs[0].Commit(), txs[1].Commit()}
}

// TestMoleculeAutoCloseLastStepsCannotBothMissTheRoot is the controlled
// two-transaction regression for CloseRequest.AutoCloseMolecule. The closes of
// a molecule's last two open steps, in concurrent transactions, each read the
// other step as still open, so neither closes the root, and their own writes
// touch disjoint rows. If both commit, every step is closed and the root is
// stranded open. The coordination cell both rewrite
// (issueops.TouchMoleculeCoordinationInTx) must make one of them conflict
// instead, so that its retry reads the other's close; the test below drives
// that retry through the store.
func TestMoleculeAutoCloseLastStepsCannotBothMissTheRoot(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	const root = "mol-race"
	commitErrs := closeTwoStepsInRacingTransactions(t, ctx, store, seedRacingMolecule(t, ctx, store, root, 2))
	t.Logf("last-step commits: %v", commitErrs)
	if commitErrs[0] == nil && commitErrs[1] == nil {
		t.Fatalf("stranded molecule: both last-step closes committed and %s is %s with every step closed; one transaction must conflict",
			root, readStatus(t, ctx, store, root))
	}
	for i, err := range commitErrs {
		if err != nil && !isSerializationError(err) {
			t.Fatalf("commit %d error = %v, want a serialization conflict", i, err)
		}
	}
}

// TestMoleculeAutoCloseSiblingClosesConflictBeforeTheLastStep pins the cost
// the coordination cell's docs state (issueops.TouchMoleculeCoordinationInTx,
// CloseRequest.AutoCloseMolecule, the CHANGELOG): every close under an open
// auto-closing root rewrites the cell, so two concurrent sibling closes
// conflict even while a third step keeps the molecule from completing. If this
// stops conflicting, those docs are stale.
func TestMoleculeAutoCloseSiblingClosesConflictBeforeTheLastStep(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	commitErrs := closeTwoStepsInRacingTransactions(t, ctx, store, seedRacingMolecule(t, ctx, store, "mol-siblings", 3))
	t.Logf("sibling commits: %v", commitErrs)
	if commitErrs[0] == nil && commitErrs[1] == nil {
		t.Fatal("two concurrent sibling closes under an open auto-closing root both committed; want one serialization conflict")
	}
	for i, err := range commitErrs {
		if err != nil && !isSerializationError(err) {
			t.Fatalf("commit %d error = %v, want a serialization conflict", i, err)
		}
	}
}

// raceMoleculeCloses seeds an auto-closing molecule of n steps under root and
// closes every step at once through lifecycle, whose retry replays each close
// that loses the coordination conflict. Every close must land, and the root
// must end closed, reported by exactly one of them.
func raceMoleculeCloses(t *testing.T, ctx context.Context, store *DoltStore, lifecycle publicops.Lifecycle, root string, n int) {
	t.Helper()
	steps := seedRacingMolecule(t, ctx, store, root, n)
	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		results = make([]publicops.CloseResult, n)
		errs    = make([]error, n)
	)
	for i, step := range steps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = lifecycle.Close(ctx, publicops.CloseRequest{
				Actor: "tester", IssueID: step, Reason: "done", AutoCloseMolecule: true,
			})
		}()
	}
	close(start)
	wg.Wait()

	reported := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("close %s: %v", steps[i], err)
		}
		if results[i].AutoClosedMolecule != nil {
			reported++
		}
	}
	if status := readStatus(t, ctx, store, root); status != string(types.StatusClosed) {
		t.Fatalf("%s is %s after all %d of its steps closed, want closed", root, status, n)
	}
	if reported != 1 {
		t.Errorf("%s was reported by %d closes, want exactly one", root, reported)
	}
}

// TestMoleculeAutoCloseConcurrentLastStepsCloseTheRoot races the closes of a
// molecule's last two steps through the store's Lifecycle role, whose retry
// replays the transaction that loses the coordination conflict: in every
// round the root ends closed, reported by exactly one of the two closes.
func TestMoleculeAutoCloseConcurrentLastStepsCloseTheRoot(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	const rounds = 8
	for round := range rounds {
		raceMoleculeCloses(t, ctx, store, lifecycle, fmt.Sprintf("mol-race-%d", round), 2)
	}
}

// TestMoleculeAutoCloseSiblingFanOutClosesTheRoot closes all the steps of one
// molecule at once. They serialize on the root's coordination cell, and the
// store's retry carries every one of them to a commit: the fan-out costs
// retries, not closes, and the root is closed exactly once.
func TestMoleculeAutoCloseSiblingFanOutClosesTheRoot(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}

	raceMoleculeCloses(t, ctx, store, lifecycle, "mol-fanout", 6)
}
