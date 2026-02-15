//go:build cgo

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/dolt"
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
	store       *dolt.DoltStore
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

// newTestStore creates a dolt store with issue_prefix configured (bd-166)
// This prevents "database not initialized" errors in tests
func newTestStore(t *testing.T, dbPath string) *dolt.DoltStore {
	t.Helper()

	ensureTestMode(t)

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("Failed to create dolt store: %v", err)
	}

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
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

// newTestStoreWithPrefix creates a dolt store with custom issue_prefix configured
func newTestStoreWithPrefix(t *testing.T, dbPath string, prefix string) *dolt.DoltStore {
	t.Helper()

	ensureTestMode(t)

	ctx := context.Background()
	store, err := dolt.New(ctx, &dolt.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("Failed to create dolt store: %v", err)
	}

	// CRITICAL (bd-166): Set issue_prefix to prevent "database not initialized" errors
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

// openExistingTestDB is not supported with the memory backend since memory stores
// cannot be "reopened" from a previous state.
func openExistingTestDB(t *testing.T, dbPath string) (*dolt.DoltStore, error) {
	t.Helper()
	return nil, fmt.Errorf("openExistingTestDB not supported with memory backend")
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
