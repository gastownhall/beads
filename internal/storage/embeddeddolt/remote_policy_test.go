//go:build cgo

package embeddeddolt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/types"
)

// setAllowedRemotePatterns installs an effective allowlist for one test on an
// isolated config (nothing read from the host's user or project config).
func setAllowedRemotePatterns(t *testing.T, patterns ...string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BEADS_DIR", filepath.Join(t.TempDir(), ".beads"))
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	if patterns == nil {
		patterns = []string{}
	}
	config.Set(remotecache.AllowedRemotePatternsKey, patterns)
}

// remoteBranchHash reads the tracking ref the last fetch recorded for
// remote/main, the evidence of what the remote holds.
func remoteBranchHash(t *testing.T, ctx context.Context, s *EmbeddedDoltStore, remote string) string {
	t.Helper()
	var hash string
	if err := s.withDBConn(ctx, func(db versioncontrolops.DBConn) error {
		return db.QueryRowContext(ctx, "SELECT hash FROM dolt_remote_branches WHERE name = ?", "remotes/"+remote+"/main").Scan(&hash)
	}); err != nil {
		t.Fatalf("read remotes/%s/main: %v", remote, err)
	}
	return hash
}

// newFileRemoteStore is an embedded store with two remotes, origin and team,
// on one real file:// remote that already holds the store's history.
func newFileRemoteStore(t *testing.T) (*EmbeddedDoltStore, string) {
	t.Helper()
	ctx := t.Context()
	store := newPeerAuthTestStore(t)
	if err := store.SetConfig(ctx, "issue_prefix", "fedauth"); err != nil {
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := store.Commit(ctx, "bd init"); err != nil {
		t.Fatalf("Commit(bd init): %v", err)
	}
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatal(err)
	}
	remoteURL := "file://" + remoteDir
	for _, name := range []string{defaultRemote, "team"} {
		if err := store.AddRemote(ctx, name, remoteURL); err != nil {
			t.Fatalf("AddRemote(%s): %v", name, err)
		}
	}
	if err := store.Push(ctx); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	return store, remoteURL
}

func createAndCommitIssue(t *testing.T, s *EmbeddedDoltStore, id string) {
	t.Helper()
	ctx := t.Context()
	if err := s.CreateIssue(ctx, &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}, "tester"); err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if err := s.Commit(ctx, "add "+id); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}

// GH#6233: with an allowlist that rejects a remote registered before the list
// was tightened, every embedded remote verb refuses with a policy error before
// it reaches Dolt, and the remote keeps the history it had.
func TestRemotePolicyRefusesEmbeddedTransfers(t *testing.T) {
	ctx := t.Context()
	setAllowedRemotePatterns(t)
	store, _ := newFileRemoteStore(t)
	seeded, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	createAndCommitIssue(t, store, "fedauth-local")

	config.Set(remotecache.AllowedRemotePatternsKey, []string{"dolthub://myorg/*"})

	verbs := []struct {
		name string
		call func() error
	}{
		{"Push", func() error { return store.Push(ctx) }},
		{"ForcePush", func() error { return store.ForcePush(ctx) }},
		{"PushRemote", func() error { return store.PushRemote(ctx, "team", false) }},
		{"PushRemote force", func() error { return store.PushRemote(ctx, "team", true) }},
		{"PushTo", func() error { return store.PushTo(ctx, "team") }},
		{"Pull", func() error { return store.Pull(ctx) }},
		{"PullWithStrategy", func() error { return store.PullWithStrategy(ctx, "ours") }},
		{"PullRemote", func() error { return store.PullRemote(ctx, "team") }},
		{"PullRemoteWithStrategy", func() error { return store.PullRemoteWithStrategy(ctx, "team", "ours") }},
		{"PullFrom", func() error { _, err := store.PullFrom(ctx, "team"); return err }},
		{"Fetch", func() error { return store.Fetch(ctx, "team") }},
		{"unknown remote", func() error { return store.PushRemote(ctx, "nosuch", false) }},
	}
	for _, v := range verbs {
		t.Run(v.name, func(t *testing.T) {
			err := v.call()
			if !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
				t.Fatalf("%s error = %v, want a %s refusal", v.name, err, remotecache.AllowedRemotePatternsKey)
			}
		})
	}

	// Lift the policy and fetch: the remote still holds only the seed.
	config.Set(remotecache.AllowedRemotePatternsKey, []string{})
	if err := store.Fetch(ctx, defaultRemote); err != nil {
		t.Fatalf("Fetch with no policy: %v", err)
	}
	if got := remoteBranchHash(t, ctx, store, defaultRemote); got != seeded {
		t.Fatalf("remote main = %s after refused pushes, want the seeded %s: a refused push transferred data", got, seeded)
	}
}

// The refusal is decided before a stored peer's credentials are resolved or
// installed into the process environment.
func TestRemotePolicyRefusesEmbeddedPeerBeforeCredentials(t *testing.T) {
	ctx := t.Context()
	setAllowedRemotePatterns(t)
	store := newPeerAuthTestStore(t)
	if err := store.AddFederationPeer(ctx, &storage.FederationPeer{
		Name: "team", RemoteURL: "https://peer.example/team", Username: "teamuser", Password: "teampass",
	}); err != nil {
		t.Fatalf("AddFederationPeer: %v", err)
	}
	config.Set(remotecache.AllowedRemotePatternsKey, []string{"git+https://allowed.example/*"})
	t.Setenv("DOLT_REMOTE_USER", "envuser")

	var got presentedAuth
	captureRemoteEntryPoints(t, &got)
	called := false
	err := store.withPeerAuth(ctx, "team", func(string) error { called = true; return nil })
	if !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
		t.Fatalf("withPeerAuth error = %v, want a policy refusal", err)
	}
	if called {
		t.Fatal("withPeerAuth dispatched the transfer callback for a refused peer")
	}
	if err := store.PullRemote(ctx, "team"); !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
		t.Fatalf("PullRemote error = %v, want a policy refusal", err)
	}
	if got.remote != "" {
		t.Fatalf("remote entry point reached for %q", got.remote)
	}
	if v := os.Getenv("DOLT_REMOTE_USER"); v != "envuser" {
		t.Fatalf("DOLT_REMOTE_USER = %q, want the ambient value untouched", v)
	}
}
