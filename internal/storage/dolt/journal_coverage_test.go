package dolt

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// errRollbackProbe is the sentinel a journal test returns from a transaction
// callback to force a rollback without any real failure.
var errRollbackProbe = errors.New("journal test: deliberate rollback")

// enableJournalForTest activates only this store. Journal tests run alongside
// unrelated store tests, so a process-global switch would leak across them.
func enableJournalForTest(t *testing.T, store *DoltStore) {
	t.Helper()
	store.SetEventsJournalEnabled(true)
	t.Cleanup(func() { store.SetEventsJournalEnabled(false) })
}

// jrow is one journal row read back in seq order.
type jrow struct {
	seq int64
	op  string
	id  string
}

func readJournalRows(t *testing.T, store *DoltStore) []jrow {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(),
		`SELECT seq, op, issue_id FROM bd_events_journal ORDER BY seq ASC`)
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	defer rows.Close()
	var out []jrow
	for rows.Next() {
		var r jrow
		if err := rows.Scan(&r.seq, &r.op, &r.id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func clearJournal(t *testing.T, store *DoltStore) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), "DELETE FROM bd_events_journal"); err != nil {
		t.Fatalf("clear journal: %v", err)
	}
}

// hasOpFor reports whether the journal holds a row with the given op for id.
func hasOpFor(rows []jrow, op, id string) bool {
	for _, r := range rows {
		if r.op == op && r.id == id {
			return true
		}
	}
	return false
}

