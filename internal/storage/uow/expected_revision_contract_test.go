package uow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage"
	storeops "github.com/steveyegge/beads/internal/storage/issueops"
)

// TestExpectedRevisionContract runs the R16/R17 expected-revision contract
// against the unit-of-work provider, which reaches the same
// internal/storage/issueops.CompareAndSetVersionInTx the two store backends
// wrap — through the domain issue repository (IssueSQLRepository.
// CompareAndSetVersion / CurrentVersion, internal/storage/domain/db/issue.go)
// rather than through a store accessor. UNLIKE MetadataCAS, this role has no
// public issueops interface for the provider to advertise through a Source
// accessor — the conformance contract's own CompareAndSetVersion hook is a
// bare function type (conformance.PerRecordCASWrite) — so the hooks below
// call RunTxResult/RunTxRead directly instead of going through a
// provider.ExpectedRevisionCAS()-style constructor.
//
// So this is the third wrapper over ONE body, not a third vote. What it can
// still catch is this leg's own wrapper: a request field dropped between this
// file and the use case, a commit message composed for a write that was
// refused, a refusal that stops matching errors.Is on the way back up.
//
// One provider for the whole suite (each newUOWRoleFixtureProvider boots a
// real Dolt sql-server) and NO t.Parallel: this backend has no per-test
// copy-on-write branch, so expected_revision_records is database-global and a
// parallel subtest would corrupt another subtest's row.
func TestExpectedRevisionContract(t *testing.T) {
	ctx := context.Background()
	fixture := newUOWExpectedRevisionFixture(t, ctx, "erev")

	t.Run("AcceptsAWriteNamingTheCurrentVersion", func(t *testing.T) {
		conformance.RunExpectedRevisionAcceptsAWriteNamingTheCurrentVersion(t, ctx, fixture)
	})
	t.Run("AcceptsAWriteNamingNoVersion", func(t *testing.T) {
		conformance.RunExpectedRevisionAcceptsAWriteNamingNoVersion(t, ctx, fixture)
	})
	t.Run("CoversFieldsOutsideAnyWatchedSubset", func(t *testing.T) {
		conformance.RunExpectedRevisionCoversFieldsOutsideAnyWatchedSubset(t, ctx, fixture)
	})
	t.Run("RefusalReportsTheRefusingVersionAddress", func(t *testing.T) {
		conformance.RunRefusalReportsTheRefusingVersionAddress(t, ctx, fixture)
	})
	t.Run("RefusalReportsTheRefusingVersionsChangeAttribution", func(t *testing.T) {
		conformance.RunRefusalReportsTheRefusingVersionsChangeAttribution(t, ctx, fixture)
	})
	t.Run("RefusalIsATypedOutcomeNotAGenericError", func(t *testing.T) {
		conformance.RunRefusalIsATypedOutcomeNotAGenericError(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromAnAcceptedWrite", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromAnAcceptedWrite(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromNotFound", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromNotFound(t, ctx, fixture)
	})
	t.Run("RefusalIsDistinguishableFromValidationFailure", func(t *testing.T) {
		conformance.RunRefusalIsDistinguishableFromValidationFailure(t, ctx, fixture)
	})
	t.Run("RefusalNeverSilentlyPicksAWinner", func(t *testing.T) {
		conformance.RunRefusalNeverSilentlyPicksAWinner(t, ctx, fixture)
	})
}

// newUOWExpectedRevisionFixture wires this leg's UnitOfWorkProvider into the
// R16/R17 contract. Unlike newUOWMetadataCASFixture/newUOWCommenterFixture,
// this fixture needs none of roleFixtureKit's CreateIssue/CreateWisp/
// QueryScalar/CountHistory hooks — it has no fields for them — so it does not
// route through newUOWRoleFixtureKit at all, the same reasoning
// newExpectedRevisionDoltFixture gives on the server-backed leg.
func newUOWExpectedRevisionFixture(t *testing.T, ctx context.Context, prefix string) conformance.ExpectedRevisionFixture {
	t.Helper()
	provider := newUOWRoleFixtureProvider(t, ctx, prefix)
	return conformance.ExpectedRevisionFixture{
		IssuePrefix:                   prefix,
		CompareAndSetVersion:          expectedRevisionUOWCompareAndSetVersion(provider),
		CurrentVersion:                expectedRevisionUOWCurrentVersion(provider),
		MutateOutsideExpectedRevision: expectedRevisionUOWMutateOutside(provider),
	}
}

// expectedRevisionUOWCompareAndSetVersion runs R16's whole-of-state
// per-record CAS write inside RunTxResult, the same retrying unit of work
// metadataCAS.CompareAndSetKey uses: Dolt has no row locks, so a concurrent
// writer is caught at commit time and RunTxResult replays the whole body
// against the winner's committed row.
//
// A REFUSAL COMPOSES NO COMMIT MESSAGE, which is RunTxResult's existing
// signal for a unit of work that has nothing to version — the same
// convention metadataCAS.CompareAndSetKey uses for a swap that wrote
// nothing.
func expectedRevisionUOWCompareAndSetVersion(provider UnitOfWorkProvider) conformance.PerRecordCASWrite {
	return func(ctx context.Context, id string, expected *conformance.Address, patch map[string]any) (conformance.ExpectedRevisionResult, error) {
		applicationPatch, attribution := expectedRevisionSplitAttribution(patch)
		plan := storage.CompareAndSetVersionPlan{
			ID:          id,
			Expected:    expectedRevisionAddressPtr(expected),
			Patch:       applicationPatch,
			Attribution: attribution,
		}
		result, err := RunTxResult(ctx, provider, func(ctx context.Context, uw UnitOfWork) (storage.CompareAndSetVersionResult, string, error) {
			r, err := uw.IssueUseCase().CompareAndSetVersion(ctx, plan)
			if err != nil || !r.Accepted {
				return r, "", err
			}
			return r, fmt.Sprintf("bd: compare-and-set version %s", plan.ID), nil
		})
		if err != nil {
			return conformance.ExpectedRevisionResult{}, expectedRevisionTranslateErr(err)
		}
		return expectedRevisionResultToConformance(result), nil
	}
}

// expectedRevisionUOWCurrentVersion reports the Address currently current for
// id via RunTxRead: a plain read, not retried, the unit-of-work analog of
// DoltStore.withReadTx / EmbeddedDoltStore.withConn(ctx, false, ...).
func expectedRevisionUOWCurrentVersion(provider UnitOfWorkProvider) func(ctx context.Context, id string) (conformance.Address, error) {
	return func(ctx context.Context, id string) (conformance.Address, error) {
		address, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			return uw.IssueUseCase().CurrentVersion(ctx, id)
		})
		if err != nil {
			return "", expectedRevisionTranslateErr(err)
		}
		return conformance.Address(address), nil
	}
}

