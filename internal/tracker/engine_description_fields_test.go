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

// TestEnginePullKeptLocalTextIsNotHashSkipped pins the push hash a
// kept-local settle stores. It fingerprints the remote as pulled, before the
// settle put the local text back, so a tracker with a ContentHash still sends
// the kept text: a hash of the settled issue would equal the local issue's,
// and the push would skip the issue as already pushed.
func TestEnginePullKeptLocalTextIsNotHashSkipped(t *testing.T) {
	settle, store, local := newSettleTestEngine(t, true)
	// The remote lacks the local notes, which is why the settle keeps them.
	settle.issues[0].Description = "DESC"
	settle.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			ID:          "bd-settle",
			Title:       ti.Title,
			Description: ti.Description,
			Priority:    2,
			Status:      types.StatusOpen,
			IssueType:   types.TypeTask,
		}}
	}}
	engine := NewEngine(settle, store, "sync")
	engine.PushHooks = &PushHooks{
		ContentEqual: func(l *types.Issue, r *TrackerIssue) bool {
			return l.Title == r.Title && l.Description == r.Description && l.Notes == ""
		},
		ContentHash: func(l *types.Issue) string { return l.Title + "|" + l.Description + "|" + l.Notes },
	}

	result, err := engine.Sync(context.Background(), SyncOptions{Pull: true, Push: true})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.PullStats.Updated != 1 || local.Title != "new title" || local.Notes != "N1" {
		t.Fatalf("PullStats = %+v, title %q, notes %q; want the remote title pulled and the local notes kept", result.PullStats, local.Title, local.Notes)
	}
	sent, ok := settle.updated["EXT-1"]
	if result.PushStats.Updated != 1 || !ok || sent.Notes != "N1" {
		t.Fatalf("PushStats = %+v, sent %+v; want the kept notes pushed once", result.PushStats, sent)
	}
}
