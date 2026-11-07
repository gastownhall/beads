package dolt

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"

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

// TestWriteTxRetryScheduleKeepsRetryingAcrossItsBudget pins the replay
// schedule withRetryTx uses for a writer that keeps losing commit-time
// conflicts. Uncapped, the exponential waits stretch to several seconds each
// and a loser gets at most 16 attempts in the server budget (14-15 typically)
// — measured against five concurrent journaled writers, 1-5% of
// `bd create --parent` runs exhausted it. Capping each wait at a second keeps
// attempts arriving through the whole budget: even with every wait jittered to
// its longest, the capped schedule makes 18.
func TestWriteTxRetryScheduleKeepsRetryingAcrossItsBudget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		serverMode bool
		budget     time.Duration
		minTries   int
	}{
		{name: "server", serverMode: true, budget: 15 * time.Second, minTries: 17},
		{name: "embedded", serverMode: false, budget: 5 * time.Second, minTries: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for run := 0; run < 20; run++ {
				clock := &steppingClock{now: time.Unix(0, 0)}
				bo := newWriteTxRetryBackOff(tc.serverMode)
				bo.Clock = clock
				bo.Reset()
				tries := 1
				for {
					next := bo.NextBackOff()
					if next == backoff.Stop {
						break
					}
					if next > writeTxRetryMaxInterval*3/2 {
						t.Fatalf("wait %v exceeds the jittered cap of %v", next, writeTxRetryMaxInterval*3/2)
					}
					clock.now = clock.now.Add(next)
					tries++
				}
				if tries < tc.minTries {
					t.Fatalf("a writer that keeps losing gets %d attempts in %v, want at least %d", tries, tc.budget, tc.minTries)
				}
				if elapsed := clock.now.Sub(time.Unix(0, 0)); elapsed > tc.budget {
					t.Fatalf("schedule ran %v, past its %v budget", elapsed, tc.budget)
				}
			}
		})
	}
}

type steppingClock struct{ now time.Time }

func (c *steppingClock) Now() time.Time { return c.now }

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
