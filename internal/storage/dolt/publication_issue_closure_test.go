package dolt

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/testutil/sqlgate"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// wantIssueClosure is issueClosureTables as these tests expect it, written
// out so a change to the production list has to change this one too.
var wantIssueClosure = []string{
	"child_counters", "comments", "compaction_snapshots", "dependencies",
	"id_tombstones", "issue_snapshots", "issues", "labels", "provenance_events",
}

// TestWithIssueClosure pins the widening rule: a list that names any table of
// the issue closure gains the rest of it after its own tables, and any other
// list, the config plane's included, is returned as it is.
func TestWithIssueClosure(t *testing.T) {
	if got := slices.Sorted(slices.Values(issueClosureTables)); !slices.Equal(got, wantIssueClosure) {
		t.Fatalf("issueClosureTables = %v, want %v", got, wantIssueClosure)
	}
	for _, tc := range []struct {
		name   string
		tables []string
		want   []string
	}{
		{name: "close", tables: []string{"issues", "events"},
			want: []string{"issues", "events", "dependencies", "labels", "comments", "child_counters",
				"issue_snapshots", "compaction_snapshots", "provenance_events", "id_tombstones"}},
		{name: "label add", tables: []string{"events", "labels"},
			want: []string{"events", "labels", "issues", "dependencies", "comments", "child_counters",
				"issue_snapshots", "compaction_snapshots", "provenance_events", "id_tombstones"}},
		{name: "provenance write", tables: []string{"provenance_events"},
			want: []string{"provenance_events", "issues", "dependencies", "labels", "comments",
				"child_counters", "issue_snapshots", "compaction_snapshots", "id_tombstones"}},
		{name: "metadata", tables: []string{"metadata"}, want: []string{"metadata"}},
		{name: "config plane", tables: []string{"config", "custom_statuses", "custom_types"},
			want: []string{"config", "custom_statuses", "custom_types"}},
		{name: "events only", tables: []string{"events"}, want: []string{"events"}},
		{name: "empty", tables: nil, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.tables)
			got := withIssueClosure(tc.tables)
			if !slices.Equal(got, tc.want) {
				t.Errorf("withIssueClosure(%v) = %v, want %v", tc.tables, got, tc.want)
			}
			if !slices.Equal(tc.tables, before) {
				t.Errorf("withIssueClosure modified its argument: %v, was %v", tc.tables, before)
			}
		})
	}
}

// TestMarkIssueClosureDirty is TestWithIssueClosure for a transaction's
// dirty-table tracker: a tracker that dirtied a closure table gains the
// closure, and one that dirtied none of it, or nothing, is left alone.
func TestMarkIssueClosureDirty(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dirty []string
		want  []string
	}{
		{name: "create", dirty: []string{"issues", "events"},
			want: append(slices.Clone(wantIssueClosure), "events")},
		{name: "issue counter only", dirty: []string{"issue_counter"}, want: []string{"issue_counter"}},
		{name: "nothing dirty", dirty: nil, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tracker versioncontrolops.DirtyTableTracker
			for _, table := range tc.dirty {
				tracker.MarkDirty(table)
			}
			markIssueClosureDirty(&tracker)
			var got []string
			for table := range tracker.DirtyTables() {
				got = append(got, table)
			}
			slices.Sort(got)
			want := slices.Sorted(slices.Values(tc.want))
			if !slices.Equal(got, want) {
				t.Errorf("after markIssueClosureDirty, dirty tables = %v, want %v", got, want)
			}
		})
	}
}

// TestIssueClosureTablesCoverEveryForeignKeyToIssues pins issueClosureTables
// against the migrated schema: every versioned table with a foreign key to
// issues must be on it, and nothing else may be but issues itself and
// id_tombstones, so that a migration adding a child table of issues has to
// add it here.
func TestIssueClosureTablesCoverEveryForeignKeyToIssues(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	rows, err := store.db.QueryContext(ctx, `
		SELECT DISTINCT kcu.TABLE_NAME FROM information_schema.KEY_COLUMN_USAGE kcu
		WHERE kcu.TABLE_SCHEMA = DATABASE() AND kcu.REFERENCED_TABLE_NAME = 'issues'
		AND NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1 AND kcu.TABLE_NAME LIKE di.pattern
		)`)
	if err != nil {
		t.Fatalf("read the foreign keys to issues: %v", err)
	}
	defer rows.Close()
	children := []string{}
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan a table with a foreign key to issues: %v", err)
		}
		children = append(children, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the foreign keys to issues: %v", err)
	}
	if !slices.Contains(children, "dependencies") {
		t.Fatalf("versioned tables with a foreign key to issues = %v, which lacks dependencies: the query read no keys", children)
	}
	want := slices.Sorted(slices.Values(append(children, "issues", "id_tombstones")))
	if got := slices.Sorted(slices.Values(issueClosureTables)); !slices.Equal(got, want) {
		t.Fatalf("issueClosureTables = %v, want issues, id_tombstones and every versioned table with a foreign key to issues: %v", got, want)
	}
}

