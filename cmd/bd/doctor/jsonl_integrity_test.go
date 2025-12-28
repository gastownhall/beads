package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckJSONLIntegrity_MalformedLine(t *testing.T) {
	ws := t.TempDir()
	beadsDir := filepath.Join(ws, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte("{\"id\":\"t-1\"}\n{not json}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Ensure DB exists so check suggests auto-repair.
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckJSONLIntegrity(ws)
	if check.Status != StatusError {
		t.Fatalf("expected StatusError, got %v (%s)", check.Status, check.Message)
	}
	if check.Fix == "" {
		t.Fatalf("expected Fix guidance")
	}
}

func TestCheckJSONLIntegrity_NoJSONL(t *testing.T) {
	ws := t.TempDir()
	beadsDir := filepath.Join(ws, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	check := CheckJSONLIntegrity(ws)
	if check.Status != StatusOK {
		t.Fatalf("expected StatusOK, got %v (%s)", check.Status, check.Message)
	}
}
