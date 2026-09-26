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
	if tracker.fetchCalls != 0 {
		t.Errorf("real run tracker.fetchCalls = %d, want 0: --create-only skips linked issues before any fetch", tracker.fetchCalls)
	}

	want := SyncStats{Created: 1, Updated: 0, Skipped: 1}
	for name, got := range map[string]SyncStats{"dry-run": dry.Stats, "real run": live.Stats} {
		if got.Created != want.Created || got.Updated != want.Updated || got.Skipped != want.Skipped {
			t.Errorf("%s stats = {Created:%d Updated:%d Skipped:%d}, want {Created:%d Updated:%d Skipped:%d}",
				name, got.Created, got.Updated, got.Skipped, want.Created, want.Updated, want.Skipped)
		}
	}
}

// createOnlyState returns one linked and one unlinked open task.
func createOnlyState() *engineUOWState {
	return &engineUOWState{
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
}

// TestEngineDryRunCreateOnlyForcedLinkedUpdates pins the forced-linked case of
// the #6337 gate: a linked issue in forceIDs (conflict resolved in favor of the
// local copy) is pushed by a real --create-only run, so the preview must show
// it as an update, not a skip.
func TestEngineDryRunCreateOnlyForcedLinkedUpdates(t *testing.T) {
	ctx := context.Background()
	tracker := newMockTracker("test")
	engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: createOnlyState()}), "test-actor")

	var msgs []string
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }
	force := map[string]bool{"bd-linked": true}

	dry, err := engine.doPush(ctx, SyncOptions{Push: true, DryRun: true, CreateOnly: true}, map[string]bool{}, force)
	if err != nil {
		t.Fatalf("doPush() dry-run error: %v", err)
	}
	if joined := strings.Join(msgs, "\n"); !strings.Contains(joined, "Would update in test: Already linked") {
		t.Errorf("dry-run messages = %q, want an update preview for the forced linked issue", joined)
	}
	if tracker.fetchCalls != 0 || len(tracker.created) != 0 || len(tracker.updated) != 0 {
		t.Fatalf("dry-run touched the tracker: fetch=%d created=%d updated=%d",
			tracker.fetchCalls, len(tracker.created), len(tracker.updated))
	}

	live, err := engine.doPush(ctx, SyncOptions{Push: true, CreateOnly: true}, map[string]bool{}, force)
	if err != nil {
		t.Fatalf("doPush() real run error: %v", err)
	}
	if _, ok := tracker.updated["EXT-LINKED"]; !ok || len(tracker.updated) != 1 {
		t.Errorf("real run tracker.updated = %v, want only EXT-LINKED", tracker.updated)
	}
	if tracker.fetchCalls != 0 {
		t.Errorf("real run tracker.fetchCalls = %d, want 0: forced pushes skip the freshness fetch", tracker.fetchCalls)
	}

	want := PushStats{Created: 1, Updated: 1, Skipped: 0}
	for name, got := range map[string]*PushStats{"dry-run": dry, "real run": live} {
		if got.Created != want.Created || got.Updated != want.Updated || got.Skipped != want.Skipped {
			t.Errorf("%s stats = {Created:%d Updated:%d Skipped:%d}, want {Created:%d Updated:%d Skipped:%d}",
				name, got.Created, got.Updated, got.Skipped, want.Created, want.Updated, want.Skipped)
		}
	}
}

// batchPushOnlyTracker is Linear-shaped: it implements BatchPushTracker but not
// BatchPushDryRunner, so a push dry-run falls through to the sequential loop.
type batchPushOnlyTracker struct {
	*mockTracker
	batchCalls int
}

func (m *batchPushOnlyTracker) BatchPush(_ context.Context, _ []*types.Issue, _ map[string]bool) (*BatchPushResult, error) {
	m.batchCalls++
	return &BatchPushResult{}, nil
}

var (
	_ BatchPushTracker = (*batchPushOnlyTracker)(nil)
	_ IssueTracker     = (*batchPushOnlyTracker)(nil)
)

// TestEngineBatchFallThroughDryRunCountsEachIssueOnce pins the dry-run fall
// through for batch trackers without a batch dry-runner (gastownhall/beads#6712
// review): collectBatchPushIssues already counts its skips, so the sequential
// preview must not count those issues again.
func TestEngineBatchFallThroughDryRunCountsEachIssueOnce(t *testing.T) {
	if _, ok := IssueTracker(&batchPushOnlyTracker{}).(BatchPushDryRunner); ok {
		t.Fatal("batchPushOnlyTracker must not implement BatchPushDryRunner")
	}

	cases := []struct {
		name  string
		opts  SyncOptions
		hooks *PushHooks
		want  PushStats
		msg   string
	}{
		{
			name: "create-only skips linked issue",
			opts: SyncOptions{Push: true, DryRun: true, CreateOnly: true},
			want: PushStats{Created: 1, Updated: 0, Skipped: 1},
			msg:  "Would create in test: Not yet linked",
		},
		{
			name:  "ShouldPush hook skips linked issue",
			opts:  SyncOptions{Push: true, DryRun: true},
			hooks: &PushHooks{ShouldPush: func(issue *types.Issue) bool { return issue.ID != "bd-linked" }},
			want:  PushStats{Created: 1, Updated: 0, Skipped: 1},
			msg:   "Would create in test: Not yet linked",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tracker := &batchPushOnlyTracker{mockTracker: newMockTracker("test")}
			engine := NewEngine(tracker, NewUOWStore(&engineUOWProvider{state: createOnlyState()}), "test-actor")
			engine.PushHooks = tc.hooks

			var msgs []string
			engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }

			got, err := engine.doPush(ctx, tc.opts, map[string]bool{}, map[string]bool{})
			if err != nil {
				t.Fatalf("doPush() dry-run error: %v", err)
			}
			if got.Created != tc.want.Created || got.Updated != tc.want.Updated || got.Skipped != tc.want.Skipped {
				t.Errorf("stats = {Created:%d Updated:%d Skipped:%d}, want {Created:%d Updated:%d Skipped:%d}",
					got.Created, got.Updated, got.Skipped, tc.want.Created, tc.want.Updated, tc.want.Skipped)
			}
			if total := got.Created + got.Updated + got.Skipped + got.Errors; total != 2 {
				t.Errorf("total outcomes = %d, want 2 (one per issue)", total)
			}
			joined := strings.Join(msgs, "\n")
			if !strings.Contains(joined, tc.msg) {
				t.Errorf("dry-run messages = %q, want %q", joined, tc.msg)
			}
			if strings.Contains(joined, "Would update") {
				t.Errorf("dry-run messages = %q, did not expect an update preview", joined)
			}
			if tracker.batchCalls != 0 || tracker.fetchCalls != 0 || len(tracker.created) != 0 || len(tracker.updated) != 0 {
				t.Fatalf("dry-run touched the tracker: batch=%d fetch=%d created=%d updated=%d",
					tracker.batchCalls, tracker.fetchCalls, len(tracker.created), len(tracker.updated))
			}
		})
	}
}