// heldWrite is a write the closure tests hold between its SQL COMMIT and its
// Dolt publication.
type heldWrite struct {
	name string
	// prepare seeds what the write needs and returns the write of id.
	prepare func(ctx context.Context, t *testing.T, id string) func(context.Context) error
}

// holdInPublicationWindow runs write under gate until it has committed in SQL
// and reached its publication's CALL DOLT_ADD, runs inWindow there, then
// releases the write and waits for it.
func holdInPublicationWindow(ctx context.Context, t *testing.T, gate *sqlgate.Connector, write func(context.Context) error, inWindow func()) {
	t.Helper()
	label := t.Name()
	first := gate.Arm(label)
	defer first.Release()
	done := make(chan error, 1)
	go func() { done <- write(sqlgate.WithLabel(ctx, label)) }()
	select {
	case <-first.Reached():
	case err := <-done:
		t.Fatalf("the write finished before its SQL commit point: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the write never reached its SQL commit point")
	}
	if kind := gate.CommitKind(label); kind != "Tx.Commit" {
		t.Fatalf("the write was held at %q, want its SQL Tx.Commit", kind)
	}
	// The label's next commit point is the publication's CALL DOLT_ADD,
	// after the SQL COMMIT landed.
	second := gate.Arm(label)
	defer second.Release()
	first.Release()
	select {
	case <-second.Reached():
	case err := <-done:
		t.Fatalf("the write finished without reaching its publication: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the write never reached its publication")
	}
	inWindow()
	second.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the held write: %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the held write did not finish after its release")
	}
}

// countAsOf counts the rows query (a format with one %s for the commit and one
// ? for id) finds at commit.
func countAsOf(ctx context.Context, t *testing.T, store *DoltStore, query, commit, id string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRowContext(ctx, fmt.Sprintf(query, commit), id).Scan(&n); err != nil {
		t.Fatalf("count at %s: %v", commit, err)
	}
	return n
}

