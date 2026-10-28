package dolt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/testutil"
)

func TestEnsureMatchingCLIRemoteSurfacesValidationErrors(t *testing.T) {
	store := &DoltStore{
		dbPath:   t.TempDir(),
		database: "beads",
	}

	err := store.ensureMatchingCLIRemote("origin", "ftp://server/path", "")
	if err == nil {
		t.Fatal("expected invalid remote URL to be returned as an error")
	}
	for _, want := range []string{"origin", "ftp://server/path", "invalid remote URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should contain %q", err.Error(), want)
		}
	}
}

func TestSQLCapableCLIRoutingFallsBackWhenCLIDirIsNotDoltRepo(t *testing.T) {
	ctx := context.Background()
	creds := &remoteCredentials{username: "user", password: "pass"}

	tests := []struct {
		name  string
		route func(*DoltStore) (bool, error)
	}{
		{
			name: "git protocol",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForGitProtocol(ctx, "origin")
			},
		},
		{
			name: "credential remote",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForCredentialsWithError(ctx, "origin", creds)
			},
		},
		{
			name: "cloud auth remote",
			route: func(store *DoltStore) (bool, error) {
				t.Setenv("AZURE_STORAGE_ACCOUNT", "account")
				return store.shouldUseCLIForCloudAuthWithError(ctx, "origin")
			},
		},
		{
			name: "local remote",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForLocalRemoteWithError(ctx, "origin")
			},
		},
		{
			name: "peer git protocol",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForPeerGitProtocol(ctx, "peer")
			},
		},
		{
			name: "peer credential remote",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForPeerCredentialsWithError(ctx, "peer", creds)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &DoltStore{
				serverMode: true,
				dbPath:     t.TempDir(),
				database:   "beads",
				remote:     "origin",
			}
			if err := os.MkdirAll(store.CLIDir(), 0o755); err != nil {
				t.Fatalf("create non-Dolt CLI dir: %v", err)
			}
			useCLI, err := tt.route(store)
			if err != nil {
				t.Fatalf("route returned error before SQL fallback: %v", err)
			}
			if useCLI {
				t.Fatal("expected SQL fallback when CLI directory is not an initialized Dolt repo")
			}
		})
	}
}

func TestWithCLIExecTimeoutAddsDeadline(t *testing.T) {
	ctx, cancel := withCLIExecTimeout(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected CLI exec context to have a deadline")
	}
	if until := time.Until(deadline); until <= 0 || until > cliExecTimeout {
		t.Fatalf("deadline is %s away, want within %s", until, cliExecTimeout)
	}
}

func TestCLIRoutingFallsBackToSQLWhenNoCLIDir(t *testing.T) {
	ctx := context.Background()
	creds := &remoteCredentials{username: "user", password: "pass"}

	tests := []struct {
		name  string
		route func(*DoltStore) (bool, error)
	}{
		{
			name: "git protocol",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForGitProtocol(ctx, "origin")
			},
		},
		{
			name: "credential remote",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForCredentialsWithError(ctx, "origin", creds)
			},
		},
		{
			name: "cloud auth remote",
			route: func(store *DoltStore) (bool, error) {
				t.Setenv("AZURE_STORAGE_ACCOUNT", "account")
				return store.shouldUseCLIForCloudAuthWithError(ctx, "origin")
			},
		},
		{
			name: "peer git protocol",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForPeerGitProtocol(ctx, "peer")
			},
		},
		{
			name: "peer credential remote",
			route: func(store *DoltStore) (bool, error) {
				return store.shouldUseCLIForPeerCredentialsWithError(ctx, "peer", creds)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &DoltStore{
				serverMode: true,
				dbPath:     "",
				database:   "beads",
				remote:     "origin",
			}
			useCLI, err := tt.route(store)
			if err != nil {
				t.Fatalf("route returned error before SQL fallback: %v", err)
			}
			if useCLI {
				t.Fatal("expected no CLI routing when no local CLI directory is configured")
			}
		})
	}
}

