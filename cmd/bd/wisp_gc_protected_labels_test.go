package main

import (
	"context"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// noInfraReader is an infraTypeReader for which no type is infrastructure, so
// the closed-purge filter's other branches are what the assertions measure.
type noInfraReader struct{}

func (noInfraReader) IsInfraTypeCtx(context.Context, types.IssueType) bool { return false }

// TestResolveProtectedWispLabels pins the precedence rule: a configured value
// REPLACES the built-in default (matching types.infra), config.yaml is consulted
// only when the DB value is unset, and --exclude-label always ADDS. Getting the
// precedence backwards would silently widen or narrow what a destructive
// command refuses to delete.
func TestResolveProtectedWispLabels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		dbValue string
		yaml    []string
		extra   []string
		want    []string
		absent  []string
	}{
		{
			name:   "unset everywhere falls back to the built-in default",
			want:   []string{"bd:protected"},
			absent: []string{"gt:message"},
		},
		{
			name:    "configured value replaces the default, it does not extend it",
			dbValue: "gt:message,gt:escalation",
			want:    []string{"gt:message", "gt:escalation"},
			absent:  []string{"bd:protected"},
		},
		{
			name:    "whitespace around a configured label is trimmed",
			dbValue: " gt:message ,  gt:escalation ",
			want:    []string{"gt:message", "gt:escalation"},
		},
		{
			name:   "config.yaml is the fallback when the DB value is unset",
			yaml:   []string{"yaml:keep"},
			want:   []string{"yaml:keep"},
			absent: []string{"bd:protected"},
		},
		{
			name:    "the DB value wins over config.yaml",
			dbValue: "db:keep",
			yaml:    []string{"yaml:keep"},
			want:    []string{"db:keep"},
			absent:  []string{"yaml:keep"},
		},
		{
			name:    "--exclude-label adds to the configured set",
			dbValue: "gt:message",
			extra:   []string{"ad:hoc"},
			want:    []string{"gt:message", "ad:hoc"},
		},
		{
			name:  "--exclude-label adds to the default set",
			extra: []string{"ad:hoc"},
			want:  []string{"bd:protected", "ad:hoc"},
		},
		{
			name:    "an all-whitespace config value reads as unset, not as no protection",
			dbValue: "  ,  ",
			want:    []string{"bd:protected"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveProtectedWispLabels(tc.dbValue, tc.yaml, tc.extra)
			for _, l := range tc.want {
				if !got[l] {
					t.Errorf("label %q should be protected; got set %v", l, got)
				}
			}
			for _, l := range tc.absent {
				if got[l] {
					t.Errorf("label %q should NOT be protected; got set %v", l, got)
				}
			}
		})
	}
}

// TestIsProtectedWispLabels is the unit-level regression for the hole this
// guard closes: an unread message or escalation stored as an ephemeral bead
// sits in plain open status, which every other protection in this predicate
// treats as reclaimable. Before the label check it was deleted by age alone.
func TestIsProtectedWispLabels(t *testing.T) {
	t.Parallel()

	guard := wispGuard{
		blocked:  map[string]bool{},
		statuses: map[types.Status]bool{types.StatusInProgress: true},
		labels:   map[string]bool{"gt:message": true},
	}

	unreadMail := &types.Issue{
		ID:     "gt-wisp-mail",
		Status: types.StatusOpen,
		Labels: []string{"gt:message", "gt:thread"},
	}
	if !isProtectedWisp(unreadMail, guard) {
		t.Error("an open wisp carrying a protected label must not be reclaimable by age")
	}

	// The guard must not degenerate into "protect everything": an ordinary
	// open wisp with unrelated labels stays a candidate.
	ordinary := &types.Issue{
		ID:     "gt-wisp-idle",
		Status: types.StatusOpen,
		Labels: []string{"gt:thread"},
	}
	if isProtectedWisp(ordinary, guard) {
		t.Error("an open wisp with no protected label must stay reclaimable")
	}

	// Label protection is independent of status protection: it must hold for a
	// closed wisp too, because --closed purges by status alone.
	closedMail := &types.Issue{
		ID:     "gt-wisp-read",
		Status: types.StatusClosed,
		Labels: []string{"gt:message"},
	}
	if !isProtectedWisp(closedMail, guard) {
		t.Error("a closed wisp carrying a protected label must not be reclaimable")
	}

	// A wisp with no labels at all must not panic or protect.
	if isProtectedWisp(&types.Issue{ID: "gt-wisp-bare", Status: types.StatusOpen}, guard) {
		t.Error("an unlabeled open wisp must stay reclaimable")
	}
}

// TestFilterClosedPurgeCandidatesCountsWhatItSkipped pins the reporting half:
// a guard that silently drops rows is indistinguishable from having had
// nothing to delete, so the purge must count each reason separately.
func TestFilterClosedPurgeCandidatesCountsWhatItSkipped(t *testing.T) {
	t.Parallel()

	issues := []*types.Issue{
		{ID: "keep-1", Status: types.StatusClosed},
		{ID: "pinned-1", Status: types.StatusClosed, Pinned: true},
		{ID: "labeled-1", Status: types.StatusClosed, Labels: []string{"gt:message"}},
		{ID: "labeled-2", Status: types.StatusClosed, Labels: []string{"other", "gt:message"}},
		{ID: "keep-2", Status: types.StatusClosed, Labels: []string{"other"}},
	}

	kept, skips := filterClosedPurgeCandidates(t.Context(), noInfraReader{}, issues, map[string]bool{"gt:message": true})

	var keptIDs []string
	for _, i := range kept {
		keptIDs = append(keptIDs, i.ID)
	}
	if len(kept) != 2 || keptIDs[0] != "keep-1" || keptIDs[1] != "keep-2" {
		t.Errorf("kept = %v, want [keep-1 keep-2]", keptIDs)
	}
	if skips.pinned != 1 {
		t.Errorf("skips.pinned = %d, want 1", skips.pinned)
	}
	if skips.labeled != 2 {
		t.Errorf("skips.labeled = %d, want 2", skips.labeled)
	}
	if skips.infra != 0 {
		t.Errorf("skips.infra = %d, want 0", skips.infra)
	}
}

// TestResolveProtectedWispLabelListIsSorted pins the ordering the list form
// promises. `bd purge` puts the resolved set on a sweep request that can cross
// an HTTP boundary and get logged, so Go's map iteration order would make an
// otherwise identical request serialize differently on every run.
//
// It also pins that the list and the set never disagree, which is the property
// that keeps `bd purge` and `bd mol wisp gc` honoring ONE guard rather than two
// that happen to be spelled the same.
func TestResolveProtectedWispLabelListIsSorted(t *testing.T) {
	got := resolveProtectedWispLabelList("zeta,alpha", nil, []string{"mid"})
	want := []string{"alpha", "mid", "zeta"}
	if !slices.Equal(got, want) {
		t.Fatalf("resolveProtectedWispLabelList = %v, want %v", got, want)
	}

	set := resolveProtectedWispLabels("zeta,alpha", nil, []string{"mid"})
	if len(set) != len(got) {
		t.Fatalf("set has %d entries, list has %d — the two forms must describe one set", len(set), len(got))
	}
	for _, l := range got {
		if !set[l] {
			t.Errorf("label %q is in the list and not in the set", l)
		}
	}
}
