// Package conformance provides backend-agnostic tests for Storage implementations.
//
// Usage from a backend test file:
//
//	func TestConformance(t *testing.T) {
//	    conformance.RunAll(t, func(t *testing.T) storage.DoltStorage {
//	        return newTestStore(t)
//	    })
//	}
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

var (
	ctxIface = reflect.TypeOf((*context.Context)(nil)).Elem()
	errIface = reflect.TypeOf((*error)(nil)).Elem()
)

// RunUnsupportedContract is the BEHAVIORAL half of a backend's capability contract: it
// calls every method the backend lists as legitimately unsupported and asserts each
// returns a typed storage.ErrUnsupported. completeness_test.go asserts the STRUCTURAL
// half (the generated shell equals this same allowlist); together they close the loop so
// an unsupported method can neither silently resolve to something else (structural) nor
// return the wrong error/panic (behavioral). Driven by the allowlist itself, so it stays
// exhaustive and shrinks automatically as methods graduate off the list.
//
// No live database: the generated stubs ignore their receiver and arguments, so a
// zero-value store answers them. Pass the backend's concrete store value (e.g. &Store{}).
func RunUnsupportedContract(t *testing.T, store any, unsupported map[string]string) {
	t.Helper()
	rv := reflect.ValueOf(store)
	names := make([]string, 0, len(unsupported))
	for name := range unsupported {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			m := rv.MethodByName(name)
			if !m.IsValid() {
				t.Fatalf("%q is on the unsupported allowlist but is not a method on the store (shell drift)", name)
			}
			mt := m.Type()
			in := make([]reflect.Value, mt.NumIn())
			for i := 0; i < mt.NumIn(); i++ {
				if mt.In(i) == ctxIface {
					in[i] = reflect.ValueOf(ctx())
				} else {
					in[i] = reflect.Zero(mt.In(i))
				}
			}
			var out []reflect.Value
			if mt.IsVariadic() {
				out = m.CallSlice(in)
			} else {
				out = m.Call(in)
			}
			var err error
			hasErr := false
			for _, o := range out {
				if o.Type() == errIface {
					hasErr = true
					if !o.IsNil() {
						err = o.Interface().(error)
					}
				}
			}
			if !hasErr {
				t.Fatalf("%q has no error return; cannot assert the unsupported contract", name)
			}
			var unsup *storage.ErrUnsupported
			if !errors.As(err, &unsup) {
				t.Fatalf("%q returned %v, want *storage.ErrUnsupported", name, err)
			}
			if unsup.Op != name {
				t.Errorf("%q returned unsupported error for Op %q — wrong method wired?", name, unsup.Op)
			}
		})
	}
}

// Factory creates a fresh, empty store for each test.
//
// The returned store must be initialized and ready to accept writes: in
// particular the backend's required config (e.g. issue_prefix) must already be
// set, exactly as `bd init` would leave it. The suite only exercises behavior;
// it does not perform backend initialization.
type Factory func(t *testing.T) storage.DoltStorage

// RunAll runs the full conformance suite against the given factory.
func RunAll(t *testing.T, factory Factory) {
	t.Helper()

	// Issue CRUD
	t.Run("CreateAndGet", func(t *testing.T) { testCreateAndGet(t, factory) })
	t.Run("CreateDuplicate", func(t *testing.T) { testCreateDuplicate(t, factory) })
	t.Run("GetNotFound", func(t *testing.T) { testGetNotFound(t, factory) })
	t.Run("GetByExternalRef", func(t *testing.T) { testGetByExternalRef(t, factory) })
	t.Run("GetByIDs", func(t *testing.T) { testGetByIDs(t, factory) })
	t.Run("Update", func(t *testing.T) { testUpdate(t, factory) })
	t.Run("UpdatePreservesCreatedAt", func(t *testing.T) { testUpdatePreservesCreatedAt(t, factory) })
	t.Run("UpdateNotFound", func(t *testing.T) { testUpdateNotFound(t, factory) })
	t.Run("UpdateIssueType", func(t *testing.T) { testUpdateIssueType(t, factory) })
	t.Run("CloseAndReopen", func(t *testing.T) { testCloseAndReopen(t, factory) })
	t.Run("Delete", func(t *testing.T) { testDelete(t, factory) })
	t.Run("DeleteNotFound", func(t *testing.T) { testDeleteNotFound(t, factory) })

	// Search and filter
	t.Run("SearchNoFilter", func(t *testing.T) { testSearchNoFilter(t, factory) })
	t.Run("SearchTextQuery", func(t *testing.T) { testSearchTextQuery(t, factory) })
	t.Run("SearchStatusFilter", func(t *testing.T) { testSearchStatusFilter(t, factory) })
	t.Run("SearchPriorityFilter", func(t *testing.T) { testSearchPriorityFilter(t, factory) })
	t.Run("SearchLimit", func(t *testing.T) { testSearchLimit(t, factory) })
	t.Run("CountIssues", func(t *testing.T) { testCountIssues(t, factory) })
	t.Run("CountByGroup", func(t *testing.T) { testCountByGroup(t, factory) })
	t.Run("CountByGroupIsBlockedFilter", func(t *testing.T) { testCountByGroupIsBlockedFilter(t, factory) })

	// Dependencies
	t.Run("AddAndGetDeps", func(t *testing.T) { testAddAndGetDeps(t, factory) })
	t.Run("RemoveDep", func(t *testing.T) { testRemoveDep(t, factory) })
	t.Run("DepCounts", func(t *testing.T) { testDepCounts(t, factory) })

	// Ready/Blocked
	t.Run("ReadyNoDeps", func(t *testing.T) { testReadyNoDeps(t, factory) })
	t.Run("ReadyBlockedByOpenDep", func(t *testing.T) { testReadyBlockedByOpenDep(t, factory) })
	t.Run("ReadyUnblockedByClose", func(t *testing.T) { testReadyUnblockedByClose(t, factory) })
	t.Run("BlockedIssues", func(t *testing.T) { testBlockedIssues(t, factory) })
	t.Run("EpicsEligibleForClosure", func(t *testing.T) { testEpicsEligible(t, factory) })
	t.Run("ReadyCountsPageEquivalence", func(t *testing.T) { testReadyCountsPageEquivalence(t, factory) })
	t.Run("ReadyCountsWithWisps", func(t *testing.T) { testReadyCountsWithWisps(t, factory) })
	t.Run("ReadyCountsPageChunking", func(t *testing.T) { testReadyCountsPageChunking(t, factory) })

	// Claim / lease (dead-worker recovery)
	t.Run("Claim", func(t *testing.T) { testClaim(t, factory) })
	t.Run("ClaimIdempotent", func(t *testing.T) { testClaimIdempotent(t, factory) })
	t.Run("ClaimAlreadyClaimed", func(t *testing.T) { testClaimAlreadyClaimed(t, factory) })
	t.Run("ClaimOpenForeignAssignee", func(t *testing.T) { testClaimOpenForeignAssignee(t, factory) })
	t.Run("ClaimNotClaimable", func(t *testing.T) { testClaimNotClaimable(t, factory) })
	t.Run("ClaimReadyIssue", func(t *testing.T) { testClaimReadyIssue(t, factory) })
	t.Run("ClaimReadyIssueLabelFilters", func(t *testing.T) { testClaimReadyIssueLabelFilters(t, factory) })
	t.Run("HeartbeatRenewsLease", func(t *testing.T) { testHeartbeatRenewsLease(t, factory) })
	t.Run("HeartbeatWisp", func(t *testing.T) { testHeartbeatWisp(t, factory) })
	t.Run("ReclaimExpiredLease", func(t *testing.T) { testReclaimExpiredLease(t, factory) })
	t.Run("ReclaimSkipsFreshLease", func(t *testing.T) { testReclaimSkipsFreshLease(t, factory) })
	t.Run("UnclaimIfAssigneeMatch", func(t *testing.T) { testUnclaimIfAssigneeMatch(t, factory) })
	t.Run("UnclaimIfAssigneeStale", func(t *testing.T) { testUnclaimIfAssigneeStale(t, factory) })

	// Labels
	t.Run("Labels", func(t *testing.T) { testLabels(t, factory) })
	t.Run("LabelIdempotent", func(t *testing.T) { testLabelIdempotent(t, factory) })
	t.Run("GetIssuesByLabel", func(t *testing.T) { testGetIssuesByLabel(t, factory) })

	// Comments
	t.Run("Comments", func(t *testing.T) { testComments(t, factory) })
	t.Run("CommentCount", func(t *testing.T) { testCommentCount(t, factory) })

	// Config
	t.Run("Config", func(t *testing.T) { testConfig(t, factory) })
	t.Run("LocalMetadata", func(t *testing.T) { testLocalMetadata(t, factory) })

	// Slots
	t.Run("MetadataSlots", func(t *testing.T) { testMetadataSlots(t, factory) })

	// Statistics
	t.Run("Statistics", func(t *testing.T) { testStatistics(t, factory) })

	// Stale
	t.Run("StaleIssues", func(t *testing.T) { testStaleIssues(t, factory) })

	// Portable non-VC methods (molecule/repo-mtime/streams/counts/comment/rekey/batch)
	t.Run("Portable", func(t *testing.T) { RunPortableMethods(t, factory) })

	// Audit — exhaustive strange-behavior cases derived from the Dolt reference impl.
	t.Run("Audit", func(t *testing.T) { RunAudit(t, factory) })

	// Iterators
	t.Run("IterIssues", func(t *testing.T) { testIterIssues(t, factory) })
	t.Run("IterComments", func(t *testing.T) { testIterComments(t, factory) })

	// Transaction
	t.Run("Transaction", func(t *testing.T) { testTransaction(t, factory) })
	t.Run("TransactionSnapshotReads", func(t *testing.T) { testTransactionSnapshotReads(t, factory) })
	t.Run("TransactionReadYourWrites", func(t *testing.T) { testTransactionReadYourWrites(t, factory) })
}

