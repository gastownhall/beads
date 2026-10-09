package issueops

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestCheckClosable pins the close guards' one decision: template first (no
// bypass), then — unless force — the pin (both spellings) and the assignee
// fence (separator-insensitive; unassigned is anyone's).
func TestCheckClosable(t *testing.T) {
	cases := []struct {
		name  string
		issue *types.Issue
		actor string
		force bool
		want  error
	}{
		{"nil", nil, "alice", false, nil},
		{"plain", &types.Issue{Status: types.StatusOpen}, "alice", false, nil},
		{"template", &types.Issue{IsTemplate: true}, "alice", false, publicops.ErrTemplateReadOnly},
		{"forced template", &types.Issue{IsTemplate: true}, "alice", true, publicops.ErrTemplateReadOnly},
		{"template outranks pin", &types.Issue{IsTemplate: true, Pinned: true}, "alice", false, publicops.ErrTemplateReadOnly},
		{"pinned column", &types.Issue{Pinned: true}, "alice", false, publicops.ErrPinned},
		{"pinned status", &types.Issue{Status: types.StatusPinned}, "alice", false, publicops.ErrPinned},
		{"forced pin", &types.Issue{Pinned: true}, "alice", true, nil},
		{"pin outranks holder", &types.Issue{Pinned: true, Assignee: "bob"}, "alice", false, publicops.ErrPinned},
		{"held", &types.Issue{Assignee: "bob"}, "alice", false, publicops.ErrNotOwner},
		{"forced held", &types.Issue{Assignee: "bob"}, "alice", true, nil},
		{"own", &types.Issue{Assignee: "alice"}, "alice", false, nil},
		{"unassigned", &types.Issue{}, "alice", false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckClosable("bd-1", tc.issue, tc.actor, tc.force)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("CheckClosable = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("CheckClosable = %v, want %v", err, tc.want)
			}
		})
	}
	var held *publicops.CloseNotAssigneeError
	if err := CheckClosable("bd-1", &types.Issue{Assignee: "bob"}, "alice", false); !errors.As(err, &held) ||
		held.IssueID != "bd-1" || held.Assignee != "bob" || held.Actor != "alice" {
		t.Fatalf("held refusal = %#v", err)
	}
}
