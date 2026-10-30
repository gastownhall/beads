package tracker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// pullHashStore lets the pull's create branch run against the pure test
// store: pureTestStore alone panics on CreateIssue (nil embedded Storage).
type pullHashStore struct {
	*pullFailureStore
}

func (s *pullHashStore) CreateIssue(_ context.Context, issue *types.Issue, _ string) error {
	if issue.ID == "" {
		issue.ID = fmt.Sprintf("bd-created-%d", len(s.issues)+1)
	}
	s.issues = append(s.issues, issue)
	return nil
}

// pullHashFixture wires one linked local issue ("pushed") against a mock
// tracker whose push fingerprint is the title. genID adds a GenerateID pull
// hook that fills a fresh ID, as the GitHub pull mapper does when the
// conversion leaves the ID empty.
type pullHashFixture struct {
	engine *Engine
	store  *pullHashStore
	mock   *mockTracker
	local  *types.Issue
	ref    string
}

func newPullHashFixture(t *testing.T, genID bool, extraRemote ...TrackerIssue) *pullHashFixture {
	t.Helper()
	pushedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	ref := "https://test.test/EXT-1"
	// UpdatedAt stays in the past so the pull's conflict guard never trips
	// once a sync writes last_sync.
	local := &types.Issue{
		ID: "bd-hash1", Title: "pushed", Status: types.StatusOpen,
		Priority: 2, IssueType: types.TypeTask, UpdatedAt: pushedAt, ExternalRef: &ref,
	}
	store := &pullHashStore{&pullFailureStore{
		pureTestStore: newPureTestStore(local),
		tx:            &pullTestTransaction{issue: local},
	}}
	mock := newMockTracker("test")
	mock.issues = append([]TrackerIssue{{
		ID: "EXT-1", Identifier: "EXT-1", URL: ref, Title: "pushed", UpdatedAt: pushedAt,
	}}, extraRemote...)
	mock.fieldMapper = &mockMapper{issueToBeads: func(ti *TrackerIssue) *IssueConversion {
		return &IssueConversion{Issue: &types.Issue{
			Title: ti.Title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
		}}
	}}
	engine := NewEngine(mock, store, "sync")
	engine.PushHooks = &PushHooks{
		ContentEqual: func(l *types.Issue, r *TrackerIssue) bool { return l.Title == r.Title },
		ContentHash:  func(l *types.Issue) string { return "h:" + l.Title },
	}
	if genID {
		engine.PullHooks = &PullHooks{
			GenerateID: func(_ context.Context, issue *types.Issue) error {
				if issue.ID == "" {
					issue.ID = "bd-generated"
				}
				return nil
			},
		}
	}
	return &pullHashFixture{engine: engine, store: store, mock: mock, local: local, ref: ref}
}

// editRemote changes the remote issue's title and bumps its timestamp so the
// next pull imports it.
func (f *pullHashFixture) editRemote(title string) {
	f.mock.issues[0].Title = title
	f.mock.issues[0].UpdatedAt = f.mock.issues[0].UpdatedAt.Add(time.Hour)
}