// RunDeferredReads runs the subset of the suite covering SQLite's shared
// non-version-control reads: statistics, external-ref lookup, and staleness. RunAll
// remains the full Dolt reference; SQLite runs this focused gate for methods supplied
// by issueops while its Dolt-only methods fail loudly as unsupported.
func RunDeferredReads(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("Statistics", func(t *testing.T) { testStatistics(t, factory) })
	t.Run("GetByExternalRef", func(t *testing.T) { testGetByExternalRef(t, factory) })
	t.Run("StaleIssues", func(t *testing.T) { testStaleIssues(t, factory) })
}

// --- helpers ---

func ctx() context.Context { return context.Background() }

func seedStore(t *testing.T, s storage.DoltStorage) {
	t.Helper()
	c := ctx()
	must(t, s.SetConfig(c, "issue_prefix", "test"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-1", Title: "Alpha", Priority: 0, IssueType: "bug", Status: types.StatusOpen}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-2", Title: "Beta", Priority: 1, IssueType: "task", Assignee: "alice", Status: types.StatusOpen}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-3", Title: "Gamma", Priority: 2, IssueType: "feature", Status: types.StatusInProgress}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "test-4", Title: "Delta", Priority: 1, IssueType: "bug", Status: types.StatusClosed}), "actor"))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// withDefaults fills the Status and IssueType that the storage contract
// requires on every issue (the storage layer validates them and does not
// default them; bd's CLI layer is what normally supplies open/task). Tests
// that do not care about these fields can leave them unset and stay terse;
// explicit values set by a test are preserved.
func withDefaults(i *types.Issue) *types.Issue {
	if i.Status == "" {
		i.Status = types.StatusOpen
	}
	if i.IssueType == "" {
		i.IssueType = types.TypeTask
	}
	return i
}

func issueIDs(issues []*types.Issue) []string {
	ids := make([]string, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
	}
	sort.Strings(ids)
	return ids
}

// orderedIDs is issueIDs without the sort — for asserting a contractual result
// order (e.g. GetStaleIssues' updated_at ASC) rather than set membership.
func orderedIDs(issues []*types.Issue) []string {
	ids := make([]string, len(issues))
	for i, iss := range issues {
		ids[i] = iss.ID
	}
	return ids
}

// --- Issue CRUD ---

func testCreateAndGet(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "c-1", Title: "Test", Priority: 2}), "actor"))
	got, err := s.GetIssue(ctx(), "c-1")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "Test" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Status != types.StatusOpen {
		t.Errorf("Status = %q, want open", got.Status)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

func testCreateDuplicate(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d-1", Title: "First"}), "actor"))

	// Creating an issue with an existing ID must not silently fork into a
	// second, divergent row. Backends differ on how they reconcile the second
	// write (beads' storage layer upserts; another backend might error or
	// skip), so the contract asserts only the invariant that matters for data
	// integrity: exactly one row exists for the ID afterward. It deliberately
	// does NOT bless last-writer-wins overwrite — whether a duplicate create
	// should instead surface a conflict is tracked separately.
	_ = s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "d-1", Title: "Second"}), "actor")
	got, err := s.GetIssuesByIDs(ctx(), []string{"d-1"})
	must(t, err)
	if len(got) != 1 {
		t.Errorf("after duplicate create: %d rows for d-1, want exactly 1", len(got))
	}
}

