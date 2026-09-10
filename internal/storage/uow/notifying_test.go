package uow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// ── Fakes ───────────────────────────────────────────────────────────

type firedHook struct {
	event string
	id    string
}

// notifyRunner records what the sink was handed, in order. It stands in for
// *hooks.Runner, whose own behavior this file does not re-test.
type notifyRunner struct {
	fired  []firedHook
	issues []*types.Issue
}

func (r *notifyRunner) Run(event string, issue *types.Issue) {
	id := ""
	if issue != nil {
		id = issue.ID
	}
	r.fired = append(r.fired, firedHook{event: event, id: id})
	r.issues = append(r.issues, issue)
}

func (r *notifyRunner) events() []firedHook { return r.fired }

// snapshots are the payloads the runner was handed, in the same order.
func (r *notifyRunner) snapshots() []*types.Issue { return r.issues }

func (r *notifyRunner) reset() { r.fired, r.issues = nil, nil }

// notifyIssueUC is a use case over an in-memory pair of planes. Only the verbs
// the tests drive are implemented; the embedded interface panics on anything
// else, which is the point — a test that reaches an unimplemented verb is
// testing something it did not mean to.
type notifyIssueUC struct {
	domain.IssueUseCase
	issues map[string]*types.Issue
	wisps  map[string]*types.Issue
	err    error
}

func newNotifyIssueUC(issues ...*types.Issue) *notifyIssueUC {
	uc := &notifyIssueUC{issues: map[string]*types.Issue{}, wisps: map[string]*types.Issue{}}
	for _, issue := range issues {
		uc.issues[issue.ID] = issue
	}
	return uc
}

// GetIssue and GetWisp answer a miss with ErrNotFound rather than a bare
// error, because operationIssue tells "this id is not on that plane" from "that
// plane is broken" by that sentinel alone: a bare error there aborts the
// two-plane resolve instead of falling through to the other plane.
func (u *notifyIssueUC) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	issue, ok := u.issues[id]
	if !ok {
		return nil, fmt.Errorf("%w: issue %s", publicops.ErrNotFound, id)
	}
	return issue, nil
}

func (u *notifyIssueUC) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	wisp, ok := u.wisps[id]
	if !ok {
		return nil, fmt.Errorf("%w: wisp %s", publicops.ErrNotFound, id)
	}
	return wisp, nil
}

func (u *notifyIssueUC) CreateIssue(_ context.Context, params domain.CreateIssueParams, _ string) (domain.CreateIssueResult, error) {
	if u.err != nil {
		return domain.CreateIssueResult{}, u.err
	}
	u.issues[params.Issue.ID] = params.Issue
	return domain.CreateIssueResult{Issue: params.Issue}, nil
}

func (u *notifyIssueUC) CreateWisp(_ context.Context, params domain.CreateIssueParams, _ string) (domain.CreateIssueResult, error) {
	u.wisps[params.Issue.ID] = params.Issue
	return domain.CreateIssueResult{Issue: params.Issue}, nil
}

func (u *notifyIssueUC) UpdateIssue(_ context.Context, id string, _ map[string]any, _ string) error {
	if u.err != nil {
		return u.err
	}
	return nil
}

func (u *notifyIssueUC) ApplyUpdate(_ context.Context, id string, _ domain.UpdateSpec, _ string) (*types.Issue, error) {
	return u.issues[id], nil
}

func (u *notifyIssueUC) ClaimIssue(_ context.Context, id, actor string) (domain.ClaimResult, error) {
	issue, ok := u.issues[id]
	if !ok {
		return domain.ClaimResult{}, errors.New("no such issue")
	}
	if issue.Assignee == actor {
		return domain.ClaimResult{AlreadyClaimed: true}, nil
	}
	issue.Assignee = actor
	return domain.ClaimResult{}, nil
}

func (u *notifyIssueUC) CloseIssueChecked(_ context.Context, id string, _ domain.CloseIssueParams, _ string, _ bool) (domain.CloseIssueResult, error) {
	issue, ok := u.issues[id]
	if !ok {
		return domain.CloseIssueResult{}, errors.New("no such issue")
	}
	if issue.Status == types.StatusClosed {
		return domain.CloseIssueResult{Issue: issue}, nil
	}
	issue.Status = types.StatusClosed
	return domain.CloseIssueResult{Issue: issue, Closed: true}, nil
}

func (u *notifyIssueUC) ReopenIssue(_ context.Context, id string, _ domain.ReopenIssueParams, _ string) (domain.ReopenIssueResult, error) {
	issue, ok := u.issues[id]
	if !ok {
		return domain.ReopenIssueResult{}, errors.New("no such issue")
	}
	issue.Status = types.StatusOpen
	return domain.ReopenIssueResult{Issue: issue, Reopened: true}, nil
}

