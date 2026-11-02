package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/types"
)

// TestIsBlockedBackfillMigrationsPreserveUpdatedAt reproduces
// gastownhall/beads#5981 (bd-chqckf): upgrading a store that predates the
// is_blocked projection (1.0.4 -> 1.2.2) runs 0046 and 0047, whose backfill
// UPDATEs set is_blocked without self-assigning updated_at. issues.updated_at
// carries ON UPDATE CURRENT_TIMESTAMP, so every blocked issue gets its real
// last-modified date overwritten with the migration's wall clock — silently,
// exit 0. 0059 already self-assigns updated_at for the same reason; the
// shipped 0046/0047 bytes are frozen (content-hashed), so the fix is a
// snapshot/restore around those steps in the migration runner itself.
//
// The triage repro executed the migration files directly with ExecContext;
// this version drives them through the real runner (MigrateUpTo after
// rewinding the cursor) because that is where the fix lives — running the
// frozen SQL out-of-band bypasses the runner and cannot see it.
func TestIsBlockedBackfillMigrationsPreserveUpdatedAt(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	const (
		blocker = "gh5981-blocker"
		blocked = "gh5981-blocked"
		free    = "gh5981-free"

		blockerTS = "2026-01-10 09:00:00"
		blockedTS = "2026-01-15 10:00:00"
		freeTS    = "2026-02-01 13:00:00"
	)
	createPerm(t, ctx, store, blocker)
	createPerm(t, ctx, store, blocked)
	createPerm(t, ctx, store, free)
	addDependency(t, ctx, store, blocked, blocker, types.DepBlocks)

	// Pre-upgrade state: months-old last-modified dates and the projection at
	// the column default 0 that 0046's ADD COLUMN gives every existing row.
	for _, seed := range []struct{ id, ts string }{
		{blocker, blockerTS},
		{blocked, blockedTS},
		{free, freeTS},
	} {
		if _, err := store.db.ExecContext(ctx,
			`UPDATE issues SET is_blocked = 0, updated_at = ? WHERE id = ?`, seed.ts, seed.id); err != nil {
			t.Fatalf("seed pre-upgrade row %s: %v", seed.id, err)
		}
	}

	// Rewind the cursor below 0046 so the backfill pair is genuinely pending,
	// then run it exactly the way the first post-upgrade invocation does.
	if _, err := store.db.ExecContext(ctx,
		`DELETE FROM schema_migrations WHERE version > 45`); err != nil {
		t.Fatalf("rewind schema cursor to v45: %v", err)
	}
	if _, err := schema.MigrateUpTo(ctx, store.db, 47); err != nil {
		t.Fatalf("MigrateUpTo(47): %v", err)
	}

	if !getIsBlocked(t, ctx, store, "issues", blocked) {
		t.Fatalf("%s was not backfilled as blocked; the fixture does not exercise the backfill", blocked)
	}
	if getIsBlocked(t, ctx, store, "issues", blocker) || getIsBlocked(t, ctx, store, "issues", free) {
		t.Fatalf("unexpected blocked flags on unblocked fixture issues")
	}
	for _, seed := range []struct{ id, ts string }{
		{blocker, blockerTS},
		{blocked, blockedTS},
		{free, freeTS},
	} {
		var n int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM issues WHERE id = ? AND updated_at = ?`, seed.id, seed.ts).Scan(&n); err != nil {
			t.Fatalf("read updated_at for %s: %v", seed.id, err)
		}
		if n != 1 {
			t.Errorf("%s: updated_at != %s after the is_blocked backfill migrations (migration overwrote the issue's last-modified date)",
				seed.id, seed.ts)
		}
	}
}
