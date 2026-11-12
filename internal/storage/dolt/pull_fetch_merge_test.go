package dolt

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestSQLFetchCallPassesUserFlag pins the fetch half of the server-mode SQL
// pull (bd-e2q8zs): the Hosted Dolt route authenticates with --user exactly as
// embedded mode's PullWithStrategy does, and the default route does not.
func TestSQLFetchCallPassesUserFlag(t *testing.T) {
	for _, tc := range []struct {
		user      string
		wantQuery string
		wantArgs  []any
	}{
		{user: "u", wantQuery: "CALL DOLT_FETCH('--user', ?, ?, ?)", wantArgs: []any{"u", "origin", "main"}},
		{user: "", wantQuery: "CALL DOLT_FETCH(?, ?)", wantArgs: []any{"origin", "main"}},
	} {
		query, args := sqlFetchCall(tc.user, "origin", "main")
		if query != tc.wantQuery || !reflect.DeepEqual(args, tc.wantArgs) {
			t.Errorf("sqlFetchCall(%q) = (%q, %v), want (%q, %v)", tc.user, query, args, tc.wantQuery, tc.wantArgs)
		}
	}
}

// TestFetchAndMergeTrackingRefInTx pins the statements the SQL pull issues in
// its transaction: it fetches (with --user when the route has one) and then
// merges the tracking ref with CALL DOLT_MERGE, never CALL DOLT_PULL, whose
// merge message copies the stored remote URL, credentials included. It also
// pins how the DOLT_MERGE outcome is reported: a no-op merges nothing, a
// conflicted row sets the flag, Dolt's "Already up to date." error is
// swallowed, and a failed fetch comes back as fetchErr naming the tracking
// ref, before any merge runs.
func TestFetchAndMergeTrackingRefInTx(t *testing.T) {
	for _, tc := range []struct {
		name       string
		user       string
		fetchQuery string
		fetchArgs  []any
		// fetchFails makes the fetch statement error instead of return a row.
		fetchFails bool
		// mergeErr, when non-nil, is returned by the merge statement instead
		// of a row; mergeMessage/mergeConflicts build the row otherwise.
		mergeErr       error
		mergeMessage   string
		mergeConflicts int
		wantReported   bool
		wantMerged     bool
		wantConflicted bool
		wantFetchErr   string // non-empty: want a fetchErr containing this
	}{
		{name: "remote_user", user: "e2q8zs-user", fetchQuery: "CALL DOLT_FETCH('--user', ?, ?, ?)", fetchArgs: []any{"e2q8zs-user", "origin", "main"},
			mergeMessage: doltMergeSucceededMessage, wantReported: true, wantMerged: true},
		{name: "default", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			mergeMessage: doltMergeSucceededMessage, wantReported: true, wantMerged: true},
		{name: "no-op merges nothing", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			mergeMessage: "Everything up-to-date", wantReported: true},
		{name: "conflicted merge reports the flag", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			mergeMessage: "conflicts found", mergeConflicts: 1, wantReported: true, wantConflicted: true},
		{name: "already up to date error is swallowed", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			mergeErr: errors.New("Already up to date.")},
		{name: "fetch failure names the tracking ref", fetchQuery: "CALL DOLT_FETCH(?, ?)", fetchArgs: []any{"origin", "main"},
			fetchFails: true, wantFetchErr: "fetch from origin/main"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			defer db.Close()

			mock.ExpectBegin()
			fetch := mock.ExpectQuery(tc.fetchQuery).WithArgs(toDriverValues(tc.fetchArgs)...)
			if tc.fetchFails {
				fetch.WillReturnError(errors.New("no such remote"))
			} else {
				fetch.WillReturnRows(sqlmock.NewRows([]string{"success"}).AddRow(1))
				merge := mock.ExpectQuery("CALL DOLT_MERGE(?)").WithArgs("origin/main")
				if tc.mergeErr != nil {
					merge.WillReturnError(tc.mergeErr)
				} else {
					merge.WillReturnRows(sqlmock.NewRows([]string{"hash", "fast_forward", "conflicts", "message"}).
						AddRow("abc", 0, tc.mergeConflicts, tc.mergeMessage))
				}
			}

			store := &DoltStore{branch: "main"}
			ctx := context.Background()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			report, mergeErr, fetchErr := store.fetchAndMergeTrackingRefInTx(ctx, tx, "origin", tc.user)
			if mergeErr != nil {
				t.Fatalf("mergeErr = %v, want nil", mergeErr)
			}
			if tc.wantFetchErr != "" {
				if fetchErr == nil || !strings.Contains(fetchErr.Error(), tc.wantFetchErr) {
					t.Fatalf("fetchErr = %v, want an error containing %q", fetchErr, tc.wantFetchErr)
				}
			} else if fetchErr != nil {
				t.Fatalf("fetchErr = %v, want nil", fetchErr)
			}
			if report.Reported != tc.wantReported || report.Merged != tc.wantMerged || report.Conflicted != tc.wantConflicted {
				t.Errorf("report = %+v, want Reported=%v Merged=%v Conflicted=%v",
					report, tc.wantReported, tc.wantMerged, tc.wantConflicted)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("statements: %v", err)
			}
		})
	}
}

