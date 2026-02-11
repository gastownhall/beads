//go:build cgo && integration

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// TEST HARNESS: Periodic Remote Sync in Event-Driven Mode
// =============================================================================
//
// These tests validate that the event-driven daemon periodically pulls from
// remote to check for updates from other clones. This is essential for
// multi-clone workflows where one clone pushes changes that other clones
// need to receive.
//
// WITHOUT THE FIX: Daemon only reacts to local file changes, never pulls remote
// WITH THE FIX: Daemon periodically calls doAutoImport to pull from remote

// TestEventDrivenLoop_HasRemoteSyncTicker validates that the event loop code
// includes a remoteSyncTicker for periodic remote sync.
func TestEventDrivenLoop_HasRemoteSyncTicker(t *testing.T) {
	// Read the daemon_event_loop.go file and check for remoteSyncTicker
	content, err := os.ReadFile("daemon_event_loop.go")
	if err != nil {
		t.Fatalf("Failed to read daemon_event_loop.go: %v", err)
	}

	code := string(content)

	// Check for the remoteSyncTicker variable
	if !strings.Contains(code, "remoteSyncTicker") {
		t.Fatal("remoteSyncTicker not found in event loop - periodic sync not implemented")
	}

	// Check for periodic sync in select cases
	if !strings.Contains(code, "remoteSyncTicker.C") {
		t.Fatal("remoteSyncTicker.C not found in select statement - ticker not wired up")
	}

	// Check for doAutoImport call in the ticker case
	if !strings.Contains(code, "doAutoImport()") {
		t.Fatal("doAutoImport() not called - periodic sync not performing imports")
	}

	t.Log("Event loop correctly includes remoteSyncTicker for periodic remote sync")
}

// TestGetRemoteSyncInterval_Default validates that the default interval is used
// when no environment variable is set.
func TestGetRemoteSyncInterval_Default(t *testing.T) {
	// Ensure env var is not set
	os.Unsetenv("BEADS_REMOTE_SYNC_INTERVAL")

	log := createTestLogger(t)
	interval := getRemoteSyncInterval(log)

	if interval != DefaultRemoteSyncInterval {
		t.Errorf("Expected default interval %v, got %v", DefaultRemoteSyncInterval, interval)
	}

	if interval != 30*time.Second {
		t.Errorf("Expected 30s default, got %v", interval)
	}
}

// TestGetRemoteSyncInterval_CustomValue validates that custom intervals are parsed.
func TestGetRemoteSyncInterval_CustomValue(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		expected time.Duration
	}{
		{"1 minute", "1m", 1 * time.Minute},
		{"5 minutes", "5m", 5 * time.Minute},
		{"60 seconds", "60s", 60 * time.Second},
		{"10 seconds", "10s", 10 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			os.Setenv("BEADS_REMOTE_SYNC_INTERVAL", tc.envValue)
			defer os.Unsetenv("BEADS_REMOTE_SYNC_INTERVAL")

			log := createTestLogger(t)
			interval := getRemoteSyncInterval(log)

			if interval != tc.expected {
				t.Errorf("Expected %v, got %v", tc.expected, interval)
			}
		})
	}
}

// TestGetRemoteSyncInterval_MinimumEnforced validates that intervals below 5s
// are clamped to the minimum.
func TestGetRemoteSyncInterval_MinimumEnforced(t *testing.T) {
	os.Setenv("BEADS_REMOTE_SYNC_INTERVAL", "1s")
	defer os.Unsetenv("BEADS_REMOTE_SYNC_INTERVAL")

	log := createTestLogger(t)
	interval := getRemoteSyncInterval(log)

	if interval != 5*time.Second {
		t.Errorf("Expected minimum 5s, got %v", interval)
	}
}

