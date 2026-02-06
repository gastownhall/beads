package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// TestFindDatabaseInBeadsDir_CanonicalDB tests finding the canonical beads.db file
func TestFindDatabaseInBeadsDir_CanonicalDB(t *testing.T) {
	tmpDir := t.TempDir()

	// Create canonical beads.db
	dbPath := filepath.Join(tmpDir, CanonicalDatabaseName)
	if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	result := findDatabaseInBeadsDir(tmpDir, false)

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(dbPath)
	if resultResolved != expectedResolved {
		t.Errorf("findDatabaseInBeadsDir() = %q, want %q", result, dbPath)
	}
}

// TestFindDatabaseInBeadsDir_LegacyDB tests finding legacy database names with warnings
func TestFindDatabaseInBeadsDir_LegacyDB(t *testing.T) {
	for _, legacyName := range LegacyDatabaseNames {
		t.Run(legacyName, func(t *testing.T) {
			tmpDir := t.TempDir()

			dbPath := filepath.Join(tmpDir, legacyName)
			if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
				t.Fatal(err)
			}

			// With warnOnIssues=true should still find the db
			result := findDatabaseInBeadsDir(tmpDir, true)

			resultResolved, _ := filepath.EvalSymlinks(result)
			expectedResolved, _ := filepath.EvalSymlinks(dbPath)
			if resultResolved != expectedResolved {
				t.Errorf("findDatabaseInBeadsDir() = %q, want %q", result, dbPath)
			}
		})
	}
}

// TestFindDatabaseInBeadsDir_MultipleDatabases tests the warning path for multiple DBs
func TestFindDatabaseInBeadsDir_MultipleDatabases(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple .db files (not canonical name)
	for _, name := range []string{"one.db", "two.db"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte{}, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Should find one of them (with warnings to stderr)
	result := findDatabaseInBeadsDir(tmpDir, true)
	if result == "" {
		t.Error("findDatabaseInBeadsDir() returned empty string, expected a db path")
	}
}

// TestFindDatabaseInBeadsDir_NoDatabase tests empty directory
func TestFindDatabaseInBeadsDir_NoDatabase(t *testing.T) {
	tmpDir := t.TempDir()

	result := findDatabaseInBeadsDir(tmpDir, false)
	if result != "" {
		t.Errorf("findDatabaseInBeadsDir() = %q, want empty string", result)
	}
}

// TestFindDatabaseInBeadsDir_SkipsBackupAndVC tests that backup and vc.db files are skipped
func TestFindDatabaseInBeadsDir_SkipsBackupAndVC(t *testing.T) {
	tmpDir := t.TempDir()

	// Create only backup and vc.db files (should be ignored)
	for _, name := range []string{"beads.backup.db", "vc.db"} {
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte{}, 0644); err != nil {
			t.Fatal(err)
		}
	}

	result := findDatabaseInBeadsDir(tmpDir, false)
	if result != "" {
		t.Errorf("findDatabaseInBeadsDir() should skip backup/vc files, got %q", result)
	}
}

