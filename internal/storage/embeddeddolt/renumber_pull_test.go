//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/depid"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/rowid"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// buildOfflineChildCollision builds the GH#4796 report fixture in one
// embedded database, using a real file:// remote: the branch holds the local
// clone's leg (parent counter 70, children bd-rnp.71/.72/.73), while
// remotes/origin/main holds the other clone's leg (bd-rnp.71/.72, distinct
// content). A single database can build it because pushing a leg and then
// rewinding the branch to the other leg leaves exactly the diverged shape a
// failed pull sees — no second engine is needed (the remote_sync_gate_test
// idiom).
//
// The fixture's OpenSQL handle is released on return: embedded engine handles
// serialize (a second open blocks until the first is cleaned up), so the
// store-under-test cannot open its own until this one is gone.
//
// Returns the pinned heads: local (branch), remote (tracking ref), and the
// common ancestor C0.
func buildOfflineChildCollision(t *testing.T, beadsDir string) (localHead, remoteHead, base string) {
	t.Helper()
	ctx := t.Context()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open (create): %v", err)
	}
	store.Close()

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	// A plain defer, not t.Cleanup: the handle must be gone when this helper
	// returns, and t.Fatalf's Goexit still runs it. The branch is already back
	// on the local leg and the throwaway database dies with the test, so the
	// peer-branch cleanup the earlier draft carried here served nothing.
	defer func() { _ = cleanup() }()

	mustExec := func(stage, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	head := func() string {
		t.Helper()
		var hash string
		if err := db.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log LIMIT 1").Scan(&hash); err != nil {
			t.Fatalf("read head hash: %v", err)
		}
		return hash
	}
	seedChild := func(id, title string) {
		t.Helper()
		mustExec("seed "+id, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title)
	}

	// C0: the shared ancestor — parent with counter 70 and no children.
	mustExec("seed parent", "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES ('bd-rnp', 'parent epic', '', '', '', '', 'open', 2, 'epic')")
	mustExec("seed counter 70", "INSERT INTO child_counters (parent_id, last_child) VALUES ('bd-rnp', 70)")
	mustExec("commit C0", "CALL DOLT_COMMIT('-Am', 'test: seed ancestor counter 70 (C0)')")
	c0 := head()
	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
	mustExec("push C0", "CALL DOLT_PUSH('origin', 'main')")

	// The local clone's leg: children 71-73, counter 73.
	seedChild("bd-rnp.71", "local child 71")
	seedChild("bd-rnp.72", "local child 72")
	seedChild("bd-rnp.73", "local child 73")
	mustExec("local counter 73", "UPDATE child_counters SET last_child = 73 WHERE parent_id = 'bd-rnp'")
	mustExec("commit local leg", "CALL DOLT_COMMIT('-Am', 'test: local children 71-73')")
	localHead = head()

	// The other clone's leg: children 71-72 with distinct content, counter
	// 72, published.
	mustExec("rewind to C0", "CALL DOLT_RESET('--hard', ?)", c0)
	seedChild("bd-rnp.71", "remote child 71")
	seedChild("bd-rnp.72", "remote child 72")
	mustExec("remote counter 72", "UPDATE child_counters SET last_child = 72 WHERE parent_id = 'bd-rnp'")
	mustExec("commit remote leg", "CALL DOLT_COMMIT('-Am', 'test: remote children 71-72')")
	remoteHead = head()
	mustExec("push remote leg", "CALL DOLT_PUSH('origin', 'main')")

	// Back on the local leg: the branch diverges from remotes/origin/main,
	// with C0 the common ancestor.
	mustExec("rewind to local leg", "CALL DOLT_RESET('--hard', ?)", localHead)
	return localHead, remoteHead, c0
}

