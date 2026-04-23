package remotecache

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// skipIfNoDolt skips the test if the dolt CLI is not installed. Under
// GitHub Actions the test fails instead — CI must install dolt.
//
// (Duplicated inline rather than importing testutil.RequireDoltBinary to avoid
// an import cycle: testutil → doltutil → remotecache.)
func skipIfNoDolt(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("dolt"); err != nil {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("dolt binary missing under GITHUB_ACTIONS: %v — the CI workflow must install dolt", err)
		}
		t.Skipf("dolt CLI not found, skipping integration test: %v", err)
	}
}

// initDoltRemote creates a file:// dolt remote by initializing a dolt repo,
// adding a file:// remote, and pushing to it. Returns the file:// URL that
// can be used with dolt clone.
func initDoltRemote(t *testing.T, dir string) string {
	t.Helper()

	// Create the "source" repo that we'll push from
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// dolt init
	cmd := exec.Command("dolt", "init", "--name", "test", "--email", "test@test.com")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt init failed: %v\n%s", err, out)
	}

	// Create a table so there's data to clone
	cmd = exec.Command("dolt", "sql", "-q", "CREATE TABLE test_table (id INT PRIMARY KEY, name VARCHAR(100))")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create table failed: %v\n%s", err, out)
	}

	cmd = exec.Command("dolt", "add", ".")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt add failed: %v\n%s", err, out)
	}

	cmd = exec.Command("dolt", "commit", "-m", "init")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt commit failed: %v\n%s", err, out)
	}

	// Create the remote directory and add it as a file:// remote
	remoteDir := filepath.Join(dir, "remote-storage")
	if err := os.MkdirAll(remoteDir, 0o750); err != nil {
		t.Fatal(err)
	}
	remoteURL := "file://" + remoteDir

	cmd = exec.Command("dolt", "remote", "add", "origin", remoteURL)
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt remote add failed: %v\n%s", err, out)
	}

	// Push to create the remote storage
	cmd = exec.Command("dolt", "push", "origin", "main")
	cmd.Dir = srcDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt push failed: %v\n%s", err, out)
	}

	return remoteURL
}

