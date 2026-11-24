package tracker

import (
	"context"
	"reflect"
	"strings"
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

// TestEngineCreateOnlyPullDryRunMatchesRealRun covers AC-2: the dry-run
// preview of a --create-only pull shows no update for the linked bead, shows
// the import of the unmatched issue, writes nothing, and reports the stats of
// the real run.
func TestEngineCreateOnlyPullDryRunMatchesRealRun(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	tracker := newMockTracker("test")
	tracker.issues = createOnlyPullRemote()
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")
	var msgs []string
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }

	dry, err := engine.Sync(ctx, SyncOptions{Pull: true, DryRun: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() dry-run error: %v", err)
	}
	joined := strings.Join(msgs, "\n")
	if strings.Contains(joined, "Would update local issue") {
		t.Errorf("dry-run messages = %q, did not expect a local update preview under --create-only", joined)
	}
	if !strings.Contains(joined, "Would import: EXT-NEW - Only remote") {
		t.Errorf("dry-run messages = %q, want an import preview for EXT-NEW", joined)
	}
	assertLinkedBeadUntouched(t, state)
	if len(state.issues) != 2 || state.commits != 0 {
		t.Errorf("dry-run wrote to the store: beads=%d commits=%d, want 2 beads and no commit", len(state.issues), state.commits)
	}
	if len(tracker.created) != 0 || len(tracker.updated) != 0 {
		t.Errorf("dry-run wrote to the tracker: created=%d updated=%d", len(tracker.created), len(tracker.updated))
	}

	liveState := createOnlyState()
	liveTracker := newMockTracker("test")
	liveTracker.issues = createOnlyPullRemote()
	live, err := NewEngine(liveTracker, NewUOWStore(&engineUOWProvider{state: liveState}), "test-actor").
		Sync(ctx, SyncOptions{Pull: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() real run error: %v", err)
	}
	d, l := dry.PullStats, live.PullStats
	if d.Created != l.Created || d.Updated != l.Updated || d.Skipped != l.Skipped {
		t.Errorf("dry-run PullStats {Created:%d Updated:%d Skipped:%d} != real run {Created:%d Updated:%d Skipped:%d}",
			d.Created, d.Updated, d.Skipped, l.Created, l.Updated, l.Skipped)
	}
}
