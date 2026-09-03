//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/formula"
)

func writeFormulaFixture(t *testing.T, p proxiedProject, f *formula.Formula) {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal formula %s: %v", f.Formula, err)
	}
	formulasDir := filepath.Join(p.beadsDir, "formulas")
	if err := os.MkdirAll(formulasDir, 0o755); err != nil {
		t.Fatalf("mkdir formulas dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(formulasDir, f.Formula+formula.FormulaExt), data, 0o644); err != nil {
		t.Fatalf("write formula fixture %s: %v", f.Formula, err)
	}
}