// TestPrepareCLIRouteForGitProtocolColdStartWindow pins the wy-6k7f7 recovery
// in the GH#2118 cold-start window: a freshly (auto-)started sql-server
// reports an EMPTY dolt_remotes even though the remote is persisted on disk
// in .dolt/repo_state.json. The route decider must consult the persisted
// enumeration instead of treating the empty listing as proof:
//   - a persisted git-protocol remote routes over the CLI (the push proceeds
//     — full recovery, the CLI transport never needed the SQL listing);
//   - a persisted non-git remote would need the SQL route the cold server
//     refuses, so the decider fails with the cold-start explanation instead
//     of letting DOLT_PUSH emit a bare "remote not found";
//   - nothing persisted keeps today's (false, nil) SQL fallback.
//
// Needs the dolt binary (real CLI repo for remote materialization checks)
// plus sqlmock for the empty server-side listing; no test server.
func TestPrepareCLIRouteForGitProtocolColdStartWindow(t *testing.T) {
	testutil.RequireDoltBinary(t)
	ctx := context.Background()

	newColdStore := func(t *testing.T, served bool) *DoltStore {
		t.Helper()
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		// Every ListRemotes in these scenarios sees the cold server's empty
		// dolt_remotes.
		mock.MatchExpectationsInOrder(false)
		for i := 0; i < 4; i++ {
			mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
				WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}))
		}
		store := &DoltStore{
			serverMode: true,
			dbPath:     t.TempDir(),
			database:   "testdb",
			remote:     "origin",
			db:         db,
			cfg:        &Config{ServerHost: "127.0.0.1", ServerPort: testServedPort},
		}
		clearServerPortEnv(t)
		// bd's own server serves the Dolt root on the dialed port. The
		// routes below run the dolt CLI there, so the test stands in for
		// that server's proof.
		if served {
			standInForServedDatabase(t, store.CLIDir(), testServedPort)
		}
		initLocalDoltRepoForRemote(t, store.CLIDir())
		return store
	}

	t.Run("persisted_git_protocol_remote_recovers_cli_route", func(t *testing.T) {
		store := newColdStore(t, true)
		const url = "git+ssh://git@example.com/org/repo.git"
		if err := doltutil.AddCLIRemote(store.CLIDir(), "origin", url); err != nil {
			t.Fatalf("AddCLIRemote: %v", err)
		}

		useCLI, err := store.prepareCLIRouteForGitProtocol(ctx, "origin")
		if err != nil {
			t.Fatalf("prepareCLIRouteForGitProtocol: %v", err)
		}
		if !useCLI {
			t.Fatal("persisted git-protocol remote should recover the CLI route in the cold-start window")
		}
	})

	t.Run("persisted_non_git_remote_fails_with_cold_start_hint", func(t *testing.T) {
		store := newColdStore(t, true)
		if err := doltutil.AddCLIRemote(store.CLIDir(), "origin", "https://doltremoteapi.dolthub.com/org/repo"); err != nil {
			t.Fatalf("AddCLIRemote: %v", err)
		}

		_, err := store.prepareCLIRouteForGitProtocol(ctx, "origin")
		if err == nil {
			t.Fatal("persisted non-git remote in the window should fail with the cold-start explanation, not fall to a bare SQL 'remote not found'")
		}
		for _, want := range []string{"GH#2118", "persisted on disk"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q should contain %q", err.Error(), want)
			}
		}
	})

	// A copy no server on the dialed port serves is not the server's data,
	// even at 127.0.0.1: its remotes neither recover the CLI route, which
	// would push the copy, nor explain a cold start the server is not in.
	t.Run("persisted_remote_in_an_unserved_copy_keeps_sql_fallback", func(t *testing.T) {
		for _, url := range []string{"git+ssh://git@example.com/org/repo.git", "https://doltremoteapi.dolthub.com/org/repo"} {
			store := newColdStore(t, false)
			if err := doltutil.AddCLIRemote(store.CLIDir(), "origin", url); err != nil {
				t.Fatalf("AddCLIRemote: %v", err)
			}
			useCLI, err := store.prepareCLIRouteForGitProtocol(ctx, "origin")
			if err != nil || useCLI {
				t.Fatalf("prepareCLIRouteForGitProtocol(%s) = %v, %v; want the SQL fallback (false, nil) for a copy nothing serves", url, useCLI, err)
			}
		}
	})

	t.Run("nothing_persisted_keeps_sql_fallback", func(t *testing.T) {
		store := newColdStore(t, true)

		useCLI, err := store.prepareCLIRouteForGitProtocol(ctx, "origin")
		if err != nil {
			t.Fatalf("prepareCLIRouteForGitProtocol: %v", err)
		}
		if useCLI {
			t.Fatal("no remote anywhere should keep the SQL fallback, not invent a CLI route")
		}
	})
}

