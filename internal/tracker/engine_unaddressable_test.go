package tracker

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// TestEngineWarnsOnceForUnaddressableRefs pins how a sync reports the refs a
// tracker recognizes but cannot address (gastownhall/beads#7781): a pull and a
// bulk push each warn in one line however many they skip, so a database
// linked to several repositories is not flooded, while a push that names
// beads by ID warns for each of them. A bulk pull takes a listed issue into
// the bead that links it under the ref the tracker builds in its configured
// target and imports the rest; a pull by ID skips the issue, listing it.
func TestEngineWarnsOnceForUnaddressableRefs(t *testing.T) {
	ctx := context.Background()
	bead := func(id, ref string) *types.Issue {
		return &types.Issue{
			ID: id, Title: id, Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2,
			ExternalRef: strPtr(ref),
		}
	}
	bind := func(beads ...*types.Issue) (*Engine, *engineUOWState, *mockTracker, *[]string) {
		state := &engineUOWState{
			issues:  map[string]*types.Issue{},
			configs: map[string]string{"issue_prefix": "bd"},
		}
		for _, b := range beads {
			state.issues[b.ID] = b
		}
		// The tracker recognizes every ref but cannot address one under
		// /foreign/, as the GitHub tracker treats another repository's
		// issue URL, and its fetch returns three such issues.
		mock := newMockTracker("test")
		mock.issues = []TrackerIssue{
			{ID: "3", Identifier: "EXT-3", URL: "https://test.test/foreign/EXT-3", Title: "Moved 3"},
			{ID: "4", Identifier: "EXT-4", URL: "https://test.test/foreign/EXT-4", Title: "Moved 4"},
			{ID: "5", Identifier: "EXT-5", URL: "https://test.test/foreign/EXT-5", Title: "Moved 5"},
		}
		tr := &mockExternalRefTracker{mockTracker: mock, extract: func(ref string) string {
			if strings.Contains(ref, "/foreign/") {
				return ""
			}
			return mock.ExtractIdentifier(ref)
		}}
		engine := NewEngine(tr, NewUOWStore(&engineUOWProvider{state: state}), "test-actor")
		engine.PullHooks = &PullHooks{GenerateID: func(_ context.Context, issue *types.Issue) error {
			issue.ID = "bd-" + strings.ToLower(strings.ReplaceAll(issue.Title, " ", "-"))
			return nil
		}}
		var warnings []string
		engine.OnWarning = func(msg string) { warnings = append(warnings, msg) }
		return engine, state, mock, &warnings
	}
	t.Run("pull", func(t *testing.T) {
		// The issue list returned EXT-3, EXT-4 and EXT-5, so they are the
		// target's own. bd-h3 and bd-h5 link EXT-3 and EXT-5 under the
		// configured spelling (BuildExternalRef without a URL), so the pull
		// takes those into them rather than import duplicates; nothing links
		// EXT-4.
		engine, state, _, warnings := bind(
			bead("bd-h3", "https://test.test/EXT-3"),
			bead("bd-h5", "https://test.test/EXT-5"),
		)
		result, err := engine.Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("Sync() error: %v", err)
		}
		if result.PullStats.Created != 1 || result.PullStats.Updated != 2 || result.PullStats.Skipped != 0 {
			t.Errorf("pull: created=%d updated=%d skipped=%d, want created=1 updated=2 skipped=0",
				result.PullStats.Created, result.PullStats.Updated, result.PullStats.Skipped)
		}
		if imported := state.issues["bd-moved-4"]; imported == nil || derefStr(imported.ExternalRef) != "https://test.test/foreign/EXT-4" {
			t.Errorf("EXT-4, which no bead links, was not imported under its own URL: %+v", imported)
		}
		if len(state.issues) != 3 {
			t.Errorf("store holds %d issues, want bd-h3, bd-h5 and the EXT-4 import", len(state.issues))
		}
		for id, title := range map[string]string{"bd-h3": "Moved 3", "bd-h5": "Moved 5"} {
			if got := state.issues[id]; got.Title != title {
				t.Errorf("%s title = %q, want %q", id, got.Title, title)
			}
		}
		want := []string{"3 test issues came back under URLs outside the configured test repository or project, " +
			"e.g. https://test.test/foreign/EXT-3"}
		if !reflect.DeepEqual(*warnings, want) {
			t.Errorf("pull warnings = %q, want %q", *warnings, want)
		}
	})

	t.Run("pull by ID", func(t *testing.T) {
		// An issue fetched on its own may have moved to another target, so a
		// pull by ID skips it, whether no bead links it or one links it
		// under the URL it came back under: that bead is linked outside the
		// target and was not asked for.
		for _, beads := range [][]*types.Issue{nil, {bead("bd-f4", "https://test.test/foreign/EXT-4")}} {
			engine, state, _, warnings := bind(beads...)
			result, err := engine.Sync(ctx, SyncOptions{Pull: true, IssueIDs: []string{"EXT-4"}})
			if err != nil {
				t.Fatalf("Sync() error: %v", err)
			}
			if result.PullStats.Created != 0 || result.PullStats.Updated != 0 || result.PullStats.Skipped != 1 || len(state.issues) != len(beads) {
				t.Errorf("pull of EXT-4 with %d beads: created=%d updated=%d skipped=%d issues=%d, want created=0 updated=0 skipped=1 issues=%d",
					len(beads), result.PullStats.Created, result.PullStats.Updated, result.PullStats.Skipped, len(state.issues), len(beads))
			}
			if got := state.issues["bd-f4"]; got != nil && got.Title != "bd-f4" {
				t.Errorf("bd-f4 was overwritten with title %q", got.Title)
			}
			want := []string{"Skipped test issue https://test.test/foreign/EXT-4: its URL is outside the configured test repository or project"}
			if !reflect.DeepEqual(*warnings, want) {
				t.Errorf("pull by ID warnings = %q, want %q", *warnings, want)
			}
		}
	})

	foreign := func() []*types.Issue {
		return []*types.Issue{bead("bd-f1", "https://test.test/foreign/EXT-1"), bead("bd-f2", "https://test.test/foreign/EXT-2")}
	}
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("bulk push dry-run=%v", dryRun), func(t *testing.T) {
			engine, _, _, warnings := bind(foreign()...)
			if _, err := engine.Sync(ctx, SyncOptions{Push: true, DryRun: dryRun}); err != nil {
				t.Fatalf("Sync() error: %v", err)
			}
			want := []string{"Skipped 2 local issues linked outside the configured test repository or project, e.g. bd-f1"}
			if !reflect.DeepEqual(*warnings, want) {
				t.Errorf("bulk push warnings = %q, want %q", *warnings, want)
			}
		})
	}

	t.Run("push by ID", func(t *testing.T) {
		engine, _, _, warnings := bind(foreign()...)
		if _, err := engine.Sync(ctx, SyncOptions{Push: true, IssueIDs: []string{"bd-f1", "bd-f2"}}); err != nil {
			t.Fatalf("Sync() error: %v", err)
		}
		want := []string{
			"Local issue bd-f1 is linked outside the configured test repository or project, skipping push",
			"Local issue bd-f2 is linked outside the configured test repository or project, skipping push",
		}
		if !reflect.DeepEqual(*warnings, want) {
			t.Errorf("push by ID warnings = %q, want %q", *warnings, want)
		}
	})

	// bd-h3 links EXT-3 in the configured target, but the tracker answers
	// that ref with the issue under its /foreign/ URL: the push must not
	// write the bead over an issue the pull skips.
	t.Run("push of an issue served under another name", func(t *testing.T) {
		engine, _, mock, warnings := bind(bead("bd-h3", "https://test.test/EXT-3"))
		result, err := engine.Sync(ctx, SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("Sync() error: %v", err)
		}
		if result.PushStats.Updated != 0 || result.PushStats.Skipped != 1 || len(mock.updated) != 0 {
			t.Errorf("push: updated=%d skipped=%d tracker updates=%v, want updated=0 skipped=1 and no update",
				result.PushStats.Updated, result.PushStats.Skipped, mock.updated)
		}
		want := []string{"Skipped pushing bd-h3: test serves its issue as https://test.test/foreign/EXT-3, " +
			"outside the configured test repository or project"}
		if !reflect.DeepEqual(*warnings, want) {
			t.Errorf("push warnings = %q, want %q", *warnings, want)
		}
	})

	// A tracker that could not learn its target's names cannot tell an issue
	// that left the target from one of its own under a name it missed, so
	// the push updates the issue of bd-h3, edited locally, as any other.
	t.Run("push of an issue served under another name, target names unknown", func(t *testing.T) {
		edited := bead("bd-h3", "https://test.test/EXT-3")
		edited.UpdatedAt = time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
		engine, _, mock, warnings := bind(edited)
		engine.Tracker = namesUnknownTracker{engine.Tracker}
		result, err := engine.Sync(ctx, SyncOptions{Push: true})
		if err != nil {
			t.Fatalf("Sync() error: %v", err)
		}
		if _, ok := mock.updated["EXT-3"]; !ok || result.PushStats.Updated != 1 || len(*warnings) != 0 {
			t.Errorf("push: updated=%d tracker updates=%v warnings=%q, want EXT-3 updated and no warning",
				result.PushStats.Updated, mock.updated, *warnings)
		}
	})
}

