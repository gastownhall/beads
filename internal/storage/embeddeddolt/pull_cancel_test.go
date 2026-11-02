//go:build cgo

package embeddeddolt

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	doltembed "github.com/dolthub/driver/v2"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// cancelAfterMergeConn hands every statement to the store's engine
// connection and cancels the pull's caller just before the first statement
// after DOLT_MERGE. That is the point where the conflict flags have let a
// conflicted merge into the working set and only the settle pass, or its
// abort, can take it out again.
type cancelAfterMergeConn struct {
	versioncontrolops.DBConn
	cancel context.CancelFunc

	merged   bool
	canceled bool
}

func (c *cancelAfterMergeConn) next(query string) {
	if c.merged && !c.canceled {
		c.cancel()
		c.canceled = true
	}
	if strings.Contains(query, "DOLT_MERGE(") {
		c.merged = true
	}
}

func (c *cancelAfterMergeConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.next(query)
	return c.DBConn.ExecContext(ctx, query, args...)
}

func (c *cancelAfterMergeConn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	c.next(query)
	return c.DBConn.QueryContext(ctx, query, args...)
}

func (c *cancelAfterMergeConn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	c.next(query)
	return c.DBConn.QueryRowContext(ctx, query, args...)
}

// cancelPullsAfterMerge keeps the real versioncontrolops pull on the store's
// own engine connection, fetch included, but runs it through a
// cancelAfterMergeConn that cancels the caller with cancel.
func cancelPullsAfterMerge(t *testing.T, cancel context.CancelFunc) *cancelAfterMergeConn {
	t.Helper()
	conn := &cancelAfterMergeConn{cancel: cancel}
	prevPull, prevPullStrategy := vcPull, vcPullWithStrategy
	t.Cleanup(func() { vcPull, vcPullWithStrategy = prevPull, prevPullStrategy })
	vcPull = func(ctx context.Context, db versioncontrolops.DBConn, remote, branch, user string) error {
		conn.DBConn = db
		return versioncontrolops.Pull(ctx, conn, remote, branch, user)
	}
	vcPullWithStrategy = func(ctx context.Context, db versioncontrolops.DBConn, remote, branch, user, strategy string) error {
		conn.DBConn = db
		return versioncontrolops.PullWithStrategy(ctx, conn, remote, branch, user, strategy)
	}
	return conn
}

