package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupGitRepo creates a temporary git repository for testing
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create .beads directory
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Use cached git template instead of spawning git init per test
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	return dir
}

func TestCheckGitHooks(t *testing.T) {
	// This test needs to run in a git repository
	// We test the basic case where hooks are not installed
	t.Run("not in git repo returns N/A", func(t *testing.T) {
		tmpDir := t.TempDir()
		runInDir(t, tmpDir, func() {
			check := CheckGitHooks("0.49.6")

			if check.Status != StatusOK {
				t.Errorf("expected status %q, got %q", StatusOK, check.Status)
			}
			if check.Message != "N/A (not a git repository)" {
				t.Errorf("unexpected message: %s", check.Message)
			}
		})
	})
}

// setupGitRepoInDir initializes a git repo in the given directory with .beads
func setupGitRepoInDir(t *testing.T, dir string) {
	t.Helper()

	// Create .beads directory
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("failed to create .beads directory: %v", err)
	}

	// Use cached git template instead of spawning git init per test
	initGitTemplate()
	if gitTemplateErr != nil {
		t.Fatalf("git template init failed: %v", gitTemplateErr)
	}
	if err := copyGitDir(gitTemplateDir, dir); err != nil {
		t.Fatalf("failed to copy git template: %v", err)
	}

	// Force repo-local hooks path for test isolation. This prevents global
	// core.hooksPath from affecting hook detection behavior in tests.
	cmd := exec.Command("git", "config", "core.hooksPath", ".git/hooks")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set core.hooksPath for test repo: %v", err)
	}
}

// Edge case tests for CheckGitHooks

func TestCheckGitHooks_CorruptedHookFiles(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(t *testing.T, dir string)
		expectedStatus string
		expectInMsg    string
	}{
		{
			name: "pre-commit hook is directory instead of file",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				// create pre-commit as directory instead of file
				os.MkdirAll(filepath.Join(hooksDir, "pre-commit"), 0755)
				// create valid post-merge and pre-push hooks
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte("#!/bin/sh\nbd sync\n"), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte("#!/bin/sh\nbd sync\n"), 0755)
			},
			// os.Stat reports directories as existing, so CheckGitHooks sees it as installed
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "hook file with no execute permissions",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				// create hooks but with no execute permissions
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte("#!/bin/sh\nbd sync\n"), 0644)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte("#!/bin/sh\nbd sync\n"), 0644)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte("#!/bin/sh\nbd sync\n"), 0644)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "empty hook file",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				// create empty hook files
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(""), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(""), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(""), 0755)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "hook file with binary content",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				// create hooks with binary content
				binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), binaryContent, 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), binaryContent, 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), binaryContent, 0755)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "outdated bd hook versions are flagged",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				oldHook := "#!/bin/sh\n# bd-hooks-version: 0.49.1\nbd hooks run pre-push\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(oldHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(oldHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(oldHook), 0755)
			},
			expectedStatus: "warning",
			expectInMsg:    "outdated",
		},
		{
			name: "current bd hook versions are accepted",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				currentHook := "#!/bin/sh\n# bd-hooks-version: 0.49.6\nbd hooks run pre-push\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(currentHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(currentHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(currentHook), 0755)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "marker-managed hooks at current version are accepted (GH#2244)",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				markerHook := "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\n" +
					"# This section is managed by beads. Do not remove these markers.\n" +
					"if command -v bd >/dev/null 2>&1; then\n" +
					"  export BD_GIT_HOOK=1\n" +
					"  bd hooks run pre-commit \"$@\"\n" +
					"  _bd_exit=$?; if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi\n" +
					"fi\n" +
					"# --- END BEADS INTEGRATION ---\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(markerHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(markerHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(markerHook), 0755)
			},
			expectedStatus: "ok",
			expectInMsg:    "All recommended hooks installed",
		},
		{
			name: "marker-managed hooks at older version are flagged outdated",
			setup: func(t *testing.T, dir string) {
				setupGitRepoInDir(t, dir)
				gitDir := filepath.Join(dir, ".git")
				hooksDir := filepath.Join(gitDir, "hooks")
				os.MkdirAll(hooksDir, 0755)
				markerHook := "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.49.1 ---\n" +
					"if command -v bd >/dev/null 2>&1; then\n" +
					"  bd hooks run pre-commit \"$@\"\n" +
					"fi\n" +
					"# --- END BEADS INTEGRATION ---\n"
				os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(markerHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "post-merge"), []byte(markerHook), 0755)
				os.WriteFile(filepath.Join(hooksDir, "pre-push"), []byte(markerHook), 0755)
			},
			expectedStatus: "warning",
			expectInMsg:    "outdated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tt.setup(t, tmpDir)

			runInDir(t, tmpDir, func() {
				check := CheckGitHooks("0.49.6")

				if check.Status != tt.expectedStatus {
					t.Errorf("expected status %q, got %q (message: %s)", tt.expectedStatus, check.Status, check.Message)
				}
				if tt.expectInMsg != "" && !strings.Contains(check.Message, tt.expectInMsg) {
					t.Errorf("expected message to contain %q, got %q", tt.expectInMsg, check.Message)
				}
			})
		})
	}
}

