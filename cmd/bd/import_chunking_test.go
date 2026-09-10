package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// chunkRecordingStore records every CreateIssuesWithFullOptions call as a
// snapshot (issue rows plus the dependencies attached at call time), so tests
// can assert on transaction boundaries. failOnCall simulates a mid-import
// failure at the Nth call (1-based; 0 = never fail).
//
// Tests that need a real, persisting engine (durable committed prefixes,
// readiness recomputation, event dedup) live in import_chunking_embedded_test.go
// and run against embedded Dolt behind BEADS_TEST_EMBEDDED_DOLT; the shared
// helpers below are reused by both files.
type chunkRecordingStore struct {
	storage.DoltStorage
	batches [][]*types.Issue
	// conflictSkip records opts.ConflictSkip per batch: true marks a
	// dependency-pass transaction, false a phase-1 row chunk.
	conflictSkip []bool
	calls        int
	failOnCall   int
}

func (f *chunkRecordingStore) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return nil, nil
}

func (f *chunkRecordingStore) CreateIssuesWithFullOptions(_ context.Context, issues []*types.Issue, _ string, opts storage.BatchCreateOptions) error {
	f.calls++
	if f.failOnCall != 0 && f.calls == f.failOnCall {
		return errors.New("simulated chunk failure")
	}
	snapshot := make([]*types.Issue, len(issues))
	for i, issue := range issues {
		cp := *issue
		cp.Dependencies = append([]*types.Dependency(nil), issue.Dependencies...)
		snapshot[i] = &cp
	}
	f.batches = append(f.batches, snapshot)
	f.conflictSkip = append(f.conflictSkip, opts.ConflictSkip)
	return nil
}

func setImportChunkSize(t *testing.T, n int) {
	t.Helper()
	old := importChunkSize
	importChunkSize = n
	t.Cleanup(func() { importChunkSize = old })
}

func setImportProgressBuffer(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := importProgress
	buf := &bytes.Buffer{}
	importProgress = buf
	t.Cleanup(func() { importProgress = old })
	return buf
}

// recordImportPauses replaces the inter-chunk sleep with a counter so tests
// run at full speed while still asserting the pause is issued.
func recordImportPauses(t *testing.T) *int {
	t.Helper()
	old := importPause
	count := 0
	importPause = func(time.Duration) { count++ }
	t.Cleanup(func() { importPause = old })
	return &count
}

func chunkTestIssues(n int) []*types.Issue {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	issues := make([]*types.Issue, n)
	for i := range issues {
		issues[i] = &types.Issue{
			ID:        fmt.Sprintf("bd-chunk%02d", i+1),
			Title:     fmt.Sprintf("chunk issue %d", i+1),
			UpdatedAt: base,
		}
		issues[i].SetDefaults()
	}
	return issues
}

// An import at or below the chunk size must keep today's semantics exactly:
// one CreateIssuesWithFullOptions call, dependencies inline, one transaction.
func TestImportIssuesCoreSingleBatchAtOrBelowChunkSize(t *testing.T) {
	setImportChunkSize(t, 4)
	recordImportPauses(t)
	issues := chunkTestIssues(4)
	issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[3].ID, Type: types.DepBlocks}}

	store := &chunkRecordingStore{}
	result, err := importIssuesCore(context.Background(), "", store, issues, ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("calls = %d, want exactly 1 transaction for a small import", store.calls)
	}
	if len(store.batches[0]) != 4 {
		t.Fatalf("batch size = %d, want 4", len(store.batches[0]))
	}
	foundDep := false
	for _, issue := range store.batches[0] {
		if issue.ID == issues[0].ID && len(issue.Dependencies) == 1 {
			foundDep = true
		}
	}
	if !foundDep {
		t.Fatalf("small import must keep dependencies inline in the single batch")
	}
	if result.Created != 4 {
		t.Fatalf("Created = %d, want 4", result.Created)
	}
}

