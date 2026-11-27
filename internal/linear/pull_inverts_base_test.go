package linear

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// Base-only variant of the #8506 round-trip check: uses only functions that
// exist on base, so it compiles there and fails on the description it pulls.
func TestPullInvertsPushDescriptionBaseOnly(t *testing.T) {
	src := &types.Issue{Title: "t", Description: "DESC", Design: "DESIGN"}
	marker := GenerateIdempotencyMarker("bd-9", "a@b.test", 9)
	pushed := AppendIdempotencyMarker(BuildLinearDescription(src), marker)

	li := &Issue{
		ID:          "uuid-1",
		Identifier:  "T-1",
		Title:       "t",
		Description: pushed,
		Priority:    2,
		State:       &State{Type: "started", Name: "In Progress"},
		CreatedAt:   "2024-01-15T10:00:00Z",
		UpdatedAt:   "2024-01-16T12:00:00Z",
	}
	pulled := IssueToBeads(li, DefaultMappingConfig()).Issue.(*types.Issue)
	if pulled.Description != "DESC" {
		t.Fatalf("pulled description = %q, want %q", pulled.Description, "DESC")
	}
}
