package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// createOnlyPullMapper gives each pulled remote issue a stable local ID derived
// from its identifier, so an unmatched remote issue is created under a
// collision-free ID instead of the empty default.
func createOnlyPullMapper() *mockMapper {
	return &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
		return &IssueConversion{
			Issue: &types.Issue{
				ID:        "bd-pulled-" + strings.ToLower(ti.Identifier),
				Title:     ti.Title,
				Priority:  2,
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
			},
		}
	}}
}

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

// TestEngineCreateOnlyPullCreatesUnmatchedAndHoldsLinked covers the full AC-1
// contract: a --create-only pull leaves the linked bead untouched, creates a
// bead for the remote issue that matches nothing, and reports one skip.
func TestEngineCreateOnlyPullCreatesUnmatchedAndHoldsLinked(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	tracker := newMockTracker("test")
	tracker.fieldMapper = createOnlyPullMapper()
	tracker.issues = []TrackerIssue{
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
			Title:      "Brand new remote",
			State:      types.StatusOpen,
			Priority:   2,
		},
	}
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}

	// The linked bead is left alone: no update is applied to it at all.
	if got := state.issues["bd-linked"].Title; got != "Already linked" {
		t.Errorf("linked bead title = %q, want %q (left alone)", got, "Already linked")
	}
	if len(state.updateSpec.Fields) != 0 {
		t.Errorf("issue update %v was applied under --create-only, want none", state.updateSpec.Fields)
	}

	// The unmatched remote issue is created and linked to its ref.
	created := state.issues["bd-pulled-ext-new"]
	if created == nil {
		t.Fatalf("no bead created for unmatched remote issue EXT-NEW")
	}
	if created.Title != "Brand new remote" {
		t.Errorf("created bead title = %q, want %q", created.Title, "Brand new remote")
	}
	if created.ExternalRef == nil || *created.ExternalRef != "https://test.test/EXT-NEW" {
		t.Errorf("created bead external_ref = %v, want the remote ref", created.ExternalRef)
	}

	if result.PullStats.Created != 1 || result.PullStats.Updated != 0 || result.PullStats.Skipped != 1 {
		t.Errorf("PullStats = {Created:%d Updated:%d Skipped:%d}, want {1 0 1}",
			result.PullStats.Created, result.PullStats.Updated, result.PullStats.Skipped)
	}
}

// TestEngineCreateOnlyPullHoldsFallbackMatch pins the fallback-index tier: a
// linked bead whose stored ref went stale is matched by identifier and held,
// and the held remote issue must not create a duplicate bead.
func TestEngineCreateOnlyPullHoldsFallbackMatch(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	// The linked bead's stored ref is stale (the remote issue moved), so the
	// exact lookup misses and the fallback index matches it by identifier.
	state.issues["bd-linked"].ExternalRef = strPtr("https://test.test/OLD-LINKED")
	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{{
		ID:         "ext-linked",
		Identifier: "OLD-LINKED",
		URL:        "https://moved.test/OLD-LINKED",
		Title:      "Retitled remotely",
		State:      types.StatusOpen,
		Priority:   2,
	}}
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	if _, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true}); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if got := state.issues["bd-linked"].Title; got != "Already linked" {
		t.Errorf("fallback-matched bead title = %q, want %q (left alone)", got, "Already linked")
	}
	if len(state.issues) != 2 {
		t.Errorf("issues = %d rows, want 2: a held remote issue must not create a duplicate", len(state.issues))
	}
}

// TestEngineCreateOnlyPullUpdatesLinkedWithoutFlag is the control for the
// gate: the same fixtures with the flag off do update the linked bead, which
// proves the --create-only gate is what stops the write.
func TestEngineCreateOnlyPullUpdatesLinkedWithoutFlag(t *testing.T) {
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

	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if got := state.issues["bd-linked"].Title; got != "Retitled remotely" {
		t.Errorf("linked bead title = %q, want the remote title (flag off updates)", got)
	}
	if result.PullStats.Updated != 1 {
		t.Errorf("PullStats.Updated = %d, want 1", result.PullStats.Updated)
	}
}

