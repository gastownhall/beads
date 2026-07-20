//go:build cgo

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
)

// These tests exercise the chunked-import logic against a real, persisting
// engine (embedded Dolt, the in-process store the repo ships) rather than the
// call-recording mock in import_chunking_test.go: they assert that bounded
// transactions leave a durable committed prefix after a crash, that readiness
// edges are visible at every freeze point, and that concurrent row updates do
// not drop the import's deferred edges — invariants the mock cannot answer
// because it never actually stores rows or computes readiness.
//
// They require cgo and are skipped unless BEADS_TEST_EMBEDDED_DOLT=1, following
// the cmd/bd embedded-test convention (see import_embedded_test.go,
// create_embedded_test.go). The shared helpers (setImportChunkSize,
// recordImportPauses, setImportProgressBuffer, chunkTestIssues) live in
// import_chunking_test.go and are reused here.
//
// A prior revision of this suite included TestImportChunkedConcurrentAvailability,
// which opened two connection pools on one database file to probe SQLite's
// two-connection file-lock fairness. That test targeted an
// internal/storage/sqlite backend that is not present on main; embedded Dolt
// serializes writes in-process and refcount-aliases handles per data directory,
// so the two-pool premise does not hold and the probe cannot be ported
// faithfully. The inter-chunk pause it validated end-to-end remains covered by
// TestImportChunkedPausesBetweenChunkTransactions, which asserts a pause is
// issued between every adjacent import transaction. Re-add an availability probe
// when a backend with the relevant concurrency semantics lands.

func requireEmbeddedDolt(t *testing.T) {
	t.Helper()
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt chunked-import tests")
	}
}

// failNthCreateStore wraps a real store and fails the Nth batch-create call,
// simulating a crash mid-import against a real engine.
type failNthCreateStore struct {
	storage.DoltStorage
	calls      int
	failOnCall int
}

func (f *failNthCreateStore) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	f.calls++
	if f.calls == f.failOnCall {
		return errors.New("simulated crash")
	}
	return f.DoltStorage.CreateIssuesWithFullOptions(ctx, issues, actor, opts)
}

