package tracker

import (
	"context"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// TestEngineCreateOnlyPullLeavesLinkedIssuesAlone pins bd-bs2b0y (#8507,
// PR #8621): a --create-only sync must not rewrite a local bead that is
// already linked to an external item; the pull holds for linked beads.
func TestEngineCreateOnlyPullLeavesLinkedIssuesAlone(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{{
		ID:         "ext-linked",
		Identifier: "EXT-LINKED",
		Title:      "Retitled remotely",
		State:      types.StatusOpen,
		Priority:   2,
	}}
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	if _, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true}); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if got := state.issues["bd-linked"].Title; got != "Already linked" {
		t.Errorf("linked bead title = %q after --create-only pull, want %q (left alone)", got, "Already linked")
	}
}

// createOnlyPullRemote returns the remote side of the create-only pull
// fixtures: the linked bead's remote issue, retitled remotely, and one remote
// issue no local bead matches.
func createOnlyPullRemote() []TrackerIssue {
	return []TrackerIssue{
		{
			ID:         "ext-linked",
			Identifier: "EXT-LINKED",
			Title:      "Retitled remotely",
			State:      types.StatusOpen,
			Priority:   2,
		},
		{
			ID:         "ext-new",
			Identifier: "EXT-NEW",
			Title:      "Only remote",
			State:      types.StatusOpen,
			Priority:   2,
		},
	}
}

// assertLinkedBeadUntouched fails when the pull applied any update to the
// store or changed the linked bead.
func assertLinkedBeadUntouched(t *testing.T, state *engineUOWState) {
	t.Helper()
	if !reflect.DeepEqual(state.updateSpec, domain.UpdateSpec{}) {
		t.Errorf("pull applied an issue update %+v, want none under --create-only", state.updateSpec)
	}
	linked := state.issues["bd-linked"]
	if linked.Title != "Already linked" {
		t.Errorf("linked bead title = %q, want %q (left alone)", linked.Title, "Already linked")
	}
	if linked.ExternalRef == nil || *linked.ExternalRef != "https://test.test/EXT-LINKED" {
		t.Errorf("linked bead external_ref = %v, want https://test.test/EXT-LINKED", linked.ExternalRef)
	}
}

// TestEngineCreateOnlyPullCreatesOnlyUnmatched covers AC-1: a --create-only
// pull creates a bead for the remote issue that matches no local bead, holds
// the matched linked bead, and counts it as skipped. The control run without
// --create-only proves the gate is what stops the write.
func TestEngineCreateOnlyPullCreatesOnlyUnmatched(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	tracker := newMockTracker("test")
	tracker.issues = createOnlyPullRemote()
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	assertLinkedBeadUntouched(t, state)
	if got := result.PullStats; got.Created != 1 || got.Updated != 0 || got.Skipped != 1 {
		t.Errorf("PullStats = {Created:%d Updated:%d Skipped:%d}, want {Created:1 Updated:0 Skipped:1}",
			got.Created, got.Updated, got.Skipped)
	}
	var created []*types.Issue
	for id, issue := range state.issues {
		if id != "bd-linked" && id != "bd-fresh" {
			created = append(created, issue)
		}
	}
	if len(created) != 1 || created[0].Title != "Only remote" {
		t.Errorf("created beads = %+v, want one bead titled %q", created, "Only remote")
	}

	t.Run("control without create-only updates the linked bead", func(t *testing.T) {
		state := createOnlyState()
		tracker := newMockTracker("test")
		tracker.issues = createOnlyPullRemote()
		engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")
		result, err := engine.Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync() error: %v", err)
		}
		if got := state.issues["bd-linked"].Title; got != "Retitled remotely" {
			t.Errorf("linked bead title = %q, want %q without --create-only", got, "Retitled remotely")
		}
		if result.PullStats.Updated != 1 {
			t.Errorf("PullStats.Updated = %d, want 1 without --create-only", result.PullStats.Updated)
		}
	})
}

// TestEngineCreateOnlyPullFallbackMatchHeld covers the fallback-index row of
// AC-1: a linked bead whose stored ref differs from the remote ref but whose
// match key agrees is held, and no duplicate bead is created for it.
func TestEngineCreateOnlyPullFallbackMatchHeld(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{{
		ID:         "ext-linked",
		Identifier: "EXT-LINKED",
		URL:        "https://moved.test/EXT-LINKED",
		Title:      "Retitled remotely",
		State:      types.StatusOpen,
		Priority:   2,
	}}
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	assertLinkedBeadUntouched(t, state)
	if len(state.issues) != 2 {
		t.Errorf("store holds %d beads, want 2: the fallback match must not be duplicated", len(state.issues))
	}
	if got := result.PullStats; got.Created != 0 || got.Updated != 0 || got.Skipped != 1 {
		t.Errorf("PullStats = {Created:%d Updated:%d Skipped:%d}, want {Created:0 Updated:0 Skipped:1}",
			got.Created, got.Updated, got.Skipped)
	}
}