// TestEngineCreateOnlyPullDryRunPreviewsNoUpdate pins the AC-2 contract: a
// --create-only dry run previews no update for a matched bead, still previews
// the import of the unmatched issue, writes nothing, and reports the same
// stats as the real run.
func TestEngineCreateOnlyPullDryRunPreviewsNoUpdate(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	tracker := newMockTracker("test")
	tracker.fieldMapper = createOnlyPullMapper()
	tracker.issues = []TrackerIssue{
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
			Title:      "Brand new remote",
			State:      types.StatusOpen,
			Priority:   2,
		},
	}
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")
	var msgs []string
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }

	dry, err := engine.Sync(ctx, SyncOptions{Pull: true, DryRun: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() dry-run error: %v", err)
	}
	joined := strings.Join(msgs, "\n")
	if strings.Contains(joined, "Would update local issue") {
		t.Errorf("dry-run messages = %q, want no update preview under --create-only", joined)
	}
	if !strings.Contains(joined, "Would import: EXT-NEW") {
		t.Errorf("dry-run messages = %q, want an import preview for the unmatched issue", joined)
	}
	if len(state.issues) != 2 || state.commits != 0 || len(state.updateSpec.Fields) != 0 {
		t.Fatalf("dry-run wrote to the store: issues=%d commits=%d update=%v",
			len(state.issues), state.commits, state.updateSpec.Fields)
	}
	if got := state.issues["bd-linked"].Title; got != "Already linked" {
		t.Errorf("linked bead title = %q after dry-run, want %q", got, "Already linked")
	}

	live, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() real run error: %v", err)
	}
	if dry.PullStats.Created != live.PullStats.Created ||
		dry.PullStats.Updated != live.PullStats.Updated ||
		dry.PullStats.Skipped != live.PullStats.Skipped {
		t.Errorf("dry-run PullStats {Created:%d Updated:%d Skipped:%d} != real run {Created:%d Updated:%d Skipped:%d}",
			dry.PullStats.Created, dry.PullStats.Updated, dry.PullStats.Skipped,
			live.PullStats.Created, live.PullStats.Updated, live.PullStats.Skipped)
	}
}

// TestEngineCreateOnlyPullHoldsMapperMatchedBead pins the mapper-ID tier, the
// only one that can match an unlinked local (Notion's "Beads ID"): the
// link-repair update is held under --create-only, so neither the bead's
// content nor its external_ref is written, and no bead is created.
func TestEngineCreateOnlyPullHoldsMapperMatchedBead(t *testing.T) {
	ctx := context.Background()
	state := createOnlyState()
	tracker := newMockTracker("test")
	tracker.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
		return &IssueConversion{
			Issue: &types.Issue{
				ID:        "bd-fresh",
				Title:     ti.Title,
				Priority:  2,
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
			},
		}
	}}
	tracker.issues = []TrackerIssue{{
		ID:         "ext-fresh",
		Identifier: "EXT-FRESH",
		Title:      "Fresh remote title",
		State:      types.StatusOpen,
		Priority:   2,
	}}
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	fresh := state.issues["bd-fresh"]
	if fresh.Title != "Not yet linked" {
		t.Errorf("mapper-matched bead title = %q, want %q (left alone)", fresh.Title, "Not yet linked")
	}
	if fresh.ExternalRef != nil {
		t.Errorf("mapper-matched bead external_ref = %q, want none (no link repair under --create-only)", *fresh.ExternalRef)
	}
	if len(state.issues) != 2 {
		t.Errorf("issues = %d rows, want 2: no bead may be created for a matched remote issue", len(state.issues))
	}
	if result.PullStats.Created != 0 || result.PullStats.Updated != 0 || result.PullStats.Skipped != 1 {
		t.Errorf("PullStats = {Created:%d Updated:%d Skipped:%d}, want {0 0 1}",
			result.PullStats.Created, result.PullStats.Updated, result.PullStats.Skipped)
	}
}

// TestEngineCreateOnlyPullSelectiveHoldsNamedBead pins the AC-4 contract: a
// selective pull that names a linked bead ID (bd <tracker> sync --create-only
// --issues <id>) still holds the bead; selection narrows the fetch, it does
// not lift the hold.
func TestEngineCreateOnlyPullSelectiveHoldsNamedBead(t *testing.T) {
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

	result, err := engine.Sync(ctx, SyncOptions{Pull: true, CreateOnly: true, IssueIDs: []string{"bd-linked"}})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if got := state.issues["bd-linked"].Title; got != "Already linked" {
		t.Errorf("named bead title = %q, want %q (selection does not lift --create-only)", got, "Already linked")
	}
	if result.PullStats.Updated != 0 {
		t.Errorf("PullStats.Updated = %d, want 0", result.PullStats.Updated)
	}
	if result.PullStats.Skipped != 1 {
		t.Errorf("PullStats.Skipped = %d, want 1", result.PullStats.Skipped)
	}
}