// ApplyIssueGraph mints one row per node and writes no edges of its own — the
// recorder derives those from the plan, which is the property under test.
func (u *notifyIssueUC) ApplyIssueGraph(_ context.Context, plan domain.GraphPlan, _ string) (domain.GraphApplyResult, error) {
	ids := map[string]string{}
	for _, node := range plan.Nodes {
		u.issues[node.Issue.ID] = node.Issue
		ids[node.Key] = node.Issue.ID
	}
	return domain.GraphApplyResult{IDs: ids}, nil
}

func (u *notifyIssueUC) DeleteIssue(_ context.Context, id, _ string) (domain.DeleteIssuesResult, error) {
	delete(u.issues, id)
	return domain.DeleteIssuesResult{DeletedCount: 1}, nil
}

func (u *notifyIssueUC) Unclaim(_ context.Context, id, _ string, _ bool) error { return nil }

func (u *notifyIssueUC) Heartbeat(_ context.Context, id, _ string) error { return nil }

type notifyDepUC struct {
	domain.DependencyUseCase
	records map[string][]*types.Dependency
	removed bool
}

func (u *notifyDepUC) AddDependency(_ context.Context, dep *types.Dependency, _ string) error {
	u.records[dep.IssueID] = append(u.records[dep.IssueID], dep)
	return nil
}

func (u *notifyDepUC) RemoveDependency(_ context.Context, issueID, _, _ string) error {
	u.removed = true
	return nil
}

func (u *notifyDepUC) GetIssueDependencyRecords(_ context.Context, ids []string) (map[string][]*types.Dependency, error) {
	out := map[string][]*types.Dependency{}
	for _, id := range ids {
		out[id] = u.records[id]
	}
	return out, nil
}

// notifyLabelUC answers off the same in-memory planes the issue use case
// serves, so a buffered snapshot's labels are the labels the mutation left.
type notifyLabelUC struct {
	domain.LabelUseCase
	issues *notifyIssueUC
}

func (u *notifyLabelUC) AddLabel(_ context.Context, issueID, label, _ string) error {
	issue, ok := u.issues.issues[issueID]
	if !ok {
		return errors.New("no such issue")
	}
	issue.Labels = append(issue.Labels, label)
	return nil
}

func (u *notifyLabelUC) SetLabels(_ context.Context, issueID string, labels []string, _ string) error {
	issue, ok := u.issues.issues[issueID]
	if !ok {
		return errors.New("no such issue")
	}
	issue.Labels = append([]string(nil), labels...)
	return nil
}

func (u *notifyLabelUC) GetLabels(_ context.Context, issueID string) ([]string, error) {
	issue, ok := u.issues.issues[issueID]
	if !ok {
		return nil, errors.New("no such issue")
	}
	return append([]string(nil), issue.Labels...), nil
}

func (u *notifyLabelUC) GetWispLabels(_ context.Context, wispID string) ([]string, error) {
	wisp, ok := u.issues.wisps[wispID]
	if !ok {
		return nil, errors.New("no such wisp")
	}
	return append([]string(nil), wisp.Labels...), nil
}

type notifyCommentUC struct {
	domain.CommentUseCase
}

func (u *notifyCommentUC) AddCommentToIssue(_ context.Context, issueID, author, text string) (*types.Comment, error) {
	return &types.Comment{IssueID: issueID, Author: author, Text: text}, nil
}

// notifyFixture wires a notifying provider over one mock unit of work.
type notifyFixture struct {
	provider UnitOfWorkProvider
	uow      *mockUnitOfWork
	issues   *notifyIssueUC
	deps     *notifyDepUC
	runner   *notifyRunner
}

func newNotifyFixture(t *testing.T, seed ...*types.Issue) *notifyFixture {
	t.Helper()
	issues := newNotifyIssueUC(seed...)
	deps := &notifyDepUC{records: map[string][]*types.Dependency{}}
	uw := &mockUnitOfWork{
		issueUseCase:      issues,
		dependencyUseCase: deps,
		labelUseCase:      &notifyLabelUC{issues: issues},
		commentUseCase:    &notifyCommentUC{},
	}
	runner := &notifyRunner{}
	return &notifyFixture{
		provider: NewNotifyingProvider(&mockUnitOfWorkProvider{uows: []*mockUnitOfWork{uw}}, Sinks{Hook: runner}),
		uow:      uw,
		issues:   issues,
		deps:     deps,
		runner:   runner,
	}
}

