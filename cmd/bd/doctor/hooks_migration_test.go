package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanHookMigration_NotGitRepo(t *testing.T) {
	tmpDir := t.TempDir()

	plan, err := PlanHookMigration(tmpDir)
	if err != nil {
		t.Fatalf("PlanHookMigration returned error: %v", err)
	}

	if plan.IsGitRepo {
		t.Fatalf("expected IsGitRepo=false, got true")
	}
	if plan.NeedsMigrationCount != 0 {
		t.Fatalf("expected no migrations, got %d", plan.NeedsMigrationCount)
	}
}

func TestPlanHookMigration_LegacyWithOldSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir)
	forceRepoHooksPath(t, tmpDir)

	_, hooksDir, err := resolveGitHooksDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookFile(t, filepath.Join(hooksDir, "pre-commit.old"), "#!/bin/sh\necho legacy\n")

	plan, err := PlanHookMigration(tmpDir)
	if err != nil {
		t.Fatalf("PlanHookMigration returned error: %v", err)
	}

	hook, ok := findHookPlan(plan, "pre-commit")
	if !ok {
		t.Fatalf("pre-commit hook not found in plan")
	}

	if !hook.NeedsMigration {
		t.Fatalf("expected pre-commit to need migration")
	}
	if hook.State != "legacy_with_old_sidecar" {
		t.Fatalf("expected state legacy_with_old_sidecar, got %q", hook.State)
	}
	if plan.NeedsMigrationCount != 1 {
		t.Fatalf("expected 1 migration, got %d", plan.NeedsMigrationCount)
	}
}

func TestPlanHookMigration_MarkerManaged(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir)
	forceRepoHooksPath(t, tmpDir)

	_, hooksDir, err := resolveGitHooksDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n# --- END BEADS INTEGRATION v0.57.0 ---\n")

	plan, err := PlanHookMigration(tmpDir)
	if err != nil {
		t.Fatalf("PlanHookMigration returned error: %v", err)
	}

	hook, ok := findHookPlan(plan, "pre-commit")
	if !ok {
		t.Fatalf("pre-commit hook not found in plan")
	}

	if hook.NeedsMigration {
		t.Fatalf("expected pre-commit to be managed already")
	}
	if hook.State != "marker_managed" {
		t.Fatalf("expected state marker_managed, got %q", hook.State)
	}
	if plan.NeedsMigrationCount != 0 {
		t.Fatalf("expected no migrations, got %d", plan.NeedsMigrationCount)
	}
}

func TestPlanHookMigration_BrokenMarker(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir)
	forceRepoHooksPath(t, tmpDir)

	_, hooksDir, err := resolveGitHooksDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n")

	plan, err := PlanHookMigration(tmpDir)
	if err != nil {
		t.Fatalf("PlanHookMigration returned error: %v", err)
	}

	hook, ok := findHookPlan(plan, "pre-commit")
	if !ok {
		t.Fatalf("pre-commit hook not found in plan")
	}

	if !hook.NeedsMigration {
		t.Fatalf("expected pre-commit to need migration")
	}
	if hook.MarkerState != hookMarkerStateBroken {
		t.Fatalf("expected marker state broken, got %q", hook.MarkerState)
	}
	if plan.BrokenMarkerCount != 1 {
		t.Fatalf("expected BrokenMarkerCount=1, got %d", plan.BrokenMarkerCount)
	}
}

func TestPlanHookMigration_MissingHookWithBackupSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	setupGitRepoInDir(t, tmpDir)
	forceRepoHooksPath(t, tmpDir)

	_, hooksDir, err := resolveGitHooksDir(tmpDir)
	if err != nil {
		t.Fatalf("resolveGitHooksDir failed: %v", err)
	}

	writeHookFile(t, filepath.Join(hooksDir, "pre-commit.backup"), "#!/bin/sh\necho backup\n")

	plan, err := PlanHookMigration(tmpDir)
	if err != nil {
		t.Fatalf("PlanHookMigration returned error: %v", err)
	}

	hook, ok := findHookPlan(plan, "pre-commit")
	if !ok {
		t.Fatalf("pre-commit hook not found in plan")
	}

	if !hook.NeedsMigration {
		t.Fatalf("expected pre-commit to need migration")
	}
	if hook.State != "missing_with_backup_sidecar" {
		t.Fatalf("expected state missing_with_backup_sidecar, got %q", hook.State)
	}
}

