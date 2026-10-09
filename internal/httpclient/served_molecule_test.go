//go:build cgo

package httpclient

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/httpclient/wire"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The molecule contract against the served surface: the client sends
// auto_close_molecule and dials advanceMolecule, so the server's library runs
// the auto-close in the step close's transaction and the advance server-side.
//
// Against a server whose handshake lacks the two molecule tokens (an older
// server) the client refuses before dialing; the served refusal test below
// pins that the step it was asked to close stays open and unclaimed.

func moleculeContractCases() []struct {
	name string
	run  func(*testing.T, context.Context, conformance.MoleculeFixture)
} {
	return []struct {
		name string
		run  func(*testing.T, context.Context, conformance.MoleculeFixture)
	}{
		{"CloseAutoClosesTheCompletedRoot", conformance.RunMoleculeCloseAutoClosesTheCompletedRoot},
		{"AutoCloseIsOptInAndRuleBound", conformance.RunMoleculeAutoCloseIsOptInAndRuleBound},
		{"RecloseHealsAStrandedRoot", conformance.RunMoleculeRecloseHealsAStrandedRoot},
		{"BatchCloseAutoClosesTheRootOnce", conformance.RunMoleculeBatchCloseAutoClosesTheRootOnce},
		{"RefusedRootKeepsTheStepClose", conformance.RunMoleculeRefusedRootKeepsTheStepClose},
		{"StepperClaimsTheNextReadyStepForTheActor", conformance.RunMoleculeStepperClaimsTheNextReadyStepForTheActor},
		{"StepperNeverTakesAStepAnotherActorHolds", conformance.RunMoleculeStepperNeverTakesAStepAnotherActorHolds},
		{"StepperAnswersCompleteAndOutsideAMolecule", conformance.RunMoleculeStepperAnswersCompleteAndOutsideAMolecule},
		{"StepperClaimsAWispStep", conformance.RunMoleculeStepperClaimsAWispStep},
	}
}

func newServedMoleculeFixture(t *testing.T, env *servedEnv, subject *Store) conformance.MoleculeFixture {
	t.Helper()
	lifecycle, err := subject.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	closer, err := subject.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser(): %v", err)
	}
	stepper, err := subject.MoleculeStepper()
	if err != nil {
		t.Fatalf("MoleculeStepper(): %v", err)
	}
	return conformance.MoleculeFixture{
		IssuePrefix:   env.prefix,
		Lifecycle:     lifecycle,
		BatchCloser:   closer,
		Stepper:       stepper,
		CreateIssue:   env.createIssue,
		CreateWisp:    env.createWisp,
		AddDependency: env.addDependency,
		QueryScalar:   env.queryScalar,
	}
}

// degradedSubject is a client of env's server whose handshake lacks tokens, as
// an older server's would.
func degradedSubject(t *testing.T, env *servedEnv, strike ...string) *Store {
	t.Helper()
	snapshot, err := env.subject.snapshot(t.Context())
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	for _, token := range strike {
		if !slices.Contains(snapshot.Capabilities, token) {
			t.Fatalf("the served build does not advertise %q; the degraded arm would prove nothing", token)
		}
	}
	masked := *snapshot
	masked.Capabilities = slices.DeleteFunc(slices.Clone(snapshot.Capabilities), func(token string) bool {
		return slices.Contains(strike, token)
	})
	return New(env.subject.target, env.subject.wire, &masked)
}

func advanceToken(t *testing.T) string {
	t.Helper()
	token, ok := wire.CapabilityFor(wire.OpAdvanceMolecule)
	if !ok || token == "" {
		t.Fatal("advanceMolecule has no capability token")
	}
	return token
}

func TestServedMoleculeContract(t *testing.T) {
	for _, tc := range moleculeContractCases() {
		t.Run(tc.name, func(t *testing.T) {
			env := newServedEnv(t, "hmol")
			tc.run(t, t.Context(), newServedMoleculeFixture(t, env, env.subject))
		})
	}
}