// TestEmbeddedDoltPlainPullReportsRenumberPlan (GH#4796, bd-28sb25 AC-2): the
// report fixture — base counter 70, remote bd-rnp.71/.72, distinct local
// bd-rnp.71/.72/.73 — under a plain pull. The pull fails as it always did
// (the issues add/add declines; the both-changed counter settles at max per
// AC-1), the read-only plan proposes exactly bd-rnp.71 -> bd-rnp.74 and
// bd-rnp.72 -> bd-rnp.75 (above both sides' high-water marks, local P.73
// untouched), and nothing is renamed: no id, no counter, no commit moves.
func TestEmbeddedDoltPlainPullReportsRenumberPlan(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	localHead, remoteHead, base := buildOfflineChildCollision(t, beadsDir)

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	pullErr := store.Pull(ctx)
	if pullErr == nil {
		t.Fatal("plain pull of the offline-collision fixture succeeded; want the issues add/add decline")
	}
	var mce *versioncontrolops.MergeConflictsError
	if !errors.As(pullErr, &mce) {
		t.Fatalf("pull error = %v, want *versioncontrolops.MergeConflictsError", pullErr)
	}

	// The best-effort plan: exactly the report's mappings, above both
	// high-water marks, with the pinned heads it was computed from.
	plan, err := store.PlanChildRenumbers(ctx, "")
	if err != nil {
		t.Fatalf("PlanChildRenumbers: %v", err)
	}
	want := []storage.ChildRenumberMapping{
		{OldID: "bd-rnp.71", NewID: "bd-rnp.74", Root: true},
		{OldID: "bd-rnp.72", NewID: "bd-rnp.75", Root: true},
	}
	if len(plan.Mappings) != len(want) {
		t.Fatalf("plan mappings = %+v, want %+v", plan.Mappings, want)
	}
	for i, m := range want {
		if plan.Mappings[i] != m {
			t.Errorf("plan mapping %d = %+v, want %+v", i, plan.Mappings[i], m)
		}
	}
	if plan.CounterFloors["bd-rnp"] != 75 {
		t.Errorf("counter floor = %d, want 75", plan.CounterFloors["bd-rnp"])
	}
	if plan.LocalHead != localHead || plan.RemoteHead != remoteHead {
		t.Errorf("plan heads = (%s, %s), want (%s, %s)", plan.LocalHead, plan.RemoteHead, localHead, remoteHead)
	}
	if plan.Base != base {
		t.Errorf("plan base = %s, want %s", plan.Base, base)
	}
	if plan.Remote != "origin" || plan.RemoteRef != "remotes/origin/main" {
		t.Errorf("plan remote = %q ref = %q, want origin remotes/origin/main", plan.Remote, plan.RemoteRef)
	}

	// No identity changed and nothing committed: the plan is read-only, and
	// the failed pull left the working set as it found it.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	defer func() { _ = cleanup() }()

	var ids []string
	rows, err := db.QueryContext(ctx, "SELECT id FROM issues WHERE id LIKE 'bd-rnp.%' ORDER BY id")
	if err != nil {
		t.Fatalf("read local children: %v", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan child id: %v", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read local children: %v", err)
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	wantIDs := []string{"bd-rnp.71", "bd-rnp.72", "bd-rnp.73"}
	if len(sorted) != 3 || sorted[0] != wantIDs[0] || sorted[1] != wantIDs[1] || sorted[2] != wantIDs[2] {
		t.Errorf("local children after planning = %v, want %v (no renames applied)", sorted, wantIDs)
	}

	var last int
	if err := db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = 'bd-rnp'").Scan(&last); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if last != 73 {
		t.Errorf("counter after planning = %d, want 73 (no floor raised)", last)
	}

	var headHash string
	if err := db.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log LIMIT 1").Scan(&headHash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			t.Fatal("dolt_log empty")
		}
		t.Fatalf("read head: %v", err)
	}
	if headHash != localHead {
		t.Errorf("head after planning = %s, want %s (no rename commit)", headHash, localHead)
	}
}

