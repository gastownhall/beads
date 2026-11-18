package issueops

import (
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestAssignedIDsResetAfterFailure pins the field rule of bd-t4h0n7 (PR
// #6754): after a create fails definitely, the issue ids, default dependency
// owners and comment ids the store assigned go back to empty, while the ones
// the caller supplied stay; after an indeterminate commit (it may have
// landed), or no failure, every assigned value stays.
func TestAssignedIDsResetAfterFailure(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantReset bool
	}{
		{"definite failure", errors.New("insert failed"), true},
		{"indeterminate commit", fmt.Errorf("commit: connection lost: %w", storage.ErrCommitIndeterminate), false},
		{"success", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			minted := &types.Issue{
				Comments:     []*types.Comment{{Text: "derived"}, {ID: "supplied-comment", Text: "supplied"}},
				Dependencies: []*types.Dependency{{DependsOnID: "bd-target"}, {IssueID: "bd-owner", DependsOnID: "bd-target"}},
			}
			explicit := &types.Issue{ID: "bd-explicit", Comments: []*types.Comment{{Text: "derived"}}}
			assigned := RecordAssignedIDs(minted, nil, explicit)

			// What the create assigns as it runs.
			minted.ID = "bd-minted"
			minted.Comments[0].ID = "derived-1"
			minted.Dependencies[0].IssueID = "bd-minted"
			explicit.Comments[0].ID = "derived-2"

			assigned.ResetAfterFailure(tc.err)

			want := map[string][2]string{
				"minted id":            {minted.ID, "bd-minted"},
				"minted comment":       {minted.Comments[0].ID, "derived-1"},
				"minted edge owner":    {minted.Dependencies[0].IssueID, "bd-minted"},
				"explicit comment":     {explicit.Comments[0].ID, "derived-2"},
				"supplied comment":     {minted.Comments[1].ID, "supplied-comment"},
				"supplied edge owner":  {minted.Dependencies[1].IssueID, "bd-owner"},
				"supplied explicit id": {explicit.ID, "bd-explicit"},
			}
			for field, gotWant := range want {
				got, keep := gotWant[0], gotWant[1]
				wantValue := keep
				if tc.wantReset && keep != "supplied-comment" && keep != "bd-owner" && keep != "bd-explicit" {
					wantValue = ""
				}
				if got != wantValue {
					t.Errorf("%s = %q, want %q", field, got, wantValue)
				}
			}
		})
	}
}
