package issueops

import (
	"errors"
	"fmt"
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
		// bd-740y4x: the report pair, both actor/assignee directions — a
		// rig-qualified holder under its raw slash spelling is the same
		// principal as the session-name spelling, so the close is allowed.
		{"held slash vs session spelling", &types.Issue{Assignee: "gascity/bob"}, "gascity--bob", false, nil},
		{"held session spelling vs slash", &types.Issue{Assignee: "gascity--bob"}, "gascity/bob", false, nil},
		// bd-740y4x: negative controls for the report pair — a different agent
		// in the same rig, the same agent in a different rig, and every
		// dotted-axis spelling of the rig name still refuse: the slash axis and
		// the dotted alias axis are deliberately distinct (ga-2vy9p2).
		{"held by a different agent in the same rig", &types.Issue{Assignee: "gascity/bob"}, "gascity--alice", false, publicops.ErrNotOwner},
		{"held by the same agent name in a different rig", &types.Issue{Assignee: "gascity/bob"}, "python419--bob", false, publicops.ErrNotOwner},
		{"held slash vs dotted-axis double-underscore spelling", &types.Issue{Assignee: "gascity/bob"}, "gascity__bob", false, publicops.ErrNotOwner},
		{"held slash vs dotted-axis dot spelling", &types.Issue{Assignee: "gascity/bob"}, "gascity.bob", false, publicops.ErrNotOwner},
		{"held slash vs dotted-axis single-underscore spelling", &types.Issue{Assignee: "gascity/bob"}, "gascity_bob", false, publicops.ErrNotOwner},
		{"held slash vs dotted-axis hyphen spelling", &types.Issue{Assignee: "gascity/bob"}, "gascity-bob", false, publicops.ErrNotOwner},
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
	// bd-740y4x: a report-pair negative control is the same ownership refusal,
	// retaining both spellings so the close sentence names the holder and the
	// actor exactly as supplied.
	var slashHeld *publicops.CloseNotAssigneeError
	if err := CheckClosable("bd-1", &types.Issue{Assignee: "gascity/bob"}, "gascity--alice", false); !errors.As(err, &slashHeld) ||
		slashHeld.IssueID != "bd-1" || slashHeld.Assignee != "gascity/bob" || slashHeld.Actor != "gascity--alice" {
		t.Fatalf("same-rig different-agent refusal = %#v", err)
	}
}

// TestRefusedMoleculeAutoCloseReportsTheRefusalsOwnSentence pins
// RefusedMoleculeAutoClose: whatever the layers between a root's refused close
// and the auto-close wrapped around it (the unit of work's use case and
// repository), Refusal is the sentence the refusing guard or policy wrote.
func TestRefusedMoleculeAutoCloseReportsTheRefusalsOwnSentence(t *testing.T) {
	for _, refusal := range []error{
		publicops.NewCloseBlockedError("bd-1", []string{"bd-2"}),
		&publicops.CloseOpenChildrenError{IssueID: "bd-1", OpenChildren: 2},
		&publicops.TemplateReadOnlyError{IssueID: "bd-1"},
		&publicops.PinnedError{IssueID: "bd-1"},
		&publicops.CloseNotAssigneeError{IssueID: "bd-1", Assignee: "bob", Actor: "alice"},
	} {
		wrapped := fmt.Errorf("close bd-1: %w", fmt.Errorf("db: IssueSQLRepository.CloseChecked bd-1: %w", refusal))
		for _, err := range []error{refusal, wrapped} {
			if got := RefusedMoleculeAutoClose(err).Refusal; got != refusal.Error() {
				t.Errorf("RefusedMoleculeAutoClose(%q).Refusal = %q, want %q", err, got, refusal.Error())
			}
		}
	}
}