func testGetNotFound(t *testing.T, f Factory) {
	s := f(t)
	_, err := s.GetIssue(ctx(), "nonexistent")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func testGetByExternalRef(t *testing.T, f Factory) {
	s := f(t)
	ref := "gh-42"
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "e-1", Title: "Ext", ExternalRef: &ref}), "actor"))
	got, err := s.GetIssueByExternalRef(ctx(), "gh-42")
	if err != nil {
		t.Fatalf("GetIssueByExternalRef: %v", err)
	}
	if got.ID != "e-1" {
		t.Errorf("ID = %q", got.ID)
	}
	_, err = s.GetIssueByExternalRef(ctx(), "gh-999")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("missing ref: %v, want ErrNotFound", err)
	}
}

func testGetByIDs(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "g-1", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "g-2", Title: "B"}), "a"))
	got, err := s.GetIssuesByIDs(ctx(), []string{"g-1", "missing", "g-2"})
	if err != nil {
		t.Fatalf("GetIssuesByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func testUpdate(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "u-1", Title: "Old", Priority: 1}), "a"))
	must(t, s.UpdateIssue(ctx(), "u-1", map[string]interface{}{"title": "New", "priority": 3}, "a"))
	got, _ := s.GetIssue(ctx(), "u-1")
	if got.Title != "New" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.Priority != 3 {
		t.Errorf("Priority = %d", got.Priority)
	}
}

func testUpdatePreservesCreatedAt(t *testing.T, f Factory) {
	s := f(t)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "up-1", Title: "T", CreatedAt: created, CreatedBy: "orig"}), "orig"))
	must(t, s.UpdateIssue(ctx(), "up-1", map[string]interface{}{"title": "Changed"}, "updater"))
	got, _ := s.GetIssue(ctx(), "up-1")
	if !got.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt changed to %v", got.CreatedAt)
	}
	if got.CreatedBy != "orig" {
		t.Errorf("CreatedBy = %q", got.CreatedBy)
	}
}

func testUpdateNotFound(t *testing.T, f Factory) {
	s := f(t)
	err := s.UpdateIssue(ctx(), "missing", map[string]interface{}{"title": "x"}, "a")
	if err == nil {
		t.Error("expected error")
	}
}

func testUpdateIssueType(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ut-1", Title: "T", IssueType: "task"}), "a"))
	must(t, s.UpdateIssueType(ctx(), "ut-1", "epic", "a"))
	got, _ := s.GetIssue(ctx(), "ut-1")
	if got.IssueType != "epic" {
		t.Errorf("IssueType = %q", got.IssueType)
	}
}

func testCloseAndReopen(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cr-1", Title: "T"}), "a"))
	must(t, s.CloseIssue(ctx(), "cr-1", "done", "closer", "sess"))
	got, _ := s.GetIssue(ctx(), "cr-1")
	if got.Status != types.StatusClosed {
		t.Errorf("after close: Status = %q", got.Status)
	}
	if got.ClosedAt == nil {
		t.Error("ClosedAt is nil")
	}
	must(t, s.ReopenIssue(ctx(), "cr-1", "not done", "opener"))
	got, _ = s.GetIssue(ctx(), "cr-1")
	if got.Status != types.StatusOpen {
		t.Errorf("after reopen: Status = %q", got.Status)
	}
	if got.ClosedAt != nil {
		t.Error("ClosedAt should be nil after reopen")
	}
}

func testDelete(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "del-1", Title: "T"}), "a"))
	must(t, s.DeleteIssue(ctx(), "del-1"))
	_, err := s.GetIssue(ctx(), "del-1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
}

func testDeleteNotFound(t *testing.T, f Factory) {
	s := f(t)
	err := s.DeleteIssue(ctx(), "nonexistent")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// --- Search ---

func testSearchNoFilter(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	results, err := s.SearchIssues(ctx(), "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("SearchIssues: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("len = %d, want 4", len(results))
	}
}

func testSearchTextQuery(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	results, _ := s.SearchIssues(ctx(), "Alpha", types.IssueFilter{})
	if len(results) != 1 || results[0].ID != "test-1" {
		t.Errorf("text query 'Alpha': got %v", issueIDs(results))
	}
}

func testSearchStatusFilter(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	open := types.StatusOpen
	results, _ := s.SearchIssues(ctx(), "", types.IssueFilter{Status: &open})
	if len(results) != 2 {
		t.Errorf("status=open: len = %d, want 2", len(results))
	}
}

func testSearchPriorityFilter(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	p := 1
	results, _ := s.SearchIssues(ctx(), "", types.IssueFilter{Priority: &p})
	if len(results) != 2 {
		t.Errorf("priority=1: len = %d, want 2", len(results))
	}
}

func testSearchLimit(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	results, _ := s.SearchIssues(ctx(), "", types.IssueFilter{Limit: 2})
	if len(results) != 2 {
		t.Errorf("limit=2: len = %d", len(results))
	}
}

func testCountIssues(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	count, err := s.CountIssues(ctx(), "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("CountIssues: %v", err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}
}

func testCountByGroup(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	counts, _ := s.CountIssuesByGroup(ctx(), types.IssueFilter{}, "status")
	if counts["open"] != 2 {
		t.Errorf("open = %d, want 2", counts["open"])
	}
	if counts["closed"] != 1 {
		t.Errorf("closed = %d, want 1", counts["closed"])
	}
}

// testCountByGroupIsBlockedFilter proves the additive IssueFilter.IsBlocked predicate honors the
// denormalized is_blocked column in the grouped-count path on every backend: IsBlocked=&true returns
// only is_blocked=true beads per status, &false the complement, and nil the whole population
// (unchanged from testCountByGroup). Blockedness is produced the same way a real writer does — an open
// blocker on a blocks-type dependency, which the write path denormalizes onto is_blocked.
func testCountByGroupIsBlockedFilter(t *testing.T, f Factory) {
	s := f(t)
	// Two open beads blocked by an open blocker (is_blocked=true after AddDependency), one open
	// bead left unblocked, and one closed bead (is_blocked=false) — a (status × is_blocked) mix.
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ibf-blk", Title: "Blocker", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ibf-w1", Title: "Waiter1", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ibf-w2", Title: "Waiter2", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ibf-free", Title: "Free", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ibf-done", Title: "Done", Status: types.StatusClosed}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ibf-w1", DependsOnID: "ibf-blk", Type: types.DepBlocks}, "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ibf-w2", DependsOnID: "ibf-blk", Type: types.DepBlocks}, "a"))

	blockedTrue := true
	blockedFalse := false

	// IsBlocked=&true: only the two blocked waiters, both status=open.
	onlyBlocked, err := s.CountIssuesByGroup(ctx(), types.IssueFilter{IsBlocked: &blockedTrue}, "status")
	if err != nil {
		t.Fatalf("CountIssuesByGroup(IsBlocked=true): %v", err)
	}
	if onlyBlocked["open"] != 2 {
		t.Errorf("IsBlocked=true open = %d, want 2 (the blocked waiters only)", onlyBlocked["open"])
	}
	if onlyBlocked["closed"] != 0 {
		t.Errorf("IsBlocked=true closed = %d, want 0", onlyBlocked["closed"])
	}

	// IsBlocked=&false: the complement — the blocker + the free bead (open) and the done bead (closed).
	onlyUnblocked, err := s.CountIssuesByGroup(ctx(), types.IssueFilter{IsBlocked: &blockedFalse}, "status")
	if err != nil {
		t.Fatalf("CountIssuesByGroup(IsBlocked=false): %v", err)
	}
	if onlyUnblocked["open"] != 2 {
		t.Errorf("IsBlocked=false open = %d, want 2 (blocker + free)", onlyUnblocked["open"])
	}
	if onlyUnblocked["closed"] != 1 {
		t.Errorf("IsBlocked=false closed = %d, want 1 (done)", onlyUnblocked["closed"])
	}

	// nil: the whole population, and the true/false split partitions it exactly (no double-count).
	all, err := s.CountIssuesByGroup(ctx(), types.IssueFilter{}, "status")
	if err != nil {
		t.Fatalf("CountIssuesByGroup(nil): %v", err)
	}
	if all["open"] != 4 || all["closed"] != 1 {
		t.Errorf("nil filter open/closed = %d/%d, want 4/1", all["open"], all["closed"])
	}
	for _, status := range []string{"open", "closed"} {
		if onlyBlocked[status]+onlyUnblocked[status] != all[status] {
			t.Errorf("status %q: blocked(%d) + unblocked(%d) != all(%d) — the is_blocked split must partition",
				status, onlyBlocked[status], onlyUnblocked[status], all[status])
		}
	}
}

