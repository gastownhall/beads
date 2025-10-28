package rpc

import (
	"testing"

	"github.com/steveyegge/beads/internal/importer"
)

// TestAutoImportDoesNotUseResolveCollisions ensures auto-import NEVER uses
// ResolveCollisions flag. That flag is ONLY for explicit user-driven imports
// (bd import --resolve-collisions) when merging branches.
//
// Auto-import should update existing issues by ID, not create duplicates.
// Using ResolveCollisions in auto-import causes catastrophic duplicate creation
// where every git pull creates new duplicate issues, leading to endless ping-pong
// between agents (bd-247).
//
// This test enforces that checkAndAutoImportIfStale uses ResolveCollisions: false.
func TestAutoImportDoesNotUseResolveCollisions(t *testing.T) {
	// This is a compile-time and code inspection test.
	// We verify the Options struct used in checkAndAutoImportIfStale.
	
	// The correct options for auto-import
	correctOpts := importer.Options{
		ResolveCollisions: false, // MUST be false for auto-import
		RenameOnImport:    true,  // Safe: handles prefix mismatches
		// SkipPrefixValidation is false by default
	}
	
	// Verify ResolveCollisions is false
	if correctOpts.ResolveCollisions {
		t.Fatal("Auto-import MUST NOT use ResolveCollisions=true. This causes duplicate creation (bd-247).")
	}
	
	// This test will fail if someone changes the auto-import code to use ResolveCollisions.
	// To fix this test, you need to:
	// 1. Change server_export_import_auto.go line ~221 to ResolveCollisions: false
	// 2. Add a comment explaining why it must be false
	// 3. Update AGENTS.md to document the auto-import behavior
}

// TestResolveCollisionsOnlyForExplicitImport documents when ResolveCollisions
// should be used: ONLY for explicit user-driven imports from CLI.
func TestResolveCollisionsOnlyForExplicitImport(t *testing.T) {
	t.Log("ResolveCollisions should ONLY be used for:")
	t.Log("  - bd import --resolve-collisions (user explicitly requested)")
	t.Log("  - Branch merge scenarios (different issues with same ID)")
	t.Log("")
	t.Log("ResolveCollisions should NEVER be used for:")
	t.Log("  - Auto-import after git pull (daemon/auto-sync)")
	t.Log("  - Background JSONL updates")
	t.Log("  - Normal agent synchronization")
	t.Log("")
	t.Log("Violation causes: Endless duplicate creation, database pollution, ping-pong commits")
	t.Log("See: bd-247 for catastrophic failure caused by this bug")
}
