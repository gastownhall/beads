package flatfile

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/conformance"
)

// TestConformance runs bd's backend-agnostic storage conformance suite against the
// flat-file backend. Flat-file is embedded (pure Go, one JSON file per issue), so it
// always runs — no env gate. Every failure is a flat-file gap: an
// allowlisted-unsupported method or a latent divergence.
func TestConformance(t *testing.T) {
	conformance.RunAll(t, flatfileConformanceFactory())
}

// flatfileConformanceFactory returns a fresh, directory-isolated store per sub-test,
// seeded with issue_prefix as `bd init` leaves it.
func flatfileConformanceFactory() conformance.Factory {
	return func(t *testing.T) storage.DoltStorage {
		ctx := context.Background()
		st, err := NewFlatFileStore(filepath.Join(t.TempDir(), ".beads"))
		if err != nil {
			t.Fatalf("NewFlatFileStore: %v", err)
		}
		if err := st.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			t.Fatalf("SetConfig(issue_prefix): %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
}