// clearServerPortEnv blanks BEADS_DOLT_SERVER_PORT and BEADS_DOLT_PORT for
// the rest of the test. With either set, no Dolt database in a store's CLIDir
// counts as its server's (cliDirServesServer), and in the Dolt lane the
// package's TestMain exports both for the suite's own server, so a test of
// the CLI routes for a server bd runs clears them first.
func clearServerPortEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")
}

// leftoverRemoteRows lists, as the server's dolt_remotes does, the remotes
// the CLI-route tests below route: a git-protocol default remote, a
// git-protocol federation peer, and an Azure remote for the cloud-auth route.
func leftoverRemoteRows(gitURL, azURL string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"name", "url", "params"}).
		AddRow("origin", gitURL, nil).
		AddRow("peer", gitURL, nil).
		AddRow("cloud", azURL, nil)
}

// TestCLIRoutesUseALeftoverDatabaseOnlyWhereBdRunsTheServer pins every CLI
// route of push, pull and fetch (default remote and federation peer) to the
// server bd may run. CLIDir holds a Dolt database whose remotes match the
// server's, as a clone bd init left in .beads/dolt/<db> does. That database
// is the server's only for a plain TCP server where bd runs its server
// (configfile.BdRunsServerAt) and the environment names no port, or names
// the port bd's state files show its own live dolt sql-server for the
// workspace on; there every route keeps taking the dolt CLI. At any other
// host, for a gateway, a TLS endpoint or a unix socket, or for another port
// BEADS_DOLT_SERVER_PORT or BEADS_DOLT_PORT names, including one where the
// recorded PID is now a process that is not dolt, it is at best a stale
// copy, and pushing it would report success without the server's commits,
// so every route falls back to SQL, which the server runs on its own data.
func TestCLIRoutesUseALeftoverDatabaseOnlyWhereBdRunsTheServer(t *testing.T) {
	testutil.RequireDoltBinary(t)
	clearPredicateEnv(t)
	clearCloudAuthEnv(t)
	t.Setenv("AZURE_STORAGE_ACCOUNT", "account")
	ctx := context.Background()
	creds := &remoteCredentials{username: "user", password: "pass"}
	const gitURL = "git+ssh://git@example.com/org/repo.git"
	const azURL = "az://account.blob.core.windows.net/container"

	routes := []struct {
		name  string
		route func(*DoltStore) (bool, error)
	}{
		{"git protocol", func(s *DoltStore) (bool, error) { return s.prepareCLIRouteForGitProtocol(ctx, "origin") }},
		{"credential remote", func(s *DoltStore) (bool, error) { return s.prepareCLIRouteForCredentials(ctx, "origin", creds) }},
		{"cloud auth remote", func(s *DoltStore) (bool, error) { return s.prepareCLIRouteForCloudAuth(ctx, "cloud") }},
		{"local remote", func(s *DoltStore) (bool, error) { return s.shouldUseCLIForLocalRemoteWithError(ctx, "origin") }},
		{"peer git protocol", func(s *DoltStore) (bool, error) { return s.prepareCLIRouteForPeerGitProtocol(ctx, "peer") }},
		{"peer credential remote", func(s *DoltStore) (bool, error) { return s.prepareCLIRouteForPeerCredentials(ctx, "peer", creds) }},
	}

	for _, tc := range []struct {
		name    string
		cfg     Config
		portVar string
		// bdServerOnPort, when set, is the port bd's state files for the
		// workspace show its own live server on; notDolt makes the process
		// they record one that is not a dolt sql-server.
		bdServerOnPort int
		notDolt        bool
		// The Dolt root is served by a live server on the dialed port
		// unless unserved is set, or servedOnPort names another port.
		unserved     bool
		servedOnPort int
		wantCLI      bool
	}{
		{name: "host=127.0.0.1", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, wantCLI: true},
		{name: "host=127.0.0.1/a copy nothing serves", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, unserved: true},
		{name: "host=127.0.0.1/served on another port", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, servedOnPort: 3311},
		{name: "host=127.0.0.1/BEADS_DOLT_SERVER_PORT/bd server on that port, directory not served", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, portVar: "BEADS_DOLT_SERVER_PORT", bdServerOnPort: 3310, unserved: true},
		{name: "host=127.0.0.1/BEADS_DOLT_SERVER_PORT", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, portVar: "BEADS_DOLT_SERVER_PORT"},
		{name: "host=127.0.0.1/BEADS_DOLT_PORT", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, portVar: "BEADS_DOLT_PORT"},
		{name: "host=127.0.0.1/BEADS_DOLT_SERVER_PORT/bd server on that port", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, portVar: "BEADS_DOLT_SERVER_PORT", bdServerOnPort: 3310, wantCLI: true},
		{name: "host=127.0.0.1/BEADS_DOLT_PORT/bd server on that port", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, portVar: "BEADS_DOLT_PORT", bdServerOnPort: 3310, wantCLI: true},
		{name: "host=127.0.0.1/BEADS_DOLT_SERVER_PORT/bd server on another port", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, portVar: "BEADS_DOLT_SERVER_PORT", bdServerOnPort: 3311},
		{name: "host=127.0.0.1/BEADS_DOLT_SERVER_PORT/a process that is not dolt on that port", cfg: Config{ServerHost: "127.0.0.1", ServerPort: 3310}, portVar: "BEADS_DOLT_SERVER_PORT", bdServerOnPort: 3310, notDolt: true},
		{name: "host=127.0.0.1/gateway", cfg: Config{ServerHost: "127.0.0.1", Gateway: true}},
		{name: "host=127.0.0.1/TLS", cfg: Config{ServerHost: "127.0.0.1", ServerTLS: true}},
		{name: "host=127.0.0.1/unix socket", cfg: Config{ServerHost: "127.0.0.1", ServerSocket: "/tmp/dolt.sock"}},
		{name: "host=::1", cfg: Config{ServerHost: "::1"}},
		{name: "host=127.0.0.2", cfg: Config{ServerHost: "127.0.0.2"}},
		{name: "host=db.example.com", cfg: Config{ServerHost: "db.example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.portVar != "" {
				t.Setenv(tc.portVar, "3310")
			}
			beadsDir := unmanagedWorkspace(t)
			switch {
			case tc.notDolt:
				beadsDir = managedWorkspace(t, tc.bdServerOnPort)
			case tc.bdServerOnPort > 0:
				beadsDir = doltManagedWorkspace(t, tc.bdServerOnPort)
			}
			dbPath := filepath.Join(beadsDir, "dolt")
			cliDir := filepath.Join(dbPath, "beads")
			if !tc.unserved {
				port := tc.cfg.ServerPort
				if tc.servedOnPort != 0 {
					port = tc.servedOnPort
				}
				// The routes run the dolt CLI in cliDir, so the test stands
				// in for the proof that a server on port serves it.
				standInForServedDatabase(t, cliDir, port)
			}
			initLocalDoltRepoForRemote(t, cliDir)
			for name, url := range map[string]string{"origin": gitURL, "peer": gitURL, "cloud": azURL} {
				if err := doltutil.AddCLIRemote(cliDir, name, url); err != nil {
					t.Fatalf("AddCLIRemote(%s): %v", name, err)
				}
			}

			for _, tt := range routes {
				t.Run(tt.name, func(t *testing.T) {
					db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
					if err != nil {
						t.Fatalf("sqlmock: %v", err)
					}
					t.Cleanup(func() { _ = db.Close() })
					mock.MatchExpectationsInOrder(false)
					for i := 0; i < 3; i++ {
						mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
							WillReturnRows(leftoverRemoteRows(gitURL, azURL))
					}
					cfg := tc.cfg
					store := &DoltStore{
						serverMode: true,
						beadsDir:   beadsDir,
						dbPath:     dbPath,
						database:   "beads",
						remote:     "origin",
						db:         db,
						cfg:        &cfg,
					}

					useCLI, err := tt.route(store)
					if err != nil {
						t.Fatalf("route returned error: %v", err)
					}
					if useCLI != tc.wantCLI {
						t.Fatalf("route through the dolt CLI in %s = %v for %s, want %v (only bd's own server serves that directory)", cliDir, useCLI, tc.name, tc.wantCLI)
					}
				})
			}
		})
	}
}