// provisionChunkStore opens a fresh embedded Dolt store in a temp data dir. The
// store auto-provisions its schema, so no subprocess `bd init` is needed; each
// call uses a distinct t.TempDir() so the per-dir refcount cache never aliases
// two stores together.
func provisionChunkStore(t *testing.T) storage.DoltStorage {
	t.Helper()
	ctx := context.Background()
	store, err := embeddeddolt.Open(ctx, t.TempDir(), "beads", "main")
	if err != nil {
		t.Fatalf("provision embedded dolt store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("seed issue_prefix: %v", err)
	}
	return store
}

// End-to-end against the real engine: a mid-import failure leaves a committed,
// queryable prefix, and re-running the same import converges — every row
// present, cross-chunk dependencies wired, no duplicated events.
func TestImportChunkedRealStoreResumeConverges(t *testing.T) {
	requireEmbeddedDolt(t)
	setImportChunkSize(t, 5)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	ctx := context.Background()
	store := provisionChunkStore(t)

	makeIssues := func() []*types.Issue {
		issues := chunkTestIssues(12)
		// Blocking forward reference in file order: 1 -> 11.
		issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[10].ID, Type: types.DepBlocks}}
		return issues
	}

	failing := &failNthCreateStore{DoltStorage: store, failOnCall: 2}
	_, err := importIssuesCore(ctx, "", failing, makeIssues(), ImportOptions{SkipPrefixValidation: true})
	if err == nil {
		t.Fatalf("importIssuesCore succeeded, want simulated crash at chunk 2")
	}

	// Some rows are committed and queryable, others absent: the transactions
	// really are bounded.
	committed, absent := 0, 0
	for i := 1; i <= 12; i++ {
		if _, err := store.GetIssue(ctx, fmt.Sprintf("bd-chunk%02d", i)); err == nil {
			committed++
		} else {
			absent++
		}
	}
	if committed != 5 || absent != 7 {
		t.Fatalf("after crash at chunk 2: committed=%d absent=%d, want exactly the first chunk (5) durable", committed, absent)
	}

	// Re-run the identical import: it must converge.
	result, err := importIssuesCore(ctx, "", store, makeIssues(), ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("re-run importIssuesCore: %v", err)
	}
	if result.Created != 12 {
		t.Fatalf("re-run Created = %d, want all 12 rows accounted for", result.Created)
	}
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("bd-chunk%02d", i)
		if _, err := store.GetIssue(ctx, id); err != nil {
			t.Fatalf("issue %s missing after re-run: %v", id, err)
		}
	}
	deps, err := store.GetDependencies(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "bd-chunk11" {
		t.Fatalf("cross-chunk dependency not wired, got %#v", deps)
	}
	// Rows upserted twice must not accrue duplicate created events.
	for _, id := range []string{"bd-chunk01", "bd-chunk02"} {
		events, err := store.GetEvents(ctx, id, 50)
		if err != nil {
			t.Fatalf("GetEvents(%s): %v", id, err)
		}
		created := 0
		for _, e := range events {
			if e.EventType == types.EventCreated {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("%s has %d created events after resume, want 1", id, created)
		}
	}
}

// No freeze point of a chunked import may expose a blocked bead as ready:
// whenever an imported row is visible to a concurrent reader, the blocking
// edges its import file declares must be visible too. This is the red-team
// ready-window finding: the previous two-phase layout committed every row
// dep-less first, so `bd ready` mid-import offered blocked work for dispatch.
func TestImportChunkedNoReadyWindowAtAnyFreezePoint(t *testing.T) {
	requireEmbeddedDolt(t)
	setImportChunkSize(t, 5)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	ctx := context.Background()

	makeIssues := func() []*types.Issue {
		issues := chunkTestIssues(12)
		// bd-chunk01 is BLOCKED by bd-chunk11 (forward ref in file order).
		issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[10].ID, Type: types.DepBlocks}}
		return issues
	}

	// Learn how many transactions a full import issues.
	full := provisionChunkStore(t)
	counting := &failNthCreateStore{DoltStorage: full}
	if _, err := importIssuesCore(ctx, "", counting, makeIssues(), ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("full import: %v", err)
	}
	totalCalls := counting.calls
	if totalCalls < 2 {
		t.Fatalf("totalCalls = %d, want a chunked import", totalCalls)
	}
	ready, err := full.GetReadyWork(ctx, types.WorkFilter{Limit: 50})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	for _, issue := range ready {
		if issue.ID == "bd-chunk01" {
			t.Fatalf("bd-chunk01 ready after a full import; it is blocked by open bd-chunk11")
		}
	}

	// Freeze the store at every possible transaction boundary and check the
	// invariant: row visible => blocking edge visible => not ready.
	for failAt := 1; failAt <= totalCalls; failAt++ {
		store := provisionChunkStore(t)
		failing := &failNthCreateStore{DoltStorage: store, failOnCall: failAt}
		if _, err := importIssuesCore(ctx, "", failing, makeIssues(), ImportOptions{SkipPrefixValidation: true}); err == nil {
			t.Fatalf("freeze point %d: import unexpectedly succeeded", failAt)
		}
		if _, err := store.GetIssue(ctx, "bd-chunk01"); err != nil {
			continue // row not committed yet: nothing to observe
		}
		deps, err := store.GetDependencies(ctx, "bd-chunk01")
		if err != nil {
			t.Fatalf("freeze point %d: GetDependencies: %v", failAt, err)
		}
		if len(deps) == 0 {
			t.Fatalf("freeze point %d: bd-chunk01 committed without its blocking edge — ready window", failAt)
		}
		ready, err := store.GetReadyWork(ctx, types.WorkFilter{Limit: 50})
		if err != nil {
			t.Fatalf("freeze point %d: GetReadyWork: %v", failAt, err)
		}
		for _, issue := range ready {
			if issue.ID == "bd-chunk01" {
				t.Fatalf("freeze point %d: blocked bead bd-chunk01 offered as ready work mid-import", failAt)
			}
		}
	}
}

