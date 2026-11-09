package issueops

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/types"
)

// ValidateIssueForInsert reports the error the batch writer would refuse issue
// with, without changing it. It runs PrepareIssueForInsert, the writer's own
// normalization and validation, on a copy, and adds the label length check
// AddLabelInTx applies to each label, so an import can set aside a record the
// writer is certain to refuse before it writes anything (GH#4492).
//
// It covers only what the record itself decides. Database state (an id
// conflict, a retired id, a concurrent update) is still the writer's, and the
// writer validates again: configuration can change between a screen and the
// write.
func ValidateIssueForInsert(issue *types.Issue, customStatuses, customTypes []string) error {
	if issue == nil {
		return fmt.Errorf("issue is nil")
	}
	// PrepareIssueForInsert assigns fields and replaces timestamp pointers but
	// never writes through one, so a shallow copy keeps the caller's issue as
	// it was.
	probe := *issue
	if err := PrepareIssueForInsert(&probe, customStatuses, customTypes); err != nil {
		return err
	}
	for _, label := range issue.Labels {
		if err := types.CheckFieldLen("label", label); err != nil {
			return fmt.Errorf("validation failed for issue %s: %w", issue.ID, err)
		}
	}
	return nil
}

// ReadImportVocabularyInTx reads the custom statuses and custom types the
// batch writer validates against, through the same resolvers NewBatchContext
// uses, and returns their errors as they are.
func ReadImportVocabularyInTx(ctx context.Context, tx DBTX) (customStatuses, customTypes []string, err error) {
	customStatuses, err = GetCustomStatusesTx(ctx, tx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get custom statuses: %w", err)
	}
	customTypes, err = ResolveCustomTypesInTx(ctx, tx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get custom types: %w", err)
	}
	return customStatuses, customTypes, nil
}
