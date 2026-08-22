package dolt

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournalAccessor_ServerMode guards the OTHER store's journal
// capability. The embedded store reads through a per-operation connection; this
// one reads through withReadTx and prunes through withRetryTx, so the two can
// regress independently even though the query bodies are shared at the issueops
// seam. `bd events` against a server-mode workspace takes only this path.
func TestEventsJournalAccessor_ServerMode(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()

	enableJournalForTest(t, store)
	clearJournal(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	must(store.CreateIssue(ctx, mk("bd-acc-1"), "actor"), "create 1")
	must(store.CreateIssue(ctx, mk("bd-acc-2"), "actor"), "create 2")
	must(store.UpdateIssue(ctx, "bd-acc-1", map[string]interface{}{"title": "renamed"}, "actor"), "update")
	must(store.DeleteIssue(ctx, "bd-acc-2"), "delete")

	rows, err := store.ReadEventsJournal(ctx, 0, 0)
	must(err, "read all")
	wantOps := []string{"create", "create", "update", "delete"}
	if len(rows) != len(wantOps) {
		t.Fatalf("read %d rows, want %d: %+v", len(rows), len(wantOps), rows)
	}
	var prev int64
	for i, w := range wantOps {
		if rows[i].Op != w {
			t.Errorf("row %d op = %q, want %q", i, rows[i].Op, w)
		}
		if rows[i].Seq <= prev {
			t.Errorf("row %d seq %d not strictly greater than prev %d", i, rows[i].Seq, prev)
		}
		prev = rows[i].Seq
	}
	if rows[0].TS == "" {
		t.Error("journal row carries no timestamp; consumers order and age records by it")
	}

	// since + limit are the two knobs `bd events tail` drives.
	sinceRows, err := store.ReadEventsJournal(ctx, rows[1].Seq, 0)
	must(err, "read since")
	if len(sinceRows) != 2 {
		t.Fatalf("since read = %d rows, want 2: %+v", len(sinceRows), sinceRows)
	}
	limited, err := store.ReadEventsJournal(ctx, 0, 2)
	must(err, "read limit")
	if len(limited) != 2 {
		t.Fatalf("limit read = %d rows, want 2", len(limited))
	}

	// A retain-rows floor keeps the newest rows even against a wide --before,
	// and the write transaction commits the delete.
	n, err := store.PruneEventsJournal(ctx, 1_000_000, 0, 2)
	must(err, "prune retain-rows")
	if n != 2 {
		t.Fatalf("prune with retain-rows=2 deleted %d, want 2 (keep newest 2)", n)
	}
	if got := readJournalRows(t, store); len(got) != 2 {
		t.Fatalf("journal rows after prune = %d, want 2: %+v", len(got), got)
	}

	// The server-mode read reaches the same truncation decision as the embedded
	// one: a checkpoint below the retained floor fails typed rather than
	// serving the surviving suffix as a whole history.
	_, err = store.ReadEventsJournal(ctx, 0, 0)
	var trunc *storage.EventsJournalTruncatedError
	if !errors.As(err, &trunc) {
		t.Fatalf("read from a pruned-past checkpoint = %v, want *EventsJournalTruncatedError", err)
	}
	if trunc.Floor != rows[2].Seq {
		t.Errorf("Floor = %d, want %d", trunc.Floor, rows[2].Seq)
	}
	if trunc.Head != rows[len(rows)-1].Seq {
		t.Errorf("Head = %d, want %d", trunc.Head, rows[len(rows)-1].Seq)
	}
}
