package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/conformance"
)

// TestConformance runs the backend-agnostic storage conformance suite
// (internal/storage/conformance) against the server-mode Dolt backend, so the
// storage contract is enforced against a real implementation rather than only
// asserted.
//
// Like every other test in this package it requires the shared Dolt test
// server started by TestMain; it self-skips via setupTestStore -> skipIfNoDolt
// when no server is available (e.g. under -short or BEADS_TEST_SKIP=dolt).
// setupTestStore gives each conformance sub-test its own COW branch for
// isolation and registers cleanup.
func TestConformance(t *testing.T) {
	conformance.RunAll(t, func(t *testing.T) storage.DoltStorage {
		store, cleanup := setupTestStore(t)
		t.Cleanup(cleanup)
		return store
	})
}