// journalDepDeltas renders the journal as op:id[->target:kind] strings, the
// form the relationship-delta assertions compare against.
func journalDepDeltas(t *testing.T, store *DoltStore) []string {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(),
		`SELECT op, issue_id, dep_json FROM bd_events_journal ORDER BY seq ASC`)
	if err != nil {
		t.Fatalf("query journal: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var op, id string
		var depJSON []byte
		if err := rows.Scan(&op, &id, &depJSON); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if len(depJSON) == 0 {
			got = append(got, op+":"+id)
			continue
		}
		var dep issueops.EventDep
		if err := json.Unmarshal(depJSON, &dep); err != nil {
			t.Fatalf("decode dep payload: %v", err)
		}
		got = append(got, op+":"+id+"->"+dep.Target+":"+dep.Kind)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return got
}

// TestEventsJournal_SeamEntryPoints drives the mutation entry points the
// op-by-op tests do not — rename, wisps, reopen, ready-claim, lease reclaim,
// by-source-repo bulk delete, unclaim, and creation-time dependencies — through
// the real DoltStore and asserts each lands in the journal at the issueops seam.
func TestEventsJournal_SeamEntryPoints(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}

	t.Run("rename", func(t *testing.T) {
		if err := store.CreateIssue(ctx, mk("bd-rn-1"), "actor"); err != nil {
			t.Fatalf("create: %v", err)
		}
		iss, err := store.GetIssue(ctx, "bd-rn-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		clearJournal(t, store)
		if err := store.UpdateIssueID(ctx, "bd-rn-1", "bd-rn-2", iss, "actor"); err != nil {
			t.Fatalf("rename: %v", err)
		}
		rows := readJournalRows(t, store)
		if !hasOpFor(rows, "delete", "bd-rn-1") || !hasOpFor(rows, "create", "bd-rn-2") {
			t.Fatalf("rename must journal an old-node delete and new-node create; got %+v", rows)
		}
	})

	t.Run("wisp create/update/close", func(t *testing.T) {
		clearJournal(t, store)
		w := &types.Issue{Title: "wisp work", IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true}
		if err := store.CreateIssue(ctx, w, "actor"); err != nil {
			t.Fatalf("create wisp: %v", err)
		}
		if err := store.UpdateIssue(ctx, w.ID, map[string]interface{}{"title": "wisp renamed"}, "actor"); err != nil {
			t.Fatalf("update wisp: %v", err)
		}
		if err := store.CloseIssue(ctx, w.ID, "done", "actor", ""); err != nil {
			t.Fatalf("close wisp: %v", err)
		}
		rows := readJournalRows(t, store)
		for _, op := range []string{"create", "update", "close"} {
			if !hasOpFor(rows, op, w.ID) {
				t.Fatalf("wisp must journal %q for %s; got %+v", op, w.ID, rows)
			}
		}
	})

	t.Run("active wisp delete", func(t *testing.T) {
		w := &types.Issue{Title: "wisp doomed", IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true}
		if err := store.CreateIssue(ctx, w, "actor"); err != nil {
			t.Fatalf("create wisp: %v", err)
		}
		clearJournal(t, store)
		if err := store.DeleteIssue(ctx, w.ID); err != nil {
			t.Fatalf("delete wisp: %v", err)
		}
		if rows := readJournalRows(t, store); !hasOpFor(rows, "delete", w.ID) {
			t.Fatalf("active wisp delete must journal a delete for %s; got %+v", w.ID, rows)
		}
	})

	t.Run("wisp slot set/clear", func(t *testing.T) {
		// Wisp slot writes route through mergeMetadataWisp/clearMetadataWisp,
		// which mint their own transaction rather than going through
		// withWriteTx. That branch journaled nothing while the identical
		// permanent-issue branch journaled fine — the asymmetry this covers.
		w := &types.Issue{Title: "wisp slots", IssueType: types.TypeTask, Status: types.StatusOpen, Ephemeral: true}
		if err := store.CreateIssue(ctx, w, "actor"); err != nil {
			t.Fatalf("create wisp: %v", err)
		}
		clearJournal(t, store)
		if err := store.SlotSet(ctx, w.ID, "phase", "build", "actor"); err != nil {
			t.Fatalf("slot set on wisp: %v", err)
		}
		if rows := readJournalRows(t, store); !hasOpFor(rows, "update", w.ID) {
			t.Fatalf("wisp SlotSet must journal an update for %s; got %+v", w.ID, rows)
		}
		clearJournal(t, store)
		if err := store.SlotClear(ctx, w.ID, "phase", "actor"); err != nil {
			t.Fatalf("slot clear on wisp: %v", err)
		}
		if rows := readJournalRows(t, store); !hasOpFor(rows, "update", w.ID) {
			t.Fatalf("wisp SlotClear must journal an update for %s; got %+v", w.ID, rows)
		}
	})

	t.Run("transaction comment writes", func(t *testing.T) {
		// The storage.Transaction surface writes comment rows directly rather
		// than through the journaling issueops leaves, so both methods must
		// journal on their own.
		if err := store.CreateIssue(ctx, mk("bd-tx-comment-1"), "actor"); err != nil {
			t.Fatalf("create: %v", err)
		}
		clearJournal(t, store)
		imported := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
		if err := store.RunInTransaction(ctx, "journal transaction comments", func(tx storage.Transaction) error {
			if _, err := tx.ImportIssueComment(ctx, "bd-tx-comment-1", "alice", "imported in tx", imported); err != nil {
				return err
			}
			return tx.AddComment(ctx, "bd-tx-comment-1", "bob", "audit in tx")
		}); err != nil {
			t.Fatalf("transaction comments: %v", err)
		}
		rows := readJournalRows(t, store)
		var comments int
		for _, r := range rows {
			if r.op == "comment" && r.id == "bd-tx-comment-1" {
				comments++
			}
		}
		if comments != 2 {
			t.Fatalf("transaction comment writes must each journal a comment op; got %+v", rows)
		}
	})

	t.Run("transaction label writes", func(t *testing.T) {
		if err := store.CreateIssue(ctx, mk("bd-tx-label-1"), "actor"); err != nil {
			t.Fatalf("create: %v", err)
		}
		clearJournal(t, store)
		if err := store.RunInTransaction(ctx, "journal transaction labels", func(tx storage.Transaction) error {
			if err := tx.AddLabel(ctx, "bd-tx-label-1", "demo", "actor"); err != nil {
				return err
			}
			return tx.RemoveLabel(ctx, "bd-tx-label-1", "demo", "actor")
		}); err != nil {
			t.Fatalf("transaction labels: %v", err)
		}
		rows := readJournalRows(t, store)
		if len(rows) != 2 || !hasOpFor(rows, "update", "bd-tx-label-1") {
			t.Fatalf("transaction add/remove label must each journal an update; got %+v", rows)
		}
	})

	t.Run("reopen", func(t *testing.T) {
		if err := store.CreateIssue(ctx, mk("bd-ro-1"), "actor"); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.CloseIssue(ctx, "bd-ro-1", "done", "actor", ""); err != nil {
			t.Fatalf("close: %v", err)
		}
		clearJournal(t, store)
		if err := store.ReopenIssue(ctx, "bd-ro-1", "back", "actor"); err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if rows := readJournalRows(t, store); !hasOpFor(rows, "update", "bd-ro-1") {
			t.Fatalf("reopen must journal an update; got %+v", rows)
		}
	})

	t.Run("ready-claim and unclaim", func(t *testing.T) {
		if err := store.CreateIssue(ctx, mk("bd-rc-1"), "actor"); err != nil {
			t.Fatalf("create: %v", err)
		}
		clearJournal(t, store)
		claimed, err := store.ClaimReadyIssue(ctx, types.WorkFilter{}, "worker")
		if err != nil {
			t.Fatalf("claim-ready: %v", err)
		}
		if claimed == nil {
			t.Fatalf("claim-ready returned no issue")
		}
		if rows := readJournalRows(t, store); !hasOpFor(rows, "update", claimed.ID) {
			t.Fatalf("ready-claim must journal an update for %s; got %+v", claimed.ID, rows)
		}
		clearJournal(t, store)
		if err := store.UnclaimIssue(ctx, claimed.ID, "worker", false); err != nil {
			t.Fatalf("unclaim: %v", err)
		}
		if rows := readJournalRows(t, store); !hasOpFor(rows, "update", claimed.ID) {
			t.Fatalf("unclaim must journal an update for %s; got %+v", claimed.ID, rows)
		}
	})

	t.Run("lease reclaim", func(t *testing.T) {
		if err := store.CreateIssue(ctx, mk("bd-lease-1"), "actor"); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.ClaimIssue(ctx, "bd-lease-1", "worker"); err != nil {
			t.Fatalf("claim: %v", err)
		}
		clearJournal(t, store)
		// Negative olderThan pushes the cutoff into the future so the fresh lease
		// counts as expired and is reclaimed deterministically.
		reclaimed, err := store.ReclaimExpiredLeases(ctx, -24*time.Hour, types.ReclaimFilter{}, "reaper")
		if err != nil {
			t.Fatalf("reclaim: %v", err)
		}
		if len(reclaimed) == 0 {
			t.Fatalf("expected at least one reclaimed lease")
		}
		if rows := readJournalRows(t, store); !hasOpFor(rows, "update", reclaimed[0].ID) {
			t.Fatalf("lease reclaim must journal an update for %s; got %+v", reclaimed[0].ID, rows)
		}
	})

	t.Run("delete by source repo", func(t *testing.T) {
		if err := store.CreateIssue(ctx, mk("bd-sr-1"), "actor"); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.CreateIssue(ctx, mk("bd-sr-2"), "actor"); err != nil {
			t.Fatalf("create: %v", err)
		}
		// source_repo is internal routing state not set on the create path here;
		// stamp it directly so the bulk delete has a repo to match.
		if _, err := store.db.ExecContext(ctx,
			"UPDATE issues SET source_repo = 'repo-z' WHERE id IN ('bd-sr-1','bd-sr-2')"); err != nil {
			t.Fatalf("stamp source_repo: %v", err)
		}
		clearJournal(t, store)
		n, err := store.DeleteIssuesBySourceRepo(ctx, "repo-z")
		if err != nil {
			t.Fatalf("delete by source repo: %v", err)
		}
		if n != 2 {
			t.Fatalf("expected 2 deleted, got %d", n)
		}
		rows := readJournalRows(t, store)
		if !hasOpFor(rows, "delete", "bd-sr-1") || !hasOpFor(rows, "delete", "bd-sr-2") {
			t.Fatalf("by-source-repo bulk delete must journal each row; got %+v", rows)
		}
	})

	t.Run("creation-time comments", func(t *testing.T) {
		// Bulk import carries comments inline on the issue. Issue hydration
		// joins labels but NOT comments, so the create snapshot cannot carry
		// them — without their own ops an imported comment is unreplayable,
		// which is the whole point of the comment op.
		clearJournal(t, store)
		withComments := mk("test-ct-comments")
		withComments.Comments = []*types.Comment{
			{Author: "alice", Text: "first imported", CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
			{Author: "bob", Text: "second imported", CreatedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)},
		}
		if err := store.CreateIssues(ctx, []*types.Issue{withComments}, "actor"); err != nil {
			t.Fatalf("create with comments: %v", err)
		}
		rows := readJournalRows(t, store)
		var createSeq int64
		var commentSeqs []int64
		for _, r := range rows {
			if r.id != "test-ct-comments" {
				continue
			}
			switch r.op {
			case "create":
				createSeq = r.seq
			case "comment":
				commentSeqs = append(commentSeqs, r.seq)
			}
		}
		if createSeq == 0 {
			t.Fatalf("create must journal; got %+v", rows)
		}
		if len(commentSeqs) != 2 {
			t.Fatalf("each creation-time comment must journal a comment op; got %+v", rows)
		}
		for _, seq := range commentSeqs {
			if seq <= createSeq {
				t.Fatalf("comment op at seq %d must follow the create at seq %d — a consumer must not be told about a comment on a bead it has not seen created", seq, createSeq)
			}
		}
		// The payload must carry the replayable text, not just a snapshot.
		var payloads []issueops.EventComment
		crows, err := store.db.QueryContext(ctx,
			`SELECT comment_json FROM bd_events_journal WHERE op = 'comment' AND issue_id = ? ORDER BY seq`, "test-ct-comments")
		if err != nil {
			t.Fatal(err)
		}
		defer crows.Close()
		for crows.Next() {
			var raw []byte
			if err := crows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var c issueops.EventComment
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			payloads = append(payloads, c)
		}
		if err := crows.Err(); err != nil {
			t.Fatal(err)
		}
		if len(payloads) != 2 || payloads[0].Text != "first imported" || payloads[1].Text != "second imported" {
			t.Fatalf("creation-time comment payloads = %+v, want both texts replayable", payloads)
		}
		if payloads[0].ID == "" || payloads[0].Author != "alice" {
			t.Fatalf("creation-time comment payload missing id/author: %+v", payloads[0])
		}
	})

	t.Run("creation-time dependency", func(t *testing.T) {
		if err := store.CreateIssue(ctx, mk("test-ct-target"), "actor"); err != nil {
			t.Fatalf("create target: %v", err)
		}
		clearJournal(t, store)
		dep := mk("test-ct-dep")
		dep.Dependencies = []*types.Dependency{{
			IssueID: "test-ct-dep", DependsOnID: "test-ct-target", Type: types.DepBlocks,
		}}
		if err := store.CreateIssues(ctx, []*types.Issue{dep}, "actor"); err != nil {
			t.Fatalf("create with dep: %v", err)
		}
		rows := readJournalRows(t, store)
		if !hasOpFor(rows, "create", "test-ct-dep") {
			t.Fatalf("create must journal; got %+v", rows)
		}
		if !hasOpFor(rows, "dep_add", "test-ct-dep") {
			t.Fatalf("creation-time dependency must journal a dep_add for the source; got %+v", rows)
		}
	})
}

// TestEventsJournal_RenameRelationshipDeltas proves a cursor consumer can
// replay a rename without querying current graph state. Both source and target
// endpoints are explicit in old-edge removals and new-edge additions.
func TestEventsJournal_RenameRelationshipDeltas(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	for _, id := range []string{"bd-rn-old", "bd-rn-out", "bd-rn-in"} {
		if err := store.CreateIssue(ctx, mk(id), "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "bd-rn-old", DependsOnID: "bd-rn-out", Type: types.DepBlocks},
		{IssueID: "bd-rn-in", DependsOnID: "bd-rn-old", Type: types.DepRelated},
	} {
		if err := store.AddDependency(ctx, dep, "actor"); err != nil {
			t.Fatalf("add dependency %+v: %v", dep, err)
		}
	}
	clearJournal(t, store)
	issue, err := store.GetIssue(ctx, "bd-rn-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateIssueID(ctx, "bd-rn-old", "bd-rn-new", issue, "actor"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	got := journalDepDeltas(t, store)
	want := []string{
		"dep_remove:bd-rn-in->bd-rn-old:related",
		"dep_remove:bd-rn-old->bd-rn-out:blocks",
		"delete:bd-rn-old",
		"create:bd-rn-new",
		"dep_add:bd-rn-in->bd-rn-new:related",
		"dep_add:bd-rn-new->bd-rn-out:blocks",
	}
	if len(got) != len(want) {
		t.Fatalf("rename journal = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rename journal[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

// TestEventsJournal_DeleteRelationshipDeltas guards the direct-store path:
// both incoming and outgoing edges are explicit dep_remove rows before the
// deleted node, so replay never relies on implicit database cascades.
func TestEventsJournal_DeleteRelationshipDeltas(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	for _, id := range []string{"bd-del-center", "bd-del-in", "bd-del-out"} {
		if err := store.CreateIssue(ctx, mk(id), "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "bd-del-center", DependsOnID: "bd-del-out", Type: types.DepBlocks},
		{IssueID: "bd-del-in", DependsOnID: "bd-del-center", Type: types.DepRelated},
	} {
		if err := store.AddDependency(ctx, dep, "actor"); err != nil {
			t.Fatalf("add dependency %+v: %v", dep, err)
		}
	}
	clearJournal(t, store)
	if err := store.DeleteIssue(ctx, "bd-del-center"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got := journalDepDeltas(t, store)
	want := []string{
		"dep_remove:bd-del-center->bd-del-out:blocks",
		"dep_remove:bd-del-in->bd-del-center:related",
		"delete:bd-del-center",
	}
	if len(got) != len(want) {
		t.Fatalf("delete journal = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delete journal[%d] = %q, want %q (all=%v)", i, got[i], want[i], got)
		}
	}
}

// TestEventsJournal_CascadeDeleteJournalsEveryChild asserts a cascade delete
// records every bead it removes — the finding the decorator design could not
// cover.
func TestEventsJournal_CascadeDeleteJournalsEveryChild(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	for _, id := range []string{"bd-cd-parent", "bd-cd-child", "bd-cd-grandchild"} {
		if err := store.CreateIssue(ctx, mk(id), "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	for _, dep := range []*types.Dependency{
		{IssueID: "bd-cd-child", DependsOnID: "bd-cd-parent", Type: types.DepParentChild},
		{IssueID: "bd-cd-grandchild", DependsOnID: "bd-cd-child", Type: types.DepParentChild},
	} {
		if err := store.AddDependency(ctx, dep, "actor"); err != nil {
			t.Fatalf("add dependency %+v: %v", dep, err)
		}
	}
	clearJournal(t, store)

	if _, err := store.DeleteIssues(ctx, []string{"bd-cd-parent"}, true, true, false); err != nil {
		t.Fatalf("cascade delete: %v", err)
	}
	rows := readJournalRows(t, store)
	for _, id := range []string{"bd-cd-parent", "bd-cd-child", "bd-cd-grandchild"} {
		if !hasOpFor(rows, "delete", id) {
			t.Fatalf("cascade delete must journal %s; got %+v", id, rows)
		}
	}
}

// TestEventsJournal_ReplayFromZero proves the journal is a complete, ordered,
// replayable record: applying every row in seq order (set snapshot on
// create/update/close/comment/dep_*, drop on delete) reconstructs exactly the
// store's final live set.
func TestEventsJournal_ReplayFromZero(t *testing.T) {
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
	must(store.CreateIssue(ctx, mk("bd-rp-1"), "a"), "create 1")
	must(store.CreateIssue(ctx, mk("bd-rp-2"), "a"), "create 2")
	must(store.CreateIssue(ctx, mk("bd-rp-3"), "a"), "create 3")
	must(store.UpdateIssue(ctx, "bd-rp-1", map[string]interface{}{"title": "renamed"}, "a"), "update")
	must(store.CloseIssue(ctx, "bd-rp-2", "done", "a", ""), "close")
	must(store.DeleteIssue(ctx, "bd-rp-3"), "delete")

	live := map[string]bool{}
	for _, r := range readJournalRows(t, store) {
		if r.op == "delete" {
			delete(live, r.id)
			continue
		}
		live[r.id] = true
	}

	got := make([]string, 0, len(live))
	for id := range live {
		got = append(got, id)
	}
	sort.Strings(got)
	// The store's actual surviving set: rp-1 (updated) and rp-2 (closed but still
	// a row); rp-3 was deleted.
	want := []string{"bd-rp-1", "bd-rp-2"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("replayed live set %v, want %v", got, want)
	}
	for _, id := range want {
		if _, err := store.GetIssue(ctx, id); err != nil {
			t.Fatalf("replayed id %s should still exist in the store: %v", id, err)
		}
	}
	if _, err := store.GetIssue(ctx, "bd-rp-3"); err == nil {
		t.Fatalf("bd-rp-3 was deleted; replay must not resurrect it")
	}
}

// TestEventsJournal_DependencySnapshotsFollowBlockedState proves that a
// consumer never observes a dependency delta whose issue snapshot predates the
// derived is_blocked state committed with that graph change. It also pins the
// exact metadata replacement payload used to replay same-type refreshes.
func TestEventsJournal_DependencySnapshotsFollowBlockedState(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	for _, id := range []string{"bd-js-source", "bd-js-target"} {
		if err := store.CreateIssue(ctx, mk(id), "actor"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	clearJournal(t, store)

	add := func(metadata string) {
		t.Helper()
		if err := store.AddDependency(ctx, &types.Dependency{IssueID: "bd-js-source", DependsOnID: "bd-js-target", Type: types.DepBlocks, Metadata: metadata}, "actor"); err != nil {
			t.Fatalf("add dependency: %v", err)
		}
	}
	readLast := func() (types.Issue, issueops.EventDep) {
		t.Helper()
		var issueJSON, depJSON []byte
		if err := store.db.QueryRowContext(ctx, `SELECT issue_json, dep_json FROM bd_events_journal ORDER BY seq DESC LIMIT 1`).Scan(&issueJSON, &depJSON); err != nil {
			t.Fatalf("read journal: %v", err)
		}
		var issue types.Issue
		var dep issueops.EventDep
		if err := json.Unmarshal(issueJSON, &issue); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(depJSON, &dep); err != nil {
			t.Fatal(err)
		}
		return issue, dep
	}

	add(`{"revision":"A"}`)
	issue, dep := readLast()
	if !issue.IsBlocked || dep.Metadata != `{"revision":"A"}` {
		t.Fatalf("initial dep_add = issue(blocked=%v), dep=%+v; want blocked snapshot and metadata A", issue.IsBlocked, dep)
	}
	add(`{"revision":"B"}`)
	issue, dep = readLast()
	if !issue.IsBlocked || dep.Metadata != `{"revision":"B"}` {
		t.Fatalf("metadata refresh = issue(blocked=%v), dep=%+v; want blocked snapshot and metadata B", issue.IsBlocked, dep)
	}
	if err := store.RemoveDependency(ctx, "bd-js-source", "bd-js-target", "actor"); err != nil {
		t.Fatalf("remove dependency: %v", err)
	}
	issue, dep = readLast()
	if issue.IsBlocked || dep.Metadata != `{"revision":"B"}` {
		t.Fatalf("dep_remove = issue(blocked=%v), dep=%+v; want unblocked snapshot and metadata B", issue.IsBlocked, dep)
	}
}

// TestEventsJournal_DerivedBlockedStateChanges proves that mutations affecting
// another bead's persisted readiness emit a post-recompute update for that
// bead. Without these rows, cursor consumers retain stale is_blocked values
// until a later full reconciliation.
func TestEventsJournal_DerivedBlockedStateChanges(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)

	mk := func(id string) *types.Issue {
		return &types.Issue{ID: id, Title: id, IssueType: types.TypeTask, Status: types.StatusOpen}
	}
	mustCreate := func(tb testing.TB, ids ...string) {
		tb.Helper()
		for _, id := range ids {
			if err := store.CreateIssue(ctx, mk(id), "actor"); err != nil {
				tb.Fatalf("create %s: %v", id, err)
			}
		}
	}
	mustAdd := func(tb testing.TB, source, target string, kind types.DependencyType) {
		tb.Helper()
		if err := store.AddDependency(ctx, &types.Dependency{
			IssueID: source, DependsOnID: target, Type: kind,
		}, "actor"); err != nil {
			tb.Fatalf("add dependency %s -> %s (%s): %v", source, target, kind, err)
		}
	}
	assertBlocked := func(tb testing.TB, id string, want bool) {
		tb.Helper()
		var got int
		if err := store.db.QueryRowContext(ctx, "SELECT is_blocked FROM issues WHERE id = ?", id).Scan(&got); err != nil {
			tb.Fatalf("read is_blocked for %s: %v", id, err)
		}
		if (got != 0) != want {
			tb.Fatalf("%s is_blocked = %d, want %v", id, got, want)
		}
	}
	assertJournalUpdate := func(tb testing.TB, id string, wantBlocked bool) {
		tb.Helper()
		rows, err := store.db.QueryContext(ctx,
			`SELECT issue_json FROM bd_events_journal WHERE op = 'update' AND issue_id = ? ORDER BY seq`, id)
		if err != nil {
			tb.Fatal(err)
		}
		defer rows.Close()
		var snapshots []types.Issue
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				tb.Fatal(err)
			}
			var issue types.Issue
			if err := json.Unmarshal(raw, &issue); err != nil {
				tb.Fatal(err)
			}
			snapshots = append(snapshots, issue)
		}
		if err := rows.Err(); err != nil {
			tb.Fatal(err)
		}
		if len(snapshots) != 1 || snapshots[0].IsBlocked != wantBlocked {
			tb.Fatalf("journal updates for %s = %+v, want one snapshot with is_blocked=%v", id, snapshots, wantBlocked)
		}
	}

	t.Run("closing blocker updates depender", func(t *testing.T) {
		mustCreate(t, "bd-derived-close-source", "bd-derived-close-target")
		mustAdd(t, "bd-derived-close-source", "bd-derived-close-target", types.DepBlocks)
		assertBlocked(t, "bd-derived-close-source", true)
		clearJournal(t, store)

		if err := store.CloseIssue(ctx, "bd-derived-close-target", "done", "actor", ""); err != nil {
			t.Fatal(err)
		}
		assertBlocked(t, "bd-derived-close-source", false)
		assertJournalUpdate(t, "bd-derived-close-source", false)
	})

	t.Run("deleting blocker updates depender", func(t *testing.T) {
		mustCreate(t, "bd-derived-delete-source", "bd-derived-delete-target")
		mustAdd(t, "bd-derived-delete-source", "bd-derived-delete-target", types.DepBlocks)
		assertBlocked(t, "bd-derived-delete-source", true)
		clearJournal(t, store)

		if err := store.DeleteIssue(ctx, "bd-derived-delete-target"); err != nil {
			t.Fatal(err)
		}
		assertBlocked(t, "bd-derived-delete-source", false)
		assertJournalUpdate(t, "bd-derived-delete-source", false)
	})

	t.Run("removing parent blocker updates descendant", func(t *testing.T) {
		mustCreate(t, "bd-derived-parent", "bd-derived-child", "bd-derived-parent-target")
		mustAdd(t, "bd-derived-child", "bd-derived-parent", types.DepParentChild)
		mustAdd(t, "bd-derived-parent", "bd-derived-parent-target", types.DepBlocks)
		assertBlocked(t, "bd-derived-parent", true)
		assertBlocked(t, "bd-derived-child", true)
		clearJournal(t, store)

		if err := store.RemoveDependency(ctx, "bd-derived-parent", "bd-derived-parent-target", "actor"); err != nil {
			t.Fatal(err)
		}
		assertBlocked(t, "bd-derived-parent", false)
		assertBlocked(t, "bd-derived-child", false)
		assertJournalUpdate(t, "bd-derived-child", false)
	})
}

// TestEventsJournal_CommentPayloads pins the exact comment record returned by
// the structured, imported, and audit-comment write paths.
func TestEventsJournal_CommentPayloads(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx := context.Background()
	enableJournalForTest(t, store)
	issue := &types.Issue{ID: "bd-jc", Title: "comments", IssueType: types.TypeTask, Status: types.StatusOpen}
	if err := store.CreateIssue(ctx, issue, "actor"); err != nil {
		t.Fatal(err)
	}
	clearJournal(t, store)

	first, err := store.AddIssueComment(ctx, issue.ID, "alice", "structured")
	if err != nil {
		t.Fatal(err)
	}
	importedAt := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	second, err := store.ImportIssueComment(ctx, issue.ID, "bob", "imported", importedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddComment(ctx, issue.ID, "carol", "audit"); err != nil {
		t.Fatal(err)
	}

	rows, err := store.db.QueryContext(ctx, `SELECT op, comment_json FROM bd_events_journal ORDER BY seq ASC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []issueops.EventComment
	for rows.Next() {
		var op string
		var raw []byte
		if err := rows.Scan(&op, &raw); err != nil {
			t.Fatal(err)
		}
		if op != string(issueops.EventCommentWrite) {
			t.Fatalf("op = %q, want comment", op)
		}
		var comment issueops.EventComment
		if err := json.Unmarshal(raw, &comment); err != nil {
			t.Fatal(err)
		}
		got = append(got, comment)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("comment rows = %+v, want three", got)
	}
	if got[0].ID != first.ID || got[0].Author != "alice" || got[0].Text != "structured" || got[0].Source != "structured" {
		t.Fatalf("structured comment = %+v", got[0])
	}
	if got[1].ID != second.ID || !got[1].CreatedAt.Equal(importedAt) || got[1].Source != "structured" {
		t.Fatalf("imported comment = %+v, want id %s timestamp %s", got[1], second.ID, importedAt)
	}
	if got[2].ID == "" || got[2].Author != "carol" || got[2].Text != "audit" || got[2].Source != "audit" || got[2].CreatedAt.IsZero() {
		t.Fatalf("audit comment = %+v", got[2])
	}
}
