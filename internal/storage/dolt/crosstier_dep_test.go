package dolt

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// These tests pin the two-session transaction seam: DoltStore.RunInTransaction
// runs versioned tables on one SQL session (regularTx) and dolt-ignored wisp
// tables on another (ignoredTx). A dependency whose write table lives on one
// session and whose target issue lives on the other must still resolve targets
// created earlier in the same logical transaction — the shape `bd create
// --deps blocks:<other-tier-id>` produces since create+deps became one
// transaction. Regression coverage for the red-team finding where such creates
// hard-failed with "issue not found" on the Dolt server backend.

func crossTierRegularIssue(id, title string) *types.Issue {
	return &types.Issue{
		ID:        id,
		Title:     title,
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}
}

func crossTierWispIssue(id, title string) *types.Issue {
	iss := crossTierRegularIssue(id, title)
	iss.Ephemeral = true
	return iss
}

func assertCrossTierIsBlocked(ctx context.Context, t *testing.T, db *sql.DB, table, id string, want bool) {
	t.Helper()
	var got bool
	if err := db.QueryRowContext(ctx, "SELECT is_blocked FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("query %s.is_blocked for %s: %v", table, id, err)
	}
	if got != want {
		t.Fatalf("%s.is_blocked for %s = %v, want %v", table, id, got, want)
	}
}

func assertDepEdge(ctx context.Context, t *testing.T, store *DoltStore, sourceID, targetID string) {
	t.Helper()
	deps, err := store.GetDependencyRecords(ctx, sourceID)
	if err != nil {
		t.Fatalf("GetDependencyRecords(%s): %v", sourceID, err)
	}
	for _, d := range deps {
		if d.DependsOnID == targetID {
			return
		}
	}
	t.Fatalf("no dependency edge %s -> %s; records: %+v", sourceID, targetID, deps)
}

// New wisp created in the transaction blocks an existing regular issue
// (`bd create "blocker" --ephemeral --deps blocks:<regular-id>`): the edge
// writes to the regular tier while the target wisp is uncommitted on the
// ignored session.
func TestRunInTransactionNewWispBlocksExistingRegular(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	regular := crossTierRegularIssue("test-xtier-blocked-regular", "regular issue blocked by new wisp")
	if err := store.CreateIssue(ctx, regular, "tester"); err != nil {
		t.Fatalf("CreateIssue regular: %v", err)
	}

	wisp := crossTierWispIssue("test-xtier-new-wisp-blocker", "new wisp blocking a regular issue")
	if err := store.RunInTransaction(ctx, "test: create wisp blocking regular", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, wisp, "tester"); err != nil {
			return err
		}
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID:     regular.ID,
			DependsOnID: wisp.ID,
			Type:        types.DepBlocks,
		}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction create wisp + blocks edge: %v", err)
	}

	assertWispCount(ctx, t, store.db, wisp.ID, 1)
	assertDepEdge(ctx, t, store, regular.ID, wisp.ID)
	assertCrossTierIsBlocked(ctx, t, store.db, "issues", regular.ID, true)
}

// New regular issue created in the transaction blocks an existing wisp
// (`bd create "blocker" --deps blocks:<wisp-id>`): the edge writes to the
// ignored tier while the target regular issue is uncommitted on the regular
// session.
func TestRunInTransactionNewRegularBlocksExistingWisp(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	wisp := crossTierWispIssue("test-xtier-blocked-wisp", "wisp blocked by new regular issue")
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}

	regular := crossTierRegularIssue("test-xtier-new-regular-blocker", "new regular issue blocking a wisp")
	if err := store.RunInTransaction(ctx, "test: create regular blocking wisp", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, regular, "tester"); err != nil {
			return err
		}
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID:     wisp.ID,
			DependsOnID: regular.ID,
			Type:        types.DepBlocks,
		}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction create regular + blocks edge: %v", err)
	}

	assertIssueCount(ctx, t, store.db, regular.ID, 1)
	assertDepEdge(ctx, t, store, wisp.ID, regular.ID)
	assertCrossTierIsBlocked(ctx, t, store.db, "wisps", wisp.ID, true)
}