// TestFindDatabaseInBeadsDir_WithMetadataJSON tests the metadata.json config path
func TestFindDatabaseInBeadsDir_WithMetadataJSON(t *testing.T) {
	tmpDir := t.TempDir()

	// Create metadata.json with sqlite backend
	metadataContent := `{"backend": "sqlite", "database": "beads.db"}`
	if err := os.WriteFile(filepath.Join(tmpDir, "metadata.json"), []byte(metadataContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create the db file that metadata.json references
	dbPath := filepath.Join(tmpDir, "beads.db")
	if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	result := findDatabaseInBeadsDir(tmpDir, false)

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(dbPath)
	if resultResolved != expectedResolved {
		t.Errorf("findDatabaseInBeadsDir() = %q, want %q", result, dbPath)
	}
}

// TestFindDatabaseInBeadsDir_DoltBackend tests the Dolt backend path in metadata.json
func TestFindDatabaseInBeadsDir_DoltBackend(t *testing.T) {
	tmpDir := t.TempDir()

	// Create metadata.json with dolt backend
	metadataContent := `{"backend": "dolt"}`
	if err := os.WriteFile(filepath.Join(tmpDir, "metadata.json"), []byte(metadataContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Create the dolt directory
	doltDir := filepath.Join(tmpDir, "dolt")
	if err := os.MkdirAll(doltDir, 0755); err != nil {
		t.Fatal(err)
	}

	result := findDatabaseInBeadsDir(tmpDir, false)

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(doltDir)
	if resultResolved != expectedResolved {
		t.Errorf("findDatabaseInBeadsDir() with dolt = %q, want %q", result, doltDir)
	}
}

// TestGetConfiguredBackend tests the GetConfiguredBackend function
func TestGetConfiguredBackend(t *testing.T) {
	t.Run("no config returns sqlite", func(t *testing.T) {
		tmpDir := t.TempDir()
		result := GetConfiguredBackend(tmpDir)
		if result != "sqlite" {
			t.Errorf("GetConfiguredBackend() = %q, want %q", result, "sqlite")
		}
	})

	t.Run("sqlite config", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataContent := `{"backend": "sqlite"}`
		if err := os.WriteFile(filepath.Join(tmpDir, "metadata.json"), []byte(metadataContent), 0644); err != nil {
			t.Fatal(err)
		}
		result := GetConfiguredBackend(tmpDir)
		if result != "sqlite" {
			t.Errorf("GetConfiguredBackend() = %q, want %q", result, "sqlite")
		}
	})

	t.Run("dolt config", func(t *testing.T) {
		tmpDir := t.TempDir()
		metadataContent := `{"backend": "dolt"}`
		if err := os.WriteFile(filepath.Join(tmpDir, "metadata.json"), []byte(metadataContent), 0644); err != nil {
			t.Fatal(err)
		}
		result := GetConfiguredBackend(tmpDir)
		if result != "dolt" {
			t.Errorf("GetConfiguredBackend() = %q, want %q", result, "dolt")
		}
	})
}

// TestFindAllDatabases_Unit tests FindAllDatabases without the integration tag
func TestFindAllDatabases_Unit(t *testing.T) {
	// Save original env
	originalDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalDir != "" {
			os.Setenv("BEADS_DIR", originalDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		git.ResetCaches()
	})
	os.Unsetenv("BEADS_DIR")

	t.Run("finds closest database", func(t *testing.T) {
		tmpDir := t.TempDir()
		tmpDir, _ = filepath.EvalSymlinks(tmpDir)

		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		dbPath := filepath.Join(beadsDir, "beads.db")
		if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
			t.Fatal(err)
		}

		t.Chdir(tmpDir)
		git.ResetCaches()

		databases := FindAllDatabases()
		if len(databases) != 1 {
			t.Fatalf("expected 1 database, got %d", len(databases))
		}
		if databases[0].BeadsDir != beadsDir {
			t.Errorf("BeadsDir = %q, want %q", databases[0].BeadsDir, beadsDir)
		}
	})

	t.Run("no databases returns empty slice", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)
		git.ResetCaches()

		databases := FindAllDatabases()
		if databases == nil {
			t.Error("FindAllDatabases() should never return nil")
		}
		if len(databases) != 0 {
			t.Errorf("expected 0 databases, got %d", len(databases))
		}
	})
}

// TestFindLocalBeadsDir tests the findLocalBeadsDir function
func TestFindLocalBeadsDir_WithBEADS_DIR(t *testing.T) {
	tmpDir := t.TempDir()

	// Save and set BEADS_DIR
	originalDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalDir != "" {
			os.Setenv("BEADS_DIR", originalDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		git.ResetCaches()
	})

	os.Setenv("BEADS_DIR", tmpDir)

	result := findLocalBeadsDir()
	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(tmpDir)
	if resultResolved != expectedResolved {
		t.Errorf("findLocalBeadsDir() = %q, want %q", result, tmpDir)
	}
}

