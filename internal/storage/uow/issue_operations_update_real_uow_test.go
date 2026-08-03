package uow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestIssueOperationsOwnerPatchPersists(t *testing.T) {
	ctx := context.Background()
	operations := newRealIssueOperations(t, ctx)

	created, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:        "bd-owner-patch",
			Title:     "owner patch",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	updated, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor:   "tester",
		IssueID: created.Issue.ID,
		Patch: issueops.IssuePatch{
			Owner: issueops.Field[string]{Set: true, Value: "updated-owner"},
		},
	})
	if err != nil {
		t.Fatalf("Update(owner) error = %v", err)
	}
	if !updated.Changed {
		t.Fatal("Update(owner).Changed = false, want true")
	}
	if updated.Issue == nil || updated.Issue.Owner != "updated-owner" {
		t.Fatalf("Update(owner).Issue = %#v, want updated-owner", updated.Issue)
	}
}

func TestIssueOperationsRejectsInvalidCanonicalUpdatesWithoutMutation(t *testing.T) {
	ctx := context.Background()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)
	created, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:        "bd-invalid-update",
			Title:     "valid incumbent",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	negativeEstimate := -1
	checks := []struct {
		name  string
		patch issueops.IssuePatch
	}{
		{"empty title", issueops.IssuePatch{Title: issueops.Field[string]{Set: true}}},
		{"overlong title", issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: strings.Repeat("x", 501)}}},
		{"negative priority", issueops.IssuePatch{Priority: issueops.Field[int]{Set: true, Value: -1}}},
		{"priority above maximum", issueops.IssuePatch{Priority: issueops.Field[int]{Set: true, Value: 5}}},
		{"negative estimated minutes", issueops.IssuePatch{EstimatedMinutes: issueops.Field[*int]{Set: true, Value: &negativeEstimate}}},
	}

	before := readIssueMutationSnapshot(t, ctx, provider, created.Issue.ID, false)
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			_, err := operations.Update(ctx, issueops.UpdateRequest{
				Actor:   "tester",
				IssueID: created.Issue.ID,
				Patch:   check.patch,
			})
			if !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("Update() error = %v, want ErrValidation", err)
			}
			after := readIssueMutationSnapshot(t, ctx, provider, created.Issue.ID, false)
			if after != before {
				t.Fatalf("invalid update changed durable state: before=%+v after=%+v", before, after)
			}
			stored := readStoredIssue(t, ctx, provider, created.Issue.ID)
			if stored.Title != "valid incumbent" || stored.Priority != 2 || stored.EstimatedMinutes != nil {
				t.Fatalf("invalid update persisted candidate state: %#v", stored)
			}
		})
	}
}

func TestIssueOperationsCreateRejectsCrossTierIDCollisions(t *testing.T) {
	ctx := context.Background()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)

	durable, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:        "bd-cross-tier-durable",
			Title:     "durable incumbent",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	})
	if err != nil {
		t.Fatalf("Create(durable incumbent) error = %v", err)
	}
	durableBefore := readIssueMutationSnapshot(t, ctx, provider, durable.Issue.ID, false)

	_, err = operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:        durable.Issue.ID,
			Title:     "wisp replacement",
			IssueType: types.TypeTask,
			Priority:  2,
			Ephemeral: true,
		},
	})
	if !errors.Is(err, issueops.ErrAlreadyExists) {
		t.Fatalf("Create(wisp collision) error = %v, want ErrAlreadyExists", err)
	}
	durableAfter := readIssueMutationSnapshot(t, ctx, provider, durable.Issue.ID, false)
	if durableAfter != durableBefore {
		t.Fatalf("wisp collision changed durable incumbent: before=%+v after=%+v", durableBefore, durableAfter)
	}
	storedDurable := readStoredIssue(t, ctx, provider, durable.Issue.ID)
	if storedDurable.Title != "durable incumbent" {
		t.Fatalf("durable incumbent title = %q, want unchanged", storedDurable.Title)
	}
	assertStoredIssueMissing(t, ctx, provider, durable.Issue.ID, true)

	wisp, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:        "bd-cross-tier-wisp",
			Title:     "wisp incumbent",
			IssueType: types.TypeTask,
			Priority:  2,
			Ephemeral: true,
		},
	})
	if err != nil {
		t.Fatalf("Create(wisp incumbent) error = %v", err)
	}
	wispBefore := readIssueMutationSnapshot(t, ctx, provider, wisp.Issue.ID, true)

	_, err = operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:        wisp.Issue.ID,
			Title:     "durable replacement",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	})
	if !errors.Is(err, issueops.ErrAlreadyExists) {
		t.Fatalf("Create(durable collision) error = %v, want ErrAlreadyExists", err)
	}
	wispAfter := readIssueMutationSnapshot(t, ctx, provider, wisp.Issue.ID, true)
	if wispAfter != wispBefore {
		t.Fatalf("durable collision changed wisp incumbent: before=%+v after=%+v", wispBefore, wispAfter)
	}
	storedWisp := readStoredWisp(t, ctx, provider, wisp.Issue.ID)
	if storedWisp.Title != "wisp incumbent" {
		t.Fatalf("wisp incumbent title = %q, want unchanged", storedWisp.Title)
	}
	assertStoredIssueMissing(t, ctx, provider, wisp.Issue.ID, false)
}

