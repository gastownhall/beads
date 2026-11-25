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
// fence (separator- and case-insensitive; unassigned is anyone's).
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
		// GH#8628: letter case is not part of an identity.
		{"own under another letter case", &types.Issue{Assignee: "hierophant"}, "Hierophant", false, nil},
		{"own under another case and separator", &types.Issue{Assignee: "gastown.mayor"}, "GASTOWN__MAYOR", false, nil},
		{"case fold never widens to another name", &types.Issue{Assignee: "hierophant"}, "hierophant2", false, publicops.ErrNotOwner},
		{"padded name is another name", &types.Issue{Assignee: "hierophant"}, " Hierophant", false, publicops.ErrNotOwner},
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

// TestCheckClosableAssigneeCaseInsensitive reproduces GH#8628 (bd-f76042):
// an issue assigned to "hierophant" must be closable by actor "Hierophant"
// without --force — the two spellings name the same actor.
func TestCheckClosableAssigneeCaseInsensitive(t *testing.T) {
	issue := &types.Issue{ID: "cst-z8sw", Status: types.StatusInProgress, Assignee: "hierophant"}
	if err := CheckClosable("cst-z8sw", issue, "Hierophant", false); err != nil {
		t.Fatalf("CheckClosable(assignee %q, actor %q) = %v, want nil (case-insensitive actor match)", issue.Assignee, "Hierophant", err)
	}
	if !ActorMatches("hierophant", "Hierophant") {
		t.Fatalf("ActorMatches(%q, %q) = false, want true", "hierophant", "Hierophant")
	}
	var held *publicops.CloseNotAssigneeError
	if err := CheckClosable("cst-z8sw", issue, "hierophant2", false); !errors.As(err, &held) || held.Assignee != "hierophant" || held.Actor != "hierophant2" {
		t.Fatalf("CheckClosable(assignee %q, actor %q) = %v, want the not-assignee refusal", issue.Assignee, "hierophant2", err)
	}
}
