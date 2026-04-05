package beads

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestFindBeadsDir_CwdPriority verifies that a .beads/ directory in cwd takes
// priority over a .beads/ directory at the git worktree root.
//
// Scenario: A "rig" subdirectory has its own .beads/ inside a git worktree
// that also has .beads/ at its root. Before this fix, step 2b
// (git.GetRepoRoot → check .beads/) fired before the cwd walk, grabbing
// the worktree root's .beads/ instead of the rig's local one.
func TestFindBeadsDir_CwdPriority(t *testing.T) {
	// Save and restore env
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create a git repo (simulating the worktree root)
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create root-level .beads/ with project files (the "wrong" one)
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"root_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "config.yaml"), []byte("issue_prefix: root\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a rig subdirectory with its own .beads/ (the "right" one)
	rigDir := filepath.Join(tmpDir, "my-rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"rig_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "config.yaml"), []byte("issue_prefix: rig\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// cd into the rig directory
	t.Chdir(rigDir)

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(rigBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (rig's .beads should win over root's)", result, rigBeadsDir)
	}
}

// TestFindDatabasePath_CwdPriority verifies FindDatabasePath (the database
// discovery path) also prefers cwd's .beads/ over the git worktree root's.
func TestFindDatabasePath_CwdPriority(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	origBeadsDB := os.Getenv("BEADS_DB")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
		if origBeadsDB != "" {
			os.Setenv("BEADS_DB", origBeadsDB)
		} else {
			os.Unsetenv("BEADS_DB")
		}
	})
	os.Unsetenv("BEADS_DIR")
	os.Unsetenv("BEADS_DB")

	tmpDir := t.TempDir()

	// Create a git repo
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create root-level .beads/ with a dolt dir (the "wrong" one)
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	rootDoltDir := filepath.Join(rootBeadsDir, "dolt")
	if err := os.MkdirAll(rootDoltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"root_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create rig subdirectory with its own .beads/ and dolt dir
	rigDir := filepath.Join(tmpDir, "my-rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	rigDoltDir := filepath.Join(rigBeadsDir, "dolt")
	if err := os.MkdirAll(rigDoltDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"rig_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// cd into the rig directory
	t.Chdir(rigDir)

	result := FindDatabasePath()

	// The database path should be under the rig's .beads/, not the root's
	if result == "" {
		t.Fatal("FindDatabasePath() returned empty, expected rig's database path")
	}

	resultResolved, _ := filepath.EvalSymlinks(result)
	rigDirResolved, _ := filepath.EvalSymlinks(rigDir)
	rootBeadsDirResolved, _ := filepath.EvalSymlinks(rootBeadsDir)
	if !isUnder(resultResolved, rigDirResolved) {
		t.Errorf("FindDatabasePath() = %q, want path under %q (rig's .beads should win)", result, rigDirResolved)
	}
	if isUnder(resultResolved, rootBeadsDirResolved) {
		t.Errorf("FindDatabasePath() = %q, should NOT be under root's .beads %q", result, rootBeadsDir)
	}
}

// TestFindBeadsDir_CwdWithoutBeads_FallsBackToWalk verifies that when cwd
// has no .beads/, the normal walk-up behavior still works.
func TestFindBeadsDir_CwdWithoutBeads_FallsBackToWalk(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create a git repo
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create root-level .beads/ only (no rig-level .beads/)
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a subdirectory WITHOUT .beads/
	subDir := filepath.Join(tmpDir, "some", "deep", "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(subDir)

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(rootBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (should fall back to root when cwd has no .beads/)", result, rootBeadsDir)
	}
}

// TestFindBeadsDir_CwdBeadsDirWithRedirect verifies that cwd's .beads/
// redirect is followed when the cwd check fires.
func TestFindBeadsDir_CwdBeadsDirWithRedirect(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create a git repo
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create root-level .beads/
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"root_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a redirect target
	targetBeadsDir := filepath.Join(tmpDir, "shared-beads")
	if err := os.MkdirAll(targetBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"shared_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create rig subdirectory with .beads/ that has a redirect
	rigDir := filepath.Join(tmpDir, "my-rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write redirect file pointing to the shared target
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte(targetBeadsDir), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(rigDir)

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(targetBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (cwd .beads/ redirect should be followed)", result, targetBeadsDir)
	}
}

// TestFindBeadsDir_BEADS_DIR_StillTakesPriority verifies that BEADS_DIR env
// var still takes priority over the cwd check.
func TestFindBeadsDir_BEADS_DIR_StillTakesPriority(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})

	tmpDir := t.TempDir()

	// Create an explicit BEADS_DIR target
	explicitBeadsDir := filepath.Join(tmpDir, "explicit-beads")
	if err := os.MkdirAll(explicitBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(explicitBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"explicit_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Setenv("BEADS_DIR", explicitBeadsDir)

	// Create cwd with its own .beads/
	cwdDir := filepath.Join(tmpDir, "cwd-project")
	cwdBeadsDir := filepath.Join(cwdDir, ".beads")
	if err := os.MkdirAll(cwdBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"cwd_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(cwdDir)

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(explicitBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (BEADS_DIR should still take priority over cwd)", result, explicitBeadsDir)
	}
}

// TestFindBeadsDir_CwdEmptyBeadsDir_SkipsToCwdWalk verifies that when cwd
// has a .beads/ directory without any project files, it's skipped and the
// normal walk-up behavior continues.
func TestFindBeadsDir_CwdEmptyBeadsDir_SkipsToCwdWalk(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create a git repo
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create root-level .beads/ with project files
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create rig with empty .beads/ (no project files)
	rigDir := filepath.Join(tmpDir, "empty-rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No metadata.json, no config.yaml, no dolt/ — empty dir

	t.Chdir(rigDir)

	result := FindBeadsDir()

	// Should fall through to the root's .beads/ since rig's is empty
	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(rootBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (empty cwd .beads/ should be skipped)", result, rootBeadsDir)
	}
}

// TestFindBeadsDir_RigSubdir_WalksUpToRigBeads verifies that when CWD is a
// subdirectory within a rig, the walk-up finds the rig's .beads/ (not the
// git root's .beads/). This is the primary fix for GH#3027 — the old step 1b
// only checked CWD directly, missing rig subdirectories.
func TestFindBeadsDir_RigSubdir_WalksUpToRigBeads(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create a git repo (simulating the main repo root)
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create root-level .beads/ with project files (the "wrong" one)
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"root_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "config.yaml"), []byte("issue_prefix: root\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a rig subdirectory with its own .beads/ (the "right" one)
	rigDir := filepath.Join(tmpDir, "my-rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"rig_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "config.yaml"), []byte("issue_prefix: rig\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a deep subdirectory within the rig (CWD will be here)
	deepDir := filepath.Join(rigDir, "polecats", "furiosa", "src")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// cd into the deep subdirectory within the rig
	t.Chdir(deepDir)

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(rigBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (should walk up from rig subdir to find rig's .beads/)", result, rigBeadsDir)
	}
}

// TestFindBeadsDir_DeepSubdir_NoRig_WalksToGitRoot verifies that when CWD is
// a deep subdirectory with no rig .beads/ in between, the walk-up finds the
// git root's .beads/.
func TestFindBeadsDir_DeepSubdir_NoRig_WalksToGitRoot(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create a git repo
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create root-level .beads/ only
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"root_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a deep subdirectory without any .beads/ in between
	deepDir := filepath.Join(tmpDir, "a", "b", "c", "d")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(deepDir)

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(rootBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (should walk up to git root's .beads/)", result, rootBeadsDir)
	}
}

// TestFindBeadsDir_NestedRigs_FindsNearest verifies that with multiple nested
// rigs, the walk-up finds the nearest (innermost) rig's .beads/.
func TestFindBeadsDir_NestedRigs_FindsNearest(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create a git repo
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Root .beads/
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"root_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Outer rig .beads/
	outerRigDir := filepath.Join(tmpDir, "outer-rig")
	outerBeadsDir := filepath.Join(outerRigDir, ".beads")
	if err := os.MkdirAll(outerBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"outer_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Inner rig .beads/ (nested inside outer)
	innerRigDir := filepath.Join(outerRigDir, "inner-rig")
	innerBeadsDir := filepath.Join(innerRigDir, ".beads")
	if err := os.MkdirAll(innerBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(innerBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"inner_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// CWD is a subdirectory of inner rig
	deepDir := filepath.Join(innerRigDir, "src", "pkg")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(deepDir)

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(innerBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (should find nearest/innermost rig's .beads/)", result, innerBeadsDir)
	}
}

// TestFindBeadsDir_RigSubdirWithRedirect verifies that when CWD is a subdirectory
// within a rig and the rig's .beads/ has a redirect, the redirect is followed.
func TestFindBeadsDir_RigSubdirWithRedirect(t *testing.T) {
	origBeadsDir := os.Getenv("BEADS_DIR")
	t.Cleanup(func() {
		if origBeadsDir != "" {
			os.Setenv("BEADS_DIR", origBeadsDir)
		} else {
			os.Unsetenv("BEADS_DIR")
		}
	})
	os.Unsetenv("BEADS_DIR")

	tmpDir := t.TempDir()

	// Create a git repo
	cmd := exec.Command("git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	// Create root-level .beads/
	rootBeadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(rootBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"root_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create redirect target
	targetBeadsDir := filepath.Join(tmpDir, "shared-beads")
	if err := os.MkdirAll(targetBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetBeadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_database":"shared_db"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create rig with .beads/ that has a redirect
	rigDir := filepath.Join(tmpDir, "my-rig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	if err := os.MkdirAll(rigBeadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte(targetBeadsDir), 0o644); err != nil {
		t.Fatal(err)
	}

	// CWD is a subdirectory within the rig
	subDir := filepath.Join(rigDir, "polecats", "nux")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(subDir)

	result := FindBeadsDir()

	resultResolved, _ := filepath.EvalSymlinks(result)
	expectedResolved, _ := filepath.EvalSymlinks(targetBeadsDir)
	if resultResolved != expectedResolved {
		t.Errorf("FindBeadsDir() = %q, want %q (should follow redirect from rig .beads/ found via walk-up)", result, targetBeadsDir)
	}
}

// isUnder returns true if child is under parent in the directory tree.
func isUnder(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	// rel should not start with ".." (going up) and should not be absolute
	return !filepath.IsAbs(rel) && (rel == "." || (len(rel) >= 2 && rel[:2] != ".."))
}
