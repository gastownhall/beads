package uow

import (
	"context"

	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
)

// checkLabelVocabularyForGuardedWrite is this backend's half of the bda-yxac
// chokepoint: the dolt and embedded stores enforce labels.vocabulary inside
// ExecuteCreate/ExecuteUpdate/ExecuteCreateBatch, and the uow backend's
// guarded verbs (Create, Update, CreateBatch, BatchApply) enforce it here,
// through the same pure predicate (issueops.CheckLabelVocabularyAgainst).
//
// It sits at the GUARDED VERBS, not inside the domain use case, deliberately:
// direct IssueUseCase callers - formula/cook instantiation, `bd mol port`'s
// wisp-to-durable port, import - move labels that are ALREADY workspace state
// and must never be refused by a vocabulary configured after they were
// written. Enforcement is a property of the role contract every front door
// (CLI, HTTP, MCP) reaches, and the conformance suite pins all backends to it.
//
// Fails open on any read error, matching the store-side guard.
func checkLabelVocabularyForGuardedWrite(ctx context.Context, uw UnitOfWork, candidates []string) error {
	if len(candidates) == 0 {
		return nil
	}
	mode, err := uw.ConfigUseCase().GetConfig(ctx, storageissueops.LabelsVocabularyConfigKey)
	if err != nil || mode != storageissueops.LabelsVocabularyEnforce {
		return nil
	}
	defs, err := uw.LabelVocabularyUseCase().List(ctx)
	if err != nil {
		return nil
	}
	return storageissueops.CheckLabelVocabularyAgainst(defs, candidates)
}
