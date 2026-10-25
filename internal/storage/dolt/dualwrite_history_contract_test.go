package dolt

import (
	"context"
	"database/sql"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
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

// TestDualWriteVersionRowsRideTheMutationsDoltCommit is the store-half
// companion to the DualWriteFixture cases above, and it is deliberately shaped
// around the two blind spots those cases have.
//
// First, it mutates through RunInTransaction — the doltTransaction surface fed
// by runDoltTransaction — not through store.CreateIssue/UpdateIssue, which run
// on the already-scoped withWriteTx arm. Activation is bound per transaction,
// so a seam that mints fine on one arm can mint nothing at all on the other,
// and every case above happened to exercise only the arm that worked.
//
// Second, it asserts the version rows are IN the operation's Dolt commit, by
// requiring dolt_status to be clean for issue_versions afterwards. Counting
// rows in the working set cannot tell a durable history from one that never
// got staged: unstaged rows read back perfectly from SQL while being absent
// from the commit that describes them, unreplicated, and liable to be swept
// into whatever unrelated commit stages next. issue_versions replicates (see
// issueops.VersionedHistoryStagedTables), so "present" and "committed" are
// different claims and only the second one is the durability promise.
func TestDualWriteVersionRowsRideTheMutationsDoltCommit(t *testing.T) {
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

	const id = "dwc-committed"
	if err := store.RunInTransaction(ctx, "bd: create "+id, func(tx storage.Transaction) error {
		return tx.CreateIssue(ctx, &types.Issue{
			ID: id, Title: "t-" + id, IssueType: types.TypeTask, Status: types.StatusOpen,
		}, "actor")
	}); err != nil {
		t.Fatalf("RunInTransaction create: %v", err)
	}

	var versions int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?`, id).Scan(&versions); err != nil {
		t.Fatalf("count issue_versions: %v", err)
	}
	if versions != 1 {
		t.Fatalf("issue_versions rows for %s = %d, want 1 — a mutation routed through runDoltTransaction minted no version, so versioned history is not scoped on that transaction", id, versions)
	}

	var currentRevision int64
	if err := store.db.QueryRowContext(ctx,
		`SELECT current_revision FROM issues WHERE id = ?`, id).Scan(&currentRevision); err != nil {
		t.Fatalf("read current_revision: %v", err)
	}
	if currentRevision != 1 {
		t.Fatalf("issues.current_revision for %s = %d, want 1", id, currentRevision)
	}

	// All three tables issueops.VersionedHistoryStagedTables names, not just the
	// two the version rows land in. store_epoch is the one whose loss this PR
	// calls unrecoverable, and the first mint seeds its singleton row
	// (version_history.go), so in this scenario it is guaranteed dirty-then-staged:
	// a regression that dropped it from the staged set would otherwise pass here.
	for _, table := range []string{"issue_versions", "store_epoch", "issues"} {
		var dirty int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM dolt_status WHERE table_name = ?`, table).Scan(&dirty); err != nil {
			t.Fatalf("read dolt_status for %s: %v", table, err)
		}
		if dirty != 0 {
			t.Errorf("dolt_status still reports %s dirty after the mutation committed — the rows this operation wrote are outside its own Dolt commit: unreplicated, and waiting to be swept into whatever unrelated commit stages next", table)
		}
	}
}

// TestDualWriteDeleteNeighborRewriteRidesTheDeletesDoltCommit covers the
// staging plane neither DualWriteFixture nor the RunInTransaction case above can
// reach: the server-backed delete role.
//
// A delete is a version-completeness EXEMPTION for the row it removes — there is
// no surviving row to version — which is why issueops' own exemption table lists
// DeleteInTx. It is not an exemption for the TRANSACTION. DeleteInTx rewrites
// every surviving neighbor that cites a deleted id through UpdateIssueInTx, the
// minting entry point, so a delete with a neighbor really does mint; and until
// the fix this test arrived with, deleter.Delete staged its hand-listed
// sweptTables and nothing else, leaving those version rows in the working set
// while issuing its own DOLT_COMMIT.
//
// History is turned on BEFORE the fixture rows exist, so the neighbor is created
// as a participating record. A neighbor created with history off would be a
// legacy record (participation_generation NULL), and design §16.2b's write fence
// skips every update-shaped mint on a legacy record: the rewrite would mint
// nothing, and there would be no version row for this test to find. The same
// fence means a delete can no longer take a store's first mint — the only record
// its rewrite can version is one whose own create already seeded store_epoch —
// so store_epoch in the dolt_status loop below guards the delete plane's staged
// table list rather than a row this transaction writes.
func TestDualWriteDeleteNeighborRewriteRidesTheDeletesDoltCommit(t *testing.T) {
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

	const target, neighbor = "dwdel-target", "dwdel-neighbor"
	fixtures := []*types.Issue{
		{ID: target, Title: "doomed", IssueType: types.TypeTask, Status: types.StatusOpen},
		// The citation is what makes this a MINTING delete: the rewrite only
		// touches a neighbor whose text names a deleted id.
		{ID: neighbor, Title: "survivor", Description: "blocked by " + target,
			IssueType: types.TypeTask, Status: types.StatusOpen},
	}
	for _, issue := range fixtures {
		if err := store.CreateIssue(ctx, issue, "creator"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	// An INBOUND edge, so the neighborhood read finds the survivor at all;
	// Force is then what lets the delete orphan it instead of refusing.
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: neighbor, DependsOnID: target, Type: types.DepBlocks,
	}, "linker"); err != nil {
		t.Fatalf("add dep: %v", err)
	}

	neighborVersions := func() int {
		t.Helper()
		var versions int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?`, neighbor).Scan(&versions); err != nil {
			t.Fatalf("count issue_versions for %s: %v", neighbor, err)
		}
		return versions
	}
	// The neighbor's create and its dependency add have already minted rows of
	// their own; only what the delete adds on top of them is this test's concern.
	versionsBefore := neighborVersions()

	deleter, err := store.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}
	result, err := deleter.Delete(ctx, publicops.DeleteRequest{
		IDs: []string{target}, Force: true, Actor: "deleter",
	})
	if err != nil {
		t.Fatalf("delete %s: %v", target, err)
	}
	if result.ReferencesUpdated != 1 {
		t.Fatalf("ReferencesUpdated = %d, want 1 — the citation rewrite did not run, so this case is not exercising the minting delete path it exists for", result.ReferencesUpdated)
	}

	if minted := neighborVersions() - versionsBefore; minted != 1 {
		t.Fatalf("the delete minted %d issue_versions rows for the rewritten neighbor %s, want 1 — the delete's own transaction is not scoped for minting", minted, neighbor)
	}
	var latestActor string
	if err := store.db.QueryRowContext(ctx,
		`SELECT COALESCE(change_actor, '') FROM issue_versions WHERE issue_id = ? ORDER BY revision DESC LIMIT 1`,
		neighbor).Scan(&latestActor); err != nil {
		t.Fatalf("read the newest issue_versions row for %s: %v", neighbor, err)
	}
	if latestActor != "deleter" {
		t.Fatalf("the newest issue_versions row for %s is attributed to %q, want %q — it is not the delete's citation rewrite", neighbor, latestActor, "deleter")
	}

	for _, table := range []string{"issue_versions", "store_epoch", "issues"} {
		var dirty int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM dolt_status WHERE table_name = ?`, table).Scan(&dirty); err != nil {
			t.Fatalf("read dolt_status for %s: %v", table, err)
		}
		if dirty != 0 {
			t.Errorf("dolt_status still reports %s dirty after the delete committed — the neighbor's version rows are outside the delete's own Dolt commit: unreplicated, and waiting to be swept into whatever unrelated commit stages next", table)
		}
	}
}