func toDriverValues(args []any) []driver.Value {
	out := make([]driver.Value, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}

// cliPullStore is a server-mode store whose CLI pull route runs the dolt on
// PATH in its served database directory, on a sqlmock pool that answers the
// route's remote listings with origin at remoteURL.
func cliPullStore(t *testing.T, mock sqlmock.Sqlmock, db *sql.DB, beadsDir, remoteURL string) *DoltStore {
	t.Helper()
	mock.MatchExpectationsInOrder(false)
	for i := 0; i < 8; i++ {
		mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
			WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("origin", remoteURL, nil))
	}
	store := &DoltStore{
		serverMode: true,
		beadsDir:   beadsDir,
		dbPath:     filepath.Join(beadsDir, "dolt"),
		database:   "beads",
		remote:     "origin",
		branch:     "main",
		db:         db,
		cfg:        &Config{ServerHost: "127.0.0.1", ServerPort: 3310, BeadsDir: beadsDir},
		// The SQL route's own connection fails at once, so a pull that left
		// the CLI route would fail instead of passing.
		connStr: unroutableConnStr,
	}
	serveDoltDatabase(t, store, store.dbPath, 3310)
	if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt", "noms"), 0o755); err != nil {
		t.Fatal(err)
	}
	return store
}

// expectCLIMerge expects the SQL merge the CLI pull route runs after its
// fetch, on the session cliMergeSession installed: pinStoreBranch reads the
// store's branch (main) from the pool and moves the session, which starts on
// the default branch, onto it; the session then sets the force variable and
// merges ref in a transaction it commits, answering with a row whose
// conflicts flag is conflicts, and is closed. The pool runs no merge
// statement.
func expectCLIMerge(pool, session sqlmock.Sqlmock, ref string, conflicts int) {
	pool.ExpectQuery("SELECT active_branch()").WillReturnRows(activeBranchRow("main"))
	session.ExpectQuery("SELECT active_branch()").WillReturnRows(activeBranchRow("default"))
	session.ExpectExec("CALL DOLT_CHECKOUT(?)").WithArgs("main").WillReturnResult(sqlmock.NewResult(0, 0))
	session.ExpectExec("SET @@dolt_force_transaction_commit = 1").WillReturnResult(sqlmock.NewResult(0, 0))
	session.ExpectBegin()
	session.ExpectQuery("CALL DOLT_MERGE(?)").WithArgs(ref).WillReturnRows(doltMergeRow(conflicts))
	session.ExpectCommit()
	session.ExpectClose()
}

