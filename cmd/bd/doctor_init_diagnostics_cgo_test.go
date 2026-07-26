//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunInitDiagnosticsServerModeWithoutLocalDoltDir(t *testing.T) {
	projectDir := t.TempDir()
	doltPath := filepath.Join(projectDir, ".beads", "dolt")
	store := newTestStoreIsolatedDB(t, doltPath, "initdiag")
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}

	if info, err := os.Stat(doltPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected no local .beads/dolt directory, got info=%v err=%v", info, err)
	}

	result := runInitDiagnostics(projectDir)
	for _, check := range result.Checks {
		if check.Message == "No dolt database found" {
			t.Fatalf("runInitDiagnostics reported false missing database: %+v", check)
		}
	}
	if !result.OverallOK {
		t.Fatalf("runInitDiagnostics OverallOK=false: %+v", result.Checks)
	}
}
