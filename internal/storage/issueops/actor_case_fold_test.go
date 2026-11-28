package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestCheckClosableFoldsActorCase pins beads #8628: the close ownership check
// matches an actor whose casing differs from the stored assignee, and still
// refuses a genuinely different identity.
func TestCheckClosableFoldsActorCase(t *testing.T) {
	cases := []struct {
		name     string
		assignee string
		actor    string
		wantErr  bool
	}{
		{"stored lower, actor Title", "hierophant", "Hierophant", false},
		{"stored Title, actor lower", "Hierophant", "hierophant", false},
		{"stored upper, actor lower", "HIEROPHANT", "hierophant", false},
		{"different identity refused", "hierophant", "alice", true},
		{"unassigned open to anyone", "", "Hierophant", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := &types.Issue{Assignee: tc.assignee}
			err := CheckClosable("bd-1", issue, tc.actor, false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CheckClosable(assignee=%q, actor=%q) err = %v, wantErr %v", tc.assignee, tc.actor, err, tc.wantErr)
			}
		})
	}
}
