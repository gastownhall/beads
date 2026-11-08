package dolt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// These tests pin the server store's pull over GH#6368: a table that is
// dolt_ignore'd on this branch but still tracked by the remote's history makes
// the engine refuse the merge as a stomp of the ignored working table, before
// it checks whether anything needs merging. The fixture uses a scratch table
// rather than events so it does not depend on which beads migration the
// shared test server's schema is at; the engine refusal is the same.

const ignoredScratchTable = "ign_scratch"

// newIgnoredTableServerStore opens a fresh server store whose scratch table was
// tracked and published to a file:// remote, then moved onto the ignore plane
// locally (dropped from history, ignored, recreated with one extra local row).
// The remote is a strict ancestor of HEAD. With diverged, a peer that never
// ignored the table then publishes more work on top of that remote tip.
func newIgnoredTableServerStore(t *testing.T, diverged bool) (context.Context, *DoltStore) {
	t.Helper()
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)

	tmpDir := t.TempDir()
	dbName := uniqueTestDBName(t)
	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        dbName,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		_, _ = store.db.ExecContext(dropCtx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName))
		store.Close()
	})

	// One pinned session for the whole fixture: the peer branch below is
	// session state and must not leak into the store's pool.
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin fixture connection: %v", err)
	}
	defer conn.Close()
	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}

	mustExec("CREATE TABLE " + ignoredScratchTable + " (id INT PRIMARY KEY, v VARCHAR(32))")
	mustExec("INSERT INTO " + ignoredScratchTable + " VALUES (1, 'published')")
	mustExec("CALL DOLT_ADD('-A')")
	mustExec("CALL DOLT_COMMIT('-m', 'test: publish scratch table')")
	mustExec("CALL DOLT_REMOTE('add', 'origin', ?)", "file://"+filepath.Join(tmpDir, "ignored-table-remote"))
	mustExec("CALL DOLT_PUSH('origin', 'main')")
	var remoteTip string
	if err := conn.QueryRowContext(ctx, "SELECT DOLT_HASHOF('HEAD')").Scan(&remoteTip); err != nil {
		t.Fatalf("read published tip: %v", err)
	}

	// Stage the drop before the ignore rule exists: DOLT_ADD skips an
	// ignored table's deletion as well as its rows.
	mustExec("DROP TABLE " + ignoredScratchTable)
	mustExec("CALL DOLT_ADD('-A')")
	mustExec("INSERT INTO dolt_ignore VALUES (?, true)", ignoredScratchTable)
	mustExec("CALL DOLT_ADD('-A')")
	mustExec("CALL DOLT_COMMIT('-m', 'test: move scratch table onto the ignore plane')")
	mustExec("CREATE TABLE " + ignoredScratchTable + " (id INT PRIMARY KEY, v VARCHAR(32))")
	mustExec("INSERT INTO " + ignoredScratchTable + " VALUES (1, 'published'), (2, 'local only')")

	if diverged {
		mustExec("CALL DOLT_BRANCH('peer', ?)", remoteTip)
		mustExec("CALL DOLT_CHECKOUT('peer')")
		mustExec("INSERT INTO " + ignoredScratchTable + " VALUES (3, 'peer')")
		mustExec("CALL DOLT_ADD('-A')")
		mustExec("CALL DOLT_COMMIT('-m', 'test: peer work on the tracked table')")
		mustExec("CALL DOLT_PUSH('origin', 'peer:main')")
		mustExec("CALL DOLT_CHECKOUT('main')")
		mustExec("CALL DOLT_BRANCH('-D', 'peer')")
	}

	var ignored bool
	if err := conn.QueryRowContext(ctx, "SELECT ignored FROM dolt_status_ignored WHERE table_name = ?", ignoredScratchTable).Scan(&ignored); err != nil || !ignored {
		t.Fatalf("fixture scratch table is not an ignored working table: ignored=%v err=%v", ignored, err)
	}

	store.remote = "origin"
	store.branch = "main"
	return ctx, store
}

// ignoredTableState is everything a pull over the ignored table must leave
// alone: the branch head, the ignored working rows, merge state, branches.
type ignoredTableState struct {
	head     string
	rows     []string
	merging  bool
	branches []string
}

func readIgnoredTableState(t *testing.T, ctx context.Context, store *DoltStore) ignoredTableState {
	t.Helper()
	var st ignoredTableState
	var err error
	if st.head, err = store.branchHash(ctx, "main"); err != nil {
		t.Fatalf("read main head: %v", err)
	}
	st.rows = queryStrings(t, ctx, store.db, "SELECT CONCAT(id, ':', v) FROM "+ignoredScratchTable+" ORDER BY id")
	if err := store.db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&st.merging); err != nil {
		t.Fatalf("read merge status: %v", err)
	}
	st.branches = queryStrings(t, ctx, store.db, "SELECT name FROM dolt_branches ORDER BY name")
	return st
}

