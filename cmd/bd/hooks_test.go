package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

func TestGetEmbeddedHooks(t *testing.T) {
	hooks, err := getEmbeddedHooks()
	if err != nil {
		t.Fatalf("getEmbeddedHooks() failed: %v", err)
	}

	expectedHooks := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}
	for _, hookName := range expectedHooks {
		content, ok := hooks[hookName]
		if !ok {
			t.Errorf("Missing hook: %s", hookName)
			continue
		}
		if len(content) == 0 {
			t.Errorf("Hook %s has empty content", hookName)
		}
		// Verify it's a shell script
		if content[:2] != "#!" {
			t.Errorf("Hook %s doesn't start with shebang: %s", hookName, content[:50])
		}
	}
}

func TestInstallHooks(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		gitDir := filepath.Join(gitDirPath, "hooks")

		hooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}

		if err := installHooks(hooks, false, false, false); err != nil {
			t.Fatalf("installHooks() failed: %v", err)
		}

		for hookName := range hooks {
			hookPath := filepath.Join(gitDir, hookName)
			if _, err := os.Stat(hookPath); os.IsNotExist(err) {
				t.Errorf("Hook %s was not installed", hookName)
			}
			if runtime.GOOS == "windows" {
				continue
			}

			info, err := os.Stat(hookPath)
			if err != nil {
				t.Errorf("Failed to stat %s: %v", hookName, err)
				continue
			}
			if info.Mode()&0111 == 0 {
				t.Errorf("Hook %s is not executable", hookName)
			}
		}
	})
}

func TestInstallHooksBackup(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		gitDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(gitDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		existingHook := filepath.Join(gitDir, "pre-commit")
		existingContent := "#!/bin/sh\necho old hook\n"
		if err := os.WriteFile(existingHook, []byte(existingContent), 0755); err != nil {
			t.Fatalf("Failed to create existing hook: %v", err)
		}

		hooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}

		if err := installHooks(hooks, false, false, false); err != nil {
			t.Fatalf("installHooks() failed: %v", err)
		}

		backupPath := existingHook + ".backup"
		if _, err := os.Stat(backupPath); os.IsNotExist(err) {
			t.Errorf("Backup was not created")
		}

		backupContent, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("Failed to read backup: %v", err)
		}
		if string(backupContent) != existingContent {
			t.Errorf("Backup content mismatch: got %q, want %q", string(backupContent), existingContent)
		}
	})
}

func TestInstallHooksForce(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		gitDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(gitDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		existingHook := filepath.Join(gitDir, "pre-commit")
		if err := os.WriteFile(existingHook, []byte("old"), 0755); err != nil {
			t.Fatalf("Failed to create existing hook: %v", err)
		}

		hooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}

		if err := installHooks(hooks, true, false, false); err != nil {
			t.Fatalf("installHooks() failed: %v", err)
		}

		backupPath := existingHook + ".backup"
		if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
			t.Errorf("Backup should not have been created with --force")
		}
	})
}

func TestUninstallHooks(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		gitDir := filepath.Join(gitDirPath, "hooks")

		hooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}
		if err := installHooks(hooks, false, false, false); err != nil {
			t.Fatalf("installHooks() failed: %v", err)
		}

		if err := uninstallHooks(); err != nil {
			t.Fatalf("uninstallHooks() failed: %v", err)
		}

		for hookName := range hooks {
			hookPath := filepath.Join(gitDir, hookName)
			if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
				t.Errorf("Hook %s was not removed", hookName)
			}
		}
	})
}

func TestHooksCheckGitHooks(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

		statuses := CheckGitHooks()
		for _, status := range statuses {
			if status.Installed {
				t.Errorf("Hook %s should not be installed initially", status.Name)
			}
		}

		hooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}
		if err := installHooks(hooks, false, false, false); err != nil {
			t.Fatalf("installHooks() failed: %v", err)
		}

		statuses = CheckGitHooks()
		for _, status := range statuses {
			if !status.Installed {
				t.Errorf("Hook %s should be installed", status.Name)
			}
			if !status.IsShim {
				t.Errorf("Hook %s should be a thin shim", status.Name)
			}
			if status.Version != "v1" {
				t.Errorf("Hook %s shim version mismatch: got %s, want v1", status.Name, status.Version)
			}
			if status.Outdated {
				t.Errorf("Hook %s should not be outdated", status.Name)
			}
		}
	})
}

