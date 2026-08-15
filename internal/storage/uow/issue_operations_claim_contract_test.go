package uow

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// Lifecycle now runs ExecuteUpdate / ExecuteClose / ExecuteReopen, so the
// claim CAS, plane restriction, and Changed rule live in that shared body.
// What remains here is the commit-message helper this adapter still owns,
// and the batch path's own claimChanged expression (uowApplyRun.runUpdate).

type claimContractUOW struct {
	UnitOfWork
	issues  *claimContractIssues
	commits []string
}

func (u *claimContractUOW) Close(context.Context) {}

func (u *claimContractUOW) Commit(_ context.Context, message string) error {
	u.commits = append(u.commits, message)
	return nil
}

func (u *claimContractUOW) IssueUseCase() domain.IssueUseCase { return u.issues }
func (u *claimContractUOW) LabelUseCase() domain.LabelUseCase { return claimContractLabels{} }
func (u *claimContractUOW) DependencyUseCase() domain.DependencyUseCase {
	return claimContractDeps{}
}

type claimContractLabels struct{ domain.LabelUseCase }

func (claimContractLabels) GetLabels(context.Context, string) ([]string, error) { return nil, nil }
func (claimContractLabels) GetWispLabels(context.Context, string) ([]string, error) {
	return nil, nil
}

type claimContractDeps struct{ domain.DependencyUseCase }

func (claimContractDeps) GetIssueDependencyRecords(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, nil
}

func (claimContractDeps) GetWispDependencyRecords(context.Context, []string) (map[string][]*types.Dependency, error) {
	return nil, nil
}

// claimContractIssues answers from one row, in whichever plane it was seeded
// into, and applies the claim the way the real use case does.
type claimContractIssues struct {
	domain.IssueUseCase
	issue     *types.Issue
	isWisp    bool
	wispReads int
}

func (s *claimContractIssues) GetIssue(_ context.Context, id string) (*types.Issue, error) {
	if s.isWisp || s.issue == nil || s.issue.ID != id {
		return nil, sql.ErrNoRows
	}
	clone := *s.issue
	return &clone, nil
}

func (s *claimContractIssues) GetWisp(_ context.Context, id string) (*types.Issue, error) {
	s.wispReads++
	if !s.isWisp || s.issue == nil || s.issue.ID != id {
		return nil, sql.ErrNoRows
	}
	clone := *s.issue
	return &clone, nil
}

func (s *claimContractIssues) CloseIssueChecked(ctx context.Context, id string, _ domain.CloseIssueParams, _ string, _ bool) (domain.CloseIssueResult, error) {
	if s.issue == nil || s.issue.ID != id {
		return domain.CloseIssueResult{}, sql.ErrNoRows
	}
	closed := s.issue.Status == types.StatusClosed
	s.issue.Status = types.StatusClosed
	issue, err := s.GetIssue(ctx, id)
	return domain.CloseIssueResult{Issue: issue, Closed: !closed}, err
}

func (s *claimContractIssues) ReopenIssue(ctx context.Context, id string, _ domain.ReopenIssueParams, _ string) (domain.ReopenIssueResult, error) {
	if s.issue == nil || s.issue.ID != id {
		return domain.ReopenIssueResult{}, sql.ErrNoRows
	}
	reopened := s.issue.Status == types.StatusClosed
	s.issue.Status = types.StatusOpen
	issue, err := s.GetIssue(ctx, id)
	return domain.ReopenIssueResult{Issue: issue, Reopened: reopened}, err
}

// ApplyUpdate mirrors the real CAS's idempotent-preserves-spelling contract
// (domain/db's Claim, itself required to stay in lockstep with
// issueops.ClaimIssueInTx, ga-v2k49): a holder re-claiming under a respelled
// identity wins the CAS without writing, so the stored spelling survives
// unchanged. Only a genuine transition (not already in_progress under a
// matching identity) rewrites assignee/status — a fake that overwrote
// unconditionally would make every cross-spelling reclaim look like a real
// mutation via semanticIssueEqual, for a reason that has nothing to do with
// claimChanged itself.
func (s *claimContractIssues) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	if spec.Claim && s.issue != nil {
		alreadyHeld := s.issue.Status == types.StatusInProgress && storageissueops.ActorMatches(s.issue.Assignee, actor)
		if !alreadyHeld {
			s.issue.Assignee, s.issue.Status = actor, types.StatusInProgress
		}
	}
	if s.isWisp {
		return s.GetWisp(ctx, id)
	}
	return s.GetIssue(ctx, id)
}

func TestUpdateHistoryEntryNamesProvenance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		provenance string
		changed    bool
		claim      bool
		want       string
	}{
		{"caller supplies one", "bd serve: claim bd-1 by alice", true, true, "bd serve: claim bd-1 by alice"},
		{"caller supplies none", "", true, true, "update issue"},
		{"idempotent claim records none", "bd serve: claim bd-1 by alice", false, true, ""},
		{"same-value patch still records", "", false, false, "update issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := publicops.UpdateRequest{Claim: tc.claim, Provenance: tc.provenance}
			if !tc.claim {
				request.Patch = publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "same"}}
			}
			if got := updateHistoryEntry(request, tc.changed); got != tc.want {
				t.Fatalf("updateHistoryEntry() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReopenHistoryEntryNamesProvenance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		provenance string
		want       string
	}{
		{"caller supplies one", "bd: reopen bd-1", "bd: reopen bd-1"},
		{"caller supplies none", "", "reopen issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := storageissueops.HistoryEntry(tc.provenance, "reopen issue")
			if got != tc.want {
				t.Fatalf("HistoryEntry() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBatchRunUpdateIdempotentClaimAcrossSpellingReportsNoChange pins the
// ga-v2k49 contract against uowApplyRun.runUpdate — the batch path's own
// claimChanged expression. Lifecycle Update now shares ExecuteUpdate;
// this leftover fork still needs its own pin.
func TestBatchRunUpdateIdempotentClaimAcrossSpellingReportsNoChange(t *testing.T) {
	held := &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "gastown__mayor"}
	run := &uowApplyRun{uw: &claimContractUOW{issues: &claimContractIssues{issue: held}}}

	result, err := run.runUpdate(context.Background(), publicops.UpdateRequest{
		Actor: "gastown.mayor", IssueID: "bd-1", Claim: true,
	})
	if err != nil {
		t.Fatalf("runUpdate: %v, want success (same identity, different separator spelling)", err)
	}
	if result.Changed {
		t.Error("a cross-spelling re-claim by the holder reports a change")
	}
}

// TestBatchRunUpdateClaimByGenuinelyDifferentIdentityReportsAChange is the
// not-a-no-op control for the batch-path pin above.
func TestBatchRunUpdateClaimByGenuinelyDifferentIdentityReportsAChange(t *testing.T) {
	held := &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "gastown.mayor"}
	run := &uowApplyRun{uw: &claimContractUOW{issues: &claimContractIssues{issue: held}}}

	result, err := run.runUpdate(context.Background(), publicops.UpdateRequest{
		Actor: "gastown.dog-3", IssueID: "bd-1", Claim: true,
	})
	if err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if !result.Changed {
		t.Error("a claim transferring to a genuinely different identity reports no change")
	}
}
