package dolt

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
)

// TestLeaseReclaimerContract runs the LeaseReclaimer contract against the
// server-backed store, which wraps internal/storage/issueops.ExecuteReclaimInTx
// in its own retrying write transaction. Like every role suite here the cases
// share one store, so each namespaces its seeds and scopes every sweep.
func TestLeaseReclaimerContract(t *testing.T) {
	fixture, ctx, cleanup := newDoltLeaseReclaimerFixture(t, "lrc")
	defer cleanup()

	t.Run("RevertsAStaleLease", func(t *testing.T) {
		conformance.RunLeaseReclaimerRevertsAStaleLease(t, ctx, fixture)
	})
	t.Run("MintsARevisionThatFencesTheFormerHolder", func(t *testing.T) {
		conformance.RunLeaseReclaimerMintsARevisionThatFencesTheFormerHolder(t, ctx, fixture)
	})
	t.Run("LeavesALiveLeaseAlone", func(t *testing.T) {
		conformance.RunLeaseReclaimerLeavesALiveLeaseAlone(t, ctx, fixture)
	})
	t.Run("HonorsTheGraceWindow", func(t *testing.T) {
		conformance.RunLeaseReclaimerHonorsTheGraceWindow(t, ctx, fixture)
	})
	t.Run("ScopesToTheNamedIDs", func(t *testing.T) {
		conformance.RunLeaseReclaimerScopesToTheNamedIDs(t, ctx, fixture)
	})
	t.Run("AnswersAnAbsentIDWithAnEmptyResult", func(t *testing.T) {
		conformance.RunLeaseReclaimerAnswersAnAbsentIDWithAnEmptyResult(t, ctx, fixture)
	})
	t.Run("ScopesToAssigneesAndLabels", func(t *testing.T) {
		conformance.RunLeaseReclaimerScopesToAssigneesAndLabels(t, ctx, fixture)
	})
	t.Run("AttributesTheRecoveryEvent", func(t *testing.T) {
		conformance.RunLeaseReclaimerAttributesTheRecoveryEvent(t, ctx, fixture)
	})
	t.Run("FiresOnUpdateOncePerReclaimedRow", func(t *testing.T) {
		conformance.RunLeaseReclaimerFiresOnUpdateOncePerReclaimedRow(t, ctx, fixture)
	})
	t.Run("RefusesAMalformedRequest", func(t *testing.T) {
		conformance.RunLeaseReclaimerRefusesAMalformedRequest(t, ctx, fixture)
	})
	t.Run("RefusesABlankScopeEntry", func(t *testing.T) {
		conformance.RunLeaseReclaimerRefusesABlankScopeEntry(t, ctx, fixture)
	})
	t.Run("AcceptsExactlyTheCap", func(t *testing.T) {
		conformance.RunLeaseReclaimerAcceptsExactlyTheCap(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunLeaseReclaimerDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

// newDoltLeaseReclaimerFixture composes the frozen role kit with this
// backend's accessors, plus the same reclaimer taken off a HookFiringStore so
// the hook case observes the decorator a command actually holds.
func newDoltLeaseReclaimerFixture(t *testing.T, prefix string) (conformance.LeaseReclaimerFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	stop := func() {
		cancel()
		storeCleanup()
	}
	fail := func(what string, err error) {
		stop()
		t.Fatalf("%s: %v", what, err)
	}
	reclaimer, err := store.LeaseReclaimer()
	if err != nil {
		fail("LeaseReclaimer()", err)
	}
	claimer, err := store.IssueClaimer()
	if err != nil {
		fail("IssueClaimer()", err)
	}
	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		fail("IssueLifecycle()", err)
	}
	runner, fired := conformance.NewUpdateHookLog(t)
	hooked := reclaimer
	if runner != nil {
		if hooked, err = storage.NewHookFiringStore(store, runner).LeaseReclaimer(); err != nil {
			fail("hooked LeaseReclaimer()", err)
		}
	}
	kit := newDoltRoleFixtureKit(store, prefix)
	return conformance.LeaseReclaimerFixture{
		IssuePrefix:    kit.IssuePrefix,
		LeaseReclaimer: reclaimer,
		CreateIssue:    kit.CreateIssue,
		Claimer:        claimer,
		Lifecycle:      lifecycle,
		QueryScalar:    kit.QueryScalar,
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			for _, stmt := range statements {
				if _, err := store.db.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
					return fmt.Errorf("%s: %w", stmt.Query, err)
				}
			}
			return nil
		},
		HookedLeaseReclaimer: hooked,
		FiredUpdates:         fired,
	}, ctx, stop
}
