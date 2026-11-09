//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
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
