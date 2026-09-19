//go:build cgo

package embeddeddolt_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournal_EmbeddedConcurrentGaplessNoDup is the commit-ordered-seq
// proof for the embedded engine (the default local workspace). Unlike the SQL
// server — which resolves concurrent writers optimistically with a
// serialization abort at commit — the embedded engine serializes writers on the
// counter row. Either way the counter-drawn seq must come out gapless,
// commit-ordered, and duplicate-free under concurrent real mutations. N
// goroutines each create an issue through the real store path
// (store.CreateIssue -> issueops.CreateIssueInTx -> insertEventRow ->
// nextEventSeq); the journal must end with exactly one contiguous seq per
// create.
func TestEventsJournal_EmbeddedConcurrentGaplessNoDup(t *testing.T) {
	env := newTestEnv(t, "ecw")
	store := env.store
	store.SetEventsJournalEnabled(true)

	const writers = 12
	var wg sync.WaitGroup
	errs := make([]error, writers)
	done := make(chan struct{})
	go func() {
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				iss := &types.Issue{ID: fmt.Sprintf("ecw-%d", k), Title: "t", IssueType: types.TypeTask, Status: types.StatusOpen}
				errs[k] = store.CreateIssue(ctx, iss, "actor")
			}(i)
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("concurrent CreateIssue timed out (deadlock?)")
	}
	for k, e := range errs {
		if e != nil {
			t.Fatalf("create %d failed: %v", k, e)
		}
	}

	seqs := readJournalSeqs(t, env)
	if len(seqs) != writers {
		t.Fatalf("journal rows = %d, want %d", len(seqs), writers)
	}
	seen := map[int64]bool{}
	var prev int64
	for i, seq := range seqs {
		if seen[seq] {
			t.Fatalf("duplicate seq %d", seq)
		}
		seen[seq] = true
		if i > 0 && seq != prev+1 {
			t.Fatalf("seqs must be gapless and ordered: %d then %d", prev, seq)
		}
		prev = seq
	}
}

// readJournalSeqs reads bd_events_journal seqs in order over a short-lived raw
// connection. S1 has no accessor capability yet, so the test reads the table
// directly.
func readJournalSeqs(t *testing.T, env *testEnv) []int64 {
	t.Helper()
	ctx := context.Background()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, env.dataDir, env.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	rows, err := db.QueryContext(ctx, "SELECT seq FROM bd_events_journal ORDER BY seq ASC")
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var seq int64
		if err := rows.Scan(&seq); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, seq)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}
