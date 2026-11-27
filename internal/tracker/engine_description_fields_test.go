package tracker

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// settleDescriptionFieldsTracker is a mockTracker whose tracker splits
// description fields: the pull's converted issue carries the folded body,
// and SettleDescriptionFields copies the local fields back into it.
type settleDescriptionFieldsTracker struct {
	*mockTracker
	settled   int
	keptLocal bool
}

func (t *settleDescriptionFieldsTracker) SettleDescriptionFields(existing, pulled *types.Issue) (keptLocal bool) {
	t.settled++
	pulled.Description = existing.Description
	pulled.AcceptanceCriteria = existing.AcceptanceCriteria
	pulled.Design = existing.Design
	pulled.Notes = existing.Notes
	return t.keptLocal
}

// newSettleTestEngine returns an engine whose remote holds a folded body
// (description plus a notes section) and a title change, linked to local.
func newSettleTestEngine(t *testing.T, keptLocal bool) (*settleDescriptionFieldsTracker, *pullFailureStore, *types.Issue) {
	t.Helper()
	ref := "https://test.test/EXT-1"
	local := &types.Issue{
		ID:          "bd-settle",
		Title:       "old title",
		Description: "DESC",
		Notes:       "N1",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		// After the pull's write the local row is newer than the remote
		// (ApplyIssueUpdate stamps updated_at), so the push path's default
		// remote-newer skip does not fire and the kept-text push is visible.
		UpdatedAt: time.Date(2026, time.January, 2, 5, 4, 5, 0, time.UTC),
	}
	store := &pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullTestTransaction{issue: local},
	}
	settle := &settleDescriptionFieldsTracker{mockTracker: newMockTracker("test"), keptLocal: keptLocal}
	settle.issues = []TrackerIssue{{
		ID:         "EXT-1",
		Identifier: "EXT-1",
		URL:        ref,
		Title:      "new title",
		UpdatedAt:  time.Date(2026, time.January, 2, 4, 4, 5, 0, time.UTC),
	}}
	// The mapper produces what a pull of a folded body gives: the whole
	// body in Description, structured fields empty.
	settle.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			ID:          "bd-settle",
			Title:       "new title",
			Description: "DESC\n\n## Notes\nN1",
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
		}}
	}}
	return settle, store, local
}

// TestEnginePullSettlesDescriptionFields pins the engine wiring for bd
// #8506: a DescriptionFieldsTracker's settle step runs after conversion,
// the pull update carries the three description-field keys, and a settle
// that kept local text keeps the issue out of the pulled set so the push
// sends it.
func TestEnginePullSettlesDescriptionFields(t *testing.T) {
	ctx := context.Background()

	t.Run("kept local text is pushed", func(t *testing.T) {
		settle, store, local := newSettleTestEngine(t, true)

		result, err := NewEngine(settle, store, "sync").Sync(ctx, SyncOptions{Pull: true, Push: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if settle.settled != 1 {
			t.Fatalf("SettleDescriptionFields calls = %d, want 1", settle.settled)
		}
		if result.PullStats.Updated != 1 {
			t.Fatalf("PullStats = %+v, want Updated=1", result.PullStats)
		}
		if local.Title != "new title" {
			t.Fatalf("title = %q, want the remote title", local.Title)
		}
		if local.Description != "DESC" || local.Notes != "N1" {
			t.Fatalf("description fields after pull: description=%q notes=%q, want DESC/N1 (not the folded body)", local.Description, local.Notes)
		}
		if len(store.tx.appliedUpdates) != 1 {
			t.Fatalf("pull update count = %d, want 1", len(store.tx.appliedUpdates))
		}
		updates := store.tx.appliedUpdates[0]
		for _, key := range []string{"acceptance_criteria", "design", "notes"} {
			if _, ok := updates[key]; !ok {
				t.Fatalf("pull update missing %q: %v", key, updates)
			}
		}
		if updates["notes"] != "N1" || updates["acceptance_criteria"] != "" {
			t.Fatalf("pull update field values: notes=%v acceptance=%v, want N1/\"\"", updates["notes"], updates["acceptance_criteria"])
		}
		if result.PushStats.Updated != 1 || len(settle.updated) != 1 {
			t.Fatalf("push after kept-local settle: PushStats=%+v update calls=%d, want the kept text pushed", result.PushStats, len(settle.updated))
		}
	})

	t.Run("remote wins is not pushed", func(t *testing.T) {
		settle, store, local := newSettleTestEngine(t, false)

		result, err := NewEngine(settle, store, "sync").Sync(ctx, SyncOptions{Pull: true, Push: true})
		if err != nil {
			t.Fatalf("Sync: %v", err)
		}
		if settle.settled != 1 {
			t.Fatalf("SettleDescriptionFields calls = %d, want 1", settle.settled)
		}
		if local.Description != "DESC" || local.Notes != "N1" {
			t.Fatalf("description fields after pull: description=%q notes=%q, want DESC/N1", local.Description, local.Notes)
		}
		// The pull wrote the issue, so it is the pull's own echo: the push
		// must not send it again.
		if result.PushStats.Updated != 0 || len(settle.updated) != 0 {
			t.Fatalf("push after remote-wins settle: PushStats=%+v update calls=%d, want none", result.PushStats, len(settle.updated))
		}
	})
}

// TestEnginePullWithoutDescriptionFieldsTrackerIsUnchanged is a guard: a
// tracker without the capability keeps the legacy behavior — the converted
// description is written as-is and no description-field keys appear.
func TestEnginePullWithoutDescriptionFieldsTrackerIsUnchanged(t *testing.T) {
	ctx := context.Background()
	ref := "https://test.test/EXT-2"
	local := &types.Issue{
		ID:          "bd-plain",
		Title:       "old title",
		Description: "DESC",
		Notes:       "N1",
		Status:      types.StatusOpen,
		Priority:    2,
		IssueType:   types.TypeTask,
		ExternalRef: &ref,
		UpdatedAt:   time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
	store := &pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullTestTransaction{issue: local},
	}
	mock := newMockTracker("test")
	mock.issues = []TrackerIssue{{
		ID:         "EXT-2",
		Identifier: "EXT-2",
		URL:        ref,
		Title:      "new title",
		UpdatedAt:  time.Date(2026, time.January, 2, 4, 4, 5, 0, time.UTC),
	}}
	mock.fieldMapper = &mockMapper{issueToBeads: func(*TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			ID:          "bd-plain",
			Title:       "new title",
			Description: "DESC\n\n## Notes\nN1",
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
		}}
	}}

	result, err := NewEngine(mock, store, "sync").Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Updated != 1 {
		t.Fatalf("PullStats = %+v, want Updated=1", result.PullStats)
	}
	if local.Description != "DESC\n\n## Notes\nN1" {
		t.Fatalf("description = %q, want the folded body unchanged (no settle without the capability)", local.Description)
	}
	if local.Notes != "N1" {
		t.Fatalf("notes = %q, want N1 untouched", local.Notes)
	}
	for _, u := range store.tx.appliedUpdates {
		for _, key := range []string{"acceptance_criteria", "design", "notes"} {
			if _, ok := u[key]; ok {
				t.Fatalf("update without the capability carries %q: %v", key, u)
			}
		}
	}
}