// TestFindLocalBeadsDir_WalksUpTree tests that findLocalBeadsDir walks up the directory tree
func TestFindLocalBeadsDir_WalksUpTree(t *testing.T) {
	originalDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalDir != "" {
			os.Setenv("BEADS_DIR", originalDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		git.ResetCaches()
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create .beads dir at root
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create nested subdir
	subDir := filepath.Join(tmpDir, "a", "b", "c")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(subDir)
	git.ResetCaches()

	result := findLocalBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(beadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("findLocalBeadsDir() = %q, want %q", result, beadsDir)
	}
}

// TestRepoContext_GitCmdCWD tests the GitCmdCWD method
func TestRepoContext_GitCmdCWD(t *testing.T) {
	tmpDir := t.TempDir()
	if err := initGitRepo(tmpDir); err != nil {
		t.Skipf("git not available: %v", err)
	}

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ResetCaches()
		git.ResetCaches()
	})

	rc, err := GetRepoContextForWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("GetRepoContextForWorkspace failed: %v", err)
	}

	// GitCmdCWD should create a command with Dir set to CWDRepoRoot
	cmd := rc.GitCmdCWD(t.Context(), "status")
	if cmd.Dir == "" {
		t.Error("GitCmdCWD().Dir should not be empty when CWDRepoRoot is set")
	}

	resolvedDir, _ := filepath.EvalSymlinks(cmd.Dir)
	resolvedExpected, _ := filepath.EvalSymlinks(rc.CWDRepoRoot)
	if resolvedDir != resolvedExpected {
		t.Errorf("GitCmdCWD().Dir = %q, want %q", cmd.Dir, rc.CWDRepoRoot)
	}

	// The command should actually run
	output, err := cmd.Output()
	if err != nil {
		t.Errorf("GitCmdCWD command failed: %v", err)
	}
	_ = output
}

// TestRepoContext_GitCmdCWD_NoCWDRepo tests GitCmdCWD when CWDRepoRoot is empty
func TestRepoContext_GitCmdCWD_NoCWDRepo(t *testing.T) {
	rc := &RepoContext{
		BeadsDir:    "/some/path/.beads",
		RepoRoot:    "/some/path",
		CWDRepoRoot: "", // Not in a git repo
	}

	cmd := rc.GitCmdCWD(t.Context(), "status")
	if cmd.Dir != "" {
		t.Errorf("GitCmdCWD().Dir should be empty when CWDRepoRoot is empty, got %q", cmd.Dir)
	}
}

// TestRepoContext_RelPath tests the RelPath method
func TestRepoContext_RelPath(t *testing.T) {
	rc := &RepoContext{
		RepoRoot: "/home/user/project",
	}

	tests := []struct {
		name     string
		absPath  string
		expected string
		wantErr  bool
	}{
		{
			name:     "file in .beads",
			absPath:  "/home/user/project/.beads/beads.db",
			expected: ".beads/beads.db",
		},
		{
			name:     "file in subdirectory",
			absPath:  "/home/user/project/src/main.go",
			expected: "src/main.go",
		},
		{
			name:     "repo root itself",
			absPath:  "/home/user/project",
			expected: ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := rc.RelPath(tt.absPath)
			if tt.wantErr {
				if err == nil {
					t.Error("RelPath() should have returned error")
				}
				return
			}
			if err != nil {
				t.Fatalf("RelPath() returned error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("RelPath(%q) = %q, want %q", tt.absPath, result, tt.expected)
			}
		})
	}
}

