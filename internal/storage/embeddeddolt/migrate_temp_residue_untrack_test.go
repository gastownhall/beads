//go:build cgo

package embeddeddolt_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// These tests cover gastownhall/beads#7073 against the real engine: a lineage
// whose HEAD carries a __temp__ table-rebuild intermediate that an older
// binary committed (a @@dolt_transaction_commit=1 auto-commit, or a blanket
// DOLT_ADD('-A') from before the __temp__% pattern shipped).
//
// The ignored-lane cursor is clone-local, so every fresh clone of such a
// lineage replays the ignored series from zero on its first open, and
// ignored/0002 renames the HEAD-tracked __temp__wisp_child_counters onto the
// ignored final name. Nothing can stage that rename, and the next pull dies
// with Dolt's "malformed database: chunk … required but not found". The
// fixtures and the red/green shape are adapted from the reporter's probe
// (@bee-ghosttrack, #7073).

const tempResidueTable = "__temp__wisp_child_counters"

// tempResidueLineage is a hub whose HEAD carries tempResidueTable (or, for
// the control, does not), pushed to a file remote a clone can be made from.
type tempResidueLineage struct {
	hub       *pristineEmbeddedDoltFixture
	remoteURL string
}

// newTempResidueLineage commits the intermediate the way a pre-#6993 binary
// did: with no __temp__% pattern in dolt_ignore, so a blanket add takes it.
func newTempResidueLineage(t *testing.T, database string, residue bool) *tempResidueLineage {
	t.Helper()
	ctx := t.Context()
	hub := newPristineEmbeddedDoltFixture(t, database)
	closeEmbeddedDoltStore(t, hub.store)

	remote := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remote, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteURL := "file://" + remote
	withRawConn(t, hub.dataDir, hub.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "DELETE FROM dolt_ignore WHERE pattern='__temp__%'")
		if residue {
			mustExecOn(t, ctx, conn, "CREATE TABLE "+tempResidueTable+
				" (parent_id VARCHAR(255) NOT NULL PRIMARY KEY, last_child INT NOT NULL DEFAULT 0)")
		}
		mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-A')")
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'auto-commit caught the rebuild intermediate')")
		if residue && !trackedAtHead(t, ctx, conn, tempResidueTable) {
			t.Fatalf("fixture did not commit %s into HEAD; the wedge cannot be reproduced", tempResidueTable)
		}
		mustExecOn(t, ctx, conn, "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
	return &tempResidueLineage{hub: hub, remoteURL: remoteURL}
}

// clone makes a fresh clone of the lineage WITHOUT opening it through bd, so
// the first bd open is the one under test. It returns the clone's beads and
// data directories.
func (l *tempResidueLineage) clone(t *testing.T) (string, string) {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), "clone")
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	withRawConn(t, dataDir, "", func(conn *sql.Conn) {
		mustExecOn(t, t.Context(), conn, "CALL DOLT_CLONE(?, ?)", l.remoteURL, l.hub.database)
	})
	return beadsDir, dataDir
}

// publish lands one more hub commit on the remote, carrying a marker row the
// clone must receive by pulling.
func (l *tempResidueLineage) publish(t *testing.T, key, value string) {
	t.Helper()
	ctx := t.Context()
	withRawConn(t, l.hub.dataDir, l.hub.database, func(conn *sql.Conn) {
		mustExecOn(t, ctx, conn, "INSERT INTO config (`key`, value) VALUES (?, ?)", key, value)
		mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-Am', 'hub: publish a marker')")
		mustExecOn(t, ctx, conn, "CALL DOLT_PUSH('origin', 'main')")
	})
}

func openAndClose(t *testing.T, beadsDir, database string) {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open %s: %v", beadsDir, err)
	}
	closeEmbeddedDoltStore(t, store)
}

// pullOnce opens the store writable (which migrates, and so reconciles) and
// pulls, returning the pull's error.
func pullOnce(t *testing.T, beadsDir, database string) error {
	t.Helper()
	store, err := embeddeddolt.Open(t.Context(), beadsDir, database, "main")
	if err != nil {
		t.Fatalf("open %s: %v", beadsDir, err)
	}
	pullErr := store.Pull(t.Context())
	closeEmbeddedDoltStore(t, store)
	return pullErr
}

func configValue(t *testing.T, ctx context.Context, conn *sql.Conn, key string) string {
	t.Helper()
	var value string
	if err := conn.QueryRowContext(ctx, "SELECT value FROM config WHERE `key` = ?", key).Scan(&value); err != nil {
		t.Fatalf("reading config %q: %v", key, err)
	}
	return value
}

// residueStatus is every dolt_status row that mentions a __temp__ name: a
// delete, a rename half, or a modification of a tracked intermediate. Any of
// them is a non-add delta Dolt's merge refuses.
func residueStatus(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	var out []string
	for _, name := range dirtyTableNames(t, ctx, conn) {
		if strings.Contains(name, "__temp__") {
			out = append(out, name)
		}
	}
	return out
}

// TestFreshCloneOfTempResidueLineagePulls is the issue's red/green probe: a
// fresh clone of a lineage that tracks __temp__wisp_child_counters at HEAD
// opens writable, then pulls a new hub commit. Before the fix the open
// renamed the tracked intermediate onto the ignored final name and the pull
// failed with "malformed database"; the residue=false control proves the
// fixture itself pulls.
func TestFreshCloneOfTempResidueLineagePulls(t *testing.T) {
	for _, residue := range []bool{false, true} {
		t.Run(fmt.Sprintf("residue=%v", residue), func(t *testing.T) {
			ctx := t.Context()
			lineage := newTempResidueLineage(t, "residueclone", residue)
			cloneBeads, cloneData := lineage.clone(t)
			database := lineage.hub.database

			openAndClose(t, cloneBeads, database)
			withRawConn(t, cloneData, database, func(conn *sql.Conn) {
				if trackedAtHead(t, ctx, conn, tempResidueTable) {
					t.Errorf("%s is still tracked at HEAD after the clone's first writable open", tempResidueTable)
				}
				if residue := residueStatus(t, ctx, conn); len(residue) > 0 {
					t.Errorf("dolt_status after first open carries __temp__ residue %v; pull would refuse it", residue)
				}
			})

			lineage.publish(t, "hub.marker", "from the hub")
			if err := pullOnce(t, cloneBeads, database); err != nil {
				t.Fatalf("fresh clone of a lineage (residue=%v) cannot pull: %v", residue, err)
			}
			withRawConn(t, cloneData, database, func(conn *sql.Conn) {
				if got := configValue(t, ctx, conn, "hub.marker"); got != "from the hub" {
					t.Errorf("pulled hub marker = %q, want %q", got, "from the hub")
				}
				if trackedAtHead(t, ctx, conn, tempResidueTable) {
					t.Errorf("%s is tracked at HEAD again after the pull", tempResidueTable)
				}
				if residue := residueStatus(t, ctx, conn); len(residue) > 0 {
					t.Errorf("dolt_status after pull carries __temp__ residue %v", residue)
				}
			})
		})
	}
}
