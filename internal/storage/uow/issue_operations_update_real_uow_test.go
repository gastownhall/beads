package uow

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// Lifecycle over a REAL unit of work, for the promises the IssueOperations
// contract does not hold at this backend. Nothing in this file is a case any
// more; what is left is scaffolding its neighbours reach for.
// newRealIssueOperationsWithProvider is the IssueOperations contract runner's
// own fixture constructor (issue_operations_contract_test.go), and
// readIssueMutationSnapshot and readStoredIssue are read by
// issue_operations_test.go.
//
// WHAT MOVED OUT, and what covers it now:
//
//   - Cross-tier ID collisions on create. RunIssueOperationsCreateRefusesAn-
//     OccupiedID asserts all three directions (durable over durable, durable
//     over wisp, ephemeral over durable) against the raw rows of both tables,
//     which is a superset of what the case here seeded.
//   - Invalid canonical field values refused without a mutation. Every one of
//     its five rows is the same row, with the same value, in
//     TestIssueOperationsRejectsInvalidRequestsBeforeOpeningUOW — which proves
//     something strictly stronger without a database: the request never opens a
//     unit of work at all, so there is no write to look for.
//   - IssuePatch.Owner reaching storage, and a same-value write across the
//     whole public patch surface. RunIssueOperationsUpdateWritesEveryScalar-
//     PatchField does both in one case: it writes all seventeen scalar and
//     pointer fields to values that differ from the seeded ones and reads every
//     column back off the RAW ROW, then restates them and asserts row_lock,
//     updated_at and the event count did not move. The owner case here read
//     only the result issue, and the restatement case here had no write pass,
//     so a field the spec dropped entirely was invisible to both.
//   - An issue_type outside the workspace vocabulary refused on update.
//     RunIssueOperationsUpdateRefusesATypeOutsideTheWorkspaceVocabulary makes
//     the same two claims — a typed refusal over an untouched row, then a
//     configured custom type accepted — against all three backends rather than
//     only the domain use case this one reached.
//   - A claim counting as a mutation when the patch beside it restores the
//     prior state. RunIssueOperationsUpdateClaimIsAMutationWhenThePatch-
//     RestoresTheRow carries BOTH patch shapes, so the claim's own accounting
//     is load-bearing at the two stores as well as here, where the case below
//     could only reach this backend's claimChanged line.
//   - Close provenance surviving every directed persistence move. It lived in
//     closed_by_session_dolt_test.go and asserted the RESULT issue;
//     RunIssueOperationsUpdateClosedFieldsMatchClose walks the same six
//     transitions and reads close_reason and closed_by_session out of whichever
//     plane now holds the row.
//   - A missing id answered with ErrNotFound rather than ErrVersionMismatch.
//     RunLifecycleExpectedVersionIsCheckedBeforeTheNoOps names an id that was
//     never created, with and without a precondition, and asserts the sentinel
//     in both directions. The two stub cases that held it also asserted that no
//     row-level update was ATTEMPTED, and that flag turned out not to catch the
//     reorder it was written for: their own requests carry no ExpectedVersion,
//     so hoisting the version check ahead of the lookup left them green while
//     the contract case went red.

// issueMutationSnapshot is the durable state a case asserts did not move: the
// row version, the timestamp, and how many events the issue carries.
type issueMutationSnapshot struct {
	rowLock   string
	updatedAt string
	events    int
}

func readIssueMutationSnapshot(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, id string, useWisp bool) issueMutationSnapshot {
	t.Helper()
	snapshot, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (issueMutationSnapshot, error) {
		issueTable := "issues"
		eventTable := "events"
		if useWisp {
			issueTable = "wisps"
			eventTable = "wisp_events"
		}
		row, err := uw.RawSQLUseCase().Query(ctx, "SELECT CAST(row_lock AS CHAR), CAST(updated_at AS CHAR) FROM "+issueTable+" WHERE id = ?", id)
		if err != nil {
			return issueMutationSnapshot{}, err
		}
		if len(row.Rows) != 1 || len(row.Rows[0]) != 2 {
			return issueMutationSnapshot{}, fmt.Errorf("unexpected issue snapshot rows: %#v", row.Rows)
		}
		events, err := uw.RawSQLUseCase().Query(ctx, "SELECT COUNT(*) FROM "+eventTable+" WHERE issue_id = ?", id)
		if err != nil {
			return issueMutationSnapshot{}, err
		}
		if len(events.Rows) != 1 || len(events.Rows[0]) != 1 {
			return issueMutationSnapshot{}, fmt.Errorf("unexpected event count rows: %#v", events.Rows)
		}
		count, err := strconv.Atoi(fmt.Sprint(events.Rows[0][0]))
		if err != nil {
			return issueMutationSnapshot{}, fmt.Errorf("parse event count %v: %w", events.Rows[0][0], err)
		}
		return issueMutationSnapshot{
			rowLock:   fmt.Sprint(row.Rows[0][0]),
			updatedAt: fmt.Sprint(row.Rows[0][1]),
			events:    count,
		}, nil
	})
	if err != nil {
		t.Fatalf("read mutation snapshot for %s: %v", id, err)
	}
	return snapshot
}

func newRealIssueOperationsWithProvider(t *testing.T, ctx context.Context) (issueops.Lifecycle, UnitOfWorkProvider) {
	t.Helper()
	provider := newTestUOWProvider(t)
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			return "", err
		}
		return "initialize issue operations", nil
	}); err != nil {
		t.Fatalf("initialize issue operations: %v", err)
	}
	operations, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	return operations, provider
}

func readStoredIssue(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, id string) *types.Issue {
	t.Helper()
	issue, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (*types.Issue, error) {
		return uw.IssueUseCase().GetIssue(ctx, id)
	})
	if err != nil {
		t.Fatalf("read issue %s: %v", id, err)
	}
	return issue
}