// TestFindDatabasePath_BEADS_DIR_NoDatabase tests FindDatabasePath when BEADS_DIR is set
// but no database exists in it (--no-db mode)
func TestFindDatabasePath_BEADS_DIR_NoDatabase(t *testing.T) {
	originalDir := os.Getenv("BEADS_DIR")
	originalDB := os.Getenv("BEADS_DB")
	t.Cleanup(func() {
		if originalDir != "" {
			os.Setenv("BEADS_DIR", originalDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		if originalDB != "" {
			os.Setenv("BEADS_DB", originalDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	})
	os.Unsetenv("BEADS_DB")

	tmpDir := t.TempDir()
	// Set BEADS_DIR to an empty directory (no database)
	os.Setenv("BEADS_DIR", tmpDir)

	t.Chdir(tmpDir)

	result := FindDatabasePath()
	// Should return empty when BEADS_DIR has no database
	if result != "" {
		t.Errorf("FindDatabasePath() = %q, want empty string for no-db mode", result)
	}
}

// TestCheckRedirectInDir tests the checkRedirectInDir helper
func TestCheckRedirectInDir(t *testing.T) {
	t.Run("no redirect file", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		info := checkRedirectInDir(beadsDir)
		if info.IsRedirected {
			t.Error("IsRedirected should be false without redirect file")
		}
		if info.LocalDir != beadsDir {
			t.Errorf("LocalDir = %q, want %q", info.LocalDir, beadsDir)
		}
	})

	t.Run("valid redirect", func(t *testing.T) {
		tmpDir := t.TempDir()

		stubDir := filepath.Join(tmpDir, "stub", ".beads")
		if err := os.MkdirAll(stubDir, 0755); err != nil {
			t.Fatal(err)
		}

		targetDir := filepath.Join(tmpDir, "target", ".beads")
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write redirect
		if err := os.WriteFile(filepath.Join(stubDir, "redirect"), []byte(targetDir+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		info := checkRedirectInDir(stubDir)
		if !info.IsRedirected {
			t.Error("IsRedirected should be true with valid redirect")
		}

		targetResolved, _ := filepath.EvalSymlinks(targetDir)
		infoResolved, _ := filepath.EvalSymlinks(info.TargetDir)
		if infoResolved != targetResolved {
			t.Errorf("TargetDir = %q, want %q", info.TargetDir, targetDir)
		}
	})

	t.Run("invalid redirect target", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write redirect to non-existent path
		if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("/nonexistent/path\n"), 0644); err != nil {
			t.Fatal(err)
		}

		info := checkRedirectInDir(beadsDir)
		if info.IsRedirected {
			t.Error("IsRedirected should be false for invalid redirect target")
		}
	})
}

// TestFollowRedirect_ChainPrevention tests that redirect chains are not followed
func TestFollowRedirect_ChainPrevention(t *testing.T) {
	tmpDir := t.TempDir()

	// Create first .beads that redirects to second
	dir1 := filepath.Join(tmpDir, "a", ".beads")
	dir2 := filepath.Join(tmpDir, "b", ".beads")
	if err := os.MkdirAll(dir1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir2, 0755); err != nil {
		t.Fatal(err)
	}

	// dir1 redirects to dir2
	if err := os.WriteFile(filepath.Join(dir1, "redirect"), []byte(dir2+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// dir2 also has a redirect (chain)
	if err := os.WriteFile(filepath.Join(dir2, "redirect"), []byte("/some/other/path\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should follow to dir2 but not further (and log warning about chain)
	result := FollowRedirect(dir1)
	resultResolved, _ := filepath.EvalSymlinks(result)
	dir2Resolved, _ := filepath.EvalSymlinks(dir2)
	if resultResolved != dir2Resolved {
		t.Errorf("FollowRedirect() = %q, want %q (chain should stop at first redirect)", result, dir2)
	}
}

// TestFindDatabaseInTree_WithBEADS_DIR tests findDatabaseInTree with BEADS_DIR set
func TestFindDatabaseInTree_WithBEADS_DIR(t *testing.T) {
	originalDir := os.Getenv("BEADS_DIR")
	originalDB := os.Getenv("BEADS_DB")
	t.Cleanup(func() {
		if originalDir != "" {
			os.Setenv("BEADS_DIR", originalDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		if originalDB != "" {
			os.Setenv("BEADS_DB", originalDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
		git.ResetCaches()
	})
	os.Unsetenv("BEADS_DB")
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	// Init git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create .beads with db
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(beadsDir, "beads.db")
	if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	// Create subdirectory and cd to it
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(subDir)
	git.ResetCaches()

	result := findDatabaseInTree()
	if result == "" {
		t.Error("findDatabaseInTree() returned empty string, expected to find db")
	}

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(dbPath)
	if resultResolved != expectedResolved {
		t.Errorf("findDatabaseInTree() = %q, want %q", result, dbPath)
	}
}

// TestFindBeadsDir_BEADS_DIR_WithProjectFiles tests FindBeadsDir with BEADS_DIR env var
func TestFindBeadsDir_BEADS_DIR_WithProjectFiles(t *testing.T) {
	originalDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if originalDir != "" {
			os.Setenv("BEADS_DIR", originalDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})

	tmpDir := t.TempDir()

	// Create project files in the directory
	if err := os.WriteFile(filepath.Join(tmpDir, "beads.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	os.Setenv("BEADS_DIR", tmpDir)

	result := FindBeadsDir()
	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(tmpDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q", result, tmpDir)
	}
}

// TestNewSQLiteStorage_Unit tests NewSQLiteStorage creates and opens a database
func TestNewSQLiteStorage_Unit(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteStorage(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStorage() returned error: %v", err)
	}
	defer store.Close()

	// Verify store is usable
	if store == nil {
		t.Fatal("NewSQLiteStorage() returned nil store")
	}
}