func queryStrings(t *testing.T, ctx context.Context, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan %s: %v", query, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", query, err)
	}
	return out
}

func assertIgnoredTableStateIntact(t *testing.T, ctx context.Context, store *DoltStore, before ignoredTableState) {
	t.Helper()
	after := readIgnoredTableState(t, ctx, store)
	if after.head != before.head {
		t.Fatalf("pull moved HEAD: %s -> %s", before.head, after.head)
	}
	if !slices.Equal(after.rows, before.rows) {
		t.Fatalf("pull changed the ignored working rows:\nbefore %v\nafter  %v", before.rows, after.rows)
	}
	if after.merging {
		t.Fatal("pull left a merge open")
	}
	if !slices.Equal(after.branches, before.branches) {
		t.Fatalf("pull changed branches: %v -> %v", before.branches, after.branches)
	}
}

// assertPushAdvancesRemote runs the ordinary, non-force push and checks the
// remote now sits at HEAD.
func assertPushAdvancesRemote(t *testing.T, ctx context.Context, store *DoltStore, head string) {
	t.Helper()
	if err := store.Push(ctx); err != nil {
		t.Fatalf("non-force push after the no-op pull: %v", err)
	}
	if err := store.refreshTrackingRef(ctx, "origin"); err != nil {
		t.Fatalf("refresh tracking ref: %v", err)
	}
	var tip string
	if err := store.db.QueryRowContext(ctx, "SELECT hash FROM dolt_remote_branches WHERE name = 'remotes/origin/main'").Scan(&tip); err != nil {
		t.Fatalf("read remote tip: %v", err)
	}
	if tip != head {
		t.Fatalf("remote tip after push = %s, want HEAD %s", tip, head)
	}
}

func assertIgnoredTablesMergeError(t *testing.T, err error) {
	t.Helper()
	var ignoredErr *versioncontrolops.IgnoredTablesMergeError
	if !errors.As(err, &ignoredErr) {
		t.Fatalf("pull error is not an IgnoredTablesMergeError: %v", err)
	}
	if !slices.Equal(ignoredErr.Tables, []string{ignoredScratchTable}) {
		t.Fatalf("IgnoredTablesMergeError.Tables = %v, want [%s]", ignoredErr.Tables, ignoredScratchTable)
	}
	if !strings.Contains(err.Error(), "stomped by merge") || errors.Unwrap(ignoredErr) == nil {
		t.Fatalf("typed error lost the original refusal: %v", err)
	}
}

// TestPullIgnoredTableSQL runs the real SQL pull route (a file:// remote stays
// on CALL DOLT_PULL). A remote tip HEAD already contains is a no-op success
// that leaves the store untouched and lets the ordinary push advance the
// remote; divergent history is the typed ignored-table error.
func TestPullIgnoredTableSQL(t *testing.T) {
	t.Run("contained", func(t *testing.T) {
		ctx, store := newIgnoredTableServerStore(t, false)
		before := readIgnoredTableState(t, ctx, store)

		if err := store.Pull(ctx); err != nil {
			t.Fatalf("pull of a remote tip HEAD already contains = %v; want a no-op success", err)
		}
		assertIgnoredTableStateIntact(t, ctx, store, before)
		assertPushAdvancesRemote(t, ctx, store, before.head)
		if err := store.Pull(ctx); err != nil {
			t.Fatalf("second pull, remote level with HEAD = %v; want success", err)
		}
		assertIgnoredTableStateIntact(t, ctx, store, before)
	})
	t.Run("diverged", func(t *testing.T) {
		ctx, store := newIgnoredTableServerStore(t, true)
		before := readIgnoredTableState(t, ctx, store)

		assertIgnoredTablesMergeError(t, store.Pull(ctx))
		assertIgnoredTableStateIntact(t, ctx, store, before)
	})
}

