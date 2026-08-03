//go:build cgo

package embeddeddolt_test

import (
	"os"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/conformance"
)

func requireEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt tests")
	}
}

// TestConformance runs the backend-agnostic storage conformance suite
// (internal/storage/conformance) against the embedded Dolt backend, so the
// storage contract is enforced against a real implementation rather than only
// asserted. The factory returns a fresh, empty in-process store per sub-test.
func TestConformance(t *testing.T) {
	requireEmbeddedDolt(t)
	conformance.RunAll(t, func(t *testing.T) storage.DoltStorage {
		fixture := newPristineEmbeddedDoltFixture(t, pristineEmbeddedDoltDatabase)
		t.Cleanup(func() { closeEmbeddedDoltStore(t, fixture.store) })
		return fixture.store
	})
}