// TestPlanHookMigration_NonShell pins the non-shell half of hook-migration
// classification (GH#6348): bd's sh section cannot run in these hooks, so
// migration repairs one an older bd version left behind — and never
// injects, retires sidecars, or rewrites ambiguous/binary content.
func TestPlanHookMigration_NonShell(t *testing.T) {
	pyBody := "#!/usr/bin/env python3\nimport sys\n\nif __name__ == \"__main__\":\n    sys.exit(0)\n"

	t.Run("paired section needs repair even with current-version markers", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepoInDir(t, tmpDir)
		forceRepoHooksPath(t, tmpDir)

		_, hooksDir, err := resolveGitHooksDir(tmpDir)
		if err != nil {
			t.Fatalf("resolveGitHooksDir failed: %v", err)
		}

		corrupted := pyBody + "\n# --- BEGIN BEADS INTEGRATION v9.9.9 ---\nbd hooks run pre-commit \"$@\"\n# --- END BEADS INTEGRATION v9.9.9 ---\n"
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), corrupted)
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit.old"), "#!/usr/bin/env sh\necho old\n")
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit.backup"), "#!/usr/bin/env sh\necho backup\n")

		plan, err := PlanHookMigration(tmpDir)
		if err != nil {
			t.Fatalf("PlanHookMigration returned error: %v", err)
		}

		hook, ok := findHookPlan(plan, "pre-commit")
		if !ok {
			t.Fatal("pre-commit hook not found in plan")
		}
		if !hook.NeedsMigration {
			t.Fatal("a non-shell hook carrying a bd section must be reported for repair")
		}
		if hook.State != "marker_managed_non_shell" {
			t.Fatalf("state = %q, want marker_managed_non_shell", hook.State)
		}
		if hook.HookLanguage != "python3" {
			t.Fatalf("hook_language = %q, want python3", hook.HookLanguage)
		}
		if plan.NeedsMigrationCount != 1 {
			t.Fatalf("expected 1 migration, got %d", plan.NeedsMigrationCount)
		}
	})

	t.Run("ambiguous markers plan manual inspection, not repair", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepoInDir(t, tmpDir)
		forceRepoHooksPath(t, tmpDir)

		_, hooksDir, err := resolveGitHooksDir(tmpDir)
		if err != nil {
			t.Fatalf("resolveGitHooksDir failed: %v", err)
		}

		ambiguous := "#!/usr/bin/env python3\nimport sys\ns = \"# --- BEGIN BEADS INTEGRATION v1.0.0 ---\"\n"
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), ambiguous)

		plan, err := PlanHookMigration(tmpDir)
		if err != nil {
			t.Fatalf("PlanHookMigration returned error: %v", err)
		}

		hook, ok := findHookPlan(plan, "pre-commit")
		if !ok {
			t.Fatal("pre-commit hook not found in plan")
		}
		if hook.NeedsMigration {
			t.Fatal("ambiguous markers must not be reported as repairable")
		}
		if hook.State != "non_shell_manual" {
			t.Fatalf("state = %q, want non_shell_manual", hook.State)
		}
		if !strings.Contains(hook.SuggestedAction, "manually") {
			t.Fatalf("suggested action must explain manual inspection, got %q", hook.SuggestedAction)
		}
		if plan.NeedsMigrationCount != 0 {
			t.Fatalf("expected no migrations, got %d", plan.NeedsMigrationCount)
		}
	})

	t.Run("clean non-shell hook with sidecars needs no migration", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepoInDir(t, tmpDir)
		forceRepoHooksPath(t, tmpDir)

		_, hooksDir, err := resolveGitHooksDir(tmpDir)
		if err != nil {
			t.Fatalf("resolveGitHooksDir failed: %v", err)
		}

		writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), pyBody)
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit.old"), "#!/usr/bin/env sh\necho old\n")
		writeHookFile(t, filepath.Join(hooksDir, "pre-commit.backup"), "#!/usr/bin/env sh\necho backup\n")

		plan, err := PlanHookMigration(tmpDir)
		if err != nil {
			t.Fatalf("PlanHookMigration returned error: %v", err)
		}

		hook, ok := findHookPlan(plan, "pre-commit")
		if !ok {
			t.Fatal("pre-commit hook not found in plan")
		}
		if hook.NeedsMigration {
			t.Fatalf("clean non-shell hook must need no migration, state %q", hook.State)
		}
		if hook.State != "unmanaged_custom" {
			t.Fatalf("state = %q, want unmanaged_custom", hook.State)
		}
		if plan.NeedsMigrationCount != 0 {
			t.Fatalf("expected no migrations, got %d", plan.NeedsMigrationCount)
		}
	})

	t.Run("binary content is never migrated", func(t *testing.T) {
		tmpDir := t.TempDir()
		setupGitRepoInDir(t, tmpDir)
		forceRepoHooksPath(t, tmpDir)

		_, hooksDir, err := resolveGitHooksDir(tmpDir)
		if err != nil {
			t.Fatalf("resolveGitHooksDir failed: %v", err)
		}

		writeHookFile(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\n\x00binary\x00\n")

		plan, err := PlanHookMigration(tmpDir)
		if err != nil {
			t.Fatalf("PlanHookMigration returned error: %v", err)
		}

		hook, ok := findHookPlan(plan, "pre-commit")
		if !ok {
			t.Fatal("pre-commit hook not found in plan")
		}
		if hook.NeedsMigration {
			t.Fatalf("binary content must not be reported as repairable, state %q", hook.State)
		}
		if hook.State != "non_shell_manual" {
			t.Fatalf("state = %q, want non_shell_manual", hook.State)
		}
	})
}

