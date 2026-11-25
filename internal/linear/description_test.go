package linear

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// linearStoredDescription returns BuildLinearDescription(issue) the way Linear
// stores it: Linear's editor puts a blank line after each heading.
func linearStoredDescription(issue *types.Issue) string {
	stored := issue.Description
	if issue.AcceptanceCriteria != "" {
		stored += "\n\n## Acceptance Criteria\n\n" + issue.AcceptanceCriteria
	}
	if issue.Design != "" {
		stored += "\n\n## Design\n\n" + issue.Design
	}
	if issue.Notes != "" {
		stored += "\n\n## Notes\n\n" + issue.Notes
	}
	return stored
}

func fullDescriptionBead() *types.Issue {
	return &types.Issue{
		Title:              "T",
		Description:        "DESC",
		AcceptanceCriteria: "AC",
		Design:             "DESIGN",
		Notes:              "NOTES",
	}
}

// TestParseLinearDescriptionStoredForm pins AC-1: the description a push
// wrote, as Build emits it and as Linear stores it, with and without the
// create's idempotency marker, parses back into the four bead fields.
func TestParseLinearDescriptionStoredForm(t *testing.T) {
	bead := fullDescriptionBead()
	marker := GenerateIdempotencyMarker("bd-1", "a@b.c", 1)
	want := DescriptionParts{Description: "DESC", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "NOTES"}
	for name, text := range map[string]string{
		"built":                 BuildLinearDescription(bead),
		"built with marker":     AppendIdempotencyMarker(BuildLinearDescription(bead), marker),
		"stored":                linearStoredDescription(bead),
		"stored with marker":    AppendIdempotencyMarker(linearStoredDescription(bead), marker),
		"stored, trailing line": linearStoredDescription(bead) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ParseLinearDescription(text); got != want {
				t.Errorf("ParseLinearDescription(%q) = %+v, want %+v", text, got, want)
			}
		})
	}
}

// TestIssueToBeadsSplitsDescriptionSections pins AC-1 at the mapping layer:
// a pulled Linear issue fills the four fields, none holding a heading or the
// marker, and BuildLinearToLocalUpdates writes the same values.
func TestIssueToBeadsSplitsDescriptionSections(t *testing.T) {
	stored := AppendIdempotencyMarker(linearStoredDescription(fullDescriptionBead()), GenerateIdempotencyMarker("bd-1", "a@b.c", 1))
	li := &Issue{
		ID:          "uuid-1",
		Identifier:  "TEAM-1",
		Title:       "T",
		Description: stored,
		URL:         "https://linear.app/team/issue/TEAM-1/t",
		CreatedAt:   "2026-01-01T00:00:00Z",
		UpdatedAt:   "2026-01-01T00:00:00Z",
	}
	cfg := DefaultMappingConfig()

	got, ok := IssueToBeads(li, cfg).Issue.(*types.Issue)
	if !ok {
		t.Fatalf("IssueToBeads returned %T, want *types.Issue", IssueToBeads(li, cfg).Issue)
	}
	if got.Description != "DESC" || got.AcceptanceCriteria != "AC" || got.Design != "DESIGN" || got.Notes != "NOTES" {
		t.Fatalf("IssueToBeads fields = description %q, acceptance %q, design %q, notes %q; want DESC/AC/DESIGN/NOTES",
			got.Description, got.AcceptanceCriteria, got.Design, got.Notes)
	}

	updates := BuildLinearToLocalUpdates(li, cfg)
	for key, want := range map[string]string{
		"description":         "DESC",
		"acceptance_criteria": "AC",
		"design":              "DESIGN",
		"notes":               "NOTES",
	} {
		if updates[key] != want {
			t.Errorf("BuildLinearToLocalUpdates[%q] = %v, want %q", key, updates[key], want)
		}
	}

	// A body without sections writes no section keys, so the updates never
	// clear a field Linear does not show.
	li.Description = "Just text"
	updates = BuildLinearToLocalUpdates(li, cfg)
	for _, key := range []string{"acceptance_criteria", "design", "notes"} {
		if _, ok := updates[key]; ok {
			t.Errorf("BuildLinearToLocalUpdates(no sections)[%q] = %v, want no key", key, updates[key])
		}
	}
}
