package main

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// protectedLabelsKey is the setting the guard reads, spelled out so these
// tests name the key operators type rather than a constant they cannot see.
const protectedLabelsKey = "wisp.protected_labels"

// collectedIDs returns the IDs of a GC selection as a set.
func collectedIDs(issues []*types.Issue) map[string]bool {
	ids := make(map[string]bool, len(issues))
	for _, issue := range issues {
		ids[issue.ID] = true
	}
	return ids
}

// TestWispGCProtectedLabelSparesAgeAndAllCandidates pins the defect behind
// wisp.protected_labels: a write-once record such as unread mail sits in plain
// open status, which is age-reclaimable by design, so age GC deleted it — and
// with --all deleted the closed ones too. A wisp carrying a configured label is
// never selected; unlabeled peers and a wisp carrying only an unrelated label
// stay reclaimable, so the guard cannot degenerate into protecting everything.
func TestWispGCProtectedLabelSparesAgeAndAllCandidates(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	openMail := &types.Issue{ID: "gc-open-mail", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"keep:mail"}}
	closedMail := &types.Issue{ID: "gc-closed-mail", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"thread", "keep:mail"}}
	openPlain := &types.Issue{ID: "gc-open-plain", Status: types.StatusOpen, UpdatedAt: stale}
	closedPlain := &types.Issue{ID: "gc-closed-plain", Status: types.StatusClosed, UpdatedAt: stale}
	otherLabel := &types.Issue{ID: "gc-other-label", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"keep:mail-archive", "KEEP:MAIL"}}

	r := &cascadeAgeReader{
		issues: []*types.Issue{openMail, closedMail, openPlain, closedPlain, otherLabel},
		config: map[string]string{protectedLabelsKey: "keep:mail"},
	}
	got, err := findAbandonedWisps(context.Background(), r, true, time.Hour, nil)
	if err != nil {
		t.Fatalf("findAbandonedWisps(--all): %v", err)
	}
	ids := collectedIDs(got)
	for _, id := range []string{openMail.ID, closedMail.ID} {
		if ids[id] {
			t.Errorf("wisp %s carries protected label keep:mail but was selected by age GC --all; selected=%v", id, ids)
		}
	}
	for _, id := range []string{openPlain.ID, closedPlain.ID, otherLabel.ID} {
		if !ids[id] {
			t.Errorf("wisp %s carries no protected label and must stay reclaimable by --all; selected=%v", id, ids)
		}
	}
}

// TestWispGCProtectedLabelUnsetProtectsNothing pins the empty default: core
// ships the extension point, not a policy. With the setting unset, blank or
// `[]`, a labeled wisp — including one labeled bd:protected — is reclaimed
// like any other.
func TestWispGCProtectedLabelUnsetProtectsNothing(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	for _, value := range []string{"", "  ", "[]"} {
		labeled := &types.Issue{ID: "gc-labeled", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"bd:protected", "keep:mail"}}
		r := &cascadeAgeReader{issues: []*types.Issue{labeled}, config: map[string]string{protectedLabelsKey: value}}
		got, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
		if err != nil {
			t.Fatalf("findAbandonedWisps with %s=%q: %v", protectedLabelsKey, value, err)
		}
		if !collectedIDs(got)[labeled.ID] {
			t.Errorf("with %s=%q nothing is protected, but the labeled wisp was not selected", protectedLabelsKey, value)
		}
	}
}
