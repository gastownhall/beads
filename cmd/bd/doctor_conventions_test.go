package main

import (
	"testing"
)

func TestConventionsLint_NoStore(t *testing.T) {
	// Save and restore global store
	origStore := store
	store = nil
	defer func() { store = origStore }()

	checks := runConventionsLint()
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].Status != statusWarning {
		t.Errorf("expected warning status, got %s", checks[0].Status)
	}
	if checks[0].Name != "conventions.lint" {
		t.Errorf("expected name conventions.lint, got %s", checks[0].Name)
	}
}

func TestConventionsStale_NoStore(t *testing.T) {
	origStore := store
	store = nil
	defer func() { store = origStore }()

	checks := runConventionsStale()
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	if checks[0].Status != statusWarning {
		t.Errorf("expected warning status, got %s", checks[0].Status)
	}
	if checks[0].Name != "conventions.stale" {
		t.Errorf("expected name conventions.stale, got %s", checks[0].Name)
	}
}

func TestConventionsOrphans_NoGit(t *testing.T) {
	// In a temp dir with no git history, orphan check should succeed gracefully
	checks := runConventionsOrphans(t.TempDir())
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	// Should be OK (skipped) since there's no git repo
	if checks[0].Name != "conventions.orphans" {
		t.Errorf("expected name conventions.orphans, got %s", checks[0].Name)
	}
}
