package uow

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// The three request completions this backend has to honour, over a stub unit of
// work: the plane restriction, the provenance label, and the one update that
// records no history entry. The claim's own CAS is proved against real Dolt
// elsewhere; what is unprovable there — that a wisp is never even PROBED, and
// that a polling re-claim mints nothing — is what these pin.

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

func (s *claimContractIssues) ApplyUpdate(ctx context.Context, id string, spec domain.UpdateSpec, actor string) (*types.Issue, error) {
	if spec.Claim && s.issue != nil {
		s.issue.Assignee, s.issue.Status = actor, types.StatusInProgress
	}
	if s.isWisp {
		return s.GetWisp(ctx, id)
	}
	return s.GetIssue(ctx, id)
}

func newClaimContract(t *testing.T, issue *types.Issue, isWisp bool) (publicops.Lifecycle, *claimContractUOW) {
	t.Helper()
	uw := &claimContractUOW{issues: &claimContractIssues{issue: issue, isWisp: isWisp}}
	operations, err := NewIssueOperations(providerFunc(func(context.Context) (UnitOfWork, error) { return uw, nil }))
	if err != nil {
		t.Fatalf("NewIssueOperations: %v", err)
	}
	return operations, uw
}

// providerFunc adapts one unit of work into a provider, so every attempt in a
// call sees the state the previous one left.
type providerFunc func(context.Context) (UnitOfWork, error)

func (f providerFunc) NewUOW(ctx context.Context) (UnitOfWork, error) { return f(ctx) }
func (f providerFunc) Close(context.Context) error                    { return nil }

func TestUpdateProvenanceNamesTheHistoryEntry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		provenance string
		want       string
	}{
		{"caller supplies one", "bd serve: claim bd-1 by alice", "bd serve: claim bd-1 by alice"},
		{"caller supplies none", "", "update issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operations, uw := newClaimContract(t, &types.Issue{ID: "bd-1", Status: types.StatusOpen}, false)
			result, err := operations.Update(context.Background(), publicops.UpdateRequest{
				Actor: "alice", IssueID: "bd-1", Claim: true, Provenance: tc.provenance,
			})
			if err != nil {
				t.Fatalf("Update: %v", err)
			}
			if !result.Changed {
				t.Error("a won claim reports no change")
			}
			if !slices.Equal(uw.commits, []string{tc.want}) {
				t.Errorf("history entries = %q, want exactly [%s]", uw.commits, tc.want)
			}
		})
	}
}

// Reopen answers to the same rule update does, and it needs its own coverage
// because this backend's default is the one that disagrees with the
// store-backed implementations: "reopen issue" here against "bd: reopen issue"
// there. A caller that spells a label gets the same entry from both.
//
// The close that sets each case up carries no label — CloseRequest has none —
// so it always records this backend's own default, which is asserted alongside.
func TestReopenProvenanceNamesTheHistoryEntry(t *testing.T) {
	for _, tc := range []struct {
		name       string
		provenance string
		wantReopen string
	}{
		{"caller supplies one", "bd: reopen bd-1", "bd: reopen bd-1"},
		{"caller supplies none", "", "reopen issue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			operations, uw := newClaimContract(t, &types.Issue{ID: "bd-1", Status: types.StatusOpen}, false)

			if _, err := operations.Close(context.Background(), publicops.CloseRequest{
				Actor: "alice", IssueID: "bd-1",
			}); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if _, err := operations.Reopen(context.Background(), publicops.ReopenRequest{
				Actor: "alice", IssueID: "bd-1", Provenance: tc.provenance,
			}); err != nil {
				t.Fatalf("Reopen: %v", err)
			}

			if want := []string{"close issue", tc.wantReopen}; !slices.Equal(uw.commits, want) {
				t.Errorf("history entries = %q, want %q", uw.commits, want)
			}
		})
	}
}

// A polling caller must not fill the history with entries for a claim it
// already holds: the CAS matched no row, so there is nothing to record.
func TestIdempotentClaimRecordsNoHistoryEntry(t *testing.T) {
	held := &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "alice"}
	operations, uw := newClaimContract(t, held, false)

	result, err := operations.Update(context.Background(), publicops.UpdateRequest{
		Actor: "alice", IssueID: "bd-1", Claim: true, Provenance: "bd serve: claim bd-1 by alice",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.Changed {
		t.Error("a re-claim by the holder reports a change")
	}
	if len(uw.commits) != 0 {
		t.Errorf("history entries = %q, want none for a claim that wrote nothing", uw.commits)
	}
}

// An update that carries a patch still records its entry even when the
// post-state compares equal: the row was written, whatever the comparison says.
func TestSameValueUpdateStillRecordsItsHistoryEntry(t *testing.T) {
	operations, uw := newClaimContract(t, &types.Issue{ID: "bd-1", Status: types.StatusOpen, Title: "same"}, false)

	if _, err := operations.Update(context.Background(), publicops.UpdateRequest{
		Actor:   "alice",
		IssueID: "bd-1",
		Patch:   publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: "same"}},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !slices.Equal(uw.commits, []string{"update issue"}) {
		t.Errorf("history entries = %q, want the default entry", uw.commits)
	}
}

func TestIssuePlaneOnlyRefusesAWispWithoutProbingIt(t *testing.T) {
	operations, uw := newClaimContract(t, &types.Issue{ID: "bd-w1", Status: types.StatusOpen, Ephemeral: true}, true)

	_, err := operations.Update(context.Background(), publicops.UpdateRequest{
		Actor: "alice", IssueID: "bd-w1", Claim: true, IssuePlaneOnly: true,
	})
	if !errors.Is(err, publicops.ErrNotFound) {
		t.Fatalf("Update error = %v, want ErrNotFound for a wisp id", err)
	}
	if uw.issues.wispReads != 0 {
		t.Errorf("wisp reads = %d, want 0: an issue-plane-only update never probes the other plane", uw.issues.wispReads)
	}
	if len(uw.commits) != 0 {
		t.Errorf("a refused update recorded %q", uw.commits)
	}
}

// The zero value keeps the both-plane resolve every caller gets today.
func TestBothPlaneResolveStaysTheDefault(t *testing.T) {
	operations, uw := newClaimContract(t, &types.Issue{ID: "bd-w1", Status: types.StatusOpen, Ephemeral: true}, true)

	result, err := operations.Update(context.Background(), publicops.UpdateRequest{
		Actor: "alice", IssueID: "bd-w1", Claim: true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.Issue == nil || result.Issue.Assignee != "alice" {
		t.Fatalf("Update result = %+v, want the claimed wisp", result.Issue)
	}
	if uw.issues.wispReads == 0 {
		t.Error("the default resolve never looked in the wisp plane")
	}
}
