package flatfile

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Oracle: issueops.ClaimReadyIssueInTx (internal/storage/issueops/claim.go)
// and the interface contract "returns (nil, nil) when nothing is ready"
// (sqlkit capabilities.go), which cmd/bd/ready.go relies on.

func TestClaimReadyIssueExhaustedReturnsNilNil(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	claimed, err := s.ClaimReadyIssue(ctx, types.WorkFilter{}, "worker")
	if err != nil {
		t.Fatalf("ClaimReadyIssue with no issues: err = %v, want nil", err)
	}
	if claimed != nil {
		t.Errorf("ClaimReadyIssue with no issues: claimed = %+v, want nil", claimed)
	}
}

func TestClaimReadyIssueForcesUnassignedPool(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// bd-1 is open but assigned to someone else: the reference's forced
	// Unassigned filter keeps it out of the pool entirely. bd-2 is claimable.
	assigned := &types.Issue{ID: "bd-1", Title: "taken", Status: types.StatusOpen, Priority: 1, IssueType: types.TypeTask, Assignee: "other"}
	free := &types.Issue{ID: "bd-2", Title: "free", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := s.CreateIssue(ctx, assigned, "a"); err != nil {
		t.Fatalf("CreateIssue bd-1: %v", err)
	}
	if err := s.CreateIssue(ctx, free, "a"); err != nil {
		t.Fatalf("CreateIssue bd-2: %v", err)
	}

	// A caller Limit of 1 must not truncate the retry pool (the reference
	// zeroes Limit before computing ready work).
	claimed, err := s.ClaimReadyIssue(ctx, types.WorkFilter{Limit: 1}, "worker")
	if err != nil {
		t.Fatalf("ClaimReadyIssue: %v", err)
	}
	if claimed == nil || claimed.ID != "bd-2" {
		t.Fatalf("claimed = %+v, want bd-2", claimed)
	}
	if claimed.Assignee != "worker" || claimed.Status != types.StatusInProgress {
		t.Errorf("claimed issue = assignee %q status %s, want worker/in_progress", claimed.Assignee, claimed.Status)
	}

	// bd-1 must be untouched.
	got, err := s.GetIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("GetIssue bd-1: %v", err)
	}
	if got.Assignee != "other" || got.Status != types.StatusOpen {
		t.Errorf("bd-1 = assignee %q status %s, want other/open", got.Assignee, got.Status)
	}

	// Pool now exhausted (bd-1 is assigned, bd-2 in_progress): (nil, nil).
	claimed, err = s.ClaimReadyIssue(ctx, types.WorkFilter{}, "worker")
	if err != nil || claimed != nil {
		t.Errorf("exhausted pool = (%+v, %v), want (nil, nil)", claimed, err)
	}
}
