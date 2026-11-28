package tracker

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEngineDryRunSkipsUnaddressableRef pins the sequential push dry run to
// the real run's skip of a recognized ref the tracker cannot address
// (gastownhall/beads#7781): the preview must count it skipped, not "Would
// update", and its counts must equal the real run's.
func TestEngineDryRunSkipsUnaddressableRef(t *testing.T) {
	ctx := context.Background()
	state := &engineUOWState{
		issues: map[string]*types.Issue{
			"bd-foreign": {
				ID:          "bd-foreign",
				Title:       "Linked elsewhere",
				Status:      types.StatusOpen,
				IssueType:   types.TypeTask,
				Priority:    2,
				ExternalRef: strPtr("https://test.test/foreign/EXT-1"),
			},
			"bd-local": {
				ID:          "bd-local",
				Title:       "Linked here",
				Status:      types.StatusOpen,
				IssueType:   types.TypeTask,
				Priority:    2,
				ExternalRef: strPtr("https://test.test/EXT-2"),
			},
		},
		configs: map[string]string{"issue_prefix": "bd"},
	}
	// The tracker recognizes every ref but cannot address one under
	// /foreign/, as the GitHub tracker treats another repository's issue URL.
	mock := newMockTracker("test")
	tr := &mockExternalRefTracker{mockTracker: mock, extract: func(ref string) string {
		if strings.Contains(ref, "/foreign/") {
			return ""
		}
		return mock.ExtractIdentifier(ref)
	}}
	engine := NewEngine(tr, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")

	var msgs []string
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }

	dry, err := engine.Sync(ctx, SyncOptions{Push: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync() dry-run error: %v", err)
	}
	joined := strings.Join(msgs, "\n")
	if strings.Contains(joined, "Linked elsewhere") {
		t.Errorf("dry-run messages = %q, want no preview for the unaddressable issue", joined)
	}
	if !strings.Contains(joined, "Would update in test: Linked here") {
		t.Errorf("dry-run messages = %q, want an update preview for the addressable issue", joined)
	}

	live, err := engine.Sync(ctx, SyncOptions{Push: true})
	if err != nil {
		t.Fatalf("Sync() real run error: %v", err)
	}
	if _, ok := mock.updated["EXT-1"]; ok || len(mock.updated) != 1 {
		t.Errorf("real run updated %v, want only EXT-2", mock.updated)
	}

	want := SyncStats{Created: 0, Updated: 1, Skipped: 1}
	for name, got := range map[string]SyncStats{"dry-run": dry.Stats, "real run": live.Stats} {
		if got.Created != want.Created || got.Updated != want.Updated || got.Skipped != want.Skipped {
			t.Errorf("%s stats = {Created:%d Updated:%d Skipped:%d}, want {Created:%d Updated:%d Skipped:%d}",
				name, got.Created, got.Updated, got.Skipped, want.Created, want.Updated, want.Skipped)
		}
	}
}
