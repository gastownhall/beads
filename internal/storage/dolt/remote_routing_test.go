package dolt

import (
	"context"
	"os"
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