func (f *notifyFixture) newUOW(t *testing.T) UnitOfWork {
	t.Helper()
	uw, err := f.provider.NewUOW(context.Background())
	if err != nil {
		t.Fatalf("NewUOW: %v", err)
	}
	return uw
}

func seedIssue(id string) *types.Issue {
	return &types.Issue{ID: id, Title: id, Status: types.StatusOpen}
}

// closeThroughBatch closes ids through the REAL batch composition rather than
// through the use case directly. That is the whole point of the rows that call
// it: closeBatchItem and the single close reach the same recording verb, so
// only a test that goes through the composition can tell the two firing rules
// apart.
func closeThroughBatch(ctx context.Context, t *testing.T, uw UnitOfWork, ids ...string) {
	t.Helper()
	for _, id := range ids {
		outcome := closeBatchItem(ctx, uw,
			publicops.CloseBatchRequest{Actor: "tester"},
			publicops.BatchCloseItem{IssueID: id})
		if outcome.Err != nil {
			t.Fatalf("closeBatchItem(%s): %v", id, outcome.Err)
		}
	}
}

// ── The contract ────────────────────────────────────────────────────

// TestNewNotifyingProviderWithoutSinksReturnsInnerUnwrapped is the zero-cost
// half of the contract: a bd with hooks disabled must not pay for this file,
// and must not lose the concrete provider's own capabilities behind a wrapper.
func TestNewNotifyingProviderWithoutSinksReturnsInnerUnwrapped(t *testing.T) {
	inner := &mockUnitOfWorkProvider{}
	if got := NewNotifyingProvider(inner, Sinks{}); got != UnitOfWorkProvider(inner) {
		t.Fatalf("NewNotifyingProvider with no sinks = %T, want the inner provider unwrapped", got)
	}
	if got := NewNotifyingProvider(nil, Sinks{Hook: &notifyRunner{}}); got != nil {
		t.Fatalf("NewNotifyingProvider over a nil provider = %T, want nil rather than a wrapper around nothing", got)
	}
}

// TestUnwrapProviderReachesTheProviderBeneathTheHooks pins the escape hatch a
// caller that must NOT run hooks takes — `bd serve`, which documents that it
// runs none, serves from beneath this layer.
func TestUnwrapProviderReachesTheProviderBeneathTheHooks(t *testing.T) {
	inner := &mockUnitOfWorkProvider{}
	notifying := NewNotifyingProvider(inner, Sinks{Hook: &notifyRunner{}})

	if !ProviderFiresHooks(notifying) {
		t.Fatal("ProviderFiresHooks(notifying) = false, want true")
	}
	if got := UnwrapProvider(notifying); got != UnitOfWorkProvider(inner) {
		t.Fatalf("UnwrapProvider = %T, want the inner provider", got)
	}
	if ProviderFiresHooks(inner) {
		t.Fatal("ProviderFiresHooks(inner) = true, want false for an undecorated provider")
	}
	if got := UnwrapProvider(inner); got != UnitOfWorkProvider(inner) {
		t.Fatalf("UnwrapProvider of an undecorated provider = %T, want it unchanged", got)
	}
	if ProviderFiresHooks(nil) || UnwrapProvider(nil) != nil {
		t.Fatal("a nil provider neither fires hooks nor unwraps to anything")
	}
}

// TestNotifyingUOWFiresNothingUntilCommit is the post-commit contract. A hook
// that ran mid-transaction would report a mutation that a later rollback erases.
func TestNotifyingUOWFiresNothingUntilCommit(t *testing.T) {
	f := newNotifyFixture(t, seedIssue("bd-1"))
	ctx := context.Background()
	uw := f.newUOW(t)

	if _, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: seedIssue("bd-2")}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := uw.IssueUseCase().UpdateIssue(ctx, "bd-1", map[string]any{"title": "new"}, "tester"); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	if len(f.runner.events()) != 0 {
		t.Fatalf("hooks fired before commit: %v", f.runner.events())
	}

	if err := uw.Commit(ctx, "bd: test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want := []firedHook{{hooks.EventCreate, "bd-2"}, {hooks.EventUpdate, "bd-1"}}
	assertFired(t, f.runner.events(), want)
}

