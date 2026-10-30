package tracker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

const pushHashTestRef = "https://test.test/EXT-1"

// pullHashStore adds CreateIssue to pullFailureStore so a pull can reach its
// create branch: the embedded nil storage.Storage panics on create.
type pullHashStore struct {
	*pullFailureStore
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
	store := &pullHashStore{&pullFailureStore{
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

func (f *pushHashFixture) editRemote(title string) {
	f.mock.issues[0].Title = title
	f.mock.issues[0].UpdatedAt = f.mock.issues[0].UpdatedAt.Add(time.Hour)
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

		// `bd github pull` builds its engine without PushHooks.
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
}
