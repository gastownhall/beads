package uow

import (
	"context"
	"fmt"
	"strconv"
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

	t.Run("SingleAttemptKeepsMintedIDsOnTheCallerStructs", func(t *testing.T) {
		// GH#8455's fix resets the caller's structs at the top of EVERY
		// attempt, including the only attempt of an uncontended import. This
		// pins that the reset changes nothing on that path: the minted id is
		// still on the caller's struct — the proxied bd import builds
		// ImportedIDs from it (cmd/bd/import_shared.go) — and the counts and
		// the one-commit contract are exactly today's.
		before := countHistory(t)
		when := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
		row := &types.Issue{
			Title: "Minted onto the caller's struct", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 2, CreatedAt: when, UpdatedAt: when,
		}
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test", Issues: []*types.Issue{row}, Source: "minted.jsonl",
		})
		if err != nil {
			t.Fatalf("ImportBatch: %v", err)
		}
		if result.Created != 1 || len(result.StaleRejectedIDs) != 0 || result.MemoriesImported != 0 {
			t.Errorf("result = %+v, want Created=1 with nothing rejected", result)
		}
		if row.ID == "" {
			t.Fatal("the caller's struct carries no minted id — the proxied bd import would report ImportedIDs empty")
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE id = ?", row.ID); got != 1 {
			t.Errorf("rows stored under the minted id %s = %d, want 1", row.ID, got)
		}
		if after := countHistory(t); after != before+1 {
			t.Errorf("history entries = %d, want %d", after, before+1)
		}
	})
}

// replayFaultProvider models the hazard GH#8455 names: a proxied bd import
// whose FIRST attempt writes everything — ids minted onto the caller's
// structs, aux rows included — and then loses the commit to a serialization
// error. The first unit of work it hands out fails at Commit AFTER a real
// rollback of the attempt, so RunTxResult's replay runs against a database
// that holds neither attempt 1's writes nor its locks. beforeReplay runs in
// that window (after the rollback, before the replay) on the INNER provider —
// exactly where another writer lands a row under the id attempt 1 minted.
type replayFaultProvider struct {
	UnitOfWorkProvider
	// rowA is the caller's ID-less row. Attempt 1's mint lands on it, and
	// the faulting Commit reads that mint off it for beforeReplay.
	rowA *types.Issue
	// beforeReplay, when set, runs after attempt 1's rollback and before the
	// replay, with the id attempt 1 minted onto rowA.
	beforeReplay func(ctx context.Context, mintedID string)
	// uows counts the units of work handed out; faulted counts the commits
	// failed on purpose. A subtest wants exactly one fault and at least two
	// units of work.
	uows, faulted int
}

func (p *replayFaultProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	uw, err := p.UnitOfWorkProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	p.uows++
	return &replayFaultUOW{UnitOfWork: uw, provider: p, fail: p.uows == 1}, nil
}

// replayFaultUOW decorates the unit of work RunTxResult commits through. It
// implements unitOfWorkUnwrapper so importStatementRunner's
// unwrapUOW(uw).(*baseUOW) still reaches the real transaction, the same way
// beforeScopedCommitUOW does for the heartbeat contracts.
type replayFaultUOW struct {
	UnitOfWork
	provider *replayFaultProvider
	fail     bool
}

func (u *replayFaultUOW) Unwrap() UnitOfWork { return u.UnitOfWork }

func (u *replayFaultUOW) Commit(ctx context.Context, message string) error {
	if !u.fail {
		return u.UnitOfWork.Commit(ctx, message)
	}
	u.provider.faulted++
	minted := u.provider.rowA.ID
	// A REAL rollback, not just a synthetic error: it discards attempt 1's
	// writes and releases the issue_counter row lock the mint still holds,
	// so a writer in beforeReplay can draw the same counter value, and the
	// deferred closeAttempt in RunTxResultWithin finds nothing left to do
	// (RollbackUnlessCommitted).
	u.UnitOfWork.Close(ctx)
	if u.provider.beforeReplay != nil {
		u.provider.beforeReplay(ctx, minted)
	}
	return newMySQLError(1213)
}

