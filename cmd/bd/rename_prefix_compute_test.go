package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestComputeRepairRenames_PreservesSuffixNoCollision pins beads#6591: the
// common case (no real collisions) must keep every issue's mnemonic suffix,
// including dotted children, instead of minting a hash id unconditionally.
func TestComputeRepairRenames_PreservesSuffixNoCollision(t *testing.T) {
	t.Parallel()

	correct := []*types.Issue{{ID: "kb-u6ch"}}
	incorrect := []issueSort{
		{issue: &types.Issue{ID: "cre-db-nwb"}, prefix: "cre-db", number: 0},
		{issue: &types.Issue{ID: "cre-db-nwb.3"}, prefix: "cre-db", number: 0},
		{issue: &types.Issue{ID: "investment-c2a"}, prefix: "investment", number: 0},
	}

	renameMap, collided, err := computeRepairRenames("kb", correct, incorrect, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(collided) != 0 {
		t.Fatalf("expected no collisions, got %v", collided)
	}

	want := map[string]string{
		"cre-db-nwb":     "kb-nwb",
		"cre-db-nwb.3":   "kb-nwb.3",
		"investment-c2a": "kb-c2a",
	}
	for oldID, wantID := range want {
		got, ok := renameMap[oldID]
		if !ok {
			t.Fatalf("missing rename for %s", oldID)
		}
		if got != wantID {
			t.Fatalf("renameMap[%s] = %s, want %s (suffix must be preserved, not hashed)", oldID, got, wantID)
		}
	}
}

// TestComputeRepairRenames_FallsBackOnCollisionWithExisting pins the other
// half of beads#6591: an incorrect-prefix issue whose suffix-preserving id
// would collide with an already-correct id must fall back to a hash id
// instead of silently colliding.
func TestComputeRepairRenames_FallsBackOnCollisionWithExisting(t *testing.T) {
	t.Parallel()

	correct := []*types.Issue{{ID: "kb-1"}}
	incorrect := []issueSort{
		{issue: &types.Issue{ID: "old-1", Title: "collides with kb-1"}, prefix: "old", number: 1},
	}

	renameMap, collided, err := computeRepairRenames("kb", correct, incorrect, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(collided) != 1 || collided[0] != "old-1" {
		t.Fatalf("expected old-1 reported as a collision, got %v", collided)
	}

	got := renameMap["old-1"]
	if got == "kb-1" {
		t.Fatalf("old-1 must not collide with the existing kb-1, got %s", got)
	}
	if got[:3] != "kb-" || len(got) != 11 { // "kb-" + 8 hex chars
		t.Fatalf("expected a kb-<8 hex> hash fallback id, got %q", got)
	}
}

// TestComputeRepairRenames_FallsBackOnCollisionWithinBatch covers two
// incorrect-prefix issues from different source prefixes that would both
// naively rewrite to the same suffix (e.g. "old-1" and "another-1" both
// consolidating toward "kb-1" with no pre-existing kb-1). Both must resolve
// to distinct, real ids — no data loss, no silent overwrite.
func TestComputeRepairRenames_FallsBackOnCollisionWithinBatch(t *testing.T) {
	t.Parallel()

	incorrect := []issueSort{
		{issue: &types.Issue{ID: "old-1", Title: "first"}, prefix: "old", number: 1},
		{issue: &types.Issue{ID: "another-1", Title: "second"}, prefix: "another", number: 1},
	}

	renameMap, collided, err := computeRepairRenames("kb", nil, incorrect, "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(collided) != 1 {
		t.Fatalf("expected exactly one issue to need a hash fallback (the second one processed), got %v", collided)
	}

	firstID := renameMap["old-1"]
	secondID := renameMap["another-1"]
	if firstID == secondID {
		t.Fatalf("old-1 and another-1 must not resolve to the same id, both got %s", firstID)
	}
	if firstID != "kb-1" {
		t.Fatalf("expected the first-processed issue to keep the suffix-preserving id kb-1, got %s", firstID)
	}
}
