package main

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

// The protected-label guard for wisp GC (bd-961adp): a wisp carrying a label
// the workspace named in wisp.protected_labels is never reclaimed by
// `bd mol wisp gc`, while unlabeled peers stay reclaimable — both directions
// are asserted everywhere, so the guard cannot degenerate into protecting
// everything or nothing.
//
// The fakes wrap cascadeAgeReader (the molReader fake the cascade-age tests
// use) and answer GetConfig, the one read the guard adds. Protection is
// workspace policy in the database, per the approved design: there is no
// built-in label (a wisp carrying only bd:protected stays eligible), and a
// YAML or environment value is ignored in favor of the stored setting.

// protectedLabelsReader serves findAbandonedWisps a stored
// wisp.protected_labels value beside cascadeAgeReader's fixed rows.
type protectedLabelsReader struct {
	*cascadeAgeReader
	config map[string]string
}

func (r protectedLabelsReader) GetConfig(_ context.Context, key string) (string, error) {
	return r.config[key], nil
}

// newProtectedLabelsReader builds a reader whose search returns issues and
// whose wisp.protected_labels setting is setting.
func newProtectedLabelsReader(setting string, issues ...*types.Issue) protectedLabelsReader {
	return protectedLabelsReader{
		cascadeAgeReader: &cascadeAgeReader{issues: issues},
		config:           map[string]string{workapi.SettingKeyWispProtectedLabels: setting},
	}
}

func collectedIDs(t *testing.T, sel wispGCSelection) map[string]bool {
	t.Helper()
	ids := make(map[string]bool, len(sel.abandoned))
	for _, issue := range sel.abandoned {
		ids[issue.ID] = true
	}
	return ids
}

// TestWispGCAgeSparesProtectedLabel reproduces bd-961adp at the selection
// level: with the workspace protecting keep:mail, `wisp gc --age` selected a
// stale open wisp carrying that label — exactly where unread mail and open
// escalations sit, so the longer a message went unread, the more certainly GC
// deleted it. The labeled wisp must be spared and its unlabeled stale sibling
// reclaimed.
//
// (Adapted from the triage repro, which expected a built-in bd:protected
// default; the approved design made protection explicit workspace policy, so
// the label is configured through the reader instead of implied.)
func TestWispGCAgeSparesProtectedLabel(t *testing.T) {
	now := time.Now()
	stale := now.Add(-24 * time.Hour)
	protected := &types.Issue{ID: "gc-protected", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"keep:mail"}}
	plain := &types.Issue{ID: "gc-plain", Status: types.StatusOpen, UpdatedAt: stale}

	r := newProtectedLabelsReader(`["keep:mail"]`, protected, plain)
	sel, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
	if err != nil {
		t.Fatalf("findAbandonedWisps: %v", err)
	}
	ids := collectedIDs(t, sel)
	if ids["gc-protected"] {
		t.Errorf("wisp carrying protected label keep:mail was selected for age GC; want it spared (got %v)", ids)
	}
	if !ids["gc-plain"] {
		t.Errorf("unlabeled stale wisp not selected; want it reclaimable (got %v)", ids)
	}
}

// TestWispGCAgeUnrelatedLabelDoesNotProtect pins the exactness of the match:
// a label the workspace did not name protects nothing, case is significant,
// and neither is a label a substring of.
func TestWispGCAgeUnrelatedLabelDoesNotProtect(t *testing.T) {
	now := time.Now()
	stale := now.Add(-24 * time.Hour)
	for _, tc := range []struct {
		name    string
		setting string
		labels  []string
	}{
		{name: "unrelated label", setting: `["keep:mail"]`, labels: []string{"other"}},
		{name: "wrong case", setting: `["Keep:Mail"]`, labels: []string{"keep:mail"}},
		{name: "substring is not a match", setting: `["keep:mail"]`, labels: []string{"keep:mailbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wisp := &types.Issue{ID: "gc-labeled", Status: types.StatusOpen, UpdatedAt: stale, Labels: tc.labels}
			r := newProtectedLabelsReader(tc.setting, wisp)
			sel, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
			if err != nil {
				t.Fatalf("findAbandonedWisps: %v", err)
			}
			if !collectedIDs(t, sel)["gc-labeled"] {
				t.Errorf("wisp with labels %v under setting %s was spared; want it reclaimable", tc.labels, tc.setting)
			}
		})
	}
}