// cliStompPullError is what the CLI route hands the post-transport handler for
// this fixture: the engine's own refusal (captured from the same merge on this
// store) as `dolt pull` prints it, wrapped by cliTransferError and passed
// through finishCLIPull's conflict cleanup.
func cliStompPullError(t *testing.T, ctx context.Context, store *DoltStore) error {
	t.Helper()
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "CALL DOLT_FETCH('origin', 'main')"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	_, mergeErr := conn.ExecContext(ctx, "CALL DOLT_MERGE('origin/main')")
	if mergeErr == nil {
		t.Fatal("fixture merge was not refused")
	}
	idx := strings.Index(mergeErr.Error(), "error: local changes would be stomped by merge:")
	if idx < 0 {
		t.Fatalf("fixture merge refused for another reason: %v", mergeErr)
	}
	out := []byte("Fetching...\n" + mergeErr.Error()[idx:] + "\n")
	return store.finishCLIPull(ctx, cliTransferError("dolt pull", "origin", context.Background(), out, errors.New("exit status 1")))
}

// TestPullIgnoredTableCLI feeds the CLI route's wrapped failure through the
// same post-transport handler the real pull uses, with the outcomes of the
// SQL route.
func TestPullIgnoredTableCLI(t *testing.T) {
	t.Run("contained", func(t *testing.T) {
		ctx, store := newIgnoredTableServerStore(t, false)
		before := readIgnoredTableState(t, ctx, store)

		if err := store.finishPull(ctx, "origin", before.head, cliStompPullError(t, ctx, store)); err != nil {
			t.Fatalf("CLI pull of a remote tip HEAD already contains = %v; want a no-op success", err)
		}
		assertIgnoredTableStateIntact(t, ctx, store, before)
		assertPushAdvancesRemote(t, ctx, store, before.head)
	})
	t.Run("diverged", func(t *testing.T) {
		ctx, store := newIgnoredTableServerStore(t, true)
		before := readIgnoredTableState(t, ctx, store)

		assertIgnoredTablesMergeError(t, store.finishPull(ctx, "origin", before.head, cliStompPullError(t, ctx, store)))
		assertIgnoredTableStateIntact(t, ctx, store, before)
	})
}

// ignoredTableRemoteURL reads the URL the fixture registered for origin.
func ignoredTableRemoteURL(t *testing.T, ctx context.Context, store *DoltStore) string {
	t.Helper()
	var url string
	if err := store.db.QueryRowContext(ctx, "SELECT url FROM dolt_remotes WHERE name = 'origin'").Scan(&url); err != nil {
		t.Fatalf("read origin url: %v", err)
	}
	return url
}

// fakeStompingDoltOnPath puts a dolt on PATH that answers the version floor
// and the remote listing the CLI routes read, records every other call, and
// answers `dolt pull` with the engine's stomped-tables diagnostic for the
// fixture's scratch table and exit 1: the failure a git-protocol or
// credentialed remote's subprocess reports for GH#6368, deterministically and
// without a reachable remote of that kind. It returns the call log's path.
func fakeStompingDoltOnPath(t *testing.T, remoteURL string) string {
	t.Helper()
	binDir := t.TempDir()
	calls := filepath.Join(binDir, "calls.log")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then echo 'dolt version " + doltversion.RequiredMin.String() + "'; exit 0; fi\n" +
		"if [ \"$1 $2\" = 'remote -v' ]; then echo 'origin " + remoteURL + "'; exit 0; fi\n" +
		"echo \"$@\" >> '" + calls + "'\n" +
		"if [ \"$1\" = pull ]; then printf 'error: local changes would be stomped by merge:\\n\\t" + ignoredScratchTable + "\\n Please commit your changes before you merge.\\n'; exit 1; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatalf("write the fake dolt: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return calls
}

// standInCLIRemoteDir makes store's CLIDir count as the served database its
// CLI routes may run the dolt binary in (hasCLIDatabase): a .dolt directory
// whose repo_state.json records the same origin the server sees, and the
// stand-in for the proof a live server on the dialed port serves it, the
// established pattern (standInForServedDatabase), since the suite's own server
// serves a data directory of its own.
func standInCLIRemoteDir(t *testing.T, store *DoltStore, remoteURL string) {
	t.Helper()
	clearServerPortEnv(t)
	cliDir := store.CLIDir()
	standInForServedDatabase(t, cliDir, store.cfg.ServerPort)
	if err := os.MkdirAll(filepath.Join(cliDir, ".dolt"), 0o755); err != nil {
		t.Fatalf("create the CLI database dir: %v", err)
	}
	state := fmt.Sprintf(`{"remotes":{"origin":{"name":"origin","url":%q}}}`, remoteURL)
	if err := os.WriteFile(filepath.Join(cliDir, ".dolt", "repo_state.json"), []byte(state), 0o600); err != nil {
		t.Fatalf("record the origin remote in the CLI database dir: %v", err)
	}
}

