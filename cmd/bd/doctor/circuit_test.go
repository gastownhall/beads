package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCheckCircuitBreaker_ReadsPerUserDir verifies the check scans the same
// per-user directory the storage layer writes (GH#4223): on Unix that is
// beads-circuit-<uid> under the temp root. Base scanned the unsuffixed
// shared path and missed stale state planted in the new location.
func TestCheckCircuitBreaker_ReadsPerUserDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	t.Setenv("BEADS_TEST_CIRCUIT_DIR", "")

	// The expected location is computed independently of the storage
	// layer's resolver: the per-user name on Unix, the shared one wherever
	// the platform has no euid (os.Geteuid is -1 there).
	name := "beads-circuit"
	if uid := os.Geteuid(); uid >= 0 {
		name = "beads-circuit-" + strconv.Itoa(uid)
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale, err := json.Marshal(map[string]string{
		"state":      "open",
		"tripped_at": time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(dir, "beads-dolt-circuit-127-0-0-1-3307-peruser.json")
	if err := os.WriteFile(staleFile, stale, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckCircuitBreaker()
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (message %q)", check.Status, StatusWarning, check.Message)
	}
	if !strings.Contains(check.Message, dir) {
		t.Errorf("message %q does not name the per-user circuit directory %q", check.Message, dir)
	}
}

// A state directory the storage layer refuses (here a symlink, as another
// local user could plant at the predictable path) disables the breaker
// (GH#4223). The check reports that with the manual remedy and reads none of
// the directory's entries: the stale state behind the link is not counted,
// and --fix, which never sweeps such a directory, is not offered as the cure.
func TestCheckCircuitBreaker_UnsafeDirReportsDisabledBreaker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	target := t.TempDir()
	stale, err := json.Marshal(map[string]string{
		"state":      "open",
		"tripped_at": time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "beads-dolt-circuit-127-0-0-1-3307-unsafe.json"), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BEADS_TEST_CIRCUIT_DIR", linked)

	check := CheckCircuitBreaker()
	if check.Status != StatusWarning {
		t.Fatalf("status = %q, want %q (message %q)", check.Status, StatusWarning, check.Message)
	}
	for _, want := range []string{"disabled", linked, "is a symlink"} {
		if !strings.Contains(check.Message, want) {
			t.Errorf("message %q does not mention %q", check.Message, want)
		}
	}
	if strings.Contains(check.Message, "stale") {
		t.Errorf("message %q counts entries read through the unsafe directory", check.Message)
	}
	if !strings.Contains(check.Fix, "TMPDIR") || strings.Contains(check.Fix, "--fix") {
		t.Errorf("fix %q, want the manual remedy, not bd doctor --fix", check.Fix)
	}
}