// Tests for CheckOrphanedIssues

// TestParseBDHookVersion verifies version extraction from all hook formats (GH#2244).
func TestParseBDHookVersion(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantVersion string
		wantOK      bool
	}{
		{
			name:        "legacy bd-hooks-version comment",
			content:     "#!/bin/sh\n# bd-hooks-version: 0.55.0\nbd hooks run pre-commit\n",
			wantVersion: "0.55.0",
			wantOK:      true,
		},
		{
			name:        "section marker format",
			content:     "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hooks run pre-commit\n# --- END BEADS INTEGRATION ---\n",
			wantVersion: "0.57.0",
			wantOK:      true,
		},
		{
			name:        "section marker with user content before",
			content:     "#!/bin/sh\n# my custom stuff\necho hello\n# --- BEGIN BEADS INTEGRATION v0.56.1 ---\nbd hooks run pre-commit\n# --- END BEADS INTEGRATION ---\n",
			wantVersion: "0.56.1",
			wantOK:      true,
		},
		{
			name:    "no version markers at all",
			content: "#!/bin/sh\necho hello\n",
			wantOK:  false,
		},
		{
			name:    "empty content",
			content: "",
			wantOK:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, ok := parseBDHookVersion(tt.content)
			if ok != tt.wantOK {
				t.Errorf("parseBDHookVersion() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && version != tt.wantVersion {
				t.Errorf("parseBDHookVersion() version = %q, want %q", version, tt.wantVersion)
			}
		})
	}
}

// TestIsBdHookContent verifies detection of all bd hook formats (GH#2244).
func TestIsBdHookContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"shim marker", "#!/bin/sh\n# bd-shim 0.55.0\nbd hooks run pre-commit\n", true},
		{"inline marker", "#!/bin/sh\n# bd (beads)\nbd sync\n", true},
		{"section marker", "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hooks run pre-commit\n# --- END BEADS INTEGRATION ---\n", true},
		{"bd hooks run call", "#!/bin/sh\nbd hooks run pre-commit\n", true},
		{"not a bd hook", "#!/bin/sh\necho hello\n", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBdHookContent(tt.content)
			if got != tt.want {
				t.Errorf("isBdHookContent() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestCheckOrphanedIssues_DoltBackend verifies that CheckOrphanedIssues returns
// N/A for the Dolt backend (orphan detection not yet reimplemented for Dolt).
func TestCheckOrphanedIssues_DoltBackend(t *testing.T) {
	check := CheckOrphanedIssues(t.TempDir())

	if check.Status != StatusOK {
		t.Errorf("expected status %q, got %q", StatusOK, check.Status)
	}
	if !strings.Contains(check.Message, "N/A") {
		t.Errorf("expected N/A message, got %q", check.Message)
	}
}

func TestCheckStaleLegacyHooks(t *testing.T) {
	t.Run("no legacy files", func(t *testing.T) {
		dir := t.TempDir()
		setupGitRepoInDir(t, dir)
		runInDir(t, dir, func() {
			check := CheckStaleLegacyHooks()
			if check.Status != StatusOK {
				t.Errorf("expected OK, got %q: %s", check.Status, check.Message)
			}
		})
	})

	t.Run("stale legacy hook with removed bd hook command", func(t *testing.T) {
		dir := t.TempDir()
		setupGitRepoInDir(t, dir)
		hooksDir := filepath.Join(dir, ".git", "hooks")
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit.legacy"),
			"#!/bin/sh\nexec bd hook pre-commit \"$@\"\n")
		runInDir(t, dir, func() {
			check := CheckStaleLegacyHooks()
			if check.Status != StatusWarning {
				t.Errorf("expected warning, got %q: %s", check.Status, check.Message)
			}
			if !strings.Contains(check.Detail, "pre-commit.legacy") {
				t.Errorf("expected detail to mention pre-commit.legacy, got %q", check.Detail)
			}
		})
	})

	t.Run("legacy hook with current bd hooks run command is OK", func(t *testing.T) {
		dir := t.TempDir()
		setupGitRepoInDir(t, dir)
		hooksDir := filepath.Join(dir, ".git", "hooks")
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit.legacy"),
			"#!/bin/sh\nbd hooks run pre-commit \"$@\"\n")
		runInDir(t, dir, func() {
			check := CheckStaleLegacyHooks()
			if check.Status != StatusOK {
				t.Errorf("expected OK, got %q: %s", check.Status, check.Message)
			}
		})
	})

	t.Run("not in git repo", func(t *testing.T) {
		dir := t.TempDir()
		runInDir(t, dir, func() {
			check := CheckStaleLegacyHooks()
			if check.Status != StatusOK {
				t.Errorf("expected OK, got %q", check.Status)
			}
		})
	})
}
