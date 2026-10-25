package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

// The tests below pin the WIRING half of the GH#4995 exemption. The mechanism
// half — that WithImmediateVersionCommit defeats the route-wide deferral —
// is pinned in internal/storage/uow; what is pinned here is that each proxied
// dual whose direct twin is an explicit commit point actually hands that
// exemption to its transaction, and that the duals whose twin is NOT one do
// not. Without this, deleting explicitCommitPointContext from a dual is green
// in every lane that does not have a real proxied server.
//
// The seam is uow.RunTxResult's own first act: it asks the provider for a unit
// of work, passing the caller's context. A provider that records that context
// and then refuses observes exactly "the context this dual hands its
// transaction", with no server, no store and no fixture beyond the reads the
// dual makes to decide what to write.

var errStopBeforeProbeTx = errors.New("exemption wiring probe: stop before the transaction")

// exemptionProbeProvider serves readUOW to the first `reads` NewUOW calls (the
// dual's own read phase) and refuses the rest, recording every context it was
// handed. The refusal is not a serialization error, so RunTxResult treats it as
// permanent and makes exactly one NewUOW call per transaction.
type exemptionProbeProvider struct {
	readUOW uow.UnitOfWork
	reads   int
	ctxs    []context.Context
}

func (p *exemptionProbeProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	p.ctxs = append(p.ctxs, ctx)
	if len(p.ctxs) <= p.reads {
		return p.readUOW, nil
	}
	return nil, errStopBeforeProbeTx
}

func (p *exemptionProbeProvider) Close(context.Context) error { return nil }

// txCtxs is the contexts handed to RunTx*, in call order: everything after the
// leading read opens.
func (p *exemptionProbeProvider) txCtxs() []context.Context {
	if len(p.ctxs) <= p.reads {
		return nil
	}
	return p.ctxs[p.reads:]
}

// installExemptionProbe points the package's proxied provider at the probe for
// the duration of one test.
func installExemptionProbe(t *testing.T, p *exemptionProbeProvider) {
	t.Helper()
	prev := uowProvider
	uowProvider = p
	t.Cleanup(func() { uowProvider = prev })
}

// exemptionProbeUOW embeds the interface without implementing it: anything a
// dual's read phase needs beyond the three use cases below panics and names
// itself, rather than being silently satisfied.
type exemptionProbeUOW struct {
	uow.UnitOfWork
	issues domain.IssueUseCase
	labels domain.LabelUseCase
	deps   domain.DependencyUseCase
}

func (u exemptionProbeUOW) Close(context.Context)                       {}
func (u exemptionProbeUOW) IssueUseCase() domain.IssueUseCase           { return u.issues }
func (u exemptionProbeUOW) LabelUseCase() domain.LabelUseCase           { return u.labels }
func (u exemptionProbeUOW) DependencyUseCase() domain.DependencyUseCase { return u.deps }

// CommentUseCase is collected by workapi.NewUOWDetailSource when the read
// phase resolves an id; nothing on that path calls it.
func (u exemptionProbeUOW) CommentUseCase() domain.CommentUseCase { return nil }

type exemptionProbeIssues struct {
	domain.IssueUseCase
	byID map[string]*types.Issue
}

func (s exemptionProbeIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if issue, ok := s.byID[id]; ok && !issue.Ephemeral {
		return issue, nil
	}
	return nil, storage.ErrNotFound
}

func (s exemptionProbeIssues) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	if issue, ok := s.byID[id]; ok && issue.Ephemeral {
		return issue, nil
	}
	return nil, storage.ErrNotFound
}

func (s exemptionProbeIssues) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) (domain.SearchPage, error) {
	var page domain.SearchPage
	for _, id := range filter.IDs {
		if issue, ok := s.byID[id]; ok {
			page.Items = append(page.Items, issue)
		}
	}
	return page, nil
}

type exemptionProbeLabels struct {
	domain.LabelUseCase
}

func (exemptionProbeLabels) GetLabels(context.Context, string) ([]string, error)     { return nil, nil }
func (exemptionProbeLabels) GetWispLabels(context.Context, string) ([]string, error) { return nil, nil }

type exemptionProbeDeps struct {
	domain.DependencyUseCase
}

func (exemptionProbeDeps) ListWithIssueMetadata(context.Context, string, domain.DepListFilter) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}

