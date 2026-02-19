package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckClassicArtifacts_NoArtifacts(t *testing.T) {
	dir := t.TempDir()

	// Create a basic .beads directory with no artifacts
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := CheckClassicArtifacts(dir)
	if check.Status != StatusOK {
		t.Errorf("expected StatusOK, got %s: %s", check.Status, check.Message)
	}
}

func TestScanForArtifacts_JSONLInDoltDir(t *testing.T) {
	dir := t.TempDir()

	// Create a .beads directory with dolt/ and stale JSONL files
	beadsDir := filepath.Join(dir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create JSONL artifacts
	// Note: issues.jsonl is NOT an artifact (the pre-commit hook exports
	// Dolt -> JSONL on every git commit so the file is tracked in git).
	for _, name := range []string{"issues.jsonl", "issues.jsonl.new", "beads.left.jsonl"} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(`{"id":"test"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Create empty interactions.jsonl (should be skipped as harmless)
	if err := os.WriteFile(filepath.Join(beadsDir, "interactions.jsonl"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	// issues.jsonl.new and beads.left.jsonl should be found.
	// issues.jsonl is NOT an artifact (it's exported by the pre-commit hook).
	// interactions.jsonl (empty) should be skipped.
	if len(report.JSONLArtifacts) != 2 {
		t.Errorf("expected 2 JSONL artifacts, got %d", len(report.JSONLArtifacts))
		for _, f := range report.JSONLArtifacts {
			t.Logf("  found: %s", f.Path)
		}
	}

	// issues.jsonl should NOT appear at all (it's not an artifact)
	for _, f := range report.JSONLArtifacts {
		if filepath.Base(f.Path) == "issues.jsonl" {
			t.Error("issues.jsonl should NOT be detected as an artifact")
		}
	}

	// issues.jsonl.new should be safe to delete
	for _, f := range report.JSONLArtifacts {
		if filepath.Base(f.Path) == "issues.jsonl.new" && !f.SafeDelete {
			t.Error("issues.jsonl.new should be safe to delete")
		}
	}
}

func TestScanForArtifacts_SQLiteFiles(t *testing.T) {
	dir := t.TempDir()

	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create dolt/ directory so isDoltNative returns true (SQLite files
	// are only flagged as artifacts when Dolt is the active backend)
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create SQLite artifacts
	for _, name := range []string{"beads.db", "beads.db-shm", "beads.db-wal", "beads.backup-20260204.db"} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	report := ScanForArtifacts(dir)

	if len(report.SQLiteArtifacts) != 4 {
		t.Errorf("expected 4 SQLite artifacts, got %d", len(report.SQLiteArtifacts))
	}

	// beads.db should NOT be safe to delete
	for _, f := range report.SQLiteArtifacts {
		if filepath.Base(f.Path) == "beads.db" && f.SafeDelete {
			t.Error("beads.db should NOT be safe to delete")
		}
	}

	// WAL/SHM should be safe
	for _, f := range report.SQLiteArtifacts {
		name := filepath.Base(f.Path)
		if (name == "beads.db-shm" || name == "beads.db-wal") && !f.SafeDelete {
			t.Errorf("%s should be safe to delete", name)
		}
	}

	// Backup should be safe
	for _, f := range report.SQLiteArtifacts {
		name := filepath.Base(f.Path)
		if name == "beads.backup-20260204.db" && !f.SafeDelete {
			t.Error("backup should be safe to delete")
		}
	}
}

func TestScanForArtifacts_CruftBeadsDir(t *testing.T) {
	// Create a structure like: beads/polecats/testpolecat/.beads/ with extra files
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "testpolecat")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Add redirect file (expected)
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}

	// Add cruft files
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	if len(report.CruftBeadsDirs) != 1 {
		t.Errorf("expected 1 cruft beads dir, got %d", len(report.CruftBeadsDirs))
	}

	if len(report.CruftBeadsDirs) > 0 && !report.CruftBeadsDirs[0].SafeDelete {
		t.Error("cruft beads dir with redirect should be safe to delete")
	}
}

func TestScanForArtifacts_CruftBeadsDirNoRedirect(t *testing.T) {
	// Cruft dir without redirect should be detected but NOT safe to delete
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "testpolecat")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// No redirect file, just extra files
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	// Should be detected as cruft (it's in a polecat location) but NOT safe to delete
	if len(report.CruftBeadsDirs) != 1 {
		t.Errorf("expected 1 cruft beads dir, got %d", len(report.CruftBeadsDirs))
	}
	if len(report.CruftBeadsDirs) > 0 && report.CruftBeadsDirs[0].SafeDelete {
		t.Error("cruft beads dir without redirect should NOT be safe to delete")
	}
}

func TestScanForArtifacts_CrewDir(t *testing.T) {
	dir := t.TempDir()
	crewDir := filepath.Join(dir, "crew", "testcrew")
	beadsDir := filepath.Join(crewDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Add redirect and cruft
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "extra.txt"), []byte("cruft"), 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	if len(report.CruftBeadsDirs) != 1 {
		t.Errorf("expected 1 cruft beads dir for crew, got %d", len(report.CruftBeadsDirs))
	}
}

func TestScanForArtifacts_RedirectValidation(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create redirect pointing to nonexistent target
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("/nonexistent/path"), 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	if len(report.RedirectIssues) != 1 {
		t.Errorf("expected 1 redirect issue, got %d", len(report.RedirectIssues))
	}
}

func TestScanForArtifacts_EmptyRedirect(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create empty redirect
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	if len(report.RedirectIssues) != 1 {
		t.Errorf("expected 1 redirect issue (empty), got %d", len(report.RedirectIssues))
	}
}

func TestScanForArtifacts_ValidRedirect(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	targetDir := filepath.Join(dir, "target-beads")

	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create redirect pointing to valid target
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(targetDir), 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	if len(report.RedirectIssues) != 0 {
		t.Errorf("expected 0 redirect issues for valid target, got %d", len(report.RedirectIssues))
	}
}

func TestIsRedirectExpectedDir(t *testing.T) {
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
		{"nested project", "/foo/bar/.beads", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRedirectExpectedDir(tt.path)
			if got != tt.expected {
				t.Errorf("isRedirectExpectedDir(%q) = %v, want %v", tt.path, got, tt.expected)
			}
		})
	}
}

func TestScanForArtifacts_SkipsGitkeep(t *testing.T) {
	dir := t.TempDir()
	polecatsDir := filepath.Join(dir, "polecats", "test")
	beadsDir := filepath.Join(polecatsDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// redirect + .gitkeep only = no cruft
	if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, ".gitkeep"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	if len(report.CruftBeadsDirs) != 0 {
		t.Errorf("expected 0 cruft dirs (redirect + .gitkeep only), got %d", len(report.CruftBeadsDirs))
	}
}

func TestScanForArtifacts_NonEmptyInteractionsJSONL(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Non-empty interactions.jsonl should be detected
	if err := os.WriteFile(filepath.Join(beadsDir, "interactions.jsonl"), []byte(`{"id":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	report := ScanForArtifacts(dir)

	if len(report.JSONLArtifacts) != 1 {
		t.Errorf("expected 1 JSONL artifact (non-empty interactions), got %d", len(report.JSONLArtifacts))
	}
}

func TestCheckClassicArtifacts_WithArtifacts(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create dolt/ directory so isDoltNative returns true
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create a SQLite artifact
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db-wal"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckClassicArtifacts(dir)
	if check.Status != StatusWarning {
		t.Errorf("expected StatusWarning, got %s: %s", check.Status, check.Message)
	}
	if check.Name != "Classic Artifacts" {
		t.Errorf("expected name 'Classic Artifacts', got %q", check.Name)
	}
}
