package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/git"
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
		cmd := gitCommand("config", "core.hooksPath", ".git/hooks")
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

const nonShellMigrationPython = "#!/usr/bin/env python3\nimport sys\n\n\nif __name__ == \"__main__\":\n    sys.exit(0)\n"

const nonShellMigrationLegacyShim = "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-push \"$@\"\n"

// nonShellMigrationPort is GH#6348's workaround: bd's section ported to Python
// between its markers (five lines).
const nonShellMigrationPort = "# --- BEGIN BEADS INTEGRATION v1.2.2 ---\n" +
	"# This section is managed by beads. Do not remove these markers.\n" +
	"import subprocess as _sp\n" +
	"_sp.call([\"bd\", \"hooks\", \"run\", \"pre-push\"] + sys.argv[1:])\n" +
	"# --- END BEADS INTEGRATION v1.2.2 ---\n"

func planAndApplyHookMigrationForTest(t *testing.T, repoDir string) (doctor.HookMigrationPlan, hookMigrationExecutionPlan, hookMigrationApplySummary) {
	t.Helper()
	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)
	if len(execPlan.BlockingErrors) > 0 {
		t.Fatalf("unexpected blocking errors: %v", execPlan.BlockingErrors)
	}
	summary, err := applyHookMigrationExecution(execPlan)
	if err != nil {
		t.Fatalf("applyHookMigrationExecution failed: %v", err)
	}
	return plan, execPlan, summary
}

func hookMigrationPlanFor(t *testing.T, plan doctor.HookMigrationPlan, name string) doctor.HookMigrationHookPlan {
	t.Helper()
	for _, hook := range plan.Hooks {
		if hook.Name == name {
			return hook
		}
	}
	t.Fatalf("hook %s missing from plan", name)
	return doctor.HookMigrationHookPlan{}
}

func assertHookMigrationConverged(t *testing.T, repoDir string) {
	t.Helper()
	plan, err := doctor.PlanHookMigration(repoDir)
	if err != nil {
		t.Fatalf("PlanHookMigration failed: %v", err)
	}
	if plan.NeedsMigrationCount != 0 {
		t.Fatalf("expected the migration to converge, still needed: %+v", plan.Hooks)
	}
	execPlan := buildHookMigrationExecutionPlan(plan)
	if execPlan.operationCount() != 0 || len(execPlan.BlockingErrors) != 0 {
		t.Fatalf("expected a no-op rerun, got ops=%+v blocking=%v", execPlan.outputOperations(), execPlan.BlockingErrors)
	}
}

