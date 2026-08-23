package dolt

import (
	"context"
	"os"
	"path/filepath"
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

	err := store.ensureMatchingCLIRemote("origin", "ftp://server/path")
	if err == nil {
		t.Fatal("expected invalid remote URL to be returned as an error")
	}
	for _, want := range []string{"origin", "ftp://server/path", "invalid remote URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should contain %q", err.Error(), want)
		}
	}
}

func TestServerOwnedRemoteCredentialsDisableCLIRouting(t *testing.T) {
	ctx := context.Background()
	store := &DoltStore{serverMode: true}
	creds := &remoteCredentials{username: "root", serverOwned: true}

	routes := []struct {
		name string
		run  func() (bool, error)
	}{
		{"credentials", func() (bool, error) {
			return store.prepareCLIRouteForCredentials(ctx, "origin", creds)
		}},
		{"cloud auth", func() (bool, error) {
			return store.prepareCLIRouteForCloudAuth(ctx, "origin", creds)
		}},
		{"local remote", func() (bool, error) {
			return store.shouldUseCLIForLocalRemoteWithError(ctx, "origin", creds)
		}},
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			useCLI, err := route.run()
			if err != nil {
				t.Fatalf("route returned error: %v", err)
			}
			if useCLI {
				t.Fatal("expected SQL routing when the server owns remote credentials")
			}
		})
	}
}