// --- Dependencies ---

func testAddAndGetDeps(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dep-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dep-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "dep-b", DependsOnID: "dep-a", Type: types.DepBlocks}, "a"))
	deps, _ := s.GetDependencies(ctx(), "dep-b")
	if len(deps) != 1 || deps[0].ID != "dep-a" {
		t.Errorf("GetDependencies = %v", issueIDs(deps))
	}
	dependents, _ := s.GetDependents(ctx(), "dep-a")
	if len(dependents) != 1 || dependents[0].ID != "dep-b" {
		t.Errorf("GetDependents = %v", issueIDs(dependents))
	}
}

func testRemoveDep(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rd-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rd-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rd-b", DependsOnID: "rd-a", Type: types.DepBlocks}, "a"))
	must(t, s.RemoveDependency(ctx(), "rd-b", "rd-a", "a"))
	deps, _ := s.GetDependencies(ctx(), "rd-b")
	if len(deps) != 0 {
		t.Errorf("after remove: len = %d", len(deps))
	}
}

func testDepCounts(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dc-a", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "dc-b", Title: "B"}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "dc-b", DependsOnID: "dc-a", Type: types.DepBlocks}, "a"))
	depCount, _ := s.CountDependencies(ctx(), "dc-b")
	if depCount != 1 {
		t.Errorf("CountDependencies = %d", depCount)
	}
	deptCount, _ := s.CountDependents(ctx(), "dc-a")
	if deptCount != 1 {
		t.Errorf("CountDependents = %d", deptCount)
	}
}

// --- Ready/Blocked ---

func testReadyNoDeps(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "r-1", Title: "T", Status: types.StatusOpen}), "a"))
	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	if len(ready) != 1 || ready[0].ID != "r-1" {
		t.Errorf("ready = %v", issueIDs(ready))
	}
}

func testReadyBlockedByOpenDep(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rb-1", Title: "Blocker", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "rb-2", Title: "Blocked", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "rb-2", DependsOnID: "rb-1", Type: types.DepBlocks}, "a"))
	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	ids := issueIDs(ready)
	if len(ids) != 1 || ids[0] != "rb-1" {
		t.Errorf("ready = %v, want [rb-1]", ids)
	}
}

func testReadyUnblockedByClose(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ru-1", Title: "Dep", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ru-2", Title: "Waiter", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ru-2", DependsOnID: "ru-1", Type: types.DepBlocks}, "a"))
	must(t, s.CloseIssue(ctx(), "ru-1", "done", "a", "s"))
	ready, _ := s.GetReadyWork(ctx(), types.WorkFilter{})
	ids := issueIDs(ready)
	if len(ids) != 1 || ids[0] != "ru-2" {
		t.Errorf("after close: ready = %v, want [ru-2]", ids)
	}
}

func testBlockedIssues(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "bl-1", Title: "Blocker", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "bl-2", Title: "Blocked", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "bl-2", DependsOnID: "bl-1", Type: types.DepBlocks}, "a"))
	blocked, _ := s.GetBlockedIssues(ctx(), types.WorkFilter{})
	if len(blocked) != 1 || blocked[0].ID != "bl-2" {
		t.Errorf("blocked = %v", blocked)
	}
	if blocked[0].BlockedByCount != 1 {
		t.Errorf("BlockedByCount = %d", blocked[0].BlockedByCount)
	}
}

func testEpicsEligible(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ep-1", Title: "Epic", IssueType: types.TypeEpic, Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ep-1a", Title: "Child", Status: types.StatusClosed}), "a"))
	must(t, s.AddDependency(ctx(), &types.Dependency{IssueID: "ep-1a", DependsOnID: "ep-1", Type: types.DepParentChild}, "a"))
	epics, _ := s.GetEpicsEligibleForClosure(ctx())
	if len(epics) != 1 || !epics[0].EligibleForClose {
		t.Errorf("expected epic eligible for closure")
	}
}

// --- Labels ---

func testLabels(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "lb-1", Title: "T"}), "a"))
	must(t, s.AddLabel(ctx(), "lb-1", "bug", "a"))
	must(t, s.AddLabel(ctx(), "lb-1", "urgent", "a"))
	labels, _ := s.GetLabels(ctx(), "lb-1")
	if len(labels) != 2 {
		t.Errorf("labels = %v", labels)
	}
	must(t, s.RemoveLabel(ctx(), "lb-1", "bug", "a"))
	labels, _ = s.GetLabels(ctx(), "lb-1")
	if len(labels) != 1 || labels[0] != "urgent" {
		t.Errorf("after remove: labels = %v", labels)
	}
}

func testLabelIdempotent(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "li-1", Title: "T"}), "a"))
	must(t, s.AddLabel(ctx(), "li-1", "x", "a"))
	must(t, s.AddLabel(ctx(), "li-1", "x", "a"))
	labels, _ := s.GetLabels(ctx(), "li-1")
	if len(labels) != 1 {
		t.Errorf("labels = %v, want 1", labels)
	}
}

