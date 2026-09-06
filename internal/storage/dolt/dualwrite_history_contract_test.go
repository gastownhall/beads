package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// TestDualWriteContract runs the dual-write history contract with
// storage.VersionedHistoryConfigurer ON. Like TestJournalContract, this is an
// engine check rather than an independent per-leg vote: all three legs share
// RecordVersionInTx, and the dolt leg is where a write that escaped its
// transaction, or a read that raced it, would have somewhere to go wrong.
func TestDualWriteContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltDualWriteFixture(t, "dwc", true)
	defer cleanup()

	t.Run("MintsOneVersionRowPerAcceptedMutation", func(t *testing.T) {
		conformance.RunDualWriteMintsOneVersionRowPerAcceptedMutation(t, ctx, fixture)
	})
	t.Run("NoOpMutationMintsNoRow", func(t *testing.T) {
		conformance.RunDualWriteNoOpMutationMintsNoRow(t, ctx, fixture)
	})
	t.Run("AttributionIsRecordedWithTheMutation", func(t *testing.T) {
		conformance.RunDualWriteAttributionIsRecordedWithTheMutation(t, ctx, fixture)
	})
	t.Run("CurrentRevisionMatchesTheNewVersionRow", func(t *testing.T) {
		conformance.RunDualWriteCurrentRevisionMatchesTheNewVersionRow(t, ctx, fixture)
	})
	t.Run("NoOpMutationLeavesThePriorVersionRowUnperturbed", func(t *testing.T) {
		conformance.RunDualWriteNoOpMutationLeavesThePriorVersionRowUnperturbed(t, ctx, fixture)
	})
}

// TestDualWriteContractFlagOff runs FR-7 against a fixture constructed with
// the flag OFF from the start. It is a separate top-level test, not a subtest
// of TestDualWriteContract, because sharing one store between a flag-on and a
// flag-off case would make "flag off" mean "flag not yet turned on this
// session" rather than the thing FR-7 actually promises.
func TestDualWriteContractFlagOff(t *testing.T) {
	fixture, ctx, cleanup := newDoltDualWriteFixture(t, "dwcoff", false)
	defer cleanup()

	t.Run("FlagOffProducesNoVersionRows", func(t *testing.T) {
		conformance.RunDualWriteFlagOffProducesNoVersionRows(t, ctx, fixture)
	})
}

// TestDualWriteFixtureKitIsWired is the explicit per-leg guardrail design
// §8.5 calls for in place of AST auto-discovery: DualWriteFixture's type name
// ends in "Fixture" like every other role fixture, so
// TestEveryLegWiresEveryRoleContract's scan of backend/conformance DOES
// enumerate its six RunDualWriteXxx functions as entrypoints — but that scan
// is satisfied the moment this leg's own test files reference all six by
// name, which TestDualWriteContract and TestDualWriteContractFlagOff already
// do between them. What that satisfied scan cannot catch is a fixture built
// with a nil closure that happens to never run in this leg's own case list —
// silent by construction, since a nil func field only panics the one time
// something calls it. This test exists to make that failure loud instead:
// it fails the moment any of the five closures is nil, independent of
// whether any case above happens to exercise it.
func TestDualWriteFixtureKitIsWired(t *testing.T) {
	fixture, _, cleanup := newDoltDualWriteFixture(t, "dwk", true)
	defer cleanup()

	if fixture.Mutate == nil {
		t.Error("DualWriteFixture.Mutate is nil")
	}
	if fixture.MutateAsNoOp == nil {
		t.Error("DualWriteFixture.MutateAsNoOp is nil")
	}
	if fixture.CurrentRevision == nil {
		t.Error("DualWriteFixture.CurrentRevision is nil")
	}
	if fixture.VersionRowCount == nil {
		t.Error("DualWriteFixture.VersionRowCount is nil")
	}
	if fixture.LatestVersionAttribution == nil {
		t.Error("DualWriteFixture.LatestVersionAttribution is nil")
	}
}