// Against an older server (the molecule tokens struck from the handshake) a
// close that asks for the auto-close and an advance are refused before they
// are dialed: the step stays open, nothing is claimed, and the refusal names
// the missing token.
func TestServedMoleculeRefusesAgainstAnOlderServer(t *testing.T) {
	env := newServedEnv(t, "hmold")
	ctx := t.Context()
	subject := degradedSubject(t, env, wire.CapCloseAutoCloseMolecule, advanceToken(t))
	fixture := newServedMoleculeFixture(t, env, subject)
	for _, issue := range []*types.Issue{
		{ID: "hmold-r", Title: "root", IssueType: types.TypeMolecule, Status: types.StatusOpen},
		{ID: "hmold-r.1", Title: "step", IssueType: types.TypeTask, Status: types.StatusOpen},
		{ID: "hmold-r.2", Title: "next", IssueType: types.TypeTask, Status: types.StatusOpen},
	} {
		if err := env.createIssue(ctx, issue, "t"); err != nil {
			t.Fatal(err)
		}
	}
	for _, child := range []string{"hmold-r.1", "hmold-r.2"} {
		if err := env.addDependency(ctx, &types.Dependency{IssueID: child, DependsOnID: "hmold-r", Type: types.DepParentChild}, "t"); err != nil {
			t.Fatal(err)
		}
	}
	status := func(id string) string {
		t.Helper()
		got, err := env.subject.GetIssue(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return string(got.Status) + "/" + got.Assignee
	}
	requireRefusal := func(what string, err error, token string) {
		t.Helper()
		var unsup *storage.ErrUnsupported
		if !errors.As(err, &unsup) || unsup.Capability != token {
			t.Fatalf("%s = %v, want the pre-dial refusal naming %q", what, err, token)
		}
	}

	_, err := fixture.Lifecycle.Close(ctx, issueops.CloseRequest{Actor: "alice", IssueID: "hmold-r.1", AutoCloseMolecule: true})
	requireRefusal("Close", err, wire.CapCloseAutoCloseMolecule)
	_, err = fixture.BatchCloser.CloseBatch(ctx, issueops.CloseBatchRequest{
		Actor: "alice", Items: []issueops.BatchCloseItem{{IssueID: "hmold-r.1"}}, AutoCloseMolecule: true,
	})
	requireRefusal("CloseBatch", err, wire.CapCloseAutoCloseMolecule)
	if got := status("hmold-r.1"); got != "open/" {
		t.Errorf("the refused close's step = %s, want open/ (nothing dialed)", got)
	}

	_, err = fixture.Stepper.Advance(ctx, issueops.AdvanceRequest{Actor: "alice", ClosedStepID: "hmold-r.1", AutoClaim: true})
	requireRefusal("Advance", err, advanceToken(t))
	if got := status("hmold-r.2"); got != "open/" {
		t.Errorf("the refused advance's next step = %s, want open/ (nothing claimed)", got)
	}
}

// The ROLE has no anchor cap and the operation refuses more than 100: the
// client splits, and every anchor comes back once, in request order.
func TestServedEdgeReaderSplitsPastTheWireCap(t *testing.T) {
	env := newServedEnv(t, "hedgecap")
	ctx := t.Context()
	if err := env.createIssue(ctx, &types.Issue{ID: "hedgecap-a", Title: "a", IssueType: types.TypeTask, Status: types.StatusOpen}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := env.createIssue(ctx, &types.Issue{ID: "hedgecap-b", Title: "b", IssueType: types.TypeTask, Status: types.StatusOpen}, "t"); err != nil {
		t.Fatal(err)
	}
	if err := env.addDependency(ctx, &types.Dependency{IssueID: "hedgecap-b", DependsOnID: "hedgecap-a", Type: types.DepBlocks}, "t"); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, 250)
	for i := range 248 {
		ids = append(ids, fmt.Sprintf("hedgecap-missing-%03d", i))
	}
	ids = append(ids, "hedgecap-a", "hedgecap-b")
	reader, err := env.subject.EdgeReader()
	if err != nil {
		t.Fatal(err)
	}
	res, err := reader.ReadEdges(ctx, issueops.EdgeReadRequest{IDs: ids})
	if err != nil {
		t.Fatalf("ReadEdges over %d anchors: %v", len(ids), err)
	}
	if len(res.Anchors) != len(ids) {
		t.Fatalf("ReadEdges answered %d anchors for %d", len(res.Anchors), len(ids))
	}
	for i, anchor := range res.Anchors {
		if anchor.ID != ids[i] {
			t.Fatalf("anchor %d = %s, want %s (request order)", i, anchor.ID, ids[i])
		}
	}
	last := res.Anchors[len(res.Anchors)-1]
	if last.Missing || len(last.Edges) != 1 || last.Edges[0].DependsOnID != "hedgecap-a" {
		t.Errorf("hedgecap-b = %+v, want its one edge to hedgecap-a", last)
	}
	if !res.Anchors[0].Missing {
		t.Errorf("an id naming nothing was not reported missing: %+v", res.Anchors[0])
	}
}

// Against a server without issues.batchGet the role still answers: one
// getIssue per distinct id, the same rows and the same Missing.
func TestServedBatchGetterAnswersAgainstAServerWithoutTheBatch(t *testing.T) {
	env := newServedEnv(t, "hbgfb")
	ctx := t.Context()
	if err := env.createIssue(ctx, &types.Issue{ID: "hbgfb-a", Title: "a", IssueType: types.TypeTask, Status: types.StatusOpen, Labels: []string{"x"}}, "t"); err != nil {
		t.Fatal(err)
	}
	batchGet, _ := wire.CapabilityFor(wire.OpBatchGetIssues)
	getter, err := degradedSubject(t, env, batchGet).BatchGetter()
	if err != nil {
		t.Fatal(err)
	}
	res, err := getter.GetMany(ctx, issueops.GetManyRequest{IDs: []string{"hbgfb-a", "hbgfb-nope", "hbgfb-a"}})
	if err != nil {
		t.Fatalf("GetMany without issues.batchGet: %v", err)
	}
	if len(res.Issues) != 1 || res.Issues[0].ID != "hbgfb-a" || !slices.Equal(res.Issues[0].Labels, []string{"x"}) || res.Issues[0].RowVersion == 0 {
		t.Errorf("Issues = %+v, want hbgfb-a once, labeled, with its revision", res.Issues)
	}
	if !slices.Equal(res.Missing, []string{"hbgfb-nope"}) {
		t.Errorf("Missing = %v, want [hbgfb-nope]", res.Missing)
	}
}
