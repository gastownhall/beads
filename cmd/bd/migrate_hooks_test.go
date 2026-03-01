package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

func TestBuildHookMigrationJSON(t *testing.T) {
	plan := doctor.HookMigrationPlan{
		Path:                "/tmp/repo",
		IsGitRepo:           true,
		NeedsMigrationCount: 2,
		TotalHooks:          5,
	}

	out := buildHookMigrationJSON(plan, false)

	if status, ok := out["status"].(string); !ok || status != "planning_only" {
		t.Fatalf("expected status planning_only, got %#v", out["status"])
	}
	if planningOnly, ok := out["planning_only"].(bool); !ok || !planningOnly {
		t.Fatalf("expected planning_only=true, got %#v", out["planning_only"])
	}
	if dryRun, ok := out["dry_run"].(bool); !ok || !dryRun {
		t.Fatalf("expected dry_run=true, got %#v", out["dry_run"])
	}
}

func TestValidateHookMigrationDryRunRequested(t *testing.T) {
	if err := validateHookMigrationDryRunRequested(true); err != nil {
		t.Fatalf("expected no error for dry-run mode, got %v", err)
	}

	err := validateHookMigrationDryRunRequested(false)
	if err == nil {
		t.Fatalf("expected error when --dry-run is missing")
	}
	if !strings.Contains(err.Error(), "--dry-run is required") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "#2218") {
		t.Fatalf("expected issue reference #2218, got: %v", err)
	}
	if !strings.Contains(err.Error(), "resolving #2218 is merged") {
		t.Fatalf("expected merge condition note, got: %v", err)
	}
}

func TestFormatHookMigrationPlan_NotGitRepo(t *testing.T) {
	lines := formatHookMigrationPlan(doctor.HookMigrationPlan{
		Path:      "/tmp/no-git",
		IsGitRepo: false,
	})

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

	lines := formatHookMigrationPlan(plan)
	rendered := strings.Join(lines, "\n")

	if !strings.Contains(rendered, "Needs migration: 1/5") {
		t.Fatalf("expected migration summary, got: %s", rendered)
	}
	if !strings.Contains(rendered, "- pre-commit: legacy_with_old_sidecar [migrate]") {
		t.Fatalf("expected hook entry, got: %s", rendered)
	}
	if !strings.Contains(rendered, "Next: run 'bd migrate hooks --dry-run --json'") {
		t.Fatalf("expected next-step hint, got: %s", rendered)
	}
}
