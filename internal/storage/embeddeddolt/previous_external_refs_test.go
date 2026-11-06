//go:build cgo

package embeddeddolt_test

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestPreviousExternalRefsMatchesPerIssueLookup covers AC-5 on the embedded
// backend: over a committed history with relinks, NULL and empty refs, a
// delete and recreate, a late create, a latest-date tie and a side branch
// merged after every cutoff, the batch read returns exactly what the
// per-issue PreviousExternalRef returns, id by id and cutoff by cutoff.
func TestPreviousExternalRefsMatchesPerIssueLookup(t *testing.T) {
	requireEmbeddedDolt(t)
	te := newTestEnv(t, "perh")
	ctx := t.Context()

	// The scenario is committed through its own engine, which must be shut
	// before the store opens the database again: an embedded database admits
	// one engine at a time, and the store's open would wait on this one.
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
	if err != nil {
		t.Fatalf("OpenSQL: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = cleanup()
		t.Fatalf("pin connection: %v", err)
	}
	scenario := testutil.BuildExternalRefHistoryScenario(t, ctx, conn, "perh")
	if err := conn.Close(); err != nil {
		t.Fatalf("close scenario connection: %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("close scenario engine: %v", err)
	}

	testutil.AssertPreviousExternalRefsParity(t, ctx, te.store, scenario, "perh")
	testutil.AssertExternalRefHistoryDiscriminator(t, ctx, te.store, scenario, "perh")
}
