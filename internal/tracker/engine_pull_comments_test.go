//go:build cgo

package tracker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// Regression coverage for the GitHub sync comment-loss incident: a pull that
// imports an issue must also import its comment thread, later comments must
// merge in without duplicating existing ones, and once the thread is fully
// imported the issue counts as unchanged (no perpetual updates).
func TestEnginePullImportsCommentThread(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	created := time.Now().Add(-24 * time.Hour)
	extURL := "https://github.test/acme/widgets/issues/9"

	thread := func(texts ...string) []*types.Comment {
		out := make([]*types.Comment, 0, len(texts))
		for i, text := range texts {
			out = append(out, &types.Comment{
				ID:        "gh-" + string(rune('a'+i)),
				Author:    "alice",
				Text:      text,
				CreatedAt: created.Add(time.Duration(i) * time.Minute),
			})
		}
		return out
	}

	makeTracker := func(comments []*types.Comment) *mockTracker {
		tr := newMockTracker("github")
		tr.issues = []TrackerIssue{{
			ID:         "ext-9",
			Identifier: "9",
			URL:        extURL,
			Title:      "Commented issue",
			// Old timestamp: keeps DetectConflicts out of the picture so the
			// assertions below exercise the plain pull paths deterministically.
			UpdatedAt: created,
		}}
		tr.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
			return &IssueConversion{
				Issue: &types.Issue{
					Title:       ti.Title,
					Description: ti.Description,
					Priority:    2,
					Status:      types.StatusOpen,
					IssueType:   types.TypeTask,
					Comments:    comments,
				},
			}
		}}
		return tr
	}

	// First sync creates the issue together with its one-comment thread.
	engine := NewEngine(makeTracker(thread("first")), store, "test-actor")
	result, err := engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() #1 error: %v", err)
	}
	if result.Stats.Created != 1 {
		t.Fatalf("Stats.Created = %d, want 1", result.Stats.Created)
	}
	issues, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues() error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("stored issues = %d, want 1", len(issues))
	}
	id := issues[0].ID

	assertComments := func(want int, phase string) {
		t.Helper()
		got, err := store.GetIssueComments(ctx, id)
		if err != nil {
			t.Fatalf("%s: GetIssueComments() error: %v", phase, err)
		}
		if len(got) != want {
			t.Fatalf("%s: comments = %d, want %d", phase, len(got), want)
		}
	}

	assertComments(1, "after create")

	// Second sync with a second remote comment: must take the update path and
	// merge exactly one new comment.
	engine = NewEngine(makeTracker(thread("first", "second")), store, "test-actor")
	result, err = engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() #2 error: %v", err)
	}
	if result.Stats.Updated != 1 {
		t.Errorf("#2 Stats.Updated = %d, want 1 (new comment pending import)", result.Stats.Updated)
	}
	assertComments(2, "after merge")

	// Third sync with no remote change: nothing pending, issue skipped — not
	// updated forever.
	engine = NewEngine(makeTracker(thread("first", "second")), store, "test-actor")
	result, err = engine.Sync(ctx, SyncOptions{Pull: true})
	if err != nil {
		t.Fatalf("Sync() #3 error: %v", err)
	}
	if result.Stats.Updated != 0 || result.Stats.Created != 0 {
		t.Errorf("#3 stats = {Updated:%d Created:%d}, want no writes",
			result.Stats.Updated, result.Stats.Created)
	}
	assertComments(2, "after settled re-pull")
}

// A push dry-run must reach the same verdict as a real push when there is no
// stored content hash: fetching the remote and comparing content. Without
// this, every freshly imported issue shows up as a phantom "Would update".
func TestEnginePushDryRunSkipsContentEqualRemote(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	issue := &types.Issue{
		ID:          "bd-dryphantom1",
		Title:       "Imported issue",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: strPtr("https://test.test/EXT-DP1"),
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("test")
	tracker.issues = []TrackerIssue{
		{ID: "EXT-DP1", Identifier: "EXT-DP1", URL: "https://test.test/EXT-DP1", Title: "Imported issue"},
	}

	var msgs []string
	engine := NewEngine(tracker, store, "test-actor")
	engine.PushHooks = &PushHooks{
		ContentEqual: func(local *types.Issue, remote *TrackerIssue) bool {
			return local.Title == remote.Title
		},
		FieldDiff: func(local *types.Issue, remote *TrackerIssue) []string {
			if local.Title == remote.Title {
				return nil
			}
			return []string{"title"}
		},
	}
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }

	result, err := engine.Sync(ctx, SyncOptions{Push: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Updated != 0 || result.Stats.Skipped != 1 {
		t.Errorf("stats = {Updated:%d Skipped:%d}, want {Updated:0 Skipped:1}",
			result.Stats.Updated, result.Stats.Skipped)
	}
	joined := strings.Join(msgs, "\n")
	if strings.Contains(joined, "Would update") {
		t.Errorf("messages = %q, want no phantom update preview", joined)
	}
	if len(tracker.updated) != 0 {
		t.Errorf("tracker.updated = %d, want 0 (dry-run must not write)", len(tracker.updated))
	}
}

// When content genuinely differs, the dry-run preview must say WHICH fields
// would change — including destructive label removals — instead of an opaque
// "would update" line.
func TestEnginePushDryRunDisclosesChangedFields(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	issue := &types.Issue{
		ID:          "bd-drydiff1",
		Title:       "Local title",
		Status:      types.StatusOpen,
		IssueType:   types.TypeTask,
		Priority:    2,
		ExternalRef: strPtr("https://test.test/EXT-DD1"),
	}
	if err := store.CreateIssue(ctx, issue, "test-actor"); err != nil {
		t.Fatalf("CreateIssue() error: %v", err)
	}

	tracker := newMockTracker("github")
	tracker.issues = []TrackerIssue{
		{ID: "EXT-DD1", Identifier: "EXT-DD1", URL: "https://test.test/EXT-DD1", Title: "Remote title"},
	}

	var msgs []string
	engine := NewEngine(tracker, store, "test-actor")
	engine.PushHooks = &PushHooks{
		ContentEqual: func(local *types.Issue, remote *TrackerIssue) bool {
			return local.Title == remote.Title
		},
		FieldDiff: func(local *types.Issue, remote *TrackerIssue) []string {
			if local.Title == remote.Title {
				return nil
			}
			return []string{"title", "labels (-2: bug, question)"}
		},
	}
	engine.OnMessage = func(msg string) { msgs = append(msgs, msg) }

	result, err := engine.Sync(ctx, SyncOptions{Push: true, DryRun: true})
	if err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	if result.Stats.Updated != 1 {
		t.Errorf("Stats.Updated = %d, want 1 (content differs)", result.Stats.Updated)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{"Would update in github", "[title, labels (-2: bug, question)]"} {
		if !strings.Contains(joined, want) {
			t.Errorf("messages = %q, want it to contain %q", joined, want)
		}
	}
}