// TestApplyHookMigration_NonShell pins GH#6348 for bd migrate hooks and the
// bd doctor --fix hook migration: they never add bd's shell section to a
// non-shell hook, repair sections an earlier install appended, leave a section
// someone edited alone, restore a non-shell sidecar as-is and retire it, and
// stop on markers they cannot remove safely.
func TestApplyHookMigration_NonShell(t *testing.T) {
	section := generateHookSection("pre-push")
	oldSection := strings.NewReplacer(
		hookSectionBeginLine(), hookSectionBeginPrefix+" v0.56.1 ---",
		hookSectionEndLine(), hookSectionEndPrefix+" v0.56.1 ---",
	).Replace(section)

	t.Run("AC6_marker_repair_keeps_sidecars", func(t *testing.T) {
		for name, injected := range map[string]string{"current_version": section, "old_version": oldSection} {
			t.Run(name, func(t *testing.T) {
				repoDir, hooksDir := setupHookMigrationRepo(t)
				hookPath := filepath.Join(hooksDir, "pre-push")
				writeHookMigrationFile(t, hookPath, injectHookSection(nonShellMigrationPython, injected))
				oldSidecar := "#!/usr/bin/env python3\nprint('old')\n"
				backupSidecar := "#!/usr/bin/env python3\nprint('backup')\n"
				writeHookMigrationFile(t, hookPath+".old", oldSidecar)
				writeHookMigrationFile(t, hookPath+".backup", backupSidecar)

				plan, _, summary := planAndApplyHookMigrationForTest(t, repoDir)
				hook := hookMigrationPlanFor(t, plan, "pre-push")
				if hook.State != "non_shell_marker_repair" || !hook.NeedsMigration {
					t.Fatalf("expected non_shell_marker_repair needing migration, got %+v", hook)
				}
				if got := mustReadHookMigrationFile(t, hookPath); got != nonShellMigrationPython {
					t.Fatalf("expected the original python hook back, got:\n%q", got)
				}
				if mustReadHookMigrationFile(t, hookPath+".old") != oldSidecar || mustReadHookMigrationFile(t, hookPath+".backup") != backupSidecar {
					t.Fatal("sidecars must be kept unchanged")
				}
				assertMissingHookMigrationFile(t, hookPath+".old.migrated")
				assertMissingHookMigrationFile(t, hookPath+".backup.migrated")
				if !strings.Contains(strings.Join(formatHookMigrationApplySummary(summary), "\n"), "bd integration skipped") {
					t.Fatalf("summary must report the skipped integration: %+v", summary)
				}
				assertHookMigrationConverged(t, repoDir)
			})
		}
	})

	t.Run("AC6_clean_non_shell_hook_is_a_noop", func(t *testing.T) {
		repoDir, hooksDir := setupHookMigrationRepo(t)
		hookPath := filepath.Join(hooksDir, "pre-push")
		content := nonShellMigrationPython + "# bd-shim v1\n# bd-hooks-version: 0.40.0\n"
		writeHookMigrationFile(t, hookPath, content)
		writeHookMigrationFile(t, hookPath+".old", "#!/bin/sh\necho old\n")
		writeHookMigrationFile(t, hookPath+".backup", "#!/bin/sh\necho backup\n")

		plan, err := doctor.PlanHookMigration(repoDir)
		if err != nil {
			t.Fatalf("PlanHookMigration failed: %v", err)
		}
		hook := hookMigrationPlanFor(t, plan, "pre-push")
		if hook.State != "non_shell_custom" || hook.NeedsMigration {
			t.Fatalf("expected non_shell_custom with no migration, got %+v", hook)
		}
		assertHookMigrationConverged(t, repoDir)
		if mustReadHookMigrationFile(t, hookPath) != content {
			t.Fatal("hook must be unchanged")
		}
	})

	t.Run("AC6_edited_section_is_a_noop", func(t *testing.T) {
		// GH#6348's workaround: bd's section ported to Python between the
		// markers. A line someone added to bd's own code is theirs too.
		// Migration must neither delete them nor block on them.
		beforeFi := strings.Replace(section, "\nfi\n"+hookSectionEndLine(), "\nprint('mine')\nfi\n"+hookSectionEndLine(), 1)
		if beforeFi == section {
			t.Fatal("the closing fi of the section was not found")
		}
		for name, edited := range map[string]string{"python_port": nonShellMigrationPort, "line_added_before_fi": beforeFi} {
			t.Run(name, func(t *testing.T) {
				repoDir, hooksDir := setupHookMigrationRepo(t)
				hookPath := filepath.Join(hooksDir, "pre-push")
				content := nonShellMigrationPython + "\n" + edited
				writeHookMigrationFile(t, hookPath, content)
				writeHookMigrationFile(t, hookPath+".old", "#!/usr/bin/env python3\nprint('old')\n")

				plan, err := doctor.PlanHookMigration(repoDir)
				if err != nil {
					t.Fatalf("PlanHookMigration failed: %v", err)
				}
				hook := hookMigrationPlanFor(t, plan, "pre-push")
				if hook.State != doctor.HookStateNonShellEdited || hook.NeedsMigration || hook.MarkerProblem != "" {
					t.Fatalf("expected non_shell_marker_edited with no migration, got %+v", hook)
				}
				first := strings.Count(nonShellMigrationPython, "\n") + 2
				lines := fmt.Sprintf("lines %d-%d", first, first+strings.Count(edited, "\n")-1)
				if !strings.Contains(hook.SuggestedAction, lines) {
					t.Fatalf("suggested action must name the edited section (%s): %q", lines, hook.SuggestedAction)
				}
				execPlan := buildHookMigrationExecutionPlan(plan)
				if execPlan.operationCount() != 0 || len(execPlan.BlockingErrors) != 0 {
					t.Fatalf("expected no operations and no blocking errors, got ops=%+v blocking=%v", execPlan.outputOperations(), execPlan.BlockingErrors)
				}
				if _, err := applyHookMigrationExecution(execPlan); err != nil {
					t.Fatalf("applyHookMigrationExecution failed: %v", err)
				}
				if mustReadHookMigrationFile(t, hookPath) != content {
					t.Fatal("the edited hook must be unchanged")
				}
				assertHookMigrationConverged(t, repoDir)
			})
		}
	})

	t.Run("AC7_non_shell_sidecar_restored_without_injection", func(t *testing.T) {
		repaired := injectHookSection(nonShellMigrationPython, section)
		cases := []struct {
			name         string
			active       string // "" = missing hook
			old, backup  string
			wantSource   string
			wantContents string
		}{
			{name: "legacy_shim_with_old", active: nonShellMigrationLegacyShim, old: nonShellMigrationPython, wantSource: ".old", wantContents: nonShellMigrationPython},
			{name: "missing_hook_with_backup", backup: nonShellMigrationPython, wantSource: ".backup", wantContents: nonShellMigrationPython},
			{name: "both_sidecars_old_wins", active: nonShellMigrationLegacyShim, old: nonShellMigrationPython, backup: "#!/usr/bin/env python3\nprint('backup')\n", wantSource: ".old", wantContents: nonShellMigrationPython},
			{name: "sidecar_with_paired_section", active: nonShellMigrationLegacyShim, old: repaired, wantSource: ".old", wantContents: nonShellMigrationPython},
			{name: "sidecar_with_ported_section", active: nonShellMigrationLegacyShim, old: nonShellMigrationPython + nonShellMigrationPort, wantSource: ".old", wantContents: nonShellMigrationPython + nonShellMigrationPort},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				repoDir, hooksDir := setupHookMigrationRepo(t)
				hookPath := filepath.Join(hooksDir, "pre-push")
				if tc.active != "" {
					writeHookMigrationFile(t, hookPath, tc.active)
				}
				if tc.old != "" {
					writeHookMigrationFile(t, hookPath+".old", tc.old)
				}
				if tc.backup != "" {
					writeHookMigrationFile(t, hookPath+".backup", tc.backup)
				}

				plan, err := doctor.PlanHookMigration(repoDir)
				if err != nil {
					t.Fatalf("PlanHookMigration failed: %v", err)
				}
				dryRun := buildHookMigrationExecutionPlan(plan)
				var restore *hookMigrationOutputOperation
				var retired, wantRetired []string
				for _, op := range dryRun.outputOperations() {
					if op.HookName != "pre-push" {
						continue
					}
					if op.Action == "retire_sidecar" {
						retired = append(retired, op.SourcePath)
						continue
					}
					op := op
					restore = &op
				}
				if restore == nil || restore.Action != "restore_hook" || restore.SourcePath != hookPath+tc.wantSource {
					t.Fatalf("expected restore_hook from %s, got %+v", tc.wantSource, restore)
				}
				// The sidecars are retired as on the shell path, so the
				// restored script is not chained again as .old.
				if tc.old != "" {
					wantRetired = append(wantRetired, hookPath+".old")
				}
				if tc.backup != "" {
					wantRetired = append(wantRetired, hookPath+".backup")
				}
				if strings.Join(retired, "\n") != strings.Join(wantRetired, "\n") {
					t.Fatalf("retired sidecars = %v, want %v", retired, wantRetired)
				}
				if !strings.Contains(strings.Join(formatHookMigrationOperations(dryRun), "\n"), "without bd's shell section") {
					t.Fatalf("dry run must describe the restore: %v", formatHookMigrationOperations(dryRun))
				}

				_, _, summary := planAndApplyHookMigrationForTest(t, repoDir)
				if got := mustReadHookMigrationFile(t, hookPath); got != tc.wantContents {
					t.Fatalf("restored hook mismatch, got:\n%q", got)
				}
				for suffix, sidecar := range map[string]string{".old": tc.old, ".backup": tc.backup} {
					if sidecar == "" {
						continue
					}
					assertMissingHookMigrationFile(t, hookPath+suffix)
					if mustReadHookMigrationFile(t, hookPath+suffix+".migrated") != sidecar {
						t.Fatalf("%s must be retired unchanged to %s.migrated", suffix, suffix)
					}
				}
				if !strings.Contains(strings.Join(formatHookMigrationApplySummary(summary), "\n"), "bd integration skipped") {
					t.Fatalf("summary must report the skipped integration: %+v", summary)
				}
				assertHookMigrationConverged(t, repoDir)
			})
		}
	})

	t.Run("AC7_restored_sidecar_is_not_chained_again", func(t *testing.T) {
		// bd hooks run <hook>, which the migration output advises calling
		// from the restored script, chains any non-bd .old. Left in place,
		// the .old would run a second time on every git operation.
		repoDir, hooksDir := setupHookMigrationRepo(t)
		hookPath := filepath.Join(hooksDir, "pre-push")
		ran := filepath.Join(t.TempDir(), "old-hook-ran")
		awkHook := "#!/usr/bin/awk -f\nBEGIN { printf \"ran\\n\" > \"" + ran + "\" }\n"
		writeHookMigrationFile(t, hookPath, nonShellMigrationLegacyShim)
		writeHookMigrationFile(t, hookPath+".old", awkHook)

		_, execPlan, _ := planAndApplyHookMigrationForTest(t, repoDir)
		if len(execPlan.WriteOps) != 1 || execPlan.WriteOps[0].outputAction() != "restore_hook" {
			t.Fatalf("expected one restore_hook, got %+v", execPlan.outputOperations())
		}
		if got := mustReadHookMigrationFile(t, hookPath); got != awkHook {
			t.Fatalf("the awk script must be the active hook, got:\n%q", got)
		}

		var code int
		runInDir(t, repoDir, func() { code = runPrePushHook(nil) })
		if code != 0 {
			t.Fatalf("runPrePushHook() = %d, want 0", code)
		}
		if _, err := os.Stat(ran); !os.IsNotExist(err) {
			t.Fatalf("bd hooks run pre-push ran the restored script again as .old (stat err=%v)", err)
		}
	})

	t.Run("AC8_ambiguous_markers_block", func(t *testing.T) {
		begin := hookSectionBeginLine() + "\n"
		cases := []struct {
			name           string
			active, old    string
			wantInBlocking string
		}{
			{name: "orphan_begin_in_hook", active: nonShellMigrationPython + begin + "print('x')\n", wantInBlocking: "manual inspection"},
			{name: "binary_with_markers", active: "\x7fELF\x00\x01\n" + section, wantInBlocking: "binary"},
			{name: "ambiguous_old_sidecar", active: nonShellMigrationLegacyShim, old: nonShellMigrationPython + begin, wantInBlocking: "manual inspection"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				repoDir, hooksDir := setupHookMigrationRepo(t)
				hookPath := filepath.Join(hooksDir, "pre-push")
				writeHookMigrationFile(t, hookPath, tc.active)
				if tc.old != "" {
					writeHookMigrationFile(t, hookPath+".old", tc.old)
				}
				plan, err := doctor.PlanHookMigration(repoDir)
				if err != nil {
					t.Fatalf("PlanHookMigration failed: %v", err)
				}
				execPlan := buildHookMigrationExecutionPlan(plan)
				blocking := strings.Join(execPlan.BlockingErrors, "\n")
				if !strings.Contains(blocking, "pre-push") || !strings.Contains(blocking, tc.wantInBlocking) {
					t.Fatalf("expected a pre-push blocking error naming %q, got: %v", tc.wantInBlocking, execPlan.BlockingErrors)
				}
				for _, op := range execPlan.WriteOps {
					if op.HookName == "pre-push" {
						t.Fatalf("no write op for a blocked hook: %+v", op)
					}
				}
				if mustReadHookMigrationFile(t, hookPath) != tc.active {
					t.Fatal("hook must be unchanged")
				}
			})
		}

		t.Run("user_modified_legacy_still_blocks", func(t *testing.T) {
			repoDir, hooksDir := setupHookMigrationRepo(t)
			hookPath := filepath.Join(hooksDir, "pre-push")
			writeHookMigrationFile(t, hookPath, nonShellMigrationLegacyShim+"\n./my-linter.sh\n")
			writeHookMigrationFile(t, hookPath+".old", nonShellMigrationPython)
			plan, err := doctor.PlanHookMigration(repoDir)
			if err != nil {
				t.Fatalf("PlanHookMigration failed: %v", err)
			}
			blocking := strings.Join(buildHookMigrationExecutionPlan(plan).BlockingErrors, "\n")
			if !strings.Contains(blocking, "user-modified") {
				t.Fatalf("expected the user-modified legacy block, got: %s", blocking)
			}
		})
	})
}