// TestWispGCAgeUnsetProtectedLabelsProtectNothing covers the unset and empty
// settings: missing, blank and [] all mean no configured protection, so a
// labeled wisp stays reclaimable until the workspace opts in.
func TestWispGCAgeUnsetProtectedLabelsProtectNothing(t *testing.T) {
	now := time.Now()
	stale := now.Add(-24 * time.Hour)
	for _, setting := range []string{"", "   ", "[]"} {
		wisp := &types.Issue{ID: "gc-labeled", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"keep:mail"}}
		r := newProtectedLabelsReader(setting, wisp)
		sel, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
		if err != nil {
			t.Fatalf("findAbandonedWisps with setting %q: %v", setting, err)
		}
		if !collectedIDs(t, sel)["gc-labeled"] {
			t.Errorf("wisp was spared by setting %q; unset/blank/[] must protect nothing", setting)
		}
	}
}

// TestWispGCAgeInvalidProtectedLabelsAborts pins the fail-closed read: a
// stored value that cannot be parsed aborts the sweep with an error rather
// than reading as "no labels configured" — an unreadable setting must not
// reclaim exactly the records it was meant to keep.
func TestWispGCAgeInvalidProtectedLabelsAborts(t *testing.T) {
	now := time.Now()
	stale := now.Add(-24 * time.Hour)
	for _, setting := range []string{
		`["keep:mail"`,  // malformed JSON array
		`[1,2]`,         // not an array of strings
		`[""]`,          // names no label
		`,`,             // names no label
		`["keep mail"]`, // internal whitespace
		`keep mail,a`,   // internal whitespace in CSV form
	} {
		wisp := &types.Issue{ID: "gc-labeled", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"keep:mail"}}
		r := newProtectedLabelsReader(setting, wisp)
		sel, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
		if err == nil {
			t.Errorf("setting %q: findAbandonedWisps succeeded with %d candidates; want an abort error", setting, len(sel.abandoned))
		}
		if len(sel.abandoned) != 0 {
			t.Errorf("setting %q: selection is not empty after abort (got %d candidates)", setting, len(sel.abandoned))
		}
	}
}

// TestWispGCAgeCountsLabelSpares pins what the skip report counts: a wisp the
// labels alone spared. A labeled wisp too young to select was saved by age,
// one also pinned was saved by another guard, and a closed one without --all
// was never a candidate — none of them is a label save, or the count would
// overstate the setting's reach and read as misconfiguration.
func TestWispGCAgeCountsLabelSpares(t *testing.T) {
	now := time.Now()
	stale := now.Add(-24 * time.Hour)
	fresh := now.Add(-2 * time.Second)
	issues := []*types.Issue{
		{ID: "gc-plain", Status: types.StatusOpen, UpdatedAt: stale},
		{ID: "gc-labeled-old", Status: types.StatusOpen, UpdatedAt: stale, Labels: []string{"keep:mail"}},
		{ID: "gc-labeled-young", Status: types.StatusOpen, UpdatedAt: fresh, Labels: []string{"keep:mail"}},
		{ID: "gc-pinned-labeled", Status: types.StatusOpen, UpdatedAt: stale, Pinned: true, Labels: []string{"keep:mail"}},
		{ID: "gc-closed-labeled", Status: types.StatusClosed, UpdatedAt: stale, Labels: []string{"keep:mail"}},
	}
	r := newProtectedLabelsReader(`["keep:mail"]`, issues...)
	sel, err := findAbandonedWisps(context.Background(), r, false, time.Hour, nil)
	if err != nil {
		t.Fatalf("findAbandonedWisps: %v", err)
	}
	if sel.labelSpared != 1 {
		t.Errorf("labelSpared = %d, want 1 (only gc-labeled-old was saved by the label alone); selection=%+v", sel.labelSpared, sel)
	}
	ids := collectedIDs(t, sel)
	if !ids["gc-plain"] {
		t.Errorf("unlabeled stale wisp not selected; want it reclaimable (got %v)", ids)
	}
}
