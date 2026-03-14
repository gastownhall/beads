package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/beads/cmd/bd/doctor"
	"github.com/gastownhall/beads/internal/git"
)

func TestApplyHookMigrationExecution_LegacyWithOldSidecar(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	writeHookMigrationFile(t, preCommitPath, "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookMigrationFile(t, preCommitPath+".old", "#!/usr/bin/env sh\necho old-custom\n")

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)

	summary, err := applyHookMigrationExecution(execPlan)
	if err != nil {
		t.Fatalf("applyHookMigrationExecution failed: %v", err)
	}
	if summary.WrittenHookCount != 1 {
		t.Fatalf("expected 1 written hook, got %d", summary.WrittenHookCount)
	}
	if summary.RetiredCount != 1 {
		t.Fatalf("expected 1 retired artifact, got %d", summary.RetiredCount)
	}

	rendered := mustReadHookMigrationFile(t, preCommitPath)
	if !strings.Contains(rendered, "echo old-custom") {
		t.Fatalf("expected migrated hook to preserve .old body, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, hookSectionBeginPrefix) || !strings.Contains(rendered, hookSectionEndPrefix) {
		t.Fatalf("expected migrated hook to contain marker section, got:\n%s", rendered)
	}

	assertMissingHookMigrationFile(t, preCommitPath+".old")
	assertExistsHookMigrationFile(t, preCommitPath+".old.migrated")
}

func TestApplyHookMigrationExecution_LegacyWithBothSidecarsPrefersOld(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	writeHookMigrationFile(t, preCommitPath, "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookMigrationFile(t, preCommitPath+".old", "#!/usr/bin/env sh\necho from-old\n")
	writeHookMigrationFile(t, preCommitPath+".backup", "#!/usr/bin/env sh\necho from-backup\n")

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)

	if _, err := applyHookMigrationExecution(execPlan); err != nil {
		t.Fatalf("applyHookMigrationExecution failed: %v", err)
	}

	rendered := mustReadHookMigrationFile(t, preCommitPath)
	if !strings.Contains(rendered, "echo from-old") {
		t.Fatalf("expected .old content to be preferred, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "echo from-backup") {
		t.Fatalf("expected .backup content to be ignored, got:\n%s", rendered)
	}

	assertExistsHookMigrationFile(t, preCommitPath+".old.migrated")
	assertExistsHookMigrationFile(t, preCommitPath+".backup.migrated")
	assertMissingHookMigrationFile(t, preCommitPath+".old")
	assertMissingHookMigrationFile(t, preCommitPath+".backup")
}

func TestApplyHookMigrationExecution_LegacyWithBackupSidecar(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	writeHookMigrationFile(t, preCommitPath, "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookMigrationFile(t, preCommitPath+".backup", "#!/usr/bin/env sh\necho backup-custom\n")

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}

	var hook *doctor.HookMigrationHookPlan
	for i := range plan.Hooks {
		if plan.Hooks[i].Name == "pre-commit" {
			hook = &plan.Hooks[i]
			break
		}
	}
	if hook == nil {
		t.Fatal("pre-commit not found in plan")
	}
	if hook.State != "legacy_with_backup_sidecar" {
		t.Fatalf("expected state legacy_with_backup_sidecar, got %q", hook.State)
	}

	execPlan := buildHookMigrationExecutionPlan(plan)
	summary, err := applyHookMigrationExecution(execPlan)
	if err != nil {
		t.Fatalf("applyHookMigrationExecution failed: %v", err)
	}
	if summary.WrittenHookCount != 1 {
		t.Fatalf("expected 1 written hook, got %d", summary.WrittenHookCount)
	}
	if summary.RetiredCount != 1 {
		t.Fatalf("expected 1 retired artifact, got %d", summary.RetiredCount)
	}

	rendered := mustReadHookMigrationFile(t, preCommitPath)
	if !strings.Contains(rendered, "echo backup-custom") {
		t.Fatalf("expected migrated hook to preserve .backup body, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, hookSectionBeginPrefix) || !strings.Contains(rendered, hookSectionEndPrefix) {
		t.Fatalf("expected migrated hook to contain marker section, got:\n%s", rendered)
	}

	assertMissingHookMigrationFile(t, preCommitPath+".backup")
	assertExistsHookMigrationFile(t, preCommitPath+".backup.migrated")
}

func TestApplyHookMigrationExecution_CustomWithSidecarsPreservesHookBody(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	writeHookMigrationFile(t, preCommitPath, "#!/usr/bin/env sh\necho custom-body\n")
	writeHookMigrationFile(t, preCommitPath+".old", "#!/usr/bin/env sh\necho stale-old\n")
	writeHookMigrationFile(t, preCommitPath+".backup", "#!/usr/bin/env sh\necho stale-backup\n")

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)

	if _, err := applyHookMigrationExecution(execPlan); err != nil {
		t.Fatalf("applyHookMigrationExecution failed: %v", err)
	}

	rendered := mustReadHookMigrationFile(t, preCommitPath)
	if !strings.Contains(rendered, "echo custom-body") {
		t.Fatalf("expected migrated hook to preserve custom body, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, hookSectionBeginPrefix) || !strings.Contains(rendered, hookSectionEndPrefix) {
		t.Fatalf("expected migrated hook to contain marker section, got:\n%s", rendered)
	}

	assertExistsHookMigrationFile(t, preCommitPath+".old.migrated")
	assertExistsHookMigrationFile(t, preCommitPath+".backup.migrated")
}

func TestApplyHookMigrationExecution_MarkerBrokenIsRepaired(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	brokenContent := "#!/usr/bin/env sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hooks run pre-commit \"$@\"\n"
	writeHookMigrationFile(t, preCommitPath, brokenContent)

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)
	if len(execPlan.BlockingErrors) > 0 {
		t.Fatalf("broken markers should not be blocking errors, got: %v", execPlan.BlockingErrors)
	}
	if len(execPlan.WriteOps) == 0 {
		t.Fatal("expected a write op to repair the broken marker")
	}

	summary, err := applyHookMigrationExecution(execPlan)
	if err != nil {
		t.Fatalf("expected apply to succeed for broken marker state, got: %v", err)
	}
	if summary.WrittenHookCount == 0 {
		t.Fatal("expected at least one hook to be written during repair")
	}

	rendered := mustReadHookMigrationFile(t, preCommitPath)
	if !strings.Contains(rendered, hookSectionBeginPrefix) || !strings.Contains(rendered, hookSectionEndPrefix) {
		t.Fatalf("expected repaired hook to contain valid marker section, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "bd hooks run pre-commit") {
		t.Fatalf("expected repaired hook to contain hook invocation, got:\n%s", rendered)
	}
}

func TestApplyHookMigrationExecution_Idempotent(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	writeHookMigrationFile(t, preCommitPath, "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookMigrationFile(t, preCommitPath+".old", "#!/usr/bin/env sh\necho old-custom\n")

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)
	if _, err := applyHookMigrationExecution(execPlan); err != nil {
		t.Fatalf("first apply failed: %v", err)
	}

	secondPlan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("second PlanHookMigration failed: %v", err)
	}
	secondExec := buildHookMigrationExecutionPlan(secondPlan)
	if secondExec.operationCount() != 0 {
		t.Fatalf("expected second execution to be no-op, got %d operations", secondExec.operationCount())
	}

	summary, err := applyHookMigrationExecution(secondExec)
	if err != nil {
		t.Fatalf("second apply should be no-op, got error: %v", err)
	}
	if summary.WrittenHookCount != 0 || summary.RetiredCount != 0 {
		t.Fatalf("expected no-op summary on second apply, got %+v", summary)
	}
}

func TestApplyHookMigrationExecution_RetireCollisionFailsBeforeWrites(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	legacyHook := "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n"
	writeHookMigrationFile(t, preCommitPath, legacyHook)
	writeHookMigrationFile(t, preCommitPath+".old", "#!/usr/bin/env sh\necho old-custom\n")
	writeHookMigrationFile(t, preCommitPath+".old.migrated", "#!/usr/bin/env sh\necho conflicting-content\n")

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)

	if _, err := applyHookMigrationExecution(execPlan); err == nil {
		t.Fatal("expected retire collision to fail apply")
	} else if !strings.Contains(err.Error(), "artifact collision") {
		t.Fatalf("expected collision error, got: %v", err)
	}

	rendered := mustReadHookMigrationFile(t, preCommitPath)
	if rendered != legacyHook {
		t.Fatalf("expected hook file to remain unchanged when collision blocks apply")
	}
}

func TestBuildExecutionPlan_ModifiedLegacyHookBlocks(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	// Legacy shim with user-added custom logic
	modifiedLegacy := "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n\n# My custom linting\n./run-my-linter.sh\n"
	writeHookMigrationFile(t, preCommitPath, modifiedLegacy)
	writeHookMigrationFile(t, preCommitPath+".old", "#!/usr/bin/env sh\necho old-custom\n")

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)

	if len(execPlan.BlockingErrors) == 0 {
		t.Fatal("expected blocking error for user-modified legacy hook with sidecar")
	}

	found := false
	for _, msg := range execPlan.BlockingErrors {
		if strings.Contains(msg, "user-modified") && strings.Contains(msg, "pre-commit") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected blocking error mentioning user-modified pre-commit, got: %v", execPlan.BlockingErrors)
	}

	// No write ops should be emitted for the blocked hook
	for _, op := range execPlan.WriteOps {
		if op.HookName == "pre-commit" {
			t.Fatal("expected no write op for blocked pre-commit hook")
		}
	}
}

