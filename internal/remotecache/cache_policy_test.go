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

// setAllowedRemotePatternsForTest installs federation.allowed-remote-patterns
// for one test on a freshly initialized config, restoring the process config
// on cleanup.
func setAllowedRemotePatternsForTest(t *testing.T, patterns []string) {
	t.Helper()
	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	config.Set("federation.allowed-remote-patterns", patterns)
}

// policyRefusal reports whether err is the allowlist's refusal.
func policyRefusal(err error) bool {
	var policyErr *RemotePolicyError
	return errors.As(err, &policyErr)
}

// GH#6233: a committed config.yaml reaches Cache.Ensure through routing.*
// (bd create) and repos.additional (bd repo sync), so an out-of-policy
// remote must be refused before the cache entry — or anything else — is
// created.
func TestCacheEnsureRefusedByAllowedRemotePatternsCreatesNothing(t *testing.T) {
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})
	cache := &Cache{Dir: t.TempDir()}
	remote := "https://evil.example.com/attacker/repo.git"

	_, err := cache.Ensure(context.Background(), remote)
	if !policyRefusal(err) {
		t.Fatalf("Ensure(%q) error = %v, want *RemotePolicyError", remote, err)
	}
	if _, statErr := os.Stat(cache.entryDir(remote)); !os.IsNotExist(statErr) {
		t.Errorf("Ensure created the cache entry for a refused remote (stat err = %v)", statErr)
	}
}

// The warm-origin half: the URL argument matches the list, but the cached
// clone's own recorded origin — what dolt pull/push dials — does not. Both
// Ensure and Push must refuse before acquiring the lock; a cache populated
// before the list tightened is not grandfathered.
func TestCacheWarmOriginRefusedByAllowedRemotePatterns(t *testing.T) {
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})
	cache := &Cache{Dir: t.TempDir()}
	remote := "git+https://github.com/myorg/registry"

	target := cache.cloneTarget(remote)
	if err := os.MkdirAll(filepath.Join(target, ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	state := `{"remotes":{"origin":{"url":"https://evil.example.com/attacker/registry"}}}`
	if err := os.WriteFile(filepath.Join(target, ".dolt", "repo_state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := cache.Ensure(context.Background(), remote); !policyRefusal(err) {
		t.Fatalf("Ensure warm error = %v, want the recorded origin refused", err)
	}
	if err := cache.Push(context.Background(), remote); !policyRefusal(err) {
		t.Fatalf("Push warm error = %v, want the recorded origin refused", err)
	}
	if _, statErr := os.Stat(cache.lockPath(remote)); !os.IsNotExist(statErr) {
		t.Errorf("the cache lock was created for a refused remote (stat err = %v)", statErr)
	}
}

// Under a non-empty list a warm entry whose origin cannot be read refuses
// (fail closed): an unverifiable destination is not authorization to dial it.
func TestCacheUnreadableWarmOriginFailsClosedUnderList(t *testing.T) {
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})
	cache := &Cache{Dir: t.TempDir()}
	remote := "git+https://github.com/myorg/registry"

	target := cache.cloneTarget(remote)
	if err := os.MkdirAll(filepath.Join(target, ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ".dolt", "repo_state.json"), []byte(`{"remotes": not-json`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := cache.Ensure(context.Background(), remote); !policyRefusal(err) {
		t.Fatalf("Ensure error = %v, want a fail-closed refusal on an unreadable origin", err)
	}
}

// The benign companion: a list that matches both the URL and the recorded
// origin leaves the warm-cache behavior unchanged — a still-fresh entry is
// handed back without a pull, exactly as with no list.
func TestCacheWarmFreshEntryUsableUnderMatchingList(t *testing.T) {
	skipIfNoDolt(t)
	setAllowedRemotePatternsForTest(t, []string{"git+https://github.com/myorg/*"})
	cache := &Cache{Dir: t.TempDir(), FreshFor: defaultFreshFor}
	remote := "git+https://github.com/myorg/registry"

	target := cache.cloneTarget(remote)
	if err := os.MkdirAll(filepath.Join(target, ".dolt"), 0o750); err != nil {
		t.Fatal(err)
	}
	state := `{"remotes":{"origin":{"url":"` + remote + `"}}}`
	if err := os.WriteFile(filepath.Join(target, ".dolt", "repo_state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	cache.writeMeta(remote, &CacheMeta{RemoteURL: remote, LastPull: time.Now().UnixNano()})

	entry, err := cache.Ensure(context.Background(), remote)
	if err != nil {
		t.Fatalf("Ensure under a matching list: %v", err)
	}
	if entry != cache.entryDir(remote) {
		t.Errorf("Ensure entry = %q, want %q", entry, cache.entryDir(remote))
	}
}
