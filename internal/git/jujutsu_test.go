package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsJujutsuRepo(t *testing.T) {
	// Save original directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	defer func() {
		_ = os.Chdir(origDir)
		ResetCaches()
	}()

	t.Run("not a jj repo", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
		ResetCaches()

		if IsJujutsuRepo() {
			t.Error("Expected IsJujutsuRepo() to return false for non-jj directory")
		}
	})

	t.Run("jj repo root", func(t *testing.T) {
		tmpDir := t.TempDir()
		jjDir := filepath.Join(tmpDir, ".jj")
		if err := os.Mkdir(jjDir, 0750); err != nil {
			t.Fatalf("Failed to create .jj directory: %v", err)
		}

		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
		ResetCaches()

		if !IsJujutsuRepo() {
			t.Error("Expected IsJujutsuRepo() to return true for jj repo root")
		}
	})

	t.Run("jj repo subdirectory", func(t *testing.T) {
		tmpDir := t.TempDir()
		jjDir := filepath.Join(tmpDir, ".jj")
		if err := os.Mkdir(jjDir, 0750); err != nil {
			t.Fatalf("Failed to create .jj directory: %v", err)
		}
		subDir := filepath.Join(tmpDir, "src", "lib")
		if err := os.MkdirAll(subDir, 0750); err != nil {
			t.Fatalf("Failed to create subdirectory: %v", err)
		}

		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
		ResetCaches()

		if !IsJujutsuRepo() {
			t.Error("Expected IsJujutsuRepo() to return true for jj repo subdirectory")
		}
	})
}

func TestIsColocatedJJGit(t *testing.T) {
	// Save original directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	defer func() {
		_ = os.Chdir(origDir)
		ResetCaches()
	}()

	t.Run("jj only (not colocated)", func(t *testing.T) {
		tmpDir := t.TempDir()
		jjDir := filepath.Join(tmpDir, ".jj")
		if err := os.Mkdir(jjDir, 0750); err != nil {
			t.Fatalf("Failed to create .jj directory: %v", err)
		}

		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
		ResetCaches()

		if IsColocatedJJGit() {
			t.Error("Expected IsColocatedJJGit() to return false for jj-only repo")
		}
	})

	t.Run("not a repo", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
		ResetCaches()

		if IsColocatedJJGit() {
			t.Error("Expected IsColocatedJJGit() to return false for non-repo")
		}
	})
}

func TestGetJujutsuRoot(t *testing.T) {
	// Save original directory
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	defer func() {
		_ = os.Chdir(origDir)
		ResetCaches()
	}()

	t.Run("not a jj repo", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
		ResetCaches()

		_, err := GetJujutsuRoot()
		if err == nil {
			t.Error("Expected GetJujutsuRoot() to return error for non-jj directory")
		}
	})

	t.Run("jj repo root", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Resolve symlinks for comparison (macOS /var -> /private/var)
		tmpDir, _ = filepath.EvalSymlinks(tmpDir)

		jjDir := filepath.Join(tmpDir, ".jj")
		if err := os.Mkdir(jjDir, 0750); err != nil {
			t.Fatalf("Failed to create .jj directory: %v", err)
		}

		if err := os.Chdir(tmpDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
		ResetCaches()

		root, err := GetJujutsuRoot()
		if err != nil {
			t.Fatalf("GetJujutsuRoot() returned error: %v", err)
		}
		if root != tmpDir {
			t.Errorf("Expected root %q, got %q", tmpDir, root)
		}
	})

	t.Run("jj repo subdirectory", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Resolve symlinks for comparison (macOS /var -> /private/var)
		tmpDir, _ = filepath.EvalSymlinks(tmpDir)

		jjDir := filepath.Join(tmpDir, ".jj")
		if err := os.Mkdir(jjDir, 0750); err != nil {
			t.Fatalf("Failed to create .jj directory: %v", err)
		}
		subDir := filepath.Join(tmpDir, "src", "lib")
		if err := os.MkdirAll(subDir, 0750); err != nil {
			t.Fatalf("Failed to create subdirectory: %v", err)
		}

		if err := os.Chdir(subDir); err != nil {
			t.Fatalf("Failed to chdir: %v", err)
		}
		ResetCaches()

		root, err := GetJujutsuRoot()
		if err != nil {
			t.Fatalf("GetJujutsuRoot() returned error: %v", err)
		}
		if root != tmpDir {
			t.Errorf("Expected root %q, got %q", tmpDir, root)
		}
	})
}

// TestGetJujutsuRootStopsAtGitBoundary verifies that GetJujutsuRoot() stops
// walking up the directory tree when it encounters a .git directory, even when
// a .jj directory exists above the .git boundary.  This prevents a plain git
// repo nested inside a JJ workspace from inheriting the parent JJ context.
func TestGetJujutsuRootStopsAtGitBoundary(t *testing.T) {
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current directory: %v", err)
	}
	defer func() {
		_ = os.Chdir(origDir)
		ResetCaches()
	}()

	// Directory structure:
	//   tmpDir/
	//   +-- jj-parent/      <- has .jj/ (simulates a JJ workspace root)
	//       +-- nested-git/ <- has .git/ (plain git repo; no .jj at this level)
	//
	// When CWD is nested-git/, GetJujutsuRoot() must stop at the .git boundary
	// and NOT walk up into jj-parent/ where .jj/ lives.
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir) // resolve macOS /var -> /private/var

	jjParent := filepath.Join(tmpDir, "jj-parent")
	if err := os.MkdirAll(jjParent, 0750); err != nil {
		t.Fatalf("Failed to create jj-parent: %v", err)
	}
	if err := os.Mkdir(filepath.Join(jjParent, ".jj"), 0750); err != nil {
		t.Fatalf("Failed to create .jj: %v", err)
	}

	nestedGit := filepath.Join(jjParent, "nested-git")
	if err := os.MkdirAll(nestedGit, 0750); err != nil {
		t.Fatalf("Failed to create nested-git: %v", err)
	}
	if err := os.Mkdir(filepath.Join(nestedGit, ".git"), 0750); err != nil {
		t.Fatalf("Failed to create .git: %v", err)
	}

	if err := os.Chdir(nestedGit); err != nil {
		t.Fatalf("Failed to chdir to nested-git: %v", err)
	}
	ResetCaches()

	root, err := GetJujutsuRoot()
	if err == nil {
		t.Errorf("Expected error: .git boundary should prevent walking to parent .jj, but got root: %q", root)
	}
}
