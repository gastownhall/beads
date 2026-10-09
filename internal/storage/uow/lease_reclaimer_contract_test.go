package uow

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// TestLeaseReclaimerContract runs the LeaseReclaimer contract against the
// unit-of-work provider, which reaches internal/storage/issueops.
// ExecuteReclaimInTx through domain.IssueUseCase.Reclaim inside one committing
// unit of work.
func TestLeaseReclaimerContract(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fixture := newUOWLeaseReclaimerFixture(t, ctx, "lrc")

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
	t.Run("AcceptsExactlyTheCap", func(t *testing.T) {
		conformance.RunLeaseReclaimerAcceptsExactlyTheCap(t, ctx, fixture)
	})
	t.Run("DoesNotMutateTheCallerRequest", func(t *testing.T) {
		conformance.RunLeaseReclaimerDoesNotMutateTheCallerRequest(t, ctx, fixture)
	})
}

// newUOWLeaseReclaimerFixture takes every role through the provider's own
// capability accessors, and the hooked reclaimer off the notifying provider
// the proxied CLI route wraps around its provider.
func newUOWLeaseReclaimerFixture(t *testing.T, ctx context.Context, prefix string) conformance.LeaseReclaimerFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	source, ok := provider.(LeaseReclaimerSource)
	if !ok {
		t.Fatalf("provider %T does not offer the LeaseReclaimer accessor", provider)
	}
	reclaimer, err := source.LeaseReclaimer()
	if err != nil {
		t.Fatalf("LeaseReclaimer(): %v", err)
	}
	claimer, err := provider.(IssueClaimerSource).IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	lifecycle, err := provider.(IssueLifecycleSource).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	runner, fired := conformance.NewUpdateHookLog(t)
	hooked := reclaimer
	if runner != nil {
		notifying, ok := NewNotifyingProvider(provider, Sinks{Hook: runner}).(LeaseReclaimerSource)
		if !ok {
			t.Fatal("the notifying provider does not offer the LeaseReclaimer accessor")
		}
		if hooked, err = notifying.LeaseReclaimer(); err != nil {
			t.Fatalf("hooked LeaseReclaimer(): %v", err)
		}
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.LeaseReclaimerFixture{
		IssuePrefix:    kit.IssuePrefix,
		LeaseReclaimer: reclaimer,
		CreateIssue:    kit.CreateIssue,
		Claimer:        claimer,
		Lifecycle:      lifecycle,
		QueryScalar:    kit.QueryScalar,
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				for _, stmt := range statements {
					if _, err := uw.RawSQLUseCase().Exec(ctx, stmt.Query, stmt.Args...); err != nil {
						return "", fmt.Errorf("%s: %w", stmt.Query, err)
					}
				}
				return "seed lease reclaimer rows", nil
			})
		},
		HookedLeaseReclaimer: hooked,
		FiredUpdates:         fired,
	}
}
