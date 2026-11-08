package dolt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/doltversion"
)

// These tests pin the shared-server side of gastownhall/beads#6368: a migrated
// store (events on the dolt_ignored working plane) with local ignored events
// rows pulls from a remote whose main is already contained in the branch this
// store reads. Both server pull routes die with the engine's stomped-tables
// refusal before they ever compare histories — CALL DOLT_PULL with the wrapped
// engine error, the CLI route with a cliTransferError carrying the subprocess
// output — and the post-transport handler must recognize the proven no-op and
// report pull success, without changing the rows the stomp guard protected,
// the branch head, or the merge state. The embedded store's own shape is
// pinned in internal/storage/embeddeddolt; the sync-level outcome in
// cmd/bd/sync_test.go.

// eventsFlipDropMessage is the migration 0062 self-commit that dropped the
// versioned events table. This store's own history contains it, so its parent
// — where events is still versioned — is a commit every migrated HEAD
// contains: the source of a contained remote tip.
const eventsFlipDropMessage = "schema: drop versioned events table for ignored-plane flip (bd-red8u)"

// ignoredServerPullFixture builds the #6368 no-op shape on a fresh database of
// the shared test server: the flip's parent commit pushed as the remote's
// main, then local work — a committed bead and ignored events rows — leaving
// the remote a strict ancestor of the branch the store reads. It returns the
// store, the remote URL, and the working events rows at the end of the
// fixture.
func ignoredServerPullFixture(t *testing.T) (store *DoltStore, remoteURL string, events []string) {
	t.Helper()
	skipIfNoDolt(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

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

	mustExec := func(stage, query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
	}
	scalarInt := func(query string) int {
		t.Helper()
		var n int
		if err := store.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	scalarString := func(query string) string {
		t.Helper()
		var s string
		if err := store.db.QueryRowContext(ctx, query).Scan(&s); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return s
	}

	// The contained remote tip: the parent of the flip commit, where events is
	// still versioned. dolt_log walks newest-first, so the row after the flip
	// is its parent (the migration history is linear; the fixture's
	// containment evidence below fails loudly if it is not).
	rows, err := store.db.QueryContext(ctx, "SELECT commit_hash, message FROM dolt_log")
	if err != nil {
		t.Fatalf("read dolt_log: %v", err)
	}
	var flipHash, preflipHash string
	for rows.Next() {
		var hash, message string
		if err := rows.Scan(&hash, &message); err != nil {
			t.Fatalf("scan dolt_log row: %v", err)
		}
		if flipHash != "" {
			preflipHash = hash
			break
		}
		if message == eventsFlipDropMessage {
			flipHash = hash
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dolt_log: %v", err)
	}
	rows.Close()
	if flipHash == "" || preflipHash == "" || flipHash == preflipHash {
		t.Fatalf("could not find the flip commit's parent in dolt_log (flip %q, next %q)", flipHash, preflipHash)
	}
	mustExec("branch at pre-flip tip", "CALL DOLT_BRANCH('preflip', ?)", preflipHash)

	// file:// inside the server's filesystem; Dolt creates it on push. The
	// remote's main is pinned at the pre-flip tip by the refspec push, while
	// this store's main keeps every migration after it.
	remoteURL = "file://" + filepath.Join(tmpDir, "ignored-pull-remote")
	mustExec("add remote", "CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
	mustExec("push pre-flip tip as the remote's main", "CALL DOLT_PUSH('origin', 'preflip:main')")

	store.remote = "origin"
	store.branch = "main"

	// Local work ahead of the remote: a committed bead (tracked — commit it
	// explicitly so the pull's auto-commit has nothing left to move the head
	// with) and ignored events rows (never committable: staging anti-joins
	// dolt_ignore, which is exactly the working state the stomp guard
	// protects).
	mustExec("insert bead", `
INSERT INTO issues (id, title, description, design, acceptance_criteria, notes, status, priority, issue_type)
VALUES ('ignored-pull-1', 'ignored pull seed', '', '', '', '', 'open', 2, 'task');
INSERT INTO events (issue_id, event_type, actor, created_at, id)
VALUES ('ignored-pull-1', 'created', 'tester', '2026-11-08 00:00:00', 'evt-ignored-1');
`)
	mustExec("commit tracked work", "CALL DOLT_COMMIT('-Am', 'test: local work ahead of the pre-flip remote')")

	// Fixture evidence: events is engine-confirmed ignored, and the remote's
	// main is contained in this branch — the no-op shape, or the fixture does
	// not reproduce #6368.
	if got := scalarInt("SELECT COUNT(*) FROM dolt_status_ignored WHERE table_name = 'events' AND ignored"); got != 1 {
		t.Fatalf("dolt_status_ignored lists events %d time(s); the fixture does not reproduce #6368", got)
	}
	base := scalarString("SELECT DOLT_MERGE_BASE('main', 'remotes/origin/main')")
	tip := scalarString("SELECT DOLT_HASHOF('origin/main')")
	if base != tip {
		t.Fatalf("remote main %s is not contained in this branch (merge base %s); the fixture is not the no-op shape", tip, base)
	}

	eventRows, err := store.db.QueryContext(ctx, "SELECT id FROM events ORDER BY id")
	if err != nil {
		t.Fatalf("read events rows: %v", err)
	}
	for eventRows.Next() {
		var id string
		if err := eventRows.Scan(&id); err != nil {
			t.Fatalf("scan events row: %v", err)
		}
		events = append(events, id)
	}
	if err := eventRows.Err(); err != nil {
		t.Fatalf("iterate events rows: %v", err)
	}
	eventRows.Close()
	return store, remoteURL, events
}

// requireIgnoredPullOutcome asserts what both routes must leave behind when
// the refused no-op pull is recognized: the pull succeeded, the branch head
// did not move, the ignored events rows are exactly what they were, and no
// merge is open.
func requireIgnoredPullOutcome(t *testing.T, ctx context.Context, store *DoltStore, headBefore string, eventsBefore []string) {
	t.Helper()
	if err := store.Pull(ctx); err != nil {
		t.Fatalf("pull of a contained remote main = %v; want the no-op pull to succeed", err)
	}
	headAfter, err := store.branchHash(ctx, "main")
	if err != nil {
		t.Fatalf("read main's head after the pull: %v", err)
	}
	if headAfter != headBefore {
		t.Fatalf("no-op pull moved main: %s -> %s", headBefore, headAfter)
	}
	rows, err := store.db.QueryContext(ctx, "SELECT id FROM events ORDER BY id")
	if err != nil {
		t.Fatalf("read events rows after the pull: %v", err)
	}
	var eventsAfter []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan events row: %v", err)
		}
		eventsAfter = append(eventsAfter, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate events rows: %v", err)
	}
	rows.Close()
	if strings.Join(eventsAfter, ",") != strings.Join(eventsBefore, ",") {
		t.Fatalf("no-op pull changed local ignored events rows:\nbefore %v\nafter  %v", eventsBefore, eventsAfter)
	}
	var merging bool
	if err := store.db.QueryRowContext(ctx, "SELECT is_merging FROM dolt_merge_status").Scan(&merging); err != nil {
		t.Fatalf("read dolt_merge_status: %v", err)
	}
	if merging {
		t.Fatal("no-op pull left a merge open")
	}
}

// TestPullIgnoredTableAncestorSQLRoute is the #6368 no-op over the SQL pull
// route (file:// remotes stay on CALL DOLT_PULL): the engine's refusal,
// wrapped by the route's own error framing, must be recognized as the no-op it
// guarded. The recovery this makes possible — an ordinary push moving the
// remote to this store's head, clearing the wedge for every replica — and the
// equal-tip pull every replica sees afterwards are pinned here too.
func TestPullIgnoredTableAncestorSQLRoute(t *testing.T) {
	store, _, eventsBefore := ignoredServerPullFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	headBefore, err := store.branchHash(ctx, "main")
	if err != nil {
		t.Fatalf("read main's head before the pull: %v", err)
	}
	requireIgnoredPullOutcome(t, ctx, store, headBefore, eventsBefore)

	// The recovery: an ordinary (never force) push advances the remote to this
	// store's head.
	if err := store.Push(ctx); err != nil {
		t.Fatalf("push after the no-op pull failed: %v", err)
	}
	headAfterPush, err := store.branchHash(ctx, "main")
	if err != nil {
		t.Fatalf("read main's head after the push: %v", err)
	}
	var remoteHash string
	if err := store.db.QueryRowContext(ctx,
		"SELECT hash FROM dolt_remote_branches WHERE name = 'remotes/origin/main'").Scan(&remoteHash); err != nil {
		t.Fatalf("read the remote's main after the push: %v", err)
	}
	if remoteHash != headAfterPush {
		t.Fatalf("push left the remote's main at %s, want this store's head %s", remoteHash, headAfterPush)
	}

	// The equal-tip pull every other replica runs after the push: it must
	// succeed for the same reason — and, with a clean tracked working set, it
	// must not move the head either.
	if err := store.Pull(ctx); err != nil {
		t.Fatalf("second pull of the pushed tip = %v; want the equal-tip no-op to succeed", err)
	}
	got, err := store.branchHash(ctx, "main")
	if err != nil {
		t.Fatalf("read main's head after the second pull: %v", err)
	}
	if got != headAfterPush {
		t.Fatalf("equal-tip pull moved main: %s -> %s", headAfterPush, got)
	}
}

// fakeStompingDoltOnPath puts a dolt on PATH that answers the version floor
// and the remote listing the CLI routes read, records every other call, and
// answers `dolt pull` with the engine's stomped-tables diagnostic and exit 1 —
// the exact failure shape a git-protocol or credentialed remote's subprocess
// reports for #6368, deterministically and without a reachable remote of that
// kind. It returns the call log's path.
func fakeStompingDoltOnPath(t *testing.T, remoteURL string) string {
	t.Helper()
	binDir := t.TempDir()
	calls := filepath.Join(binDir, "calls.log")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then echo 'dolt version " + doltversion.RequiredMin.String() + "'; exit 0; fi\n" +
		"if [ \"$1 $2\" = 'remote -v' ]; then echo 'origin " + remoteURL + "'; exit 0; fi\n" +
		"echo \"$@\" >> '" + calls + "'\n" +
		"if [ \"$1\" = pull ]; then printf 'error: local changes would be stomped by merge:\\n\\tevents\\n Please commit your changes before you merge.\\n'; exit 1; fi\n" +
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
// stand-in for the proof a live server on the dialed port serves it — the
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

// TestPullIgnoredTableAncestorCLIRoute is the same #6368 no-op over the CLI
// pull route: credentials on the default remote route the transport through
// the dolt subprocess (the credential route keys on the credentials, not the
// URL scheme, so the file:// remote stays fetchable over SQL for the
// containment evidence), and the subprocess's refusal — a real cliTransferError
// wrapping the engine's diagnostic — must be recognized the same way. The
// route is proven taken by the fake dolt's call log.
func TestPullIgnoredTableAncestorCLIRoute(t *testing.T) {
	store, remoteURL, eventsBefore := ignoredServerPullFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// The credential route: creds on the default remote, a CLI database dir
	// the store may run the binary in, and the remote materialized there.
	store.remoteUser = "testuser"
	standInCLIRemoteDir(t, store, remoteURL)
	calls := fakeStompingDoltOnPath(t, remoteURL)

	headBefore, err := store.branchHash(ctx, "main")
	if err != nil {
		t.Fatalf("read main's head before the pull: %v", err)
	}
	requireIgnoredPullOutcome(t, ctx, store, headBefore, eventsBefore)

	// Control: the pull really went through the dolt subprocess (and its
	// refusal), not around it — the fake dolt's log names the pull.
	logged, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read the fake dolt's call log: %v", err)
	}
	if !strings.Contains(string(logged), "pull") {
		t.Fatalf("the CLI route never ran a dolt pull (calls logged: %q)", strings.TrimSpace(string(logged)))
	}

	// The stomp guard's evidence still holds through the recognized no-op: the
	// events rows are uncommitted working rows, so dolt_status_ignored still
	// lists events — the state an operator recovering with `bd dolt push`
	// starts from.
	var ignored int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM dolt_status_ignored WHERE table_name = 'events' AND ignored").Scan(&ignored); err != nil {
		t.Fatalf("read dolt_status_ignored: %v", err)
	}
	if ignored != 1 {
		t.Fatalf("dolt_status_ignored lists events %d time(s) after the recognized no-op", ignored)
	}
}
