package issueops

import (
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestAuthorizeAssigneeTransferWithPools pins every branch of the fence
// predicate. Both backends that enforce the fence evaluate this one function,
// so its branch semantics are the whole policy — the DBTX wrapper adds only
// the config read.
func TestAuthorizeAssigneeTransferWithPools(t *testing.T) {
	held := func() *types.Issue {
		return &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "holder"}
	}
	transfer := func(mutate func(*publicops.UpdateRequest)) publicops.UpdateRequest {
		request := publicops.UpdateRequest{
			Actor:   "rival",
			IssueID: "bd-1",
			Patch:   publicops.IssuePatch{Assignee: publicops.Field[string]{Set: true, Value: "rival"}},
		}
		if mutate != nil {
			mutate(&request)
		}
		return request
	}
	holder := "holder"

	cases := []struct {
		name    string
		before  *types.Issue
		request publicops.UpdateRequest
		pools   []string
		refuse  bool
	}{
		{
			name:    "unset assignee patch",
			before:  held(),
			request: transfer(func(r *publicops.UpdateRequest) { r.Patch.Assignee = publicops.Field[string]{} }),
		},
		{
			name:    "reasserts the current assignee",
			before:  held(),
			request: transfer(func(r *publicops.UpdateRequest) { r.Patch.Assignee.Value = "holder" }),
		},
		{
			name:    "expected assignee compare-and-set",
			before:  held(),
			request: transfer(func(r *publicops.UpdateRequest) { r.ExpectedAssignee = &holder }),
		},
		{
			name:    "forced transfer",
			before:  held(),
			request: transfer(func(r *publicops.UpdateRequest) { r.ForceAssigneeTransfer = true }),
		},
		{
			name:    "holder is not in progress",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusOpen, Assignee: "holder"},
			request: transfer(nil),
		},
		{
			name:    "unassigned",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusInProgress},
			request: transfer(nil),
		},
		{
			name:    "actor already holds it",
			before:  held(),
			request: transfer(func(r *publicops.UpdateRequest) { r.Actor = "holder" }),
		},
		{
			name:    "holder is a configured pool alias",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "crew"},
			request: transfer(nil),
			pools:   []string{"other", "crew"},
		},
		{
			name:    "unassigning a foreign live holder",
			before:  held(),
			request: transfer(func(r *publicops.UpdateRequest) { r.Patch.Assignee.Value = "" }),
			refuse:  true,
		},
		{
			name:    "foreign live holder with no pools",
			before:  held(),
			request: transfer(nil),
			refuse:  true,
		},
		{
			name:    "foreign live holder outside the pools",
			before:  held(),
			request: transfer(nil),
			pools:   []string{"crew"},
			refuse:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AuthorizeAssigneeTransferWithPools(tc.before, tc.request, tc.pools)
			if !tc.refuse {
				if err != nil {
					t.Fatalf("AuthorizeAssigneeTransferWithPools = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, storage.ErrAlreadyClaimed) {
				t.Fatalf("AuthorizeAssigneeTransferWithPools = %v, want ErrAlreadyClaimed", err)
			}
			// The refusal names the holder so a caller can report who has it.
			if !strings.Contains(err.Error(), tc.before.Assignee) || !strings.Contains(err.Error(), tc.before.ID) {
				t.Errorf("refusal %q omits issue %s or holder %q", err, tc.before.ID, tc.before.Assignee)
			}
		})
	}
}