// A readiness edge that points INTO a tolerated readiness cycle must still ride
// inline with its row across every import freeze point: the cycle-fallback
// ordering must not place a valid dependent of a cycle in an earlier chunk than
// the cycle member it blocks on. The import only meets a blocking cycle in
// corrupted or legacy JSONL, which it tolerates (SkipDependencyValidationErrors),
// but the no-ready-window invariant must hold there too — otherwise the
// dependent commits ready-without-blocker for the rest of the import. Regression
// for the attempt-1 review finding.
func TestImportChunkedNoReadyWindowWithReadinessEdgeIntoCycle(t *testing.T) {
	requireEmbeddedDolt(t)
	setImportChunkSize(t, 5)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	ctx := context.Background()

	makeIssues := func() []*types.Issue {
		issues := chunkTestIssues(12)
		// bd-chunk11 <-> bd-chunk12 form a tolerated blocking cycle. bd-chunk01
		// validly blocks on bd-chunk11; in file order it precedes the cycle, so
		// a plain file-order fallback would chunk it ahead of bd-chunk11 and
		// defer the live edge.
		issues[10].Dependencies = []*types.Dependency{{IssueID: issues[10].ID, DependsOnID: issues[11].ID, Type: types.DepBlocks}}
		issues[11].Dependencies = []*types.Dependency{{IssueID: issues[11].ID, DependsOnID: issues[10].ID, Type: types.DepBlocks}}
		issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[10].ID, Type: types.DepBlocks}}
		return issues
	}

	// A full import must tolerate the cycle, leave bd-chunk01 blocked by open
	// bd-chunk11, and report how many transactions the import issues.
	full := provisionChunkStore(t)
	counting := &failNthCreateStore{DoltStorage: full}
	if _, err := importIssuesCore(ctx, "", counting, makeIssues(), ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("full import (tolerated cycle): %v", err)
	}
	totalCalls := counting.calls
	if totalCalls < 2 {
		t.Fatalf("totalCalls = %d, want a chunked import", totalCalls)
	}
	deps, err := full.GetDependencies(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("GetDependencies(bd-chunk01): %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "bd-chunk11" {
		t.Fatalf("bd-chunk01 must be blocked by bd-chunk11 after import, got %#v", deps)
	}
	ready, err := full.GetReadyWork(ctx, types.WorkFilter{Limit: 50})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	for _, issue := range ready {
		if issue.ID == "bd-chunk01" {
			t.Fatalf("bd-chunk01 ready after a full import; it is blocked by open bd-chunk11 (cycle member)")
		}
	}

	// Freeze at every transaction boundary: whenever bd-chunk01 is visible, its
	// blocking edge into the cycle must be visible too, so it is never offered
	// as ready mid-import.
	for failAt := 1; failAt <= totalCalls; failAt++ {
		store := provisionChunkStore(t)
		failing := &failNthCreateStore{DoltStorage: store, failOnCall: failAt}
		if _, err := importIssuesCore(ctx, "", failing, makeIssues(), ImportOptions{SkipPrefixValidation: true}); err == nil {
			t.Fatalf("freeze point %d: import unexpectedly succeeded", failAt)
		}
		if _, err := store.GetIssue(ctx, "bd-chunk01"); err != nil {
			continue // row not committed yet: nothing to observe
		}
		deps, err := store.GetDependencies(ctx, "bd-chunk01")
		if err != nil {
			t.Fatalf("freeze point %d: GetDependencies: %v", failAt, err)
		}
		if len(deps) == 0 {
			t.Fatalf("freeze point %d: bd-chunk01 committed without its blocking edge into the cycle — ready window", failAt)
		}
		ready, err := store.GetReadyWork(ctx, types.WorkFilter{Limit: 50})
		if err != nil {
			t.Fatalf("freeze point %d: GetReadyWork: %v", failAt, err)
		}
		for _, issue := range ready {
			if issue.ID == "bd-chunk01" {
				t.Fatalf("freeze point %d: blocked bead bd-chunk01 offered as ready work mid-import", failAt)
			}
		}
	}
}

