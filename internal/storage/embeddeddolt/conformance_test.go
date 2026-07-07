//go:build cgo

package embeddeddolt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/conformance"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestConformance runs the backend-agnostic storage conformance suite
// (internal/storage/conformance) against the embedded Dolt backend, so the
// storage contract is enforced against a real implementation rather than only
// asserted. The factory returns a fresh, empty in-process store per sub-test.
func TestConformance(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
	conformance.RunAll(t, func(t *testing.T) storage.DoltStorage {
		ctx := t.Context()
		beadsDir := filepath.Join(t.TempDir(), ".beads")
		store, err := embeddeddolt.Open(ctx, beadsDir, "test", "main")
		if err != nil {
			t.Fatalf("Open embedded store: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })

		// A fresh store is uninitialized; the conformance factory contract
		// requires an init'd store (issue_prefix set), as `bd init` leaves it.
		if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			t.Fatalf("SetConfig(issue_prefix): %v", err)
		}
		if err := store.Commit(ctx, "bd init"); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		return store
	})
}
