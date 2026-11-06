//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestEmbeddedMigrate0032DebtFreeCloneRepairsWispFlagsOnly pins the debt-free
// half of the is_blocked repair (gastownhall/beads#7037): a clone whose main
// history already carries migration 0075 and its repair — received by pull,
// so no obligation key sits at HEAD and the tracked plane owes nothing —
// still holds clone-local wisp rows the tracked repair never touched, because
// wisps never replicate. On that clone, ignored 0032's hook must recompute
// the wisp plane alone: stale flags correct, and not one tracked byte moves
// (no issue flag, no stamp, no metadata key, no commit).
//
// The origin seeds the 0059-era over-set at main 74 and publishes it; the
// adopter clones at 74. The origin then migrates — minting and consuming the
// obligation — and pushes; the adopter, still a clean clone, pulls the
// repaired history, then catches its ignored plane up to 31 (the position a
// real clone holds) and plants stale wisp flags in its clone-local working
// set. Only ignored 0032 is pending when it opens, which is exactly the world
// the wisp-only pass exists for.
func TestEmbeddedMigrate0032DebtFreeCloneRepairsWispFlagsOnly(t *testing.T) {
	requireEmbedded(t)
	ctx := t.Context()
	originData := seedMainSchemaAt(t, ctx, 74)
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}
	remoteURL := "file://" + remoteDir

	// Origin: the 0059-era store with its over-set flag, published at main 74.
	withRawConn(t, originData, preflipDatabase, func(conn *sql.Conn) {
		rows := []stampedIssue{
			{id: "wd-blocker", status: "open", stamp: historicalStamp(1)},
			{id: "wd-blocked", status: "open", stamp: historicalStamp(2), wantBlocked: true},
			{id: "wd-related", status: "open", stamp: historicalStamp(3)},
		}
		insertStampedIssues(t, ctx, conn, "issues", rows, true)
		insertIssueEdges(t, ctx, conn, [][3]string{
			{"wd-blocked", "wd-blocker", "blocks"},
			{"wd-related", "wd-blocked", "relates-to"},
		})
		mustExecOn(t, ctx, conn, "UPDATE issues SET is_blocked = 1, updated_at = updated_at WHERE id = 'wd-related'")
		commitFixture(t, ctx, conn, "test: v74 store crossed 0059 on an engine with dolt#11886")
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	_, cloneData := cloneLineage(t, ctx, remoteURL)

	// Origin migrates past 0075 — minting the obligation, running the full
	// recompute, consuming the key — and publishes the repaired history.
	withRawConn(t, originData, preflipDatabase, func(conn *sql.Conn) {
		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (origin): %v", err)
		}
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	// The adopter pulls the repaired history while still a clean clone, then
	// holds the pre-open position of a real store: ignored plane at 31,
	// clone-local wisp rows with 0059-era flags in the working set. Main is
	// current by pull (cursor 75, no key at HEAD), so the only pending
	// migration when it opens is ignored 0032: the debt-free, wisp-only world.
	withRawConn(t, cloneData, preflipDatabase, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_PULL('origin')")
		if got := scalarInt(t, ctx, conn, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations"); got != 75 {
			t.Fatalf("clone main cursor = %d after pull, want 75", got)
		}
		if got := readIsBlocked(t, ctx, conn, "issues", "wd-related"); got {
			t.Fatalf("pull delivered uncorrected flags: wd-related still blocked at origin HEAD")
		}
		if _, err := schema.MigrateIgnoredUpTo(ctx, conn, 31); err != nil {
			t.Fatalf("MigrateIgnoredUpTo: %v", err)
		}
		// w-free has no edges and must not be blocked; seed it over-set, the
		// shape 0059-era wisp recomputes left behind. w-waited blocks on the
		// genuinely blocked wd-blocked and must be blocked; seed it clear.
		mustExecOn(t, ctx, conn, `
INSERT INTO wisps (id, title, status, is_blocked, updated_at) VALUES
  ('w-free', 'no edges', 'open', 1, ?),
  ('w-waited', 'waits on wd-blocked', 'open', 0, ?)
`, historicalStamp(4), historicalStamp(5))
		mustExecOn(t, ctx, conn, `
INSERT INTO wisp_dependencies (id, issue_id, depends_on_issue_id, type, created_by)
VALUES ('wdep-1', 'w-waited', 'wd-blocked', 'blocks', 'fixture')
`)
		preCommits := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log")

		if _, err := schema.MigrateUp(ctx, conn); err != nil {
			t.Fatalf("MigrateUp (clone): %v", err)
		}

		if got := readIsBlocked(t, ctx, conn, "wisps", "w-free"); got {
			t.Errorf("wisps w-free: is_blocked = true after the debt-free repair, want false")
		}
		if got := readIsBlocked(t, ctx, conn, "wisps", "w-waited"); !got {
			t.Errorf("wisps w-waited: is_blocked = false after the debt-free repair, want true")
		}
		if corrected, err := issueops.CountIsBlockedInconsistenciesInTx(ctx, conn); err != nil {
			t.Fatalf("counting is_blocked inconsistencies: %v", err)
		} else if corrected != 0 {
			t.Errorf("full recompute still corrects %d row(s) on the debt-free clone", corrected)
		}

		// The tracked plane must be byte-identical: the pulled, already-correct
		// issue flags keep their stamps, no obligation key appears, and the
		// pass mints no commit.
		requireStamp(t, ctx, conn, "issues", "wd-related", historicalStamp(3))
		if got := readIsBlocked(t, ctx, conn, "issues", "wd-related"); got {
			t.Errorf("issues wd-related: is_blocked = true after the debt-free repair, want the pulled false")
		}
		if keyRows := scalarInt(t, ctx, conn,
			"SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_repair_owed_0075'"); keyRows != 0 {
			t.Errorf("debt-free clone holds %d obligation key row(s); the wisp-only pass must not mint one", keyRows)
		}
		if got := scalarInt(t, ctx, conn, "SELECT COUNT(*) FROM dolt_log"); got != preCommits {
			t.Errorf("open minted %d commit(s) on a debt-free clone (dolt_log %d -> %d); the wisp-only pass writes no tracked table",
				got-preCommits, preCommits, got)
		}
		if !ignoredCursorHas(t, ctx, conn, 32) {
			t.Fatal("ignored cursor does not record 0032 after the debt-free pass")
		}
	})
}