// A large import must be split into bounded transactions so the write lock is
// released between chunks instead of being held for the whole batch.
func TestImportIssuesCoreChunksLargeImports(t *testing.T) {
	setImportChunkSize(t, 3)
	recordImportPauses(t)
	progress := setImportProgressBuffer(t)
	issues := chunkTestIssues(8)

	store := &chunkRecordingStore{}
	result, err := importIssuesCore(context.Background(), "", store, issues, ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if store.calls != 3 {
		t.Fatalf("calls = %d, want 3 bounded transactions (3+3+2)", store.calls)
	}
	wantSizes := []int{3, 3, 2}
	seen := map[string]int{}
	for i, batch := range store.batches {
		if len(batch) != wantSizes[i] {
			t.Fatalf("batch %d size = %d, want %d", i, len(batch), wantSizes[i])
		}
		for _, issue := range batch {
			seen[issue.ID]++
		}
	}
	for _, issue := range issues {
		if seen[issue.ID] != 1 {
			t.Fatalf("issue %s written %d times, want exactly once", issue.ID, seen[issue.ID])
		}
	}
	if result.Created != 8 {
		t.Fatalf("Created = %d, want 8", result.Created)
	}
	if got := progress.String(); !strings.Contains(got, "8/8") {
		t.Fatalf("progress output missing final count, got %q", got)
	}
}

// Exactly chunk-size and chunk-size+1 imports: no empty trailing chunk, and
// the boundary issue lands in a second transaction.
func TestImportIssuesCoreChunkBoundaries(t *testing.T) {
	setImportChunkSize(t, 3)
	recordImportPauses(t)

	store := &chunkRecordingStore{}
	if _, err := importIssuesCore(context.Background(), "", store, chunkTestIssues(3), ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("calls = %d, want 1 for an exactly-chunk-size import", store.calls)
	}

	store = &chunkRecordingStore{}
	if _, err := importIssuesCore(context.Background(), "", store, chunkTestIssues(4), ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if store.calls != 2 {
		t.Fatalf("calls = %d, want 2 for a chunk-size+1 import", store.calls)
	}
	if got := []int{len(store.batches[0]), len(store.batches[1])}; got[0] != 3 || got[1] != 1 {
		t.Fatalf("batch sizes = %v, want [3 1]", got)
	}
}

// Readiness-affecting dependencies must land in the same transaction as the
// dependent's row, whatever order the JSONL puts the rows in: the import
// reorders rows so every (acyclic) blocking target lands in the same or an
// earlier chunk, and the edge rides inline with the row. No separate
// dependency pass may exist for them — a dependency pass is a window in which
// a concurrent reader sees the row without its edges.
func TestImportChunkedBlockingDepsLandInSameTransactionAsRow(t *testing.T) {
	setImportChunkSize(t, 3)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	issues := chunkTestIssues(7)
	// Forward reference across the chunk boundary in file order: 1 -> 7.
	issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[6].ID, Type: types.DepBlocks}}
	// Backward reference: 5 -> 1.
	issues[4].Dependencies = []*types.Dependency{{IssueID: issues[4].ID, DependsOnID: issues[0].ID, Type: types.DepBlocks}}

	store := &chunkRecordingStore{}
	if _, err := importIssuesCore(context.Background(), "", store, issues, ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if store.calls != 3 {
		t.Fatalf("calls = %d, want 3 row chunks and NO separate dependency pass", store.calls)
	}

	// Every dependency must ride in its owner's row batch, and its target must
	// already exist by the end of that batch (same or earlier batch).
	batchOf := map[string]int{}
	for b, batch := range store.batches {
		for _, issue := range batch {
			batchOf[issue.ID] = b
		}
	}
	wantDeps := map[string]string{
		issues[0].ID: issues[6].ID,
		issues[4].ID: issues[0].ID,
	}
	got := map[string]string{}
	for _, batch := range store.batches {
		for _, issue := range batch {
			for _, dep := range issue.Dependencies {
				got[issue.ID] = dep.DependsOnID
				tb, ok := batchOf[dep.DependsOnID]
				if !ok {
					t.Fatalf("dependency target %s never written", dep.DependsOnID)
				}
				if tb > batchOf[issue.ID] {
					t.Fatalf("issue %s (batch %d) carries an edge to %s (batch %d): target does not exist when the edge commits",
						issue.ID, batchOf[issue.ID], dep.DependsOnID, tb)
				}
			}
		}
	}
	for id, target := range wantDeps {
		if got[id] != target {
			t.Fatalf("edge %s -> %s not written inline (got %q)", id, target, got[id])
		}
	}
	// The caller's issues must come back with dependencies intact so a retry
	// of the same slice still carries them.
	if len(issues[0].Dependencies) != 1 || len(issues[4].Dependencies) != 1 {
		t.Fatalf("original issues lost their dependencies after import")
	}
}

// A failure mid-import must surface as an error naming the committed prefix,
// stop issuing further transactions, and leave the input re-runnable.
func TestImportIssuesCoreChunkedMidFailureLeavesCommittedPrefix(t *testing.T) {
	setImportChunkSize(t, 3)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	issues := chunkTestIssues(8)
	issues[7].Dependencies = []*types.Dependency{{IssueID: issues[7].ID, DependsOnID: issues[0].ID, Type: types.DepBlocks}}

	store := &chunkRecordingStore{failOnCall: 2}
	_, err := importIssuesCore(context.Background(), "", store, issues, ImportOptions{SkipPrefixValidation: true})
	if err == nil {
		t.Fatalf("importIssuesCore succeeded, want mid-chunk failure to surface")
	}
	if !strings.Contains(err.Error(), "3 issues already committed") {
		t.Fatalf("error %q does not name the committed prefix", err)
	}
	if !strings.Contains(err.Error(), "re-run") {
		t.Fatalf("error %q does not tell the user the import is re-runnable", err)
	}
	if store.calls != 2 {
		t.Fatalf("calls = %d, want to stop after the failing chunk", store.calls)
	}
	if len(store.batches) != 1 || len(store.batches[0]) != 3 {
		t.Fatalf("committed prefix = %d batches, want exactly the first chunk", len(store.batches))
	}
	if len(issues[7].Dependencies) != 1 {
		t.Fatalf("failure path lost the caller's dependencies; retry would drop edges")
	}
}

// The bounded transactions must not run back-to-back: a chunked import that
// re-takes the write lock microseconds after each commit starves every
// concurrent bd operation for the whole import (SQLite busy-polling has no
// fairness queue). A pause must separate every adjacent pair of import
// transactions, including the boundary into the deferred-dependency pass.
func TestImportChunkedPausesBetweenChunkTransactions(t *testing.T) {
	setImportChunkSize(t, 3)
	setImportProgressBuffer(t)
	pauses := recordImportPauses(t)
	issues := chunkTestIssues(8)
	// A non-blocking forward reference forces a deferred-dependency pass, so
	// the count also covers the phase boundary. 3 row chunks + 1 dep pass.
	issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[7].ID, Type: types.DepRelated}}

	store := &chunkRecordingStore{}
	if _, err := importIssuesCore(context.Background(), "", store, issues, ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if store.calls != 4 {
		t.Fatalf("calls = %d, want 3 row chunks + 1 deferred-dependency pass", store.calls)
	}
	if *pauses != store.calls-1 {
		t.Fatalf("pauses = %d, want one between every adjacent pair of transactions (%d)", *pauses, store.calls-1)
	}
	if importInterChunkPause <= 0 {
		t.Fatalf("importInterChunkPause = %v, want a positive gap for lock fairness", importInterChunkPause)
	}
}

