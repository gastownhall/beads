//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/beads/internal/configfile"
)

func TestDetectBootstrapAction_NoneWhenDatabaseExists(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	// Create dolt directory with content so it's detected as existing
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(filepath.Join(doltDir, "beads"), 0o750); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "none" {
		t.Errorf("action = %q, want %q", plan.Action, "none")
	}
	if !plan.HasExisting {
		t.Error("HasExisting = false, want true")
	}
}

func TestDetectBootstrapAction_RestoreWhenBackupExists(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	backupDir := filepath.Join(beadsDir, "backup")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupDir, "issues.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "restore" {
		t.Errorf("action = %q, want %q", plan.Action, "restore")
	}
	if plan.BackupDir != backupDir {
		t.Errorf("BackupDir = %q, want %q", plan.BackupDir, backupDir)
	}
}

func TestDetectBootstrapAction_InitWhenNothingExists(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}

	cfg := configfile.DefaultConfig()
	plan := detectBootstrapAction(beadsDir, cfg)

	if plan.Action != "init" {
		t.Errorf("action = %q, want %q", plan.Action, "init")
	}
}
