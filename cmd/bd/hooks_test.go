package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
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
	// Create temp directory and init git repo
	tmpDir := t.TempDir()

	// Change to temp directory
	t.Chdir(tmpDir)

	// Initialize a real git repo (required for git rev-parse)
	if err := exec.Command("git", "init").Run(); err != nil {
		t.Skipf("Skipping test: git init failed: %v", err)
	}

	gitDir := filepath.Join(tmpDir, ".git", "hooks")

	// Get embedded hooks
	hooks, err := getEmbeddedHooks()
	if err != nil {
		t.Fatalf("getEmbeddedHooks() failed: %v", err)
	}

	// Install hooks
	if err := installHooks(hooks, false, false); err != nil {
		t.Fatalf("installHooks() failed: %v", err)
	}

	// Verify hooks were installed
	for hookName := range hooks {
		hookPath := filepath.Join(gitDir, hookName)
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			t.Errorf("Hook %s was not installed", hookName)
		}
		// Windows does not support POSIX executable bits, so skip the check there.
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
}

func TestInstallHooksBackup(t *testing.T) {
	// Create temp directory and init git repo
	tmpDir := t.TempDir()

	// Change to temp directory
	t.Chdir(tmpDir)

	// Initialize a real git repo (required for git rev-parse)
	if err := exec.Command("git", "init").Run(); err != nil {
		t.Skipf("Skipping test: git init failed: %v", err)
	}

	gitDir := filepath.Join(tmpDir, ".git", "hooks")

	// Create an existing hook
	existingHook := filepath.Join(gitDir, "pre-commit")
	existingContent := "#!/bin/sh\necho old hook\n"
	if err := os.WriteFile(existingHook, []byte(existingContent), 0755); err != nil {
		t.Fatalf("Failed to create existing hook: %v", err)
	}

	// Get embedded hooks
	hooks, err := getEmbeddedHooks()
	if err != nil {
		t.Fatalf("getEmbeddedHooks() failed: %v", err)
	}

	// Install hooks (should backup existing)
	if err := installHooks(hooks, false, false); err != nil {
		t.Fatalf("installHooks() failed: %v", err)
	}

	// Verify backup was created
	backupPath := existingHook + ".backup"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("Backup was not created")
	}

	// Verify backup has original content
	backupContent, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("Failed to read backup: %v", err)
	}
	if string(backupContent) != existingContent {
		t.Errorf("Backup content mismatch: got %q, want %q", string(backupContent), existingContent)
	}
}

func TestInstallHooksForce(t *testing.T) {
	// Create temp directory and init git repo
	tmpDir := t.TempDir()

	// Change to temp directory first, then init
	t.Chdir(tmpDir)

	// Initialize a real git repo (required for git rev-parse)
	if err := exec.Command("git", "init").Run(); err != nil {
		t.Skipf("Skipping test: git init failed: %v", err)
	}

	gitDir := filepath.Join(tmpDir, ".git", "hooks")

	// Create an existing hook
	existingHook := filepath.Join(gitDir, "pre-commit")
	if err := os.WriteFile(existingHook, []byte("old"), 0755); err != nil {
		t.Fatalf("Failed to create existing hook: %v", err)
	}

	// Get embedded hooks
	hooks, err := getEmbeddedHooks()
	if err != nil {
		t.Fatalf("getEmbeddedHooks() failed: %v", err)
	}

	// Install hooks with force (should not create backup)
	if err := installHooks(hooks, true, false); err != nil {
		t.Fatalf("installHooks() failed: %v", err)
	}

	// Verify no backup was created
	backupPath := existingHook + ".backup"
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Errorf("Backup should not have been created with --force")
	}
}

func TestUninstallHooks(t *testing.T) {
	// Create temp directory and init git repo
	tmpDir := t.TempDir()

	// Change to temp directory first, then init
	t.Chdir(tmpDir)

	// Initialize a real git repo (required for git rev-parse)
	if err := exec.Command("git", "init").Run(); err != nil {
		t.Skipf("Skipping test: git init failed: %v", err)
	}

	gitDir := filepath.Join(tmpDir, ".git", "hooks")

	// Get embedded hooks and install them
	hooks, err := getEmbeddedHooks()
	if err != nil {
		t.Fatalf("getEmbeddedHooks() failed: %v", err)
	}
	if err := installHooks(hooks, false, false); err != nil {
		t.Fatalf("installHooks() failed: %v", err)
	}

	// Uninstall hooks
	if err := uninstallHooks(); err != nil {
		t.Fatalf("uninstallHooks() failed: %v", err)
	}

	// Verify hooks were removed
	hookNames := []string{"pre-commit", "post-merge", "pre-push", "post-checkout"}
	for _, hookName := range hookNames {
		hookPath := filepath.Join(gitDir, hookName)
		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			t.Errorf("Hook %s was not removed", hookName)
		}
	}
}

func TestHooksCheckGitHooks(t *testing.T) {
	// Create temp directory and init git repo
	tmpDir := t.TempDir()

	// Change to temp directory first, then init
	t.Chdir(tmpDir)

	// Initialize a real git repo (required for git rev-parse)
	if err := exec.Command("git", "init").Run(); err != nil {
		t.Skipf("Skipping test: git init failed: %v", err)
	}

	// Initially no hooks installed
	statuses := CheckGitHooks()

	for _, status := range statuses {
		if status.Installed {
			t.Errorf("Hook %s should not be installed initially", status.Name)
		}
	}

	// Install hooks
	hooks, err := getEmbeddedHooks()
	if err != nil {
		t.Fatalf("getEmbeddedHooks() failed: %v", err)
	}
	if err := installHooks(hooks, false, false); err != nil {
		t.Fatalf("installHooks() failed: %v", err)
	}

	// Check again
	statuses = CheckGitHooks()

	for _, status := range statuses {
		if !status.Installed {
			t.Errorf("Hook %s should be installed", status.Name)
		}
		if status.Version != Version {
			t.Errorf("Hook %s version mismatch: got %s, want %s", status.Name, status.Version, Version)
		}
		if status.Outdated {
			t.Errorf("Hook %s should not be outdated", status.Name)
		}
	}
}

func TestInstallHooksShared(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Change to temp directory
	t.Chdir(tmpDir)

	// Initialize a real git repo (needed for git config command)
	if err := exec.Command("git", "init").Run(); err != nil {
		t.Skipf("Skipping test: git init failed (git may not be available): %v", err)
	}

	// Get embedded hooks
	hooks, err := getEmbeddedHooks()
	if err != nil {
		t.Fatalf("getEmbeddedHooks() failed: %v", err)
	}

	// Install hooks in shared mode
	if err := installHooks(hooks, false, true); err != nil {
		t.Fatalf("installHooks() with shared=true failed: %v", err)
	}

	// Verify hooks were installed to .beads-hooks/
	sharedHooksDir := ".beads-hooks"
	for hookName := range hooks {
		hookPath := filepath.Join(sharedHooksDir, hookName)
		if _, err := os.Stat(hookPath); os.IsNotExist(err) {
			t.Errorf("Hook %s was not installed to .beads-hooks/", hookName)
		}
		// Windows does not support POSIX executable bits, so skip the check there.
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

	// Verify hooks were NOT installed to .git/hooks/
	standardHooksDir := filepath.Join(".git", "hooks")
	for hookName := range hooks {
		hookPath := filepath.Join(standardHooksDir, hookName)
		if _, err := os.Stat(hookPath); !os.IsNotExist(err) {
			t.Errorf("Hook %s should not be in .git/hooks/ when using --shared", hookName)
		}
	}
}