// orderImportIssuesForChunking must emit a cycle member before a valid row that
// blocks on it, even when that row precedes the cycle in file order. A plain
// file-order cycle fallback would chunk the dependent ahead of its blocker and
// defer the live readiness edge. Regression for the attempt-1 review finding.
func TestOrderImportIssuesForChunkingPlacesCycleBeforeDependent(t *testing.T) {
	issues := chunkTestIssues(4)
	// bd-chunk03 <-> bd-chunk04 is a tolerated blocking cycle; bd-chunk01
	// validly blocks on bd-chunk03 — an acyclic edge pointing into the cycle.
	issues[2].Dependencies = []*types.Dependency{{IssueID: issues[2].ID, DependsOnID: issues[3].ID, Type: types.DepBlocks}}
	issues[3].Dependencies = []*types.Dependency{{IssueID: issues[3].ID, DependsOnID: issues[2].ID, Type: types.DepBlocks}}
	issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[2].ID, Type: types.DepBlocks}}

	ordered := orderImportIssuesForChunking(issues)
	if len(ordered) != len(issues) {
		t.Fatalf("ordered length = %d, want %d", len(ordered), len(issues))
	}
	pos := map[string]int{}
	for i, issue := range ordered {
		pos[issue.ID] = i
	}
	if pos["bd-chunk03"] > pos["bd-chunk01"] {
		t.Fatalf("bd-chunk03 (blocker on a cycle) at %d must precede its dependent bd-chunk01 at %d so the edge rides inline",
			pos["bd-chunk03"], pos["bd-chunk01"])
	}
}

func crossBucketTestIssue(id string, wisp bool) *types.Issue {
	issue := &types.Issue{ID: id, Title: id, UpdatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), Ephemeral: wisp}
	issue.SetDefaults()
	return issue
}

