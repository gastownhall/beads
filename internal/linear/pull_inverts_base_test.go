package linear

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPullInvertsPushDescriptionBaseOnly is the base-only regression test a
// pull inverts the description fold a push made: the pulled description is
// DESC with the structured fields restored, on a base that holds only
// functions existing before the change (GH#8506).
func TestPullInvertsPushDescriptionBaseOnly(t *testing.T) {
	bead := &types.Issue{
		Title:              "T",
		Description:        "DESC",
		AcceptanceCriteria: "AC",
		Design:             "DESIGN",
		Notes:              "NOTES",
	}
	body := AppendIdempotencyMarker(BuildLinearDescription(bead), GenerateIdempotencyMarker("bd-x", "a@b.c", 9))

	li := &Issue{
		Identifier:  "TEAM-9",
		Title:       "T",
		Description: body,
		URL:         "https://linear.app/team/issue/TEAM-9/t",
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-01-01T00:00:00Z",
	}

	got := IssueToBeads(li, DefaultMappingConfig()).Issue.(*types.Issue)
	if got.Description != "DESC" {
		t.Errorf("pulled description = %q, want %q", got.Description, "DESC")
	}
}