func TestInstallHooksShared(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed (git may not be available): %v", err)
		}

		hooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}

		if err := installHooks(hooks, false, true, false); err != nil {
			t.Fatalf("installHooks() with shared=true failed: %v", err)
		}

		sharedHooksDir := ".beads-hooks"
		for hookName := range hooks {
			hookPath := filepath.Join(sharedHooksDir, hookName)
			if _, err := os.Stat(hookPath); os.IsNotExist(err) {
				t.Errorf("Hook %s was not installed to .beads-hooks/", hookName)
			}
			if runtime.GOOS == "windows" {
				continue
			}

			info, err := os.Stat(hookPath)
			if err != nil {
				t.Errorf("Failed to stat %s: %v", hookName, err)
				continue
			}
			if info.Mode()&0111 == 0 {
				t.Errorf("Hook %s is not executable", hookName)
			}
		}

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		standardHooksDir := filepath.Join(gitDirPath, "hooks")
		for hookName := range hooks {
			hookPath := filepath.Join(standardHooksDir, hookName)
			if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
				t.Errorf("Hook %s should not be in .git/hooks/ when using --shared", hookName)
			}
		}
	})
}

func TestInstallHooksChaining(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		gitDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(gitDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		// Create an existing hook
		existingHook := filepath.Join(gitDir, "pre-commit")
		existingContent := "#!/bin/sh\necho old hook\n"
		if err := os.WriteFile(existingHook, []byte(existingContent), 0755); err != nil {
			t.Fatalf("Failed to create existing hook: %v", err)
		}

		hooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}

		// Install with chain=true
		if err := installHooks(hooks, false, false, true); err != nil {
			t.Fatalf("installHooks() with chain=true failed: %v", err)
		}

		// Verify the original hook was renamed to .old
		oldPath := existingHook + ".old"
		if _, err := os.Stat(oldPath); os.IsNotExist(err) {
			t.Errorf("Existing hook was not renamed to .old for chaining")
		}

		oldContent, err := os.ReadFile(oldPath)
		if err != nil {
			t.Fatalf("Failed to read .old hook: %v", err)
		}
		if string(oldContent) != existingContent {
			t.Errorf(".old hook content mismatch: got %q, want %q", string(oldContent), existingContent)
		}

		// Verify new hook was installed
		if _, err := os.Stat(existingHook); os.IsNotExist(err) {
			t.Errorf("New pre-commit hook was not installed")
		}

		// Verify .backup was NOT created (chain mode uses .old, not .backup)
		backupPath := existingHook + ".backup"
		if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
			t.Errorf("Backup was created but should not be in chain mode")
		}
	})
}

func TestFormatHookWarnings(t *testing.T) {
	tests := []struct {
		name     string
		statuses []HookStatus
		want     string
	}{
		{
			name:     "no issues",
			statuses: []HookStatus{{Name: "pre-commit", Installed: true}},
			want:     "",
		},
		{
			name:     "one missing",
			statuses: []HookStatus{{Name: "pre-commit", Installed: false}},
			want:     "⚠️  Git hooks not installed (1 missing)",
		},
		{
			name: "multiple missing",
			statuses: []HookStatus{
				{Name: "pre-commit", Installed: false},
				{Name: "post-merge", Installed: false},
			},
			want: "⚠️  Git hooks not installed (2 missing)",
		},
		{
			name:     "one outdated",
			statuses: []HookStatus{{Name: "pre-commit", Installed: true, Outdated: true}},
			want:     "⚠️  Git hooks are outdated (1 hooks)",
		},
		{
			name: "mixed missing and outdated",
			statuses: []HookStatus{
				{Name: "pre-commit", Installed: false},
				{Name: "post-merge", Installed: true, Outdated: true},
			},
			want: "⚠️  Git hooks not installed (1 missing)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatHookWarnings(tt.statuses)
			if tt.want == "" && got != "" {
				t.Errorf("FormatHookWarnings() = %q, want empty", got)
			} else if tt.want != "" && !strContains(got, tt.want) {
				t.Errorf("FormatHookWarnings() = %q, want to contain %q", got, tt.want)
			}
		})
	}
}

func strContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || strContains(s[1:], substr)))
}

