package tracker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

const pushHashTestRef = "https://test.test/EXT-1"

// pullHashStore adds CreateIssue to pullFailureStore so a pull can reach its
// create branch: the embedded nil storage.Storage panics on create. It also
// counts the writes of push hash entries.
type pullHashStore struct {
	*pullFailureStore
	pushHashWrites int
}

func (s *pullHashStore) SetLocalMetadata(ctx context.Context, key, value string) error {
	if strings.Contains(key, ".pushhash.") {
		s.pushHashWrites++
	}
	return s.pullFailureStore.SetLocalMetadata(ctx, key, value)
}

func (s *pullHashStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	if issue.ID == "" {
		issue.ID = fmt.Sprintf("bd-created%d", len(s.issues))
	}
	s.issues = append(s.issues, issue)
	return nil
}

type pushHashFixture struct {
	local  *types.Issue
	store  *pullHashStore
	mock   *mockTracker
	engine *Engine
}

// newPushHashFixture links local issue bd-hash1 ("pushed") to remote EXT-1
// ("pushed"). The local UpdatedAt is in the past so the pull's conflict guard
// never skips it after an earlier sync writes last_sync.
func newPushHashFixture() *pushHashFixture {
	ref := pushHashTestRef
	past := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	local := &types.Issue{
		ID: "bd-hash1", Title: "pushed", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, UpdatedAt: past, ExternalRef: &ref,
	}
	store := &pullHashStore{pullFailureStore: &pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullTestTransaction{issue: local},
	}}
	mock := newMockTracker("test")
	mock.issues = []TrackerIssue{{
		ID: "EXT-1", Identifier: "EXT-1", URL: ref, Title: "pushed", UpdatedAt: past,
	}}
	mock.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			Title: ti.Title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}}
	}}
	engine := NewEngine(mock, store, "sync")
	engine.PushHooks = titlePushHooks()
	return &pushHashFixture{local: local, store: store, mock: mock, engine: engine}
}

func titlePushHooks() *PushHooks {
	return &PushHooks{
		ContentEqual: func(l *types.Issue, r *TrackerIssue) bool { return l.Title == r.Title },
		ContentHash:  func(l *types.Issue) string { return "h:" + l.Title },
	}
}

// editRemote edits the remote issue and dates the edit after every last_sync
// the test writes, as a real edit after the last sync is. An incremental pull
// leaves a linked issue alone for a row dated before its fetch window
// (skipOverlapRow), so an edit dated near the fixture's past time would never
// be pulled.
func (f *pushHashFixture) editRemote(title string) {
	f.mock.issues[0].Title = title
	f.mock.issues[0].UpdatedAt = time.Now().UTC().Add(time.Hour)
}

func (f *pushHashFixture) pushHashes() map[string]string {
	out := make(map[string]string)
	for key, value := range f.store.localMetadata {
		if strings.Contains(key, ".pushhash.") {
			out[key] = value
		}
	}
	return out
}

func runSync(t *testing.T, engine *Engine, opts SyncOptions) *SyncResult {
	t.Helper()
	result, err := engine.Sync(context.Background(), opts)
	if err != nil {
		t.Fatalf("Sync(%+v): %v", opts, err)
	}
	return result
}