// A waits-for waiter's is_blocked state is gated on its spawner having an active
// parent-child child, and the per-chunk is_blocked recompute only re-evaluates
// the rows in its own transaction. So when the waiter and its spawner's active
// child straddle a chunk boundary in file order (waiter first, child later), a
// naive ordering leaves the waiter's chunk computing is_blocked=0 against a
// still-childless spawner, and the later chunk that imports the child never
// re-blocks the waiter — it sits ready for the rest of the import and after it.
// Ordering the spawner inline with the waiter is not enough; every in-batch
// child of a waited spawner must be emitted no later than the waiter. Regression
// for the attempt-2 review blocker.
func TestImportChunkedWaitsForChildImportedLaterStaysBlocked(t *testing.T) {
	requireEmbeddedDolt(t)
	setImportChunkSize(t, 5)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	ctx := context.Background()

	makeIssues := func() []*types.Issue {
		// File order: waiter, spawner, three fillers, active child. At chunk
		// size 5 the waiter (chunk 0) and the child (chunk 1) straddle the
		// boundary unless the import reorders the child ahead of the waiter.
		// bd-chunk01 waits for bd-chunk02; bd-chunk06 is an open parent-child
		// child of bd-chunk02.
		issues := chunkTestIssues(6)
		issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[1].ID, Type: types.DepWaitsFor}}
		issues[5].Dependencies = []*types.Dependency{{IssueID: issues[5].ID, DependsOnID: issues[1].ID, Type: types.DepParentChild}}
		return issues
	}

	full := provisionChunkStore(t)
	counting := &failNthCreateStore{DoltStorage: full}
	if _, err := importIssuesCore(ctx, "", counting, makeIssues(), ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("full import (waits-for child across chunks): %v", err)
	}
	if counting.calls < 2 {
		t.Fatalf("calls = %d, want a chunked (>=2 transaction) import so the waiter and child can straddle a boundary", counting.calls)
	}

	// The waits-for edge must be wired inline so the gate can see it.
	waiterDeps, err := full.GetDependencies(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("GetDependencies(bd-chunk01): %v", err)
	}
	if len(waiterDeps) != 1 || waiterDeps[0].ID != "bd-chunk02" {
		t.Fatalf("bd-chunk01 must wait for bd-chunk02 after import, got %#v", waiterDeps)
	}

	// The waiter must NOT be ready: its spawner bd-chunk02 has an open child
	// bd-chunk06, so the waits-for gate keeps bd-chunk01 blocked.
	ready, err := full.GetReadyWork(ctx, types.WorkFilter{Limit: 50})
	if err != nil {
		t.Fatalf("GetReadyWork: %v", err)
	}
	for _, issue := range ready {
		if issue.ID == "bd-chunk01" {
			t.Fatalf("bd-chunk01 ready after a full import; it waits for bd-chunk02 whose active child bd-chunk06 was imported in a later chunk")
		}
	}
}

// raceInjectingStore simulates a concurrent writer landing between import
// transactions: before the Nth CreateIssuesWithFullOptions call it updates an
// issue directly, bumping its updated_at — exactly what a concurrent
// `bd update` or a gc claim does in the windows the chunking opens up.
type raceInjectingStore struct {
	storage.DoltStorage
	calls        int
	injectBefore int
	injectID     string
	injected     bool
	t            *testing.T
}

func (r *raceInjectingStore) CreateIssuesWithFullOptions(ctx context.Context, issues []*types.Issue, actor string, opts storage.BatchCreateOptions) error {
	r.calls++
	if r.calls == r.injectBefore && !r.injected {
		r.injected = true
		if err := r.DoltStorage.UpdateIssue(ctx, r.injectID, map[string]interface{}{"assignee": "concurrent-racer"}, "racer"); err != nil {
			r.t.Fatalf("inject concurrent update: %v", err)
		}
	}
	return r.DoltStorage.CreateIssuesWithFullOptions(ctx, issues, actor, opts)
}

