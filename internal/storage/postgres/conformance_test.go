package postgres

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/conformance"
	"github.com/steveyegge/beads/internal/storage/pgdialect"
)

// TestConformance runs bd's backend-agnostic storage conformance suite
// (internal/storage/conformance, ~40 encoded behavior tests) against the
// Postgres wedge. This is the real conformance measurement — far broader than
// the smoke. Every failure is a PG gap: either an allowlisted-unsupported method
// (expected — see completeness_test.go) or a latent divergence nothing else
// caught (the interesting ones). Gated on BEADS_PG_TEST_URL.
func TestConformance(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set")
	}
	conformance.RunAll(t, pgConformanceFactory(url))
}

// TestDeferredReads runs the shared deferred non-version-control reads (statistics,
// external-ref, stale) that the wedge now implements through issueops, as a GREEN
// gate. RunAll stays the fail-loud measurement (red on genuinely Dolt-only methods
// like slots), so this focused gate is what conformance.sh runs for the wedge.
// Gated on BEADS_PG_TEST_URL.
func TestDeferredReads(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set")
	}
	conformance.RunDeferredReads(t, pgConformanceFactory(url))
}

// TestPortableMethods runs the full portable-method behavior contract (molecule/
// repo-mtime/streams/counts/comment/rekey/batch). Red until the methods are wired into
// sqlkit; green after. Gated on BEADS_PG_TEST_URL.
func TestPortableMethods(t *testing.T) {
	url := os.Getenv("BEADS_PG_TEST_URL")
	if url == "" {
		t.Skip("BEADS_PG_TEST_URL not set")
	}
	conformance.RunPortableMethods(t, pgConformanceFactory(url))
}

// pgConformanceFactory provisions a fresh, isolated schema per sub-test against the
// Postgres server at url, seeded with issue_prefix as `bd init` leaves it.
func pgConformanceFactory(url string) conformance.Factory {
	base := time.Now().UnixNano()
	var seq int64
	return func(t *testing.T) storage.DoltStorage {
		ctx := context.Background()
		schema := fmt.Sprintf("conf_%d_%d", base, atomic.AddInt64(&seq, 1))

		raw, err := pgdialect.OpenRaw(url, schema)
		if err != nil {
			t.Fatalf("OpenRaw: %v", err)
		}
		if err := InitSchema(ctx, raw, schema); err != nil {
			_ = raw.Close()
			t.Fatalf("InitSchema: %v", err)
		}
		_ = raw.Close()

		st, err := New(ctx, Config{DSN: url, Schema: schema})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		// The conformance factory contract wants an init'd store (issue_prefix
		// set), as `bd init` leaves it. No Commit — PG has no commit graph.
		if err := st.SetConfig(ctx, "issue_prefix", "test"); err != nil {
			t.Fatalf("SetConfig(issue_prefix): %v", err)
		}
		t.Cleanup(func() {
			_, _ = st.DB().ExecContext(context.Background(),
				fmt.Sprintf(`DROP SCHEMA IF EXISTS %q CASCADE`, schema))
			_ = st.Close()
		})
		return st
	}
}
