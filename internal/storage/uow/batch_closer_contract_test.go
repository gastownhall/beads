package uow

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
)

// The unit-of-work backend's wiring of the BatchCloser contract, and the only
// one of the three that runs a genuinely separate body: the two store backends
// share internal/storage/issueops.ExecuteCloseBatch, while this one closes each
// item through the domain use cases inside one RunTxResult.
//
// ONE PROVIDER FOR THE WHOLE SUITE, subtests namespaced by IssuePrefix, because
// each provider boots a real Dolt sql-server. NO t.Parallel: this backend has no
// per-test copy-on-write branch, so dolt_log is database-global and a parallel
// subtest would move another subtest's history delta. The configured issue
// prefix is "bc" and every subtest prefix extends it, so the explicit ids the
// contract seeds pass this backend's prefix validation.
func TestBatchCloserContract(t *testing.T) {
	ctx := context.Background()
	provider := newUOWRoleFixtureProvider(t, ctx, "bc")

	for _, test := range []struct {
		name   string
		prefix string
		run    func(*testing.T, context.Context, conformance.BatchCloserFixture)
	}{
		{name: "OutcomesMirrorItemsIndexForIndex", prefix: "bc-mirror", run: conformance.RunBatchCloserOutcomesMirrorItemsIndexForIndex},
		{name: "PerItemRefusalIsAResultAndSurvivorsCommit", prefix: "bc-refusal", run: conformance.RunBatchCloserPerItemRefusalIsAResultAndSurvivorsCommit},
		{name: "OutcomeSnapshotIsTheDocumentedShape", prefix: "bc-shape", run: conformance.RunBatchCloserOutcomeSnapshotIsTheDocumentedShape},
		{name: "RequestValidationReturnsZeroResultAndChangesNothing", prefix: "bc-valid", run: conformance.RunBatchCloserRequestValidationReturnsZeroResultAndChangesNothing},
		{name: "ClaimFilterValueFailureIsARequestValidationFailure", prefix: "bc-badsort", run: conformance.RunBatchCloserClaimFilterValueFailureIsARequestValidationFailure},
		{name: "BackendFailureReturnsNoOutcomes", prefix: "bc-deadctx", run: conformance.RunBatchCloserBackendFailureReturnsNoOutcomes},
		{name: "IdempotentRecloseIsAPerItemSuccess", prefix: "bc-reclose", run: conformance.RunBatchCloserIdempotentRecloseIsAPerItemSuccess},
		{name: "AllIdempotentBatchLandsNothing", prefix: "bc-allidem", run: conformance.RunBatchCloserAllIdempotentBatchLandsNothing},
		{name: "DuplicateItemRecloseAtItsOwnIndex", prefix: "bc-dup", run: conformance.RunBatchCloserDuplicateItemRecloseAtItsOwnIndex},
		{name: "WispItemClosesAndEarnsTheClaim", prefix: "bc-wispclaim", run: conformance.RunBatchCloserWispItemClosesAndEarnsTheClaim},
		{name: "DurableHistoryNeverNamesAWisp", prefix: "bc-wisphistory", run: conformance.RunBatchCloserDurableHistoryNeverNamesAWisp},
		{name: "ForceBypassesOnlyClosePolicy", prefix: "bc-force", run: conformance.RunBatchCloserForceBypassesOnlyClosePolicy},
		{name: "ClaimNextHydratesWhenSomethingClosed", prefix: "bc-claimhit", run: conformance.RunBatchCloserClaimNextHydratesWhenSomethingClosed},
		{name: "ClaimNextIsNilWhenNothingClosed", prefix: "bc-claimnone", run: conformance.RunBatchCloserClaimNextIsNilWhenNothingClosed},
		{name: "ClaimNextIsNilWhenTheFrontIsEmpty", prefix: "bc-claimempty", run: conformance.RunBatchCloserClaimNextIsNilWhenTheFrontIsEmpty},
		{name: "ClaimNextSeesAnUnblockingFromItsOwnBatch", prefix: "bc-claimunblock", run: conformance.RunBatchCloserClaimNextSeesAnUnblockingFromItsOwnBatch},
		{name: "RecordsOneHistoryEntryForWhatLanded", prefix: "bc-history", run: conformance.RunBatchCloserRecordsOneHistoryEntryForWhatLanded},
		{name: "AllRefusedBatchRecordsNoHistory", prefix: "bc-nohistory", run: conformance.RunBatchCloserAllRefusedBatchRecordsNoHistory},
		{name: "DoesNotMutateTheCallerRequest", prefix: "bc-snapshot", run: conformance.RunBatchCloserDoesNotMutateTheCallerRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, ctx, newUOWBatchCloserFixture(t, provider, test.prefix))
		})
	}
}

func newUOWBatchCloserFixture(t *testing.T, provider UnitOfWorkProvider, prefix string) conformance.BatchCloserFixture {
	t.Helper()
	source, ok := provider.(BatchCloserSource)
	if !ok {
		t.Fatalf("provider %T does not offer the BatchCloser accessor", provider)
	}
	closer, err := source.BatchCloser()
	if err != nil {
		t.Fatalf("BatchCloser(): %v", err)
	}
	kit := newUOWRoleFixtureKit(provider, prefix)
	return conformance.BatchCloserFixture{
		IssuePrefix:   kit.IssuePrefix,
		Closer:        closer,
		CreateIssue:   kit.CreateIssue,
		CreateWisp:    kit.CreateWisp,
		AddDependency: kit.AddDependency,
		// Local, because the shared kit exposes no comment hook. Folding it in
		// is an S0 follow-up (bd-kue5t); editing the frozen kit from this slice
		// would collide with the five running beside it.
		AddComment: func(ctx context.Context, issueID, actor, text string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				_, err := uw.CommentUseCase().AddCommentToIssue(ctx, issueID, actor, text)
				return "seed comment on " + issueID, err
			})
		},
		QueryScalar:  kit.QueryScalar,
		CountHistory: kit.CountHistory,
	}
}
