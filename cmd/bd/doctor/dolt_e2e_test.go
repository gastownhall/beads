//go:build cgo

package doctor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// e2eDoctorResult mirrors the JSON output struct from cmd/bd/doctor.go.
// Kept minimal — only the fields we assert on.
type e2eDoctorResult struct {
	Path      string             `json:"path"`
	Checks    []e2eDoctorCheck   `json:"checks"`
	OverallOK bool               `json:"overall_ok"`
}

type e2eDoctorCheck struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Category string `json:"category,omitempty"`
}

// testBDPath holds the path to the bd binary built from the current branch.
// Built once via sync.Once to avoid redundant compilations across tests.
// testBDDir is cleaned up by TestMain after all tests complete.
var (
	testBDPath string
	testBDDir  string
	testBDOnce sync.Once
	testBDErr  error
)

// TestMain cleans up the temp directory holding the built bd binary.
func TestMain(m *testing.M) {
	code := m.Run()
	if testBDDir != "" {
		os.RemoveAll(testBDDir)
	}
	os.Exit(code)
}

// buildTestBD compiles the bd binary from the current worktree. This ensures
// E2E tests run against the code in this branch, not the system-installed bd.
func buildTestBD(t *testing.T) string {
	t.Helper()

	testBDOnce.Do(func() {
		bdBinary := "bd-test"
		if runtime.GOOS == "windows" {
			bdBinary = "bd-test.exe"
		}

		dir, err := os.MkdirTemp("", "bd-dolt-e2e-*")
		if err != nil {
			testBDErr = err
			return
		}
		testBDDir = dir

		testBDPath = filepath.Join(dir, bdBinary)

		// Build from cmd/bd relative to the module root.
		// The test runs from cmd/bd/doctor/, so module root is ../../../
		cmd := exec.Command("go", "build", "-o", testBDPath, "./cmd/bd")

		// Find the module root by looking for go.mod
		modRoot := findModuleRoot(t)
		cmd.Dir = modRoot

		// Set CGO flags for ICU (required for Dolt backend)
		icuPrefix := icuPrefixPath()
		if icuPrefix != "" {
			cmd.Env = append(os.Environ(),
				"CGO_CFLAGS=-I"+icuPrefix+"/include",
				"CGO_CPPFLAGS=-I"+icuPrefix+"/include",
				"CGO_LDFLAGS=-L"+icuPrefix+"/lib",
			)
		} else {
			cmd.Env = os.Environ()
		}

		out, err := cmd.CombinedOutput()
		if err != nil {
			testBDErr = &buildError{output: string(out), err: err}
			return
		}
	})

	if testBDErr != nil {
		t.Skipf("skipping E2E test: failed to build bd binary: %v", testBDErr)
	}

	return testBDPath
}

type buildError struct {
	output string
	err    error
}

func (e *buildError) Error() string {
	return e.err.Error() + "\n" + e.output
}

// findModuleRoot walks up from the test's source directory to find go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()

	// Start from the directory containing this test file.
	// runtime.Caller gives us the source file path at compile time.
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not determine test file location")
	}

	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod in any parent directory")
		}
		dir = parent
	}
}

// icuPrefixPath returns the ICU4C prefix from brew, or empty string if unavailable.
func icuPrefixPath() string {
	out, err := exec.Command("brew", "--prefix", "icu4c").Output()
	if err != nil {
		return ""
	}
	return string(out[:len(out)-1]) // trim trailing newline
}

