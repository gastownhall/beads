package issueops

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestCreateAssignedIDsReset pins the recorder the create replay and
// failure paths use (bd-t4h0n7, PR #6754): it records exactly the identity
// fields that were the caller's empties — the ones the create assigns — and
// Reset clears them, so a replay re-derives them and a definite failure
// leaves the caller's structs without phantom identities. Fields the caller
// supplied are never recorded and never cleared. An indeterminate commit is
// the one failure that keeps what the attempt assigned: it may have landed.
func TestCreateAssignedIDsReset(t *testing.T) {
	issue := &types.Issue{
		Title: "generated identities",
		Dependencies: []*types.Dependency{
			{DependsOnID: "bd-dep", Type: types.DepBlocks},
		},
		Comments: []*types.Comment{{Author: "tester", Text: "c"}},
	}

	reset := RecordCreateIdentities([]*types.Issue{issue})
	// The fields the create assigns, assigned as a first attempt would.
	issue.ID = "bd-minted"
	issue.Dependencies[0].IssueID = "bd-minted"
	issue.Comments[0].ID = "bd-comment"
	reset.Reset()
	if issue.ID != "" || issue.Dependencies[0].IssueID != "" || issue.Comments[0].ID != "" {
		t.Fatalf("after Reset: id=%q dep owner=%q comment=%q, want all empty",
			issue.ID, issue.Dependencies[0].IssueID, issue.Comments[0].ID)
	}

	// Supplied identities are never the create's to clear.
	supplied := &types.Issue{
		ID:     "bd-named",
		Dependencies: []*types.Dependency{{IssueID: "bd-named", DependsOnID: "bd-dep", Type: types.DepBlocks}},
		Comments:     []*types.Comment{{ID: "bd-named-comment", Author: "tester", Text: "c"}},
	}
	if reset := RecordCreateIdentities([]*types.Issue{supplied}); len(reset.assigned) != 0 {
		t.Fatalf("recorded %d fields for a fully-supplied create, want 0", len(reset.assigned))
	}

	// A definite failure resets; an indeterminate commit retains.
	definite := errors.Join(storage.ErrAlreadyExists, errors.New("insert refused"))
	retained := &types.Issue{Title: "t"}
	retainedReset := RecordCreateIdentities([]*types.Issue{retained})
	retained.ID = "bd-maybe"
	retainedReset.ResetOnFailedTransaction(definite)
	if retained.ID != "" {
		t.Fatalf("definite failure kept the minted id %q, want it reset", retained.ID)
	}
	retained.ID = "bd-maybe"
	retainedReset.ResetOnFailedTransaction(fmt.Errorf("commit write tx: %w", storage.ErrCommitIndeterminate))
	if retained.ID != "bd-maybe" {
		t.Fatalf("indeterminate commit reset the minted id, want it retained: it may have landed")
	}
	retained.ID = "bd-maybe"
	retainedReset.ResetOnFailedTransaction(nil)
	if retained.ID != "" {
		t.Fatalf("nil error (settled failure) kept the minted id %q, want it reset", retained.ID)
	}

	// Robustness: nil issues and nil aux entries record nothing and reset
	// nothing.
	var nilSafe *createIdentityReset = RecordCreateIdentities([]*types.Issue{nil, {ID: "bd-x"}})
	nilSafe.Reset()
	var nilRecorder *createIdentityReset
	nilRecorder.Reset()
	nilRecorder.ResetOnFailedTransaction(definite)
}