func TestIsRebaseInProgress(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Create .git directory
	if err := os.MkdirAll(".git", 0755); err != nil {
		t.Fatalf("Failed to create .git: %v", err)
	}

	// Should be false initially
	if isRebaseInProgress() {
		t.Error("isRebaseInProgress() = true, want false (no rebase marker)")
	}

	// Create rebase-merge marker
	if err := os.MkdirAll(".git/rebase-merge", 0755); err != nil {
		t.Fatalf("Failed to create rebase-merge: %v", err)
	}
	if !isRebaseInProgress() {
		t.Error("isRebaseInProgress() = false, want true (rebase-merge exists)")
	}

	// Remove rebase-merge
	if err := os.RemoveAll(".git/rebase-merge"); err != nil {
		t.Fatalf("Failed to remove rebase-merge: %v", err)
	}

	// Create rebase-apply marker
	if err := os.MkdirAll(".git/rebase-apply", 0755); err != nil {
		t.Fatalf("Failed to create rebase-apply: %v", err)
	}
	if !isRebaseInProgress() {
		t.Error("isRebaseInProgress() = false, want true (rebase-apply exists)")
	}
}

func TestHasBeadsJSONL(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	// Should be false initially (no .beads directory)
	if hasBeadsJSONL() {
		t.Error("hasBeadsJSONL() = true, want false (no .beads)")
	}

	// Create .beads directory without any JSONL files
	if err := os.MkdirAll(".beads", 0755); err != nil {
		t.Fatalf("Failed to create .beads: %v", err)
	}
	if hasBeadsJSONL() {
		t.Error("hasBeadsJSONL() = true, want false (no JSONL files)")
	}

	// Create issues.jsonl
	if err := os.WriteFile(".beads/issues.jsonl", []byte("{}"), 0644); err != nil {
		t.Fatalf("Failed to create issues.jsonl: %v", err)
	}
	if !hasBeadsJSONL() {
		t.Error("hasBeadsJSONL() = false, want true (issues.jsonl exists)")
	}
}

// TestInstallHooksChainingSkipsBdShim verifies that bd hooks install --chain
// does NOT rename existing bd shims to .old (which would cause infinite recursion).
// See: https://github.com/steveyegge/beads/issues/843
func TestInstallHooksChainingSkipsBdShim(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		gitDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(gitDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		// Create an existing hook that IS a bd shim
		existingHook := filepath.Join(gitDir, "pre-commit")
		shimContent := "#!/bin/sh\n# bd-shim v1\nexec bd hooks run pre-commit \"$@\"\n"
		if err := os.WriteFile(existingHook, []byte(shimContent), 0755); err != nil {
			t.Fatalf("Failed to create existing shim hook: %v", err)
		}

		hooks, err := getEmbeddedHooks()
		if err != nil {
			t.Fatalf("getEmbeddedHooks() failed: %v", err)
		}

		// Install with chain=true
		if err := installHooks(hooks, false, false, true); err != nil {
			t.Fatalf("installHooks() with chain=true failed: %v", err)
		}

		// Verify the shim was NOT renamed to .old (would cause infinite loop)
		oldPath := existingHook + ".old"
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Errorf("bd shim was renamed to .old - this would cause infinite recursion!")
		}

		// Verify new hook was installed (overwrote the shim)
		if _, err := os.Stat(existingHook); os.IsNotExist(err) {
			t.Errorf("New pre-commit hook was not installed")
		}
	})
}

// TestRunChainedHookSkipsBdShim verifies that runChainedHook() skips
// .old hooks that are bd shims (to prevent infinite recursion).
// See: https://github.com/steveyegge/beads/issues/843
func TestRunChainedHookSkipsBdShim(t *testing.T) {
	tmpDir := t.TempDir()
	runInDir(t, tmpDir, func() {
		if err := exec.Command("git", "init").Run(); err != nil {
			t.Skipf("Skipping test: git init failed: %v", err)
		}

		gitDirPath, err := git.GetGitDir()
		if err != nil {
			t.Fatalf("git.GetGitDir() failed: %v", err)
		}
		gitDir := filepath.Join(gitDirPath, "hooks")
		if err := os.MkdirAll(gitDir, 0750); err != nil {
			t.Fatalf("Failed to create hooks directory: %v", err)
		}

		// Create a .old hook that IS a bd shim (simulating the problematic state)
		oldHook := filepath.Join(gitDir, "pre-commit.old")
		shimContent := "#!/bin/sh\n# bd-shim v1\nexec bd hooks run pre-commit \"$@\"\n"
		if err := os.WriteFile(oldHook, []byte(shimContent), 0755); err != nil {
			t.Fatalf("Failed to create .old shim hook: %v", err)
		}

		// runChainedHook should return 0 (skip the shim) instead of executing it
		exitCode := runChainedHook("pre-commit", nil)
		if exitCode != 0 {
			t.Errorf("runChainedHook() = %d, want 0 (should skip bd shim)", exitCode)
		}
	})
}
