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
			// ga-5ksp5: the same Gas Town identity spelled two different
			// ways (dot vs underscore separator) must still count as
			// "actor already holds it", not a foreign transfer.
			name:    "actor already holds it under a different spelling",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "gastown.mayor"},
			request: transfer(func(r *publicops.UpdateRequest) { r.Actor = "gastown_mayor" }),
		},
		{
			// ga-5ksp5: reasserting the current holder under a different
			// (and doubled-separator) spelling is still the same
			// idempotent no-op as reasserting it verbatim.
			name:    "reasserts the current assignee under a different spelling",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "gastown.mayor"},
			request: transfer(func(r *publicops.UpdateRequest) { r.Patch.Assignee.Value = "gastown__mayor" }),
		},
		{
			// ga-5ksp5: canonicalization must not over-match — an actor
			// whose identity genuinely differs from the holder (not just
			// a respelling of it) stays refused.
			name:    "foreign actor with an unrelated identity is still refused",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "gastown.mayor"},
			request: transfer(func(r *publicops.UpdateRequest) { r.Actor = "gastown_dog-1" }),
			refuse:  true,
		},
		{
			// GH#8628: letter case is the same class of drift as a separator
			// respelling — the holder updating its own assignee (here: to its
			// own other casing) is an edit of its own claim, not a transfer.
			name:    "holder edits its own live claim under a case variant",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "hierophant"},
			request: transfer(func(r *publicops.UpdateRequest) { r.Actor = "Hierophant"; r.Patch.Assignee.Value = "Hierophant" }),
		},
		{
			// GH#8628's control: case-folding the fence must not unlock a
			// stranger transferring a live claim to a third party.
			name:    "stranger transferring a differently cased live claim is still refused",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "hierophant"},
			request: transfer(func(r *publicops.UpdateRequest) { r.Patch.Assignee.Value = "carol" }),
			refuse:  true,
		},
		{
			// Pool aliases keep exact-string membership (ga-v2k49, reason on
			// the predicate's own loop): a case variant of a configured alias
			// is NOT the alias, so an issue held under a different casing than
			// the configured alias stays fenced.
			name:    "case-variant pool alias does not take the claim",
			before:  &types.Issue{ID: "bd-1", Status: types.StatusInProgress, Assignee: "hierophant"},
			request: transfer(nil),
			pools:   []string{"HIEROPHANT"},
			refuse:  true,
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