// TestDualWriteStampsTheCurrentStoreEpochOnEachVersionRow pins FR-6, the one
// dual-write requirement DualWriteFixture cannot reach: every issue_versions
// row this phase inserts carries the CURRENT store_epoch.epoch value (design
// §16's "stamped"), and inserting that row must never itself change
// store_epoch.epoch (design's "never bumped" — that bump belongs to a later,
// unbuilt phase, not to RecordVersionInTx). This lives on the dolt leg only,
// not because the property is dolt-specific, but because none of
// DualWriteFixture's five closures expose either epoch column — see
// dualwrite_history_contract.go's "WHAT THIS CONTRACT DELIBERATELY DOES NOT
// PIN" — and the dolt leg is the one with a plain *sql.DB to read them from
// directly.
func TestDualWriteStampsTheCurrentStoreEpochOnEachVersionRow(t *testing.T) {
	store, storeCleanup := setupTestStore(t)
	defer storeCleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	configurer, ok := any(store).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", store)
	}
	configurer.SetVersionedHistoryEnabled(true)
	defer configurer.SetVersionedHistoryEnabled(false)

	create := func(id string) error {
		return store.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "actor")
	}
	storeEpoch := func() (int, error) {
		var epoch int
		err := store.db.QueryRowContext(ctx, `SELECT epoch FROM store_epoch WHERE id = 1`).Scan(&epoch)
		return epoch, err
	}
	versionEpoch := func(id string) (int, error) {
		var epoch int
		err := store.db.QueryRowContext(ctx,
			`SELECT epoch FROM issue_versions WHERE issue_id = ? ORDER BY revision DESC LIMIT 1`, id).
			Scan(&epoch)
		return epoch, err
	}

	const first, second = "dwe-stamp-first", "dwe-stamp-second"
	if err := create(first); err != nil {
		t.Fatalf("creating %s: %v", first, err)
	}
	epochAfterFirst, err := storeEpoch()
	if err != nil {
		t.Fatalf("reading store_epoch after the first mutation: %v", err)
	}
	firstVersionEpoch, err := versionEpoch(first)
	if err != nil {
		t.Fatalf("reading issue_versions.epoch for %s: %v", first, err)
	}
	if firstVersionEpoch != epochAfterFirst {
		t.Errorf("issue_versions.epoch for %s = %d, want the current store_epoch.epoch %d: FR-6 requires "+
			"every version row to be stamped with the store's current epoch at the moment it is minted",
			first, firstVersionEpoch, epochAfterFirst)
	}

	if err := create(second); err != nil {
		t.Fatalf("creating %s: %v", second, err)
	}
	epochAfterSecond, err := storeEpoch()
	if err != nil {
		t.Fatalf("reading store_epoch after the second mutation: %v", err)
	}
	if epochAfterSecond != epochAfterFirst {
		t.Errorf("store_epoch.epoch went from %d to %d across an ordinary accepted mutation, want "+
			"unchanged: FR-6 requires RecordVersionInTx to stamp the epoch onto the version row, never "+
			"to bump store_epoch.epoch itself — that bump belongs to a later, unbuilt phase",
			epochAfterFirst, epochAfterSecond)
	}
	secondVersionEpoch, err := versionEpoch(second)
	if err != nil {
		t.Fatalf("reading issue_versions.epoch for %s: %v", second, err)
	}
	if secondVersionEpoch != epochAfterSecond {
		t.Errorf("issue_versions.epoch for %s = %d, want the current store_epoch.epoch %d",
			second, secondVersionEpoch, epochAfterSecond)
	}
}

func newDoltDualWriteFixture(t *testing.T, prefix string, enabled bool) (conformance.DualWriteFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	// Through the type assertion `bd serve` makes, never the concrete method
	// set: dual-write history is not on storage.DoltStorage, so publishing it
	// IS implementing this interface, matching newDoltJournalFixture's own
	// discipline for storage.EventsJournalCursor above.
	configurer, ok := any(store).(storage.VersionedHistoryConfigurer)
	if !ok {
		cancel()
		storeCleanup()
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", store)
	}
	configurer.SetVersionedHistoryEnabled(enabled)
	fixture := conformance.DualWriteFixture{
		IssuePrefix: prefix,
		Mutate: func(ctx context.Context, id string) error {
			return store.CreateIssue(ctx, &types.Issue{
				ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
			}, "actor")
		},
		MutateAsNoOp: func(ctx context.Context, id string) error {
			// Re-sets title to the exact value Mutate already gave it, so
			// issueops.DiscardNoopIssueUpdates discards it before it ever
			// reaches RecordVersionInTx.
			return store.UpdateIssue(ctx, id, map[string]any{"title": "t-" + id}, "actor")
		},
		CurrentRevision: func(ctx context.Context, id string) (int64, error) {
			var revision int64
			err := store.db.QueryRowContext(ctx, `SELECT current_revision FROM issues WHERE id = ?`, id).
				Scan(&revision)
			return revision, err
		},
		VersionRowCount: func(ctx context.Context, id string) (int, error) {
			var count int
			err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?`, id).
				Scan(&count)
			return count, err
		},
		LatestVersionAttribution: func(ctx context.Context, id string) (actor, agent, message string, err error) {
			err = store.db.QueryRowContext(ctx, `
				SELECT COALESCE(change_actor, ''), COALESCE(change_agent, ''), COALESCE(change_message, '')
				FROM issue_versions
				WHERE issue_id = ?
				ORDER BY revision DESC
				LIMIT 1`, id).Scan(&actor, &agent, &message)
			return actor, agent, message, err
		},
	}
	return fixture, ctx, func() {
		// Dual-write is instance-scoped, and this store outlives the fixture
		// in the shared-database harness. Leaving it on would version every
		// mutation a later test in this package makes.
		configurer.SetVersionedHistoryEnabled(false)
		cancel()
		storeCleanup()
	}
}