func TestIssueOperationsTypedIssueTypeUsesConfiguredTypes(t *testing.T) {
	ctx := context.Background()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)

	created, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:        "bd-typed-update",
			Title:     "type patch",
			IssueType: types.TypeTask,
			Priority:  2,
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	beforeInvalidType := readIssueMutationSnapshot(t, ctx, provider, created.Issue.ID, false)

	_, err = operations.Update(ctx, issueops.UpdateRequest{
		Actor:   "tester",
		IssueID: created.Issue.ID,
		Patch: issueops.IssuePatch{
			IssueType: issueops.Field[issueops.IssueType]{Set: true, Value: issueops.IssueType("not-configured")},
		},
	})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("Update(invalid typed issue type) error = %v, want ErrValidation", err)
	}
	stored := readStoredIssue(t, ctx, provider, created.Issue.ID)
	if stored.IssueType != types.TypeTask || stored.RowVersion != created.Issue.RowVersion {
		t.Fatalf("invalid IssueType persisted: %#v", stored)
	}
	afterInvalidType := readIssueMutationSnapshot(t, ctx, provider, created.Issue.ID, false)
	if afterInvalidType != beforeInvalidType {
		t.Fatalf("invalid IssueType changed persisted state: before=%+v after=%+v", beforeInvalidType, afterInvalidType)
	}

	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, "types.custom", "research"); err != nil {
			return "", err
		}
		return "configure custom type", nil
	}); err != nil {
		t.Fatalf("configure custom type: %v", err)
	}
	updated, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor:   "tester",
		IssueID: created.Issue.ID,
		Patch: issueops.IssuePatch{
			IssueType: issueops.Field[issueops.IssueType]{Set: true, Value: issueops.IssueType("research")},
		},
	})
	if err != nil {
		t.Fatalf("Update(custom typed issue type) error = %v", err)
	}
	if updated.Issue == nil || updated.Issue.IssueType != issueops.IssueType("research") {
		t.Fatalf("Update(custom typed issue type).Issue = %#v, want research", updated.Issue)
	}
}

func TestIssueOperationsSamePublicFieldsLeaveRowAndEventsUntouched(t *testing.T) {
	ctx := context.Background()
	operations, provider := newRealIssueOperationsWithProvider(t, ctx)
	minutes := 45
	externalRef := "external-noop"
	dueAt := time.Date(2031, time.March, 2, 3, 4, 5, 0, time.UTC)
	deferUntil := time.Date(2031, time.March, 1, 3, 4, 5, 0, time.UTC)
	created, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:                 "bd-unchanged-update",
			Title:              "unchanged title",
			Description:        "unchanged description",
			Design:             "unchanged design",
			AcceptanceCriteria: "unchanged acceptance",
			Notes:              "unchanged notes",
			SpecID:             "spec-noop",
			AwaitID:            "await-noop",
			Status:             issueops.StatusOpen,
			Priority:           2,
			IssueType:          types.TypeTask,
			Owner:              "unchanged-owner",
			ClosedBySession:    "session-noop",
			EstimatedMinutes:   &minutes,
			ExternalRef:        &externalRef,
			DueAt:              &dueAt,
			DeferUntil:         &deferUntil,
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	before := readIssueMutationSnapshot(t, ctx, provider, created.Issue.ID, false)

	updated, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor:   "tester",
		IssueID: created.Issue.ID,
		Patch: issueops.IssuePatch{
			Title:              issueops.Field[string]{Set: true, Value: "unchanged title"},
			Description:        issueops.Field[string]{Set: true, Value: "unchanged description"},
			Design:             issueops.Field[string]{Set: true, Value: "unchanged design"},
			AcceptanceCriteria: issueops.Field[string]{Set: true, Value: "unchanged acceptance"},
			Notes:              issueops.Field[string]{Set: true, Value: "unchanged notes"},
			SpecID:             issueops.Field[string]{Set: true, Value: "spec-noop"},
			AwaitID:            issueops.Field[string]{Set: true, Value: "await-noop"},
			Status:             issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusOpen},
			Priority:           issueops.Field[int]{Set: true, Value: 2},
			IssueType:          issueops.Field[issueops.IssueType]{Set: true, Value: types.TypeTask},
			Assignee:           issueops.Field[string]{Set: true, Value: ""},
			Owner:              issueops.Field[string]{Set: true, Value: "unchanged-owner"},
			ClosedBySession:    issueops.Field[string]{Set: true, Value: "session-noop"},
			EstimatedMinutes:   issueops.Field[*int]{Set: true, Value: &minutes},
			ExternalRef:        issueops.Field[*string]{Set: true, Value: &externalRef},
			DueAt:              issueops.Field[*time.Time]{Set: true, Value: &dueAt},
			DeferUntil:         issueops.Field[*time.Time]{Set: true, Value: &deferUntil},
		},
	})
	if err != nil {
		t.Fatalf("Update(same public fields) error = %v", err)
	}
	if updated.Changed {
		t.Fatalf("Update(same public fields).Changed = true, want false: %#v", updated.Issue)
	}
	after := readIssueMutationSnapshot(t, ctx, provider, created.Issue.ID, false)
	if after != before {
		t.Fatalf("same public fields changed durable state: before=%+v after=%+v", before, after)
	}
}

