package tracker

import (
	"context"
	"testing"
)

// #8507: --create-only must hold on a pull too. A pull with CreateOnly may
// create beads for unlinked remote issues, but it must not update a bead that
// is already linked, even when the remote title changed.
func TestEngineCreateOnlyPullLeavesLinkedIssuesAlone(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{{
		ID:         "uuid-linked",
		Identifier: "EXT-LINKED",
		URL:        "https://test.test/EXT-LINKED",
		Title:      "Edited in Linear",
	}}
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	if _, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true}); err != nil {
		t.Fatalf("Sync(pull, create-only): %v", err)
	}
	if got := state.issues["bd-linked"].Title; got != "Already linked" {
		t.Fatalf("linked bead title = %q after a create-only pull, want it unchanged", got)
	}
}
