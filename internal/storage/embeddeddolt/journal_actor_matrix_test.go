//go:build cgo

package embeddeddolt_test

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// journalOpActor is one (op, issue_id, actor) triple read straight out of the
// journal table.
type journalOpActor struct {
	op    string
	id    string
	actor string
}

func readJournalOpActors(t *testing.T, env *testEnv) []journalOpActor {
	t.Helper()
	ctx := context.Background()
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, env.dataDir, env.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer cleanup()
	rows, err := db.QueryContext(ctx, "SELECT op, issue_id, actor FROM bd_events_journal ORDER BY seq ASC")
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	defer rows.Close()
	var out []journalOpActor
	for rows.Next() {
		var r journalOpActor
		if err := rows.Scan(&r.op, &r.id, &r.actor); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// TestEventsJournal_ActorMatrixOnEmbedded runs one mutation of every journaled
// kind through the DEFAULT LOCAL WORKSPACE — the embedded store, no daemon, no
// SQL server — and asserts at the database that each landed a row and that each
// row names the right identity.
//
// It is the release gate the field report implies. Its two earlier claims were
// that the journal had no actor column and no rows; both turned out to be
// something else (a migration that had since landed, and default-off
// activation), but the sub-claim underneath them — that a consumer cannot trust
// this column — was real for deletes. A per-op matrix is what makes that
// trustworthy as a whole rather than one op at a time: a future path that
// forgets to thread its actor fails here even if its own test does not exist
// yet.
//
// The empty cases are asserted as deliberately as the attributed ones. A
// derived is_blocked recompute is bd's own bookkeeping, not something the actor
// who tripped it did, and the 0066 contract says a consumer must read an empty
// actor as system/unknown rather than as a conflicting writer — so a test that
// let an empty actor pass anywhere would be pinning nothing.
func TestEventsJournal_ActorMatrixOnEmbedded(t *testing.T) {
	env := newTestEnv(t, "am")
	store := env.store
	store.SetEventsJournalEnabled(true)
	ctx := context.Background()

	mustCreate := func(id string) {
		t.Helper()
		if err := store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "alice"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// The §1 spread: every op a consumer can observe, in one workspace.
	mustCreate("am-a")
	mustCreate("am-b")
	if err := store.UpdateIssue(ctx, "am-a", map[string]any{"title": "renamed"}, "alice"); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Label edits journal as `update` — a label is part of the bead's state, so
	// a replaying consumer needs the whole post-mutation snapshot, not an event
	// kind of its own.
	if err := store.AddLabel(ctx, "am-a", "urgent", "alice"); err != nil {
		t.Fatalf("add label: %v", err)
	}
	if err := store.RemoveLabel(ctx, "am-a", "urgent", "alice"); err != nil {
		t.Fatalf("remove label: %v", err)
	}
	if _, err := store.AddIssueComment(ctx, "am-a", "alice", "a note"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "am-b", DependsOnID: "am-a", Type: types.DepBlocks,
	}, "alice"); err != nil {
		t.Fatalf("add dep: %v", err)
	}
	// A direct `bd dep remove`, which has always been attributed.
	if err := store.RemoveDependency(ctx, "am-b", "am-a", "alice"); err != nil {
		t.Fatalf("remove dep: %v", err)
	}
	// Re-add, so the delete below has an edge to cascade off.
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: "am-b", DependsOnID: "am-a", Type: types.DepBlocks,
	}, "alice"); err != nil {
		t.Fatalf("re-add dep: %v", err)
	}
	if err := store.CloseIssue(ctx, "am-b", "done", "alice", ""); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The role, which is what `bd delete` reaches — not storage.DeleteIssue.
	deleter, err := store.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}
	if _, err := deleter.Delete(ctx, publicops.DeleteRequest{
		IDs: []string{"am-a"}, Force: true, Actor: "alice",
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	rows := readJournalOpActors(t, env)
	has := func(op, id, actor string) bool {
		for _, r := range rows {
			if r.op == op && r.id == id && r.actor == actor {
				return true
			}
		}
		return false
	}

	for _, want := range []struct {
		what  string
		op    string
		id    string
		actor string
	}{
		{"create", "create", "am-a", "alice"},
		{"update", "update", "am-a", "alice"},
		{"comment", "comment", "am-a", "alice"},
		{"dep_add", "dep_add", "am-b", "alice"},
		{"dep_remove (direct)", "dep_remove", "am-b", "alice"},
		{"close", "close", "am-b", "alice"},
		// The delete row and the edge the cascade took with it. The edge is
		// journaled under its SOURCE, the surviving bead, which is why the two
		// rows name different ids and the same actor.
		{"delete (role path)", "delete", "am-a", "alice"},
		{"dep_remove (cascade)", "dep_remove", "am-b", "alice"},
	} {
		if !has(want.op, want.id, want.actor) {
			t.Errorf("no %s row for %s with actor %q\njournal: %+v", want.what, want.id, want.actor, rows)
		}
	}

	// The other half of the contract: derived readiness maintenance is bd's own
	// bookkeeping and stays unattributed. Removing am-b's only blocker flips its
	// persisted is_blocked, which journals as an update carrying no actor.
	if !has("update", "am-b", "") {
		t.Errorf("no unattributed derived is_blocked update for am-b; the '' half of the contract is not being exercised\njournal: %+v", rows)
	}

	// Nothing may carry an actor bd never saw, and no attributed op may slip
	// through empty. Every '' row in this workspace is a derived update.
	for _, r := range rows {
		if r.actor == "" && r.op != "update" {
			t.Errorf("op %q for %s recorded no actor; only derived is_blocked updates may\njournal: %+v", r.op, r.id, rows)
		}
		if r.actor != "" && r.actor != "alice" {
			t.Errorf("row %+v names an actor no mutation used", r)
		}
	}
}