// TestEnginePullRecordsPushHash pins that a pull keeps the stored push hash in
// step with the remote. Otherwise a local revert to the last-pushed content
// matches the stale hash and the push skips it, leaving the remote on the
// pulled content.
func TestEnginePullRecordsPushHash(t *testing.T) {
	t.Run("revert_after_pull_is_pushed", func(t *testing.T) {
		f := newPushHashFixture()
		key := f.engine.pushHashKey(f.local.ID)

		runSync(t, f.engine, SyncOptions{Push: true})
		if got, want := f.store.localMetadata[key], "v3:8:h:pushed0:"+pushHashTestRef; got != want {
			t.Fatalf("push hash after the push = %q, want %q", got, want)
		}

		f.editRemote("remote edit")
		runSync(t, f.engine, SyncOptions{Pull: true})
		if f.local.Title != "remote edit" {
			t.Fatalf("pull did not import the remote edit: title=%q", f.local.Title)
		}
		if got, want := f.store.localMetadata[key], "v3:13:h:remote edit0:"+pushHashTestRef; got != want {
			t.Fatalf("push hash after the pull = %q, want %q", got, want)
		}

		f.local.Title = "pushed"
		fetchesBefore := f.mock.fetchCalls
		runSync(t, f.engine, SyncOptions{Push: true})
		if f.mock.fetchCalls == fetchesBefore {
			t.Fatalf("push of the revert did not fetch the remote")
		}
		if sent, ok := f.mock.updated["EXT-1"]; !ok || sent.Title != "pushed" {
			t.Fatalf("push skipped the revert to %q; remote stays %q", f.local.Title, f.mock.issues[0].Title)
		}
	})

	t.Run("pull_then_push_is_a_no_op", func(t *testing.T) {
		f := newPushHashFixture()
		runSync(t, f.engine, SyncOptions{Push: true})

		f.editRemote("remote edit")
		f.mock.issues = append(f.mock.issues, TrackerIssue{
			ID: "EXT-2", Identifier: "EXT-2", URL: "https://test.test/EXT-2", Title: "new remote",
			UpdatedAt: f.mock.issues[0].UpdatedAt,
		})
		pull := runSync(t, f.engine, SyncOptions{Pull: true})
		if pull.PullStats.Updated != 1 || pull.PullStats.Created != 1 {
			t.Fatalf("PullStats = %+v, want one update and one create", pull.PullStats)
		}

		f.mock.fetchCalls = 0
		runSync(t, f.engine, SyncOptions{Push: true})
		if f.mock.fetchCalls != 0 || len(f.mock.updated) != 0 || len(f.mock.created) != 0 {
			t.Fatalf("push after the pull: fetchCalls=%d updated=%d created=%d, want all 0",
				f.mock.fetchCalls, len(f.mock.updated), len(f.mock.created))
		}
	})

	t.Run("generated_id_does_not_key_the_hash", func(t *testing.T) {
		f := newPushHashFixture()
		f.engine.PullHooks = &PullHooks{GenerateID: func(_ context.Context, issue *types.Issue) error {
			if issue.ID == "" {
				issue.ID = "bd-generated"
			}
			return nil
		}}
		runSync(t, f.engine, SyncOptions{Push: true})

		f.editRemote("remote edit")
		runSync(t, f.engine, SyncOptions{Pull: true})
		if got, want := f.store.localMetadata["test.pushhash.bd-hash1"], "v3:13:h:remote edit0:"+pushHashTestRef; got != want {
			t.Fatalf("push hash of the local issue = %q, want %q", got, want)
		}
		if value, ok := f.store.localMetadata["test.pushhash.bd-generated"]; ok {
			t.Fatalf("pull stored a push hash under the generated ID: %q", value)
		}
	})

	t.Run("pull_without_content_hash_clears", func(t *testing.T) {
		f := newPushHashFixture()
		key := f.engine.pushHashKey(f.local.ID)
		runSync(t, f.engine, SyncOptions{Push: true})

		// Every tracker but GitHub builds its engine without ContentHash.
		f.editRemote("remote edit")
		runSync(t, NewEngine(f.mock, f.store, "sync"), SyncOptions{Pull: true})
		if got := f.store.localMetadata[key]; got != "" {
			t.Fatalf("push hash after a pull without ContentHash = %q, want it cleared", got)
		}

		f.local.Title = "pushed"
		fetchesBefore := f.mock.fetchCalls
		runSync(t, f.engine, SyncOptions{Push: true})
		if f.mock.fetchCalls == fetchesBefore {
			t.Fatalf("push of the revert did not fetch the remote")
		}
		if sent, ok := f.mock.updated["EXT-1"]; !ok || sent.Title != "pushed" {
			t.Fatalf("push skipped the revert to %q; remote stays %q", f.local.Title, f.mock.issues[0].Title)
		}
		if f.store.localMetadata[key] == "" {
			t.Fatalf("push of the revert recorded no push hash")
		}

		// A ContentHash that has no fingerprint for the pulled content clears too.
		f.editRemote("unhashable")
		noHash := NewEngine(f.mock, f.store, "sync")
		noHash.PushHooks = titlePushHooks()
		noHash.PushHooks.ContentHash = func(l *types.Issue) string {
			if l.Title == "unhashable" {
				return ""
			}
			return "h:" + l.Title
		}
		runSync(t, noHash, SyncOptions{Pull: true})
		if got := f.store.localMetadata[key]; got != "" {
			t.Fatalf("push hash after a pull ContentHash cannot fingerprint = %q, want it cleared", got)
		}
	})

	// Local and remote can reach the same content without a push, for example
	// through the same edit on both sides. A pull that reaches the issue then
	// finds it equal and writes nothing, yet the stored hash still describes
	// the last push. (A pull-only run reaches a local edit only once the edit
	// is older than the last sync; the conflict guard skips it before that.)
	t.Run("equal_pull_refreshes_a_stale_hash", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			hooks    *PushHooks
			wantHash string
		}{
			{name: "content_hash", hooks: titlePushHooks(), wantHash: "v3:6:h:same0:" + pushHashTestRef},
			{name: "no_content_hash", wantHash: ""},
		} {
			t.Run(test.name, func(t *testing.T) {
				f := newPushHashFixture()
				key := f.engine.pushHashKey(f.local.ID)
				runSync(t, f.engine, SyncOptions{Push: true})

				f.local.Title = "same"
				f.editRemote("same")
				puller := NewEngine(f.mock, f.store, "sync")
				puller.PushHooks = test.hooks
				pull := runSync(t, puller, SyncOptions{Pull: true})
				if pull.PullStats.Updated != 0 || pull.PullStats.Skipped != 1 {
					t.Fatalf("PullStats = %+v, want the equal issue skipped", pull.PullStats)
				}
				if got := f.store.localMetadata[key]; got != test.wantHash {
					t.Fatalf("push hash after the equal pull = %q, want %q", got, test.wantHash)
				}

				f.local.Title = "pushed"
				fetchesBefore := f.mock.fetchCalls
				runSync(t, f.engine, SyncOptions{Push: true})
				if f.mock.fetchCalls == fetchesBefore {
					t.Fatalf("push of the revert did not fetch the remote")
				}
				if sent, ok := f.mock.updated["EXT-1"]; !ok || sent.Title != "pushed" {
					t.Fatalf("push skipped the revert to %q; remote stays %q", f.local.Title, f.mock.issues[0].Title)
				}
			})
		}
	})

	t.Run("equal_pull_writes_only_a_changed_entry", func(t *testing.T) {
		f := newPushHashFixture()
		runSync(t, f.engine, SyncOptions{Push: true})
		// A remote edit that leaves the title alone, so each pull reaches the
		// issue and finds it equal.
		f.editRemote("pushed")

		noHash := NewEngine(f.mock, f.store, "sync")
		for i, step := range []struct {
			engine     *Engine
			wantWrites int
		}{
			{engine: f.engine, wantWrites: 0}, // the entry already holds the pulled content's hash
			{engine: noHash, wantWrites: 1},   // clears it
			{engine: noHash, wantWrites: 0},   // nothing left to clear
		} {
			writes := f.store.pushHashWrites
			runSync(t, step.engine, SyncOptions{Pull: true})
			if got := f.store.pushHashWrites - writes; got != step.wantWrites {
				t.Fatalf("equal pull %d wrote %d push hash entries, want %d", i+1, got, step.wantWrites)
			}
		}
	})

	// A conversion without a label set leaves the labels of the row as they
	// are, so the hash must cover the labels the row keeps. Otherwise the next
	// push of the unchanged issue fetches it, and a push hook that compares
	// labels would send them back over the remote.
	t.Run("nil_labels_hash_the_labels_the_row_keeps", func(t *testing.T) {
		f := newPushHashFixture()
		f.local.Labels = []string{"local"}
		f.engine.PushHooks.ContentHash = func(l *types.Issue) string {
			return "h:" + l.Title + ":" + strings.Join(l.Labels, ",")
		}
		runSync(t, f.engine, SyncOptions{Push: true})

		f.editRemote("remote edit")
		runSync(t, f.engine, SyncOptions{Pull: true})
		if f.local.Title != "remote edit" || len(f.local.Labels) != 1 {
			t.Fatalf("pull: title=%q labels=%q, want the remote edit and the local label", f.local.Title, f.local.Labels)
		}

		f.mock.fetchCalls = 0
		runSync(t, f.engine, SyncOptions{Push: true})
		if f.mock.fetchCalls != 0 || len(f.mock.updated) != 0 {
			t.Fatalf("push after the pull: fetchCalls=%d updated=%d, want both 0", f.mock.fetchCalls, len(f.mock.updated))
		}
	})

	t.Run("pulls_that_must_not_touch_the_cache", func(t *testing.T) {
		assertUnchanged := func(t *testing.T, f *pushHashFixture, before map[string]string) {
			t.Helper()
			after := f.pushHashes()
			if len(after) != len(before) {
				t.Fatalf("push hashes = %v, want %v", after, before)
			}
			for key, value := range before {
				if got, ok := after[key]; !ok || got != value {
					t.Fatalf("push hashes = %v, want %v", after, before)
				}
			}
		}

		t.Run("dry_run", func(t *testing.T) {
			f := newPushHashFixture()
			runSync(t, f.engine, SyncOptions{Push: true})
			before := f.pushHashes()

			f.editRemote("remote edit")
			result := runSync(t, f.engine, SyncOptions{Pull: true, DryRun: true})
			if result.PullStats.Updated != 1 || f.local.Title != "pushed" {
				t.Fatalf("dry-run pull: stats=%+v title=%q", result.PullStats, f.local.Title)
			}
			assertUnchanged(t, f, before)
		})

		t.Run("dry_run_equal", func(t *testing.T) {
			f := newPushHashFixture()
			runSync(t, f.engine, SyncOptions{Push: true})
			before := f.pushHashes()

			f.local.Title = "same"
			f.editRemote("same")
			result := runSync(t, f.engine, SyncOptions{Pull: true, DryRun: true})
			if result.PullStats.Skipped != 1 {
				t.Fatalf("dry-run pull: stats=%+v, want the equal issue skipped", result.PullStats)
			}
			assertUnchanged(t, f, before)
		})

		t.Run("failed_update", func(t *testing.T) {
			f := newPushHashFixture()
			f.store.tx.addLabelErr = errors.New("label write failed")
			f.mock.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
				return &IssueConversion{Issue: &types.Issue{
					Title: ti.Title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
					Labels: []string{"remote"},
				}}
			}}
			runSync(t, f.engine, SyncOptions{Push: true})
			before := f.pushHashes()

			f.editRemote("remote edit")
			result := runSync(t, f.engine, SyncOptions{Pull: true})
			if result.PullStats.Errors != 1 || f.local.Title != "pushed" {
				t.Fatalf("failed pull: stats=%+v title=%q", result.PullStats, f.local.Title)
			}
			assertUnchanged(t, f, before)
		})

		t.Run("no_content_hash_no_entry", func(t *testing.T) {
			f := newPushHashFixture()
			before := f.pushHashes()

			f.editRemote("remote edit")
			result := runSync(t, NewEngine(f.mock, f.store, "sync"), SyncOptions{Pull: true})
			if result.PullStats.Updated != 1 || f.local.Title != "remote edit" {
				t.Fatalf("pull: stats=%+v title=%q", result.PullStats, f.local.Title)
			}
			assertUnchanged(t, f, before)
		})
	})
}

