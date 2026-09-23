package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEngineDryRunHonorsCreateOnly pins the sequential push dry-run to the
// real run's --create-only gate (gastownhall/beads#6337): a linked issue that a
// real --create-only push skips must preview as skipped, not "Would update".
// Runs on the pure-Go UOW store so it needs no Dolt server.
func TestEngineDryRunHonorsCreateOnly(t *testing.T) {
	ctx := context.Background()
	state := &engineUOWState{
		issues: map[string]*types.Issue{
			"bd-linked": {
				ID:          "bd-linked",
				Title:       "Already linked",
				Status:      types.StatusOpen,
				IssueType:   types.TypeTask,
				Priority:    2,
				ExternalRef: strPtr("https://test.test/EXT-LINKED"),
			},
			"bd-fresh": {
				ID:        "bd-fresh",
				Title:     "Not yet linked",
				Status:    types.StatusOpen,
				IssueType: types.TypeTask,
				Priority:  2,
			},
		},
		configs: map[string]string{"issue_prefix": "bd"},
	}
	tracker := newMockTracker("test")
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	var msgs []string
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }

	dry, err := engine.Sync(ctx, SyncOptions{Push: true, DryRun: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() dry-run error: %v", err)
	}
	joined := strings.Join(msgs, "\n")
	if strings.Contains(joined, "Would update") {
		t.Errorf("dry-run messages = %q, did not expect an update preview under --create-only", joined)
	}
	if !strings.Contains(joined, "Would create in test: Not yet linked") {
		t.Errorf("dry-run messages = %q, want create preview for the unlinked issue", joined)
	}
	if tracker.fetchCalls != 0 || len(tracker.created) != 0 || len(tracker.updated) != 0 {
		t.Fatalf("dry-run touched the tracker: fetch=%d created=%d updated=%d",
			tracker.fetchCalls, len(tracker.created), len(tracker.updated))
	}

	live, err := engine.Sync(ctx, SyncOptions{Push: true, CreateOnly: true})
	if err != nil {
		t.Fatalf("Sync() real run error: %v", err)
	}
	if len(tracker.updated) != 0 {
		t.Errorf("real run tracker.updated = %d, want 0 under --create-only", len(tracker.updated))
	}

	want := SyncStats{Created: 1, Updated: 0, Skipped: 1}
	for name, got := range map[string]SyncStats{"dry-run": dry.Stats, "real run": live.Stats} {
		if got.Created != want.Created || got.Updated != want.Updated || got.Skipped != want.Skipped {
			t.Errorf("%s stats = {Created:%d Updated:%d Skipped:%d}, want {Created:%d Updated:%d Skipped:%d}",
				name, got.Created, got.Updated, got.Skipped, want.Created, want.Updated, want.Skipped)
		}
	}
}
