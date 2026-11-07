package dolt

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// openPeerStore opens a second handle — its own connection pool, as a second
// bd process would have — on the same database as store.
func openPeerStore(t *testing.T, ctx context.Context, store *DoltStore) *DoltStore {
	t.Helper()
	peer, err := New(ctx, &Config{
		Path:           t.TempDir(),
		CommitterName:  "test",
		CommitterEmail: "test@example.com",
		ServerHost:     "127.0.0.1",
		ServerPort:     testServerPort,
		Database:       store.database,
		MaxOpenConns:   2,
	})
	if err != nil {
		t.Fatalf("open peer store for %s: %v", store.database, err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	return peer
}

// TestConcurrentCounterModeCreatesNeverShareAnID: in counter mode the ID is
// minted from the issue_counter row inside the create transaction, so two
// writers that overlap mint the same number and the loser's transaction is
// rolled back and replayed. The replay must mint again: re-using the
// rolled-back attempt's ID took the explicit-ID upsert path and silently
// overwrote the winner's issue, returning one ID to both writers.
func TestConcurrentCounterModeCreatesNeverShareAnID(t *testing.T) {
	base, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := base.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("set counter mode: %v", err)
	}
	const writers, rounds = 5, 4
	stores := []*DoltStore{base}
	for len(stores) < writers {
		stores = append(stores, openPeerStore(t, ctx, base))
	}

	var mu sync.Mutex
	titleByID := map[string]string{}
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds*3)
	for w, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record := func(issue *types.Issue) {
				mu.Lock()
				defer mu.Unlock()
				if prev, dup := titleByID[issue.ID]; dup {
					errs <- fmt.Errorf("id %s handed to both %q and %q", issue.ID, prev, issue.Title)
				}
				titleByID[issue.ID] = issue.Title
			}
			for r := range rounds {
				single := &types.Issue{Title: fmt.Sprintf("single %d-%d", w, r), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
				if err := s.CreateIssue(ctx, single, "writer"); err != nil {
					errs <- fmt.Errorf("create %s: %w", single.Title, err)
				} else {
					record(single)
				}
				batch := []*types.Issue{
					{Title: fmt.Sprintf("batch %d-%d-a", w, r), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
					{Title: fmt.Sprintf("batch %d-%d-b", w, r), Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
				}
				if err := s.CreateIssues(ctx, batch, "writer"); err != nil {
					errs <- fmt.Errorf("create batch %d-%d: %w", w, r, err)
				} else {
					for _, issue := range batch {
						record(issue)
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for id, title := range titleByID {
		got, err := base.GetIssue(ctx, id)
		if err != nil {
			t.Errorf("read %s: %v", id, err)
			continue
		}
		if got.Title != title {
			t.Errorf("issue %s holds %q, want %q: a replayed create overwrote it", id, got.Title, title)
		}
	}
	if want := writers * rounds * 3; len(titleByID) != want && !t.Failed() {
		t.Fatalf("distinct issues = %d, want %d", len(titleByID), want)
	}
}
