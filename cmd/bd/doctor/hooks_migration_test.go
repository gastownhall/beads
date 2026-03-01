package doctor

import (
	"os"
	"os/exec"
	"path/filepath"
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
	cmd := exec.Command("git", "config", "core.hooksPath", ".git/hooks")
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to set core.hooksPath for test repo: %v", err)
	}
}