// TestImporterUOWReplayReMintsUnsetIDs (GH#8455): a serialization failure at
// COMMIT time replays ImportBatch's closure after the batch-upsert engine has
// minted ids onto the caller's structs. The replay must see the structs as
// the caller handed them over — re-minting exactly the fields the caller left
// empty — instead of reading the aborted attempt's mints as caller-supplied
// ids and upserting under them.
//
// ONE PROVIDER FOR THE WHOLE SUITE and NO t.Parallel, like TestImporterUOW:
// dolt_log is database-global here, and the counter subtests advance
// issue_counter in a shared sequence.
func TestImporterUOWReplayReMintsUnsetIDs(t *testing.T) {
	ctx := context.Background()
	inner := newUOWRoleFixtureProvider(t, ctx, "imr")
	kit := newUOWRoleFixtureKit(inner, "imr")
	if err := kit.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("set issue_id_mode=counter: %v", err)
	}

	// The anchor is the dependency target the subtests' rows hang their
	// blocks edges on; it is AUTO-MINTED under the plain prefix.
	anchor := &types.Issue{
		Title: "replay anchor", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2,
	}
	if err := kit.CreateIssue(ctx, anchor, "replay-seed"); err != nil {
		t.Fatalf("seed the anchor: %v", err)
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
	// counterFor reads a subtest counter's committed last_id — 0 when the
	// row does not exist, the state a replay that upserted under the
	// aborted mint leaves behind (its seed rolled back with attempt 1).
	counterFor := func(t *testing.T, sub string) int {
		t.Helper()
		return queryInt(t, "SELECT COALESCE((SELECT last_id FROM issue_counter WHERE prefix = ?), 0)", "imr-"+sub)
	}
	suffixFor := func(t *testing.T, sub, id string) int {
		t.Helper()
		n, err := strconv.Atoi(strings.TrimPrefix(id, "imr-"+sub+"-"))
		if err != nil {
			t.Fatalf("id %q does not carry a numeric %s counter suffix: %v", id, sub, err)
		}
		return n
	}

	// rowA builds the caller's ID-less row, minted under the subtest's own
	// IDPrefix so every subtest draws from its OWN issue_counter row and
	// they stay independent even when one of them leaves the broken state
	// behind. CreatedAt/UpdatedAt are pinned to 2024-01-01 so a competitor
	// created during the test is strictly newer and the broken replay
	// deterministically takes the stale-reject branch rather than the ODKU's
	// equal-timestamp tie.
	rowA := func(title, sub string) *types.Issue {
		when := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		return &types.Issue{
			Title: title, Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, IDPrefix: sub,
			Dependencies: []*types.Dependency{{DependsOnID: anchor.ID, Type: types.DepBlocks}},
			Comments:     []*types.Comment{{Author: "importer-test", Text: "comment on " + title, CreatedAt: when}},
			CreatedAt:    when, UpdatedAt: when,
		}
	}
	// rowB carries every id the caller supplied; the subtests pin that the
	// replay reset never clears those.
	rowB := func(suffix string) *types.Issue {
		when := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
		id := "imr-kept-" + suffix
		return &types.Issue{
			ID: id, Title: "Caller-supplied ids stay (" + suffix + ")", Status: types.StatusOpen,
			IssueType: types.TypeTask, Priority: 2,
			Dependencies: []*types.Dependency{{IssueID: id, DependsOnID: anchor.ID, Type: types.DepBlocks}},
			Comments:     []*types.Comment{{ID: id + "-c1", Author: "importer-test", Text: "kept comment", CreatedAt: when}},
			CreatedAt:    when, UpdatedAt: when,
		}
	}
	assertRowBKept := func(t *testing.T, row *types.Issue, suffix string) {
		t.Helper()
		id, commentID := "imr-kept-"+suffix, "imr-kept-"+suffix+"-c1"
		if row.ID != id || row.Dependencies[0].IssueID != id || row.Comments[0].ID != commentID {
			t.Errorf("caller-supplied ids on the struct = (%q, %q, %q), want (%q, %q, %q)",
				row.ID, row.Dependencies[0].IssueID, row.Comments[0].ID, id, id, commentID)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE id = ?", id); got != 1 {
			t.Errorf("rows stored under the caller's id %s = %d, want 1", id, got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?", id, anchor.ID); got != 1 {
			t.Errorf("edges stored under the caller's id %s = %d, want 1", id, got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM comments WHERE id = ? AND issue_id = ?", commentID, id); got != 1 {
			t.Errorf("comments stored under the caller's id %s = %d, want 1", commentID, got)
		}
	}
	seedCompetitor := func(t *testing.T, ctx context.Context, sub string) *types.Issue {
		t.Helper()
		competitor := &types.Issue{
			Title: "concurrent competitor row", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2,
			IDPrefix: sub,
		}
		if err := kit.CreateIssue(ctx, competitor, "competitor-writer"); err != nil {
			t.Fatalf("the concurrent writer's create: %v", err)
		}
		return competitor
	}

	// runReplayedBatch imports the two rows through a provider whose first
	// commit fails with a serialization error after a real rollback, and
	// returns the batch result and the id attempt 1 minted onto the ID-less
	// row.
	runReplayedBatch := func(t *testing.T, idless, kept *types.Issue, beforeReplay func(context.Context, string), allowStale bool) (publicops.ImportBatchResult, string) {
		t.Helper()
		var mintedAttempt1 string
		provider := &replayFaultProvider{
			UnitOfWorkProvider: inner,
			rowA:               idless,
			beforeReplay: func(ctx context.Context, minted string) {
				mintedAttempt1 = minted
				if beforeReplay != nil {
					beforeReplay(ctx, minted)
				}
			},
		}
		imp, err := NewImporter(provider)
		if err != nil {
			t.Fatalf("NewImporter: %v", err)
		}
		result, err := imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor: "importer-test", Issues: []*types.Issue{idless, kept},
			AllowStale: allowStale, Source: "replay.jsonl",
		})
		if err != nil {
			t.Fatalf("ImportBatch: %v", err)
		}
		if provider.faulted != 1 {
			t.Fatalf("the commit fault fired %d times, want exactly 1", provider.faulted)
		}
		if provider.uows < 2 {
			t.Fatalf("the import ran %d unit(s) of work; the replay did not happen", provider.uows)
		}
		if mintedAttempt1 == "" {
			t.Fatalf("attempt 1 minted no id onto the caller's struct; the fixture cannot observe the hazard")
		}
		return result, mintedAttempt1
	}

	t.Run("no_competitor", func(t *testing.T) {
		const sub = "nc"
		idless, kept := rowA("re-minted with no other writer", sub), rowB(sub)
		result, x := runReplayedBatch(t, idless, kept, nil, false)

		// AC-1: the replay re-minted the same counter value (the rollback
		// restored it), the row is stored under it, the committed counter
		// names it, and the next auto-mint draws the following value.
		if result.Created != 2 || len(result.StaleRejectedIDs) != 0 {
			t.Errorf("result = %+v, want Created=2 with nothing rejected", result)
		}
		if idless.ID != x {
			t.Errorf("the caller's struct id = %q, want the re-mint %q", idless.ID, x)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE id = ?", x); got != 1 {
			t.Errorf("rows stored under %s = %d, want 1", x, got)
		}
		if got, want := counterFor(t, sub), suffixFor(t, sub, x); got != want {
			t.Errorf("issue_counter.last_id for imr-%s = %d, want %d — the replay upserted under the aborted mint and never advanced the counter", sub, got, want)
		}
		assertRowBKept(t, kept, sub)
		next := &types.Issue{Title: "first create after the replay", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2, IDPrefix: sub}
		if err := kit.CreateIssue(ctx, next, "replay-seed"); err != nil {
			t.Fatalf("the next auto-minted create: %v (the counter re-drew the aborted attempt's value)", err)
		}
		if want := fmt.Sprintf("imr-%s-%d", sub, suffixFor(t, sub, x)+1); next.ID != want {
			t.Errorf("the next auto-minted create got %q, want %q", next.ID, want)
		}
	})

	t.Run("competitor_stale_guard", func(t *testing.T) {
		idless, kept := rowA("re-minted past a concurrent writer", "cg"), rowB("cg")
		result, x := runReplayedBatch(t, idless, kept, func(ctx context.Context, minted string) {
			competitor := seedCompetitor(t, ctx, "cg")
			if competitor.ID != minted {
				t.Fatalf("the competitor minted %q; want the rolled-back attempt's %q — the race did not reproduce", competitor.ID, minted)
			}
		}, false)

		// AC-2: the other writer's row is untouched and the import re-minted
		// past it under the stale guard.
		if len(result.StaleRejectedIDs) != 0 || result.Created != 2 {
			t.Errorf("result = Created %d, StaleRejectedIDs %v — want Created 2 with nothing rejected", result.Created, result.StaleRejectedIDs)
		}
		if idless.ID == "" || idless.ID == x {
			t.Fatalf("the caller's struct id = %q, want a fresh re-mint other than the aborted %q", idless.ID, x)
		}
		if got := queryString(t, "SELECT title FROM issues WHERE id = ?", x); got != "concurrent competitor row" {
			t.Errorf("the competitor's row at %s = %q, want its own title", x, got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE id = ?", idless.ID); got != 1 {
			t.Errorf("rows stored under the re-minted %s = %d, want 1", idless.ID, got)
		}
		// AC-3: the dependency and the comment re-landed under the re-minted
		// id — none under the aborted one.
		if dep := idless.Dependencies[0]; dep.IssueID != idless.ID {
			t.Errorf("dep.IssueID on the struct = %q, want the re-minted %q", dep.IssueID, idless.ID)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ? AND depends_on_issue_id = ?", idless.ID, anchor.ID); got != 1 {
			t.Errorf("edges under the re-minted %s = %d, want 1", idless.ID, got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM dependencies WHERE issue_id = ?", x); got != 0 {
			t.Errorf("edges under the aborted %s = %d, want 0", x, got)
		}
		if c := idless.Comments[0]; c.ID == "" {
			t.Error("the caller's comment struct carries no minted comment id")
		} else if got := queryInt(t, "SELECT COUNT(*) FROM comments WHERE id = ? AND issue_id = ?", c.ID, idless.ID); got != 1 {
			t.Errorf("comments under the minted %s = %d, want 1", c.ID, got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM comments WHERE issue_id = ?", x); got != 0 {
			t.Errorf("comments under the aborted %s = %d, want 0", x, got)
		}
		assertRowBKept(t, kept, "cg")
	})

	t.Run("competitor_allow_stale", func(t *testing.T) {
		// The stale guard is off; the replay still re-mints, so the import
		// lands under its own id and the competitor keeps its columns.
		// Before the fix the replay upserted under the aborted mint and
		// overwrote the competitor's row wholesale.
		idless, kept := rowA("allow-stale keeps the other writer's columns", "as"), rowB("as")
		_, x := runReplayedBatch(t, idless, kept, func(ctx context.Context, minted string) {
			competitor := seedCompetitor(t, ctx, "as")
			if competitor.ID != minted {
				t.Fatalf("the competitor minted %q; want the rolled-back attempt's %q — the race did not reproduce", competitor.ID, minted)
			}
		}, true)

		if got := queryString(t, "SELECT title FROM issues WHERE id = ?", x); got != "concurrent competitor row" {
			t.Errorf("the competitor's row at %s = %q, want its own title (--allow-stale must not overwrite another writer's row)", x, got)
		}
		if idless.ID == "" || idless.ID == x {
			t.Fatalf("the caller's struct id = %q, want a fresh re-mint other than the aborted %q", idless.ID, x)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE id = ?", idless.ID); got != 1 {
			t.Errorf("rows stored under the re-minted %s = %d, want 1", idless.ID, got)
		}
		assertRowBKept(t, kept, "as")
	})

	t.Run("hash_mode_competitor", func(t *testing.T) {
		// Default hash mode: the mint is content-derived, so the replay's
		// first candidate IS the aborted attempt's id, now held by another
		// writer. The replay must step to the next nonce instead of
		// upserting under it.
		if err := kit.SetConfig(ctx, "issue_id_mode", ""); err != nil {
			t.Fatalf("switch to hash mode: %v", err)
		}
		t.Cleanup(func() {
			if err := kit.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
				t.Errorf("restore counter mode: %v", err)
			}
		})
		idless, kept := rowA("hash-mode replay steps to the next nonce", "hm"), rowB("hm")
		_, x := runReplayedBatch(t, idless, kept, func(ctx context.Context, minted string) {
			// The competitor takes the aborted mint's id explicitly: in
			// hash mode an explicit id does not disturb the candidate
			// ladder, and the deterministic candidate order is what the
			// replay must walk past.
			if err := kit.CreateIssue(ctx, &types.Issue{
				ID: minted, Title: "hash competitor row", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2,
			}, "competitor-writer"); err != nil {
				t.Fatalf("seed the hash competitor at %s: %v", minted, err)
			}
		}, false)

		if idless.ID == "" || idless.ID == x {
			t.Fatalf("the caller's struct id = %q, want the next nonce candidate after the taken %q", idless.ID, x)
		}
		if got := queryString(t, "SELECT title FROM issues WHERE id = ?", x); got != "hash competitor row" {
			t.Errorf("the competitor's row at %s = %q, want its own title", x, got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE id = ?", idless.ID); got != 1 {
			t.Errorf("rows stored under the re-minted %s = %d, want 1", idless.ID, got)
		}
		assertRowBKept(t, kept, "hm")
	})
}
