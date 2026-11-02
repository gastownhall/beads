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

// TestWispGCProtectedLabelSparesCascadeDependents pins the second filter site.
// The cascade expansion reaches dependents the parent search never selected —
// without --all it collects an abandoned parent's closed steps — so a label
// guard on the search alone would let a protected dependent go down with its
// parent. Protection is per record: a protected intermediate does not shield
// an unprotected wisp further down the chain, which recursion has already
// reached.
func TestWispGCProtectedLabelSparesCascadeDependents(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	parent := &types.Issue{ID: "gc-parent", Status: types.StatusOpen, UpdatedAt: stale}
	keptStep := &types.Issue{ID: "gc-kept-step", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"keep:mail"}}
	plainStep := &types.Issue{ID: "gc-plain-step", Status: types.StatusClosed, UpdatedAt: stale}
	behindKept := &types.Issue{ID: "gc-behind-kept", Status: types.StatusClosed, UpdatedAt: stale}

	r := &cascadeAgeReader{
		issues:   []*types.Issue{parent, keptStep, plainStep, behindKept},
		children: map[string]bool{keptStep.ID: true, plainStep.ID: true, behindKept.ID: true},
		config:   map[string]string{protectedLabelsKey: `["keep:mail"]`},
	}
	got, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
	if err != nil {
		t.Fatalf("findAbandonedWisps: %v", err)
	}
	ids := collectedIDs(got)
	if ids[keptStep.ID] {
		t.Errorf("closed dependent %s carries protected label keep:mail but was cascaded into GC; selected=%v", keptStep.ID, ids)
	}
	for _, id := range []string{parent.ID, plainStep.ID, behindKept.ID} {
		if !ids[id] {
			t.Errorf("unprotected wisp %s must still be collected (parent or cascade dependent); selected=%v", id, ids)
		}
	}
}

// TestWispGCClosedPurgeSparesProtectedLabels pins the closed purge, shared by
// the direct and proxied routes: --closed selects purely by status, so without
// the label check a protected record would only need closing to be deleted.
// Pinned and infra wisps keep their own counts; a wisp a label alone saves is
// counted as a label skip.
func TestWispGCClosedPurgeSparesProtectedLabels(t *testing.T) {
	closedMail := &types.Issue{ID: "gc-closed-mail", Status: types.StatusClosed, Labels: []string{"keep:mail"}}
	closedAlert := &types.Issue{ID: "gc-closed-alert", Status: types.StatusClosed, Labels: []string{"thread", "keep:alert"}}
	closedPlain := &types.Issue{ID: "gc-closed-plain", Status: types.StatusClosed}
	closedOther := &types.Issue{ID: "gc-closed-other", Status: types.StatusClosed, Labels: []string{"thread"}}
	pinnedMail := &types.Issue{ID: "gc-pinned-mail", Status: types.StatusClosed, Pinned: true, Labels: []string{"keep:mail"}}

	r := &cascadeAgeReader{
		issues: []*types.Issue{closedMail, closedAlert, closedPlain, closedOther, pinnedMail},
		config: map[string]string{protectedLabelsKey: "keep:mail,keep:alert"},
	}
	got, skips, err := selectClosedPurgeCandidates(context.Background(), r, nil, nil)
	if err != nil {
		t.Fatalf("selectClosedPurgeCandidates: %v", err)
	}
	ids := collectedIDs(got)
	want := map[string]bool{closedPlain.ID: true, closedOther.ID: true}
	if len(ids) != len(want) || !ids[closedPlain.ID] || !ids[closedOther.ID] {
		t.Errorf("closed purge candidates = %v, want only the unprotected %v", ids, want)
	}
	if skips.labeled != 2 || skips.pinned != 1 {
		t.Errorf("closed purge skips = %+v, want 2 label skips and 1 pinned (a pinned wisp counts as pinned)", skips)
	}
}

