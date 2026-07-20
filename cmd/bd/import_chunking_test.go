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
	batches    [][]*types.Issue
	calls      int
	failOnCall int
}

func (f *chunkRecordingStore) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return nil, nil
}

func (f *chunkRecordingStore) CreateIssuesWithFullOptions(_ context.Context, issues []*types.Issue, _ string, _ storage.BatchCreateOptions) error {
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