func testGetIssuesByLabel(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "gl-1", Title: "A"}), "a"))
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "gl-2", Title: "B"}), "a"))
	must(t, s.AddLabel(ctx(), "gl-1", "shared", "a"))
	must(t, s.AddLabel(ctx(), "gl-2", "shared", "a"))
	issues, _ := s.GetIssuesByLabel(ctx(), "shared")
	if len(issues) != 2 {
		t.Errorf("GetIssuesByLabel: len = %d", len(issues))
	}
}

// --- Comments ---

func testComments(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cm-1", Title: "T"}), "a"))
	c1, err := s.AddIssueComment(ctx(), "cm-1", "alice", "First")
	if err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	if c1.Text != "First" {
		t.Errorf("comment text = %q", c1.Text)
	}
	_, _ = s.AddIssueComment(ctx(), "cm-1", "bob", "Second")
	comments, _ := s.GetIssueComments(ctx(), "cm-1")
	if len(comments) != 2 {
		t.Errorf("comments len = %d", len(comments))
	}
	// Verify chronological order.
	if comments[0].CreatedAt.After(comments[1].CreatedAt) {
		t.Error("comments not in chronological order")
	}
}

func testCommentCount(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "cc-1", Title: "T"}), "a"))
	_, _ = s.AddIssueComment(ctx(), "cc-1", "a", "one")
	_, _ = s.AddIssueComment(ctx(), "cc-1", "a", "two")
	count, _ := s.CountIssueComments(ctx(), "cc-1")
	if count != 2 {
		t.Errorf("count = %d", count)
	}
}

// --- Config ---

func testConfig(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.SetConfig(ctx(), "key1", "val1"))
	must(t, s.SetConfig(ctx(), "key2", "val2"))
	v, _ := s.GetConfig(ctx(), "key1")
	if v != "val1" {
		t.Errorf("GetConfig = %q", v)
	}
	all, _ := s.GetAllConfig(ctx())
	if len(all) < 2 {
		t.Errorf("GetAllConfig len = %d", len(all))
	}
}

func testLocalMetadata(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.SetLocalMetadata(ctx(), "lk", "lv"))
	v, _ := s.GetLocalMetadata(ctx(), "lk")
	if v != "lv" {
		t.Errorf("GetLocalMetadata = %q", v)
	}
	// Missing key returns empty string, not error.
	v, err := s.GetLocalMetadata(ctx(), "missing")
	if err != nil {
		t.Errorf("missing key error: %v", err)
	}
	if v != "" {
		t.Errorf("missing key value = %q", v)
	}
}

// --- Metadata Slots ---

func testMetadataSlots(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "sl-1", Title: "T"}), "a"))
	must(t, s.SlotSet(ctx(), "sl-1", "mykey", "myval", "a"))
	v, _ := s.SlotGet(ctx(), "sl-1", "mykey")
	if v != "myval" {
		t.Errorf("SlotGet = %q", v)
	}
	must(t, s.SlotClear(ctx(), "sl-1", "mykey", "a"))
	v, _ = s.SlotGet(ctx(), "sl-1", "mykey")
	if v != "" {
		t.Errorf("after clear: SlotGet = %q", v)
	}
}

// --- Statistics ---

func testStatistics(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	stats, err := s.GetStatistics(ctx())
	if err != nil {
		t.Fatalf("GetStatistics: %v", err)
	}
	if stats.TotalIssues != 4 {
		t.Errorf("TotalIssues = %d", stats.TotalIssues)
	}
	if stats.ClosedIssues != 1 {
		t.Errorf("ClosedIssues = %d", stats.ClosedIssues)
	}
}

// --- Stale ---

func testStaleIssues(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Two issues last touched years ago (stalest first by updated_at), one fresh,
	// and one aged-but-closed. Staleness is decided on updated_at, which CreateIssue
	// honors when preset (issueops/create.go), so no clock manipulation is needed —
	// and a year between the aged timestamps keeps the order unambiguous across
	// backends (no whole-second tie).
	y2020 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	y2021 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "sl-old1", Title: "oldest open", Status: types.StatusOpen, CreatedAt: y2020, UpdatedAt: y2020}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "sl-old2", Title: "aged in-progress", Status: types.StatusInProgress, CreatedAt: y2021, UpdatedAt: y2021}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "sl-fresh", Title: "fresh open", Status: types.StatusOpen}), "actor"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "sl-closed", Title: "aged but closed", Status: types.StatusClosed, CreatedAt: y2020, UpdatedAt: y2020}), "actor"))

	// Default (open + in_progress): the two aged issues, stalest first; the fresh one
	// and the closed one are excluded. Order is contractual (updated_at ASC).
	got, err := s.GetStaleIssues(c, types.StaleFilter{Days: 30})
	if err != nil {
		t.Fatalf("GetStaleIssues: %v", err)
	}
	if seq := orderedIDs(got); !slices.Equal(seq, []string{"sl-old1", "sl-old2"}) {
		t.Fatalf("GetStaleIssues(Days=30) = %v, want [sl-old1 sl-old2]", seq)
	}

	// Status filter narrows to open only.
	openOnly, err := s.GetStaleIssues(c, types.StaleFilter{Days: 30, Status: "open"})
	must(t, err)
	if seq := orderedIDs(openOnly); !slices.Equal(seq, []string{"sl-old1"}) {
		t.Fatalf("GetStaleIssues(status=open) = %v, want [sl-old1]", seq)
	}

	// Limit caps the result set.
	limited, err := s.GetStaleIssues(c, types.StaleFilter{Days: 30, Limit: 1})
	must(t, err)
	if len(limited) != 1 {
		t.Fatalf("GetStaleIssues(limit=1) returned %d, want 1", len(limited))
	}

	// Nothing is stale on a century horizon.
	none, err := s.GetStaleIssues(c, types.StaleFilter{Days: 36500})
	must(t, err)
	if len(none) != 0 {
		t.Fatalf("GetStaleIssues(Days=36500) = %v, want none", orderedIDs(none))
	}
}

// --- Iterators ---

func testIterIssues(t *testing.T, f Factory) {
	s := f(t)
	seedStore(t, s)
	it, err := s.IterIssues(ctx(), "", types.IssueFilter{})
	if err != nil {
		t.Fatalf("IterIssues: %v", err)
	}
	defer func() { _ = it.Close() }()
	count := 0
	for it.Next(ctx()) {
		count++
		if it.Value().ID == "" {
			t.Error("empty ID from iterator")
		}
	}
	if err := it.Err(); err != nil {
		t.Fatalf("Iter error: %v", err)
	}
	if count != 4 {
		t.Errorf("iterated %d, want 4", count)
	}
}