// assertNoSameBatchCrossBucketEdge fails if any recorded batch carries an edge
// whose target is a row of that same batch on the other plane — the exact
// shape the engine's per-batch cross-bucket filter skip-reports as a
// cross-bucket dependency. It also fails if an edge commits
// before its target row exists (target first written in a later batch).
func assertNoSameBatchCrossBucketEdge(t *testing.T, store *chunkRecordingStore) {
	t.Helper()
	firstBatchOf := map[string]int{}
	wispByID := map[string]bool{}
	for b, batch := range store.batches {
		for _, issue := range batch {
			if _, ok := firstBatchOf[issue.ID]; !ok {
				firstBatchOf[issue.ID] = b
			}
			wispByID[issue.ID] = issue.Ephemeral
		}
	}
	for b, batch := range store.batches {
		inBatch := map[string]bool{}
		for _, issue := range batch {
			inBatch[issue.ID] = true
		}
		for _, issue := range batch {
			for _, dep := range issue.Dependencies {
				tb, ok := firstBatchOf[dep.DependsOnID]
				if !ok {
					t.Fatalf("dependency target %s never written", dep.DependsOnID)
				}
				if tb > b {
					t.Fatalf("batch %d: %s -> %s commits before its target exists (target first written in batch %d)", b, issue.ID, dep.DependsOnID, tb)
				}
				if inBatch[dep.DependsOnID] && wispByID[issue.ID] != wispByID[dep.DependsOnID] {
					t.Fatalf("batch %d: %s -> %s is a regular<->wisp edge whose target is in the same batch; the engine would skip-report it as cross-bucket", b, issue.ID, dep.DependsOnID)
				}
			}
		}
	}
}

// depTargetsIn returns the dependency targets batch b carries for issue id.
func depTargetsIn(store *chunkRecordingStore, b int, id string) []string {
	var targets []string
	for _, issue := range store.batches[b] {
		if issue.ID != id {
			continue
		}
		for _, dep := range issue.Dependencies {
			targets = append(targets, dep.DependsOnID)
		}
	}
	return targets
}