// TestIssueClosure_PeerCommitInDeleteWindowPublishesTheDeleteWhole holds a
// delete or demotion of b after its SQL COMMIT and before its Dolt
// publication, while an unrelated write publishes. b has a blocking dependent,
// a label and a comment, so its removal from issues takes rows out of
// dependencies, labels and comments in the same transaction. The peer stages
// issues (a close, through stageAndCommit; a create in RunInTransaction,
// through the transaction publisher): without the issue closure its commit
// took b's issues row removal but kept the HEAD rows naming b, a published
// commit that breaks fk_dep_issue_target, fk_labels_issue and
// fk_comments_issue. With it, the peer's commit carries the whole delete.
func TestIssueClosure_PeerCommitInDeleteWindowPublishesTheDeleteWhole(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	gate := raceGatedStore(t, store)

	roleDelete := func(ctx context.Context, req publicops.DeleteRequest) error {
		deleter, err := store.Deleter()
		if err != nil {
			return err
		}
		res, err := deleter.Delete(ctx, req)
		if err == nil && res.Deleted != 1 {
			return fmt.Errorf("deleted %d rows, want 1", res.Deleted)
		}
		return err
	}
	deleteIssue := heldWrite{"DeleteIssue", func(_ context.Context, _ *testing.T, id string) func(context.Context) error {
		return func(ctx context.Context) error { return store.DeleteIssue(ctx, id) }
	}}
	writes := []heldWrite{
		deleteIssue,
		{"DeleteIssues", func(_ context.Context, _ *testing.T, id string) func(context.Context) error {
			return func(ctx context.Context) error {
				res, err := store.DeleteIssues(ctx, []string{id}, false, true, false)
				if err == nil && res.DeletedCount != 1 {
					return fmt.Errorf("deleted %d issues, want 1", res.DeletedCount)
				}
				return err
			}
		}},
		{"Deleter", func(_ context.Context, _ *testing.T, id string) func(context.Context) error {
			return func(ctx context.Context) error {
				return roleDelete(ctx, publicops.DeleteRequest{Actor: "tester", IDs: []string{id}, Force: true})
			}
		}},
		{"DeleterFenced", func(ctx context.Context, t *testing.T, id string) func(context.Context) error {
			issue, err := store.GetIssue(ctx, id)
			if err != nil {
				t.Fatalf("read %s's version: %v", id, err)
			}
			version := issue.RowVersion
			return func(ctx context.Context) error {
				return roleDelete(ctx, publicops.DeleteRequest{Actor: "tester", IDs: []string{id}, Force: true, ExpectedVersion: &version})
			}
		}},
		{"DemoteToWisp", func(_ context.Context, _ *testing.T, id string) func(context.Context) error {
			return func(ctx context.Context) error {
				return store.DemoteToWisp(ctx, id, map[string]interface{}{"wisp": true}, "tester")
			}
		}},
	}
	type peer struct {
		name string
		// write publishes an unrelated write for case i and returns its
		// commit message.
		write func(ctx context.Context, t *testing.T, i int) string
	}
	closePeer := peer{"close", func(ctx context.Context, t *testing.T, i int) string {
		a := fmt.Sprintf("pd%d-a", i)
		if err := store.CloseIssue(ctx, a, "done", "tester", ""); err != nil {
			t.Fatalf("close %s inside the window: %v", a, err)
		}
		return "bd: close " + a
	}}
	// bd create's path: RunInTransaction publishes through the transaction
	// publisher (finishDoltTransaction), not stageAndCommit.
	txCreatePeer := peer{"transaction create", func(ctx context.Context, t *testing.T, i int) string {
		z := fmt.Sprintf("pd%d-z", i)
		msg := "bd: create " + z + " in a transaction"
		if err := store.RunInTransaction(ctx, msg, func(tx storage.Transaction) error {
			return tx.CreateIssue(ctx, &types.Issue{
				ID: z, Title: "peer " + z, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask,
			}, "tester")
		}); err != nil {
			t.Fatalf("create %s inside the window: %v", z, err)
		}
		return msg
	}}
	type closureCase struct {
		write heldWrite
		peer  peer
	}
	var cases []closureCase
	for _, w := range writes {
		cases = append(cases, closureCase{w, closePeer})
	}
	cases = append(cases, closureCase{deleteIssue, txCreatePeer})

	for i, tc := range cases {
		t.Run(tc.write.name+"/peer "+tc.peer.name, func(t *testing.T) {
			ctx, cancel := testContext(t)
			defer cancel()
			a, b, c := fmt.Sprintf("pd%d-a", i), fmt.Sprintf("pd%d-b", i), fmt.Sprintf("pd%d-c", i)
			for _, id := range []string{a, b, c} {
				createPerm(t, ctx, store, id)
			}
			addDependency(t, ctx, store, c, b, types.DepBlocks)
			if err := store.AddLabel(ctx, b, "closure", "tester"); err != nil {
				t.Fatalf("label %s: %v", b, err)
			}
			if _, err := store.AddIssueComment(ctx, b, "tester", "closure comment"); err != nil {
				t.Fatalf("comment on %s: %v", b, err)
			}
			requireCleanTables(ctx, t, store, "issues", "dependencies", "labels", "comments")
			write := tc.write.prepare(ctx, t, b)

			var peerMsg string
			holdInPublicationWindow(ctx, t, gate, write, func() {
				var present int
				if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM issues WHERE id = ?", b).Scan(&present); err != nil {
					t.Fatalf("read %s: %v", b, err)
				}
				if present != 0 {
					t.Fatalf("%s is still in the working set while the publication is held", b)
				}
				peerMsg = tc.peer.write(ctx, t, i)
			})
			requireCleanTables(ctx, t, store, "issues", "dependencies", "labels", "comments")

			log := readDoltLog(ctx, t, store)
			peerAt := doltLogIndex(log, func(m string) bool { return m == peerMsg })
			if peerAt < 0 {
				t.Fatalf("dolt_log has no %q commit; log: %v", peerMsg, log)
			}
			const asOf = " AS OF '%s'"
			if n := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM issues"+asOf+" WHERE id = ?", log[peerAt].hash, b); n != 0 {
				t.Fatalf("the peer's commit %s still holds %s: it did not publish inside the delete's window", log[peerAt].hash, b)
			}
			for _, entry := range log[:peerAt+1] {
				issues := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM issues"+asOf+" WHERE id = ?", entry.hash, b)
				deps := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM dependencies"+asOf+" WHERE depends_on_issue_id = ?", entry.hash, b)
				labels := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM labels"+asOf+" WHERE issue_id = ?", entry.hash, b)
				comments := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM comments"+asOf+" WHERE issue_id = ?", entry.hash, b)
				if issues == 0 && deps+labels+comments > 0 {
					t.Errorf("published commit %s %q has no %s issues row but %d dependencies, %d labels and %d comments rows naming it",
						entry.hash, entry.message, b, deps, labels, comments)
				}
			}
		})
	}
}

