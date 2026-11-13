package uow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/domain"
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

// importReplayFaultProvider forces exactly one replay of an import attempt:
// the FIRST Commit of the units of work it opens rolls the attempt back for
// real (so attempt 1's writes and its issue_counter row lock are gone), runs
// between — the window a concurrent writer lands in — and then fails with a
// retryable serialization error, so RunTxResult replays the closure. Every
// later Commit passes through.
type importReplayFaultProvider struct {
	UnitOfWorkProvider
	between func(context.Context)
	faults  int
}

func (p *importReplayFaultProvider) NewUOW(ctx context.Context) (UnitOfWork, error) {
	uw, err := p.UnitOfWorkProvider.NewUOW(ctx)
	if err != nil {
		return nil, err
	}
	return &importReplayFaultUOW{UnitOfWork: uw, provider: p}, nil
}

type importReplayFaultUOW struct {
	UnitOfWork
	provider *importReplayFaultProvider
}

// Unwrap keeps importStatementRunner reaching the real transaction.
func (u *importReplayFaultUOW) Unwrap() UnitOfWork { return u.UnitOfWork }

func (u *importReplayFaultUOW) Commit(ctx context.Context, message string) error {
	if u.provider.faults > 0 {
		return u.UnitOfWork.Commit(ctx, message)
	}
	u.provider.faults++
	// A real rollback; RunTxResult's deferred close then has nothing to do.
	u.UnitOfWork.Close(ctx)
	if u.provider.between != nil {
		u.provider.between(ctx)
	}
	return newMySQLError(1213)
}

