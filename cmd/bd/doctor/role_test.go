package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckBeadsRole_NotConfigured(t *testing.T) {
	// Create a temp directory with git init but no beads.role config
	tmpDir := newGitRepo(t)

	// Check role - should return warning since not configured
	check := CheckBeadsRole(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("expected status %s, got %s", StatusWarning, check.Status)
	}
	if check.Name != "Role Configuration" {
		t.Errorf("expected name 'Role Configuration', got %q", check.Name)
	}
	if check.Fix != "bd config set beads.role maintainer" {
		t.Errorf("expected fix 'bd config set beads.role maintainer', got %q", check.Fix)
	}
}

func TestCheckBeadsRole_Maintainer(t *testing.T) {
	tmpDir := newGitRepo(t)

	// Set beads.role to maintainer
	cmd := exec.Command("git", "config", "beads.role", "maintainer")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config failed: %v", err)
	}

	check := CheckBeadsRole(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "Configured as maintainer" {
		t.Errorf("expected message 'Configured as maintainer', got %q", check.Message)
	}
}

func TestCheckBeadsRole_Contributor(t *testing.T) {
	tmpDir := newGitRepo(t)

	// Set beads.role to contributor
	cmd := exec.Command("git", "config", "beads.role", "contributor")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config failed: %v", err)
	}

	check := CheckBeadsRole(tmpDir)

	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "Configured as contributor" {
		t.Errorf("expected message 'Configured as contributor', got %q", check.Message)
	}
}

func TestCheckBeadsRole_InvalidValue(t *testing.T) {
	tmpDir := newGitRepo(t)

	// Set beads.role to an invalid value
	cmd := exec.Command("git", "config", "beads.role", "admin")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config failed: %v", err)
	}

	check := CheckBeadsRole(tmpDir)

	if check.Status != StatusWarning {
		t.Errorf("expected status %s, got %s", StatusWarning, check.Status)
	}
	if check.Fix != "bd init" {
		t.Errorf("expected fix 'bd init', got %q", check.Fix)
	}
}

func TestCheckBeadsRole_NotGitRepo(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "beads-role-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Don't initialize git - just a plain directory
	check := CheckBeadsRole(tmpDir)

	// Should return OK/N/A since we're not in a git repo — the role may
	// be correctly configured in a worktree (e.g., rig roots use .repo.git).
	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
	}
	if check.Message != "N/A (not a git repository)" {
		t.Errorf("expected message 'N/A (not a git repository)', got %q", check.Message)
	}
}

func TestCheckBeadsRole_NonexistentPath(t *testing.T) {
	// Test with a path that doesn't exist — git will report "not a git repository"
	check := CheckBeadsRole(filepath.Join(os.TempDir(), "nonexistent-beads-test-dir"))

	// Should return OK/N/A since the path is not a git repository
	if check.Status != StatusOK {
		t.Errorf("expected status %s, got %s", StatusOK, check.Status)
	}
}