// TestWispGCExcludeLabelAddsToSetting pins that --exclude-label adds to the
// stored setting rather than replacing it, in age GC and in the closed purge:
// a wisp carrying either label is spared. bd:protected is not a default, so a
// wisp carrying only it stays eligible.
func TestWispGCExcludeLabelAddsToSetting(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	fromSetting := &types.Issue{ID: "gc-setting", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"a"}}
	fromFlag := &types.Issue{ID: "gc-flag", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"b"}}
	defaultLabel := &types.Issue{ID: "gc-default", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"bd:protected"}}
	plain := &types.Issue{ID: "gc-plain", Status: types.StatusClosed, UpdatedAt: stale}

	r := &cascadeAgeReader{
		issues: []*types.Issue{fromSetting, fromFlag, defaultLabel, plain},
		config: map[string]string{protectedLabelsKey: "a"},
	}
	extra, err := parseExcludeLabels([]string{" b ", ""})
	if err != nil {
		t.Fatalf("parseExcludeLabels: %v", err)
	}
	aged, _, err := selectAbandonedWisps(context.Background(), r, true, time.Hour, nil, extra)
	if err != nil {
		t.Fatalf("selectAbandonedWisps: %v", err)
	}
	closed, _, err := selectClosedPurgeCandidates(context.Background(), r, nil, extra)
	if err != nil {
		t.Fatalf("selectClosedPurgeCandidates: %v", err)
	}
	for mode, got := range map[string][]*types.Issue{"age --all": aged, "--closed": closed} {
		ids := collectedIDs(got)
		if len(ids) != 2 || !ids[defaultLabel.ID] || !ids[plain.ID] {
			t.Errorf("%s selected %v, want exactly %s and %s (setting a plus --exclude-label b protect the others)",
				mode, ids, defaultLabel.ID, plain.ID)
		}
	}

	if _, err := parseExcludeLabels([]string{"keep mail"}); err == nil {
		t.Errorf("parseExcludeLabels accepted a name containing whitespace")
	}
}

// TestWispGCLabelSkipCountIsDistinct pins the count behind "Skipping N
// wisp(s) carrying a protected label": distinct wisps a label alone held back.
// A labeled wisp the sweep reached and the cascade reached again counts once,
// and a closed one only the cascade reached counts. One too young, one live
// work or a pin protects anyway, and a closed one the sweep passes over
// without --all (no parent reaches it), is not a label skip: counting them
// would overstate the setting's reach and read as misconfiguration.
func TestWispGCLabelSkipCountIsDistinct(t *testing.T) {
	stale := time.Now().Add(-24 * time.Hour)
	parent := &types.Issue{ID: "gc-parent", Status: types.StatusOpen, UpdatedAt: stale}
	twice := &types.Issue{ID: "gc-twice", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"keep:mail"}}
	step := &types.Issue{ID: "gc-step", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"keep:mail"}}
	young := &types.Issue{ID: "gc-young", Status: types.StatusOpen, UpdatedAt: time.Now(), Labels: []string{"keep:mail"}}
	live := &types.Issue{ID: "gc-live", Status: types.StatusInProgress, UpdatedAt: stale, Labels: []string{"keep:mail"}}
	pinned := &types.Issue{ID: "gc-pinned", Status: types.StatusOpen, UpdatedAt: stale, Pinned: true, Labels: []string{"keep:mail"}}
	closed := &types.Issue{ID: "gc-closed", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"keep:mail"}}

	r := &cascadeAgeReader{
		issues:   []*types.Issue{parent, twice, step, young, live, pinned, closed},
		children: map[string]bool{twice.ID: true, step.ID: true},
		config:   map[string]string{protectedLabelsKey: "keep:mail"},
	}
	got, skips, err := selectAbandonedWisps(context.Background(), r, false, time.Hour, nil, nil)
	if err != nil {
		t.Fatalf("selectAbandonedWisps: %v", err)
	}
	if ids := collectedIDs(got); len(ids) != 1 || !ids[parent.ID] {
		t.Errorf("selected %v, want only %s", ids, parent.ID)
	}
	if skips != 2 {
		t.Errorf("label skips = %d, want 2 (%s once though reached twice, and %s)", skips, twice.ID, step.ID)
	}
}
