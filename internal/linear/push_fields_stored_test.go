package linear

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// remoteIssueFor builds the Linear-side issue a push of local would leave
// behind, carrying body as its stored description.
func remoteIssueFor(local *types.Issue, config *MappingConfig, body string) *Issue {
	return &Issue{
		Title:       local.Title,
		Description: body,
		Priority:    PriorityToLinear(local.Priority, config),
		State:       &State{ID: "state-3", Name: "In Progress", Type: "started"},
	}
}

// TestPushFieldsEqualLinearStoredDescription pins that a body Linear stored
// (blank line after the heading, optional idempotency marker, trailing blank
// line) does not read as drift against the push form. Before bd #8506's fix
// the pull copied that stored body and the byte compare pushed on every
// sync cycle.
func TestPushFieldsEqualLinearStoredDescription(t *testing.T) {
	config := DefaultMappingConfig()
	marker := GenerateIdempotencyMarker("bd-pf", "a@b.c", 3)
	local := &types.Issue{
		Title:       "Ship the fix",
		Description: "Main body",
		Notes:       "Local-only notes",
		Status:      types.StatusInProgress,
		Priority:    1,
	}

	equal := []struct{ name, body string }{
		{"push form", "Main body\n\n## Notes\nLocal-only notes"},
		{"stored form", "Main body\n\n## Notes\n\nLocal-only notes"},
		{"stored form with marker", "Main body\n\n## Notes\n\nLocal-only notes\n" + marker + "\n"},
		{"stored form with trailing blank", "Main body\n\n## Notes\n\nLocal-only notes\n"},
	}
	for _, tc := range equal {
		t.Run(tc.name, func(t *testing.T) {
			if !PushFieldsEqual(local, remoteIssueFor(local, config, tc.body), config, nil) {
				t.Fatalf("PushFieldsEqual(body %q) = false, want true (no drift)", tc.body)
			}
		})
	}

	unequal := []struct{ name, body string }{
		{"body text differs", "Other body\n\n## Notes\n\nLocal-only notes"},
		{"section text differs", "Main body\n\n## Notes\n\nEdited notes"},
		{"section missing", "Main body"},
	}
	for _, tc := range unequal {
		t.Run(tc.name, func(t *testing.T) {
			if PushFieldsEqual(local, remoteIssueFor(local, config, tc.body), config, nil) {
				t.Fatalf("PushFieldsEqual(body %q) = true, want false (drift)", tc.body)
			}
		})
	}

	// The BatchPush comparable: the engine pre-folds the description and the
	// tracker clears the structured fields before comparing. A stored remote
	// body must still compare equal.
	t.Run("batch push comparable issue", func(t *testing.T) {
		comparable := *local
		comparable.Description = BuildLinearDescription(local)
		comparable.AcceptanceCriteria = ""
		comparable.Design = ""
		comparable.Notes = ""
		body := "Main body\n\n## Notes\n\nLocal-only notes\n" + marker + "\n"
		if !PushFieldsEqual(&comparable, remoteIssueFor(local, config, body), config, nil) {
			t.Fatalf("PushFieldsEqual(folded comparable, stored body %q) = false, want true", body)
		}
	})
}

// TestPushFieldsEqualToBeadsStoredDescription pins the beads-form fallback
// comparator on both forms a pull can leave behind: fields split, or the
// stored body still folded into Description.
func TestPushFieldsEqualToBeadsStoredDescription(t *testing.T) {
	marker := GenerateIdempotencyMarker("bd-pfb", "a@b.c", 4)
	local := &types.Issue{
		Title:       "Ship the fix",
		Description: "Main body",
		Notes:       "Local-only notes",
		Status:      types.StatusInProgress,
		Priority:    1,
	}
	split := &types.Issue{
		Title:       "Ship the fix",
		Description: "Main body",
		Notes:       "Local-only notes",
		Status:      types.StatusInProgress,
		Priority:    1,
	}
	stored := &types.Issue{
		Title:       "Ship the fix",
		Description: "Main body\n\n## Notes\n\nLocal-only notes\n" + marker + "\n",
		Status:      types.StatusInProgress,
		Priority:    1,
	}
	drift := &types.Issue{
		Title:       "Ship the fix",
		Description: "Main body",
		Notes:       "Edited notes",
		Status:      types.StatusInProgress,
		Priority:    1,
	}

	if !PushFieldsEqualToBeads(local, split) {
		t.Fatal("PushFieldsEqualToBeads(split form) = false, want true")
	}
	if !PushFieldsEqualToBeads(local, stored) {
		t.Fatal("PushFieldsEqualToBeads(stored form) = false, want true")
	}
	if PushFieldsEqualToBeads(local, drift) {
		t.Fatal("PushFieldsEqualToBeads(drifted notes) = true, want false")
	}
}

// TestDescriptionFieldsEqual covers the comparator directly: split vs folded
// forms of the same content are equal; any text difference is not.
func TestDescriptionFieldsEqual(t *testing.T) {
	split := &types.Issue{Description: "D", AcceptanceCriteria: "AC", Design: "DESIGN", Notes: "N"}
	folded := &types.Issue{Description: "D\n\n## Acceptance Criteria\n\nAC\n\n## Design\n\nDESIGN\n\n## Notes\n\nN"}
	if !DescriptionFieldsEqual(split, folded) {
		t.Fatal("DescriptionFieldsEqual(split, folded) = false, want true")
	}
	if DescriptionFieldsEqual(split, &types.Issue{Description: "D", Notes: "N"}) {
		t.Fatal("DescriptionFieldsEqual(split, partial) = true, want false")
	}
	if DescriptionFieldsEqual(split, nil) {
		t.Fatal("DescriptionFieldsEqual(split, nil) = true, want false")
	}
}
