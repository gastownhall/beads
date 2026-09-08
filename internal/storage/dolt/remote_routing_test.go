package dolt

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
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