// TestImporterUOWReplayReMintsUnsetIDs (bd-8qq0ho, GH#8455): ImportBatch hands
// the caller's structs to the create engine, which mints Issue.ID,
// Dependency.IssueID and Comment.ID onto them in place. When RunTxResult
// replays the attempt after a serialization failure, the replay must see the
// structs as attempt 1 did — the ID-less row re-minted, not upserted under the
// rolled-back attempt's id — while caller-supplied ids stay exactly as given.
//
// Row A carries no ids at all and is pinned to 2024 timestamps, so a row a
// competitor writes today is strictly newer and an upsert onto it
// deterministically takes the stale-guard branch. Row B carries all three ids.
//
// ONE PROVIDER, NO t.Parallel (the subtests share the counter and dolt_log).
func TestImporterUOWReplayReMintsUnsetIDs(t *testing.T) {
	ctx := context.Background()
	const prefix = "imr"
	const anchor = "imr-anchor"
	inner := newUOWRoleFixtureProvider(t, ctx, prefix)
	kit := newUOWRoleFixtureKit(inner, prefix)
	if err := kit.CreateIssue(ctx, &types.Issue{
		ID: anchor, Title: "anchor", Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2,
	}, "importer-test"); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}
	if err := kit.SetConfig(ctx, "issue_id_mode", "counter"); err != nil {
		t.Fatalf("set counter mode: %v", err)
	}

	queryInt := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := kit.QueryScalar(ctx, query, args, &n); err != nil {
			t.Fatalf("QueryScalar %q: %v", query, err)
		}
		return n
	}
	// autoMint creates one issue with no explicit id on the unfaulted
	// provider and returns the id the workspace minted for it.
	autoMint := func(t *testing.T, title string) string {
		t.Helper()
		var id string
		if err := RunTx(ctx, inner, func(ctx context.Context, uw UnitOfWork) (string, error) {
			created, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
				Issue: &types.Issue{
					Title: title, Status: types.StatusOpen, IssueType: types.TypeTask, Priority: 2,
				},
				CreateOnly: true,
			}, "other-writer")
			if err != nil {
				return "", err
			}
			id = created.Issue.ID
			return "auto-mint " + id, nil
		}); err != nil {
			t.Fatalf("auto-mint %q: %v", title, err)
		}
		return id
	}

	stamp := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	type replayRun struct {
		rowA, rowB    *types.Issue
		attempt1ID    string // the id attempt 1 minted onto row A (X)
		result        publicops.ImportBatchResult
		competitorRow string // title the competitor wrote under X, if any
	}
	// importWithReplay imports rows A and B through a provider that fails the
	// first commit; competitor, when set, runs after attempt 1's rollback and
	// before the replay, and returns the title it wrote under X.
	importWithReplay := func(t *testing.T, name string, allowStale bool, competitor func(t *testing.T, x string) string) replayRun {
		t.Helper()
		run := replayRun{
			rowA: &types.Issue{
				Title: "replayed row " + name, Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2,
				Dependencies: []*types.Dependency{{DependsOnID: anchor, Type: types.DepBlocks}},
				Comments:     []*types.Comment{{Author: "importer-test", Text: "fresh comment " + name, CreatedAt: stamp}},
				CreatedAt:    stamp, UpdatedAt: stamp,
			},
			rowB: &types.Issue{
				ID: "imr-b-" + name, Title: "explicit row " + name, Status: types.StatusOpen,
				IssueType: types.TypeTask, Priority: 2,
				Dependencies: []*types.Dependency{{IssueID: "imr-b-" + name, DependsOnID: anchor, Type: types.DepBlocks}},
				Comments:     []*types.Comment{{ID: "imr-b-" + name + "-c1", Author: "importer-test", Text: "carried comment " + name, CreatedAt: stamp}},
				CreatedAt:    stamp, UpdatedAt: stamp,
			},
		}
		provider := &importReplayFaultProvider{UnitOfWorkProvider: inner}
		provider.between = func(context.Context) {
			run.attempt1ID = run.rowA.ID
			if run.attempt1ID == "" {
				t.Fatal("attempt 1 committed no mint onto row A; the fault fired before the engine ran")
			}
			if competitor != nil {
				run.competitorRow = competitor(t, run.attempt1ID)
			}
		}
		imp, err := NewImporter(provider)
		if err != nil {
			t.Fatalf("NewImporter: %v", err)
		}
		run.result, err = imp.ImportBatch(ctx, publicops.ImportBatchRequest{
			Actor:      "importer-test",
			Source:     "replay-" + name + ".jsonl",
			AllowStale: allowStale,
			Issues:     []*types.Issue{run.rowA, run.rowB},
		})
		if err != nil {
			t.Fatalf("ImportBatch: %v", err)
		}
		if provider.faults != 1 {
			t.Fatalf("fault fired %d time(s), want exactly 1 replay", provider.faults)
		}
		return run
	}
	// AC-1/AC-3/AC-2's "stored under issue.ID" half: row A landed exactly
	// once, under the id on the caller's struct, with its edge and comment.
	assertRowALanded := func(t *testing.T, run replayRun) {
		t.Helper()
		a := run.rowA
		if a.ID == "" {
			t.Fatal("row A has no id on the caller's struct after the import")
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE title = ?", a.Title); got != 1 {
			t.Errorf("rows titled %q = %d, want 1", a.Title, got)
		}
		if got := queryInt(t, "SELECT COUNT(*) FROM issues WHERE id = ? AND title = ?", a.ID, a.Title); got != 1 {
			t.Errorf("row A not stored under the caller's struct id %q", a.ID)
		}
	}
	assertCleanResult := func(t *testing.T, run replayRun) {
		t.Helper()
		if run.result.Created != 2 || len(run.result.StaleRejectedIDs) != 0 || len(run.result.RetiredIDs) != 0 {
			t.Errorf("result = %+v, want Created=2 with nothing stale-rejected or retired", run.result)
		}
	}
	// AC-1: no other writer. The replay re-draws the same counter value, the
	// counter ends at it, and the next auto-mint draws the one after.
	t.Run("no_competitor", func(t *testing.T) {
		run := importWithReplay(t, "no_competitor", false, nil)
		assertCleanResult(t, run)
		assertRowALanded(t, run)
		x := run.attempt1ID
		if run.rowA.ID != x {
			t.Errorf("row A id = %q, want the replay to re-mint attempt 1's counter value %q", run.rowA.ID, x)
		}
		n, err := strconv.Atoi(strings.TrimPrefix(x, prefix+"-"))
		if err != nil {
			t.Fatalf("attempt 1 minted %q, not a counter id", x)
		}
		if got := queryInt(t, "SELECT last_id FROM issue_counter WHERE prefix = ?", prefix); got != n {
			t.Errorf("issue_counter.last_id = %d, want %d", got, n)
		}
		if next := autoMint(t, "after no_competitor"); next != fmt.Sprintf("%s-%d", prefix, n+1) {
			t.Errorf("next auto-mint = %q, want %s-%d", next, prefix, n+1)
		}
	})
}