// TestEmbeddedDoltPullRenumberChildren (GH#4796, bd-28sb25 AC-3): the report
// fixture under `bd dolt pull --renumber-children`. The colliding local
// children bd-rnp.71/.72 are renamed to bd-rnp.74/.75 — above both clones'
// high-water marks — in one commit, and the pinned remote revision then
// merges clean: the remote keeps bd-rnp.71/.72, the local children survive
// as .74/.75 plus the untouched .73, and the counter settles at the raised
// floor 75, so the next local create cannot re-mint a colliding id.
func TestEmbeddedDoltPullRenumberChildren(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	localHead, _, _ := buildOfflineChildCollision(t, beadsDir)

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	result, err := store.PullWithChildRenumbers(ctx, "")
	if err != nil {
		t.Fatalf("PullWithChildRenumbers: %v", err)
	}
	if result == nil {
		t.Fatal("PullWithChildRenumbers returned a nil result on success")
	}
	if result.Retained {
		t.Errorf("result.Retained = true; the first invocation has no earlier repair to retain")
	}
	if result.RenameCommit == "" {
		t.Fatal("result.RenameCommit is empty; the renames were not committed")
	}
	if result.RenameCommit == localHead {
		t.Errorf("result.RenameCommit = localHead %s; no rename commit was created", localHead)
	}
	want := []storage.ChildRenumberMapping{
		{OldID: "bd-rnp.71", NewID: "bd-rnp.74", Root: true},
		{OldID: "bd-rnp.72", NewID: "bd-rnp.75", Root: true},
	}
	if result.Plan == nil || len(result.Plan.Mappings) != len(want) {
		t.Fatalf("result plan mappings = %+v, want %+v", result.Plan, want)
	}
	for i, m := range want {
		if result.Plan.Mappings[i] != m {
			t.Errorf("result mapping %d = %+v, want %+v", i, result.Plan.Mappings[i], m)
		}
	}

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	defer func() { _ = cleanup() }()

	// The merged database: the remote's .71/.72, the renamed .74/.75, and the
	// untouched .73, each carrying its own side's content.
	for id, title := range map[string]string{
		"bd-rnp.71": "remote child 71",
		"bd-rnp.72": "remote child 72",
		"bd-rnp.73": "local child 73",
		"bd-rnp.74": "local child 71",
		"bd-rnp.75": "local child 72",
	} {
		var got string
		if err := db.QueryRowContext(ctx,
			"SELECT title FROM issues WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("read %s after repair: %v", id, err)
		}
		if got != title {
			t.Errorf("title of %s = %q, want %q", id, got, title)
		}
	}
	var children int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id LIKE 'bd-rnp.%'").Scan(&children); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if children != 5 {
		t.Errorf("children after repair = %d, want 5", children)
	}

	var last int
	if err := db.QueryRowContext(ctx,
		"SELECT last_child FROM child_counters WHERE parent_id = 'bd-rnp'").Scan(&last); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	if last != 75 {
		t.Errorf("counter after repair = %d, want the raised floor 75", last)
	}

	var conflicts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("count dolt_conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("dolt_conflicts after repair = %d, want 0", conflicts)
	}

	var head string
	if err := db.QueryRowContext(ctx,
		"SELECT hash FROM dolt_branches WHERE name = 'main'").Scan(&head); err != nil {
		t.Fatalf("read branch head: %v", err)
	}
	if head == localHead || head == result.RenameCommit {
		t.Errorf("branch head = %s; the merge of the remote revision never advanced past the local leg (%s) or the rename commit (%s)",
			head, localHead, result.RenameCommit)
	}
}

// subtreeCommentID computes the content-derived id the bd-ri8bd derivation
// mints for the fixture's identical comment under issueID at the given
// ordinal — the same id both clones mint independently for the same content,
// which is exactly the convergence the rename-time rekey must not break.
func subtreeCommentID(issueID string, ordinal int) string {
	return rowid.New("comments", ordinal, rowid.Digest([]sql.NullString{
		{String: issueID, Valid: true},
		{String: "tester", Valid: true},
		{String: "identical comment", Valid: true},
		{String: "2026-01-01 00:00:00", Valid: true},
	}))
}

