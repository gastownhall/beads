package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/git"
)

// setupGuardTestRepo creates a git repo with one tracked script and chdirs
// into it. Returns the repo dir.
func setupGuardTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()

	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")

	if err := os.MkdirAll(filepath.Join(repoDir, "scripts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "scripts", "team-pre-push"), []byte("#!/bin/sh\necho team hook\n"), 0755); err != nil {
		t.Fatal(err)
	}
	run("add", "scripts/team-pre-push")
	run("commit", "-m", "add team hook script")

	t.Chdir(repoDir)
	git.ResetCaches()
	return repoDir
}

// bd-5vdt8: a symlinked hook file must refuse installation instead of
// writing through the link into the target (historically a tracked repo
// script, re-dirtying every clone).
func TestInstallHooksRefusesSymlinkedHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	target := filepath.Join(repoDir, "scripts", "team-pre-push")
	originalContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-push")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, hookPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	installErr := installHooksWithOptions(managedHookNames, false, false, false, false)
	if installErr == nil {
		t.Fatal("expected install to refuse symlinked hook, got nil error")
	}
	if !strings.Contains(installErr.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got: %v", installErr)
	}

	afterContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterContent) != string(originalContent) {
		t.Fatalf("symlink target was modified:\n%s", afterContent)
	}

	// Preflight must refuse before writing ANY hook — no partial install.
	for _, name := range managedHookNames {
		if name == "pre-push" {
			continue
		}
		if _, statErr := os.Lstat(filepath.Join(repoDir, ".git", "hooks", name)); !os.IsNotExist(statErr) {
			t.Errorf("hook %s was written despite refusal", name)
		}
	}
}

// bd-5vdt8: a hook file tracked by git (e.g. core.hooksPath pointing into
// the working tree) must refuse installation instead of dirtying the tree.
func TestInstallHooksRefusesTrackedHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	if err := os.MkdirAll(filepath.Join(repoDir, "hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	trackedHook := filepath.Join(repoDir, "hooks", "pre-commit")
	if err := os.WriteFile(trackedHook, []byte("#!/bin/sh\necho tracked hook\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "hooks/pre-commit"},
		{"commit", "-m", "add tracked hook"},
		{"config", "core.hooksPath", "hooks"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	git.ResetCaches()

	installErr := installHooksWithOptions(managedHookNames, false, false, false, false)
	if installErr == nil {
		t.Fatal("expected install to refuse tracked hook, got nil error")
	}
	if !strings.Contains(installErr.Error(), "tracked by git") {
		t.Fatalf("expected tracked-file refusal, got: %v", installErr)
	}
}

// A tracked hook that bd OWNS (section markers) must still be maintainable:
// teams commit .beads/hooks/ like shared .beads-hooks/, and refusing would
// break reinstall/upgrade for them (caught by TestEmbeddedHooks in CI).
func TestInstallHooksAllowsTrackedBdOwnedHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	hooksDir := filepath.Join(repoDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	bdHook := "#!/usr/bin/env sh\n" + generateHookSection("pre-commit")
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(bdHook), 0755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "hooks/pre-commit"},
		{"commit", "-m", "commit bd-managed hook"},
		{"config", "core.hooksPath", "hooks"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v\n%s", args, err, out)
		}
	}
	git.ResetCaches()

	if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("reinstall over a tracked bd-owned hook must succeed, got: %v", err)
	}
}

// bd-5vdt8: injecting the bd section into a hook bd does not own must
// preserve the original as a .backup sidecar.
func TestInstallHooksBacksUpForeignHook(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	userContent := "#!/bin/sh\necho my custom hook\n"
	hookPath := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(userContent), 0755); err != nil {
		t.Fatal(err)
	}

	if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	backup, err := os.ReadFile(hookPath + ".backup")
	if err != nil {
		t.Fatalf("expected .backup sidecar: %v", err)
	}
	if string(backup) != userContent {
		t.Fatalf(".backup does not match original content:\n%s", backup)
	}

	merged, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "echo my custom hook") {
		t.Fatal("user content lost from hook after injection")
	}
	if !strings.Contains(string(merged), hookSectionBeginPrefix) {
		t.Fatal("bd section not injected into hook")
	}

	// Reinstall must not overwrite the backup with the merged content.
	if err := installHooksWithOptions(managedHookNames, false, false, false, false); err != nil {
		t.Fatalf("reinstall failed: %v", err)
	}
	backup2, err := os.ReadFile(hookPath + ".backup")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup2) != userContent {
		t.Fatal(".backup was clobbered on reinstall")
	}
}

// Shared installs (.beads-hooks/) are deliberately committed, so the
// tracked-file guard must not fire for them.
func TestGuardHookWritePathAllowsTrackedWhenShared(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	tracked := filepath.Join(repoDir, "scripts", "team-pre-push")
	if err := guardHookWritePath(tracked, true); err != nil {
		t.Fatalf("expected tracked file to be allowed with allowTracked=true, got: %v", err)
	}
	if err := guardHookWritePath(tracked, false); err == nil {
		t.Fatal("expected tracked file to be refused with allowTracked=false")
	}
}

// The hook-migration apply path shares the same guard: a symlinked hook
// must refuse the migrated write.
func TestApplyHookMigrationRefusesSymlink(t *testing.T) {
	repoDir := setupGuardTestRepo(t)

	target := filepath.Join(repoDir, "scripts", "team-pre-push")
	hooksDir := filepath.Join(repoDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.Symlink(target, hookPath); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}

	plan := hookMigrationExecutionPlan{
		WriteOps: []hookMigrationWriteOp{
			{
				HookName:   "pre-commit",
				HookPath:   hookPath,
				State:      "missing_no_artifacts",
				SourceKind: hookMigrationWriteFromTemplate,
			},
		},
	}
	if _, err := applyHookMigrationExecution(plan); err == nil {
		t.Fatal("expected migration apply to refuse symlinked hook")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got: %v", err)
	}
}