// TestDualWriteDeleteSkipsALegacyNeighborsRewrite is the other half of the case
// above: a neighbor created while history was OFF is a legacy record
// (participation_generation NULL), so design §16.2b's write fence skips the
// delete's citation rewrite for it. The rewrite still runs and the delete still
// commits cleanly; it just mints nothing. Without this, a change that let the
// delete mint for a legacy neighbor would go unnoticed now that the test above
// creates its neighbor with history on.
func TestDualWriteDeleteSkipsALegacyNeighborsRewrite(t *testing.T) {
	store, storeCleanup := setupTestStore(t)
	defer storeCleanup()
	ctx, cancel := testContext(t)
	defer cancel()
	configurer, ok := any(store).(storage.VersionedHistoryConfigurer)
	if !ok {
		t.Fatalf("%T does not implement storage.VersionedHistoryConfigurer", store)
	}
	defer configurer.SetVersionedHistoryEnabled(false)

	const target, neighbor = "dwdel-legacy-target", "dwdel-legacy-neighbor"
	for _, issue := range []*types.Issue{
		{ID: target, Title: "doomed", IssueType: types.TypeTask, Status: types.StatusOpen},
		{ID: neighbor, Title: "survivor", Description: "blocked by " + target,
			IssueType: types.TypeTask, Status: types.StatusOpen},
	} {
		if err := store.CreateIssue(ctx, issue, "creator"); err != nil {
			t.Fatalf("create %s: %v", issue.ID, err)
		}
	}
	if err := store.AddDependency(ctx, &types.Dependency{
		IssueID: neighbor, DependsOnID: target, Type: types.DepBlocks,
	}, "linker"); err != nil {
		t.Fatalf("add dep: %v", err)
	}
	// Only now, after the neighbor already exists as a legacy record.
	configurer.SetVersionedHistoryEnabled(true)

	deleter, err := store.Deleter()
	if err != nil {
		t.Fatalf("Deleter(): %v", err)
	}
	result, err := deleter.Delete(ctx, publicops.DeleteRequest{
		IDs: []string{target}, Force: true, Actor: "deleter",
	})
	if err != nil {
		t.Fatalf("delete %s: %v", target, err)
	}
	if result.ReferencesUpdated != 1 {
		t.Fatalf("ReferencesUpdated = %d, want 1 — the citation rewrite did not run, so this case is not exercising the path it exists for", result.ReferencesUpdated)
	}

	var versions int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM issue_versions WHERE issue_id = ?`, neighbor).Scan(&versions); err != nil {
		t.Fatalf("count issue_versions for %s: %v", neighbor, err)
	}
	if versions != 0 {
		t.Errorf("the delete minted %d issue_versions rows for the legacy neighbor %s, want 0: the rewrite is an update-shaped write to a record that has not declared participation (design §16.2b)", versions, neighbor)
	}
	var generation sql.NullInt64
	if err := store.db.QueryRowContext(ctx,
		`SELECT participation_generation FROM issues WHERE id = ?`, neighbor).Scan(&generation); err != nil {
		t.Fatalf("read participation_generation for %s: %v", neighbor, err)
	}
	if generation.Valid {
		t.Errorf("participation_generation for the legacy neighbor %s = %d after the delete, want NULL: an ordinary write must not promote a legacy record (R2.3)", neighbor, generation.Int64)
	}
	for _, table := range []string{"issue_versions", "store_epoch", "issues"} {
		var dirty int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM dolt_status WHERE table_name = ?`, table).Scan(&dirty); err != nil {
			t.Fatalf("read dolt_status for %s: %v", table, err)
		}
		if dirty != 0 {
			t.Errorf("dolt_status still reports %s dirty after the delete committed", table)
		}
	}
}