func testIterComments(t *testing.T, f Factory) {
	s := f(t)
	must(t, s.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "ic-1", Title: "T"}), "a"))
	_, _ = s.AddIssueComment(ctx(), "ic-1", "a", "one")
	_, _ = s.AddIssueComment(ctx(), "ic-1", "a", "two")
	it, err := s.IterIssueComments(ctx(), "ic-1")
	if err != nil {
		t.Fatalf("IterIssueComments: %v", err)
	}
	defer func() { _ = it.Close() }()
	count := 0
	for it.Next(ctx()) {
		count++
	}
	if count != 2 {
		t.Errorf("iterated %d comments, want 2", count)
	}
}

// --- Transaction ---

func testTransaction(t *testing.T, f Factory) {
	s := f(t)
	err := s.RunInTransaction(ctx(), "test", func(tx storage.Transaction) error {
		if err := tx.CreateIssue(ctx(), withDefaults(&types.Issue{ID: "tx-1", Title: "In TX"}), "a"); err != nil {
			return err
		}
		return tx.AddLabel(ctx(), "tx-1", "from-tx", "a")
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
	got, err := s.GetIssue(ctx(), "tx-1")
	if err != nil {
		t.Fatalf("GetIssue after tx: %v", err)
	}
	if got.Title != "In TX" {
		t.Errorf("title = %q", got.Title)
	}
	labels, _ := s.GetLabels(ctx(), "tx-1")
	found := false
	for _, l := range labels {
		if l == "from-tx" {
			found = true
		}
	}
	if !found {
		t.Errorf("label 'from-tx' not found, got %v", labels)
	}
}

// sumCounts totals a CountIssuesByGroup result.
func sumCounts(m map[string]int) int {
	total := 0
	for _, n := range m {
		total += n
	}
	return total
}

// snapView bundles the composite-view reads taken at a single point inside a
// transaction so the baseline and post-mutation snapshots can be compared as
// plain data instead of a wall of inline reads. Both snapshots share these five
// reads; point-specific extras (comments, the snap-1-scoped event feed, and the
// batch blocked flags) are read separately by the caller.
type snapView struct {
	issues []*types.Issue
	counts map[string]int
	deps   []*types.Dependency
	nDep   int
	events []*types.Event
}

// readSnapView captures the five composite-view reads shared by the baseline and
// post-mutation snapshots in testTransactionSnapshotReads.
func readSnapView(c context.Context, tx storage.Transaction) (snapView, error) {
	var v snapView
	var err error
	if v.issues, err = tx.SearchIssues(c, "", types.IssueFilter{}); err != nil {
		return v, err
	}
	if v.counts, err = tx.CountIssuesByGroup(c, types.IssueFilter{}, "status"); err != nil {
		return v, err
	}
	if v.deps, err = tx.GetDependentRecords(c, "snap-1", "", 0, ""); err != nil {
		return v, err
	}
	if v.nDep, err = tx.CountDependentRecords(c, "snap-1", ""); err != nil {
		return v, err
	}
	if v.events, err = tx.EventsSince(c, storage.EventCursor{}, "", 1000); err != nil {
		return v, err
	}
	return v, nil
}

// testTransactionSnapshotReads proves the new reads run on the transaction's
// live snapshot rather than a fresh (committed-only) connection: it reads a
// composite view off the opening snapshot, mutates INSIDE the transaction, and
// re-reads — asserting every read reflects the in-transaction mutation. A
// non-transactional implementation would return the pre-mutation state on the
// second read and fail every delta. It keeps the cross-read arithmetic checks so
// a torn read (values drawn from different points) is also caught.
//
// The fixture includes a committed wisp so the count-vs-search wisp asymmetry is
// exercised rather than silently avoided (CountIssuesByGroup merges wisps;
// SearchIssues reads the issues table only). External-connection mutation (the
// classic snapshot-isolation probe) is not used because the Dolt store pins a
// single connection per transaction (MaxOpenConns=1), so a second op would
// deadlock; the in-transaction mutation is the deterministic equivalent.
func testTransactionSnapshotReads(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "snap-1", Title: "Root", Status: types.StatusOpen, IssueType: "bug", Priority: 0}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "snap-2", Title: "Child", Status: types.StatusOpen, IssueType: "task", Priority: 1}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "snap-3", Title: "Other", Status: types.StatusInProgress, IssueType: "task", Priority: 1}), "a"))
	// A committed wisp: counted by CountIssuesByGroup but not by SearchIssues.
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "snap-wisp", Title: "Wisp", Status: types.StatusOpen, Ephemeral: true}), "a"))
	const committedWisps = 1
	// snap-2 depends on (is blocked by) snap-1.
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "snap-2", DependsOnID: "snap-1", Type: types.DepBlocks}, "a"))
	// Two comments on snap-1.
	if _, err := s.AddIssueComment(c, "snap-1", "a", "first"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}
	if _, err := s.AddIssueComment(c, "snap-1", "a", "second"); err != nil {
		t.Fatalf("AddIssueComment: %v", err)
	}

	err := s.RunInTransaction(c, "bd: snapshot", func(tx storage.Transaction) error {
		// --- Baseline reads off the opening snapshot. ---
		base, err := readSnapView(c, tx)
		if err != nil {
			return err
		}
		comments0, err := tx.GetIssueCommentsPage(c, "snap-1", storage.CommentPageCursor{}, 0)
		if err != nil {
			return err
		}
		scoped0, err := tx.EventsSince(c, storage.EventCursor{}, "snap-1", 1000)
		if err != nil {
			return err
		}
		assertSnapBaseline(t, base, comments0, scoped0, committedWisps)

		// --- Mutate INSIDE the transaction, then re-read the same view. ---
		if err := tx.CreateIssue(c, withDefaults(&types.Issue{ID: "snap-4", Title: "InTx", Status: types.StatusOpen, IssueType: "task", Priority: 1}), "a"); err != nil {
			return err
		}
		if err := tx.AddDependency(c, &types.Dependency{IssueID: "snap-4", DependsOnID: "snap-1", Type: types.DepBlocks}, "a"); err != nil {
			return err
		}

		after, err := readSnapView(c, tx)
		if err != nil {
			return err
		}
		blocked1, err := tx.IsBlockedBatch(c, []string{"snap-1", "snap-2", "snap-3", "snap-4"})
		if err != nil {
			return err
		}
		assertSnapDelta(t, base, after, blocked1)
		return nil
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
}

// containsDependentFrom reports whether deps contains an inbound edge sourced at
// sourceID.
func containsDependentFrom(deps []*types.Dependency, sourceID string) bool {
	for _, d := range deps {
		if d.IssueID == sourceID {
			return true
		}
	}
	return false
}

// containsIssueID reports whether issues contains an issue with the given id.
func containsIssueID(issues []*types.Issue, id string) bool {
	for _, i := range issues {
		if i.ID == id {
			return true
		}
	}
	return false
}

// assertSnapBaseline checks the opening-snapshot reads: the durable+wisp count
// sum, presence of every durable issue, the single inbound edge on snap-1 (with
// list/count agreement), the two comments, a non-empty event feed, and that the
// snap-1-scoped feed never leaks another issue's events.
func assertSnapBaseline(t *testing.T, v snapView, comments []*types.Comment, scoped []*types.Event, committedWisps int) {
	t.Helper()
	// CountIssuesByGroup merges wisps on every backend, so its buckets sum to the
	// 3 durable issues + the committed wisp. Whether SearchIssues also counts that
	// committed wisp is backend-specific (the classic Dolt tx reads the issues
	// table only; the embedded tx merges both), so we assert the durable issues
	// are all present rather than a wisp-free equality — see the count-vs-search
	// wisp-scoping asymmetry noted on storage.Transaction.
	const durableSeeded = 3
	if got := sumCounts(v.counts); got != durableSeeded+committedWisps {
		t.Errorf("baseline sum(CountIssuesByGroup)=%d, want %d (durable + committed wisp)", got, durableSeeded+committedWisps)
	}
	for _, id := range []string{"snap-1", "snap-2", "snap-3"} {
		if !containsIssueID(v.issues, id) {
			t.Errorf("baseline SearchIssues missing durable issue %s", id)
		}
	}
	if len(v.deps) != 1 || v.deps[0].IssueID != "snap-2" {
		t.Errorf("baseline GetDependentRecords(snap-1)=%v, want one edge from snap-2", v.deps)
	}
	if v.nDep != len(v.deps) {
		t.Errorf("baseline CountDependentRecords=%d != len(GetDependentRecords)=%d", v.nDep, len(v.deps))
	}
	if len(comments) != 2 {
		t.Errorf("baseline GetIssueCommentsPage(snap-1)=%d comments, want 2", len(comments))
	}
	if len(v.events) == 0 {
		t.Errorf("baseline EventsSince(all) returned no events")
	}
	for _, e := range scoped {
		if e.IssueID != "snap-1" {
			t.Errorf("EventsSince(snap-1) leaked event for %s", e.IssueID)
		}
	}
}

// assertSnapDelta checks that every composite-view read reflects exactly the
// in-transaction create + edge (one new issue, one new inbound edge on snap-1
// from snap-4 with snap-4 reading blocked) with no torn or missing rows.
func assertSnapDelta(t *testing.T, base, after snapView, blocked map[string]bool) {
	t.Helper()
	if len(after.issues) != len(base.issues)+1 {
		t.Errorf("SearchIssues after in-tx create = %d, want %d", len(after.issues), len(base.issues)+1)
	}
	if sumCounts(after.counts) != sumCounts(base.counts)+1 {
		t.Errorf("CountIssuesByGroup total after in-tx create = %d, want %d", sumCounts(after.counts), sumCounts(base.counts)+1)
	}
	if len(after.deps) != len(base.deps)+1 {
		t.Errorf("GetDependentRecords(snap-1) after in-tx edge = %d, want %d", len(after.deps), len(base.deps)+1)
	}
	if !containsDependentFrom(after.deps, "snap-4") {
		t.Errorf("GetDependentRecords(snap-1) missing in-tx edge from snap-4: %v", after.deps)
	}
	if after.nDep != base.nDep+1 {
		t.Errorf("CountDependentRecords(snap-1) after in-tx edge = %d, want %d", after.nDep, base.nDep+1)
	}
	if !blocked["snap-4"] {
		t.Errorf("snap-4 should read blocked after its in-tx dependency")
	}
	if !blocked["snap-2"] {
		t.Errorf("snap-2 should still read blocked")
	}
	if blocked["snap-1"] || blocked["snap-3"] {
		t.Errorf("snap-1/snap-3 should read not-blocked, got %v", blocked)
	}
	if len(after.events) <= len(base.events) {
		t.Errorf("EventsSince(all) did not grow after in-tx writes: %d <= %d", len(after.events), len(base.events))
	}
}

// testTransactionReadYourWrites creates an issue graph inside a transaction and
// reads it back through the new composite-view methods BEFORE commit, proving
// the transaction sees its own uncommitted writes — including through the
// grouped-count and event-feed reads.
func testTransactionReadYourWrites(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	err := s.RunInTransaction(c, "bd: ryw", func(tx storage.Transaction) error {
		if err := seedReadYourWritesGraph(c, tx); err != nil {
			return err
		}
		if err := assertInTxDependentReads(t, c, tx); err != nil {
			return err
		}
		return assertInTxCountsAndEvents(t, c, tx)
	})
	if err != nil {
		t.Fatalf("RunInTransaction: %v", err)
	}
}

// seedReadYourWritesGraph creates the ryw-1 <- ryw-2 (blocked-by) graph inside
// the transaction that reads it back.
func seedReadYourWritesGraph(c context.Context, tx storage.Transaction) error {
	if err := tx.CreateIssue(c, withDefaults(&types.Issue{ID: "ryw-1", Title: "Blocker", Status: types.StatusOpen}), "a"); err != nil {
		return err
	}
	if err := tx.CreateIssue(c, withDefaults(&types.Issue{ID: "ryw-2", Title: "Blocked", Status: types.StatusOpen}), "a"); err != nil {
		return err
	}
	return tx.AddDependency(c, &types.Dependency{IssueID: "ryw-2", DependsOnID: "ryw-1", Type: types.DepBlocks}, "a")
}

// assertInTxDependentReads proves the dependents/blocked family reads the graph's
// own uncommitted writes: the single inbound edge on ryw-1 (list, by-target map,
// and count all agree) and the blocked flags (ryw-2 blocked, ryw-1 not) through
// both IsBlockedBatch and IsBlocked.
func assertInTxDependentReads(t *testing.T, c context.Context, tx storage.Transaction) error {
	t.Helper()
	dependents, err := tx.GetDependentRecords(c, "ryw-1", "", 0, "")
	if err != nil {
		return err
	}
	if len(dependents) != 1 || dependents[0].IssueID != "ryw-2" {
		t.Errorf("in-tx GetDependentRecords(ryw-1) = %v, want one edge from ryw-2", dependents)
	}
	byTarget, err := tx.GetDependentRecordsForIssues(c, []string{"ryw-1"})
	if err != nil {
		return err
	}
	if len(byTarget["ryw-1"]) != 1 {
		t.Errorf("in-tx GetDependentRecordsForIssues[ryw-1] = %d, want 1", len(byTarget["ryw-1"]))
	}
	nDep, err := tx.CountDependentRecords(c, "ryw-1", "")
	if err != nil {
		return err
	}
	if nDep != 1 {
		t.Errorf("in-tx CountDependentRecords(ryw-1) = %d, want 1", nDep)
	}
	batch, err := tx.IsBlockedBatch(c, []string{"ryw-1", "ryw-2"})
	if err != nil {
		return err
	}
	if !batch["ryw-2"] || batch["ryw-1"] {
		t.Errorf("in-tx IsBlockedBatch = %v, want ryw-2 blocked, ryw-1 not", batch)
	}
	isBlocked, blockers, err := tx.IsBlocked(c, "ryw-2")
	if err != nil {
		return err
	}
	if !isBlocked || len(blockers) != 1 || blockers[0] != "ryw-1" {
		t.Errorf("in-tx IsBlocked(ryw-2) = %v, %v, want true, [ryw-1]", isBlocked, blockers)
	}
	return nil
}

// assertInTxCountsAndEvents proves the grouped counts and durable event feed also
// reflect the transaction's own uncommitted writes.
func assertInTxCountsAndEvents(t *testing.T, c context.Context, tx storage.Transaction) error {
	t.Helper()
	// The two in-tx durable issues are visible through the grouped counts.
	counts, err := tx.CountIssuesByGroup(c, types.IssueFilter{}, "status")
	if err != nil {
		return err
	}
	if sumCounts(counts) != 2 {
		t.Errorf("in-tx CountIssuesByGroup total = %d, want 2 (both uncommitted issues)", sumCounts(counts))
	}
	// The in-tx create event is visible through the durable event feed.
	evs, err := tx.EventsSince(c, storage.EventCursor{}, "ryw-1", 100)
	if err != nil {
		return err
	}
	if len(evs) == 0 {
		t.Errorf("in-tx EventsSince(ryw-1) returned no events for the uncommitted create")
	}
	return nil
}

// --- Ready-work counts equivalence (perf/ready-counts) ---

// marshalCounts renders a counts slice to JSON for byte-equivalence comparison
// between the page-pushed and unbounded queries.
func marshalCounts(t *testing.T, items []*types.IssueWithCounts) string {
	t.Helper()
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal counts: %v", err)
	}
	return string(b)
}