// TestIssueClosure_ExplicitCommitInDeleteWindowPublishesTheDeleteWhole is
// the delete window for commitWorkingSet, behind the explicit commit points
// (Commit, which the CLI's auto-commit after a command calls; the commit
// before a pull and the one after its blocked-state recompute; a merge
// resolution). It stages the tables its dolt_status read listed, so its
// window is between that read and its DOLT_ADD. Here the read sees only a
// pending issues edit; then DeleteIssue(b) commits in SQL and is held before
// its own publication; then the explicit commit stages. Staging the listed
// tables alone published b's issues row removal while the dependencies,
// labels and comments rows naming b stayed at HEAD. With the closure the
// explicit commit carries the whole delete.
func TestIssueClosure_ExplicitCommitInDeleteWindowPublishesTheDeleteWhole(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	gate := raceGatedStore(t, store)
	// The commit before a pull refuses a dirty internal config key (GH#2455),
	// and the fixture's issue_prefix may be one.
	settleCtx, settleCancel := testContext(t)
	defer settleCancel()
	if err := store.CommitWithConfig(settleCtx, "settle the fixture's config"); err != nil {
		t.Fatalf("settle config: %v", err)
	}

	for i, tc := range []struct {
		name   string
		commit func(ctx context.Context, message string) error
	}{
		{"Commit", store.Commit},
		{"commitBeforePull", store.commitBeforePull},
		{"CommitMergeResolution", store.CommitMergeResolution},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := testContext(t)
			defer cancel()
			a, b, c := fmt.Sprintf("ec%d-a", i), fmt.Sprintf("ec%d-b", i), fmt.Sprintf("ec%d-c", i)
			for _, id := range []string{a, b, c} {
				createPerm(t, ctx, store, id)
			}
			addDependency(t, ctx, store, c, b, types.DepBlocks)
			if err := store.AddLabel(ctx, b, "closure", "tester"); err != nil {
				t.Fatalf("label %s: %v", b, err)
			}
			if _, err := store.AddIssueComment(ctx, b, "tester", "closure comment"); err != nil {
				t.Fatalf("comment on %s: %v", b, err)
			}
			requireCleanTables(ctx, t, store, "issues", "dependencies", "labels", "comments")
			// A pending issues edit, such as the blocked-state recompute after
			// a pull leaves: the explicit commit's dolt_status read names
			// issues and no other table of the closure.
			if _, err := store.db.ExecContext(ctx, "UPDATE issues SET title = 'pending edit' WHERE id = ?", a); err != nil {
				t.Fatalf("pending issues edit on %s: %v", a, err)
			}

			msg := "explicit commit " + tc.name
			label := t.Name() + "/explicit-commit"
			hold := gate.Arm(label)
			defer hold.Release()
			done := make(chan error, 1)
			go func() { done <- tc.commit(sqlgate.WithLabel(ctx, label), msg) }()
			select {
			case <-hold.Reached():
			case err := <-done:
				t.Fatalf("the explicit commit finished before its DOLT_ADD: %v", err)
			case <-time.After(30 * time.Second):
				t.Fatal("the explicit commit never reached its DOLT_ADD")
			}
			if kind := gate.CommitKind(label); kind != "CALL DOLT_ADD" {
				t.Fatalf("the explicit commit was held at %q, want its CALL DOLT_ADD", kind)
			}

			holdInPublicationWindow(ctx, t, gate, func(ctx context.Context) error { return store.DeleteIssue(ctx, b) }, func() {
				hold.Release()
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("the explicit commit: %v", err)
					}
				case <-time.After(60 * time.Second):
					t.Fatal("the explicit commit did not finish after its release")
				}
			})
			requireCleanTables(ctx, t, store, "issues", "dependencies", "labels", "comments")

			log := readDoltLog(ctx, t, store)
			at := doltLogIndex(log, func(m string) bool { return m == msg })
			if at < 0 {
				t.Fatalf("dolt_log has no %q commit; log: %v", msg, log)
			}
			const asOf = " AS OF '%s'"
			if n := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM issues"+asOf+" WHERE id = ?", log[at].hash, b); n != 0 {
				t.Fatalf("the explicit commit %s still holds %s: it did not stage the delete that committed in its window", log[at].hash, b)
			}
			for _, entry := range log[:at+1] {
				issues := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM issues"+asOf+" WHERE id = ?", entry.hash, b)
				deps := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM dependencies"+asOf+" WHERE depends_on_issue_id = ?", entry.hash, b)
				labels := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM labels"+asOf+" WHERE issue_id = ?", entry.hash, b)
				comments := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM comments"+asOf+" WHERE issue_id = ?", entry.hash, b)
				if issues == 0 && deps+labels+comments > 0 {
					t.Errorf("published commit %s %q has no %s issues row but %d dependencies, %d labels and %d comments rows naming it",
						entry.hash, entry.message, b, deps, labels, comments)
				}
			}
		})
	}
}

