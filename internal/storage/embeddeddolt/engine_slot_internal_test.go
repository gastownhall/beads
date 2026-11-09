//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// engineOpenCounter wraps the production opener, counting engine opens and
// engine disposals. Installed on the store's engine slot (the opener seam)
// before the store's first operation, it sees every open the slot makes.
type engineOpenCounter struct {
	inner  engineOpener
	opens  atomic.Int64
	closes atomic.Int64
}

func (c *engineOpenCounter) open(ctx context.Context, dir, database, branch string) (*sql.DB, func() error, error) {
	db, cleanup, err := c.inner(ctx, dir, database, branch)
	if err != nil {
		return nil, nil, err
	}
	c.opens.Add(1)
	return db, func() error {
		err := cleanup()
		c.closes.Add(1)
		return err
	}, nil
}

// engineTestStore opens a store on a fresh database with the counting opener
// and linger (an idle interval long enough to suppress idle disposal)
// installed. Seams go in before the first operation, the design's requirement,
// and the store's bootstrap open (initSchema's one-shot raw OpenSQL) stays
// outside the count: only opens the engine slot makes are counted.
func engineTestStore(t *testing.T, database string, linger time.Duration) (*EmbeddedDoltStore, *engineOpenCounter) {
	t.Helper()
	skipUnlessEmbeddedDolt(t)
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), ".beads"), database, "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.engine.opener != nil || store.engine.db != nil {
		t.Fatalf("engine slot already in use before test seams installed")
	}
	counter := &engineOpenCounter{inner: OpenSQL}
	store.engine.opener = counter.open
	store.engine.idleAfter = linger
	return store, counter
}

// engineOpenCountersEqual waits until want opens and closes have been counted,
// or fails after timeout with the current counts. Idle disposal and Close run
// on their own goroutine timing, so the exact moment a disposal lands is not
// observable synchronously.
func engineWaitForCounters(t *testing.T, counter *engineOpenCounter, wantOpens, wantCloses int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if counter.opens.Load() == wantOpens && counter.closes.Load() == wantCloses {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("engine opens/closes = %d/%d after %v, want %d/%d",
				counter.opens.Load(), counter.closes.Load(), timeout, wantOpens, wantCloses)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEmbeddedEngineReusedAcrossTransactions pins AC-1 of gastownhall/beads#5397:
// one cached engine serves every operation a store runs across its lifetime —
// config writes, the journal-shape probe, journaled and unjournaled
// transactions, reads, searches and the dependency-tree walk — instead of the
// per-operation cold-start open the base paid (measured ~110ms each). The tree
// walk is the expensive case on the base: WalkDescendants issues one search per
// node, 25 nodes here, so the base opened the engine 25+ times for the walk
// alone.
func TestEmbeddedEngineReusedAcrossTransactions(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	ctx := context.Background()
	store, counter := engineTestStore(t, "engine_reuse", time.Hour)

	if err := store.SetConfig(ctx, "issue_prefix", "er"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	// Journal activation probes the journal table's shape through the engine
	// slot (probeJournalShape); journaled transactions then dual-write.
	store.SetEventsJournalEnabled(true)

	const children = 24
	root := &types.Issue{ID: "er-root", Title: "er-root", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, root, "tester"); err != nil {
		t.Fatalf("CreateIssue root: %v", err)
	}
	seen := make(map[string]bool)
	for i := range children {
		id := fmt.Sprintf("er-child-%02d", i)
		seen[id] = true
		child := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, child, "tester"); err != nil {
			t.Fatalf("CreateIssue %s: %v", id, err)
		}
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: id, DependsOnID: "er-root", Type: types.DepParentChild}, "tester"); err != nil {
			t.Fatalf("AddDependency %s: %v", id, err)
		}
	}

	// Reads over the same cached engine.
	got, err := store.GetIssue(ctx, "er-root")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "er-root" {
		t.Fatalf("GetIssue().Title = %q, want %q", got.Title, "er-root")
	}
	hits, err := store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(hits) != children+1 {
		t.Fatalf("SearchIssues found %d issues, want %d", len(hits), children+1)
	}

	// The node-by-node tree walk issues one search per node; the
	// single-transaction capability walk issues one query. Both must find the
	// whole seeded tree, and neither may reopen the engine.
	walked, err := storage.WalkDescendants(ctx, "er-root", types.IssueFilter{}, func(ctx context.Context, f types.IssueFilter) ([]*types.Issue, error) {
		return store.SearchIssues(ctx, "", f)
	})
	if err != nil {
		t.Fatalf("WalkDescendants: %v", err)
	}
	if len(walked) != children {
		t.Fatalf("WalkDescendants found %d issues, want %d", len(walked), children)
	}
	treed, err := storage.SearchDescendants(ctx, store, "er-root", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchDescendants: %v", err)
	}
	if len(treed) != children {
		t.Fatalf("SearchDescendants found %d issues, want %d", len(treed), children)
	}
	for _, iss := range treed {
		if !seen[iss.ID] {
			t.Fatalf("SearchDescendants returned unexpected issue %s", iss.ID)
		}
	}

	if opens := counter.opens.Load(); opens != 1 {
		t.Fatalf("engine opened %d times across the store's operations, want 1", opens)
	}
	if closes := counter.closes.Load(); closes != 0 {
		t.Fatalf("engine disposed %d times while the store is open, want 0", closes)
	}
}