// setupMinimalGitRepo creates a temp dir with a git repo and .beads/ directory.
// Doctor requires git context for workspace detection.
func setupMinimalGitRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	for _, args := range [][]string{
		{"init"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@test.com"},
	} {
		cmd := exec.Command("git", append([]string{"-C", tmpDir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("failed to create .beads dir: %v", err)
	}

	return tmpDir
}

// runBDDoctor executes `bd doctor <path> --json` using the branch-built binary
// and returns the parsed result, raw stdout, and any exec error. The JSON is on
// stdout even when exit code is 1 (doctor exits 1 when checks fail).
func runBDDoctor(t *testing.T, bdPath, path string) (e2eDoctorResult, string, error) {
	t.Helper()

	cmd := exec.Command(bdPath, "doctor", path, "--json")
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")

	out, execErr := cmd.Output()

	var result e2eDoctorResult
	if len(out) > 0 {
		if jsonErr := json.Unmarshal(out, &result); jsonErr != nil {
			t.Fatalf("failed to parse doctor JSON output: %v\nraw output: %s", jsonErr, out)
		}
	}

	return result, string(out), execErr
}

// TestE2E_DoctorSQLiteBackend verifies that `bd doctor --json` reports all 4
// dolt checks as N/A when the workspace uses SQLite backend.
func TestE2E_DoctorSQLiteBackend(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode (requires bd binary build)")
	}

	bdPath := buildTestBD(t)
	tmpDir := setupMinimalGitRepo(t)

	// Write metadata.json marking this as SQLite backend
	metadataPath := filepath.Join(tmpDir, ".beads", "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"backend":"sqlite"}`), 0o644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	result, _, _ := runBDDoctor(t, bdPath, tmpDir)

	// Find the 4 dolt checks and verify they are N/A
	doltCheckNames := map[string]bool{
		"Dolt Connection": false,
		"Dolt Schema":     false,
		"Dolt-JSONL Sync": false,
		"Dolt Status":     false,
	}

	for _, check := range result.Checks {
		if _, isDolt := doltCheckNames[check.Name]; isDolt {
			doltCheckNames[check.Name] = true

			if check.Message != "N/A (SQLite backend)" {
				t.Errorf("check %q: expected message %q, got %q",
					check.Name, "N/A (SQLite backend)", check.Message)
			}
		}
	}

	// Verify all 4 dolt checks were present
	for name, found := range doltCheckNames {
		if !found {
			t.Errorf("expected dolt check %q in output, not found", name)
		}
	}

	// Verify no orphan lock file was created
	lockPath := filepath.Join(tmpDir, ".beads", "dolt-access.lock")
	if _, err := os.Stat(lockPath); err == nil {
		t.Errorf("lock file should not exist for SQLite backend, found at %s", lockPath)
	}
}

// TestE2E_DoctorDoltBackendNoDB verifies that `bd doctor --json` handles a dolt
// workspace without a real database: exits non-zero, reports connection error,
// and does not leave orphan LOCK files.
func TestE2E_DoctorDoltBackendNoDB(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping E2E test in short mode (requires bd binary build)")
	}

	bdPath := buildTestBD(t)
	tmpDir := setupMinimalGitRepo(t)

	// Write metadata.json marking this as dolt backend
	metadataPath := filepath.Join(tmpDir, ".beads", "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatalf("failed to write metadata.json: %v", err)
	}

	// Create dolt directory (needed for lock acquisition path)
	doltDir := filepath.Join(tmpDir, ".beads", "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("failed to create dolt dir: %v", err)
	}

	result, _, execErr := runBDDoctor(t, bdPath, tmpDir)

	// Exit code should be non-zero (doctor reports failures)
	if execErr == nil {
		t.Error("expected non-zero exit code for dolt backend without real database")
	}

	// Verify "Dolt Connection" check is present with error status
	foundConnection := false
	for _, check := range result.Checks {
		if check.Name == "Dolt Connection" {
			foundConnection = true
			if check.Status != "error" {
				t.Errorf("Dolt Connection check: expected status %q, got %q", "error", check.Status)
			}
			break
		}
	}
	if !foundConnection {
		t.Error("expected 'Dolt Connection' check in output, not found")
	}

	// Verify no orphan lock file — prove the lock is released by acquiring
	// an exclusive lock. If the lock were still held, this would time out.
	exLock, err := dolt.AcquireAccessLock(doltDir, true, 2*time.Second)
	if err != nil {
		t.Errorf("could not acquire exclusive lock after bd doctor (lock not released?): %v", err)
	} else {
		exLock.Release()
	}
}
