package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/cmd/bd/doctor"
)

func TestFixPendingMigrations_AppliesHookMigration(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	writeHookMigrationFile(t, preCommitPath, "#!/usr/bin/env sh\n# bd-shim v2\n# bd-hooks-version: 0.56.1\nexec bd hooks run pre-commit \"$@\"\n")
	writeHookMigrationFile(t, preCommitPath+".old", "#!/usr/bin/env sh\necho old-custom\n")
	if err := os.MkdirAll(filepath.Join(repoDir, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	pending := doctor.DetectPendingMigrations(repoDir)
	if len(pending) == 0 {
		t.Fatal("expected pending hook migration before fix")
	}

	if err := fixPendingMigrations(repoDir); err != nil {
		t.Fatalf("fixPendingMigrations failed: %v", err)
	}

	rendered := mustReadHookMigrationFile(t, preCommitPath)
	if !strings.Contains(rendered, hookSectionBeginPrefix) || !strings.Contains(rendered, hookSectionEndPrefix) {
		t.Fatalf("expected migrated hook to contain marker section, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "echo old-custom") {
		t.Fatalf("expected migrated hook to preserve sidecar content, got:\n%s", rendered)
	}

	assertMissingHookMigrationFile(t, preCommitPath+".old")
	assertExistsHookMigrationFile(t, preCommitPath+".old.migrated")
}

func TestFixPendingMigrations_BrokenMarkerBlocks(t *testing.T) {
	repoDir, hooksDir := setupHookMigrationRepo(t)
	preCommitPath := filepath.Join(hooksDir, "pre-commit")

	brokenContent := "#!/usr/bin/env sh\n# --- BEGIN BEADS INTEGRATION v0.57.0 ---\nbd hooks run pre-commit \"$@\"\n"
	writeHookMigrationFile(t, preCommitPath, brokenContent)
	if err := os.MkdirAll(filepath.Join(repoDir, ".beads"), 0o755); err != nil {
		t.Fatalf("failed to create .beads: %v", err)
	}

	err := fixPendingMigrations(repoDir)
	if err == nil {
		t.Fatal("expected fixPendingMigrations to fail for broken marker state")
	}
	if !strings.Contains(err.Error(), "hook migration is blocked") {
		t.Fatalf("expected blocked migration error, got: %v", err)
	}

	rendered := mustReadHookMigrationFile(t, preCommitPath)
	if rendered != brokenContent {
		t.Fatalf("expected broken hook content to remain unchanged, got:\n%s", rendered)
	}
}