// expectCleanCLIResolve expects finishCLIPull's resolver to find nothing to
// resolve or repair.
func expectCleanCLIResolve(mock sqlmock.Sqlmock) {
	mock.ExpectExec("SET @@dolt_allow_commit_conflicts = 1").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT `table`, num_conflicts FROM dolt_conflicts").
		WillReturnRows(sqlmock.NewRows([]string{"table", "num_conflicts"}))
	mock.ExpectQuery("SELECT `table` FROM dolt_constraint_violations WHERE num_violations > 0").
		WillReturnRows(sqlmock.NewRows([]string{"table"}))
	mock.ExpectRollback()
	mock.ExpectExec("SET @@dolt_allow_commit_conflicts = 0").WillReturnResult(sqlmock.NewResult(0, 0))
}

func readCallLog(t *testing.T, callLog string) string {
	t.Helper()
	raw, err := os.ReadFile(callLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

// TestCLIPullRoutesFetchThenMergeTrackingRef pins the CLI pull routes
// (bd-e2q8zs): a `dolt pull` subprocess forwards to the server's DOLT_PULL,
// whose merge message copies the stored remote URL, credentials included. The
// git-protocol and credential routes now run `dolt fetch [--user u] <remote>
// <branch>` with the same credential environment, then merge
// <remote>/<branch> themselves with CALL DOLT_MERGE on the merge's own
// session (see TestCLIPullMergeRunsOnAPinnedLongTimeoutSession), and
// finishCLIPull's resolver runs on the pool.
func TestCLIPullRoutesFetchThenMergeTrackingRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	clearPredicateEnv(t)
	clearCloudAuthEnv(t)

	for _, tc := range []struct {
		name      string
		remoteURL string
		user      string
		password  string
		// portVar names the store's port, which sends a credential route to
		// the CLI only for bd's own server.
		portVar   bool
		workspace func(*testing.T) string
		wantCall  string
	}{
		{
			name:      "git-protocol",
			remoteURL: "git+ssh://git@example.com/org/repo.git",
			workspace: unmanagedWorkspace,
			wantCall:  "|fetch origin main|DOLT_REMOTE_PASSWORD=",
		},
		{
			name:      "credential",
			remoteURL: "https://doltremoteapi.dolthub.com/org/repo",
			user:      "user",
			password:  "client-secret",
			portVar:   true,
			workspace: func(t *testing.T) string { return doltManagedWorkspace(t, 3310) },
			wantCall:  "|fetch --user user origin main|DOLT_REMOTE_PASSWORD=client-secret",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.portVar {
				t.Setenv("BEADS_DOLT_SERVER_PORT", "3310")
			}
			callLog := fakeDoltOnPath(t, tc.remoteURL)
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := cliPullStore(t, mock, db, tc.workspace(t), tc.remoteURL)
			store.remoteUser = tc.user
			store.remotePassword = tc.password
			session := cliMergeSession(t)
			expectCLIMerge(mock, session, "origin/main", 0)
			expectCleanCLIResolve(mock)

			if _, err := store.pullTransportReporting(context.Background(), "origin"); err != nil {
				t.Fatalf("pull: %v", err)
			}

			calls := readCallLog(t, callLog)
			cliDir, err := filepath.EvalSymlinks(store.CLIDir())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(calls, cliDir+tc.wantCall) {
				t.Errorf("dolt calls %q lack the fetch %q", calls, cliDir+tc.wantCall)
			}
			if strings.Contains(calls, "|pull ") {
				t.Errorf("the pull ran `dolt pull`, whose merge message records the remote URL:\n%s", calls)
			}
			if err := session.ExpectationsWereMet(); err != nil {
				t.Errorf("the CLI route did not merge origin/main on its own session: %v", err)
			}
			if err := expectationsMetBesideSpareListings(db, mock); err != nil {
				t.Errorf("the CLI route's pool statements: %v", err)
			}
		})
	}
}