// TestNotifyingUOWRollbackFiresNothing covers both ways an attempt ends without
// a commit: the caller closes it, or the commit itself fails.
func TestNotifyingUOWRollbackFiresNothing(t *testing.T) {
	t.Run("closed without committing", func(t *testing.T) {
		f := newNotifyFixture(t, seedIssue("bd-1"))
		ctx := context.Background()
		uw := f.newUOW(t)
		if err := uw.IssueUseCase().UpdateIssue(ctx, "bd-1", map[string]any{"title": "new"}, "tester"); err != nil {
			t.Fatalf("UpdateIssue: %v", err)
		}
		uw.Close(ctx)
		if got := f.runner.events(); len(got) != 0 {
			t.Fatalf("rolled-back unit of work fired %v", got)
		}
		// The buffer is gone, not merely unread: a Commit after the rollback
		// must not replay it.
		if err := uw.Commit(ctx, "bd: test"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := f.runner.events(); len(got) != 0 {
			t.Fatalf("committing after a rollback replayed %v", got)
		}
	})

	t.Run("commit fails", func(t *testing.T) {
		f := newNotifyFixture(t, seedIssue("bd-1"))
		f.uow.commitErr = errors.New("commit refused")
		ctx := context.Background()
		uw := f.newUOW(t)
		if err := uw.IssueUseCase().UpdateIssue(ctx, "bd-1", map[string]any{"title": "new"}, "tester"); err != nil {
			t.Fatalf("UpdateIssue: %v", err)
		}
		if err := uw.Commit(ctx, "bd: test"); err == nil {
			t.Fatal("Commit succeeded, want the mock's refusal")
		}
		if got := f.runner.events(); len(got) != 0 {
			t.Fatalf("failed commit fired %v", got)
		}
	})
}

// TestNotifyingUOWDropsAWorkThatCommitsNothing pins the RunTx contract: an
// empty commit message means the attempt is rolled back rather than committed
// (tx.go), and nothing landed means nothing to report.
func TestNotifyingUOWDropsAWorkThatCommitsNothing(t *testing.T) {
	f := newNotifyFixture(t, seedIssue("bd-1"))
	err := RunTx(context.Background(), f.provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		return "", uw.IssueUseCase().UpdateIssue(ctx, "bd-1", map[string]any{"title": "new"}, "tester")
	})
	if err != nil {
		t.Fatalf("RunTx: %v", err)
	}
	if got := f.runner.events(); len(got) != 0 {
		t.Fatalf("work that committed nothing fired %v", got)
	}
}

// TestNotifyingProviderFiresThroughRunTx is the end-to-end shape every role in
// this package writes through: provider → unit of work → use case → commit.
func TestNotifyingProviderFiresThroughRunTx(t *testing.T) {
	f := newNotifyFixture(t, seedIssue("bd-1"))
	err := RunTx(context.Background(), f.provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if _, err := uw.IssueUseCase().CloseIssueChecked(ctx, "bd-1", domain.CloseIssueParams{}, "tester", false); err != nil {
			return "", err
		}
		return "bd: close issue", nil
	})
	if err != nil {
		t.Fatalf("RunTx: %v", err)
	}
	assertFired(t, f.runner.events(), []firedHook{{hooks.EventClose, "bd-1"}})
}

