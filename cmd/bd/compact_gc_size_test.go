package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestRunCompactDoltDryRunUsesActiveDatabaseSizer(t *testing.T) {
	output := runCompactDoltDryRunForTest(t, &gcSizeStoreStub{size: 42}, true)

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode dry-run JSON: %v\noutput: %s", err, output)
	}
	if got := result["size_before"]; got != float64(42) {
		t.Fatalf("size_before = %#v, want 42 from ActiveDatabaseSizer", got)
	}
	if got := result["size_display"]; got != "42 B" {
		t.Fatalf("size_display = %#v, want %q", got, "42 B")
	}
}

func TestRunCompactDoltDryRunOmitsUnsupportedSize(t *testing.T) {
	unsupported := &gcSizeStoreStub{err: &storage.ErrUnsupported{
		Op:      "ActiveDatabaseSize",
		Backend: "external",
	}}

	t.Run("JSON", func(t *testing.T) {
		output := runCompactDoltDryRunForTest(t, unsupported, true)
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("decode dry-run JSON: %v\noutput: %s", err, output)
		}
		for _, key := range []string{"size_before", "size_display"} {
			if got, ok := result[key]; ok {
				t.Fatalf("unsupported measurement emitted %s=%#v", key, got)
			}
		}
	})

	t.Run("text", func(t *testing.T) {
		output := runCompactDoltDryRunForTest(t, unsupported, false)
		if strings.Contains(output, "Current size:") {
			t.Fatalf("unsupported measurement emitted a text size line:\n%s", output)
		}
		if !strings.Contains(output, "Run without --dry-run") {
			t.Fatalf("dry-run completion guidance missing:\n%s", output)
		}
	})
}

func runCompactDoltDryRunForTest(t *testing.T, candidate storage.DoltStorage, asJSON bool) string {
	t.Helper()

	oldStore, oldDryRun, oldJSON := store, compactDryRun, jsonOutput
	t.Cleanup(func() {
		store, compactDryRun, jsonOutput = oldStore, oldDryRun, oldJSON
	})

	beadsDir := filepath.Join(t.TempDir(), ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatalf("create shared Dolt root: %v", err)
	}
	// This sibling-root content makes the old recursive-walk behavior return a
	// value distinct from the fake active database measurement.
	if err := os.WriteFile(filepath.Join(doltDir, "sibling-data"), make([]byte, 128), 0o644); err != nil {
		t.Fatalf("seed sibling data: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)

	store = candidate
	compactDryRun = true
	jsonOutput = asJSON
	return captureStdout(t, func() error {
		return runCompactDolt(t.Context())
	})
}
