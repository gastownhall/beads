package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGuardHookWritePathRefusesSymlinkedParentDir pins the parent-directory
// half of the shared write guard (carried from #7889 by #8070): a hook path
// whose immediate parent is a symlink must be refused before the
// nonexistent-leaf early return, so a write cannot create the hook in the
// link's target.
func TestGuardHookWritePathRefusesSymlinkedParentDir(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside-hooks")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "linked-hooks")
	if err := os.Symlink(outside, linkedDir); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(linkedDir, "pre-commit") // leaf does not exist

	err := guardHookWritePathWithProbe(hookPath, false, gitTrackedFileContext)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected guard to refuse hook write through symlinked parent directory, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "pre-commit")); statErr == nil {
		t.Errorf("hook created in the symlink target despite the refusal")
	}
}

// TestApplyHookMigrationRefusesSymlinkedHooksDir covers the same refusal
// through the guard's other caller, the bd migrate hooks --apply writer: a
// prepared write whose destination directory is a symlink is refused before
// os.WriteFile follows the link.
func TestApplyHookMigrationRefusesSymlinkedHooksDir(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside-hooks")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "linked-hooks")
	if err := os.Symlink(outside, linkedDir); err != nil {
		t.Fatal(err)
	}

	execPlan := hookMigrationExecutionPlan{
		WriteOps: []hookMigrationWriteOp{
			{
				HookName:   "pre-commit",
				HookPath:   filepath.Join(linkedDir, "pre-commit"),
				State:      "write",
				SourceKind: hookMigrationWriteFromTemplate,
			},
		},
	}

	_, err := applyHookMigrationExecution(execPlan)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected hook migration to refuse writing through a symlinked hooks directory, got %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "pre-commit")); statErr == nil {
		t.Errorf("migrated hook written into the symlink target despite the refusal")
	}
}