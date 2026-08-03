package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUninstallHooksUnsetsBeadsRole verifies AC2: bd hooks uninstall clears
// beads.role in addition to core.hooksPath, so a manual `rm -rf .beads/`
// followed by a proper uninstall doesn't leave stale beads-managed git
// config behind (GH#4440).
func TestUninstallHooksUnsetsBeadsRole(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		cmd := exec.Command("git", "config", "beads.role", "primary")
		cmd.Dir = tmpDir
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to set beads.role: %v", err)
		}

		if err := uninstallHooks(); err != nil {
			t.Fatalf("uninstallHooks() failed: %v", err)
		}

		getCmd := exec.Command("git", "config", "--get", "beads.role")
		getCmd.Dir = tmpDir
		out, err := getCmd.Output()
		if err == nil {
			t.Errorf("expected beads.role to be unset after uninstallHooks(), got %q", strings.TrimSpace(string(out)))
		}
	})
}

// TestUninstallHooksNoBeadsRoleIsNotAnError verifies that an already-absent
// beads.role is treated as success, not surfaced as a failure.
func TestUninstallHooksNoBeadsRoleIsNotAnError(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		if err := uninstallHooks(); err != nil {
			t.Fatalf("uninstallHooks() failed when beads.role was never set: %v", err)
		}
	})
}

// A duplicated beads.role makes git refuse the unset as ambiguous — and it
// exits 5 to say so, the same code it uses for "key not set". Reading the key
// before unsetting is what keeps the two apart; treating exit 5 as success
// would report a clean uninstall with the key still set, which is precisely
// the failure AC2 exists to prevent.
func TestUninstallHooksReportsAmbiguousBeadsRole(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		for _, value := range []string{"primary", "secondary"} {
			cmd := exec.Command("git", "config", "--add", "beads.role", value)
			cmd.Dir = tmpDir
			if err := cmd.Run(); err != nil {
				t.Fatalf("failed to add beads.role=%s: %v", value, err)
			}
		}

		err := uninstallHooks()
		if err == nil {
			t.Fatal("uninstallHooks() = nil, want an error: git cannot unset a multi-valued beads.role")
		}
		if !strings.Contains(err.Error(), "beads.role") {
			t.Errorf("error %q does not name beads.role", err)
		}

		// And the key really is still set — the error was not spurious.
		getCmd := exec.Command("git", "config", "--get-all", "beads.role")
		getCmd.Dir = tmpDir
		out, getErr := getCmd.Output()
		if getErr != nil {
			t.Fatalf("expected beads.role to still be set, --get-all failed: %v", getErr)
		}
		if len(strings.Fields(string(out))) != 2 {
			t.Errorf("beads.role values = %q, want both still present", strings.TrimSpace(string(out)))
		}
	})
}

// TestResetHooksPathIfBeadsManagedReportsFailureLoudly verifies AC2: when
// the underlying git config command genuinely fails, resetHooksPathIfBeadsManaged
// returns a non-nil error (and thus uninstallHooks propagates it) instead of
// silently printing a scrolling stderr warning and reporting success.
func TestResetHooksPathIfBeadsManagedReportsFailureLoudly(t *testing.T) {
	tmpDir := newGitRepo(t)
	runInDir(t, tmpDir, func() {
		gitDir := filepath.Join(tmpDir, ".git")
		info, err := os.Stat(gitDir)
		if err != nil {
			t.Fatalf("failed to stat .git: %v", err)
		}
		orig := info.Mode()

		// There has to be something to unset, or there is no git invocation to
		// fail: the reset reads beads.role first and only unsets it when it is
		// actually present.
		setCmd := exec.Command("git", "config", "beads.role", "primary")
		setCmd.Dir = tmpDir
		if err := setCmd.Run(); err != nil {
			t.Fatalf("failed to set beads.role: %v", err)
		}

		// Make .git/ read+execute only (no write), so `git rev-parse` (used to
		// resolve repoRoot) and `git config --get` still succeed but
		// `git config --unset` cannot create its lockfile — a genuine failure,
		// which resetHooksPathIfBeadsManaged must not swallow.
		if err := os.Chmod(gitDir, 0555); err != nil {
			t.Fatalf("failed to chmod .git: %v", err)
		}
		t.Cleanup(func() {
			_ = os.Chmod(gitDir, orig)
		})

		err = resetHooksPathIfBeadsManaged()
		if err == nil {
			t.Fatal("expected resetHooksPathIfBeadsManaged to return an error when the config lockfile cannot be created")
		}
		if !strings.Contains(err.Error(), "beads.role") {
			t.Errorf("error %q does not name the key it failed to unset", err)
		}
	})
}