// fakeDoltOnPath puts a dolt on PATH that appends "<dir>|<args>|<the
// DOLT_REMOTE_PASSWORD it got>" to the returned log for every call, and
// answers `dolt remote -v` with origin at remoteURL.
func fakeDoltOnPath(t *testing.T, remoteURL string) string {
	t.Helper()
	binDir := t.TempDir()
	callLog := filepath.Join(binDir, "calls.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$(pwd -P)|$*|DOLT_REMOTE_PASSWORD=${DOLT_REMOTE_PASSWORD:-}\" >> '%s'\nif [ \"$1 $2\" = \"remote -v\" ]; then echo 'origin %s'; fi\nexit 0\n", callLog, remoteURL)
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(script), 0o755); err != nil { // #nosec G306 -- test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog
}

// TestPushAtAHostBdDoesNotRunRunsNoDoltInTheLeftoverDatabase is the push end
// of that gate, with a dolt on PATH that records every call. A server-mode
// store at the IPv6 loopback whose .beads/dolt/<db> holds a Dolt database
// with the git-protocol remote runs no dolt there (no fsck, no push, no
// remote listing), forced or not, and pushes with CALL DOLT_PUSH on the
// server instead. So does the same store at 127.0.0.1 for a gateway, or when
// BEADS_DOLT_SERVER_PORT or BEADS_DOLT_PORT names its port, unless that is
// the port bd's state files show its own live dolt sql-server for the
// workspace on: a recorded PID that is now a process that is not dolt does
// not count. Otherwise the store at 127.0.0.1 keeps fsck-ing and pushing that
// directory through the dolt CLI.
func TestPushAtAHostBdDoesNotRunRunsNoDoltInTheLeftoverDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	clearPredicateEnv(t)
	const gitURL = "git+ssh://git@example.com/org/repo.git"
	callLog := fakeDoltOnPath(t, gitURL)

	for _, tc := range []struct {
		host           string
		portVar        string
		gateway        bool
		bdServerOnPort bool
		// notDolt makes the process bd's state files record on that port
		// one that is not a dolt sql-server.
		notDolt bool
		// unserved leaves the Dolt root without a live server on the
		// dialed port serving it, as for a copy.
		unserved bool
		force    bool
		wantCLI  bool
	}{
		{host: "127.0.0.1", wantCLI: true},
		{host: "127.0.0.1", force: true, wantCLI: true},
		{host: "127.0.0.1", unserved: true},
		{host: "127.0.0.1", unserved: true, force: true},
		{host: "127.0.0.1", portVar: "BEADS_DOLT_SERVER_PORT"},
		{host: "127.0.0.1", portVar: "BEADS_DOLT_PORT", force: true},
		{host: "127.0.0.1", portVar: "BEADS_DOLT_SERVER_PORT", bdServerOnPort: true, wantCLI: true},
		{host: "127.0.0.1", portVar: "BEADS_DOLT_SERVER_PORT", bdServerOnPort: true, notDolt: true},
		{host: "127.0.0.1", gateway: true},
		{host: "::1"},
		{host: "::1", force: true},
	} {
		name := fmt.Sprintf("host=%s/force=%v", tc.host, tc.force)
		if tc.portVar != "" {
			name += "/" + tc.portVar
		}
		if tc.bdServerOnPort {
			name += "/bd server on that port"
		}
		if tc.notDolt {
			name += "/not dolt"
		}
		if tc.gateway {
			name += "/gateway"
		}
		if tc.unserved {
			name += "/a copy nothing serves"
		}
		t.Run(name, func(t *testing.T) {
			if tc.portVar != "" {
				t.Setenv(tc.portVar, "3310")
			}
			beadsDir := unmanagedWorkspace(t)
			switch {
			case tc.notDolt:
				beadsDir = managedWorkspace(t, 3310)
			case tc.bdServerOnPort:
				beadsDir = doltManagedWorkspace(t, 3310)
			}
			_ = os.Remove(callLog)
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.MatchExpectationsInOrder(false)
			for i := 0; i < 8; i++ {
				mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
					WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("origin", gitURL, nil))
			}
			store := &DoltStore{
				serverMode: true,
				beadsDir:   beadsDir,
				dbPath:     filepath.Join(beadsDir, "dolt"),
				database:   "beads",
				remote:     "origin",
				branch:     "main",
				db:         db,
				cfg:        &Config{ServerHost: tc.host, ServerPort: 3310, Gateway: tc.gateway},
				// The SQL route's own connection fails at once, which is how
				// this test tells it was taken.
				connStr: unroutableConnStr,
			}
			if !tc.unserved {
				serveDoltDatabase(t, store, store.dbPath, 3310)
			}
			if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt", "noms"), 0o755); err != nil {
				t.Fatal(err)
			}

			push := store.Push
			if tc.force {
				push = store.ForcePush
			}
			pushErr := push(context.Background())

			raw, err := os.ReadFile(callLog)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			calls := strings.TrimSpace(string(raw))
			if !tc.wantCLI {
				if calls != "" {
					t.Fatalf("push (%s) ran dolt in the leftover database:\n%s", name, calls)
				}
				if pushErr == nil || !strings.Contains(pushErr.Error(), "push to origin/main") {
					t.Fatalf("push (%s) = %v, want the SQL route's CALL DOLT_PUSH failure", name, pushErr)
				}
				return
			}
			if pushErr != nil {
				t.Fatalf("push (%s): %v", name, pushErr)
			}
			cliDir, err := filepath.EvalSymlinks(store.CLIDir())
			if err != nil {
				t.Fatal(err)
			}
			want := cliDir + "|push origin main"
			if tc.force {
				want = cliDir + "|push --force origin main"
			}
			for _, w := range []string{cliDir + "|fsck --quiet", want} {
				if !strings.Contains(calls, w) {
					t.Fatalf("push (%s): dolt calls %q lack %q", name, calls, w)
				}
			}
		})
	}
}

