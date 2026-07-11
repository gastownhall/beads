package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/conformance"
)

// TestConformance runs bd's backend-agnostic storage conformance suite against the
// SQLite backend. SQLite is embedded (pure-Go modernc), so it always runs — no env
// gate. Every failure is a SQLite gap: an allowlisted-unsupported method or a latent
// divergence.
func TestConformance(t *testing.T) {
	conformance.RunAll(t, sqliteConformanceFactory())
}

// TestDeferredReads runs the shared deferred non-version-control reads (statistics,
// external-ref, stale) that the backend now implements through issueops, as a GREEN
// gate. RunAll stays the fail-loud measurement (red on genuinely Dolt-only methods
// like slots), so this focused gate is what conformance.sh runs.
func TestDeferredReads(t *testing.T) {
	conformance.RunDeferredReads(t, sqliteConformanceFactory())
}

// TestPortableMethods runs the full portable-method behavior contract. Red until the
// methods are wired into sqlkit; green after.
func TestPortableMethods(t *testing.T) {
	conformance.RunPortableMethods(t, sqliteConformanceFactory())
}

// sqliteConformanceFactory returns a fresh, file-isolated store per sub-test, seeded
// with issue_prefix as `bd init` leaves it.
func sqliteConformanceFactory() conformance.Factory {
	return func(t *testing.T) storage.DoltStorage {
		ctx := context.Background()
		st, err := Provision(ctx, filepath.Join(t.TempDir(), "conf.db"))
		if err != nil {
			t.Fatalf("Provision: %v", err)
		}
		if err := st.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			t.Fatalf("SetConfig(issue_prefix): %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st
	}
}
