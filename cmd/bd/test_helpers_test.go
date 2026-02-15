//go:build cgo

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
)

// testIDCounter ensures unique IDs across all test runs
var testIDCounter atomic.Uint64

// generateUniqueTestID creates a globally unique test ID using prefix, test name, and atomic counter.
// This prevents ID collisions when multiple tests manipulate global state.
func generateUniqueTestID(t *testing.T, prefix string, index int) string {
	t.Helper()
	counter := testIDCounter.Add(1)
	// include test name, counter, and index for uniqueness
	data := []byte(t.Name() + prefix + string(rune(counter)) + string(rune(index)))
	hash := sha256.Sum256(data)
	return prefix + "-" + hex.EncodeToString(hash[:])[:8]
}

const windowsOS = "windows"

// initConfigForTest initializes viper config for a test and ensures cleanup.
// main.go's init() calls config.Initialize() which picks up the real .beads/config.yaml.
// TestMain resets viper, but any test calling config.Initialize() re-loads the real config.
// This helper ensures viper is reset after the test completes, preventing state pollution
// (e.g., sync.mode=dolt-native leaking into JSONL export tests).
func initConfigForTest(t *testing.T) {
	t.Helper()
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

// ensureTestMode sets BEADS_TEST_MODE environment variable to prevent production pollution
func ensureTestMode(t *testing.T) {
	t.Helper()
	os.Setenv("BEADS_TEST_MODE", "1")
	t.Cleanup(func() {
		os.Unsetenv("BEADS_TEST_MODE")
	})
}

// ensureCleanGlobalState resets global state that may have been modified by other tests.
// Call this at the start of tests that manipulate globals directly.
func ensureCleanGlobalState(t *testing.T) {
	t.Helper()
	// Reset CommandContext so accessor functions fall back to globals
	resetCommandContext()
}

// savedGlobals holds a snapshot of package-level globals for safe restoration.
// Used by saveAndRestoreGlobals to ensure test isolation.
type savedGlobals struct {
	dbPath      string
	store       storage.Storage
	storeActive bool
}

// saveAndRestoreGlobals snapshots all commonly-mutated package-level globals
// and registers a t.Cleanup() to restore them when the test completes.
// This replaces the fragile manual save/defer pattern:
//
//	oldDBPath := dbPath
//	defer func() { dbPath = oldDBPath }()
//
// With the safer:
//
//	saveAndRestoreGlobals(t)
//
// Benefits:
//   - All globals saved atomically (can't forget one)
//   - t.Cleanup runs even on panic (no risk of missed defer registration)
//   - Single call replaces multiple save/defer pairs
func saveAndRestoreGlobals(t *testing.T) *savedGlobals {
	t.Helper()
	saved := &savedGlobals{
		dbPath:      dbPath,
		store:       store,
		storeActive: storeActive,
	}
	t.Cleanup(func() {
		dbPath = saved.dbPath
		store = saved.store
		storeMutex.Lock()
		storeActive = saved.storeActive
		storeMutex.Unlock()
	})
	return saved
}

// failIfProductionDatabase checks if the database path is in a production directory
// and fails the test to prevent test pollution (bd-2c5a)
func failIfProductionDatabase(t *testing.T, dbPath string) {
	t.Helper()

	// CRITICAL (bd-2c5a): Set test mode flag
	ensureTestMode(t)

	// Get absolute path for comparison
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		t.Logf("Warning: Could not get absolute path for %s: %v", dbPath, err)
		return
	}

	// Use worktree-aware git directory detection
	gitDir, err := git.GetGitDir()
	if err != nil {
		// Not a git repository, no pollution risk
		return
	}

	// Check if database is in .beads/ directory of this git repository
	beadsPath := ""
	gitDirAbs, err := filepath.Abs(gitDir)
	if err != nil {
		t.Logf("Warning: Could not get absolute path for git dir %s: %v", gitDir, err)
		return
	}

	// The .beads directory should be at the root of the git repository
	// For worktrees, gitDir points to the main repo's .git directory
	repoRoot := filepath.Dir(gitDirAbs)
	beadsPath = filepath.Join(repoRoot, ".beads")

	if strings.HasPrefix(absPath, beadsPath) {
		// Database is in .beads/ directory of a git repository
		// This is ONLY allowed if we're in a temp directory
		if !strings.Contains(absPath, os.TempDir()) {
			t.Fatalf("PRODUCTION DATABASE POLLUTION DETECTED (bd-2c5a):\n"+
				"  Database: %s\n"+
				"  Git repo: %s\n"+
				"  Tests MUST use t.TempDir() or tempfile to create isolated databases.\n"+
				"  This prevents test issues from polluting the production database.",
				absPath, repoRoot)
		}
	}
}

// newTestStore creates a SQLite store with issue_prefix configured (bd-166)
// This prevents "database not initialized" errors in tests
func newTestStore(t *testing.T, dbPath string) storage.Storage {
	t.Helper()

	// CRITICAL (bd-2c5a): Ensure we're not polluting production database
	failIfProductionDatabase(t, dbPath)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create database directory: %v", err)
	}

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Configure Gas Town custom types for test compatibility (bd-find4)
	if err := store.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		store.Close()
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	t.Cleanup(func() { store.Close() })
	return store
}

// newTestStoreWithPrefix creates a SQLite store with custom issue_prefix configured
func newTestStoreWithPrefix(t *testing.T, dbPath string, prefix string) storage.Storage {
	t.Helper()

	// CRITICAL (bd-2c5a): Ensure we're not polluting production database
	failIfProductionDatabase(t, dbPath)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("Failed to create database directory: %v", err)
	}

	store, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Failed to create test database: %v", err)
	}

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
	ctx := context.Background()
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		store.Close()
		t.Fatalf("Failed to set issue_prefix: %v", err)
	}

	// Configure Gas Town custom types for test compatibility (bd-find4)
	if err := store.SetConfig(ctx, "types.custom", "molecule,gate,convoy,merge-request,slot,agent,role,rig,event,message"); err != nil {
		store.Close()
		t.Fatalf("Failed to set types.custom: %v", err)
	}

	t.Cleanup(func() { store.Close() })
	return store
}

// openExistingTestDB opens an existing database without modifying it.
// Used in tests where the database was already created by the code under test.
func openExistingTestDB(t *testing.T, dbPath string) (storage.Storage, error) {
	t.Helper()
	return sqlite.New(context.Background(), dbPath)
}

// runCommandInDir runs a command in the specified directory
func runCommandInDir(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.Run()
}

// runCommandInDirWithOutput runs a command in the specified directory and returns its output
func runCommandInDirWithOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// captureStderr captures stderr output from fn and returns it as a string.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	os.Stderr = old
	<-done
	_ = r.Close()

	return buf.String()
}