// buildOfflineChildSubtreeCollision builds the AC-4 fixture: the offline
// divergence of buildOfflineChildCollision, one level deeper. Both clones
// create children under the shared parent bd-rn4, and both sides' colliding
// bd-rn4.71 roots carry their own descendants, dependency edges, comments,
// and counter rows. The local clone additionally holds bd-rn4.72 with a
// counter high-water of 1 and NO live children — a parent whose next create
// must still mint suffix 2 after it is renamed.
//
// Returns the local leg's pinned head.
func buildOfflineChildSubtreeCollision(t *testing.T, beadsDir string) (localHead string) {
	t.Helper()
	ctx := t.Context()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open (create): %v", err)
	}
	store.Close()

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	// Plain defer, not t.Cleanup: the handle must be gone when this helper
	// returns so the store-under-test can open its own (embedded engine
	// handles serialize).
	defer func() { _ = cleanup() }()

	mustExec := func(stage, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	head := func() string {
		t.Helper()
		var hash string
		if err := db.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log LIMIT 1").Scan(&hash); err != nil {
			t.Fatalf("read head hash: %v", err)
		}
		return hash
	}
	seedChild := func(id, title string) {
		t.Helper()
		mustExec("seed "+id, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title)
	}
	seedCounter := func(parent string, last int) {
		t.Helper()
		mustExec("seed counter "+parent, "INSERT INTO child_counters (parent_id, last_child) VALUES (?, ?)", parent, last)
	}
	seedDep := func(source, target string) {
		t.Helper()
		mustExec("seed dep "+source+" -> "+target,
			"INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) "+
				"VALUES (?, ?, ?, 'blocks', '2026-01-01 00:00:00', 'fixture')",
			depid.New(source, target), source, target)
	}
	seedComment := func(id string) {
		t.Helper()
		mustExec("seed comment "+id,
			"INSERT INTO comments (id, issue_id, author, text, created_at) "+
				"VALUES (?, ?, 'tester', 'identical comment', '2026-01-01 00:00:00')", id, "bd-rn4.71")
	}

	// C0: the shared ancestor — parent with counter 70 and no children.
	mustExec("seed parent", "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES ('bd-rn4', 'parent epic', '', '', '', '', 'open', 2, 'epic')")
	seedCounter("bd-rn4", 70)
	mustExec("commit C0", "CALL DOLT_COMMIT('-Am', 'test: seed ancestor counter 70 (C0)')")
	c0 := head()
	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
	mustExec("push C0", "CALL DOLT_PUSH('origin', 'main')")

	// The local clone's leg: .71 with child .71.1 and two identical comments,
	// .72 with a high-water counter and no live children, and .73, with
	// dependency edges both directions between .71 and .73.
	seedChild("bd-rn4.71", "local 71")
	seedChild("bd-rn4.71.1", "local 71.1")
	seedChild("bd-rn4.72", "local 72")
	seedChild("bd-rn4.73", "local 73")
	seedCounter("bd-rn4.71", 1)
	seedCounter("bd-rn4.72", 1)
	mustExec("local parent counter", "UPDATE child_counters SET last_child = 73 WHERE parent_id = 'bd-rn4'")
	seedDep("bd-rn4.71", "bd-rn4.73")
	seedDep("bd-rn4.73", "bd-rn4.71")
	seedComment(subtreeCommentID("bd-rn4.71", 0))
	seedComment(subtreeCommentID("bd-rn4.71", 1))
	mustExec("commit local leg", "CALL DOLT_COMMIT('-Am', 'test: local children 71-73 with subtree')")
	localHead = head()

	// The other clone's leg: .71 with its own child .71.1 and the SAME
	// identical comment (same content, same derived id, under its own .71),
	// plus .72. Counter 72.
	mustExec("rewind to C0", "CALL DOLT_RESET('--hard', ?)", c0)
	seedChild("bd-rn4.71", "remote 71")
	seedChild("bd-rn4.71.1", "remote 71.1")
	seedChild("bd-rn4.72", "remote 72")
	seedCounter("bd-rn4.71", 1)
	mustExec("remote parent counter", "UPDATE child_counters SET last_child = 72 WHERE parent_id = 'bd-rn4'")
	seedComment(subtreeCommentID("bd-rn4.71", 0))
	mustExec("commit remote leg", "CALL DOLT_COMMIT('-Am', 'test: remote children 71-72 with subtree')")
	mustExec("push remote leg", "CALL DOLT_PUSH('origin', 'main')")

	// Back on the local leg: the branch diverges from remotes/origin/main.
	mustExec("rewind to local leg", "CALL DOLT_RESET('--hard', ?)", localHead)
	return localHead
}