// stampingTracker applies each push to the remote issue and dates the issue
// then, as GitHub's PATCH does. The overlap window of the next incremental
// pull therefore fetches the row of bd's own push again.
type stampingTracker struct {
	*mockTracker
}

func (s *stampingTracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*TrackerIssue, error) {
	updated, err := s.mockTracker.UpdateIssue(ctx, externalID, issue)
	if err != nil {
		return nil, err
	}
	for i := range s.issues {
		if s.issues[i].Identifier == externalID {
			s.issues[i].Title = issue.Title
			s.issues[i].Labels = append([]string{}, issue.Labels...)
			s.issues[i].UpdatedAt = time.Now().UTC()
		}
	}
	return updated, nil
}

// newLabelPushFixture is newPushHashFixture with labels handled the way the
// GitHub tracker handles them: the conversion carries the remote's label set,
// empty rather than nil when it has none, and the push sends the labels and
// fingerprints them. Both sides start with the given labels. The local issue
// has a title edit, so the first push sends it and dates the remote row
// (stampingTracker).
func newLabelPushFixture(labels ...string) *pushHashFixture {
	f := newPushHashFixture()
	f.local.Title = "edited"
	f.local.Labels = append([]string(nil), labels...)
	f.mock.issues[0].Labels = append([]string(nil), labels...)
	f.mock.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			Title: ti.Title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			Assignee: ti.Assignee, Labels: append([]string{}, ti.Labels...),
		}}
	}}
	f.engine.Tracker = &stampingTracker{mockTracker: f.mock}
	f.engine.PushHooks = labelPushHooks()
	return f
}

