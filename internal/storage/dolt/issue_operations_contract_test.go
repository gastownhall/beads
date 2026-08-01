package dolt

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/storage/conformance"
)

func TestIssueOperationsCreateRoutesInfraTypesToWisps(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateRoutesInfraTypesToWisps(t, ctx, fixture)
}

func TestIssueOperationsCreateRejectsMissingDependencyTargets(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsCreateRejectsMissingDependencyTargets(t, ctx, fixture)
}

func TestIssueOperationsUpdateFoldsMetadataIntoOneEvent(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateFoldsMetadataIntoOneEvent(t, ctx, fixture)
}

func TestIssueOperationsUpdateClosePolicy(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateClosePolicy(t, ctx, fixture)
}

func TestIssueOperationsUpdateAssigneeTransferFence(t *testing.T) {
	fixture, ctx, cleanup := newDoltIssueOperationsFixture(t)
	defer cleanup()
	conformance.RunIssueOperationsUpdateAssigneeTransferFence(t, ctx, fixture)
}

func newDoltIssueOperationsFixture(t *testing.T) (conformance.IssueOperationsStagingFixture, context.Context, func()) {
	t.Helper()
	store, storeCleanup := setupTestStore(t)
	ctx, cancel := testContext(t)
	operations, err := NewIssueOperations(store)
	if err != nil {
		cancel()
		storeCleanup()
		t.Fatalf("NewIssueOperations: %v", err)
	}
	fixture := conformance.IssueOperationsStagingFixture{
		IssuePrefix: "test",
		Operations:  operations,
		CreateIssue: store.CreateIssue,
		SetConfig:   store.SetConfig,
		QueryScalar: func(ctx context.Context, query string, args []any, dest ...any) error {
			return store.db.QueryRowContext(ctx, query, args...).Scan(dest...)
		},
	}
	return fixture, ctx, func() {
		cancel()
		storeCleanup()
	}
}