func TestDetectHookMarkerState_DuplicateBeginTags(t *testing.T) {
	content := "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n# --- END BEADS INTEGRATION v0.57.0 ---\n"
	got := detectHookMarkerState(content)
	if got != hookMarkerStateBroken {
		t.Fatalf("expected broken for duplicate BEGIN tags, got %q", got)
	}
}

func TestDetectHookMarkerState_DuplicateMarkerBlocks(t *testing.T) {
	content := "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n# --- END BEADS INTEGRATION v0.57.0 ---\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-push \"$@\"\n# --- END BEADS INTEGRATION v0.57.0 ---\n"
	got := detectHookMarkerState(content)
	if got != hookMarkerStateBroken {
		t.Fatalf("expected broken for duplicate marker blocks, got %q", got)
	}
}

func TestDetectHookMarkerState_ReversedOrder(t *testing.T) {
	content := "#!/bin/sh\n# --- END BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\n"
	got := detectHookMarkerState(content)
	if got != hookMarkerStateBroken {
		t.Fatalf("expected broken for reversed marker order, got %q", got)
	}
}

func TestDetectHookMarkerState_ValidSingle(t *testing.T) {
	content := "#!/bin/sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hook pre-commit \"$@\"\n# --- END BEADS INTEGRATION v0.57.0 ---\n"
	got := detectHookMarkerState(content)
	if got != hookMarkerStateValid {
		t.Fatalf("expected valid for correct single marker block, got %q", got)
	}
}

func TestDetectHookMarkerState_None(t *testing.T) {
	content := "#!/bin/sh\necho hello\n"
	got := detectHookMarkerState(content)
	if got != hookMarkerStateNone {
		t.Fatalf("expected none for no markers, got %q", got)
	}
}

func writeHookFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("failed to write hook file: %v", err)
	}
}

func findHookPlan(plan HookMigrationPlan, name string) (HookMigrationHookPlan, bool) {
	for _, hook := range plan.Hooks {
		if hook.Name == name {
			return hook, true
		}
	}
	return HookMigrationHookPlan{}, false
}

func forceRepoHooksPath(t *testing.T, repoPath string) {
	t.Helper()
	cmd := gitCommand("config", "core.hooksPath", ".git/hooks")
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set core.hooksPath for test repo: %v", err)
	}
}