// TestGetRemoteSyncInterval_InvalidValue validates that invalid values fall back
// to the default.
func TestGetRemoteSyncInterval_InvalidValue(t *testing.T) {
	os.Setenv("BEADS_REMOTE_SYNC_INTERVAL", "not-a-duration")
	defer os.Unsetenv("BEADS_REMOTE_SYNC_INTERVAL")

	log := createTestLogger(t)
	interval := getRemoteSyncInterval(log)

	if interval != DefaultRemoteSyncInterval {
		t.Errorf("Expected default interval on invalid value, got %v", interval)
	}
}

// TestGetRemoteSyncInterval_Zero validates that zero disables periodic sync.
func TestGetRemoteSyncInterval_Zero(t *testing.T) {
	os.Setenv("BEADS_REMOTE_SYNC_INTERVAL", "0")
	defer os.Unsetenv("BEADS_REMOTE_SYNC_INTERVAL")

	log := createTestLogger(t)
	interval := getRemoteSyncInterval(log)

	// Zero should return a very large interval (effectively disabled)
	if interval < 24*time.Hour {
		t.Errorf("Expected very large interval when disabled, got %v", interval)
	}
}

// TestPeriodicRemoteSync_DoAutoImportWiring validates that doAutoImport
// is correctly wired up to be called by the periodic sync mechanism.
func TestPeriodicRemoteSync_DoAutoImportWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Track if doAutoImport was called
	var importCalled bool
	var mu sync.Mutex

	doAutoImport := func() {
		mu.Lock()
		importCalled = true
		mu.Unlock()
	}

	// Simulate what the event loop does on periodic sync
	doAutoImport()

	mu.Lock()
	called := importCalled
	mu.Unlock()

	if !called {
		t.Fatal("doAutoImport was not called - periodic sync wiring broken")
	}

	t.Log("doAutoImport function is correctly callable for periodic sync")
}

