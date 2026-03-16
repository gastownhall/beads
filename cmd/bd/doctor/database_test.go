package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckDatabaseIntegrity(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectMessage  string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No database directory created
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
		{
			name: "empty beads dir",
			setup: func(t *testing.T, dir string) {
				// .beads exists but no dolt/ directory
			},
			expectedStatus: "ok",
			expectMessage:  "N/A (no database)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckDatabaseIntegrity(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q", tt.expectedStatus, check.Status)
			}
			if tt.expectMessage != "" && check.Message != tt.expectMessage {
				t.Errorf("expected message %q, got %q", tt.expectMessage, check.Message)
			}
		})
	}
}

func TestCheckDatabaseVersion(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No dolt/ directory - error (need to run bd init)
			},
			expectedStatus: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckDatabaseVersion(tmpDir, "0.1.0")

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
}

func TestCheckDatabaseVersion_BareParentWorktreeFallback(t *testing.T) {
	bareDir, worktreeDir := setupDoctorBareParentWorktree(t)
	bareBeadsDir := filepath.Join(bareDir, ".beads")
	bareDoltDir := filepath.Join(bareBeadsDir, "dolt")
	if err := os.MkdirAll(bareDoltDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bareBeadsDir, "config.yaml"), []byte("dolt:\n  shared-server: true\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := CheckDatabaseVersion(worktreeDir, "0.1.0")
	if check.Message == "No dolt database found" {
		t.Fatalf("CheckDatabaseVersion() should resolve bare-parent fallback, got message %q", check.Message)
	}
}

func TestCheckSchemaCompatibility(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
	}{
		{
			name: "no database",
			setup: func(t *testing.T, dir string) {
				// No database created
			},
			expectedStatus: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			beadsDir := filepath.Join(tmpDir, ".beads")
			if err := os.MkdirAll(beadsDir, 0755); err != nil {
				t.Fatal(err)
			}

			tt.setup(t, tmpDir)

			check := CheckSchemaCompatibility(tmpDir)

			if check.Status != tt.expectedStatus {
				t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
			}
		})
	}
}

func runDoctorGitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed in %s: %v\n%s", args, dir, err, output)
	}

	return strings.TrimSpace(string(output))
}

func setupDoctorBareParentWorktree(t *testing.T) (string, string) {
	t.Helper()

	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "repo.git")
	mainWorktreeDir := filepath.Join(tmpDir, "main")
	featureWorktreeDir := filepath.Join(tmpDir, "feature")

	runDoctorGitInDir(t, tmpDir, "init", "--bare", bareDir)
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "symbolic-ref", "HEAD", "refs/heads/main")
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "config", "user.email", "test@example.com")
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "config", "user.name", "Test User")
	emptyTree := runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "hash-object", "-t", "tree", "/dev/null")
	initCommit := runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "commit-tree", "-m", "Initial commit", emptyTree)
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "update-ref", "HEAD", initCommit)
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "worktree", "add", mainWorktreeDir, "main")
	runDoctorGitInDir(t, mainWorktreeDir, "branch", "feature")
	runDoctorGitInDir(t, tmpDir, "--git-dir", bareDir, "worktree", "add", featureWorktreeDir, "feature")

	return bareDir, featureWorktreeDir
}
