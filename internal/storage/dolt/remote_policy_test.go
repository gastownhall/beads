package dolt

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remotecache"
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

// newFileRemoteServerStore is a server-mode store with remotes origin and team
// on one real file:// remote that already holds the store's history, which it
// returns as the seeded commit.
func newFileRemoteServerStore(t *testing.T, ctx context.Context) (*DoltStore, string) {
	t.Helper()
	tmpDir := t.TempDir()
	store, err := New(ctx, &Config{
		Path:            tmpDir,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		Database:        uniqueTestDBName(t),
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec("CREATE TABLE policy_marker (id INT PRIMARY KEY)")
	mustExec("CALL DOLT_ADD('-A')")
	mustExec("CALL DOLT_COMMIT('-m', 'test: policy marker')")
	remoteURL := "file://" + filepath.Join(tmpDir, "policy-remote")
	mustExec("CALL DOLT_REMOTE('add', 'origin', ?)", remoteURL)
	mustExec("CALL DOLT_REMOTE('add', 'team', ?)", remoteURL)
	store.remote = "origin"
	store.branch = "main"
	if err := store.Push(ctx); err != nil {
		t.Fatalf("seed push: %v", err)
	}
	seeded, err := store.GetCurrentCommit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return store, seeded
}

func remoteMainHash(t *testing.T, ctx context.Context, s *DoltStore, remote string) string {
	t.Helper()
	var hash string
	if err := s.db.QueryRowContext(ctx, "SELECT hash FROM dolt_remote_branches WHERE name = ?", "remotes/"+remote+"/main").Scan(&hash); err != nil {
		t.Fatalf("read remotes/%s/main: %v", remote, err)
	}
	return hash
}

// GH#6233: with an allowlist that rejects a remote registered before the list
// was tightened, every server-mode transfer refuses with a policy error before
// transport, the remote keeps the history it had, and the pull entry points
// refuse before their pre-pull commit.
func TestRemotePolicyRefusesServerTransfers(t *testing.T) {
	skipIfNoDolt(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	setAllowedRemotePatterns(t)
	store, seeded := newFileRemoteServerStore(t, ctx)

	if _, err := store.db.ExecContext(ctx, "INSERT INTO policy_marker VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'test: local only')"); err != nil {
		t.Fatal(err)
	}
	// Leave a pending user kv row, which the pre-pull commit stages: a pull
	// that ran it would leave an "auto-commit before pull" commit.
	if _, err := store.db.ExecContext(ctx, "REPLACE INTO config (`key`, value) VALUES ('kv.policy-test', 'pending')"); err != nil {
		t.Fatal(err)
	}

	config.Set(remotecache.AllowedRemotePatternsKey, []string{"dolthub://myorg/*"})

	verbs := []struct {
		name string
		call func() error
	}{
		{"Push", func() error { return store.Push(ctx) }},
		{"ForcePush", func() error { return store.ForcePush(ctx) }},
		{"PushRemote", func() error { return store.PushRemote(ctx, "team", false) }},
		{"PushTo", func() error { return store.PushTo(ctx, "team") }},
		{"Pull", func() error { return store.Pull(ctx) }},
		{"PullRemote", func() error { return store.PullRemote(ctx, "team") }},
		{"PullFrom", func() error { _, err := store.PullFrom(ctx, "team"); return err }},
		{"Fetch", func() error { return store.Fetch(ctx, "team") }},
		{"Sync", func() error { _, err := store.Sync(ctx, "team", "ours"); return err }},
		{"unknown remote", func() error { return store.PushRemote(ctx, "nosuch", false) }},
	}
	for _, v := range verbs {
		t.Run(v.name, func(t *testing.T) {
			if err := v.call(); !errors.Is(err, remotecache.ErrRemoteNotAllowed) {
				t.Fatalf("%s error = %v, want a %s refusal", v.name, err, remotecache.AllowedRemotePatternsKey)
			}
		})
	}
	var prePull int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_log WHERE message LIKE 'auto-commit before %'").Scan(&prePull); err != nil {
		t.Fatal(err)
	}
	if prePull != 0 {
		t.Errorf("%d pre-pull auto-commits after refused pulls, want 0: a refused pull committed local state", prePull)
	}

	// Lift the policy and fetch: the remote still holds only the seed.
	config.Set(remotecache.AllowedRemotePatternsKey, []string{})
	if err := store.Fetch(ctx, "origin"); err != nil {
		t.Fatalf("Fetch with no policy: %v", err)
	}
	if got := remoteMainHash(t, ctx, store, "origin"); got != seeded {
		t.Fatalf("remote main = %s after refused pushes, want the seeded %s: a refused push transferred data", got, seeded)
	}
}

// GH#6233: a pattern that accepts the remote's URL (exact, or a glob over its
// parent directory) lets every server-mode transfer run as it would with no
// list.
func TestRemotePolicyAcceptsMatchingServerRemote(t *testing.T) {
	skipIfNoDolt(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	setAllowedRemotePatterns(t)
	store, _ := newFileRemoteServerStore(t, ctx)
	remotes, err := store.ListRemotes(ctx)
	if err != nil {
		t.Fatalf("ListRemotes: %v", err)
	}
	var remoteURL string
	for _, r := range remotes {
		if r.Name == "origin" {
			remoteURL = r.URL
		}
	}
	if remoteURL == "" {
		t.Fatal("fixture: origin is not registered")
	}

	for i, patterns := range [][]string{
		{remoteURL},
		{"dolthub://myorg/*", remoteURL[:strings.LastIndex(remoteURL, "/")] + "/*"},
	} {
		config.Set(remotecache.AllowedRemotePatternsKey, patterns)
		if _, err := store.db.ExecContext(ctx, "INSERT INTO policy_marker VALUES (?)", 10+i); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, "CALL DOLT_COMMIT('-Am', 'test: accepted')"); err != nil {
			t.Fatal(err)
		}
		head, err := store.GetCurrentCommit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Push(ctx); err != nil {
			t.Fatalf("patterns %d: Push: %v", i, err)
		}
		if err := store.PushTo(ctx, "team"); err != nil {
			t.Fatalf("patterns %d: PushTo: %v", i, err)
		}
		if err := store.Fetch(ctx, "origin"); err != nil {
			t.Fatalf("patterns %d: Fetch: %v", i, err)
		}
		if got := remoteMainHash(t, ctx, store, "origin"); got != head {
			t.Fatalf("patterns %d: remote main = %s, want the pushed %s", i, got, head)
		}
		if err := store.Pull(ctx); err != nil {
			t.Fatalf("patterns %d: Pull: %v", i, err)
		}
		if _, err := store.PullFrom(ctx, "team"); err != nil {
			t.Fatalf("patterns %d: PullFrom: %v", i, err)
		}
	}
}
