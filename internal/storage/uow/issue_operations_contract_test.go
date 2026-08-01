package uow

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/storage/conformance"
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
// covering the destination types the issue-operations contract scans into.
func scanRawSQLValue(dest, value any) error {
	switch target := dest.(type) {
	case *int:
		parsed, err := strconv.Atoi(rawSQLString(value))
		if err != nil {
			return fmt.Errorf("scan %v into *int: %w", value, err)
		}
		*target = parsed
	case *string:
		*target = rawSQLString(value)
	default:
		return fmt.Errorf("unsupported scan destination %T", dest)
	}
	return nil
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