func TestBuildExecutionPlan_UnmodifiedLegacyHookProceeds(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	// Clean legacy shim — no user modifications
	cleanLegacy := "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n"
	writeHookMigrationFile(t, preCommitPath, cleanLegacy)
	writeHookMigrationFile(t, preCommitPath+".old", "#!/usr/bin/env sh\necho old-custom\n")

	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)

	if len(execPlan.BlockingErrors) > 0 {
		t.Fatalf("expected no blocking errors for unmodified legacy hook, got: %v", execPlan.BlockingErrors)
	}

	foundWrite := false
	for _, op := range execPlan.WriteOps {
		if op.HookName == "pre-commit" {
			foundWrite = true
			if op.SourceKind != hookMigrationWriteFromOld {
				t.Fatalf("expected source kind %q, got %q", hookMigrationWriteFromOld, op.SourceKind)
			}
			break
		}
	}
	if !foundWrite {
		t.Fatal("expected write op for unmodified legacy hook with .old sidecar")
	}
}

func setupHookMigrationRepo(t *testing.T) (repoDir string, hooksDir string) {
	t.Helper()
	repoDir = newGitRepo(t)

	runInDir(t, repoDir, func() {
		cmd := exec.Command("git", "config", "core.hooksPath", ".git/hooks")
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to set core.hooksPath: %v", err)
		}

		var err error
		hooksDir, err = git.GetGitHooksDir()
		if err != nil {
			t.Fatalf("failed to resolve hooks dir: %v", err)
		}

		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			t.Fatalf("failed to create hooks dir: %v", err)
		}
	})

	return repoDir, hooksDir
}

func writeHookMigrationFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("failed to create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

func mustReadHookMigrationFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return string(content)
}

func assertExistsHookMigrationFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertMissingHookMigrationFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, got err=%v", path, err)
	}
}