func TestServerOwnedRemoteCredentialsDerivedFromRemoteURL(t *testing.T) {
	t.Setenv("DOLT_REMOTE_USER", "permabot")
	t.Setenv("DOLT_REMOTE_PASSWORD", "stale-client-password")

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT name, url FROM dolt_remotes").
		WillReturnRows(sqlmock.NewRows([]string{"name", "url"}).
			AddRow("origin", "https://root@dolt.permanet.io:32551/permanet_pod"))

	store := &DoltStore{
		db:                    db,
		serverMode:            true,
		dbPath:                t.TempDir(),
		database:              "permanet_pod",
		remote:                "origin",
		serverOwnedRemoteBase: "https://root@dolt.permanet.io:32551",
	}
	if err := os.MkdirAll(store.CLIDir(), 0o755); err != nil {
		t.Fatalf("create stale CLI database directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(store.CLIDir(), ".dolt"), 0o755); err != nil {
		t.Fatalf("create stale CLI marker: %v", err)
	}

	creds, err := store.credentialsForRemote(context.Background(), "origin")
	if err != nil {
		t.Fatalf("credentialsForRemote: %v", err)
	}
	if creds == nil || creds.username != "root" {
		t.Fatalf("derived credentials = %#v, want username root", creds)
	}
	if creds.password != "" {
		t.Fatal("remote URL resolution must not synthesize a client-side password")
	}
	if !creds.ownedByServer() {
		t.Fatal("username-only HTTP remote on an external server should be server-owned")
	}

	useCLI, err := store.prepareCLIRouteForCredentials(context.Background(), "origin", creds)
	if err != nil {
		t.Fatalf("prepareCLIRouteForCredentials: %v", err)
	}
	if useCLI {
		t.Fatal("server-owned credentials must not route through the stale local Dolt CLI directory")
	}
	for name, route := range map[string]func() (bool, error){
		"cloud auth": func() (bool, error) {
			return store.prepareCLIRouteForCloudAuth(context.Background(), "origin", creds)
		},
		"local remote": func() (bool, error) {
			return store.shouldUseCLIForLocalRemoteWithError(context.Background(), "origin", creds)
		},
	} {
		useCLI, routeErr := route()
		if routeErr != nil {
			t.Fatalf("%s route: %v", name, routeErr)
		}
		if useCLI {
			t.Fatalf("%s route must remain on SQL when the server owns credentials", name)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestServerOwnedRemoteUsername(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		remoteURL string
		wantUser  string
		wantErr   bool
	}{
		{name: "trusted authenticated remotes API", baseURL: "https://root@dolt.example:32551", remoteURL: "https://root@dolt.example:32551/database", wantUser: "root"},
		{name: "different host cannot receive server password", baseURL: "https://root@dolt.example:32551", remoteURL: "https://root@evil.example:32551/database", wantErr: true},
		{name: "different user is rejected", baseURL: "https://root@dolt.example:32551", remoteURL: "https://permabot@dolt.example:32551/database", wantErr: true},
		{name: "password in remote is rejected", baseURL: "https://root@dolt.example:32551", remoteURL: "https://root:secret@dolt.example:32551/database", wantErr: true},
		{name: "password in base is rejected", baseURL: "https://root:secret@dolt.example:32551", remoteURL: "https://root@dolt.example:32551/database", wantErr: true},
		{name: "plain HTTP is rejected", baseURL: "https://root@dolt.example:32551", remoteURL: "http://root@dolt.example:32551/database", wantErr: true},
		{name: "git transport is rejected", baseURL: "https://root@dolt.example:32551", remoteURL: "git+https://root@dolt.example:32551/database", wantErr: true},
		{name: "missing database is rejected", baseURL: "https://root@dolt.example:32551", remoteURL: "https://root@dolt.example:32551", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotUser, err := serverOwnedRemoteUsername(tt.baseURL, tt.remoteURL)
			if (err != nil) != tt.wantErr {
				t.Fatalf("serverOwnedRemoteUsername(%q, %q) error = %v, wantErr %v", tt.baseURL, tt.remoteURL, err, tt.wantErr)
			}
			if gotUser != tt.wantUser {
				t.Fatalf("serverOwnedRemoteUsername(%q, %q) user = %q, want %q", tt.baseURL, tt.remoteURL, gotUser, tt.wantUser)
			}
		})
	}
}

func TestAuthenticatedFetchCall(t *testing.T) {
	query, args := authenticatedFetchCall("root", "origin", "main")
	if query != "CALL DOLT_FETCH('--user', ?, ?, ?)" {
		t.Fatalf("authenticated query = %q", query)
	}
	wantArgs := []any{"root", "origin", "main"}
	if len(args) != len(wantArgs) {
		t.Fatalf("authenticated args = %#v, want %#v", args, wantArgs)
	}
	for i := range wantArgs {
		if args[i] != wantArgs[i] {
			t.Fatalf("authenticated args = %#v, want %#v", args, wantArgs)
		}
	}

	query, args = authenticatedFetchCall("", "origin", "main")
	if query != "CALL DOLT_FETCH(?, ?)" || len(args) != 2 {
		t.Fatalf("anonymous fetch = %q %#v", query, args)
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
				return store.shouldUseCLIForLocalRemoteWithError(ctx, "origin", nil)
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

	newColdStore := func(t *testing.T) *DoltStore {
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
			mock.ExpectQuery("SELECT name, url FROM dolt_remotes").
				WillReturnRows(sqlmock.NewRows([]string{"name", "url"}))
		}
		store := &DoltStore{
			serverMode: true,
			dbPath:     t.TempDir(),
			database:   "testdb",
			remote:     "origin",
			db:         db,
		}
		initLocalDoltRepoForRemote(t, store.CLIDir())
		return store
	}

	t.Run("persisted_git_protocol_remote_recovers_cli_route", func(t *testing.T) {
		store := newColdStore(t)
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
		store := newColdStore(t)
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

	t.Run("nothing_persisted_keeps_sql_fallback", func(t *testing.T) {
		store := newColdStore(t)

		useCLI, err := store.prepareCLIRouteForGitProtocol(ctx, "origin")
		if err != nil {
			t.Fatalf("prepareCLIRouteForGitProtocol: %v", err)
		}
		if useCLI {
			t.Fatal("no remote anywhere should keep the SQL fallback, not invent a CLI route")
		}
	})
}