// TestNotifyingUOWOpToHookEventMapping is the parity table. Each row states
// what the DoltStorage plumbing fires for the same operation
// (internal/storage/hook_decorator.go), which is what this plumbing must fire.
func TestNotifyingUOWOpToHookEventMapping(t *testing.T) {
	for _, test := range []struct {
		name string
		// what the DoltStorage plumbing does with the same operation, for the
		// reader comparing the two by hand.
		parity string
		run    func(ctx context.Context, t *testing.T, uw UnitOfWork)
		want   []firedHook
	}{
		{
			name:   "create",
			parity: "HookFiringStore.CreateIssue fires on_create",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: seedIssue("bd-2")}, "tester"); err != nil {
					t.Fatalf("CreateIssue: %v", err)
				}
			},
			want: []firedHook{{hooks.EventCreate, "bd-2"}},
		},
		{
			name:   "create wisp",
			parity: "the store's own CreateIssue routes an ephemeral issue to the wisps table and fires on_create all the same",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.IssueUseCase().CreateWisp(ctx, domain.CreateIssueParams{Issue: seedIssue("bd-w1")}, "tester"); err != nil {
					t.Fatalf("CreateWisp: %v", err)
				}
			},
			want: []firedHook{{hooks.EventCreate, "bd-w1"}},
		},
		{
			name:   "update",
			parity: "HookFiringStore.UpdateIssue fires on_update",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if err := uw.IssueUseCase().UpdateIssue(ctx, "bd-1", map[string]any{"title": "new"}, "tester"); err != nil {
					t.Fatalf("UpdateIssue: %v", err)
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}},
		},
		{
			name:   "guarded update",
			parity: "hookIssueOperations.Update fires on_update",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.IssueUseCase().ApplyUpdate(ctx, "bd-1", domain.UpdateSpec{Fields: map[string]any{"title": "new"}}, "tester"); err != nil {
					t.Fatalf("ApplyUpdate: %v", err)
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}},
		},
		{
			name:   "claim",
			parity: "hookIssueClaimer fires on_update",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.IssueUseCase().ClaimIssue(ctx, "bd-1", "tester"); err != nil {
					t.Fatalf("ClaimIssue: %v", err)
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}},
		},
		{
			name:   "idempotent claim",
			parity: "an already-held claim grants no lease and writes nothing",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				for i := 0; i < 2; i++ {
					if _, err := uw.IssueUseCase().ClaimIssue(ctx, "bd-1", "tester"); err != nil {
						t.Fatalf("ClaimIssue: %v", err)
					}
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}},
		},
		{
			name:   "close",
			parity: "HookFiringStore.CloseIssueChecked fires on_close",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.IssueUseCase().CloseIssueChecked(ctx, "bd-1", domain.CloseIssueParams{}, "tester", false); err != nil {
					t.Fatalf("CloseIssueChecked: %v", err)
				}
			},
			want: []firedHook{{hooks.EventClose, "bd-1"}},
		},
		{
			// The one predicate that reads backwards at first glance, and the
			// reason it is spelled out: HookFiringStore.CloseIssueChecked fires
			// on_close for the idempotent no-op too ("this includes the
			// idempotent no-op when the issue was already closed"). A re-close
			// answers "it is closed", and a script reconciling on that answer
			// must not be told only sometimes.
			//
			// THE BATCH COMPOSITIONS DISAGREE WITH THIS ONE, deliberately and
			// as of ga-2yaqp.1 — see the two rows below, which are the same
			// verb reached through closeBatchItem. This SINGLE close keeps the
			// legacy parity; the batch verbs gate on Changed. The divergence is
			// pinned on both sides here rather than left to be discovered.
			name:   "a re-close still reports the close",
			parity: "HookFiringStore.CloseIssueChecked fires on_close on success, unchanged rows included",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				for i := 0; i < 2; i++ {
					if _, err := uw.IssueUseCase().CloseIssueChecked(ctx, "bd-1", domain.CloseIssueParams{}, "tester", false); err != nil {
						t.Fatalf("CloseIssueChecked: %v", err)
					}
				}
			},
			want: []firedHook{{hooks.EventClose, "bd-1"}, {hooks.EventClose, "bd-1"}},
		},
		{
			// The batch half of the row above, and the reason this plumbing
			// needed its own fix: closeBatchItem reaches the SAME recording
			// verb, so before ga-2yaqp.1 a teardown replayed against an
			// already-closed convoy ran the workspace's on_close script once
			// per item on every pass. Proxied mode — which is what `bd serve`
			// runs — is this plumbing, so that was the user-visible symptom.
			name:   "a batch re-close reports the close once",
			parity: "hookBatchCloser fires per outcome whose Changed is set, so the second pass announces nothing",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				for i := 0; i < 2; i++ {
					closeThroughBatch(ctx, t, uw, "bd-1")
				}
			},
			want: []firedHook{{hooks.EventClose, "bd-1"}},
		},
		{
			// The discriminating row: a replay where one item was still open.
			// Only that item is a script's business, and a wrapper that
			// announced the whole pass would be indistinguishable from one that
			// announced the right item on the rows above.
			name:   "a batch pass announces only the close that landed",
			parity: "hookBatchCloser's mixed re-close row: the landed item fires, the idempotent one does not",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{Issue: seedIssue("bd-2")}, "tester"); err != nil {
					t.Fatalf("CreateIssue: %v", err)
				}
				closeThroughBatch(ctx, t, uw, "bd-1")
				// The replayed pass: bd-1 is already closed, bd-2 is not.
				closeThroughBatch(ctx, t, uw, "bd-1", "bd-2")
			},
			want: []firedHook{
				{hooks.EventCreate, "bd-2"},
				{hooks.EventClose, "bd-1"},
				{hooks.EventClose, "bd-2"},
			},
		},
		{
			// The far end of a REVERSE edge: the create wrote an edge leaving
			// the EXISTING issue, so that issue's graph changed and its
			// watchers hear about it. Nothing in the new issue's own records
			// mentions the edge.
			name:   "create with a reverse edge tells the far end",
			parity: "CreatePublicCreateDependencies swaps source/target, and dependencyHookEvents fires on_update for the source",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				params := domain.CreateIssueParams{
					Issue: seedIssue("bd-2"),
					Dependencies: []domain.DependencySpec{
						{Type: types.DepBlocks, TargetID: "bd-1", SwapDirection: true},
					},
				}
				if _, err := uw.IssueUseCase().CreateIssue(ctx, params, "tester"); err != nil {
					t.Fatalf("CreateIssue: %v", err)
				}
			},
			// Only the far end follows: the reverse edge leaves bd-1, so bd-2
			// is not a source of anything.
			want: []firedHook{{hooks.EventCreate, "bd-2"}, {hooks.EventUpdate, "bd-1"}},
		},
		{
			// A FORWARD edge leaves the NEW issue, and the created row is a
			// source like any other: the create event carries the row, the
			// update that follows carries its graph. Two edges leaving it are
			// still one update (the multiplicity note in the file header).
			name:   "create with forward edges reports the create and one update for the new row",
			parity: "CompleteIssueOperationCreate follows the create with dependencyHookEvents over the request's edges",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				params := domain.CreateIssueParams{
					Issue:        seedIssue("bd-2"),
					ParentID:     "bd-1",
					Dependencies: []domain.DependencySpec{{Type: types.DepBlocks, TargetID: "bd-1"}},
				}
				if _, err := uw.IssueUseCase().CreateIssue(ctx, params, "tester"); err != nil {
					t.Fatalf("CreateIssue: %v", err)
				}
			},
			want: []firedHook{{hooks.EventCreate, "bd-2"}, {hooks.EventUpdate, "bd-2"}},
		},
		{
			name:   "reopen",
			parity: "hookIssueOperations.Reopen fires on_update for a reopen that changed something",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.IssueUseCase().ReopenIssue(ctx, "bd-1", domain.ReopenIssueParams{}, "tester"); err != nil {
					t.Fatalf("ReopenIssue: %v", err)
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}},
		},
		{
			// A plan writes its edges beneath the use-case seam (depRepo.Insert
			// inside applyGraph), so nothing about them reaches the recorder on
			// its own. The sources come off the PLAN: the child of a parent
			// link, and the from-side of an explicit edge — which here is an
			// issue the plan did not create.
			name:   "graph apply reports its nodes and its edge sources",
			parity: "the embedded graph apply writes the same edges through the store, whose decorator fires a dependency update per source",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				plan := domain.GraphPlan{
					Nodes: []domain.GraphNode{
						{Key: "root", Issue: seedIssue("bd-10")},
						{Key: "child", Issue: seedIssue("bd-11"), ParentKey: "root"},
					},
					// bd-1 exists already: the plan rewrites ITS graph.
					Edges: []domain.GraphEdge{
						{FromID: "bd-1", ToKey: "root", Type: types.DepBlocks},
					},
				}
				if _, err := uw.IssueUseCase().ApplyIssueGraph(ctx, plan, "tester"); err != nil {
					t.Fatalf("ApplyIssueGraph: %v", err)
				}
			},
			want: []firedHook{
				{hooks.EventCreate, "bd-10"},
				{hooks.EventCreate, "bd-11"},
				{hooks.EventUpdate, "bd-11"},
				{hooks.EventUpdate, "bd-1"},
			},
		},
		{
			name:   "delete",
			parity: "hook_deleter.go recurses unwrapped: a deletion has no event to fire",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.IssueUseCase().DeleteIssue(ctx, "bd-1", "tester"); err != nil {
					t.Fatalf("DeleteIssue: %v", err)
				}
			},
			want: nil,
		},
		{
			name:   "unclaim and heartbeat",
			parity: "HookFiringStore overrides neither UnclaimIssue nor HeartbeatIssue",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if err := uw.IssueUseCase().Unclaim(ctx, "bd-1", "tester", false); err != nil {
					t.Fatalf("Unclaim: %v", err)
				}
				if err := uw.IssueUseCase().Heartbeat(ctx, "bd-1", "tester"); err != nil {
					t.Fatalf("Heartbeat: %v", err)
				}
			},
			want: nil,
		},
		{
			name:   "dependency added",
			parity: "HookFiringStore.AddDependency fires on_update for the edge's source",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				dep := &types.Dependency{IssueID: "bd-1", DependsOnID: "bd-9", Type: types.DepBlocks}
				if err := uw.DependencyUseCase().AddDependency(ctx, dep, "tester"); err != nil {
					t.Fatalf("AddDependency: %v", err)
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}},
		},
		{
			name:   "dependency removed",
			parity: "HookFiringStore.RemoveDependency fires on_update for the edge's source",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if err := uw.DependencyUseCase().RemoveDependency(ctx, "bd-1", "bd-9", "tester"); err != nil {
					t.Fatalf("RemoveDependency: %v", err)
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}},
		},
		{
			name:   "label written",
			parity: "HookFiringStore.AddLabel fires on_update",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if err := uw.LabelUseCase().AddLabel(ctx, "bd-1", "urgent", "tester"); err != nil {
					t.Fatalf("AddLabel: %v", err)
				}
				if err := uw.LabelUseCase().SetLabels(ctx, "bd-1", []string{"urgent"}, "tester"); err != nil {
					t.Fatalf("SetLabels: %v", err)
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}, {hooks.EventUpdate, "bd-1"}},
		},
		{
			name:   "comment added",
			parity: "HookFiringStore.AddIssueComment fires on_update; there is no on_comment",
			run: func(ctx context.Context, t *testing.T, uw UnitOfWork) {
				if _, err := uw.CommentUseCase().AddCommentToIssue(ctx, "bd-1", "tester", "hi"); err != nil {
					t.Fatalf("AddCommentToIssue: %v", err)
				}
			},
			want: []firedHook{{hooks.EventUpdate, "bd-1"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newNotifyFixture(t, seedIssue("bd-1"))
			ctx := context.Background()
			uw := f.newUOW(t)
			test.run(ctx, t, uw)
			if err := uw.Commit(ctx, "bd: test"); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			assertFired(t, f.runner.events(), test.want)
		})
	}
}

