package daemonrunner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	cfg := Config{
		Interval:   5 * time.Second,
		AutoCommit: true,
		Global:     false,
	}

	daemon := New(cfg, "0.19.0")

	if daemon == nil {
		t.Fatal("Expected non-nil daemon")
	}
	if daemon.cfg.Interval != cfg.Interval {
		t.Errorf("Expected interval %v, got %v", cfg.Interval, daemon.cfg.Interval)
	}
	if daemon.Version != "0.19.0" {
		t.Errorf("Expected version 0.19.0, got %s", daemon.Version)
	}
}

func TestStop(t *testing.T) {
	cfg := Config{
		Interval: 5 * time.Second,
	}
	daemon := New(cfg, "0.19.0")

	// Stop should not error even with no server running
	if err := daemon.Stop(); err != nil {
		t.Errorf("Stop() returned unexpected error: %v", err)
	}
}

func TestDetermineDatabasePath(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	dbPath := filepath.Join(beadsDir, "beads.db")

	if err := os.MkdirAll(beadsDir, 0750); err != nil {
		t.Fatalf("Failed to create beads dir: %v", err)
	}
	if err := os.WriteFile(dbPath, []byte("test"), 0600); err != nil {
		t.Fatalf("Failed to create db file: %v", err)
	}

	cfg := Config{
		WorkspacePath: tmpDir,
	}
	daemon := New(cfg, "0.19.0")

	// Override working directory for test
	oldWd, _ := os.Getwd()
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to change directory: %v", err)
	}

	if err := daemon.determineDatabasePath(); err != nil {
		t.Errorf("determineDatabasePath() failed: %v", err)
	}

	// Use EvalSymlinks to handle /var vs /private/var on macOS
	expectedDB, _ := filepath.EvalSymlinks(dbPath)
	actualDB, _ := filepath.EvalSymlinks(daemon.cfg.DBPath)
	if actualDB != expectedDB {
		t.Errorf("Expected DBPath %s, got %s", expectedDB, actualDB)
	}

	expectedBeadsDir, _ := filepath.EvalSymlinks(beadsDir)
	actualBeadsDir, _ := filepath.EvalSymlinks(daemon.cfg.BeadsDir)
	if actualBeadsDir != expectedBeadsDir {
		t.Errorf("Expected BeadsDir %s, got %s", expectedBeadsDir, actualBeadsDir)
	}

	expectedWS, _ := filepath.EvalSymlinks(tmpDir)
	actualWS, _ := filepath.EvalSymlinks(daemon.cfg.WorkspacePath)
	if actualWS != expectedWS {
		t.Errorf("Expected WorkspacePath %s, got %s", expectedWS, actualWS)
	}
}

func TestDetermineDatabasePathAlreadySet(t *testing.T) {
	existingPath := "/already/set/beads.db"
	cfg := Config{
		DBPath: existingPath,
	}
	daemon := New(cfg, "0.19.0")

	if err := daemon.determineDatabasePath(); err != nil {
		t.Errorf("determineDatabasePath() failed: %v", err)
	}

	if daemon.cfg.DBPath != existingPath {
		t.Errorf("Expected DBPath unchanged: %s, got %s", existingPath, daemon.cfg.DBPath)
	}
}

func TestGetGlobalBeadsDir(t *testing.T) {
	beadsDir, err := getGlobalBeadsDir()
	if err != nil {
		t.Fatalf("getGlobalBeadsDir() failed: %v", err)
	}

	if beadsDir == "" {
		t.Error("Expected non-empty beads directory")
	}

	// Check directory was created
	if stat, err := os.Stat(beadsDir); err != nil {
		t.Errorf("Global beads directory not created: %v", err)
	} else if !stat.IsDir() {
		t.Error("Global beads path is not a directory")
	}

	// Verify it's in home directory
	home, _ := os.UserHomeDir()
	expectedPath := filepath.Join(home, ".beads")
	if beadsDir != expectedPath {
		t.Errorf("Expected %s, got %s", expectedPath, beadsDir)
	}
}