// labelPushHooks compare and fingerprint the title and the label set, as
// GitHub's PushFieldsEqual and PushContentHash do.
func labelPushHooks() *PushHooks {
	fingerprint := func(title string, labels []string) string {
		return "h:" + title + ":" + strings.Join(normalizedStringSlice(labels), ",")
	}
	return &PushHooks{
		ContentEqual: func(l *types.Issue, r *TrackerIssue) bool {
			return fingerprint(l.Title, l.Labels) == fingerprint(r.Title, r.Labels)
		},
		ContentHash: func(l *types.Issue) string { return fingerprint(l.Title, l.Labels) },
	}
}

// TestEnginePullKeepsLabelsChangedSinceThePush pins that a pull leaves alone
// the labels changed locally since the last push while the remote still holds
// that push, so the push sends them. bd label add and remove move no
// updated_at, so the conflict guard lets the issue through, and the overlap
// window of the next incremental pull fetches the row of bd's own push again.
// A pull that wrote the remote's labels and refreshed the push hash dropped
// the edit on both sides: no later push sent it.
func TestEnginePullKeepsLabelsChangedSinceThePush(t *testing.T) {
	for _, test := range []struct {
		name           string
		pushed         []string // labels on both sides after the first push
		local          []string // local labels after the edit
		remoteAssignee string   // a remote edit of a field the push does not send
		pullThenPush   bool     // `bd github pull`, then `bd github push`; else one `bd github sync`
	}{
		{name: "sync/added", local: []string{"local"}},
		{name: "sync/added_beside_a_pushed_label", pushed: []string{"pushed"}, local: []string{"local", "pushed"}},
		{name: "sync/removed", pushed: []string{"pushed"}},
		{name: "sync/added_while_the_remote_changed_the_assignee", local: []string{"local"}, remoteAssignee: "alice"},
		{name: "pull_then_push/added", local: []string{"local"}, pullThenPush: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newLabelPushFixture(test.pushed...)
			key := f.engine.pushHashKey(f.local.ID)
			runSync(t, f.engine, SyncOptions{Push: true})
			pushedHash := f.store.localMetadata[key]
			if f.mock.issues[0].Title != "edited" || pushedHash == "" {
				t.Fatalf("first push: remote title=%q push hash=%q", f.mock.issues[0].Title, pushedHash)
			}

			f.local.Labels = append([]string(nil), test.local...)
			if test.remoteAssignee != "" {
				f.mock.issues[0].Assignee = test.remoteAssignee
				f.mock.issues[0].UpdatedAt = time.Now().UTC().Add(time.Hour)
			}
			if test.pullThenPush {
				preview := runSync(t, f.engine, SyncOptions{Pull: true, DryRun: true})
				pull := runSync(t, f.engine, SyncOptions{Pull: true})
				for _, stats := range []PullStats{preview.PullStats, pull.PullStats} {
					if stats.Updated != 0 || stats.Skipped != 1 {
						t.Fatalf("pull stats = %+v, want the issue skipped", stats)
					}
				}
				if !equalNormalizedStrings(f.local.Labels, test.local) || f.store.localMetadata[key] != pushedHash {
					t.Fatalf("pull: labels=%q push hash=%q, want %q and the hash of the first push %q",
						f.local.Labels, f.store.localMetadata[key], test.local, pushedHash)
				}
				runSync(t, f.engine, SyncOptions{Push: true})
			} else {
				runSync(t, f.engine, SyncOptions{})
			}

			if !equalNormalizedStrings(f.local.Labels, test.local) {
				t.Fatalf("local labels = %q, want %q", f.local.Labels, test.local)
			}
			if got := f.mock.issues[0].Labels; !equalNormalizedStrings(got, test.local) {
				t.Fatalf("remote labels = %q, want the local edit %q pushed", got, test.local)
			}
			if f.local.Assignee != test.remoteAssignee {
				t.Fatalf("local assignee = %q, want the remote's %q", f.local.Assignee, test.remoteAssignee)
			}

			// The push recorded what it sent, so the next one is a no-op.
			f.mock.fetchCalls = 0
			f.mock.updated = make(map[string]*types.Issue)
			runSync(t, f.engine, SyncOptions{Push: true})
			if f.mock.fetchCalls != 0 || len(f.mock.updated) != 0 {
				t.Fatalf("push after the sync: fetchCalls=%d updated=%d, want both 0", f.mock.fetchCalls, len(f.mock.updated))
			}
		})
	}
}

