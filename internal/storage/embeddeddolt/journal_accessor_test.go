//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestEventsJournalAccessor guards the embedded store's events-journal
// read/prune capability end to end. This is the path `bd events tail/export/
// prune` take in the default (embedded) workspace, where there is no stable
// *sql.DB to reach via RawDBAccessor — so it must go through the store's own
// per-operation connection. A regression here silently breaks every
// `bd events` command locally.
func TestEventsJournalAccessor(t *testing.T) {
	env := newTestEnv(t, "jrn")
	ctx := context.Background()
	store := env.store
	store.SetEventsJournalEnabled(true)
	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	must := func(err error, what string) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}

	must(store.CreateIssue(ctx, mk("jrn-1"), "actor"), "create 1")
	must(store.CreateIssue(ctx, mk("jrn-2"), "actor"), "create 2")
	must(store.UpdateIssue(ctx, "jrn-1", map[string]interface{}{"title": "renamed"}, "actor"), "update")
	must(store.CloseIssue(ctx, "jrn-1", "done", "actor", ""), "close")
	must(store.DeleteIssue(ctx, "jrn-2"), "delete")

	rows, err := store.ReadEventsJournal(ctx, 0, 0)
	must(err, "read all")
	wantOps := []string{"create", "create", "update", "close", "delete"}
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
	if rows[4].Op == "delete" && rows[4].IssueJSON != "" {
		t.Errorf("delete row must carry an empty issue payload, got %q", rows[4].IssueJSON)
	}
	if rows[0].IssueJSON == "" {
		t.Errorf("create row must carry the issue snapshot json")
	}

	// since filter: only rows past the 3rd seq (the close and the delete).
	sinceRows, err := store.ReadEventsJournal(ctx, rows[2].Seq, 0)
	must(err, "read since")
	if len(sinceRows) != 2 {
		t.Fatalf("since read = %d rows, want 2: %+v", len(sinceRows), sinceRows)
	}

	// limit caps the result.
	limited, err := store.ReadEventsJournal(ctx, 0, 2)
	must(err, "read limit")
	if len(limited) != 2 {
		t.Fatalf("limit read = %d rows, want 2", len(limited))
	}

	// retain-rows floor keeps the newest two rows even with a wide --before.
	n, err := store.PruneEventsJournal(ctx, 1_000_000, 0, 2)
	must(err, "prune retain-rows")
	if n != 3 {
		t.Fatalf("prune with retain-rows=2 deleted %d, want 3 (keep newest 2)", n)
	}
	// Reading from the retained floor-1 returns the surviving window. Reading
	// from 0 now fails typed instead of presenting the suffix as a whole
	// history — see TestEventsJournalRestartAndRetentionBoundary.
	after, err := store.ReadEventsJournal(ctx, rows[2].Seq, 0)
	must(err, "read after prune")
	if len(after) != 2 {
		t.Fatalf("after prune %d rows, want 2", len(after))
	}

	// seq continuity: a fresh mutation lands above everything after a prune.
	must(store.CreateIssue(ctx, mk("jrn-3"), "actor"), "create 3")
	post, err := store.ReadEventsJournal(ctx, after[len(after)-1].Seq, 0)
	must(err, "read post")
	if len(post) != 1 || post[0].Op != "create" {
		t.Fatalf("post-prune create not journaled above prior max seq: %+v", post)
	}
}
