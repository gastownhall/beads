package linear

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestPullSplitsPushedDescriptionSections asserts that pulling a Linear body
// produced by BuildLinearDescription + AppendIdempotencyMarker restores the
// original structured fields instead of copying the whole body (bd #8506).
func TestPullSplitsPushedDescriptionSections(t *testing.T) {
	body := "DESC\n\n## Acceptance Criteria\nAC\n\n## Design\nDESIGN\n\n## Notes\nNOTES"
	body = AppendIdempotencyMarker(body, GenerateIdempotencyMarker("bd-1", "a@b.c", 1))
	li := &Issue{
		Identifier:  "TEAM-1",
		Title:       "T",
		Description: body,
		URL:         "https://linear.app/team/issue/TEAM-1/t",
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-01-01T00:00:00Z",
	}
	cfg := DefaultMappingConfig()

	got, ok := IssueToBeads(li, cfg).Issue.(*types.Issue)
	if !ok {
		t.Fatalf("IssueToBeads returned %T, want *types.Issue", IssueToBeads(li, cfg).Issue)
	}
	if got.Description != "DESC" {
		t.Errorf("IssueToBeads description = %q, want %q", got.Description, "DESC")
	}
	if got.AcceptanceCriteria != "AC" || got.Design != "DESIGN" || got.Notes != "NOTES" {
		t.Errorf("IssueToBeads fields: acceptance=%q design=%q notes=%q, want AC/DESIGN/NOTES",
			got.AcceptanceCriteria, got.Design, got.Notes)
	}

	updates := BuildLinearToLocalUpdates(li, cfg)
	if updates["description"] != "DESC" {
		t.Errorf("BuildLinearToLocalUpdates description = %q, want %q", updates["description"], "DESC")
	}
}

// TestIssueToBeadsSplitsLinearStoredDescription checks the parser against the
// form Linear actually stores: a blank line after each section heading, with
// and without the idempotency marker.
func TestIssueToBeadsSplitsLinearStoredDescription(t *testing.T) {
	cfg := DefaultMappingConfig()
	marker := GenerateIdempotencyMarker("bd-2", "a@b.c", 2)

	cases := []struct {
		name        string
		body        string
		description string
		acceptance  string
		design      string
		notes       string
	}{
		{
			name:        "stored form with marker",
			body:        "DESC\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\n## Notes\n\nNOTES\n" + marker + "\n",
			description: "DESC", acceptance: "AC", design: "DESIGN", notes: "NOTES",
		},
		{
			name:        "stored form without marker",
			body:        "DESC\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\n## Notes\n\nNOTES",
			description: "DESC", acceptance: "AC", design: "DESIGN", notes: "NOTES",
		},
		{
			name: "build form",
			body: BuildLinearDescription(&types.Issue{
				Description:        "DESC",
				AcceptanceCriteria: "AC",
				Design:             "DESIGN",
				Notes:              "NOTES",
			}),
			description: "DESC", acceptance: "AC", design: "DESIGN", notes: "NOTES",
		},
		{
			// A create that stored only its marker holds no sections and no
			// description text.
			name: "marker only", body: marker,
		},
		{
			// A marker a previous pull absorbed into the description sits
			// above the text; the body parses once it is gone.
			name: "marker mid-text", body: "\n" + marker + "\n\nDESC\n\n## Notes\n\nNOTES",
			description: "DESC", notes: "NOTES",
		},
	}

	for _, tc := range cases {
		li := &Issue{
			Identifier:  "TEAM-2",
			Title:       "T",
			Description: tc.body,
			URL:         "https://linear.app/team/issue/TEAM-2/t",
			CreatedAt:   "2026-01-01T00:00:00Z",
			UpdatedAt:   "2026-01-01T00:00:00Z",
		}
		got, ok := IssueToBeads(li, cfg).Issue.(*types.Issue)
		if !ok {
			t.Fatalf("%s: IssueToBeads returned %T, want *types.Issue", tc.name, IssueToBeads(li, cfg).Issue)
		}
		if got.Description != tc.description {
			t.Errorf("%s: description = %q, want %q", tc.name, got.Description, tc.description)
		}
		if got.AcceptanceCriteria != tc.acceptance {
			t.Errorf("%s: acceptance = %q, want %q", tc.name, got.AcceptanceCriteria, tc.acceptance)
		}
		if got.Design != tc.design {
			t.Errorf("%s: design = %q, want %q", tc.name, got.Design, tc.design)
		}
		if got.Notes != tc.notes {
			t.Errorf("%s: notes = %q, want %q", tc.name, got.Notes, tc.notes)
		}
	}
}

// TestBuildLinearToLocalUpdatesSplitsDescriptionSections extends the
// BuildLinearToLocalUpdates coverage to the three structured keys a pull now
// writes back.
func TestBuildLinearToLocalUpdatesSplitsDescriptionSections(t *testing.T) {
	cfg := DefaultMappingConfig()
	li := &Issue{
		Identifier:  "TEAM-3",
		Title:       "T",
		Description: "DESC\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\n## Notes\n\nNOTES",
		URL:         "https://linear.app/team/issue/TEAM-3/t",
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-01-01T00:00:00Z",
	}

	updates := BuildLinearToLocalUpdates(li, cfg)
	if updates["description"] != "DESC" {
		t.Errorf("description = %v, want %q", updates["description"], "DESC")
	}
	if updates["acceptance_criteria"] != "AC" {
		t.Errorf("acceptance_criteria = %v, want %q", updates["acceptance_criteria"], "AC")
	}
	if updates["design"] != "DESIGN" {
		t.Errorf("design = %v, want %q", updates["design"], "DESIGN")
	}
	if updates["notes"] != "NOTES" {
		t.Errorf("notes = %v, want %q", updates["notes"], "NOTES")
	}
}