// TestNotifyingUOWIgnoresAPreconditionOnlyUpdate covers the call `bd close
// --expect-version` and `bd reopen --expect-version` make before the operation
// they guard: an UpdateSpec carrying only expectations writes nothing, and the
// DoltStorage plumbing passes the same precondition into the close and fires
// the close hook alone.
func TestNotifyingUOWIgnoresAPreconditionOnlyUpdate(t *testing.T) {
	version := int64(1)
	assignee := "alice"
	status := "open"
	for _, test := range []struct {
		name string
		spec domain.UpdateSpec
		want []firedHook
	}{
		{"expected version alone", domain.UpdateSpec{ExpectedVersion: &version}, nil},
		{"every expectation", domain.UpdateSpec{ExpectedVersion: &version, ExpectedAssignee: &assignee, ExpectedStatus: &status}, nil},
		{"an empty field map writes nothing", domain.UpdateSpec{Fields: map[string]any{}}, nil},
		{"a guarded field write is still a write", domain.UpdateSpec{
			ExpectedVersion: &version,
			Fields:          map[string]any{"title": "new"},
		}, []firedHook{{hooks.EventUpdate, "bd-1"}}},
		{"a guarded claim is still a write", domain.UpdateSpec{ExpectedVersion: &version, Claim: true},
			[]firedHook{{hooks.EventUpdate, "bd-1"}}},
		{"a label edit is a write", domain.UpdateSpec{AddLabels: []string{"urgent"}},
			[]firedHook{{hooks.EventUpdate, "bd-1"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newNotifyFixture(t, seedIssue("bd-1"))
			ctx := context.Background()
			uw := f.newUOW(t)
			if _, err := uw.IssueUseCase().ApplyUpdate(ctx, "bd-1", test.spec, "tester"); err != nil {
				t.Fatalf("ApplyUpdate: %v", err)
			}
			if err := uw.Commit(ctx, "bd: test"); err != nil {
				t.Fatalf("Commit: %v", err)
			}
			assertFired(t, f.runner.events(), test.want)
		})
	}
}

// TestNotifyingUOWBuffersItsOwnSnapshot pins the clone. The runner marshals the
// issue on its own goroutine while the caller goes on editing the value it was
// handed — `bd update` and `bd reopen` both strip fields off theirs — so a
// buffered snapshot that aliased the caller's issue would be a data race and a
// wrong payload.
func TestNotifyingUOWBuffersItsOwnSnapshot(t *testing.T) {
	f := newNotifyFixture(t, seedIssue("bd-1"))
	ctx := context.Background()
	uw := f.newUOW(t)

	updated, err := uw.IssueUseCase().ApplyUpdate(ctx, "bd-1", domain.UpdateSpec{Fields: map[string]any{"title": "new"}}, "tester")
	if err != nil {
		t.Fatalf("ApplyUpdate: %v", err)
	}
	updated.Title = "edited after the call"

	if err := uw.Commit(ctx, "bd: test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(f.runner.issues) != 1 {
		t.Fatalf("fired %d hooks, want 1", len(f.runner.issues))
	}
	if got := f.runner.issues[0].Title; got != "bd-1" {
		t.Fatalf("buffered snapshot title = %q, want the title at mutation time — the buffer aliases the caller's issue", got)
	}
}

// TestNotifyingUOWSnapshotCarriesLabels pins the other half of the payload a
// hook script is handed on the DoltStorage plumbing: the issue's LABELS. A
// script that routes on a label — the common shape — reads an unlabeled issue
// as an unrouted one, so a payload without them is worse than no hook.
func TestNotifyingUOWSnapshotCarriesLabels(t *testing.T) {
	f := newNotifyFixture(t, seedIssue("bd-1"))
	ctx := context.Background()
	uw := f.newUOW(t)

	if err := uw.LabelUseCase().AddLabel(ctx, "bd-1", "urgent", "tester"); err != nil {
		t.Fatalf("AddLabel: %v", err)
	}
	if err := uw.Commit(ctx, "bd: test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(f.runner.issues) != 1 {
		t.Fatalf("fired %d hooks, want 1", len(f.runner.issues))
	}
	if got := f.runner.issues[0].Labels; len(got) != 1 || got[0] != "urgent" {
		t.Fatalf("buffered snapshot labels = %v, want [urgent]", got)
	}
}

// TestNotifyingUOWDependencySnapshotCarriesTheEdges pins the payload the
// DoltStorage plumbing hands a script for an edge change: the issue WITH its
// dependency records, so a hook sees the graph the edit produced.
func TestNotifyingUOWDependencySnapshotCarriesTheEdges(t *testing.T) {
	f := newNotifyFixture(t, seedIssue("bd-1"))
	ctx := context.Background()
	uw := f.newUOW(t)

	dep := &types.Dependency{IssueID: "bd-1", DependsOnID: "bd-9", Type: types.DepBlocks}
	if err := uw.DependencyUseCase().AddDependency(ctx, dep, "tester"); err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if err := uw.Commit(ctx, "bd: test"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(f.runner.issues) != 1 {
		t.Fatalf("fired %d hooks, want 1", len(f.runner.issues))
	}
	deps := f.runner.issues[0].Dependencies
	if len(deps) != 1 || deps[0].DependsOnID != "bd-9" {
		t.Fatalf("dependency snapshot carried %+v, want the edge the call wrote", deps)
	}
}

// TestHookEventForOp states the mapping in one place, including the operation
// that maps to nothing.
func TestHookEventForOp(t *testing.T) {
	for _, test := range []struct {
		op    string
		event string
		ok    bool
	}{
		{opCreate, hooks.EventCreate, true},
		{opUpdate, hooks.EventUpdate, true},
		{opDepAdd, hooks.EventUpdate, true},
		{opDepRemove, hooks.EventUpdate, true},
		{opClose, hooks.EventClose, true},
		{"delete", "", false},
		{"", "", false},
	} {
		event, ok := hookEventForOp(test.op)
		if event != test.event || ok != test.ok {
			t.Errorf("hookEventForOp(%q) = (%q, %v), want (%q, %v)", test.op, event, ok, test.event, test.ok)
		}
	}
}

func assertFired(t *testing.T, got, want []firedHook) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("fired %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fired %v, want %v", got, want)
		}
	}
}

