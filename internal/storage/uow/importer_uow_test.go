package uow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
	publicops "github.com/steveyegge/beads/issueops"
)

// TestImporterUOW pins the Importer role's contract on the unit-of-work
// backend: one ImportBatch call is one transaction and one history entry —
// rows with their aux data, memories, and the issue_prefix reconciliation
// together — and the in-transaction stale guard reports what it kept.
//
// ONE PROVIDER FOR THE WHOLE SUITE (it boots a real Dolt sql-server) and NO
// t.Parallel: dolt_log is database-global here, so a parallel subtest would
// move another subtest's history delta.
func TestImporterUOW(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "imp")
	kit := newUOWRoleFixtureKit(provider, "imp")

	source, ok := provider.(ImporterSource)
	if !ok {
		t.Fatalf("provider %T does not offer the Importer accessor", provider)
	}
	imp, err := source.Importer()
	if err != nil {
		t.Fatalf("Importer(): %v", err)
	}

	countHistory := func(t *testing.T) int {
		t.Helper()
		n, err := kit.CountHistory(ctx)
		if err != nil {
			t.Fatalf("CountHistory: %v", err)
		}
		return n
	}
	countHistoryMatching := func(t *testing.T, pattern string) int {
		t.Helper()
		n, err := kit.CountHistoryMatching(ctx, pattern)
		if err != nil {
			t.Fatalf("CountHistoryMatching %q: %v", pattern, err)
		}
		return n
	}
	queryInt := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := kit.QueryScalar(ctx, query, args, &n); err != nil {
			t.Fatalf("QueryScalar %q: %v", query, err)
		}
		return n
	}
	queryString := func(t *testing.T, query string, args ...any) string {
		t.Helper()
		var s string
		if err := kit.QueryScalar(ctx, query, args, &s); err != nil {
			t.Fatalf("QueryScalar %q: %v", query, err)
		}
		return s
	}

	t.Run("OneBatchIsOneHistoryEntryWithAuxDataAndMemories", func(t *testing.T) {
		before := countHistory(t)
		when := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test",
			Issues: []*types.Issue{
				{
					ID: "imp-one", Title: "Imported one", Status: types.StatusOpen,
					IssueType: types.TypeTask, Priority: 2,
					Labels:    []string{"lane:test", "imported"},
					Comments:  []*types.Comment{{ID: "imp-one-c1", Author: "importer-test", Text: "carried comment", CreatedAt: when}},
					CreatedAt: when, UpdatedAt: when,
				},
				{
					ID: "imp-two", Title: "Imported two", Status: types.StatusOpen,
					IssueType: types.TypeBug, Priority: 1,
					Dependencies: []*types.Dependency{{IssueID: "imp-two", DependsOnID: "imp-one", Type: types.DepBlocks}},
					CreatedAt:    when, UpdatedAt: when,
				},
			},
			Memories: []publicops.ImportMemory{{Key: "kv.memory.importer-probe", Value: "remembered"}},
			Source:   "importer_uow_test.jsonl",
		})
		if err != nil {
			t.Fatalf("ImportBatch: %v", err)
		}
		if result.Created != 2 {
			t.Errorf("Created = %d, want 2", result.Created)
		}
		if result.MemoriesImported != 1 {
			t.Errorf("MemoriesImported = %d, want 1", result.MemoriesImported)
		}
		if len(result.StaleRejectedIDs) != 0 {
			t.Errorf("StaleRejectedIDs = %v, want none", result.StaleRejectedIDs)
		}

		if after := countHistory(t); after != before+1 {
			t.Errorf("history entries = %d, want %d (ONE commit for the whole batch)", after, before+1)
		}
		// Direct content assertions, not just row counts: the aux data must
		// actually be present on the imported rows.
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE id IN ('imp-one','imp-two')"); got != 2 {
			t.Errorf("issue rows = %d, want 2", got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM labels WHERE issue_id = 'imp-one'"); got != 2 {
			t.Errorf("imp-one labels = %d, want 2", got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM comments WHERE issue_id = 'imp-one'"); got != 1 {
			t.Errorf("imp-one comments = %d, want 1", got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM dependencies WHERE issue_id = 'imp-two' AND depends_on_issue_id = 'imp-one' AND type = 'blocks'"); got != 1 {
			t.Errorf("imp-two blocks edge = %d, want 1", got)
		}
		if got := queryString(t, "SELECT value FROM config WHERE `key` = 'kv.memory.importer-probe'"); got != "remembered" {
			t.Errorf("memory value = %q, want %q", got, "remembered")
		}
		if msg := queryString(t, "SELECT message FROM dolt_log ORDER BY date DESC, commit_hash LIMIT 1"); !strings.Contains(msg, "bd import: 2 issues, 1 memories from importer_uow_test.jsonl") {
			t.Errorf("history message = %q, want the bd import message", msg)
		}
	})

	t.Run("CrossPlaneInBatchEdgesAreWiredInTheOneCommit", func(t *testing.T) {
		// wy-a648lq: a regular<->wisp edge whose BOTH ends are rows of this
		// batch used to be skip-reported by the engine's per-batch plane filter,
		// and since a re-import upserts the rows unchanged it could never be
		// backfilled. Both directions are asserted because they land in
		// different tables and columns.
		before := countHistory(t)
		when := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test",
			Issues: []*types.Issue{
				{
					ID: "imp-plane-d", Title: "Durable end", Status: types.StatusOpen,
					IssueType: types.TypeTask, Priority: 2,
					CreatedAt: when, UpdatedAt: when,
				},
				{
					ID: "imp-plane-w", Title: "Ephemeral end", Status: types.StatusOpen,
					IssueType: types.TypeTask, Priority: 2, Ephemeral: true,
					Dependencies: []*types.Dependency{{IssueID: "imp-plane-w", DependsOnID: "imp-plane-d", Type: types.DepBlocks}},
					CreatedAt:    when, UpdatedAt: when,
				},
				{
					ID: "imp-plane-d2", Title: "Durable row blocked by the wisp", Status: types.StatusOpen,
					IssueType: types.TypeTask, Priority: 2,
					Dependencies: []*types.Dependency{{IssueID: "imp-plane-d2", DependsOnID: "imp-plane-w", Type: types.DepBlocks}},
					CreatedAt:    when, UpdatedAt: when,
				},
			},
			Source: "planes.jsonl",
		})
		if err != nil {
			t.Fatalf("ImportBatch: %v", err)
		}
		if result.Created != 3 {
			t.Errorf("Created = %d, want 3", result.Created)
		}
		if len(result.SkippedDependencies) != 0 {
			t.Errorf("SkippedDependencies = %+v, want none: both ends of each edge are rows of this batch", result.SkippedDependencies)
		}
		if after := countHistory(t); after != before+1 {
			t.Errorf("history entries = %d, want %d (the edges ride the batch's ONE commit)", after, before+1)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM wisp_dependencies WHERE issue_id = 'imp-plane-w' AND depends_on_issue_id = 'imp-plane-d' AND type = 'blocks'"); got != 1 {
			t.Errorf("wisp -> durable blocks edge = %d, want 1", got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM dependencies WHERE issue_id = 'imp-plane-d2' AND depends_on_wisp_id = 'imp-plane-w' AND type = 'blocks'"); got != 1 {
			t.Errorf("durable -> wisp blocks edge = %d, want 1", got)
		}
	})

	t.Run("ReimportConvergesWithoutDuplicatingAuxData", func(t *testing.T) {
		when := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		row := func() *types.Issue {
			return &types.Issue{
				ID: "imp-one", Title: "Imported one", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2,
				Labels:    []string{"lane:test", "imported"},
				Comments:  []*types.Comment{{ID: "imp-one-c1", Author: "importer-test", Text: "carried comment", CreatedAt: when}},
				CreatedAt: when, UpdatedAt: when,
			}
		}
		if _, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test", Issues: []*types.Issue{row()}, Source: "again.jsonl",
		}); err != nil {
			t.Fatalf("re-import: %v", err)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM labels WHERE issue_id = 'imp-one'"); got != 2 {
			t.Errorf("labels after re-import = %d, want 2 (idempotent merge)", got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM comments WHERE issue_id = 'imp-one'"); got != 1 {
			t.Errorf("comments after re-import = %d, want 1 (idempotent merge)", got)
		}
	})

	t.Run("StaleGuardRejectsInsideTheTransactionUnlessAllowed", func(t *testing.T) {
		old := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
		staleRow := func() *types.Issue {
			return &types.Issue{
				ID: "imp-one", Title: "Stale snapshot title", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 3,
				CreatedAt: old, UpdatedAt: old,
			}
		}
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test", Issues: []*types.Issue{staleRow()}, Source: "stale.jsonl",
		})
		if err != nil {
			t.Fatalf("stale import: %v", err)
		}
		if len(result.StaleRejectedIDs) != 1 || result.StaleRejectedIDs[0] != "imp-one" {
			t.Fatalf("StaleRejectedIDs = %v, want [imp-one]", result.StaleRejectedIDs)
		}
		if result.Created != 0 {
			t.Errorf("Created = %d, want 0 (the only row was rejected)", result.Created)
		}
		if got := queryString(t, "SELECT title FROM issues WHERE id = 'imp-one'"); got != "Imported one" {
			t.Errorf("title after stale import = %q, want local row kept", got)
		}

		if _, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test", Issues: []*types.Issue{staleRow()}, AllowStale: true, Source: "stale.jsonl",
		}); err != nil {
			t.Fatalf("allow-stale import: %v", err)
		}
		if got := queryString(t, "SELECT title FROM issues WHERE id = 'imp-one'"); got != "Stale snapshot title" {
			t.Errorf("title after --allow-stale = %q, want the older snapshot restored", got)
		}
	})

	t.Run("EmptyBatchCommitsNothing", func(t *testing.T) {
		before := countHistory(t)
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{Actor: "importer-test", Source: "empty.jsonl"})
		if err != nil {
			t.Fatalf("empty ImportBatch: %v", err)
		}
		if result.Created != 0 || result.MemoriesImported != 0 || result.PrefixSynced {
			t.Errorf("empty batch result = %+v, want zero outcome", result)
		}
		if after := countHistory(t); after != before {
			t.Errorf("history entries moved %d -> %d on an empty batch", before, after)
		}
	})

	t.Run("PrefixSyncAloneCommitsUnderTheSyncMessage", func(t *testing.T) {
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test", SyncIssuePrefix: "impx", Source: "prefix.jsonl",
		})
		if err != nil {
			t.Fatalf("prefix-sync ImportBatch: %v", err)
		}
		if !result.PrefixSynced {
			t.Fatalf("PrefixSynced = false, want true")
		}
		if got := queryString(t, "SELECT value FROM config WHERE `key` = 'issue_prefix'"); got != "impx" {
			t.Errorf("issue_prefix = %q, want %q", got, "impx")
		}
		// Restore for any later subtest.
		if err := kit.SetConfig(ctx, "issue_prefix", "imp"); err != nil {
			t.Fatalf("restore issue_prefix: %v", err)
		}
	})

	t.Run("SeedsPrefixBeforeCreateNeedsIt", func(t *testing.T) {
		// Simulate an externally-provisioned database: config.yaml (modeled
		// here by SyncIssuePrefix, which is what the CLI populates it from)
		// carries a prefix, but the database's own config table does not —
		// exactly what NewBatchContext's ReadConfigPrefix treats as "missing"
		// (sql.ErrNoRows OR an empty value), the state a provisioner that
		// creates the Dolt database directly (bypassing `bd init`, which is
		// what normally writes this row) leaves it in.
		if err := kit.SetConfig(ctx, "issue_prefix", ""); err != nil {
			t.Fatalf("clear issue_prefix: %v", err)
		}

		when := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test",
			Issues: []*types.Issue{{
				ID: "imp-seed", Title: "Seeded via prefix precondition", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2, CreatedAt: when, UpdatedAt: when,
			}},
			SkipPrefixValidation: true,
			SyncIssuePrefix:      "imp",
			Source:               "seed.jsonl",
		})
		if err != nil {
			t.Fatalf("ImportBatch against a database with no config-table prefix: %v", err)
		}
		if result.Created != 1 {
			t.Errorf("Created = %d, want 1", result.Created)
		}
		if got := queryString(t, "SELECT value FROM config WHERE `key` = 'issue_prefix'"); got != "imp" {
			t.Errorf("issue_prefix after seed = %q, want %q", got, "imp")
		}
		// Restore for any later subtest, as the neighbours do. The seed under
		// test happens to leave "imp" behind, but a fixture restored by the
		// code under test would silently change what a subtest inserted after
		// this one runs against.
		if err := kit.SetConfig(ctx, "issue_prefix", "imp"); err != nil {
			t.Fatalf("restore issue_prefix: %v", err)
		}
	})

	t.Run("SeedsPrefixEvenWhenEveryRowIsRejected", func(t *testing.T) {
		// The seed must report itself as the prefix write, or a batch that
		// lands no rows commits NOTHING and discards it:
		// importBatchCommitMessage returns "" when Created == 0,
		// MemoriesImported == 0 and !PrefixSynced, RunTxResultWithin skips
		// uw.Commit on an empty message (tx.go), and the deferred closeAttempt
		// ROLLS BACK the seeded row. Reachable against an
		// externally-provisioned database whenever every input row is dropped
		// — here by the in-transaction stale guard, in the CLI also by the
		// title dedup. SeedsPrefixBeforeCreateNeedsIt cannot catch this
		// (Created == 1 carries the commit), and
		// PrefixSyncAloneCommitsUnderTheSyncMessage runs with a NON-empty
		// stored prefix, so the sync writes there and the seed never fires.
		landed := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
		if _, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test",
			Issues: []*types.Issue{{
				ID: "imp-rejected", Title: "Row the stale guard will keep", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2, CreatedAt: landed, UpdatedAt: landed,
			}},
			Source: "reject-setup.jsonl",
		}); err != nil {
			t.Fatalf("seed the row the stale guard will keep: %v", err)
		}
		if err := kit.SetConfig(ctx, "issue_prefix", ""); err != nil {
			t.Fatalf("clear issue_prefix: %v", err)
		}

		// Both counts are snapshotted: TestImporterUOW shares one database in
		// subtest order, and PrefixSyncAloneCommitsUnderTheSyncMessage has
		// already committed under the sync message by the time this runs — so
		// only a DELTA can tell that the seed's own commit carries it.
		const syncMessageLike = "%sync issue_prefix from config.yaml%"
		before := countHistory(t)
		syncNamedBefore := countHistoryMatching(t, syncMessageLike)
		stale := landed.Add(-24 * time.Hour)
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test",
			Issues: []*types.Issue{{
				ID: "imp-rejected", Title: "Stale snapshot", Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 3, CreatedAt: stale, UpdatedAt: stale,
			}},
			SkipPrefixValidation: true,
			SyncIssuePrefix:      "impseed",
			Source:               "reject.jsonl",
		})
		if err != nil {
			t.Fatalf("all-rejected ImportBatch against a database with no config-table prefix: %v", err)
		}
		// Assert the batch really landed nothing for the reason this case is
		// about, so the prefix assertion below cannot be carried by a row.
		if result.Created != 0 || len(result.StaleRejectedIDs) != 1 {
			t.Fatalf("result = %+v, want Created 0 with the one row stale-rejected", result)
		}
		if !result.PrefixSynced {
			t.Errorf("PrefixSynced = false, want true — the seed wrote the prefix, so the batch must report it")
		}
		if got := queryString(t, "SELECT value FROM config WHERE `key` = 'issue_prefix'"); got != "impseed" {
			t.Errorf("issue_prefix after an all-rejected batch = %q, want %q (the seed was rolled back)", got, "impseed")
		}
		if after := countHistory(t); after != before+1 {
			t.Errorf("history entries moved %d -> %d, want exactly one commit carrying the seeded prefix", before, after)
		}
		if syncNamedAfter := countHistoryMatching(t, syncMessageLike); syncNamedAfter != syncNamedBefore+1 {
			t.Errorf("history entries naming the issue_prefix sync moved %d -> %d, want exactly one more — the seed's own commit must carry that message", syncNamedBefore, syncNamedAfter)
		}

		// Restore for any later subtest.
		if err := kit.SetConfig(ctx, "issue_prefix", "imp"); err != nil {
			t.Fatalf("restore issue_prefix: %v", err)
		}
	})

	t.Run("EmptyActorIsRefused", func(t *testing.T) {
		if _, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{Source: "noactor.jsonl"}); err == nil {
			t.Fatal("ImportBatch with empty actor should be refused")
		}
	})
}
