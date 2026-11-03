//go:build cgo

package embeddeddolt_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// The #7073 shape (reported by @bee-ghosttrack): a lineage whose HEAD carries
// a committed __temp__ rebuild intermediate replicates to every fresh clone,
// whose first open renames the intermediate onto the ignored final name and
// leaves an unstageable rename delta that wedges pull. MigrateUp's open-time
// residue reconcile must untrack the intermediate from HEAD before the series
// run, unwedging the clone. residue=false is the control: the same lineage
// with no intermediate at HEAD must keep working unchanged.
func TestEmbeddedFreshCloneOfTempResidueLineageUnwedgesPull(t *testing.T) {
	for _, residue := range []bool{true, false} {
		t.Run(fmt.Sprintf("residue=%v", residue), func(t *testing.T) {
			ctx := t.Context()
			f := newPristineEmbeddedDoltFixture(t, "residueprobe")
			closeEmbeddedDoltStore(t, f.store)

			remote := filepath.Join(t.TempDir(), "remote")
			if err := os.MkdirAll(remote, 0o700); err != nil {
				t.Fatal(err)
			}
			remoteURL := "file://" + remote
			withRawConn(t, f.dataDir, f.database, func(conn *sql.Conn) {
				if residue {
					// Commit the intermediate without the ignore pattern, the
					// way an older binary did: with the pattern in place an
					// unforced DOLT_ADD silently skips the name.
					mustExecOn(t, ctx, conn, "DELETE FROM dolt_ignore WHERE pattern='__temp__%'")
					mustExecOn(t, ctx, conn, "CREATE TABLE __temp__wisp_child_counters (parent_id VARCHAR(255) NOT NULL PRIMARY KEY, last_child INT NOT NULL DEFAULT 0)")
					mustExecOn(t, ctx, conn, "CALL DOLT_ADD('-A')")
					mustExecOn(t, ctx, conn, "CALL DOLT_COMMIT('-m', 'auto-commit caught the rebuild intermediate')")
				}
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

			// The hub publishes one more commit; the clone must be able to
			// pull it. The residue rename otherwise leaves an unstageable
			// non-add delta that makes every pull fail.
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
				t.Fatalf("pull after open of a %s lineage: %v", residueWord(residue), perr)
			}

			withRawConn(t, cloneData, f.database, func(conn *sql.Conn) {
				if tracked := trackedAtHead(t, ctx, conn, "__temp__wisp_child_counters"); tracked {
					t.Error("__temp__wisp_child_counters is still tracked at HEAD after the residue reconcile")
				}
				// No rename or delete residue may survive the repair: the
				// unstageable "__temp__X -> X" rename half is exactly what
				// wedged the pull, and the ignored final table must not be
				// reported as a tracked change either.
				for _, dirty := range dirtyTableNames(t, ctx, conn) {
					if slices.Contains([]string{"__temp__wisp_child_counters", "wisp_child_counters"}, dirty) {
						t.Errorf("dolt_status reports %q after the residue reconcile, want no rebuild residue", dirty)
					}
				}
			})
		})
	}
}

func residueWord(residue bool) string {
	if residue {
		return "residue"
	}
	return "clean"
}