// TestPullIgnoredTableCLIRoute runs the contained case through the real
// store.Pull on the CLI route: credentials on the default remote route the
// transport through the dolt subprocess (the credential route keys on the
// credentials, not the URL scheme, so the file:// remote stays fetchable over
// SQL for the containment evidence), and the subprocess's refusal, a real
// cliTransferError, must reach the shared post-transport handler and be
// recognized as the no-op. The fake dolt's call log proves the route was
// taken.
func TestPullIgnoredTableCLIRoute(t *testing.T) {
	ctx, store := newIgnoredTableServerStore(t, false)
	remoteURL := ignoredTableRemoteURL(t, ctx, store)
	store.remoteUser = "testuser"
	standInCLIRemoteDir(t, store, remoteURL)
	calls := fakeStompingDoltOnPath(t, remoteURL)
	before := readIgnoredTableState(t, ctx, store)

	if err := store.Pull(ctx); err != nil {
		t.Fatalf("CLI-route pull of a remote tip HEAD already contains = %v; want a no-op success", err)
	}
	assertIgnoredTableStateIntact(t, ctx, store, before)

	logged, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read the fake dolt's call log: %v", err)
	}
	if !strings.Contains(string(logged), "pull") {
		t.Fatalf("the CLI route never ran a dolt pull (calls logged: %q)", strings.TrimSpace(string(logged)))
	}
}

// TestPullIgnoredTableRecoveredRunsMaintenance pins that a recognized no-op
// is an ordinary successful pull: the successful-pull maintenance runs, so a
// pending is_blocked recompute marker (bd-578h9.11), which only that
// maintenance consumes, is gone, while the ignored rows and merge state stay
// as they were.
func TestPullIgnoredTableRecoveredRunsMaintenance(t *testing.T) {
	ctx, store := newIgnoredTableServerStore(t, false)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin marker tx: %v", err)
	}
	if err := issueops.MarkIsBlockedRecomputePendingInTx(ctx, tx, "full"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("mark pending: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit marker: %v", err)
	}
	before := readIgnoredTableState(t, ctx, store)

	if err := store.Pull(ctx); err != nil {
		t.Fatalf("pull of a remote tip HEAD already contains = %v; want a no-op success", err)
	}
	var markers int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM metadata WHERE `key` = 'is_blocked_recompute_pending'").Scan(&markers); err != nil {
		t.Fatalf("count markers: %v", err)
	}
	if markers != 0 {
		t.Fatalf("pending recompute marker survived the recovered pull (%d rows); the successful-pull maintenance did not run", markers)
	}
	after := readIgnoredTableState(t, ctx, store)
	if !slices.Equal(after.rows, before.rows) {
		t.Fatalf("pull changed the ignored working rows:\nbefore %v\nafter  %v", before.rows, after.rows)
	}
	if after.merging {
		t.Fatal("pull left a merge open")
	}
}

// TestPullIgnoredTableUnprovable pins that no automatic success is claimed
// from missing evidence, even on a remote that is in fact contained: a
// tracking ref that cannot be refreshed keeps the confirmed refusal as the
// typed error, and an unknown store branch keeps the original failure.
func TestPullIgnoredTableUnprovable(t *testing.T) {
	t.Run("refresh fails", func(t *testing.T) {
		ctx, store := newIgnoredTableServerStore(t, false)
		before := readIgnoredTableState(t, ctx, store)
		pullErr := cliStompPullError(t, ctx, store)
		remoteDir := strings.TrimPrefix(ignoredTableRemoteURL(t, ctx, store), "file://")
		if err := os.RemoveAll(remoteDir); err != nil {
			t.Fatalf("remove the fixture remote: %v", err)
		}
		if err := store.refreshTrackingRef(ctx, "origin"); err == nil {
			t.Fatal("fixture: the tracking ref still refreshes with the remote gone")
		}

		assertIgnoredTablesMergeError(t, store.finishPull(ctx, "origin", before.head, pullErr))
		assertIgnoredTableStateIntact(t, ctx, store, before)
	})
	t.Run("unknown branch", func(t *testing.T) {
		ctx, store := newIgnoredTableServerStore(t, false)
		before := readIgnoredTableState(t, ctx, store)
		pullErr := cliStompPullError(t, ctx, store)
		store.branch = "no-such-branch"

		err := store.finishPull(ctx, "origin", before.head, pullErr)
		store.branch = "main"
		var ignoredErr *versioncontrolops.IgnoredTablesMergeError
		if err == nil || errors.As(err, &ignoredErr) || !strings.Contains(err.Error(), "stomped by merge") {
			t.Fatalf("pull with unknown branch evidence = %v; want the original refusal, unclassified", err)
		}
		assertIgnoredTableStateIntact(t, ctx, store, before)
	})
}