// TestEmbeddedDoltPullRenumberChildrenSubtree (GH#4796, bd-28sb25 AC-4): the
// repair carries a colliding subtree whole. The local .71 (with child .71.1,
// both-direction dependency edges to .73, and two identical comments) maps to
// .74 with its descendant at .74.1; .72 — a counter high-water of 1 with no
// live children — maps to .75. After the merge the remote keeps .71/.71.1/.72
// while the local content lives under .74/.74.1/.75: every edge followed its
// issue, the counter rows moved with their values preserved (the next create
// under .74 or .75 mints suffix 2), the comments that followed the rename got
// ids re-derived from the new issue id while the remote's identical comment
// kept its own, and a second sync needs no repair at all.
func TestEmbeddedDoltPullRenumberChildrenSubtree(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	localHead := buildOfflineChildSubtreeCollision(t, beadsDir)

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	result, err := store.PullWithChildRenumbers(ctx, "")
	if err != nil {
		t.Fatalf("PullWithChildRenumbers: %v", err)
	}
	if result == nil {
		t.Fatal("PullWithChildRenumbers returned a nil result on success")
	}
	if result.RenameCommit == "" {
		t.Fatal("result.RenameCommit is empty; the renames were not committed")
	}
	if result.RenameCommit == localHead {
		t.Errorf("result.RenameCommit = localHead %s; no rename commit was created", localHead)
	}
	want := []storage.ChildRenumberMapping{
		{OldID: "bd-rn4.71", NewID: "bd-rn4.74", Root: true},
		{OldID: "bd-rn4.71.1", NewID: "bd-rn4.74.1"},
		{OldID: "bd-rn4.72", NewID: "bd-rn4.75", Root: true},
	}
	if result.Plan == nil || len(result.Plan.Mappings) != len(want) {
		t.Fatalf("result plan mappings = %+v, want %+v", result.Plan, want)
	}
	for i, m := range want {
		if result.Plan.Mappings[i] != m {
			t.Errorf("result mapping %d = %+v, want %+v", i, result.Plan.Mappings[i], m)
		}
	}

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	// Released explicitly below, before the store-level sync checks: the
	// embedded engine serializes open handles, and the store must be free to
	// act once the assertions are done.
	releaseVerify := func() { _ = cleanup() }

	// The merged database keeps both sides' identities: the remote's
	// .71/.71.1/.72 alongside the renamed .74/.74.1/.75 and the untouched
	// .73, each carrying its own side's content.
	for id, title := range map[string]string{
		"bd-rn4.71":   "remote 71",
		"bd-rn4.71.1": "remote 71.1",
		"bd-rn4.72":   "remote 72",
		"bd-rn4.73":   "local 73",
		"bd-rn4.74":   "local 71",
		"bd-rn4.74.1": "local 71.1",
		"bd-rn4.75":   "local 72",
	} {
		var got string
		if err := db.QueryRowContext(ctx,
			"SELECT title FROM issues WHERE id = ?", id).Scan(&got); err != nil {
			t.Errorf("read %s after repair: %v", id, err)
		} else if got != title {
			t.Errorf("title of %s = %q, want %q", id, got, title)
		}
	}
	var children int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE id LIKE 'bd-rn4.%'").Scan(&children); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if children != 7 {
		t.Errorf("children after repair = %d, want 7", children)
	}

	// The dependency edges followed the identities they name: .71's outbound
	// edge now belongs to .74, and .71's inbound edge now points at .74, both
	// re-keyed to the deterministic depid of their new endpoints.
	type depRow struct {
		id, source, target string
	}
	var deps []depRow
	rows, err := db.QueryContext(ctx,
		"SELECT id, issue_id, depends_on_issue_id FROM dependencies")
	if err != nil {
		t.Fatalf("read dependencies: %v", err)
	}
	for rows.Next() {
		var d depRow
		if err := rows.Scan(&d.id, &d.source, &d.target); err != nil {
			rows.Close()
			t.Fatalf("scan dependency: %v", err)
		}
		deps = append(deps, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read dependencies: %v", err)
	}
	wantDeps := map[string]string{
		"bd-rn4.74": "bd-rn4.73",
		"bd-rn4.73": "bd-rn4.74",
	}
	if len(deps) != len(wantDeps) {
		t.Errorf("dependency rows = %+v, want the two re-keyed edges %+v", deps, wantDeps)
	}
	for _, d := range deps {
		target, ok := wantDeps[d.source]
		if !ok {
			t.Errorf("unexpected dependency %s -> %s after repair", d.source, d.target)
			continue
		}
		if d.target != target {
			t.Errorf("dependency %s -> %s, want target %s", d.source, d.target, target)
		}
		if wantID := depid.New(d.source, d.target); d.id != wantID {
			t.Errorf("dependency id %s for %s -> %s, want the derived %s", d.id, d.source, d.target, wantID)
		}
	}

	// The comments: the two that followed .71 to .74 carry ids re-derived
	// from the NEW issue id (multiplicity preserved — ordinals 0 and 1),
	// while the remote's identical comment under .71 kept its own id. All
	// three coexist; nothing was silently dropped by a shared primary key.
	for issueID, wantIDs := range map[string][]string{
		"bd-rn4.74": {subtreeCommentID("bd-rn4.74", 0), subtreeCommentID("bd-rn4.74", 1)},
		"bd-rn4.71": {subtreeCommentID("bd-rn4.71", 0)},
	} {
		var gotIDs []string
		rows, err := db.QueryContext(ctx,
			"SELECT id FROM comments WHERE issue_id = ? ORDER BY id", issueID)
		if err != nil {
			t.Fatalf("read comments for %s: %v", issueID, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				t.Fatalf("scan comment id: %v", err)
			}
			gotIDs = append(gotIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("read comments for %s: %v", issueID, err)
		}
		if len(gotIDs) != len(wantIDs) {
			t.Errorf("comments under %s = %v, want %v", issueID, gotIDs, wantIDs)
			continue
		}
		sort.Strings(gotIDs)
		sortedWant := append([]string(nil), wantIDs...)
		sort.Strings(sortedWant)
		for i := range sortedWant {
			if gotIDs[i] != sortedWant[i] {
				t.Errorf("comment %d under %s = %s, want %s", i, issueID, gotIDs[i], sortedWant[i])
			}
		}
	}
	var comments int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM comments").Scan(&comments); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if comments != 3 {
		t.Errorf("comments after repair = %d, want 3 (two re-keyed under .74, one remote under .71)", comments)
	}

	// The counter rows followed the renamed parents with their high-water
	// values preserved: .74 and .75 each hold 1 (the remote's .71 counter row
	// arrived by merge alongside them), the parent settled at the raised
	// floor 75, and nothing remains under a renamed-away id.
	for parent, want := range map[string]int{
		"bd-rn4":    75,
		"bd-rn4.71": 1,
		"bd-rn4.74": 1,
		"bd-rn4.75": 1,
	} {
		var got int
		if err := db.QueryRowContext(ctx,
			"SELECT last_child FROM child_counters WHERE parent_id = ?", parent).Scan(&got); err != nil {
			t.Fatalf("read counter for %s: %v", parent, err)
		}
		if got != want {
			t.Errorf("counter for %s = %d, want %d", parent, got, want)
		}
	}
	for _, parent := range []string{"bd-rn4.72"} {
		var stale int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM child_counters WHERE parent_id = ?", parent).Scan(&stale); err != nil {
			t.Fatalf("check counter for %s: %v", parent, err)
		}
		if stale != 0 {
			t.Errorf("%d counter row(s) remain for the renamed-away %s", stale, parent)
		}
	}

	// The preserved high-water decides the next create: a parent whose
	// counter moved with value 1 — even .75, which has no live children —
	// mints suffix 2, never re-minting a colliding suffix.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin next-id tx: %v", err)
	}
	for _, parent := range []string{"bd-rn4.74", "bd-rn4.75"} {
		next, err := issueops.GetNextChildIDTx(ctx, tx, parent)
		if err != nil {
			t.Fatalf("GetNextChildIDTx(%s): %v", parent, err)
		}
		if want := parent + ".2"; next != want {
			t.Errorf("next child of %s = %s, want %s (high-water preserved)", parent, next, want)
		}
	}
	// The mint writes the counter; roll it back so the working set stays
	// clean for the subsequent-sync checks.
	_ = tx.Rollback()

	var conflicts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_conflicts").Scan(&conflicts); err != nil {
		t.Fatalf("count dolt_conflicts: %v", err)
	}
	if conflicts != 0 {
		t.Errorf("dolt_conflicts after repair = %d, want 0", conflicts)
	}
	releaseVerify()

	// A subsequent sync is clean: the two clones converged, so a plain pull
	// succeeds and a second repair finds nothing to rename.
	if err := store.Pull(ctx); err != nil {
		t.Fatalf("second plain pull: %v", err)
	}
	again, err := store.PullWithChildRenumbers(ctx, "")
	if err != nil {
		t.Fatalf("second PullWithChildRenumbers: %v", err)
	}
	if again != nil {
		t.Errorf("second PullWithChildRenumbers proposed %+v; the clones converged and need no repair", again)
	}
}

