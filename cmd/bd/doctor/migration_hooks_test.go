package doctor

import (
	"path/filepath"
	"testing"
)

func TestDetectPendingMigrations_Hooks(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir)
	forceRepoHooksPath(t, tmpDir)

	_, hooksDir, err := resolveGitHooksDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookFile(t, filepath.Join(hooksDir, "pre-commit.old"), "#!/bin/sh\necho old\n")

	pending := DetectPendingMigrations(tmpDir)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending migration, got %d", len(pending))
	}

	m := pending[0]
	if m.Name != "hooks" {
		t.Fatalf("expected migration name 'hooks', got %q", m.Name)
	}
	if m.Command != "bd migrate hooks --dry-run" {
		t.Fatalf("expected command 'bd migrate hooks --dry-run', got %q", m.Command)
	}
	if m.Priority != 2 {
		t.Fatalf("expected recommended priority 2, got %d", m.Priority)
	}
}

func TestDetectPendingMigrations_HooksBrokenMarkerIsWarningInPhase1(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir)
	forceRepoHooksPath(t, tmpDir)

	_, hooksDir, err := resolveGitHooksDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n")

	pending := DetectPendingMigrations(tmpDir)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending migration, got %d", len(pending))
	}
	if pending[0].Priority != 2 {
		t.Fatalf("expected warning priority 2 in phase 1, got %d", pending[0].Priority)
	}

	check := CheckPendingMigrations(tmpDir)
	if check.Status != StatusWarning {
		t.Fatalf("expected warning status for phase 1 planning-only migration, got %q", check.Status)
	}
}
