package dolt

import (
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// TestPullFromSettlesFKCascadeViolations is the end-to-end regression test for
// bd-578h9.3: server-mode PullFrom used to run a bare DOLT_PULL with none of
// the settle machinery the default-remote pull funnels through. The
// delete-vs-insert FK divergence (bd-6dnrw.4) therefore rolled the peer merge
// back with nothing in dolt_conflicts and could never converge on retry.
// PullFrom's SQL route now goes through pullWithAutoResolve, whose forced
// transaction + tryRepairFKCascadeViolations applies the cascade semantics by
// hand. The pull also exercises the remote-parameterized GH#3144 fallback: the
// peer remote is added without branch tracking, so DOLT_PULL('peer') fails
// with the tracking error and the fallback must fetch from the PEER, not from
// the store's (empty) default remote.
//
// The peer lives entirely inside the test Dolt server (a container in CI, so
// host-side file remotes are invisible to it): the local store pushes its
// history to a server-local file:// remote, a server-side DOLT_CLONE turns
// that into a sibling peer database, and the peer's child-row insert is pushed
// back before the local PullFrom merges the divergence.
func TestPullFromSettlesFKCascadeViolations(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	// Seed the issue the peer will reference and commit it.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('fkpeer-x', 'fkpeer-x', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	// Stand up the peer inside the server: push to a server-local file remote
	// and clone it into a sibling database (clone-created remotes carry branch
	// tracking, so the peer can push back with a plain DOLT_PUSH).
	peerDB := uniqueTestDBName(t)
	remoteURL := "file:///tmp/" + peerDB + "_remote"
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: "peer", RemoteURL: remoteURL}); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("push to peer remote: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB); err != nil {
		t.Fatalf("clone peer database: %v", err)
	}

	// Peer (clone B): insert a child row referencing the issue and push. A
	// dedicated single-connection pool keeps the peer's session database from
	// leaking into the store's pool.
	peerConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: peerDB,
	}.String())
	if err != nil {
		t.Fatalf("open peer connection: %v", err)
	}
	defer peerConn.Close()
	peerConn.SetMaxOpenConns(1)
	for _, q := range []string{
		"INSERT INTO labels (issue_id, label) VALUES ('fkpeer-x', 'late-label')",
		"CALL DOLT_COMMIT('-Am', 'peer adds label')",
		"CALL DOLT_PUSH('origin', 'main')",
	} {
		if _, err := peerConn.ExecContext(ctx, q); err != nil {
			t.Fatalf("peer %q: %v", q, err)
		}
	}

	// Local (clone A): delete the issue — the FK cascade removes local children.
	if _, err := db.ExecContext(ctx, "DELETE FROM issues WHERE id = 'fkpeer-x'"); err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'delete issue')"); err != nil {
		t.Fatalf("commit delete: %v", err)
	}

	// The peer pull merges delete-vs-insert: an FK violation with nothing in
	// dolt_conflicts. Without the settle wiring this errors and can never
	// converge; with it, the dangling row is repaired and the pull succeeds.
	conflicts, err := store.PullFrom(ctx, "peer")
	if err != nil {
		t.Fatalf("PullFrom should settle the FK cascade violation, got: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no unresolved conflicts, got %d", len(conflicts))
	}

	var orphans int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM labels WHERE issue_id = 'fkpeer-x'").Scan(&orphans); err != nil {
		t.Fatalf("count dangling labels: %v", err)
	}
	if orphans != 0 {
		t.Errorf("peer pull left %d dangling label row(s) referencing the deleted issue", orphans)
	}
	var violations int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(num_violations),0) FROM dolt_constraint_violations").Scan(&violations); err != nil {
		t.Fatalf("read dolt_constraint_violations: %v", err)
	}
	if violations != 0 {
		t.Errorf("%d constraint violations survive the peer pull", violations)
	}
}