// buildOfflineChildResidualConflict builds the AC-6 fixture: the report
// divergence of buildOfflineChildCollision plus a conflict the renumber
// repair does not touch — a shared issue bd-rn6r whose title both legs
// change differently, so the merge fails even after the colliding children
// are renamed away. The repair's renames commit; its merge does not.
//
// Returns the pinned heads: local (branch) and remote (tracking ref).
func buildOfflineChildResidualConflict(t *testing.T, beadsDir string) (localHead, remoteHead string) {
	t.Helper()
	ctx := t.Context()
	dataDir := filepath.Join(beadsDir, "embeddeddolt")

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open (create): %v", err)
	}
	store.Close()

	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()

	mustExec := func(stage, q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	head := func() string {
		t.Helper()
		var hash string
		if err := db.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log LIMIT 1").Scan(&hash); err != nil {
			t.Fatalf("read head hash: %v", err)
		}
		return hash
	}
	seedChild := func(id, title string) {
		t.Helper()
		mustExec("seed "+id, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, title)
	}

	// C0: the shared ancestor — parent with counter 70, no children, and the
	// issue both legs will modify.
	mustExec("seed parent", "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES ('bd-rn6r', 'parent epic', '', '', '', '', 'open', 2, 'epic')")
	mustExec("seed solo", "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
		"VALUES ('bd-rn6r-solo', 'solo base', '', '', '', '', 'open', 2, 'task')")
	mustExec("seed counter 70", "INSERT INTO child_counters (parent_id, last_child) VALUES ('bd-rn6r', 70)")
	mustExec("commit C0", "CALL DOLT_COMMIT('-Am', 'test: seed ancestor and solo issue (C0)')")
	c0 := head()
	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(t.TempDir(), "remote"))
	mustExec("push C0", "CALL DOLT_PUSH('origin', 'main')")

	// The local clone's leg: children 71-73, counter 73, and its own change
	// to the solo issue.
	seedChild("bd-rn6r.71", "local child 71")
	seedChild("bd-rn6r.72", "local child 72")
	seedChild("bd-rn6r.73", "local child 73")
	mustExec("local counter 73", "UPDATE child_counters SET last_child = 73 WHERE parent_id = 'bd-rn6r'")
	mustExec("local solo", "UPDATE issues SET title = 'solo local' WHERE id = 'bd-rn6r-solo'")
	mustExec("commit local leg", "CALL DOLT_COMMIT('-Am', 'test: local children 71-73 and solo local')")
	localHead = head()

	// The other clone's leg: children 71-72, counter 72, and a DIFFERENT
	// change to the same solo issue.
	mustExec("rewind to C0", "CALL DOLT_RESET('--hard', ?)", c0)
	seedChild("bd-rn6r.71", "remote child 71")
	seedChild("bd-rn6r.72", "remote child 72")
	mustExec("remote counter 72", "UPDATE child_counters SET last_child = 72 WHERE parent_id = 'bd-rn6r'")
	mustExec("remote solo", "UPDATE issues SET title = 'solo remote' WHERE id = 'bd-rn6r-solo'")
	mustExec("commit remote leg", "CALL DOLT_COMMIT('-Am', 'test: remote children 71-72 and solo remote')")
	remoteHead = head()
	mustExec("push remote leg", "CALL DOLT_PUSH('origin', 'main')")

	// Back on the local leg: the branch diverges from remotes/origin/main.
	mustExec("rewind to local leg", "CALL DOLT_RESET('--hard', ?)", localHead)
	return localHead, remoteHead
}

