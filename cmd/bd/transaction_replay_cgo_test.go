//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

// openJournalStores opens n stores (n connection pools, as n concurrent bd
// processes would have) on one fresh database of the test server, with the
// events journal on: every mutation also bumps the single bd_events_seq row
// inside its transaction, so writers whose transactions overlap collide at
// commit.
func openJournalStores(t *testing.T, n int) []*dolt.DoltStore {
	t.Helper()
	ensureTestMode(t)
	if testDoltServerPort == 0 {
		t.Skip("Dolt test server not available, skipping")
	}
	if testutil.DoltContainerCrashed() {
		t.Skipf("Dolt test server crashed: %v", testutil.DoltContainerCrashError())
	}
	dbName := uniqueTestDBName(t)
	stores := make([]*dolt.DoltStore, 0, n)
	t.Cleanup(func() {
		for _, s := range stores {
			_ = s.Close()
		}
		dropTestDatabase(dbName, testDoltServerPort)
	})
	for i := 0; i < n; i++ {
		dbPath := filepath.Join(t.TempDir(), ".beads", "beads.db")
		writeTestMetadata(t, dbPath, dbName)
		ctx, cancel := context.WithTimeout(context.Background(), testStoreOpenTimeout)
		doltNewMutex.Lock()
		s, err := dolt.New(ctx, &dolt.Config{
			Path:            dbPath,
			ServerHost:      "127.0.0.1",
			ServerPort:      testDoltServerPort,
			Database:        dbName,
			CreateIfMissing: i == 0,
			MaxOpenConns:    4,
		})
		doltNewMutex.Unlock()
		if err == nil && i == 0 {
			err = s.SetConfig(ctx, "issue_prefix", "rp")
		}
		cancel()
		if err != nil {
			t.Fatalf("open store %d on %s: %v", i, dbName, err)
		}
		s.SetEventsJournalEnabled(true)
		stores = append(stores, s)
	}
	return stores
}

// replayBarrierStore holds its first transaction open after the callback has
// written, until released, so a test can order two writers' commits.
type replayBarrierStore struct {
	*dolt.DoltStore
	attempts atomic.Int32
	prepared chan struct{}
	release  chan struct{}
}

func newReplayBarrierStore(s *dolt.DoltStore) *replayBarrierStore {
	return &replayBarrierStore{DoltStore: s, prepared: make(chan struct{}), release: make(chan struct{})}
}

func (b *replayBarrierStore) RunInTransaction(ctx context.Context, msg string, fn func(tx storage.Transaction) error) error {
	return b.DoltStore.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
		if err := fn(tx); err != nil {
			return err
		}
		if b.attempts.Add(1) == 1 {
			close(b.prepared)
			select {
			case <-b.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
}

// threeStepFormula is a cooked formula: a root and three chained steps, the
// shape `bd mol wisp` clones.
func threeStepFormula() *TemplateSubgraph {
	root := &types.Issue{ID: "formula", Title: "three-step formula", IssueType: types.TypeEpic, Status: types.StatusOpen, Priority: 2}
	sg := &TemplateSubgraph{Root: root, Issues: []*types.Issue{root}, IssueMap: map[string]*types.Issue{root.ID: root}}
	var previous *types.Issue
	for _, name := range []string{"first", "second", "third"} {
		step := &types.Issue{ID: "formula." + name, Title: name + " step", IssueType: types.TypeTask, Status: types.StatusOpen, Priority: 2}
		sg.Issues = append(sg.Issues, step)
		sg.IssueMap[step.ID] = step
		sg.Dependencies = append(sg.Dependencies, &types.Dependency{IssueID: step.ID, DependsOnID: root.ID, Type: types.DepParentChild})
		if previous != nil {
			sg.Dependencies = append(sg.Dependencies, &types.Dependency{IssueID: step.ID, DependsOnID: previous.ID, Type: types.DepBlocks})
		}
		previous = step
	}
	return sg
}

func countWisps(t *testing.T, ctx context.Context, db *sql.DB, ids map[string]string) int {
	t.Helper()
	total := 0
	for _, id := range ids {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM wisps WHERE id = ?", id).Scan(&n); err != nil {
			t.Fatalf("count wisp %s: %v", id, err)
		}
		if n != 1 {
			t.Fatalf("wisp %s has %d rows, want exactly 1", id, n)
		}
		total += n
	}
	return total
}

// TestMolWispReplaysLostJournalConflict: two `bd mol wisp` runs instantiate a
// formula at the same moment with the events journal on, and the second one
// to commit loses with "sql commit (regular): Error 1213 ... try restarting
// transaction". The clone must replay its whole transaction and succeed, with
// every step created exactly once.
func TestMolWispReplaysLostJournalConflict(t *testing.T) {
	stores := openJournalStores(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	winner, loser := newReplayBarrierStore(stores[0]), newReplayBarrierStore(stores[1])
	type outcome struct {
		res *InstantiateResult
		err error
	}
	done := [2]chan outcome{make(chan outcome, 1), make(chan outcome, 1)}
	for i, s := range []*replayBarrierStore{winner, loser} {
		go func() {
			res, err := cloneSubgraph(ctx, s, threeStepFormula(), CloneOptions{
				Actor: fmt.Sprintf("writer-%d", i), Ephemeral: true, Prefix: types.IDPrefixWisp,
			})
			done[i] <- outcome{res, err}
		}()
	}
	for _, s := range []*replayBarrierStore{winner, loser} {
		select {
		case <-s.prepared:
		case <-ctx.Done():
			t.Fatalf("a clone never wrote its wisps: %v", ctx.Err())
		}
	}
	close(winner.release)
	won := <-done[0]
	close(loser.release)
	lost := <-done[1]

	if won.err != nil {
		t.Fatalf("winner: %v", won.err)
	}
	if lost.err != nil {
		t.Fatalf("loser failed instead of replaying the conflict: %v", lost.err)
	}
	if got := loser.attempts.Load(); got != 2 {
		t.Fatalf("loser transaction attempts = %d, want 2 (one lost to the conflict, one replay)", got)
	}
	db := stores[0].DB()
	if n := countWisps(t, ctx, db, won.res.IDMapping) + countWisps(t, ctx, db, lost.res.IDMapping); n != 8 {
		t.Fatalf("wisp rows = %d, want 8 (two clones of a root and three steps)", n)
	}
}