func (exemptionProbeDeps) GetForIssueIDs(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, nil
}

func newExemptionProbeUOW(issues ...*types.Issue) uow.UnitOfWork {
	byID := make(map[string]*types.Issue, len(issues))
	for _, issue := range issues {
		byID[issue.ID] = issue
	}
	return exemptionProbeUOW{
		issues: exemptionProbeIssues{byID: byID},
		labels: exemptionProbeLabels{},
		deps:   exemptionProbeDeps{},
	}
}

// deferredRouteContext is what the proxied root pre-run hands every dispatch
// under dolt.auto-commit=batch/off.
func deferredRouteContext() context.Context {
	return issueops.WithDeferredVersionCommit(context.Background())
}

// TestProxiedDualsExemptTheirExplicitCommitPoints pins the duals that reach
// their transaction directly. Deleting explicitCommitPointContext from any of
// them reds its row here.
func TestProxiedDualsExemptTheirExplicitCommitPoints(t *testing.T) {
	cases := []struct {
		name string
		call func(context.Context)
	}{
		{
			name: "batch",
			call: func(ctx context.Context) {
				_, _ = runBatchProxiedServer(ctx, nil, "bd: batch probe")
			},
		},
		{
			name: "mol bond",
			call: func(ctx context.Context) {
				_ = runMolBondProxiedServer(ctx, molBondInput{argA: "bd-a", argB: "bd-b"})
			},
		},
		{
			name: "mol squash",
			call: func(ctx context.Context) {
				_ = runMolSquashProxiedServer(ctx, molSquashInput{moleculeArg: "bd-mol"})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &exemptionProbeProvider{}
			installExemptionProbe(t, probe)

			tc.call(deferredRouteContext())

			txCtxs := probe.txCtxs()
			if len(txCtxs) != 1 {
				t.Fatalf("%s: got %d transaction context(s), want 1 — the dual never reached RunTx*", tc.name, len(txCtxs))
			}
			if issueops.VersionCommitDeferred(txCtxs[0]) {
				t.Errorf("%s: the context handed to RunTx* still defers the version commit; "+
					"its direct twin is an explicit commit point, so this dual must clear the deferral "+
					"(doltServerTx.Commit blanks the message and mints nothing)", tc.name)
			}
		})
	}
}

// TestProxiedMolBurnExemptsOnlyTheWispHalf pins the split. bd mol burn is two
// commit classes on the direct route: wisps go through burnWisps -> `transact`
// (explicit), persistent molecules go through deleteBatch -> issueOpsContext,
// whose deferral dolt's deleter honors by returning before DOLT_COMMIT. A
// command-wide exemption mints one commit per persistent molecule under
// batch/off, where the identical direct-route command defers them all.
func TestProxiedMolBurnExemptsOnlyTheWispHalf(t *testing.T) {
	wisp := &types.Issue{ID: "bd-wisp1", Ephemeral: true}
	persistent := &types.Issue{ID: "bd-mol1"}

	probe := &exemptionProbeProvider{
		reads:   1, // runMolBurnProxiedServer's classification read
		readUOW: newExemptionProbeUOW(wisp, persistent),
	}
	installExemptionProbe(t, probe)

	// force=true skips the interactive confirmation; dryRun=false so both
	// halves reach their transaction.
	_ = runMolBurnProxiedServer(deferredRouteContext(), []string{wisp.ID, persistent.ID}, false, true)

	txCtxs := probe.txCtxs()
	if len(txCtxs) != 2 {
		t.Fatalf("got %d transaction context(s), want 2 (the wisp half then the persistent half); "+
			"the classification read did not produce one of each", len(txCtxs))
	}

	if issueops.VersionCommitDeferred(txCtxs[0]) {
		t.Errorf("wisp half: the context handed to RunTxResult still defers the version commit, " +
			"but its direct twin (burnWisps -> transact) commits whatever dolt.auto-commit says")
	}
	if !issueops.VersionCommitDeferred(txCtxs[1]) {
		t.Errorf("persistent half: the context handed to RunTxResult no longer defers the version commit, " +
			"so a proxied `bd mol burn` mints one Dolt commit per persistent molecule under " +
			"dolt.auto-commit=batch/off — its direct twin (deleteBatch -> issueOpsContext) defers them " +
			"all to `bd dolt commit`")
	}
}
