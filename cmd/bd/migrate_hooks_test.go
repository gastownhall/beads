package main

import (
	"strings"
	"testing"

	"github.com/gastownhall/beads/cmd/bd/doctor"
)

func TestBuildHookMigrationJSON(t *testing.T) {
	plan := doctor.HookMigrationPlan{
		Path:                "/tmp/repo",
		IsGitRepo:           true,
		NeedsMigrationCount: 2,
		TotalHooks:          5,
	}
	mode := hookMigrationMode{
		RequestedDryRun: true,
	}
	execPlan := hookMigrationExecutionPlan{
		WriteOps: []hookMigrationWriteOp{
			{
				HookName: "pre-commit",
				HookPath: "/tmp/repo/.git/hooks/pre-commit",
				State:    "legacy_only",
			},
		},
	}

	out := buildHookMigrationJSON(plan, mode, execPlan, nil)

	if status, ok := out["status"].(string); !ok || status != "preview" {
		t.Fatalf("expected status preview, got %#v", out["status"])
	}
	if dryRun, ok := out["dry_run"].(bool); !ok || !dryRun {
		t.Fatalf("expected dry_run=true, got %#v", out["dry_run"])
	}
	if opCount, ok := out["operation_count"].(int); !ok || opCount != 1 {
		t.Fatalf("expected operation_count=1, got %#v", out["operation_count"])
	}
}

func TestValidateHookMigrationMode(t *testing.T) {
	mode, err := validateHookMigrationMode(true, false, false)
	if err != nil {
		t.Fatalf("expected no error for --dry-run mode, got %v", err)
	}
	if !mode.RequestedDryRun || mode.RequestedApply {
		t.Fatalf("unexpected mode for dry-run: %#v", mode)
	}

	mode, err = validateHookMigrationMode(false, true, true)
	if err != nil {
		t.Fatalf("expected no error for --apply --yes mode, got %v", err)
	}
	if !mode.RequestedApply || !mode.RequestedYes || mode.RequestedDryRun {
		t.Fatalf("unexpected mode for apply: %#v", mode)
	}

	_, err = validateHookMigrationMode(true, true, false)
	if err == nil || !strings.Contains(err.Error(), "cannot use --dry-run and --apply together") {
		t.Fatalf("expected mutual-exclusion error, got: %v", err)
	}

	_, err = validateHookMigrationMode(false, false, false)
	if err == nil || !strings.Contains(err.Error(), "must specify exactly one mode") {
		t.Fatalf("expected mode-required error, got: %v", err)
	}

	_, err = validateHookMigrationMode(false, false, true)
	if err == nil || !strings.Contains(err.Error(), "--yes requires --apply") {
		t.Fatalf("expected yes-without-apply error, got: %v", err)
	}
}

func TestValidateHookMigrationApplyConsent(t *testing.T) {
	if err := validateHookMigrationApplyConsent(true, false, false); err != nil {
		t.Fatalf("expected --yes to bypass prompt checks, got: %v", err)
	}

	if err := validateHookMigrationApplyConsent(false, true, false); err != nil {
		t.Fatalf("expected interactive mode to allow prompt, got: %v", err)
	}

	err := validateHookMigrationApplyConsent(false, false, true)
	if err == nil || !strings.Contains(err.Error(), "--json with --apply requires --yes") {
		t.Fatalf("expected json+apply consent error, got: %v", err)
	}

	err = validateHookMigrationApplyConsent(false, false, false)
	if err == nil || !strings.Contains(err.Error(), "requires confirmation") {
		t.Fatalf("expected non-interactive consent error, got: %v", err)
	}
}

func TestFormatHookMigrationPlan_NotGitRepo(t *testing.T) {
	lines := formatHookMigrationPlan(doctor.HookMigrationPlan{
		Path:      "/tmp/no-git",
		IsGitRepo: false,
	}, hookMigrationMode{RequestedDryRun: true})

	rendered := strings.Join(lines, "\n")
	if !strings.Contains(rendered, "not a git repository") {
		t.Fatalf("expected non-git message, got: %s", rendered)
	}
}

func TestFormatHookMigrationPlan_WithMigrations(t *testing.T) {
	plan := doctor.HookMigrationPlan{
		Path:                "/tmp/repo",
		RepoRoot:            "/tmp/repo",
		HooksDir:            "/tmp/repo/.git/hooks",
		IsGitRepo:           true,
		TotalHooks:          5,
		NeedsMigrationCount: 1,
		Hooks: []doctor.HookMigrationHookPlan{
			{
				Name:           "pre-commit",
				State:          "legacy_with_old_sidecar",
				NeedsMigration: true,
			},
		},
	}

	lines := formatHookMigrationPlan(plan, hookMigrationMode{RequestedDryRun: true})
	rendered := strings.Join(lines, "\n")

	if !strings.Contains(rendered, "Needs migration: 1/5") {
		t.Fatalf("expected migration summary, got: %s", rendered)
	}
	if !strings.Contains(rendered, "- pre-commit: legacy_with_old_sidecar [migrate]") {
		t.Fatalf("expected hook entry, got: %s", rendered)
	}
	if !strings.Contains(rendered, "Next: run 'bd migrate hooks --apply'") {
		t.Fatalf("expected next-step hint, got: %s", rendered)
	}
}
