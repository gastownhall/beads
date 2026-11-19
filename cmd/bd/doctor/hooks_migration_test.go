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

// TestPlanHookMigration_NonShell pins the plan states for hooks run by a
// non-shell interpreter (GH#6348).
func TestPlanHookMigration_NonShell(t *testing.T) {
	const python = "#!/usr/bin/env python3\nimport sys\nsys.exit(0)\n"
	// The section bd 0.56.1 wrote.
	section := "# --- BEGIN BEADS INTEGRATION v0.56.1 ---\nif command -v bd >/dev/null 2>&1; then\n  export BD_GIT_HOOK=1\n" +
		"  bd hooks run pre-push \"$@\"\n  _bd_exit=$?; if [ $_bd_exit -ne 0 ]; then exit $_bd_exit; fi\nfi\n# --- END BEADS INTEGRATION v0.56.1 ---\n"
	// The same section with a line of the user's own before its closing fi.
	added := strings.Replace(section, "\nfi\n", "\nprint('mine')\nfi\n", 1)
	const shim = "#!/bin/sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-push \"$@\"\n"
	// The GH#6348 workaround: bd's section ported to Python between its markers.
	ported := "# --- BEGIN BEADS INTEGRATION v0.56.1 ---\nimport subprocess\nsubprocess.call([\"bd\", \"hooks\", \"run\", \"pre-push\"])\n# --- END BEADS INTEGRATION v0.56.1 ---\n"

	cases := []struct {
		name           string
		files          map[string]string
		wantState      string
		wantNeeds      bool
		wantProblem    bool
		wantInterp     string
		wantSourceInt  string
		wantPendingOne bool
		wantAction     string
	}{
		{name: "clean python", files: map[string]string{"pre-push": python}, wantState: HookStateNonShellCustom, wantInterp: "python3"},
		{name: "python with sidecars", files: map[string]string{"pre-push": python, "pre-push.old": "#!/bin/sh\n", "pre-push.backup": "#!/bin/sh\n"}, wantState: HookStateNonShellCustom, wantInterp: "python3"},
		{name: "python with paired section", files: map[string]string{"pre-push": python + "\n" + section}, wantState: HookStateNonShellRepair, wantNeeds: true, wantInterp: "python3", wantPendingOne: true},
		{name: "python with orphan begin", files: map[string]string{"pre-push": python + "# --- BEGIN BEADS INTEGRATION v0.56.1 ---\n"}, wantState: HookStateNonShellManual, wantNeeds: true, wantProblem: true, wantInterp: "python3", wantPendingOne: true},
		{name: "python with ported section", files: map[string]string{"pre-push": python + "\n" + ported}, wantState: HookStateNonShellEdited, wantInterp: "python3", wantAction: "lines 5-8"},
		{name: "python with a line added to the section", files: map[string]string{"pre-push": python + "\n" + added}, wantState: HookStateNonShellEdited, wantInterp: "python3", wantAction: "lines 5-12"},
		{name: "binary with markers", files: map[string]string{"pre-push": "\x7fELF\x00" + section}, wantState: HookStateNonShellManual, wantNeeds: true, wantProblem: true, wantInterp: "binary content", wantPendingOne: true},
		{name: "legacy shim with python old", files: map[string]string{"pre-push": shim, "pre-push.old": python}, wantState: "legacy_with_old_sidecar", wantNeeds: true, wantSourceInt: "python3", wantPendingOne: true},
		{name: "legacy shim with python old holding a ported section", files: map[string]string{"pre-push": shim, "pre-push.old": python + ported}, wantState: "legacy_with_old_sidecar", wantNeeds: true, wantSourceInt: "python3", wantPendingOne: true},
		{name: "missing hook with python backup", files: map[string]string{"pre-push.backup": python}, wantState: "missing_with_backup_sidecar", wantNeeds: true, wantSourceInt: "python3", wantPendingOne: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			setupGitRepoInDir(t, tmpDir)
			forceRepoHooksPath(t, tmpDir)
			if err := os.MkdirAll(filepath.Join(tmpDir, ".beads"), 0o755); err != nil {
				t.Fatal(err)
			}
			_, hooksDir, err := resolveGitHooksDir(tmpDir)
			if err != nil {
				t.Fatalf("resolveGitHooksDir failed: %v", err)
			}
			for name, content := range tc.files {
				writeHookFile(t, filepath.Join(hooksDir, name), content)
			}

			plan, err := PlanHookMigration(tmpDir)
			if err != nil {
				t.Fatalf("PlanHookMigration returned error: %v", err)
			}
			hook, ok := findHookPlan(plan, "pre-push")
			if !ok {
				t.Fatal("pre-push hook not found in plan")
			}
			if hook.State != tc.wantState || hook.NeedsMigration != tc.wantNeeds {
				t.Fatalf("state=%q needs=%v, want %q needs=%v (%+v)", hook.State, hook.NeedsMigration, tc.wantState, tc.wantNeeds, hook)
			}
			if (hook.MarkerProblem != "") != tc.wantProblem {
				t.Fatalf("marker problem = %q, want present=%v", hook.MarkerProblem, tc.wantProblem)
			}
			if hook.Interpreter != tc.wantInterp || hook.SourceInterpreter != tc.wantSourceInt {
				t.Fatalf("interpreter=%q source=%q, want %q/%q", hook.Interpreter, hook.SourceInterpreter, tc.wantInterp, tc.wantSourceInt)
			}
			if tc.wantSourceInt != "" && !strings.Contains(hook.SuggestedAction, tc.wantSourceInt) {
				t.Fatalf("suggested action must name the source interpreter: %q", hook.SuggestedAction)
			}
			if !strings.Contains(hook.SuggestedAction, tc.wantAction) {
				t.Fatalf("suggested action must contain %q: %q", tc.wantAction, hook.SuggestedAction)
			}
			if got := len(DetectPendingMigrations(tmpDir)); (got == 1) != tc.wantPendingOne {
				t.Fatalf("pending migrations = %d, want one=%v", got, tc.wantPendingOne)
			}
		})
	}
}