// A regular<->wisp edge whose endpoints are created in the same chunk cannot be
// wired in that chunk's transaction (the engine's per-batch cross-bucket
// filter skip-reports it), so the
// import must defer it to the dependency pass, while one into an earlier chunk
// points at a committed row and rides inline. Every dependency-pass
// transaction must be single-plane, or a deferred edge's target could sit in
// the same batch on the other plane and be skipped all over again. Pre-fix the
// import filtered every in-batch cross-bucket edge out up front and reported
// it skipped — export→import lost every such edge, and a re-run could never
// backfill it (wy-4276q8).
func TestImportChunkedCrossBucketEdgesDeferredToSinglePlanePass(t *testing.T) {
	setImportChunkSize(t, 4)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	// File order and blocking edges chosen so that Kahn's ordering (indegree-0
	// rows in file order, dependents released behind them) yields
	//   chunk 0: f1 f2 f3 f4   chunk 1: w1 r2 w3 r1   chunk 2: w2
	// r1 -> w1 lands in the same chunk as its wisp target (must defer);
	// w3 -> f1 and w2 -> r2 point at earlier chunks (must ride inline).
	issues := []*types.Issue{
		crossBucketTestIssue("bd-f1", false),
		crossBucketTestIssue("bd-f2", false),
		crossBucketTestIssue("bd-f3", false),
		crossBucketTestIssue("bd-f4", false),
		crossBucketTestIssue("bd-w1", true),
		crossBucketTestIssue("bd-r1", false),
		crossBucketTestIssue("bd-w2", true),
		crossBucketTestIssue("bd-r2", false),
		crossBucketTestIssue("bd-w3", true),
	}
	issues[5].Dependencies = []*types.Dependency{{IssueID: "bd-r1", DependsOnID: "bd-w1", Type: types.DepBlocks}}
	issues[6].Dependencies = []*types.Dependency{{IssueID: "bd-w2", DependsOnID: "bd-r2", Type: types.DepBlocks}}
	issues[8].Dependencies = []*types.Dependency{{IssueID: "bd-w3", DependsOnID: "bd-f1", Type: types.DepBlocks}}

	store := &chunkRecordingStore{}
	result, err := importIssuesCore(context.Background(), "", store, issues, ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if store.calls != 4 {
		t.Fatalf("calls = %d, want 3 row chunks + 1 single-plane dependency pass", store.calls)
	}
	for b := 0; b < 3; b++ {
		if store.conflictSkip[b] {
			t.Fatalf("batch %d is a row chunk but was submitted with ConflictSkip", b)
		}
	}
	if !store.conflictSkip[3] {
		t.Fatalf("batch 3 must be the dependency pass (ConflictSkip)")
	}
	assertNoSameBatchCrossBucketEdge(t, store)

	// The same-chunk cross-bucket edge is stripped from its row write and wired
	// by the pass; the pass carries only that row.
	if got := depTargetsIn(store, 1, "bd-r1"); len(got) != 0 {
		t.Fatalf("bd-r1 row chunk carries deps %v, want none (same-chunk wisp target must be deferred)", got)
	}
	if len(store.batches[3]) != 1 || store.batches[3][0].ID != "bd-r1" {
		t.Fatalf("dependency pass rows = %v, want exactly bd-r1", store.batches[3])
	}
	if got := depTargetsIn(store, 3, "bd-r1"); len(got) != 1 || got[0] != "bd-w1" {
		t.Fatalf("dependency pass deps for bd-r1 = %v, want [bd-w1]", got)
	}
	// Cross-bucket edges into earlier chunks ride inline with their rows.
	if got := depTargetsIn(store, 1, "bd-w3"); len(got) != 1 || got[0] != "bd-f1" {
		t.Fatalf("bd-w3 inline deps = %v, want [bd-f1]", got)
	}
	if got := depTargetsIn(store, 2, "bd-w2"); len(got) != 1 || got[0] != "bd-r2" {
		t.Fatalf("bd-w2 inline deps = %v, want [bd-r2]", got)
	}
	if result.Created != 9 {
		t.Fatalf("Created = %d, want 9", result.Created)
	}
	if len(result.SkippedDependencies) != 0 {
		t.Fatalf("SkippedDependencies = %#v, want none", result.SkippedDependencies)
	}
	// The caller's slice keeps its dependencies for a retry.
	for _, i := range []int{5, 6, 8} {
		if len(issues[i].Dependencies) != 1 {
			t.Fatalf("issue %s lost its dependency after import", issues[i].ID)
		}
	}
}

// A small import (at or below the chunk size) keeps its single transaction
// unless it carries a regular<->wisp edge between two of its own rows: that
// edge would be skip-reported by the engine's per-batch cross-bucket filter,
// so the import takes the chunked path and the edge lands in a dependency pass
// instead of being skip-reported.
func TestImportIssuesCoreSmallBatchCrossBucketEdgeTakesDependencyPass(t *testing.T) {
	setImportChunkSize(t, 4)
	recordImportPauses(t)
	setImportProgressBuffer(t)

	wisp := crossBucketTestIssue("bd-w1", true)
	regular := crossBucketTestIssue("bd-r1", false)
	regular.Dependencies = []*types.Dependency{{IssueID: "bd-r1", DependsOnID: "bd-w1", Type: types.DepBlocks}}
	issues := []*types.Issue{wisp, regular}

	store := &chunkRecordingStore{}
	result, err := importIssuesCore(context.Background(), "", store, issues, ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if store.calls != 2 {
		t.Fatalf("calls = %d, want 1 row chunk + 1 dependency pass for a small import with a cross-bucket edge", store.calls)
	}
	if store.conflictSkip[0] || !store.conflictSkip[1] {
		t.Fatalf("conflictSkip = %v, want [false true] (row chunk, then dependency pass)", store.conflictSkip)
	}
	if len(store.batches[0]) != 2 {
		t.Fatalf("row chunk size = %d, want both rows", len(store.batches[0]))
	}
	assertNoSameBatchCrossBucketEdge(t, store)
	if got := depTargetsIn(store, 0, "bd-r1"); len(got) != 0 {
		t.Fatalf("row chunk carries %v for bd-r1, want none", got)
	}
	if got := depTargetsIn(store, 1, "bd-r1"); len(got) != 1 || got[0] != "bd-w1" {
		t.Fatalf("dependency pass deps for bd-r1 = %v, want [bd-w1]", got)
	}
	if result.Created != 2 {
		t.Fatalf("Created = %d, want 2", result.Created)
	}
	if len(regular.Dependencies) != 1 {
		t.Fatalf("caller's issue lost its dependency after import")
	}

	// Control: the same shape on one plane stays a single inline transaction.
	a := crossBucketTestIssue("bd-r2", false)
	b := crossBucketTestIssue("bd-r3", false)
	b.Dependencies = []*types.Dependency{{IssueID: "bd-r3", DependsOnID: "bd-r2", Type: types.DepBlocks}}
	single := &chunkRecordingStore{}
	if _, err := importIssuesCore(context.Background(), "", single, []*types.Issue{a, b}, ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("importIssuesCore (same-plane control): %v", err)
	}
	if single.calls != 1 || single.conflictSkip[0] {
		t.Fatalf("same-plane small import: calls = %d conflictSkip = %v, want one inline transaction", single.calls, single.conflictSkip)
	}
	if got := depTargetsIn(single, 0, "bd-r3"); len(got) != 1 || got[0] != "bd-r2" {
		t.Fatalf("same-plane small import deps = %v, want inline [bd-r2]", got)
	}
}