// assertReadyCountsEquivalence checks the two central claims of the ready-counts
// fast path against a real backend: (a) each bounded page — which resolves a
// page of IDs then hydrates counts constrained to those IDs — is byte-identical
// to the same-length prefix of the unbounded predicate query, and (b)
// CountReadyWork equals len(unbounded) regardless of the page cap.
func assertReadyCountsEquivalence(t *testing.T, s storage.DoltStorage, base types.WorkFilter, pages []int) {
	t.Helper()
	c := ctx()

	unboundedFilter := base
	unboundedFilter.Limit = 0
	unbounded, err := s.GetReadyWorkWithCounts(c, unboundedFilter)
	if err != nil {
		t.Fatalf("GetReadyWorkWithCounts(unbounded): %v", err)
	}

	counter, ok := s.(storage.ReadyWorkCounter)
	if !ok {
		t.Fatalf("store does not implement storage.ReadyWorkCounter")
	}

	for _, page := range pages {
		pf := base
		pf.Limit = page

		got, err := s.GetReadyWorkWithCounts(c, pf)
		if err != nil {
			t.Fatalf("GetReadyWorkWithCounts(limit=%d): %v", page, err)
		}
		want := unbounded
		if page > 0 && page < len(unbounded) {
			want = unbounded[:page]
		}
		if gj, wj := marshalCounts(t, got), marshalCounts(t, want); gj != wj {
			t.Fatalf("ready counts page (limit=%d) not byte-identical to unbounded prefix:\n got: %s\nwant: %s", page, gj, wj)
		}

		n, err := counter.CountReadyWork(c, pf)
		if err != nil {
			t.Fatalf("CountReadyWork(limit=%d): %v", page, err)
		}
		if n != len(unbounded) {
			t.Fatalf("CountReadyWork(limit=%d) = %d, want %d (len unbounded)", page, n, len(unbounded))
		}
	}
}