// TestPullFromReportsOperatorConflicts covers bd-578h9.15 on the server-mode
// SQL route: a semantic conflict the resolver declines (both clones retitle
// the same issue) rolls the merge transaction back inside settleMergeInTx, so
// peerPullOutcome's post-hoc GetConflicts can never see it — the conflicts
// must arrive captured pre-rollback inside MergeConflictsError and surface
// through PullFrom's conflict-reporting contract. Peer setup mirrors
// TestPullFromSettlesFKCascadeViolations. Both sides' updated_at is pinned to
// the same value so the LWW auto-resolver (GH#4698) declines the conflict as
// ambiguous, leaving it for the operator.
func TestPullFromReportsOperatorConflicts(t *testing.T) {
	store, cleanup := setupConcurrentTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	db := store.db

	if _, err := db.ExecContext(ctx,
		"INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES ('opcpeer-x', 'base', '', '', '', '', 'open', 2, 'task')"); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'seed issue')"); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	peerDB := uniqueTestDBName(t)
	remoteURL := "file:///tmp/" + peerDB + "_remote"
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{Name: "peer", RemoteURL: remoteURL}); err != nil {
		t.Fatalf("add federation peer: %v", err)
	}
	if err := store.PushTo(ctx, "peer"); err != nil {
		t.Fatalf("push to peer remote: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_CLONE(?, ?)", remoteURL, peerDB); err != nil {
		t.Fatalf("clone peer database: %v", err)
	}

	peerConn, err := sql.Open("mysql", doltutil.ServerDSN{
		Host: "127.0.0.1", Port: testServerPort, User: "root", Database: peerDB,
	}.String())
	if err != nil {
		t.Fatalf("open peer connection: %v", err)
	}
	defer peerConn.Close()
	peerConn.SetMaxOpenConns(1)
	for _, q := range []string{
		"UPDATE issues SET title = 'theirs', updated_at = '2026-01-01 00:00:00' WHERE id = 'opcpeer-x'",
		"CALL DOLT_COMMIT('-Am', 'peer retitles')",
		"CALL DOLT_PUSH('origin', 'main')",
	} {
		if _, err := peerConn.ExecContext(ctx, q); err != nil {
			t.Fatalf("peer %q: %v", q, err)
		}
	}

	// Pin updated_at to the same value as the peer's update so the LWW
	// safety check (issuesConflictsAreLWWSafe) rejects it as ambiguous —
	// this test exercises the operator-conflict reporting path, which
	// requires a conflict the auto-resolver declines (GH#4698).
	if _, err := db.ExecContext(ctx, "UPDATE issues SET title = 'ours', updated_at = '2026-01-01 00:00:00' WHERE id = 'opcpeer-x'"); err != nil {
		t.Fatalf("retitle locally: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'local retitles')"); err != nil {
		t.Fatalf("commit local retitle: %v", err)
	}

	conflicts, err := store.PullFrom(ctx, "peer")
	if err != nil {
		t.Fatalf("PullFrom should report operator conflicts as data, got error: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatal("PullFrom returned no conflicts for a semantic issues-table conflict")
	}
	foundIssues := false
	for _, c := range conflicts {
		if c.Field == "issues" {
			foundIssues = true
		}
	}
	if !foundIssues {
		t.Errorf("reported conflicts %+v do not name the issues table", conflicts)
	}

	// The merge must have been rolled back: no live conflicts, local value intact.
	var liveConflicts int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_conflicts").Scan(&liveConflicts); err != nil {
		t.Fatalf("count live conflicts: %v", err)
	}
	if liveConflicts != 0 {
		t.Errorf("%d conflicted table(s) remain after the rollback", liveConflicts)
	}
	var title string
	if err := db.QueryRowContext(ctx,
		"SELECT title FROM issues WHERE id = 'opcpeer-x'").Scan(&title); err != nil {
		t.Fatalf("read title: %v", err)
	}
	if title != "ours" {
		t.Errorf("local title = %q after refused pull, want %q", title, "ours")
	}
}