func TestEnsureColdStart(t *testing.T) {
	skipIfNoDolt(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	remoteURL := initDoltRemote(t, filepath.Join(tmpDir, "remote"))

	cache := &Cache{Dir: filepath.Join(tmpDir, "cache")}
	entryDir, err := cache.Ensure(ctx, remoteURL)
	if err != nil {
		t.Fatalf("Ensure (cold) failed: %v", err)
	}

	// Verify the clone exists
	target := cache.cloneTarget(remoteURL)
	if !cache.doltExists(target) {
		t.Errorf("expected .dolt directory at %s", target)
	}

	// Verify metadata was written
	meta := cache.readMeta(remoteURL)
	if meta.RemoteURL != remoteURL {
		t.Errorf("meta.RemoteURL = %q, want %q", meta.RemoteURL, remoteURL)
	}
	if meta.LastPull == 0 {
		t.Error("meta.LastPull should be set after Ensure")
	}

	// Entry dir should be the parent of the clone target
	if entryDir != cache.entryDir(remoteURL) {
		t.Errorf("entryDir = %q, want %q", entryDir, cache.entryDir(remoteURL))
	}
}

func TestEnsureWarmStart(t *testing.T) {
	skipIfNoDolt(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	remoteURL := initDoltRemote(t, filepath.Join(tmpDir, "remote"))

	cache := &Cache{Dir: filepath.Join(tmpDir, "cache")}

	// Cold start
	if _, err := cache.Ensure(ctx, remoteURL); err != nil {
		t.Fatalf("Ensure (cold) failed: %v", err)
	}

	firstMeta := cache.readMeta(remoteURL)

	// Warm start (should pull, not clone)
	if _, err := cache.Ensure(ctx, remoteURL); err != nil {
		t.Fatalf("Ensure (warm) failed: %v", err)
	}

	secondMeta := cache.readMeta(remoteURL)
	if secondMeta.LastPull <= firstMeta.LastPull {
		t.Error("LastPull should update on warm start")
	}
}

func TestEvict(t *testing.T) {
	skipIfNoDolt(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	remoteURL := initDoltRemote(t, filepath.Join(tmpDir, "remote"))

	cache := &Cache{Dir: filepath.Join(tmpDir, "cache")}
	if _, err := cache.Ensure(ctx, remoteURL); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}

	// Verify cache exists
	if !cache.doltExists(cache.cloneTarget(remoteURL)) {
		t.Fatal("expected cache entry to exist before eviction")
	}

	// Evict
	if err := cache.Evict(remoteURL); err != nil {
		t.Fatalf("Evict failed: %v", err)
	}

	// Verify gone
	if cache.doltExists(cache.cloneTarget(remoteURL)) {
		t.Error("expected cache entry to be gone after eviction")
	}
}

func TestPush(t *testing.T) {
	skipIfNoDolt(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	remoteURL := initDoltRemote(t, filepath.Join(tmpDir, "remote"))

	cache := &Cache{Dir: filepath.Join(tmpDir, "cache")}

	// Clone the remote
	if _, err := cache.Ensure(ctx, remoteURL); err != nil {
		t.Fatalf("Ensure failed: %v", err)
	}

	// Make a local change in the cached clone
	target := cache.cloneTarget(remoteURL)
	cmd := exec.Command("dolt", "sql", "-q", "INSERT INTO test_table VALUES (1, 'pushed')")
	cmd.Dir = target
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("insert failed: %v\n%s", err, out)
	}

	cmd = exec.Command("dolt", "add", ".")
	cmd.Dir = target
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt add failed: %v\n%s", err, out)
	}

	cmd = exec.Command("dolt", "commit", "-m", "add row")
	cmd.Dir = target
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt commit failed: %v\n%s", err, out)
	}

	// Push back to remote
	if err := cache.Push(ctx, remoteURL); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Verify push timestamp was recorded
	meta := cache.readMeta(remoteURL)
	if meta.LastPush == 0 {
		t.Error("meta.LastPush should be set after Push")
	}

	// Verify the data made it to the remote by cloning into a fresh dir
	verifyDir := filepath.Join(tmpDir, "verify")
	cmd = exec.Command("dolt", "clone", remoteURL, verifyDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("verification clone failed: %v\n%s", err, out)
	}

	cmd = exec.Command("dolt", "sql", "-q", "SELECT name FROM test_table WHERE id = 1", "-r", "csv")
	cmd.Dir = verifyDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("verification query failed: %v", err)
	}
	if !strings.Contains(string(out), "pushed") {
		t.Errorf("expected 'pushed' in verification output, got: %s", out)
	}
}

func TestEnsureFreshFor(t *testing.T) {
	skipIfNoDolt(t)
	ctx := context.Background()

	tmpDir := t.TempDir()
	remoteURL := initDoltRemote(t, filepath.Join(tmpDir, "remote"))

	cache := &Cache{
		Dir:      filepath.Join(tmpDir, "cache"),
		FreshFor: 1 * time.Hour, // very long TTL so second call skips pull
	}

	// Cold start (always clones)
	if _, err := cache.Ensure(ctx, remoteURL); err != nil {
		t.Fatalf("Ensure (cold) failed: %v", err)
	}

	firstMeta := cache.readMeta(remoteURL)

	// Second call should skip pull because of FreshFor
	if _, err := cache.Ensure(ctx, remoteURL); err != nil {
		t.Fatalf("Ensure (warm, fresh) failed: %v", err)
	}

	secondMeta := cache.readMeta(remoteURL)
	if secondMeta.LastPull != firstMeta.LastPull {
		t.Error("LastPull should NOT update when cache is still fresh")
	}

	// With FreshFor=0, should always pull
	cache.FreshFor = 0
	if _, err := cache.Ensure(ctx, remoteURL); err != nil {
		t.Fatalf("Ensure (warm, FreshFor=0) failed: %v", err)
	}

	thirdMeta := cache.readMeta(remoteURL)
	if thirdMeta.LastPull <= firstMeta.LastPull {
		t.Error("LastPull should update when FreshFor=0")
	}
}

func TestDefaultCache(t *testing.T) {
	cache, err := DefaultCache()
	if err != nil {
		t.Fatalf("DefaultCache failed: %v", err)
	}
	if cache.Dir == "" {
		t.Error("cache.Dir should not be empty")
	}
	// Should end with beads/remotes
	if filepath.Base(filepath.Dir(cache.Dir)) != "beads" || filepath.Base(cache.Dir) != "remotes" {
		t.Errorf("unexpected cache dir: %s", cache.Dir)
	}
}
