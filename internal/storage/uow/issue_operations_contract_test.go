package uow

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/beads/backend/conformance"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

func TestIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsCreateRoutesInfraTypesToWisps(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsCreateRejectsMissingDependencyTargets(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsCreateRefusesAnOccupiedID(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsCreateRefusesAnOccupiedID(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsCreateInheritsParentLabels(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsCreateInheritsParentLabels(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsCreateRefusesAForeignIDPrefix(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsCreateRefusesAForeignIDPrefix(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateMetadataPatchOrdersMergeSetUnset(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateClosePolicy(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateClosePolicy(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateAssigneeTransferFence(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateAssigneeTransferFence(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateConditionalGuardsGateOrdinaryEdits(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateConditionalGuardsGateOrdinaryEdits(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateClosedFieldsMatchClose(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateClosedFieldsMatchClose(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateClaimConflictCarriesTheLosingState(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateClaimConflictCarriesTheLosingState(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateClaimHonorsConfiguredActiveStatuses(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateIssuePlaneOnlyRefusesWisps(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateLabelPatchOrdering(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateLabelPatchOrdering(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateLabelPatchValueRules(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateLabelPatchValueRules(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateParentIDReplacesTheParentEdge(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateParentIDReplacesTheParentEdge(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateParentIDReplacesEveryParent(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateParentIDReplacesEveryParent(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateMetadataReplaceClearsAndValidates(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateMetadataReplaceClearsAndValidates(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsRequestValuesAreNotMutated(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsRequestValuesAreNotMutated(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdateProvenanceLabelsHistory(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdateProvenanceLabelsHistory(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func TestIssueOperationsUpdatePersistentPreservesUnversionedClass(t *testing.T) {
	ctx := context.Background()
	conformance.RunIssueOperationsUpdatePersistentPreservesUnversionedClass(t, ctx, newUOWIssueOperationsFixture(t, ctx))
}

func newUOWIssueOperationsFixture(t *testing.T, ctx context.Context) conformance.IssueOperationsStagingFixture {
	t.Helper()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)
	return conformance.IssueOperationsStagingFixture{
		IssuePrefix: "bd",
		Operations:  operations,
		CreateIssue: func(ctx context.Context, issue *types.Issue, actor string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				_, err := uw.IssueUseCase().CreateIssue(ctx, domain.CreateIssueParams{
					Issue:      issue,
					ExplicitID: issue.ID,
					Labels:     append([]string(nil), issue.Labels...),
					CreateOnly: true,
				}, actor)
				return "seed issue", err
			})
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "set " + key, uw.ConfigUseCase().SetConfig(ctx, key, value)
			})
		},
		UpdateRaw: func(ctx context.Context, id string, updates map[string]any, actor string) error {
			return RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
				return "raw update " + id, uw.IssueUseCase().UpdateIssue(ctx, id, updates, actor)
			})
		},
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			row, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) ([]any, error) {
				result, err := uw.RawSQLUseCase().Query(ctx, query, args...)
				if err != nil {
					return nil, err
				}
				if len(result.Rows) != 1 {
					return nil, fmt.Errorf("query %q returned %d rows, want 1", query, len(result.Rows))
				}
				return result.Rows[0], nil
			})
			if err != nil {
				return err
			}
			if len(row) != len(dest) {
				return fmt.Errorf("query %q returned %d columns, want %d", query, len(row), len(dest))
			}
			for i, target := range dest {
				if err := scanRawSQLValue(target, row[i]); err != nil {
					return fmt.Errorf("query %q column %d: %w", query, i, err)
				}
			}
			return nil
		},
	}
}

// scanRawSQLValue copies one raw SQL column into a destination pointer,
// covering the destination types the role conformance contracts scan into.
//
// The set of supported destinations is part of the frozen scaffolding surface
// (bd-kue5t): every uow role wiring reaches its scalar reads through here, so a
// role slice that needs a new destination type routes the addition through a
// follow-up commit against that bead rather than adding a second scanner of its
// own.
//
// A SQL NULL is not a valid source for any of the numeric or boolean
// destinations — it fails loudly instead of decaying to a zero value, because a
// count case reading zero from a NULL is a case that passes for the wrong
// reason. Read a nullable column through COALESCE, or into *string, where NULL
// is the empty string.
func scanRawSQLValue(dest, value any) error {
	switch target := dest.(type) {
	case *int:
		parsed, err := strconv.Atoi(rawSQLString(value))
		if err != nil {
			return fmt.Errorf("scan %v into *int: %w", value, err)
		}
		*target = parsed
	case *int64:
		parsed, err := strconv.ParseInt(rawSQLString(value), 10, 64)
		if err != nil {
			return fmt.Errorf("scan %v into *int64: %w", value, err)
		}
		*target = parsed
	case *bool:
		// Dolt reports a BOOLEAN column as a TINYINT, so the raw value arrives
		// as 1/0 as often as it does as true/false; ParseBool takes both.
		parsed, err := strconv.ParseBool(rawSQLString(value))
		if err != nil {
			return fmt.Errorf("scan %v into *bool: %w", value, err)
		}
		*target = parsed
	case *float64:
		parsed, err := strconv.ParseFloat(rawSQLString(value), 64)
		if err != nil {
			return fmt.Errorf("scan %v into *float64: %w", value, err)
		}
		*target = parsed
	case *time.Time:
		parsed, err := parseRawSQLTime(value)
		if err != nil {
			return fmt.Errorf("scan %v into *time.Time: %w", value, err)
		}
		*target = parsed
	case *string:
		*target = rawSQLString(value)
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
}

// rawSQLTimeLayouts are the shapes a Dolt DATETIME comes back as when the
// driver hands over a string rather than a time.Time — full precision first,
// because a result-mirrors-the-row assertion compares at column precision.
var rawSQLTimeLayouts = []string{
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
}

func parseRawSQLTime(value any) (time.Time, error) {
	if typed, ok := value.(time.Time); ok {
		return typed, nil
	}
	text := rawSQLString(value)
	if text == "" {
		return time.Time{}, fmt.Errorf("empty or NULL timestamp")
	}
	for _, layout := range rawSQLTimeLayouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("no known layout parses %q", text)
}

func rawSQLString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case []byte:
		return string(typed)
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}
