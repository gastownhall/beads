package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The proxied claim path is a day-one caller of issueops.Lifecycle, and these
// pin what "on the contract" means here: the request it makes, the roles it
// reaches them through, and the fact that it opens no unit of work of its own.
// The claim's behaviour against real Dolt is the integration suite's job.

type claimRoleProvider struct {
	lifecycle *recordingLifecycle
	details   *types.IssueDetails
	readErr   error
	uows      int
}

func (p *claimRoleProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	p.uows++
	return nil, errors.New("the claim path opened a unit of work of its own")
}

func (p *claimRoleProvider) Close(context.Context) error { return nil }

func (p *claimRoleProvider) IssueLifecycle() (issueops.Lifecycle, error) { return p.lifecycle, nil }

func (p *claimRoleProvider) IssueReader() (issueops.Reader, error) {
	return &stubIssueReader{details: p.details, err: p.readErr}, nil
}

type recordingLifecycle struct {
	issueops.Lifecycle
	request issueops.UpdateRequest
	result  issueops.UpdateResult
	err     error
}

func (l *recordingLifecycle) Update(_ context.Context, request issueops.UpdateRequest) (issueops.UpdateResult, error) {
	l.request = request
	return l.result, l.err
}

type stubIssueReader struct {
	issueops.Reader
	details *types.IssueDetails
	err     error
}

func (r *stubIssueReader) Get(context.Context, issueops.GetRequest) (*types.IssueDetails, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.details, nil
}

func withClaimRoleProvider(t *testing.T, p *claimRoleProvider) {
	t.Helper()
	oldProvider, oldHookRunner := uowProvider, hookRunner
	uowProvider = p
	hookRunner = hooks.NewRunner(t.TempDir()) // no hooks installed: RunSync no-ops
	t.Cleanup(func() {
		uowProvider = oldProvider
		hookRunner = oldHookRunner
	})
}

func claimRoleFixture(t *testing.T, before *types.Issue, result issueops.UpdateResult, err error) *claimRoleProvider {
	t.Helper()
	p := &claimRoleProvider{
		lifecycle: &recordingLifecycle{result: result, err: err},
		details:   &types.IssueDetails{Issue: *before},
	}
	withClaimRoleProvider(t, p)
	return p
}

func TestProxiedClaimRunsOnTheLifecycleContract(t *testing.T) {
	before := &types.Issue{ID: "bd-1", Status: types.StatusOpen}
	claimed := &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "agent"}
	p := claimRoleFixture(t, before, issueops.UpdateResult{Issue: claimed, Changed: true}, nil)

	oldActor := actor
	actor = "agent"
	t.Cleanup(func() { actor = oldActor })

	got, fail, err := applyUpdateProxiedOne(context.Background(), "bd-1", &updateInput{claim: true, fields: map[string]any{}})
	if err != nil || fail != nil {
		t.Fatalf("applyUpdateProxiedOne: err = %v, fail = %+v", err, fail)
	}
	if got == nil || got.Assignee != "agent" {
		t.Fatalf("updated issue = %+v, want the claimed row", got)
	}

	req := p.lifecycle.request
	if !req.Claim || req.Actor != "agent" || req.IssueID != "bd-1" {
		t.Errorf("request = %+v, want a claim of bd-1 by agent", req)
	}
	// The commit message this path has always written, carried on the request
	// instead of being spelled at the commit site.
	if req.Provenance != "bd: update bd-1" {
		t.Errorf("Provenance = %q, want the message the proxied update has always written", req.Provenance)
	}
	// A CLI claim resolves either plane; only the HTTP surface restricts it.
	if req.IssuePlaneOnly {
		t.Error("the CLI claim restricted the plane; `bd update --claim` has always resolved a wisp id")
	}
	if req.ExpectedAssignee != nil || req.ExpectedStatus != nil {
		t.Errorf("request carries guards a claim may not combine with: %+v", req)
	}
	if p.uows != 0 {
		t.Errorf("the claim path opened %d units of work; the contract owns the transaction", p.uows)
	}
}

// A lost CAS keeps its copy, its per-id failure and its exit code: the batch
// must not turn a refusal into a success, and a claim conflict is not a guard
// mismatch (exit 1, never 13).
func TestProxiedClaimConflictStaysAPerIDFailure(t *testing.T) {
	before := &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "bob"}
	conflict := &issueops.ClaimConflictError{
		IssueID:  "bd-1",
		Assignee: "bob",
		Status:   types.StatusInProgress,
		// Composed the way both real producers compose it. The fixture must
		// carry the prose, not a bare sentinel: ClaimConflictError.Error() is
		// a passthrough, so a bare sentinel here would assert against a
		// message no producer can emit.
		Err: fmt.Errorf("%w%s%s", storage.ErrAlreadyClaimed, storage.ClaimedByFragment, "bob"),
	}
	claimRoleFixture(t, before, issueops.UpdateResult{}, conflict)

	var (
		got  *types.Issue
		fail *updateIDFailure
		err  error
	)
	stderr := captureStderrDuring(t, func() {
		got, fail, err = applyUpdateProxiedOne(context.Background(), "bd-1", &updateInput{claim: true, fields: map[string]any{}})
	})
	if err != nil {
		t.Fatalf("applyUpdateProxiedOne returned a hard error: %v", err)
	}
	if got != nil || fail == nil {
		t.Fatalf("issue = %+v, fail = %+v: a lost claim is a per-id failure", got, fail)
	}
	if fail.GuardMismatch {
		t.Error("a claim conflict must not exit 13; retrying the same claim is not pointless")
	}
	if want := "Error claiming bd-1: issue already claimed by bob"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
}

// SIGINT cancels bd's root context mid-batch. That is not a verdict on the
// issue in flight: the loop aborts, rather than recording one "context
// canceled" failure for this id and then the same failure for every id left.
func TestProxiedClaimAbortsTheBatchOnCancellation(t *testing.T) {
	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cancellation.Error(), func(t *testing.T) {
			before := &types.Issue{ID: "bd-1", Status: types.StatusOpen}
			claimRoleFixture(t, before, issueops.UpdateResult{}, fmt.Errorf("update bd-1: %w", cancellation))

			got, fail, err := applyUpdateProxiedOne(context.Background(), "bd-1", &updateInput{claim: true, fields: map[string]any{}})
			if !errors.Is(err, cancellation) {
				t.Fatalf("err = %v, want %v returned so the batch aborts", err, cancellation)
			}
			if got != nil || fail != nil {
				t.Errorf("issue = %+v, fail = %+v: cancellation is not a per-id verdict", got, fail)
			}
		})
	}
}

func TestProxiedClaimReportsAMissingIssue(t *testing.T) {
	p := &claimRoleProvider{
		lifecycle: &recordingLifecycle{},
		readErr:   issueops.ErrNotFound,
	}
	withClaimRoleProvider(t, p)

	var fail *updateIDFailure
	stderr := captureStderrDuring(t, func() {
		_, fail, _ = applyUpdateProxiedOne(context.Background(), "bd-404", &updateInput{claim: true, fields: map[string]any{}})
	})
	if fail == nil || fail.Error != "issue not found" {
		t.Fatalf("fail = %+v, want the not-found verdict", fail)
	}
	if !strings.Contains(stderr, "Issue bd-404 not found") {
		t.Errorf("stderr = %q, want the not-found line", stderr)
	}
	if p.lifecycle.request.IssueID != "" {
		t.Error("a missing issue still reached the contract")
	}
}