// TestNotifyingUOWBatchApplierAnnouncesOnlyLandedCloses is the BatchApplier's
// half of the batch firing rule the parity table pins for closeBatchItem. Both
// compositions reach the same recording close verb, so both had the same defect
// and both need their own case: a fix applied to one of them passes the other's
// tests untouched (ga-2yaqp.1).
//
// The batch mixes a row that is already closed with one that is not, which is
// the shape a replayed teardown produces. Only the row that actually closed is
// a script's business.
func TestNotifyingUOWBatchApplierAnnouncesOnlyLandedCloses(t *testing.T) {
	closedAlready := seedIssue("bd-closed")
	closedAlready.Status = types.StatusClosed
	f := newNotifyFixture(t, closedAlready, seedIssue("bd-open"))

	applier, err := NewBatchApplier(f.provider)
	if err != nil {
		t.Fatalf("NewBatchApplier: %v", err)
	}
	if _, err := applier.ApplyBatch(context.Background(), publicops.ApplyBatchRequest{
		Actor: "tester",
		Items: []publicops.ApplyItem{
			{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{ID: "bd-closed"}}},
			{Kind: publicops.ItemClose, Close: &publicops.CloseItem{Target: publicops.Ref{ID: "bd-open"}}},
		},
	}); err != nil {
		t.Fatalf("ApplyBatch: %v", err)
	}

	assertFired(t, f.runner.events(), []firedHook{{hooks.EventClose, "bd-open"}})
}