// TestEnginePullRecordsPushHash pins that a pull refreshes the stored push
// hash to the pulled content. Otherwise a local revert to the last-pushed
// content matches the stale hash and the push skips it, leaving the remote on
// the pulled content (gastownhall/beads#7755).
func TestEnginePullRecordsPushHash(t *testing.T) {
	// AC-1: after a pull imports a remote edit and the local issue goes back
	// to the pushed content, the stored hash describes the pulled content and
	// the next push fetches and sends the revert.
	t.Run("revert_after_pull_is_pushed", func(t *testing.T) {
		f := newPullHashFixture(t, false)
		ctx := context.Background()
		key := f.engine.pushHashKey(f.local.ID)

		// 1. The last push sent "pushed".
		f.engine.recordPushHash(ctx, f.local, f.ref)
		if f.store.localMetadata[key] == "" {
			t.Fatalf("seed push hash not recorded")
		}

		// 2. The remote changes to "remote edit" and a pull imports it.
		f.editRemote("remote edit")
		if _, err := f.engine.Sync(ctx, SyncOptions{Pull: true}); err != nil {
			t.Fatalf("pull Sync: %v", err)
		}
		if f.local.Title != "remote edit" {
			t.Fatalf("pull did not import the remote edit: title=%q", f.local.Title)
		}
		if got, want := f.store.localMetadata[key], "v3:13:h:remote edit0:"+f.ref; got != want {
			t.Fatalf("push hash after the pull = %q, want %q", got, want)
		}

		// 3. The local issue goes back to "pushed"; the push must send the
		// revert, not skip on the hash of the last push.
		f.local.Title = "pushed"
		f.local.UpdatedAt = time.Now().Add(time.Hour)
		f.mock.fetchCalls = 0
		if _, err := f.engine.Sync(ctx, SyncOptions{Push: true}); err != nil {
			t.Fatalf("push Sync: %v", err)
		}
		if f.mock.fetchCalls == 0 {
			t.Fatal("push skipped the fetch: the pulled hash must not short-circuit a revert")
		}
		if got := f.mock.updated["EXT-1"]; got == nil || got.Title != "pushed" {
			t.Fatalf("push did not send the revert: updated[EXT-1] = %v", f.mock.updated["EXT-1"])
		}
	})

	// AC-2: a pull that updates one linked issue and creates one new issue
	// records both fingerprints, so the following push of unchanged content
	// makes no FetchIssue call and no tracker update or create.
	t.Run("pull_then_push_is_a_no_op", func(t *testing.T) {
		pushedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
		f := newPullHashFixture(t, true, TrackerIssue{
			ID: "EXT-2", Identifier: "EXT-2", URL: "https://test.test/EXT-2",
			Title: "new issue", UpdatedAt: pushedAt,
		})
		ctx := context.Background()

		// 1. The last push sent "pushed"; the remote then changes EXT-1.
		f.engine.recordPushHash(ctx, f.local, f.ref)
		f.editRemote("remote edit")
		res, err := f.engine.Sync(ctx, SyncOptions{Pull: true})
		if err != nil {
			t.Fatalf("pull Sync: %v", err)
		}
		if res.PullStats.Updated != 1 || res.PullStats.Created != 1 {
			t.Fatalf("PullStats = %+v, want one update and one create", res.PullStats)
		}

		// 2. The push of the unchanged pulled rows is a no-op.
		f.mock.fetchCalls = 0
		if _, err := f.engine.Sync(ctx, SyncOptions{Push: true}); err != nil {
			t.Fatalf("push Sync: %v", err)
		}
		if f.mock.fetchCalls != 0 {
			t.Fatalf("push fetchCalls = %d, want 0: an unchanged pulled issue must not cost a fetch", f.mock.fetchCalls)
		}
		if len(f.mock.updated) != 0 || len(f.mock.created) != 0 {
			t.Fatalf("push updated=%v created=%v, want none", f.mock.updated, f.mock.created)
		}
	})

	// AC-3: when GenerateID fills a fresh id on every conversion (as the
	// GitHub pull mapper does when the mapping leaves the id empty), the
	// pulled hash is stored under the existing local row's key, never under
	// the generated id's.
	t.Run("generated_id_does_not_key_the_hash", func(t *testing.T) {
		f := newPullHashFixture(t, true)
		ctx := context.Background()

		f.engine.recordPushHash(ctx, f.local, f.ref)
		f.editRemote("remote edit")
		if _, err := f.engine.Sync(ctx, SyncOptions{Pull: true}); err != nil {
			t.Fatalf("pull Sync: %v", err)
		}
		want := "v3:13:h:remote edit0:" + f.ref
		if got := f.store.localMetadata[f.engine.pushHashKey(f.local.ID)]; got != want {
			t.Fatalf("hash under the local id = %q, want %q", got, want)
		}
		if got, ok := f.store.localMetadata[f.engine.pushHashKey("bd-generated")]; ok {
			t.Fatalf("hash stored under the generated id: %q", got)
		}
	})

	// AC-4: a pull by an engine without ContentHash (as `bd github pull`
	// builds it) clears a hash stored by an earlier push, so the next push
	// that can fingerprint still fetches and sends the revert. A ContentHash
	// that declines the pulled content ("") clears too.
	t.Run("pull_without_content_hash_clears", func(t *testing.T) {
		f := newPullHashFixture(t, false)
		ctx := context.Background()
		key := f.engine.pushHashKey(f.local.ID)

		// 1. A push from an engine with ContentHash stored the hash.
		f.engine.recordPushHash(ctx, f.local, f.ref)
		f.editRemote("remote edit")

		// 2. `bd github pull` builds an engine with no PushHooks on the
		// same store.
		pullOnly := NewEngine(f.mock, f.store, "sync")
		if _, err := pullOnly.Sync(ctx, SyncOptions{Pull: true}); err != nil {
			t.Fatalf("pull-only Sync: %v", err)
		}
		if f.local.Title != "remote edit" {
			t.Fatalf("pull did not import the remote edit: title=%q", f.local.Title)
		}
		if got := f.store.localMetadata[key]; got != "" {
			t.Fatalf("pull without ContentHash left push hash %q, want it cleared", got)
		}

		// 3. Edge: a ContentHash that returns "" for the pulled content
		// cannot describe the remote either; it clears too.
		f.store.localMetadata[key] = "v3:8:h:pushed0:" + f.ref
		f.editRemote("unfingerprintable")
		declining := NewEngine(f.mock, f.store, "sync")
		declining.PushHooks = &PushHooks{
			ContentHash: func(l *types.Issue) string {
				if l.Title == "unfingerprintable" {
					return ""
				}
				return "h:" + l.Title
			},
		}
		if _, err := declining.Sync(ctx, SyncOptions{Pull: true}); err != nil {
			t.Fatalf("declining pull Sync: %v", err)
		}
		if got := f.store.localMetadata[key]; got != "" {
			t.Fatalf(`pull whose ContentHash returned "" left push hash %q, want it cleared`, got)
		}

		// 4. After the clear, the next push with ContentHash fetches the
		// remote and sends the revert.
		f.local.Title = "pushed"
		f.local.UpdatedAt = time.Now().Add(time.Hour)
		f.mock.fetchCalls = 0
		if _, err := f.engine.Sync(ctx, SyncOptions{Push: true}); err != nil {
			t.Fatalf("push Sync: %v", err)
		}
		if f.mock.fetchCalls == 0 {
			t.Fatal("cleared push hash must not short-circuit the fetch")
		}
		if got := f.mock.updated["EXT-1"]; got == nil || got.Title != "pushed" {
			t.Fatalf("push did not send the revert: updated[EXT-1] = %v", f.mock.updated["EXT-1"])
		}
	})
}
