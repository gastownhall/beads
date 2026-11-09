//go:build cgo

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"path/filepath"
)

// TestEmbeddedInitFromJSONLQuarantinesInvalidRows pins GH#4492 AC-4 on the
// init recovery path: `bd init --from-jsonl` imports the valid rows, keeps
// the invalid ones beside the source, reports the skips, and still succeeds.
func TestEmbeddedInitFromJSONLQuarantinesInvalidRows(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	bd := buildEmbeddedBD(t)
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScreenJSONL(t, beadsDir, "issues.jsonl", screenFixtureLines)

	out := bdRunOK(t, bd, dir, "init", "--prefix", "ce", "--from-jsonl")
	if !strings.Contains(out, "Imported 1 issues") {
		t.Errorf("init --from-jsonl output = %q, want it to import the one valid row", out)
	}
	if !strings.Contains(out, "invalid record") {
		t.Errorf("init --from-jsonl output = %q, want it to report the skipped record", out)
	}

	store := openStore(t, beadsDir, "ce")
	defer store.Close()
	ctx := context.Background()
	if _, err := store.GetIssue(ctx, "ce-1"); err != nil {
		t.Errorf("valid row ce-1 was not imported by init --from-jsonl: %v", err)
	}
	if _, err := store.GetIssue(ctx, "ce-2"); err == nil {
		t.Error("invalid row ce-2 was imported by init --from-jsonl")
	}
	saved, err := os.ReadFile(filepath.Join(beadsDir, "issues.rejected.jsonl"))
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	if strings.TrimSpace(string(saved)) != screenFixtureLines[1] {
		t.Errorf("quarantine = %q, want ce-2's exact bytes", saved)
	}
}
