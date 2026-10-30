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
}
