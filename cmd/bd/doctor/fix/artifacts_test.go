package fix

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClassicArtifacts_NoArtifacts(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	err := ClassicArtifacts(dir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestClassicArtifacts_RemovesSQLiteWAL(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create WAL/SHM files
	for _, name := range []string{"beads.db-shm", "beads.db-wal"} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	err := ClassicArtifacts(dir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// Verify WAL/SHM files were removed
	for _, name := range []string{"beads.db-shm", "beads.db-wal"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", name)
		}
	}
}

func TestClassicArtifacts_SkipsBeadsDB(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create beads.db (should be skipped)
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	err := ClassicArtifacts(dir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// beads.db should still exist (not safe to delete automatically)
	if _, err := os.Stat(filepath.Join(beadsDir, "beads.db")); os.IsNotExist(err) {
		t.Error("beads.db should NOT have been removed")
	}
}

func TestClassicArtifacts_RemovesBackupDBs(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	backupName := "beads.backup-20260204.db"
	if err := os.WriteFile(filepath.Join(beadsDir, backupName), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	err := ClassicArtifacts(dir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(beadsDir, backupName)); !os.IsNotExist(err) {
		t.Error("backup db should have been removed")
	}
}

func TestClassicArtifacts_CleansJSONLInDoltDir(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create safe-to-delete JSONL artifacts
	for _, name := range []string{"issues.jsonl.new", "beads.left.jsonl"} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(`{"id":"test"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create empty interactions.jsonl
	if err := os.WriteFile(filepath.Join(beadsDir, "interactions.jsonl"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Create issues.jsonl (should be skipped)
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(`{"id":"real"}`), 0644); err != nil {
		t.Fatal(err)
	}

	err := ClassicArtifacts(dir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// Safe files should be removed
	for _, name := range []string{"issues.jsonl.new", "beads.left.jsonl", "interactions.jsonl"} {
		if _, err := os.Stat(filepath.Join(beadsDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", name)
		}
	}

	// issues.jsonl should be kept
	if _, err := os.Stat(filepath.Join(beadsDir, "issues.jsonl")); os.IsNotExist(err) {
		t.Error("issues.jsonl should NOT have been removed")
	}
}

func TestClassicArtifacts_CleansCruftBeadsDir(t *testing.T) {
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "test")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Add redirect (expected)
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}

	// Add .gitkeep (should be preserved)
	if err := os.WriteFile(filepath.Join(beadsDir, ".gitkeep"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Add cruft
	if err := os.WriteFile(filepath.Join(beadsDir, "extra.txt"), []byte("cruft"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(beadsDir, "cruft-subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	err := ClassicArtifacts(dir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// redirect should still exist
	if _, err := os.Stat(filepath.Join(beadsDir, "redirect")); os.IsNotExist(err) {
		t.Error("redirect should NOT have been removed")
	}

	// .gitkeep should still exist
	if _, err := os.Stat(filepath.Join(beadsDir, ".gitkeep")); os.IsNotExist(err) {
		t.Error(".gitkeep should NOT have been removed")
	}

	// cruft should be removed
	if _, err := os.Stat(filepath.Join(beadsDir, "extra.txt")); !os.IsNotExist(err) {
		t.Error("extra.txt should have been removed")
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "cruft-subdir")); !os.IsNotExist(err) {
		t.Error("cruft-subdir should have been removed")
	}
}

func TestClassicArtifacts_CleansCruftWithoutRedirectFile(t *testing.T) {
	// Regression test: when a redirect-expected location has cruft files but
	// NO redirect file, the fix should still clean up the cruft.
	// Previously, the fix required both isRedirectExpected AND hasRedirect,
	// leaving stale files (config.yaml, metadata.json, README.md, issues.jsonl)
	// in .git/beads-worktrees/*/.beads/ directories.
	dir := t.TempDir()

	// Simulate .git/beads-worktrees/master/.beads/ (redirect-expected location)
	worktreeBeads := filepath.Join(dir, ".git", "beads-worktrees", "master", ".beads")
	if err := os.MkdirAll(worktreeBeads, 0755); err != nil {
		t.Fatal(err)
	}

	// Add typical stale files (NO redirect file present)
	cruftFiles := []string{"config.yaml", "metadata.json", "README.md", "issues.jsonl", ".gitignore", ".local_version"}
	for _, name := range cruftFiles {
		if err := os.WriteFile(filepath.Join(worktreeBeads, name), []byte("stale"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	err := ClassicArtifacts(dir)
	if err != nil {
		t.Errorf("expected no error, got: %v", err)
	}

	// All cruft files should be removed
	for _, name := range cruftFiles {
		if _, err := os.Stat(filepath.Join(worktreeBeads, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed (cruft in redirect-expected dir without redirect file)", name)
		}
	}
}

func TestIsRedirectExpectedLocation(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"polecat worktree", "/foo/polecats/obsidian/.beads", true},
		{"crew workspace", "/foo/crew/mel/.beads", true},
		{"refinery rig", "/foo/refinery/rig/.beads", true},
		{"beads-worktrees", "/foo/.git/beads-worktrees/abc/.beads", true},
		{"regular beads dir", "/foo/.beads", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRedirectExpectedLocation(tt.path)
			if got != tt.expected {
				t.Errorf("isRedirectExpectedLocation(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}
