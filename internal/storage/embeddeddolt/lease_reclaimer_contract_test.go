package embeddeddolt_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
)

// TestLeaseReclaimerContract runs the LeaseReclaimer contract against the
// embedded store, which wraps internal/storage/issueops.ExecuteReclaimInTx in
// one connection's transaction and records its version entry after the SQL
// commit.
func TestLeaseReclaimerContract(t *testing.T) {
	skipUnlessEmbeddedDolt(t)
	te := newTestEnv(t, "lrc")
	ctx := t.Context()
	fixture := newEmbeddedLeaseReclaimerFixture(t, te, "lrc")

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

func newEmbeddedLeaseReclaimerFixture(t *testing.T, te *testEnv, prefix string) conformance.LeaseReclaimerFixture {
	t.Helper()
	reclaimer, err := te.store.LeaseReclaimer()
	if err != nil {
		t.Fatalf("LeaseReclaimer(): %v", err)
	}
	claimer, err := te.store.IssueClaimer()
	if err != nil {
		t.Fatalf("IssueClaimer(): %v", err)
	}
	lifecycle, err := te.store.IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle(): %v", err)
	}
	runner, fired := conformance.NewUpdateHookLog(t)
	hooked := reclaimer
	if runner != nil {
		if hooked, err = storage.NewHookFiringStore(te.store, runner).LeaseReclaimer(); err != nil {
			t.Fatalf("hooked LeaseReclaimer(): %v", err)
		}
	}
	kit := newEmbeddedRoleFixtureKit(te, prefix)
	return conformance.LeaseReclaimerFixture{
		IssuePrefix:    kit.IssuePrefix,
		LeaseReclaimer: reclaimer,
		CreateIssue:    kit.CreateIssue,
		Claimer:        claimer,
		Lifecycle:      lifecycle,
		QueryScalar:    kit.QueryScalar,
		Exec: func(ctx context.Context, statements []conformance.SQLStatement) error {
			db, cleanup, err := embeddeddolt.OpenSQL(ctx, te.dataDir, te.database, "main")
			if err != nil {
				return err
			}
			defer func() { _ = cleanup() }()
			for _, stmt := range statements {
				if _, err := db.ExecContext(ctx, stmt.Query, stmt.Args...); err != nil {
					return fmt.Errorf("%s: %w", stmt.Query, err)
				}
			}
			return nil
		},
		HookedLeaseReclaimer: hooked,
		FiredUpdates:         fired,
	}
}