// A closed cross-tier blocker must not mark the source blocked: the openness
// gate has to consult the target's own session, not just existence.
func TestRunInTransactionClosedCrossTierBlockerDoesNotBlock(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	wisp := crossTierWispIssue("test-xtier-closed-blocked-wisp", "wisp with already-closed regular blocker")
	if err := store.CreateIssue(ctx, wisp, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp: %v", err)
	}

	regular := crossTierRegularIssue("test-xtier-closed-regular-blocker", "closed regular issue blocking a wisp")
	regular.Status = types.StatusClosed
	if err := store.RunInTransaction(ctx, "test: create closed regular blocking wisp", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, regular, "tester"); err != nil {
			return err
		}
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID:     wisp.ID,
			DependsOnID: regular.ID,
			Type:        types.DepBlocks,
		}, "tester")
	}); err != nil {
		t.Fatalf("RunInTransaction create closed regular + blocks edge: %v", err)
	}

	assertDepEdge(ctx, t, store, wisp.ID, regular.ID)
	assertCrossTierIsBlocked(ctx, t, store.db, "wisps", wisp.ID, false)
}

// A cross-tier dependency on a target that exists in neither tier must still
// fail with the standard not-found error and roll back the whole transaction.
func TestRunInTransactionCrossTierDepMissingTargetRollsBack(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	wisp := crossTierWispIssue("test-xtier-missing-target-wisp", "wisp whose dep target is missing")
	err := store.RunInTransaction(ctx, "test: wisp dep on missing target", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, wisp, "tester"); err != nil {
			return err
		}
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID:     wisp.ID,
			DependsOnID: "test-xtier-does-not-exist",
			Type:        types.DepBlocks,
		}, "tester")
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("RunInTransaction error = %v, want target not-found", err)
	}
	assertWispCount(ctx, t, store.db, wisp.ID, 0)
}

// A blocking cycle closed across the two tiers inside one transaction must be
// rejected even though each session sees only its own uncommitted edges.
func TestRunInTransactionCrossTierCycleRejected(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	regular := crossTierRegularIssue("test-xtier-cycle-regular", "regular issue in cross-tier cycle")
	if err := store.CreateIssue(ctx, regular, "tester"); err != nil {
		t.Fatalf("CreateIssue regular: %v", err)
	}

	wisp := crossTierWispIssue("test-xtier-cycle-wisp", "wisp in cross-tier cycle")
	err := store.RunInTransaction(ctx, "test: cross-tier blocking cycle", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, wisp, "tester"); err != nil {
			return err
		}
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID:     wisp.ID,
			DependsOnID: regular.ID,
			Type:        types.DepBlocks,
		}, "tester"); err != nil {
			return err
		}
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID:     regular.ID,
			DependsOnID: wisp.ID,
			Type:        types.DepBlocks,
		}, "tester")
	})
	if err == nil || !strings.Contains(err.Error(), "would create a cycle") {
		t.Fatalf("RunInTransaction error = %v, want cycle rejection", err)
	}
	// The cross-tier gate must return the typed sentinel, not a bare string, so
	// consumers see the same taxonomy as the same-tier path (guarded-ops S1).
	if !errors.Is(err, domain.ErrDependencyCycle) {
		t.Fatalf("cross-tier cycle error = %v, want errors.Is(domain.ErrDependencyCycle)", err)
	}
	assertWispCount(ctx, t, store.db, wisp.ID, 0)
	if deps, derr := store.GetDependencyRecords(ctx, regular.ID); derr != nil || len(deps) != 0 {
		t.Fatalf("regular issue dependency records after rollback = %v (err %v), want none", deps, derr)
	}
}