// A concurrent update to a row between its commit and the deferred-dependency
// pass must not drop the import's edges for it. This is the red-team #1
// finding: the old dependency pass resubmitted the full row with
// RejectStaleUpserts, so the now-newer stored row stale-rejected the resubmit
// and the engine dropped its deps — silently, permanently (re-runs pre-filter
// the row away). The dependency pass must wire edges for every row phase 1
// accepted, regardless of later row-level updates, and must not clobber the
// concurrent write.
func TestImportChunkedConcurrentUpdateKeepsDeferredDeps(t *testing.T) {
	requireEmbeddedDolt(t)
	setImportChunkSize(t, 5)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	ctx := context.Background()
	store := provisionChunkStore(t)

	makeIssues := func() []*types.Issue {
		issues := chunkTestIssues(12)
		// A non-blocking forward edge stays deferred (related edges do not
		// affect readiness, so the ordering pass leaves file order alone):
		// this is the shape that still crosses the inter-transaction window.
		issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[10].ID, Type: types.DepRelated}}
		return issues
	}

	// 12 issues at chunk size 5 = 3 row chunks (calls 1-3); the deferred
	// dependency pass is call 4. Inject the concurrent update just before it.
	racing := &raceInjectingStore{DoltStorage: store, injectBefore: 4, injectID: "bd-chunk01", t: t}
	result, err := importIssuesCore(ctx, "", racing, makeIssues(), ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if !racing.injected {
		t.Fatalf("race was never injected (calls=%d); test harness broken", racing.calls)
	}

	// The concurrent write survives: the dependency pass must not rewrite rows.
	got, err := store.GetIssue(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("bd-chunk01 not committed: %v", err)
	}
	if got.Assignee != "concurrent-racer" {
		t.Fatalf("assignee = %q, want the concurrent update preserved", got.Assignee)
	}

	// The import's edge survives: phase 1 accepted the row, so its deferred
	// deps must be wired even though the stored row is now newer.
	deps, err := store.GetDependencies(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "bd-chunk11" {
		t.Fatalf("deferred edge dropped after concurrent update: deps = %#v", deps)
	}

	// And the reporting must not misclassify the row: its row IS committed.
	for _, id := range result.StaleSkippedIDs {
		if id == "bd-chunk01" {
			t.Fatalf("bd-chunk01 reported stale-skipped, but its row was imported in phase 1")
		}
	}
	if result.Created != 12 {
		t.Fatalf("Created = %d, want 12", result.Created)
	}
}

// The red-team #1 repro shape verbatim, for blocking edges: a claim-like
// update racing the import must leave the blocking edge intact. With ordered
// inline wiring the row and its edge commit atomically, so no interleaving of
// row-level updates can separate them.
func TestImportChunkedConcurrentUpdateKeepsBlockingDeps(t *testing.T) {
	requireEmbeddedDolt(t)
	setImportChunkSize(t, 5)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	ctx := context.Background()
	store := provisionChunkStore(t)

	makeIssues := func() []*types.Issue {
		issues := chunkTestIssues(12)
		issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[10].ID, Type: types.DepBlocks}}
		return issues
	}

	// The blocker's row (bd-chunk11) lands before bd-chunk01's chunk. Update
	// it between chunks — the interleaving that used to poison the dep pass.
	racing := &raceInjectingStore{DoltStorage: store, injectBefore: 3, injectID: "bd-chunk11", t: t}
	if _, err := importIssuesCore(ctx, "", racing, makeIssues(), ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if !racing.injected {
		t.Fatalf("race was never injected (calls=%d); test harness broken", racing.calls)
	}
	deps, err := store.GetDependencies(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	if len(deps) != 1 || deps[0].ID != "bd-chunk11" {
		t.Fatalf("blocking edge missing after concurrent update: deps = %#v", deps)
	}

	// Re-running the identical import stays convergent and keeps the edge.
	if _, err := importIssuesCore(ctx, "", store, makeIssues(), ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("re-run importIssuesCore: %v", err)
	}
	deps, err = store.GetDependencies(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("GetDependencies after re-run: %v", err)
	}
	if len(deps) != 1 {
		t.Fatalf("blocking edge lost on re-run: deps = %#v", deps)
	}
}

// The other half of the stale-snapshot invariant (bd-578h9.8): when the row
// write itself was stale-rejected in phase 1 (a local update landed between
// the pre-filter read and the row's chunk), the snapshot's deferred deps must
// stay out too. This is why the dependency pass cannot simply run everything
// with ConflictSkip: it must be restricted to rows phase 1 accepted.
func TestImportChunkedStaleRejectedRowKeepsDeferredDepsOut(t *testing.T) {
	requireEmbeddedDolt(t)
	setImportChunkSize(t, 5)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	ctx := context.Background()
	store := provisionChunkStore(t)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// Seed bd-chunk01 with an OLDER snapshot so the incoming row passes the
	// pre-filter (incoming strictly newer than local at read time).
	seed := &types.Issue{ID: "bd-chunk01", Title: "seeded", UpdatedAt: base.Add(-time.Hour)}
	seed.SetDefaults()
	if _, err := importIssuesCore(ctx, "", store, []*types.Issue{seed}, ImportOptions{SkipPrefixValidation: true}); err != nil {
		t.Fatalf("seed import: %v", err)
	}

	issues := chunkTestIssues(12)
	issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: issues[10].ID, Type: types.DepRelated}}

	// Bump bd-chunk01 to "now" after the pre-filter read but before its row
	// chunk: the in-transaction stale guard rejects the row write, so the
	// whole snapshot — deferred deps included — must stay out.
	racing := &raceInjectingStore{DoltStorage: store, injectBefore: 1, injectID: "bd-chunk01", t: t}
	result, err := importIssuesCore(ctx, "", racing, issues, ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	if !racing.injected {
		t.Fatalf("race was never injected; test harness broken")
	}

	deps, err := store.GetDependencies(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("GetDependencies: %v", err)
	}
	if len(deps) != 0 {
		t.Fatalf("stale-rejected row's deferred deps were wired anyway: %#v (violates bd-578h9.8)", deps)
	}
	found := false
	for _, id := range result.StaleSkippedIDs {
		if id == "bd-chunk01" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bd-chunk01 not reported stale-skipped; StaleSkippedIDs = %v", result.StaleSkippedIDs)
	}
}