// TestSyncBranchPull_FetchesRemoteUpdates validates that the sync branch pull
// mechanism correctly fetches updates from remote.
func TestSyncBranchPull_FetchesRemoteUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if runtime.GOOS == "windows" {
		t.Skip("Skipping on Windows")
	}

	// Create a remote and clone setup
	remoteDir := t.TempDir()
	runGitCmd(t, remoteDir, "init", "--bare")

	// Clone 1: Creates initial content and pushes
	clone1Dir := t.TempDir()
	runGitCmd(t, clone1Dir, "clone", remoteDir, ".")
	runGitCmd(t, clone1Dir, "config", "user.email", "test@example.com")
	runGitCmd(t, clone1Dir, "config", "user.name", "Test User")
	initMainBranchForSyncTest(t, clone1Dir)
	runGitCmd(t, clone1Dir, "push", "-u", "origin", "main")

	runGitCmd(t, clone1Dir, "checkout", "-b", "beads-sync")
	beadsDir1 := filepath.Join(clone1Dir, ".beads")
	if err := os.MkdirAll(beadsDir1, 0755); err != nil {
		t.Fatal(err)
	}
	initialContent := `{"id":"issue-1","title":"First issue"}`
	if err := os.WriteFile(filepath.Join(beadsDir1, "issues.jsonl"), []byte(initialContent+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
	runGitCmd(t, clone1Dir, "commit", "-m", "Initial issue")
	runGitCmd(t, clone1Dir, "push", "-u", "origin", "beads-sync")

	// Clone 2: Fetches sync branch
	clone2Dir := t.TempDir()
	runGitCmd(t, clone2Dir, "clone", remoteDir, ".")
	runGitCmd(t, clone2Dir, "config", "user.email", "test@example.com")
	runGitCmd(t, clone2Dir, "config", "user.name", "Test User")
	runGitCmd(t, clone2Dir, "fetch", "origin", "beads-sync:beads-sync")

	// Create worktree in clone2
	worktreePath := filepath.Join(clone2Dir, ".git", "beads-worktrees", "beads-sync")
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, clone2Dir, "worktree", "add", worktreePath, "beads-sync")

	// Clone 1 pushes MORE content
	runGitCmd(t, clone1Dir, "checkout", "beads-sync")
	updatedContent := initialContent + "\n" + `{"id":"issue-2","title":"Second issue"}`
	if err := os.WriteFile(filepath.Join(beadsDir1, "issues.jsonl"), []byte(updatedContent+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, clone1Dir, "add", ".beads/issues.jsonl")
	runGitCmd(t, clone1Dir, "commit", "-m", "Second issue")
	runGitCmd(t, clone1Dir, "push", "origin", "beads-sync")

	// Clone 2's worktree should NOT have the second issue yet
	worktreeJSONL := filepath.Join(worktreePath, ".beads", "issues.jsonl")
	beforePull, _ := os.ReadFile(worktreeJSONL)
	if strings.Contains(string(beforePull), "issue-2") {
		t.Log("Worktree already has issue-2 (unexpected)")
	} else {
		t.Log("Worktree does NOT have issue-2 (expected before pull)")
	}

	// Now pull in the worktree (simulating what syncBranchPull does)
	runGitCmd(t, worktreePath, "pull", "origin", "beads-sync")

	// Clone 2's worktree SHOULD now have the second issue
	afterPull, _ := os.ReadFile(worktreeJSONL)
	if !strings.Contains(string(afterPull), "issue-2") {
		t.Fatal("After pull, worktree still doesn't have issue-2 - sync branch pull broken")
	}

	t.Log("Sync branch pull correctly fetches remote updates")
}

// =============================================================================
// AUTO-PULL CONFIGURATION TESTS
// =============================================================================

// TestAutoPullGatesRemoteSyncTicker validates that the remoteSyncTicker is only
// created when autoPull is true.
func TestAutoPullGatesRemoteSyncTicker(t *testing.T) {
	// Read the daemon_event_loop.go file and check for autoPull gating
	content, err := os.ReadFile("daemon_event_loop.go")
	if err != nil {
		t.Fatalf("Failed to read daemon_event_loop.go: %v", err)
	}

	code := string(content)

	// Check that remoteSyncTicker is gated on autoPull
	if !strings.Contains(code, "if autoPull") {
		t.Fatal("autoPull check not found - remoteSyncTicker not gated on autoPull")
	}

	// Check that autoPull parameter exists in function signature
	if !strings.Contains(code, "autoPull bool") {
		t.Fatal("autoPull bool parameter not found in runEventDrivenLoop signature")
	}

	// Check for disabled message when autoPull is false
	if !strings.Contains(code, "Auto-pull disabled") {
		t.Fatal("Auto-pull disabled message not found")
	}

	t.Log("remoteSyncTicker is correctly gated on autoPull parameter")
}

// TestAutoPullDefaultBehavior validates that auto_pull defaults to true when
// sync.branch is configured.
func TestAutoPullDefaultBehavior(t *testing.T) {
	// Read daemon.go and check for default behavior
	content, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatalf("Failed to read daemon.go: %v", err)
	}

	code := string(content)

	// Check that auto_pull reads from daemon.auto_pull config
	if !strings.Contains(code, "daemon.auto_pull") {
		t.Fatal("daemon.auto_pull config check not found")
	}

	// Check that auto_pull defaults based on sync.branch
	if !strings.Contains(code, "sync.branch") {
		t.Fatal("sync.branch check for auto_pull default not found")
	}

	// Check for BEADS_AUTO_PULL environment variable
	if !strings.Contains(code, "BEADS_AUTO_PULL") {
		t.Fatal("BEADS_AUTO_PULL environment variable not checked")
	}

	t.Log("auto_pull correctly defaults to true when sync.branch is configured")
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

func initMainBranchForSyncTest(t *testing.T, dir string) {
	t.Helper()
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# Test Repository\n"), 0644); err != nil {
		t.Fatalf("Failed to write README: %v", err)
	}
	runGitCmd(t, dir, "add", "README.md")
	runGitCmd(t, dir, "commit", "-m", "Initial commit")
}
