//go:build unix

package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// GH#4223: the check reads the per-user directory the storage layer writes,
// so a stale breaker there is reported, naming that directory.
func TestCheckCircuitBreaker_ReadsPerUserDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BEADS_TEST_CIRCUIT_DIR", "")
	t.Setenv("TMPDIR", root)
	dir := filepath.Join(root, fmt.Sprintf("beads-circuit-%d", os.Geteuid()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"state": "open", "tripped_at": time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beads-dolt-circuit-127-0-0-1-3307-db.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	check := CheckCircuitBreaker()

	if check.Status != StatusWarning {
		t.Fatalf("status = %q (%s), want %q", check.Status, check.Message, StatusWarning)
	}
	if !strings.Contains(check.Message, "1 stale") || !strings.Contains(check.Message, dir) {
		t.Errorf("message = %q, want it to report 1 stale file in %s", check.Message, dir)
	}
}