// TestEnginePullKeepsLocalLabelsWhileTheRemoteChangedOtherFields pins that a
// pull merges a local label edit with a remote edit of other pushed fields:
// the local issue takes the remote's title and keeps its labels, and the push
// sends them. The local issue with the remote's labels still matches the
// stored push hash, which proves that the remote's labels did not change since
// the push. A pull that wrote the remote's labels and refreshed the push hash
// dropped the local edit on both sides.
func TestEnginePullKeepsLocalLabelsWhileTheRemoteChangedOtherFields(t *testing.T) {
	for _, test := range []struct {
		name   string
		pushed []string // labels on both sides after the first push
		local  []string // local labels after the edit
		pulls  int      // `bd github pull` runs before `bd github push`; 0 runs one `bd github sync`
	}{
		{name: "sync/added", local: []string{"local"}},
		{name: "sync/added_beside_a_pushed_label", pushed: []string{"pushed"}, local: []string{"local", "pushed"}},
		{name: "sync/removed", pushed: []string{"pushed"}},
		// The first pull records the remote's labels in the push hash, so the
		// second finds the remote unchanged since then and keeps the labels.
		{name: "pull_pull_push/added", local: []string{"local"}, pulls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newLabelPushFixture(test.pushed...)
			runSync(t, f.engine, SyncOptions{Push: true})

			f.local.Labels = append([]string(nil), test.local...)
			f.editRemote("remote edit")
			if test.pulls == 0 {
				runSync(t, f.engine, SyncOptions{})
			}
			for pull := 1; pull <= test.pulls; pull++ {
				runSync(t, f.engine, SyncOptions{Pull: true})
				if f.local.Title != "remote edit" || !equalNormalizedStrings(f.local.Labels, test.local) {
					t.Fatalf("pull %d: local title=%q labels=%q, want %q and %q",
						pull, f.local.Title, f.local.Labels, "remote edit", test.local)
				}
			}
			if test.pulls > 0 {
				runSync(t, f.engine, SyncOptions{Push: true})
			}

			if f.local.Title != "remote edit" || !equalNormalizedStrings(f.local.Labels, test.local) {
				t.Fatalf("local title=%q labels=%q, want the remote's title and the local labels %q",
					f.local.Title, f.local.Labels, test.local)
			}
			if remote := f.mock.issues[0]; remote.Title != "remote edit" || !equalNormalizedStrings(remote.Labels, test.local) {
				t.Fatalf("remote title=%q labels=%q, want its own title and the local labels %q pushed",
					remote.Title, remote.Labels, test.local)
			}

			// The push recorded what it sent, so the next one is a no-op.
			f.mock.fetchCalls = 0
			f.mock.updated = make(map[string]*types.Issue)
			runSync(t, f.engine, SyncOptions{Push: true})
			if f.mock.fetchCalls != 0 || len(f.mock.updated) != 0 {
				t.Fatalf("push after the sync: fetchCalls=%d updated=%d, want both 0", f.mock.fetchCalls, len(f.mock.updated))
			}
		})
	}
}