// TestIssueClosure_PeerCommitInCreateWindowPublishesTheCreateWhole is the
// mirror case: a write that adds an issues row with a child row (a create
// with a label, a promotion of a labeled wisp) is held between its SQL COMMIT
// and its Dolt publication while a label write on an unrelated issue
// publishes. Without the issue closure the label write's commit took x's
// labels row but not x's issues row.
func TestIssueClosure_PeerCommitInCreateWindowPublishesTheCreateWhole(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	gate := raceGatedStore(t, store)

	writes := []heldWrite{
		{"CreateIssue", func(_ context.Context, _ *testing.T, id string) func(context.Context) error {
			return func(ctx context.Context) error {
				return store.CreateIssue(ctx, &types.Issue{
					ID: id, Title: "closure " + id, Status: types.StatusOpen, Priority: 2,
					IssueType: types.TypeTask, Labels: []string{"closure"},
				}, "tester")
			}
		}},
		{"PromoteFromEphemeral", func(ctx context.Context, t *testing.T, id string) func(context.Context) error {
			createWisp(t, ctx, store, id)
			if err := store.AddLabel(ctx, id, "closure", "tester"); err != nil {
				t.Fatalf("label wisp %s: %v", id, err)
			}
			return func(ctx context.Context) error { return store.PromoteFromEphemeral(ctx, id, "tester") }
		}},
	}
	for i, tc := range writes {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := testContext(t)
			defer cancel()
			x, y := fmt.Sprintf("pc%d-x", i), fmt.Sprintf("pc%d-y", i)
			createPerm(t, ctx, store, y)
			write := tc.prepare(ctx, t, x)

			holdInPublicationWindow(ctx, t, gate, write, func() {
				if err := store.AddLabel(ctx, y, "peer", "tester"); err != nil {
					t.Fatalf("label %s inside the window: %v", y, err)
				}
			})

			log := readDoltLog(ctx, t, store)
			peerMsg := "bd: label add " + y
			peerAt := doltLogIndex(log, func(m string) bool { return m == peerMsg })
			if peerAt < 0 {
				t.Fatalf("dolt_log has no %q commit; log: %v", peerMsg, log)
			}
			const asOf = " AS OF '%s'"
			if n := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM labels"+asOf+" WHERE issue_id = ?", log[peerAt].hash, x); n == 0 {
				t.Fatalf("the peer's commit %s has no labels row naming %s: it did not publish inside the write's window", log[peerAt].hash, x)
			}
			for _, entry := range log[:peerAt+1] {
				issues := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM issues"+asOf+" WHERE id = ?", entry.hash, x)
				labels := countAsOf(ctx, t, store, "SELECT COUNT(*) FROM labels"+asOf+" WHERE issue_id = ?", entry.hash, x)
				if issues == 0 && labels > 0 {
					t.Errorf("published commit %s %q has %d labels rows naming %s and no issues row", entry.hash, entry.message, labels, x)
				}
			}
		})
	}
}