// namesUnknownTracker wraps a tracker that could not learn the names its
// configured target goes by (TargetNamesReporter).
type namesUnknownTracker struct{ IssueTracker }

func (namesUnknownTracker) TargetNamesKnown() bool { return false }

// TestEngineReportsUnaddressablePushSkipsOnRateLimitAbort pins that a push
// stopped by a provider rate limit still reports the beads it had already
// skipped as linked outside the configured target.
func TestEngineReportsUnaddressablePushSkipsOnRateLimitAbort(t *testing.T) {
	store := newPureTestStore(
		&types.Issue{ID: "bd-f1", Title: "Linked elsewhere", Status: types.StatusOpen, IssueType: types.TypeTask,
			ExternalRef: strPtr("https://test.test/foreign/EXT-1")},
		&types.Issue{ID: "bd-new", Title: "Not linked yet", Status: types.StatusOpen, IssueType: types.TypeTask},
	)
	mock := newMockTracker("test")
	mock.createErr = &fakeRateLimitError{retryAfter: time.Minute, msg: "secondary rate limit"}
	tr := &mockExternalRefTracker{mockTracker: mock, extract: func(ref string) string {
		if strings.Contains(ref, "/foreign/") {
			return ""
		}
		return mock.ExtractIdentifier(ref)
	}}
	engine := NewEngine(tr, store, "test-actor")
	var warnings []string
	engine.OnWarning = func(msg string) { warnings = append(warnings, msg) }

	if _, err := engine.Sync(context.Background(), SyncOptions{Push: true}); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	var aborted, reported bool
	for _, w := range warnings {
		aborted = aborted || strings.HasPrefix(w, "Aborting push to test")
		reported = reported || w == "Skipped local issue bd-f1: it is linked outside the configured test repository or project"
	}
	if !aborted || !reported {
		t.Errorf("warnings = %q, want the rate-limit abort and the skip of bd-f1", warnings)
	}
}