// TestEnginePullWritesRemoteLabelsUnlessOnlyLocalChanged pins where
// pullKeepsLocalLabels stops: when the remote's labels changed too, without
// proof that the remote still holds the labels of the last push, or when the
// push hash cannot tell the labels apart, a sync writes the remote's labels
// over a local label edit and pushes nothing.
func TestEnginePullWritesRemoteLabelsUnlessOnlyLocalChanged(t *testing.T) {
	for _, test := range []struct {
		name   string
		pushed []string                 // labels on both sides after the first push
		hooks  *PushHooks               // replaces labelPushHooks
		setup  func(f *pushHashFixture) // after the push and the local label edit
	}{
		// The remote changed its title and its labels since the push, so a
		// local label edit conflicts with the remote's, which wins.
		{name: "remote_changed_the_title_and_added_a_label", setup: func(f *pushHashFixture) {
			f.editRemote("remote edit")
			f.mock.issues[0].Labels = []string{"remote"}
		}},
		{name: "remote_changed_the_title_and_removed_a_label", pushed: []string{"pushed"}, setup: func(f *pushHashFixture) {
			f.editRemote("remote edit")
			f.mock.issues[0].Labels = nil
		}},
		// No stored push hash, as on a fresh clone: nothing proves that the
		// remote still holds the last push.
		{name: "no_push_hash", setup: func(f *pushHashFixture) {
			delete(f.store.localMetadata, f.engine.pushHashKey(f.local.ID))
		}},
		// A push hash without the labels: the next push would skip the
		// issue, so a kept label would never reach the remote.
		{name: "push_hash_without_labels", hooks: titlePushHooks()},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newLabelPushFixture(test.pushed...)
			if test.hooks != nil {
				f.engine.PushHooks = test.hooks
			}
			runSync(t, f.engine, SyncOptions{Push: true})

			f.local.Labels = append(append([]string(nil), test.pushed...), "local")
			if test.setup != nil {
				test.setup(f)
			}
			want := append([]string(nil), f.mock.issues[0].Labels...)
			f.mock.updated = make(map[string]*types.Issue)
			runSync(t, f.engine, SyncOptions{})
			if !equalNormalizedStrings(f.local.Labels, want) {
				t.Fatalf("local labels after the sync = %q, want the remote's %q", f.local.Labels, want)
			}
			if got := f.mock.issues[0].Labels; len(f.mock.updated) != 0 || !equalNormalizedStrings(got, want) {
				t.Fatalf("sync pushed %d issues, leaving remote labels %q; want no push and %q",
					len(f.mock.updated), got, want)
			}
		})
	}
}
