// Pure-Go test helpers shared across cmd/bd test files.
//
// This file MUST NOT carry a `//go:build cgo` tag. Helpers here are kept
// stdlib-only (no sql.DB, no internal/storage/dolt, no embedded Dolt) so
// that pure-Go tests in this package compile under CGO_ENABLED=0 with the
// gms_pure_go build tag. The cgo-only counterparts live in
// test_helpers_test.go (tagged `//go:build cgo`).
//
// If you add a helper here that grows a cgo dependency, move it to
// test_helpers_test.go to preserve the build-tag separation.

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

const windowsOS = "windows"

// testIDCounter ensures unique IDs across all test runs.
var testIDCounter atomic.Uint64

// doltNewMutex serializes dolt.New() calls in tests. The Dolt embedded engine's
// InitStatusVariables() has an internal race condition when called concurrently
// from multiple goroutines (writes to a shared global map without synchronization).
// Serializing store creation prevents this race while allowing tests to run their
// assertions in parallel after the store is created.
//
// Declared in the pure-Go helpers file so that any test (including non-cgo
// tests that exercise pure code paths) can refer to it without forcing cgo.
var doltNewMutex sync.Mutex

// stdioMutex serializes tests that redirect os.Stdout or os.Stderr.
// These process-global file descriptors cannot be safely redirected from
// concurrent goroutines.
//
// IMPORTANT: Any test that calls cobra's Help(), Execute(), or Print*()
// MUST NOT be parallel (no t.Parallel()), OR must serialize those calls
// under stdioMutex. Setting cmd.SetOut() is NOT sufficient because cobra's
// OutOrStdout() eagerly evaluates os.Stdout as the default argument even
// when outWriter is set — the Go race detector catches this read.
//
// TestCobraParallelPolicyGuard in stdio_race_guard_test.go enforces this.
var stdioMutex sync.Mutex

// uniqueTestDBName generates a unique database name for test isolation.
func uniqueTestDBName(t *testing.T) string {
	t.Helper()
	h := sha256.Sum256([]byte(t.Name() + fmt.Sprintf("%d", time.Now().UnixNano())))
	return "testdb_" + hex.EncodeToString(h[:6])
}

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

// initConfigForTest initializes viper config for a test and ensures cleanup.
// main.go's init() calls config.Initialize() which picks up the real .beads/config.yaml.
// TestMain resets viper, but any test calling config.Initialize() re-loads the real config.
// This helper ensures viper is reset after the test completes, preventing state pollution
// (e.g., repo config values leaking into JSONL export tests).
func initConfigForTest(t *testing.T) {
	t.Helper()
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	t.Cleanup(config.ResetForTesting)
}

// ensureTestMode is a no-op; BEADS_TEST_MODE is set once in TestMain.
// Previously each test set/unset the env var, which raced under t.Parallel().
func ensureTestMode(t *testing.T) {
	t.Helper()
	// BEADS_TEST_MODE is set in TestMain and stays set for the entire test run.
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
	dbPath                string
	store                 storage.DoltStorage
	storeActive           bool
	exportOutput          string
	exportAll             bool
	exportIncludeInfra    bool
	exportScrub           bool
	exportNoMemories      bool
	exportIncludeMemories bool
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
		dbPath:                dbPath,
		store:                 store,
		storeActive:           storeActive,
		exportOutput:          exportOutput,
		exportAll:             exportAll,
		exportIncludeInfra:    exportIncludeInfra,
		exportScrub:           exportScrub,
		exportNoMemories:      exportNoMemories,
		exportIncludeMemories: exportIncludeMemories,
	}
	t.Cleanup(func() {
		dbPath = saved.dbPath
		store = saved.store
		storeMutex.Lock()
		storeActive = saved.storeActive
		storeMutex.Unlock()
		exportOutput = saved.exportOutput
		exportAll = saved.exportAll
		exportIncludeInfra = saved.exportIncludeInfra
		exportScrub = saved.exportScrub
		exportNoMemories = saved.exportNoMemories
		exportIncludeMemories = saved.exportIncludeMemories
	})
	return saved
}

// runCommandInDir runs a command in the specified directory.
func runCommandInDir(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = testEnvNoPrompt()
	return cmd.Run()
}

// runCommandInDirWithOutput runs a command in the specified directory and returns its output.
func runCommandInDirWithOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = testEnvNoPrompt()
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// testEnvNoPrompt returns the current environment with git auth prompts
// suppressed. Prevents ksshaskpass/SSH_ASKPASS popups during tests that
// configure fake git remotes (e.g. github.com/test/repo.git).
func testEnvNoPrompt() []string {
	env := os.Environ()
	env = append(env, "GIT_TERMINAL_PROMPT=0", "SSH_ASKPASS=", "GIT_ASKPASS=")
	return env
}

// captureStdout captures stdout output from fn and returns it as a string.
// Uses stdioMutex to prevent races with concurrent os.Stdout redirection (bd-cqjoi).
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := fn()

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stdout = oldStdout

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	return buf.String()
}

// captureStderr captures stderr output from fn and returns it as a string.
// Uses stdioMutex to prevent races with concurrent os.Stderr redirection.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	stdioMutex.Lock()
	defer stdioMutex.Unlock()

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

// buildBDOnce builds the bd binary once for subprocess tests.
// Uses sync.Once for efficiency when multiple tests need the binary.
var (
	initTestBD     string
	initTestBDOnce sync.Once
	initTestBDErr  error
)

// buildBDForInitTests builds (or locates) a bd binary suitable for subprocess
// tests. Uses the gms_pure_go tag so the resulting binary works in either
// CGO mode. Lives in the pure-Go helpers file so subprocess-style tests can
// run without the test package itself depending on cgo at compile time.
func buildBDForInitTests(t *testing.T) string {
	t.Helper()
	initTestBDOnce.Do(func() {
		// Check if bd binary exists in repo root (../../bd from cmd/bd/)
		bdBinary := "bd"
		if runtime.GOOS == windowsOS {
			bdBinary = "bd.exe"
		}
		repoRoot := filepath.Join("..", "..")
		existingBD := filepath.Join(repoRoot, bdBinary)
		if _, err := os.Stat(existingBD); err == nil {
			initTestBD, _ = filepath.Abs(existingBD)
			return
		}
		// Fall back to building
		tmpDir, err := os.MkdirTemp("", "bd-init-test-*")
		if err != nil {
			initTestBDErr = fmt.Errorf("failed to create temp dir: %w", err)
			return
		}
		initTestBD = filepath.Join(tmpDir, bdBinary)
		cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", initTestBD, ".")
		if out, err := cmd.CombinedOutput(); err != nil {
			initTestBDErr = fmt.Errorf("go build failed: %v\n%s", err, out)
		}
	})
	if initTestBDErr != nil {
		t.Fatalf("Failed to build bd binary: %v", initTestBDErr)
	}
	return initTestBD
}

// runGitForBootstrapTest runs a git subcommand in the given directory and
// fails the test on error. Used by bootstrap and init-safety subprocess tests.
func runGitForBootstrapTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(output))
	}
}