// A chunked import mixing regular issues and wisps must apply the cross-bucket
// dependency policy over the FULL logical set, not per chunk. Dependencies are
// stored in per-bucket tables (dependencies vs wisp_dependencies), so a batch
// cannot create a regular<->wisp edge atomically; the engine's per-chunk filter
// only sees one chunk and short-circuits on an all-one-bucket chunk, so it can
// no longer catch a cross-bucket edge whose endpoints land in different chunks.
// The up-front FilterCreateIssuesMixedBucketDependencies pass exists for exactly
// that case. This exercises it end to end against the real engine: a regular ->
// wisp edge straddling a chunk boundary must be skip-reported while both rows
// still import, and a same-bucket wisp -> wisp edge straddling the same boundary
// must survive the double filtering and be wired. Regression for the attempt-1
// test-evidence finding (the prior chunked tests seeded only regular issues).
func TestImportChunkedMixedRegularWispCrossBucketFiltering(t *testing.T) {
	requireEmbeddedDolt(t)
	setImportChunkSize(t, 5)
	recordImportPauses(t)
	setImportProgressBuffer(t)
	ctx := context.Background()
	store := provisionChunkStore(t)

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	mk := func(id string, wisp bool) *types.Issue {
		iss := &types.Issue{ID: id, Title: id, UpdatedAt: base, Ephemeral: wisp}
		iss.SetDefaults()
		return iss
	}
	// 12 rows across three chunks (5/5/2) at chunk size 5. Every edge below is
	// `related` (non-readiness), so orderImportIssuesForChunking preserves file
	// order and the two forward edges genuinely straddle the chunk-0/chunk-1
	// boundary. Chunks 0 and 1 are each mixed (regular + wisp), so the engine's
	// per-chunk mixed-bucket filter actually runs instead of short-circuiting on
	// an all-one-bucket chunk.
	makeIssues := func() []*types.Issue {
		issues := []*types.Issue{
			mk("bd-chunk01", false), // chunk 0: regular source of the cross-bucket edge
			mk("bd-wisp02", true),   // chunk 0: wisp source of the same-bucket edge
			mk("bd-chunk03", false), // chunk 0
			mk("bd-chunk04", false), // chunk 0
			mk("bd-chunk05", false), // chunk 0
			mk("bd-chunk06", false), // chunk 1
			mk("bd-wisp07", true),   // chunk 1: shared target wisp
			mk("bd-chunk08", false), // chunk 1
			mk("bd-chunk09", false), // chunk 1
			mk("bd-chunk10", false), // chunk 1
			mk("bd-chunk11", false), // chunk 2
			mk("bd-chunk12", false), // chunk 2
		}
		// Cross-bucket regular -> wisp, endpoints in different chunks: only the
		// up-front full-set filter can see both ends, so it must be skip-reported.
		issues[0].Dependencies = []*types.Dependency{{IssueID: issues[0].ID, DependsOnID: "bd-wisp07", Type: types.DepRelated}}
		// Same-bucket wisp -> wisp, endpoints in different chunks: must survive the
		// double filtering and be wired into wisp_dependencies.
		issues[1].Dependencies = []*types.Dependency{{IssueID: issues[1].ID, DependsOnID: "bd-wisp07", Type: types.DepRelated}}
		return issues
	}

	counting := &failNthCreateStore{DoltStorage: store}
	result, err := importIssuesCore(ctx, "", counting, makeIssues(), ImportOptions{SkipPrefixValidation: true})
	if err != nil {
		t.Fatalf("importIssuesCore: %v", err)
	}
	// 3 row chunks (5/5/2) plus one deferred-dependency pass for the surviving
	// same-bucket cross-chunk edge — so the endpoints really did straddle a
	// boundary and the deferred pass really ran.
	if counting.calls != 4 {
		t.Fatalf("calls = %d, want 3 row chunks + 1 deferred-dependency pass", counting.calls)
	}
	if result.Created != 12 {
		t.Fatalf("Created = %d, want 12 (every row imports even when a cross-bucket edge is dropped)", result.Created)
	}

	// The cross-bucket edge is dropped and reported exactly once; the same-bucket
	// edge is never reported.
	if len(result.SkippedDependencies) != 1 ||
		!strings.Contains(result.SkippedDependencies[0], "bd-chunk01 -> bd-wisp07") ||
		!strings.Contains(result.SkippedDependencies[0], "cross-bucket") {
		t.Fatalf("SkippedDependencies = %#v, want exactly the cross-bucket bd-chunk01 -> bd-wisp07 edge", result.SkippedDependencies)
	}

	// Both endpoints of the dropped cross-bucket edge still imported (GetIssue
	// finds the wisp in the wisps table too).
	if _, err := store.GetIssue(ctx, "bd-chunk01"); err != nil {
		t.Fatalf("regular source bd-chunk01 not imported: %v", err)
	}
	if _, err := store.GetIssue(ctx, "bd-wisp07"); err != nil {
		t.Fatalf("wisp target bd-wisp07 not imported: %v", err)
	}

	// The cross-bucket edge left no dependency on the regular source.
	regularDeps, err := store.GetDependencies(ctx, "bd-chunk01")
	if err != nil {
		t.Fatalf("GetDependencies(bd-chunk01): %v", err)
	}
	if len(regularDeps) != 0 {
		t.Fatalf("bd-chunk01 deps = %#v, want none (cross-bucket edge dropped)", regularDeps)
	}

	// The same-bucket wisp -> wisp edge survived the chunk boundary and is wired
	// into wisp_dependencies (GetDependencies routes a wisp source there).
	wispDeps, err := store.GetDependencies(ctx, "bd-wisp02")
	if err != nil {
		t.Fatalf("GetDependencies(bd-wisp02): %v", err)
	}
	if len(wispDeps) != 1 || wispDeps[0].ID != "bd-wisp07" {
		t.Fatalf("bd-wisp02 deps = %#v, want the same-bucket wisp->wisp edge to bd-wisp07 preserved", wispDeps)
	}
}