func testReadyCountsPageEquivalence(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// 12 ready issues with varied priority.
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("rc-%02d", i)
		must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: id, Title: id, Priority: i % 4, Status: types.StatusOpen}), "a"))
	}
	// A closed blocker leaves rc-01 ready but with DependencyCount=1, so the
	// hydrated counts are non-trivial (not all zero).
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "rc-dep", Title: "dep", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "rc-01", DependsOnID: "rc-dep", Type: types.DepBlocks}, "a"))
	must(t, s.CloseIssue(c, "rc-dep", "done", "a", "s"))
	// A still-open blocker keeps rc-blocked out of the ready set (rc-blk stays ready).
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "rc-blk", Title: "blk", Status: types.StatusOpen}), "a"))
	must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: "rc-blocked", Title: "blocked", Status: types.StatusOpen}), "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "rc-blocked", DependsOnID: "rc-blk", Type: types.DepBlocks}, "a"))
	// Comment, label, and parent-child so those columns vary across rows.
	must(t, s.AddComment(c, "rc-02", "a", "hi"))
	must(t, s.AddLabel(c, "rc-03", "urgent", "a"))
	must(t, s.AddDependency(c, &types.Dependency{IssueID: "rc-04", DependsOnID: "rc-01", Type: types.DepParentChild}, "a"))

	// Ready set: rc-01..rc-12 plus rc-blk = 13. Page < ready exercises the
	// by-IDs path; page == and > ready exercise the boundary.
	assertReadyCountsEquivalence(t, s, types.WorkFilter{SortPolicy: types.SortPolicyOldest}, []int{1, 5, 13, 50})
}

func testReadyCountsWithWisps(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// Durable ready issues.
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("wc-i%02d", i)
		must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: id, Title: id, Priority: i % 3, Status: types.StatusOpen}), "a"))
	}
	// Ready ephemeral wisps (routed to the wisps table).
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("wc-w%02d", i)
		must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: id, Title: id, Priority: i % 3, Status: types.StatusOpen, Ephemeral: true}), "a"))
	}

	// IncludeEphemeral makes the ready set the issues∪wisps union the counts path
	// merges, exercising CountReadyWork's two-family COUNT(*) + overlap path.
	assertReadyCountsEquivalence(t, s, types.WorkFilter{SortPolicy: types.SortPolicyOldest, IncludeEphemeral: true}, []int{1, 3, 7, 20})
}

func testReadyCountsPageChunking(t *testing.T, f Factory) {
	s := f(t)
	c := ctx()

	// A page larger than sqlbuild.QueryBatchSize (200) forces the by-IDs
	// hydration to chunk its IN-list; the merged result must still match the
	// unbounded query byte-for-byte.
	const n = 205
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("ch-%04d", i)
		must(t, s.CreateIssue(c, withDefaults(&types.Issue{ID: id, Title: id, Status: types.StatusOpen}), "a"))
	}

	// limit=100 stays within one chunk; limit=205 and 250 span two chunks.
	assertReadyCountsEquivalence(t, s, types.WorkFilter{SortPolicy: types.SortPolicyOldest}, []int{100, n, 250})
}