// A scheduling cycle whose CLOSING edge is same-tier must still be rejected when
// an earlier edge in the same logical transaction is cross-tier and pending on
// the other session. This is the create-time batch
// `bd create R --deps blocks:<wisp W>,depends-on:<regular C>` with a committed
// path C -> W: the `blocks:W` edge (W -> R) is uncommitted on the ignored
// session, and the same-tier `depends-on:C` edge (R -> C) closes the cycle
// R -> C -> W -> R. The per-edge cross-tier gate only ran the merged two-session
// cycle check when the closing edge itself crossed tiers, so a same-tier closing
// edge saw only the regular session and missed the pending W -> R edge, letting
// the cycle commit. AddDependencyWithOptions now forces the merged check once
// both dependency tiers are in play.
func TestRunInTransactionMixedTierCycleSameTierClosingEdgeRejected(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// Committed setup: regular C, wisp W, and the committed scheduling path
	// C -> W (C depends on W).
	regularC := crossTierRegularIssue("test-mixed-cycle-regular-c", "committed regular C in mixed-tier cycle")
	if err := store.CreateIssue(ctx, regularC, "tester"); err != nil {
		t.Fatalf("CreateIssue regular C: %v", err)
	}
	wispW := crossTierWispIssue("test-mixed-cycle-wisp-w", "committed wisp W in mixed-tier cycle")
	if err := store.CreateIssue(ctx, wispW, "tester"); err != nil {
		t.Fatalf("CreateIssue wisp W: %v", err)
	}
	if err := store.RunInTransaction(ctx, "seed committed C depends-on W", func(tx storage.Transaction) error {
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID:     regularC.ID,
			DependsOnID: wispW.ID,
			Type:        types.DepBlocks,
		}, "tester")
	}); err != nil {
		t.Fatalf("seed committed C -> W edge: %v", err)
	}

	// Under-test transaction: create regular R, then add the cross-tier
	// `blocks:W` edge (W -> R, pending on the ignored session) before the
	// same-tier `depends-on:C` edge (R -> C) that closes the cycle.
	regularR := crossTierRegularIssue("test-mixed-cycle-regular-r", "new regular R in mixed-tier cycle")
	err := store.RunInTransaction(ctx, "create R with blocks:W then depends-on:C", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx, regularR, "tester"); err != nil {
			return err
		}
		// blocks:W -> W depends-on R: wisp source, regular target (cross-tier,
		// pending on the ignored session).
		if err := tx.AddDependency(ctx, &types.Dependency{
			IssueID:     wispW.ID,
			DependsOnID: regularR.ID,
			Type:        types.DepBlocks,
		}, "tester"); err != nil {
			return err
		}
		// depends-on:C -> R depends-on C: both regular (same-tier closing edge).
		return tx.AddDependency(ctx, &types.Dependency{
			IssueID:     regularR.ID,
			DependsOnID: regularC.ID,
			Type:        types.DepBlocks,
		}, "tester")
	})
	if err == nil || !strings.Contains(err.Error(), "would create a cycle") {
		t.Fatalf("RunInTransaction error = %v, want cycle rejection", err)
	}
	// Same-tier closing edge on a cross-tier cycle must still surface the typed
	// sentinel (guarded-ops S1).
	if !errors.Is(err, domain.ErrDependencyCycle) {
		t.Fatalf("mixed-tier cycle error = %v, want errors.Is(domain.ErrDependencyCycle)", err)
	}

	// The under-test transaction must roll back entirely: R never lands and no
	// edge from it persists.
	assertIssueCount(ctx, t, store.db, regularR.ID, 0)
	if deps, derr := store.GetDependencyRecords(ctx, regularR.ID); derr != nil || len(deps) != 0 {
		t.Fatalf("regular R dependency records after rollback = %v (err %v), want none", deps, derr)
	}
	// The committed C -> W edge is untouched by the rolled-back transaction.
	assertDepEdge(ctx, t, store, regularC.ID, wispW.ID)
}