// expectationsMetBesideSpareListings is mock.ExpectationsWereMet for a pool
// that cliPullStore over-provisioned with remote listings: it consumes the
// listings the route did not need, then reports any other unmet expectation.
func expectationsMetBesideSpareListings(db *sql.DB, mock sqlmock.Sqlmock) error {
	for i := 0; i < 8; i++ {
		if mock.ExpectationsWereMet() == nil {
			return nil
		}
		rows, err := db.Query("SELECT name, url, params FROM dolt_remotes")
		if err != nil {
			break
		}
		_ = rows.Close()
	}
	return mock.ExpectationsWereMet()
}

// TestCLIPeerPullFetchesThenMergesTrackingRef pins the federation peer CLI
// route (bd-e2q8zs): it runs `dolt fetch [--user <u>] <peer> <branch>` with
// today's credential environment, never `dolt pull`, and merges
// <peer>/<branch> itself with CALL DOLT_MERGE on the merge's own session.
func TestCLIPeerPullFetchesThenMergesTrackingRef(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	clearPredicateEnv(t)
	const peerURL = "git+ssh://git@example.com/org/peer.git"

	for _, tc := range []struct {
		name     string
		creds    *remoteCredentials
		wantCall string
	}{
		{"default", nil, "|fetch peer main|DOLT_REMOTE_PASSWORD="},
		{"credentialed", &remoteCredentials{username: "user", password: "client-secret"},
			"|fetch --user user peer main|DOLT_REMOTE_PASSWORD=client-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			callLog := fakeDoltOnPath(t, peerURL)
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			store := cliPullStore(t, mock, db, unmanagedWorkspace(t), peerURL)
			session := cliMergeSession(t)
			expectCLIMerge(mock, session, "peer/main", 0)

			if err := store.doltCLIPullFromPeer(context.Background(), "peer", tc.creds); err != nil {
				t.Fatalf("peer pull: %v", err)
			}

			calls := readCallLog(t, callLog)
			cliDir, err := filepath.EvalSymlinks(store.CLIDir())
			if err != nil {
				t.Fatal(err)
			}
			if want := cliDir + tc.wantCall; !strings.Contains(calls, want) {
				t.Errorf("dolt calls %q lack the peer fetch %q", calls, want)
			}
			if strings.Contains(calls, "|pull ") {
				t.Errorf("the peer pull ran `dolt pull`, whose merge message records the peer URL:\n%s", calls)
			}
			if err := session.ExpectationsWereMet(); err != nil {
				t.Errorf("the peer CLI route did not merge peer/main on its own session: %v", err)
			}
			if err := expectationsMetBesideSpareListings(db, mock); err != nil {
				t.Errorf("the peer CLI route's pool statements: %v", err)
			}
		})
	}
}

// TestCLIPullMergeConflictFailsThePull pins that a conflicted CLI-route merge
// fails the pull (bd-e2q8zs): when DOLT_MERGE reports conflicts the resolver
// does not settle, finishCLIPull returns the merge's conflicts error, as a
// conflicted `dolt pull` failed. Without it, finishCLIPull would report a pull
// that merged nothing as a success. What becomes of the declined merge is not
// pinned here: it stays open in the working set, as `dolt pull` left it, which
// is GH#7839, and #7849 rolls it back instead.
func TestCLIPullMergeConflictFailsThePull(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	session := cliMergeSession(t)
	expectCLIMerge(mock, session, "origin/main", 1)
	// The resolver finds nothing it may settle, so it rolls back untouched.
	expectCleanCLIResolve(mock)

	store := &DoltStore{db: db, branch: "main"}
	ctx := context.Background()
	err = store.finishCLIPull(ctx, store.mergeTrackingRefAfterCLIFetch(ctx, "origin/main"))
	if err == nil || !strings.Contains(err.Error(), "merge origin/main: conflicts found") {
		t.Fatalf("finishCLIPull after a conflicted merge = %v, want the merge's conflicts error", err)
	}
	if err := session.ExpectationsWereMet(); err != nil {
		t.Fatalf("merge session statements: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("pool statements: %v", err)
	}
}