// newDivergedPullStore returns a store whose main branch has diverged from
// its file:// remote, registered as both "origin" and "team". The remote's
// main sets metadata key pull_cancel_probe to "theirs" and adds the edge
// pc-a blocks-on pc-b without touching is_blocked, as a row merged in from
// another clone can leave it; local main sets the key to "ours". A pull
// therefore merges a metadata conflict that the settle pass must resolve,
// and only the post-pull recompute marks pc-a blocked.
func newDivergedPullStore(t *testing.T) *EmbeddedDoltStore {
	t.Helper()
	ctx := t.Context()

	store, err := Open(ctx, filepath.Join(t.TempDir(), ".beads"), "pullcancel", "main")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("create remote dir: %v", err)
	}

	db, cleanup, err := OpenSQL(ctx, store.dataDir, store.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	defer func() { _ = cleanup() }()
	exec := func(stage, query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}

	for _, id := range []string{"pc-a", "pc-b"} {
		exec("seed "+id, "INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type) "+
			"VALUES (?, ?, '', '', '', '', 'open', 2, 'task')", id, id)
	}
	exec("commit base", "CALL DOLT_COMMIT('-Am', 'test: base')")
	var base string
	if err := db.QueryRowContext(ctx, "SELECT commit_hash FROM dolt_log LIMIT 1").Scan(&base); err != nil {
		t.Fatalf("read base commit: %v", err)
	}
	exec("add origin", "CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+remoteDir)
	exec("add team", "CALL DOLT_REMOTE('add', 'team', ?)", "file://"+remoteDir)

	// The remote's side: published, then rewound off the local branch, which
	// leaves it only on the remote.
	exec("remote metadata", "INSERT INTO metadata (`key`, value) VALUES ('pull_cancel_probe', 'theirs')")
	exec("remote edge", "INSERT INTO dependencies (id, issue_id, depends_on_issue_id, type, created_at, created_by) "+
		"VALUES (UUID(), 'pc-a', 'pc-b', 'blocks', NOW(), 'peer')")
	exec("commit remote side", "CALL DOLT_COMMIT('-Am', 'test: remote side')")
	exec("push remote side", "CALL DOLT_PUSH('origin', 'main')")
	exec("rewind to base", "CALL DOLT_RESET('--hard', ?)", base)

	exec("local metadata", "INSERT INTO metadata (`key`, value) VALUES ('pull_cancel_probe', 'ours')")
	exec("commit local side", "CALL DOLT_COMMIT('-Am', 'test: local side')")
	return store
}

// databaseHolder stands in for another process that takes the embedded
// database the way a Dolt engine does, with an exclusive flock on
// .dolt/noms/LOCK, as soon as a pull's own engine has let go of it after the
// merge: at the first engine open after the caller was canceled, which is the
// post-pull recompute's.
type databaseHolder struct {
	mu       sync.Mutex
	lock     *os.File
	took     bool
	watchdog bool // the watchdog, not the test, let the database go
}

// holdDatabaseAfterMerge routes OpenSQL's engine connector through a
// databaseHolder for store's database. Its watchdog lets the database go
// after a minute, so a pull that waits for the holder fails the test instead
// of hanging it.
func holdDatabaseAfterMerge(t *testing.T, store *EmbeddedDoltStore, conn *cancelAfterMergeConn) *databaseHolder {
	t.Helper()
	h := &databaseHolder{}
	lockPath := filepath.Join(store.dataDir, store.database, ".dolt", "noms", "LOCK")
	prev := newEmbeddedConnector
	newEmbeddedConnector = func(cfg doltembed.Config) (embeddedConnector, error) {
		if conn.canceled {
			h.take(t, lockPath)
		}
		return prev(cfg)
	}
	watchdog := time.AfterFunc(time.Minute, func() { h.letGo(true) })
	t.Cleanup(func() {
		newEmbeddedConnector = prev
		watchdog.Stop()
		h.letGo(false)
	})
	return h
}

// take locks the database once. The pull's engine has closed by now, but the
// lock is retried briefly in case its release lags the close.
func (h *databaseHolder) take(t *testing.T, lockPath string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.took {
		return
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR, 0) //nolint:gosec // G304: the test's own database directory
	if err != nil {
		t.Errorf("open the database lock: %v", err)
		return
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		err = lockfile.FlockExclusiveNonBlocking(f)
		if err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		t.Errorf("take the database lock after the merge: %v", err)
		return
	}
	h.lock, h.took = f, true
}

// letGo releases the database if it is held.
func (h *databaseHolder) letGo(byWatchdog bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.lock == nil {
		return
	}
	_ = lockfile.FlockUnlock(h.lock)
	_ = h.lock.Close()
	h.lock = nil
	h.watchdog = byWatchdog
}

func (h *databaseHolder) state() (took, watchdog bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.took, h.watchdog
}

// pullState is what a pull left in the database.
type pullState struct {
	conflicts int    // tables in dolt_conflicts
	merging   bool   // dolt_merge_status.is_merging
	probe     string // the merged pull_cancel_probe metadata value
	blocked   bool   // pc-a's is_blocked
}

func readPullState(t *testing.T, store *EmbeddedDoltStore) pullState {
	t.Helper()
	db, cleanup, err := OpenSQL(t.Context(), store.dataDir, store.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	var st pullState
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM dolt_conflicts").Scan(&st.conflicts); err != nil {
		t.Fatalf("read dolt_conflicts: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT is_merging FROM dolt_merge_status").Scan(&st.merging); err != nil {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	if err := db.QueryRowContext(t.Context(),
		"SELECT value FROM metadata WHERE `key` = 'pull_cancel_probe'").Scan(&st.probe); err != nil {
		t.Fatalf("read merged metadata: %v", err)
	}
	if err := db.QueryRowContext(t.Context(),
		"SELECT is_blocked FROM issues WHERE id = 'pc-a'").Scan(&st.blocked); err != nil {
		t.Fatalf("read is_blocked: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("close verification connection: %v", err)
	}
	return st
}

// assertMergeSettled fails the test unless the pull's merge was settled.
func assertMergeSettled(t *testing.T, st pullState) {
	t.Helper()
	if st.conflicts != 0 || st.merging {
		t.Errorf("database left mid-merge: %d conflicted table(s), is_merging=%v", st.conflicts, st.merging)
	}
	if st.probe != "theirs" {
		t.Errorf("pull_cancel_probe = %q, want the remote's %q from the settled merge", st.probe, "theirs")
	}
}

// TestEmbeddedPullFinishesMergeAfterCallerCancellation pins, on the real
// engine, that every embedded pull verb finishes its merge when its caller
// is canceled (the first SIGINT or SIGTERM of `bd dolt pull` or `bd sync`,
// GH#7989) right after DOLT_MERGE let a conflicted merge into the working
// set: the settle pass still resolves and commits it, and is_blocked is still
// recomputed. Run on the canceled context instead, the settle failed, its
// abort failed with it, and the database stayed mid-merge, refusing every
// later write and pull.
//
// The recompute reopens the database, and an open waits for as long as
// another process holds it. So when one takes the database right after the
// merge, the recompute gives up afterMergeGrace after the cancellation, with
// an error that names the repair, instead of keeping bd from exiting until
// the holder lets go.
func TestEmbeddedPullFinishesMergeAfterCallerCancellation(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	for _, ep := range remoteEntryPoints {
		if !ep.pull {
			continue
		}
		t.Run(ep.name, func(t *testing.T) {
			store := newDivergedPullStore(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			conn := cancelPullsAfterMerge(t, cancel)

			pullErr := ep.call(ctx, store)
			if !conn.canceled || ctx.Err() == nil {
				t.Fatalf("the caller was not canceled after DOLT_MERGE (%s: %v)", ep.name, pullErr)
			}
			if pullErr != nil {
				t.Errorf("%s canceled after DOLT_MERGE: %v", ep.name, pullErr)
			}

			st := readPullState(t, store)
			assertMergeSettled(t, st)
			if !st.blocked {
				t.Error("pc-a is_blocked = false: the post-pull recompute did not run")
			}
			if _, err := store.CommitPending(t.Context(), "tester"); err != nil {
				t.Errorf("CommitPending after the pull: %v", err)
			}
		})
	}

	t.Run("database held after the merge", func(t *testing.T) {
		prevGrace := afterMergeGrace
		afterMergeGrace = 200 * time.Millisecond
		t.Cleanup(func() { afterMergeGrace = prevGrace })
		for _, ep := range remoteEntryPoints {
			if !ep.pull {
				continue
			}
			t.Run(ep.name, func(t *testing.T) {
				store := newDivergedPullStore(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				conn := cancelPullsAfterMerge(t, cancel)
				holder := holdDatabaseAfterMerge(t, store, conn)

				pullErr := ep.call(ctx, store)
				took, byWatchdog := holder.state()
				holder.letGo(false)
				if !took {
					t.Fatalf("the database was not taken after the merge (%s: %v)", ep.name, pullErr)
				}
				if byWatchdog {
					t.Fatalf("%s waited for the holder to let go of the database: %v", ep.name, pullErr)
				}
				if pullErr == nil || !strings.Contains(pullErr.Error(), "run `bd recompute-blocked`") {
					t.Errorf("%s with the database held after the merge: %v, want an error naming bd recompute-blocked", ep.name, pullErr)
				}

				st := readPullState(t, store)
				assertMergeSettled(t, st)
				if st.blocked {
					t.Fatal("pc-a is_blocked = true although the recompute could not open the database")
				}
				// The operator's path: the merged rows are still uncommitted
				// (bd-6dnrw.39), which bd recompute-blocked refuses until they
				// are committed, and then the repair the error names.
				if _, err := store.CommitPending(t.Context(), "tester"); err != nil {
					t.Fatalf("CommitPending after the pull: %v", err)
				}
				if _, err := store.RecomputeAllBlocked(t.Context()); err != nil {
					t.Fatalf("the repair the error names: %v", err)
				}
				if !readPullState(t, store).blocked {
					t.Error("pc-a is_blocked = false after the repair the error names")
				}
			})
		}
	})
}
