package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
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