// TestEmbeddedDoltPullRenumberChildrenRetained (GH#4796, bd-28sb25 AC-6): a
// repair whose merge fails after the renames commits the rename once and
// never replays it. The retry finds no collision to rename — the earlier
// invocation already renamed the local children — recognizes the retained
// rename commit by the marker naming the remote revision it pinned, and
// reports the same partial result (Retained, same commit, same mappings)
// with the merge's own outcome. Once the operator resolves the residual
// conflict, the retained repair's merge completes and a further pull reports
// nothing: the repair is spent.
func TestEmbeddedDoltPullRenumberChildrenRetained(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}

	ctx := context.Background()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	localHead, remoteHead := buildOfflineChildResidualConflict(t, beadsDir)

	store, err := embeddeddolt.Open(ctx, beadsDir, "testdb", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// First repair: the renames commit, the merge fails on the solo issue's
	// both-changed title, and the partial result reports what moved.
	result, err := store.PullWithChildRenumbers(ctx, "")
	if err == nil {
		t.Fatal("PullWithChildRenumbers succeeded; want the residual bd-rn6r-solo conflict to fail the merge")
	}
	if result == nil {
		t.Fatal("PullWithChildRenumbers returned no partial result with the merge error")
	}
	if result.Retained {
		t.Error("result.Retained = true on the first invocation; no earlier repair exists")
	}
	firstRename := result.RenameCommit
	if firstRename == "" || firstRename == localHead {
		t.Fatalf("result.RenameCommit = %q; the renames were not committed", firstRename)
	}
	wantMappings := []storage.ChildRenumberMapping{
		{OldID: "bd-rn6r.71", NewID: "bd-rn6r.74", Root: true},
		{OldID: "bd-rn6r.72", NewID: "bd-rn6r.75", Root: true},
	}
	if result.Plan == nil || len(result.Plan.Mappings) != len(wantMappings) {
		t.Fatalf("first result mappings = %+v, want %+v", result.Plan, wantMappings)
	}
	for i, m := range wantMappings {
		if result.Plan.Mappings[i] != m {
			t.Errorf("first mapping %d = %+v, want %+v", i, result.Plan.Mappings[i], m)
		}
	}

	// The rename commit carries the marker naming the pinned remote revision,
	// and the failed merge left it as the branch head with the working set
	// restored.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (verify): %v", err)
	}
	var message string
	found := false
	rows, err := db.QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	for rows.Next() {
		var hash, msg string
		if err := rows.Scan(&hash, &msg); err != nil {
			rows.Close()
			t.Fatalf("scan dolt_log: %v", err)
		}
		if hash == firstRename {
			message, found = msg, true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	if !found {
		t.Fatalf("rename commit %s not in dolt_log", firstRename)
	}
	if !strings.Contains(message, "bd-renumber: remote_head="+remoteHead) {
		t.Errorf("rename commit message lacks the retained-repair marker for %s:\n%s", remoteHead, message)
	}
	var head string
	if err := db.QueryRowContext(ctx,
		"SELECT hash FROM dolt_branches WHERE name = 'main'").Scan(&head); err != nil {
		t.Fatalf("read branch head: %v", err)
	}
	if head != firstRename {
		t.Errorf("branch head after the failed merge = %s, want the rename commit %s (working set restored)", head, firstRename)
	}
	cleanup()

	// Retry: the same unmergeable state reports the retained rename instead
	// of renaming anything again.
	result, err = store.PullWithChildRenumbers(ctx, "")
	if err == nil {
		t.Fatal("retry succeeded; want the residual conflict to fail the merge again")
	}
	if result == nil {
		t.Fatal("retry returned no result; the retained repair was not reported")
	}
	if !result.Retained {
		t.Error("retry result.Retained = false; the rename commit is from an earlier invocation")
	}
	if result.RenameCommit != firstRename {
		t.Errorf("retry RenameCommit = %s, want the retained %s (no second rename commit)", result.RenameCommit, firstRename)
	}
	if result.Plan == nil || len(result.Plan.Mappings) != len(wantMappings) {
		t.Fatalf("retry mappings = %+v, want the replayed %+v", result.Plan, wantMappings)
	}
	for i, m := range wantMappings {
		if result.Plan.Mappings[i] != m {
			t.Errorf("retry mapping %d = %+v, want the replayed %+v", i, result.Plan.Mappings[i], m)
		}
	}

	// The operator resolves the residual conflict by adopting the remote
	// content, and the retained repair's merge completes.
	db, cleanup, err = embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (resolve): %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"UPDATE issues SET title = 'solo remote' WHERE id = 'bd-rn6r-solo'"); err != nil {
		t.Fatalf("adopt remote solo title: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_COMMIT('-Am', 'test: adopt the remote solo title')"); err != nil {
		t.Fatalf("commit the resolution: %v", err)
	}
	cleanup()

	result, err = store.PullWithChildRenumbers(ctx, "")
	if err != nil {
		t.Fatalf("PullWithChildRenumbers after resolving the residual conflict: %v", err)
	}
	if result == nil || !result.Retained {
		t.Fatalf("result = %+v, err = %v; want the retained repair to complete and report itself", result, err)
	}
	if result.RenameCommit != firstRename {
		t.Errorf("completed repair RenameCommit = %s, want the retained %s", result.RenameCommit, firstRename)
	}

	// The merge landed: both sides' children coexist under their own ids and
	// the solo issue carries the adopted title. The repair is spent — a
	// further pull has nothing to rename and nothing to retain.
	db, cleanup, err = embeddeddolt.OpenSQL(ctx, filepath.Join(beadsDir, "embeddeddolt"), "testdb", "main")
	if err != nil {
		t.Fatalf("OpenSQL (final): %v", err)
	}
	for id, title := range map[string]string{
		"bd-rn6r.71":   "remote child 71",
		"bd-rn6r.72":   "remote child 72",
		"bd-rn6r.73":   "local child 73",
		"bd-rn6r.74":   "local child 71",
		"bd-rn6r.75":   "local child 72",
		"bd-rn6r-solo": "solo remote",
	} {
		var got string
		if err := db.QueryRowContext(ctx,
			"SELECT title FROM issues WHERE id = ?", id).Scan(&got); err != nil {
			t.Errorf("read %s after the completed repair: %v", id, err)
		} else if got != title {
			t.Errorf("title of %s = %q, want %q", id, got, title)
		}
	}
	cleanup()

	result, err = store.PullWithChildRenumbers(ctx, "")
	if err != nil {
		t.Fatalf("further pull: %v", err)
	}
	if result != nil {
		t.Errorf("further pull reported %+v; the retained repair is spent and the result must be nil", result)
	}
}