// expectedRevisionUOWMutateOutside writes through storeops directly against
// the unit of work's own runner rather than through any domain/use-case
// method: R16-c's whole-of-state probe needs a path that is NOT gated by the
// compare-expected check (backend/conformance's own doc comment), and no
// such path belongs on the production API surface. It reaches the runner the
// same way commenter_contract_test.go's SeedCommentAt hook does — a
// *baseUOW type assertion, since Tx.Runner() is unexported outside this
// package and no domain/use-case method exists for an out-of-band write —
// and db.Runner satisfies issueops.DBTX directly (internal/storage/issueops.
// blocked_state.go's doc comment), so no adapter is needed.
func expectedRevisionUOWMutateOutside(provider UnitOfWorkProvider) func(ctx context.Context, id string, field, value string) error {
	return func(ctx context.Context, id string, field, value string) error {
		return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
			base, ok := uw.(*baseUOW)
			if !ok {
				return "", fmt.Errorf("mutate outside expected revision: unit of work %T does not expose the runner MutateExpectedRevisionFieldInTx needs", uw)
			}
			if err := storeops.MutateExpectedRevisionFieldInTx(ctx, base.tx.Runner(), id, field, value); err != nil {
				return "", err
			}
			return fmt.Sprintf("test: mutate %s outside expected revision", id), nil
		})
	}
}

// expectedRevisionSplitAttribution separates the "_attribution" convention
// key (expectedRevisionAttributionPatch, backend/conformance) from the
// application fields a whole-of-state patch actually persists: attribution
// is tracked in expected_revision_records' own change_actor/change_agent/
// change_message/change_at_nanos columns, never as a literal durable_state
// field. The caller's map is never mutated.
func expectedRevisionSplitAttribution(patch map[string]any) (map[string]any, storage.ExpectedRevisionAttribution) {
	applicationPatch := make(map[string]any, len(patch))
	var attribution storage.ExpectedRevisionAttribution
	for k, v := range patch {
		if k == "_attribution" {
			if a, ok := v.(conformance.ChangeAttribution); ok {
				attribution = storage.ExpectedRevisionAttribution{
					Actor:   a.Actor,
					Agent:   a.Agent,
					Message: a.Message,
					At:      a.At,
				}
			}
			continue
		}
		applicationPatch[k] = v
	}
	return applicationPatch, attribution
}

func expectedRevisionAddressPtr(a *conformance.Address) *string {
	if a == nil {
		return nil
	}
	s := string(*a)
	return &s
}

func expectedRevisionResultToConformance(r storage.CompareAndSetVersionResult) conformance.ExpectedRevisionResult {
	result := conformance.ExpectedRevisionResult{
		Accepted:   r.Accepted,
		NewVersion: conformance.Address(r.NewVersion),
	}
	if r.Refusal != nil {
		result.Refusal = &conformance.Refusal{
			RefusingVersion: conformance.Address(r.Refusal.RefusingVersion),
			Attribution: conformance.ChangeAttribution{
				Actor:   r.Refusal.Attribution.Actor,
				Agent:   r.Refusal.Attribution.Agent,
				Message: r.Refusal.Attribution.Message,
				At:      r.Refusal.Attribution.At,
			},
		}
	}
	return result
}

// expectedRevisionTranslateErr re-wraps this leg's storeops sentinels as the
// conformance package's — structurally distinct errors.New values with
// matching semantics, so a bare pass-through would fail the suite's
// errors.Is checks (RunRefusalIsDistinguishableFromNotFound,
// RunRefusalIsDistinguishableFromValidationFailure).
func expectedRevisionTranslateErr(err error) error {
	switch {
	case errors.Is(err, storeops.ErrExpectedRevisionNotFound):
		return fmt.Errorf("%w: %v", conformance.ErrRevisionNotFound, err)
	case errors.Is(err, storeops.ErrExpectedRevisionValidation):
		return fmt.Errorf("%w: %v", conformance.ErrRevisionValidation, err)
	default:
		return err
	}
}
