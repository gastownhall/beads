package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestLifecycleCloseReopenContract runs the Close/Reopen half of the Lifecycle
// contract against the server-backed store. It shares its validate/execute body
// with the embedded store (internal/storage/issueops ExecuteClose and
// ExecuteReopen), so this wiring and the embedded one are ONE vote on the
// semantics; the unit-of-work wiring is the second.
//
// The cases are subtests of one parent so the whole role suite shares one store
// and one copy-on-write branch: each Run namespaces its ids under the fixture
// prefix, and the event assertions take before/after deltas, which is only
// meaningful while the subtests run sequentially. setupTestStore already marks
// the PARENT parallel; no subtest here calls t.Parallel.
func TestLifecycleCloseReopenContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltLifecycleCloseReopenFixture(t, "lcr")
	defer cleanup()

	t.Run("CloseRefusalsCarryTheirTypesAndWriteNothing", func(t *testing.T) {
		conformance.RunLifecycleCloseRefusalsCarryTheirTypesAndWriteNothing(t, ctx, fixture)
	})
	t.Run("CloseAdmitsATransitivelyBlockedTarget", func(t *testing.T) {
		conformance.RunLifecycleCloseAdmitsATransitivelyBlockedTarget(t, ctx, fixture)
	})
	t.Run("CloseIsIdempotentAndKeepsTheFirstClose", func(t *testing.T) {
		conformance.RunLifecycleCloseIsIdempotentAndKeepsTheFirstClose(t, ctx, fixture)
	})
	t.Run("ReopenLeavesNonDoneStatusesUnchanged", func(t *testing.T) {
		conformance.RunLifecycleReopenLeavesNonDoneStatusesUnchanged(t, ctx, fixture)
	})
	t.Run("CloseAndReopenSpanTheConfiguredDoneCategory", func(t *testing.T) {
		conformance.RunLifecycleCloseAndReopenSpanTheConfiguredDoneCategory(t, ctx, fixture)
	})
	t.Run("ExpectedVersionIsCheckedBeforeTheNoOps", func(t *testing.T) {
		conformance.RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps(t, ctx, fixture)
	})
	t.Run("ReopenRecordsItsReason", func(t *testing.T) {
		conformance.RunLifecycleReopenRecordsItsReason(t, ctx, fixture)
	})
	t.Run("ReopenProvenanceLabelsHistory", func(t *testing.T) {
		conformance.RunLifecycleReopenProvenanceLabelsHistory(t, ctx, fixture)
	})
	t.Run("ResultsAreHydratedPostStateSnapshots", func(t *testing.T) {
		conformance.RunLifecycleResultsAreHydratedPostStateSnapshots(t, ctx, fixture)
	})
	t.Run("CloseAndReopenRequireActorAndIssueID", func(t *testing.T) {
		conformance.RunLifecycleCloseAndReopenRequireActorAndIssueID(t, ctx, fixture)
	})
}

func newDoltLifecycleCloseReopenFixture(t *testing.T, prefix string) (conformance.LifecycleCloseReopenFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	// Through the capability accessor, not NewIssueOperations: a store that
	// stopped offering the role is the regression, and a constructor call would
	// hide it.
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	fixture := conformance.LifecycleCloseReopenFixture{
		IssuePrefix:   kit.IssuePrefix,
		Lifecycle:     lifecycle,
		CreateIssue:   kit.CreateIssue,
		AddDependency: kit.AddDependency,
		SetConfig:     kit.SetConfig,
		QueryScalar:   kit.QueryScalar,
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
