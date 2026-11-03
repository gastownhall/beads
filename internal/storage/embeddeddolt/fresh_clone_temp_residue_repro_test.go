//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// Repro for bd-2e3gq2 / gastownhall/beads#7073: a lineage whose HEAD carries a
// committed __temp__ table-rebuild intermediate must not wedge a fresh clone.
// The clone runs the ignored series from zero on first open, renames the
// HEAD-tracked intermediate onto the ignored final name, and nothing stages
// that delta; the next pull then fails.
func TestFreshCloneOfTempResidueLineagePulls(t *testing.T) {
	ctx := t.Context()
	f := newPristineEmbeddedDoltFixture(t, "residueclone")
	closeEmbeddedDoltStore(t, f.store)

	remote := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remote, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteURL := "file://" + remote
	withRawConn(t, f.dataDir, f.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "DELETE FROM dolt_ignore WHERE pattern='__temp__%'")
		mustExecOn(t, ctx, conn, "CREATE TABLE __temp__wisp_child_counters (parent_id VARCHAR(255) NOT NULL PRIMARY KEY, last_child INT NOT NULL DEFAULT 0)")
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'auto-commit caught the rebuild intermediate')")
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	cloneBeads := filepath.Join(t.TempDir(), "clone")
	cloneData := filepath.Join(cloneBeads, "embeddeddolt")
	if err := os.MkdirAll(cloneData, 0o700); err != nil {
		t.Fatal(err)
	}
	withRawConn(t, cloneData, "", func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "CALL DOLT_CLONE(?, ?)", remoteURL, f.database)
	})
	store, err := embeddeddolt.Open(ctx, cloneBeads, f.database, "main")
	if err != nil {
		t.Fatalf("open fresh clone: %v", err)
	}
	closeEmbeddedDoltStore(t, store)

	// Hub publishes one more commit; the clone must be able to pull it.
	withRawConn(t, f.dataDir, f.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES ('hub.x', 'y')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'hub')")
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})

	store, err = embeddeddolt.Open(ctx, cloneBeads, f.database, "main")
	if err != nil {
		t.Fatalf("reopen fresh clone: %v", err)
	}
	perr := store.Pull(ctx)
	closeEmbeddedDoltStore(t, store)
	if perr != nil {
		withRawConn(t, cloneData, f.database, func(conn *sql.Conn) {
			t.Logf("clone __temp__wisp_child_counters tracked at HEAD=%v present=%v",
				trackedAtHead(t, ctx, conn, "__temp__wisp_child_counters"),
				tablePresent(t, ctx, conn, "__temp__wisp_child_counters"))
		})
		t.Fatalf("fresh clone of a lineage with a HEAD-tracked __temp__ intermediate cannot pull: %v", perr)
	}
}