// TestCredentialPushOnAnEnvNamedPortKeepsTheCLIOnlyForBdsOwnServer pins the
// credential route of a push to a Hosted Dolt remote whose user and
// DOLT_REMOTE_PASSWORD only this bd holds, from a store at 127.0.0.1 whose
// port BEADS_DOLT_SERVER_PORT names. Where bd's state files show its own live
// dolt sql-server for the workspace on that port, .beads/dolt/<db> is that
// server's database: the push stays on the dolt CLI there, and the dolt child
// gets the password. Otherwise, as when the recorded PID is now a process
// that is not dolt, the port may be any server's, so the push runs CALL
// DOLT_PUSH on it, which takes the password from the server process's own
// environment, and its failure says so.
func TestCredentialPushOnAnEnvNamedPortKeepsTheCLIOnlyForBdsOwnServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fake")
	}
	clearPredicateEnv(t)
	clearCloudAuthEnv(t)
	const hostedURL = "https://doltremoteapi.dolthub.com/org/repo"
	callLog := fakeDoltOnPath(t, hostedURL)
	t.Setenv("BEADS_DOLT_SERVER_PORT", "3310")

	for _, tc := range []struct {
		name string
		// workspace makes the workspace whose server state files the store
		// reads.
		workspace func(*testing.T) string
		wantCLI   bool
	}{
		{"bd server on that port", func(t *testing.T) string { return doltManagedWorkspace(t, 3310) }, true},
		{"no server bd runs", unmanagedWorkspace, false},
		{"a process that is not dolt on that port", func(t *testing.T) string { return managedWorkspace(t, 3310) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beadsDir := tc.workspace(t)
			_ = os.Remove(callLog)
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			if err != nil {
				t.Fatalf("sqlmock: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.MatchExpectationsInOrder(false)
			for i := 0; i < 8; i++ {
				mock.ExpectQuery("SELECT name, url, params FROM dolt_remotes").
					WillReturnRows(sqlmock.NewRows([]string{"name", "url", "params"}).AddRow("origin", hostedURL, nil))
			}
			store := &DoltStore{
				serverMode:     true,
				beadsDir:       beadsDir,
				dbPath:         filepath.Join(beadsDir, "dolt"),
				database:       "beads",
				remote:         "origin",
				branch:         "main",
				remoteUser:     "user",
				remotePassword: "client-secret",
				db:             db,
				cfg:            &Config{ServerHost: "127.0.0.1", ServerPort: 3310, BeadsDir: beadsDir},
				connStr:        unroutableConnStr,
			}
			serveDoltDatabase(t, store, store.dbPath, 3310)
			if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt", "noms"), 0o755); err != nil {
				t.Fatal(err)
			}

			pushErr := store.Push(context.Background())

			raw, err := os.ReadFile(callLog)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			calls := strings.TrimSpace(string(raw))
			if !tc.wantCLI {
				if calls != "" {
					t.Fatalf("push to a server the variable names ran dolt in the leftover database:\n%s", calls)
				}
				const note = "CALL DOLT_PUSH runs in the Dolt server's process, which takes DOLT_REMOTE_PASSWORD from its own environment, not from bd's"
				if pushErr == nil || !strings.Contains(pushErr.Error(), "push to origin/main") || !strings.Contains(pushErr.Error(), note) {
					t.Fatalf("push = %v, want the SQL route's CALL DOLT_PUSH failure naming where the server takes the password from (%q)", pushErr, note)
				}
				return
			}
			if pushErr != nil {
				t.Fatalf("push to bd's own server: %v", pushErr)
			}
			cliDir, err := filepath.EvalSymlinks(store.CLIDir())
			if err != nil {
				t.Fatal(err)
			}
			if want := cliDir + "|push --user user origin main|DOLT_REMOTE_PASSWORD=client-secret"; !strings.Contains(calls, want) {
				t.Fatalf("dolt calls %q lack the credential push %q", calls, want)
			}
		})
	}
}
