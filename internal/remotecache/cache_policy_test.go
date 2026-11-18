package remotecache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
)

func setCachePolicy(t *testing.T, patterns ...string) {
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
	config.Set(AllowedRemotePatternsKey, patterns)
}

// seedWarmEntry makes a fresh cache entry for remoteURL whose clone has
// registered originURL as origin.
func seedWarmEntry(t *testing.T, c *Cache, remoteURL, originURL string) {
	t.Helper()
	doltDir := filepath.Join(c.cloneTarget(remoteURL), ".dolt")
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		t.Fatal(err)
	}
	state := `{"remotes":{"origin":{"name":"origin","url":"` + originURL + `"}}}`
	if err := os.WriteFile(filepath.Join(doltDir, "repo_state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	c.writeMeta(remoteURL, &CacheMeta{RemoteURL: remoteURL, LastPull: time.Now().UnixNano()})
}

// GH#6233: Ensure and Push refuse a remote the allowlist rejects before they
// create a cache entry or run dolt, and a warm entry whose clone registered a
// rejected origin is refused even when the requested URL is allowed.
func TestCacheEnforcesAllowedRemotePatterns(t *testing.T) {
	ctx := context.Background()
	allowed := "file://" + filepath.Join(t.TempDir(), "allowed")
	rejected := "file://" + filepath.Join(t.TempDir(), "rejected")
	setCachePolicy(t, allowed)

	t.Run("cold Ensure", func(t *testing.T) {
		c := &Cache{Dir: t.TempDir()}
		if _, err := c.Ensure(ctx, rejected); !errors.Is(err, ErrRemoteNotAllowed) {
			t.Fatalf("Ensure error = %v, want a policy refusal", err)
		}
		if entries, _ := os.ReadDir(c.Dir); len(entries) != 0 {
			t.Fatalf("cache dir holds %d entries after a refused Ensure, want none", len(entries))
		}
	})
	t.Run("Push", func(t *testing.T) {
		c := &Cache{Dir: t.TempDir()}
		if err := c.Push(ctx, rejected); !errors.Is(err, ErrRemoteNotAllowed) {
			t.Fatalf("Push error = %v, want a policy refusal", err)
		}
	})
	t.Run("warm entry with a rejected registered origin", func(t *testing.T) {
		c := &Cache{Dir: t.TempDir(), FreshFor: time.Hour}
		seedWarmEntry(t, c, allowed, rejected)
		if _, err := c.Ensure(ctx, allowed); !errors.Is(err, ErrRemoteNotAllowed) {
			t.Fatalf("Ensure error = %v, want the registered origin refused", err)
		}
		if err := c.Push(ctx, allowed); !errors.Is(err, ErrRemoteNotAllowed) {
			t.Fatalf("Push error = %v, want the registered origin refused", err)
		}
	})
	t.Run("warm entry with an allowed registered origin", func(t *testing.T) {
		c := &Cache{Dir: t.TempDir(), FreshFor: time.Hour}
		seedWarmEntry(t, c, allowed, allowed)
		if _, err := c.Ensure(ctx, allowed); err != nil {
			t.Fatalf("Ensure error = %v, want the fresh entry served", err)
		}
	})
}

// GH#6233: under a non-empty list a warm entry whose registered origin cannot
// be read refuses (fail closed) before the lock or any dolt run: an
// unverifiable destination is not authorization to dial it.
func TestCacheUnreadableWarmOriginFailsClosedUnderList(t *testing.T) {
	remote := "git+https://github.com/myorg/registry"
	setCachePolicy(t, "git+https://github.com/myorg/*")
	c := &Cache{Dir: t.TempDir(), FreshFor: time.Hour}

	doltDir := filepath.Join(c.cloneTarget(remote), ".dolt")
	if err := os.MkdirAll(doltDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(doltDir, "repo_state.json"), []byte(`{"remotes": not-json`), 0o600); err != nil {
		t.Fatal(err)
	}

	for name, run := range map[string]func() error{
		"Ensure": func() error { _, err := c.Ensure(context.Background(), remote); return err },
		"Push":   func() error { return c.Push(context.Background(), remote) },
	} {
		err := run()
		var policyErr *RemotePolicyError
		if !errors.As(err, &policyErr) || !errors.Is(err, ErrRemoteNotAllowed) {
			t.Fatalf("%s error = %v, want a fail-closed %s refusal on an unreadable origin", name, err, AllowedRemotePatternsKey)
		}
		if policyErr.Reason == "" {
			t.Errorf("%s refusal = %q, want the unreadable-origin reason, not a pattern mismatch", name, err)
		}
	}
	if _, statErr := os.Stat(c.lockPath(remote)); !os.IsNotExist(statErr) {
		t.Errorf("the cache lock was created for a refused remote (stat err = %v)", statErr)
	}
}