func TestIssueOperationsClaimMutationCountsWhenPatchRestoresPriorState(t *testing.T) {
	ctx := context.Background()
	operations := newRealIssueOperations(t, ctx)
	startedAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	created, err := operations.Create(ctx, issueops.CreateRequest{
		Actor: "tester",
		Issue: &issueops.Issue{
			ID:        "bd-claim-restore",
			Title:     "claim then restore",
			IssueType: types.TypeTask,
			Priority:  2,
			StartedAt: &startedAt,
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	beforeRestore, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor:   "tester",
		IssueID: created.Issue.ID,
		Patch: issueops.IssuePatch{
			Title: issueops.Field[string]{Set: true, Value: "claim then restore"},
		},
	})
	if err != nil {
		t.Fatalf("Update(same title before claim) error = %v", err)
	}
	if beforeRestore.Changed {
		t.Fatalf("Update(same title before claim).Changed = true, want false: %#v", beforeRestore.Issue)
	}

	restored, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor:   "tester",
		IssueID: created.Issue.ID,
		Claim:   true,
		Patch: issueops.IssuePatch{
			Status:   issueops.Field[issueops.Status]{Set: true, Value: issueops.StatusOpen},
			Assignee: issueops.Field[string]{Set: true, Value: ""},
		},
	})
	if err != nil {
		t.Fatalf("Update(claim then restore) error = %v", err)
	}
	if !restored.Changed {
		t.Fatal("Update(claim then restore).Changed = false, want true")
	}
	if restored.Issue == nil || restored.Issue.Status != issueops.StatusOpen || restored.Issue.Assignee != "" {
		t.Fatalf("Update(claim then restore).Issue = %#v, want open and unassigned", restored.Issue)
	}
	if !semanticIssueEqual(beforeRestore.Issue, restored.Issue) {
		t.Fatalf("claim then restore did not restore the public state: before=%#v after=%#v", beforeRestore.Issue, restored.Issue)
	}

	claimed, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "tester", IssueID: created.Issue.ID, Claim: true})
	if err != nil {
		t.Fatalf("Update(claim) error = %v", err)
	}
	if !claimed.Changed {
		t.Fatal("Update(claim).Changed = false, want true")
	}
	alreadyClaimed, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "tester", IssueID: created.Issue.ID, Claim: true})
	if err != nil {
		t.Fatalf("Update(already claimed) error = %v", err)
	}
	if alreadyClaimed.Changed {
		t.Fatalf("Update(already claimed).Changed = true, want false: %#v", alreadyClaimed.Issue)
	}
}

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

func newRealIssueOperations(t *testing.T, ctx context.Context) issueops.Lifecycle {
	t.Helper()
	operations, _ := newRealIssueOperationsWithProvider(t, ctx)
	return operations
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

func readStoredWisp(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, id string) *types.Issue {
	t.Helper()
	issue, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (*types.Issue, error) {
		return uw.IssueUseCase().GetWisp(ctx, id)
	})
	if err != nil {
		t.Fatalf("read wisp %s: %v", id, err)
	}
	return issue
}

func assertStoredIssueMissing(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, id string, useWisp bool) {
	t.Helper()
	_, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (*types.Issue, error) {
		if useWisp {
			return uw.IssueUseCase().GetWisp(ctx, id)
		}
		return uw.IssueUseCase().GetIssue(ctx, id)
	})
	if !dberrors.IsNoRows(err) {
		t.Fatalf("read missing issue %s (wisp=%t) error = %v, want no rows", id, useWisp, err)
	}
}
