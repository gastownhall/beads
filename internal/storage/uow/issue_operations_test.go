package uow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dberrors"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

func TestIssueOperationsRejectsInvalidRequestsBeforeOpeningUOW(t *testing.T) {
	provider := &mockUnitOfWorkProvider{newUOWErr: errors.New("unexpected unit-of-work open")}
	operations, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	negativeEstimate := -1
	checks := []struct {
		name string
		call func() error
	}{
		{"create missing actor", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{Issue: &issueops.Issue{}})
			return err
		}},
		{"create nil issue", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{Actor: "a"})
			return err
		}},
		{"create embedded relations", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{Actor: "a", Issue: &issueops.Issue{Comments: []*types.Comment{{Text: "no"}}}})
			return err
		}},
		{"create duplicate dependency", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{Actor: "a", Issue: &issueops.Issue{Title: "x"}, ParentID: "bd-parent", Dependencies: []issueops.CreateDependency{{TargetID: "bd-parent", Type: types.DepParentChild}}})
			return err
		}},
		{"create overlong label", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{Actor: "a", Issue: &issueops.Issue{Title: "x", Labels: []string{strings.Repeat("x", types.MaxFieldLen+1)}}})
			return err
		}},
		{"create malformed dependency metadata", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{
				Actor:        "a",
				Issue:        &issueops.Issue{Title: "x"},
				Dependencies: []issueops.CreateDependency{{TargetID: "bd-target", Type: types.DepRelated, Metadata: "{"}},
			})
			return err
		}},
		{"create overlong dependency thread", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{
				Actor:        "a",
				Issue:        &issueops.Issue{Title: "x"},
				Dependencies: []issueops.CreateDependency{{TargetID: "bd-target", Type: types.DepRelated, ThreadID: strings.Repeat("t", types.MaxFieldLen+1)}},
			})
			return err
		}},
		{"create overlong parent ID", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{
				Actor:    "a",
				Issue:    &issueops.Issue{Title: "x"},
				ParentID: strings.Repeat("p", types.MaxFieldLen+1),
			})
			return err
		}},
		{"create overlong dependency target ID", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{
				Actor:        "a",
				Issue:        &issueops.Issue{Title: "x"},
				Dependencies: []issueops.CreateDependency{{TargetID: strings.Repeat("d", types.MaxFieldLen+1), Type: types.DepRelated}},
			})
			return err
		}},
		{"create overlong waits-for spawner ID", func() error {
			_, err := operations.Create(context.Background(), issueops.CreateRequest{
				Actor:    "a",
				Issue:    &issueops.Issue{Title: "x"},
				WaitsFor: &issueops.WaitsFor{SpawnerID: strings.Repeat("w", types.MaxFieldLen+1)},
			})
			return err
		}},
		{"update missing ID", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{Actor: "a"})
			return err
		}},
		{"update empty title", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{
				Actor: "a", IssueID: "bd-1",
				Patch: issueops.IssuePatch{Title: issueops.Field[string]{Set: true}},
			})
			return err
		}},
		{"update overlong title", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{
				Actor: "a", IssueID: "bd-1",
				Patch: issueops.IssuePatch{Title: issueops.Field[string]{Set: true, Value: strings.Repeat("x", 501)}},
			})
			return err
		}},
		{"update negative priority", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{
				Actor: "a", IssueID: "bd-1",
				Patch: issueops.IssuePatch{Priority: issueops.Field[int]{Set: true, Value: -1}},
			})
			return err
		}},
		{"update priority above maximum", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{
				Actor: "a", IssueID: "bd-1",
				Patch: issueops.IssuePatch{Priority: issueops.Field[int]{Set: true, Value: 5}},
			})
			return err
		}},
		{"update negative estimated minutes", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{
				Actor: "a", IssueID: "bd-1",
				Patch: issueops.IssuePatch{EstimatedMinutes: issueops.Field[*int]{Set: true, Value: &negativeEstimate}},
			})
			return err
		}},
		{"close missing ID", func() error {
			_, err := operations.Close(context.Background(), issueops.CloseRequest{Actor: "a"})
			return err
		}},
		{"reopen missing actor", func() error {
			_, err := operations.Reopen(context.Background(), issueops.ReopenRequest{IssueID: "bd-1"})
			return err
		}},
		{"invalid claim guards", func() error {
			expected := issueops.StatusOpen
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{Actor: "a", IssueID: "bd-1", Claim: true, ExpectedStatus: &expected})
			return err
		}},
		{"invalid force", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{Actor: "a", IssueID: "bd-1", ForceAssigneeTransfer: true})
			return err
		}},
		{"invalid persistence", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{Actor: "a", IssueID: "bd-1", Patch: issueops.IssuePatch{Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: "bad"}}})
			return err
		}},
		{"metadata replacement combined", func() error {
			_, err := operations.Update(context.Background(), issueops.UpdateRequest{Actor: "a", IssueID: "bd-1", Patch: issueops.IssuePatch{Metadata: issueops.MetadataPatch{Replace: issueops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`{}`)}, Unset: []string{"x"}}}})
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); !errors.Is(err, issueops.ErrValidation) {
				t.Fatalf("error = %v, want ErrValidation", err)
			}
		})
	}
	if provider.newUOWCalls != 0 {
		t.Fatalf("NewUOW calls = %d, want 0", provider.newUOWCalls)
	}
}

func TestNewIssueOperationsReturnsPublicOperations(t *testing.T) {
	operations, err := NewIssueOperations(&mockUnitOfWorkProvider{})
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	if operations == nil {
		t.Fatal("NewIssueOperations() returned nil")
	}
}

func TestNewIssueOperationsRejectsTypedNilProvider(t *testing.T) {
	var provider *mockUnitOfWorkProvider
	operations, err := NewIssueOperations(provider)
	if err == nil {
		t.Fatal("NewIssueOperations() error = nil, want typed-nil provider error")
	}
	if operations != nil {
		t.Fatalf("NewIssueOperations() operations = %T, want nil", operations)
	}
}

// TestIssueOperationsLifecycleWithRealUnitOfWork is one issue's whole life
// through one real unit-of-work provider, and what it is for after the
// IssueOperations and LifecycleCloseReopen contracts took the per-verb
// semantics is the SEQUENCE: state each step leaves for the next one, which no
// contract case assembles.
//
// What only this test still holds: a durable row claimed and demoted to the
// wisp plane in ONE update, carrying its labels and its dependency across; a
// foreign claim refused on that WISP row (every contract claim-conflict case is
// on the durable plane); the same-persistence no-op and the promotion back; a
// dependency's Metadata and ThreadID surviving create hydration; and a create
// that fails on a missing dependency target leaving no residue in ANY of the
// five tables it would have written, with the caller's own values still
// pristine — the contract's create-refusal cases count the issue row only.
func TestIssueOperationsLifecycleWithRealUnitOfWork(t *testing.T) {
	ctx := context.Background()
	provider := newTestUOWProvider(t)
	if err := RunTx(ctx, provider, func(ctx context.Context, uw UnitOfWork) (string, error) {
		if err := uw.ConfigUseCase().SetConfig(ctx, "issue_prefix", "bd"); err != nil {
			return "", err
		}
		return "initialize lifecycle fixture", nil
	}); err != nil {
		t.Fatalf("initialize lifecycle fixture: %v", err)
	}
	operations, err := NewIssueOperations(provider)
	if err != nil {
		t.Fatalf("NewIssueOperations() error = %v", err)
	}
	target, err := operations.Create(ctx, issueops.CreateRequest{Actor: "tester", Issue: &issueops.Issue{ID: "bd-life-target", Title: "target", IssueType: types.TypeTask, Priority: 2}})
	if err != nil {
		t.Fatalf("Create(target) error = %v", err)
	}
	if target.Issue == nil {
		t.Fatal("Create(target) returned nil issue")
	}
	importedIssue := &issueops.Issue{
		ID:        "bd-life-main",
		Title:     "main",
		IssueType: types.TypeTask,
		Priority:  2,
		Labels:    []string{"imported-label"},
	}
	importedDependency := issueops.CreateDependency{
		TargetID: target.Issue.ID,
		Type:     types.DepRelated,
		Metadata: `{"origin":"import"}`,
		ThreadID: "import-thread",
	}
	created, err := operations.Create(ctx, issueops.CreateRequest{
		Actor:        "tester",
		Issue:        importedIssue,
		Dependencies: []issueops.CreateDependency{importedDependency},
	})
	if err != nil {
		t.Fatalf("Create(main) error = %v", err)
	}
	if len(created.Issue.Labels) != 1 || created.Issue.Labels[0] != "imported-label" {
		t.Fatalf("Create(main) labels = %#v, want imported label", created.Issue.Labels)
	}
	if len(created.Issue.Dependencies) != 1 ||
		created.Issue.Dependencies[0].Metadata != `{"origin":"import"}` ||
		created.Issue.Dependencies[0].ThreadID != "import-thread" {
		t.Fatalf("Create(main) hydration = %#v", created.Issue)
	}
	if importedIssue.Labels[0] != "imported-label" ||
		importedDependency.Metadata != `{"origin":"import"}` {
		t.Fatalf("Create(main) mutated caller input: issue=%#v dependency=%#v", importedIssue, importedDependency)
	}

	// The three compare-and-set refusals that used to sit here — a stale
	// --if-assignee, a stale --if-status, and a stale ExpectedVersion on a
	// claim — are RunLifecycleUpdateConditionalGuardsGateOrdinaryEdits
	// and the stale-version arm of RunLifecycleUpdateClosePolicy, which
	// run at this backend and assert the refusal against the raw row and an
	// event-count delta rather than against a snapshot comparison. Because they
	// wrote nothing, created.Issue.RowVersion below is still the version the
	// create returned.

	updated, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor:           "tester",
		IssueID:         created.Issue.ID,
		Claim:           true,
		ExpectedVersion: &created.Issue.RowVersion,
		Patch: issueops.IssuePatch{
			Title:       issueops.Field[string]{Set: true, Value: "claimed and moved"},
			Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: issueops.PersistenceModeEphemeral},
		},
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !updated.Changed ||
		!updated.Issue.Ephemeral ||
		updated.Issue.Assignee != "tester" ||
		len(updated.Issue.Labels) != 1 ||
		len(updated.Issue.Dependencies) != 1 {
		t.Fatalf("Update() = %#v", updated)
	}
	beforeForeignClaim := readIssueMutationSnapshot(t, ctx, provider, updated.Issue.ID, true)
	if _, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "other", IssueID: updated.Issue.ID, Claim: true}); !errors.Is(err, issueops.ErrAlreadyClaimed) {
		t.Fatalf("Update(foreign claim) error = %v, want ErrAlreadyClaimed", err)
	}
	afterForeignClaim := readIssueMutationSnapshot(t, ctx, provider, updated.Issue.ID, true)
	if afterForeignClaim != beforeForeignClaim {
		t.Fatalf("foreign claim changed wisp state: before=%+v after=%+v", beforeForeignClaim, afterForeignClaim)
	}
	samePersistence, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor:   "tester",
		IssueID: updated.Issue.ID,
		Patch: issueops.IssuePatch{
			Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: issueops.PersistenceModeEphemeral},
		},
	})
	if err != nil || samePersistence.Changed {
		t.Fatalf("Update(same persistence) = %#v, %v; want unchanged", samePersistence, err)
	}
	promoted, err := operations.Update(ctx, issueops.UpdateRequest{
		Actor:   "tester",
		IssueID: updated.Issue.ID,
		Patch: issueops.IssuePatch{
			Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: issueops.PersistenceModePersistent},
		},
	})
	if err != nil || !promoted.Changed || promoted.Issue.Ephemeral || promoted.Issue.NoHistory {
		t.Fatalf("Update(promote persistence) = %#v, %v", promoted, err)
	}
	if stored := readStoredIssue(t, ctx, provider, promoted.Issue.ID); stored.ID != promoted.Issue.ID {
		t.Fatalf("promoted durable issue = %#v", stored)
	}
	parent, err := operations.Create(ctx, issueops.CreateRequest{Actor: "tester", Issue: &issueops.Issue{ID: "bd-life-parent", Title: "parent", IssueType: types.TypeEpic, Priority: 2}})
	if err != nil {
		t.Fatalf("Create(parent) error = %v", err)
	}
	child, err := operations.Create(ctx, issueops.CreateRequest{Actor: "tester", Issue: &issueops.Issue{ID: "bd-life-parent.1", Title: "child", IssueType: types.TypeTask, Priority: 2}, ParentID: parent.Issue.ID})
	if err != nil {
		t.Fatalf("Create(child) error = %v", err)
	}
	_, err = operations.Close(ctx, issueops.CloseRequest{Actor: "tester", IssueID: parent.Issue.ID, ExpectedVersion: &parent.Issue.RowVersion})
	if !errors.Is(err, issueops.ErrCloseOpenChildren) {
		t.Fatalf("Close(parent with open child) error = %v, want ErrCloseOpenChildren", err)
	}
	closedChild, err := operations.Close(ctx, issueops.CloseRequest{Actor: "tester", IssueID: child.Issue.ID, Session: "session-life-child", ExpectedVersion: &child.Issue.RowVersion})
	if err != nil {
		t.Fatalf("Close(child) error = %v", err)
	}
	if closedChild.Issue.ClosedBySession != "session-life-child" {
		t.Fatalf("Close(child) ClosedBySession = %q, want session-life-child", closedChild.Issue.ClosedBySession)
	}
	movedClosedChild, err := operations.Update(ctx, issueops.UpdateRequest{Actor: "tester", IssueID: closedChild.Issue.ID, Patch: issueops.IssuePatch{Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: issueops.PersistenceModeEphemeral}}})
	if err != nil {
		t.Fatalf("Update(closed child persistence) error = %v", err)
	}
	if movedClosedChild.Issue.ClosedBySession != "session-life-child" {
		t.Fatalf("Update(closed child persistence) ClosedBySession = %q, want session-life-child", movedClosedChild.Issue.ClosedBySession)
	}
	closedParent, err := operations.Close(ctx, issueops.CloseRequest{Actor: "tester", IssueID: parent.Issue.ID, ExpectedVersion: &parent.Issue.RowVersion})
	if err != nil || !closedParent.Changed {
		t.Fatalf("Close(parent) = %#v, %v", closedParent, err)
	}
	staleVersion := closedParent.Issue.RowVersion + 1
	_, err = operations.Reopen(ctx, issueops.ReopenRequest{Actor: "tester", IssueID: parent.Issue.ID, ExpectedVersion: &staleVersion})
	if !errors.Is(err, issueops.ErrVersionMismatch) {
		t.Fatalf("Reopen(stale) error = %v, want ErrVersionMismatch", err)
	}
	reopened, err := operations.Reopen(ctx, issueops.ReopenRequest{Actor: "tester", IssueID: parent.Issue.ID, ExpectedVersion: &closedParent.Issue.RowVersion})
	if err != nil || !reopened.Changed {
		t.Fatalf("Reopen() = %#v, %v", reopened, err)
	}
	noOp, err := operations.Reopen(ctx, issueops.ReopenRequest{Actor: "tester", IssueID: parent.Issue.ID, ExpectedVersion: &reopened.Issue.RowVersion})
	if err != nil || noOp.Changed {
		t.Fatalf("Reopen(no-op) = %#v, %v", noOp, err)
	}
	rollbackIssue := &issueops.Issue{
		ID:        "bd-life-rollback",
		Title:     "rollback",
		IssueType: types.TypeTask,
		Priority:  2,
		Labels:    []string{"rollback-label"},
	}
	rollbackDependency := issueops.CreateDependency{
		TargetID: "bd-life-missing",
		Type:     types.DepRelated,
		Metadata: `{"rollback":true}`,
		ThreadID: "rollback-thread",
	}
	_, err = operations.Create(ctx, issueops.CreateRequest{
		Actor:        "tester",
		Issue:        rollbackIssue,
		Dependencies: []issueops.CreateDependency{rollbackDependency},
	})
	if err == nil {
		t.Fatal("Create(rollback) error = nil, want dependency failure")
	}
	if rollbackIssue.Labels[0] != "rollback-label" ||
		rollbackDependency.Metadata != `{"rollback":true}` ||
		rollbackDependency.ThreadID != "rollback-thread" {
		t.Fatalf("Create(rollback) mutated caller input: issue=%#v dependency=%#v", rollbackIssue, rollbackDependency)
	}
	_, err = RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
		_, err := uw.IssueUseCase().GetIssue(ctx, "bd-life-rollback")
		return struct{}{}, err
	})
	if !dberrors.IsNoRows(err) {
		t.Fatalf("rollback issue lookup error = %v, want no rows", err)
	}
	assertNoDurableIssueRows(t, ctx, provider, rollbackIssue.ID)
}

func assertNoDurableIssueRows(t *testing.T, ctx context.Context, provider UnitOfWorkProvider, id string) {
	t.Helper()
	_, err := RunTxRead(ctx, provider, func(ctx context.Context, uw UnitOfWork) (struct{}, error) {
		for _, query := range []string{
			"SELECT COUNT(*) FROM issues WHERE id = ?",
			"SELECT COUNT(*) FROM labels WHERE issue_id = ?",
			"SELECT COUNT(*) FROM comments WHERE issue_id = ?",
			"SELECT COUNT(*) FROM dependencies WHERE issue_id = ?",
			"SELECT COUNT(*) FROM events WHERE issue_id = ?",
		} {
			result, err := uw.RawSQLUseCase().Query(ctx, query, id)
			if err != nil {
				return struct{}{}, err
			}
			if len(result.Rows) != 1 || len(result.Rows[0]) != 1 || fmt.Sprint(result.Rows[0][0]) != "0" {
				return struct{}{}, fmt.Errorf("rollback residue for %q: query %q returned %#v", id, query, result.Rows)
			}
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("assert rollback cleanup for %s: %v", id, err)
	}
}

type operationIssueUseCaseStub struct {
	domain.IssueUseCase
	getIssue func(context.Context, string) (*types.Issue, error)
	getWisp  func(context.Context, string) (*types.Issue, error)
}

func (s operationIssueUseCaseStub) GetIssue(ctx context.Context, id string) (*types.Issue, error) {
	return s.getIssue(ctx, id)
}
func (s operationIssueUseCaseStub) GetWisp(ctx context.Context, id string) (*types.Issue, error) {
	return s.getWisp(ctx, id)
}

func TestOperationIssueFallsBackOnlyAfterNotFoundWispRead(t *testing.T) {
	durableCalls := 0
	uw := &mockUnitOfWork{issueUseCase: operationIssueUseCaseStub{
		getWisp: func(context.Context, string) (*types.Issue, error) {
			return nil, storage.ErrNotFound
		},
		getIssue: func(context.Context, string) (*types.Issue, error) {
			durableCalls++
			return &types.Issue{ID: "bd-durable"}, nil
		},
	}}

	issue, isWisp, err := operationIssue(context.Background(), uw, "bd-durable", false)
	if err != nil {
		t.Fatalf("operationIssue() error = %v", err)
	}
	if isWisp || issue.ID != "bd-durable" || durableCalls != 1 {
		t.Fatalf("operationIssue() = (%#v, %t), durable calls = %d", issue, isWisp, durableCalls)
	}
}

func TestOperationIssueFallsBackToDurableAfterNoRowsWispRead(t *testing.T) {
	durableCalls := 0
	uw := &mockUnitOfWork{issueUseCase: operationIssueUseCaseStub{
		getWisp: func(context.Context, string) (*types.Issue, error) {
			return nil, sql.ErrNoRows
		},
		getIssue: func(context.Context, string) (*types.Issue, error) {
			durableCalls++
			return &types.Issue{ID: "bd-durable"}, nil
		},
	}}

	issue, isWisp, err := operationIssue(context.Background(), uw, "bd-durable", false)
	if err != nil {
		t.Fatalf("operationIssue() error = %v", err)
	}
	if isWisp || issue.ID != "bd-durable" || durableCalls != 1 {
		t.Fatalf("operationIssue() = (%#v, %t), durable calls = %d", issue, isWisp, durableCalls)
	}
}

func TestOperationIssueBothMissingMatchesPublicNotFound(t *testing.T) {
	uw := &mockUnitOfWork{issueUseCase: operationIssueUseCaseStub{
		getWisp:  func(context.Context, string) (*types.Issue, error) { return nil, sql.ErrNoRows },
		getIssue: func(context.Context, string) (*types.Issue, error) { return nil, sql.ErrNoRows },
	}}

	_, _, err := operationIssue(context.Background(), uw, "bd-missing", false)
	if !errors.Is(err, issueops.ErrNotFound) {
		t.Fatalf("operationIssue() error = %v, want ErrNotFound", err)
	}
}

func TestOperationIssuePropagatesWispReadFailure(t *testing.T) {
	wispReadErr := errors.New("wisp read unavailable")
	durableCalls := 0
	uw := &mockUnitOfWork{issueUseCase: operationIssueUseCaseStub{
		getWisp: func(context.Context, string) (*types.Issue, error) {
			return nil, wispReadErr
		},
		getIssue: func(context.Context, string) (*types.Issue, error) {
			durableCalls++
			return &types.Issue{ID: "bd-durable"}, nil
		},
	}}

	_, _, err := operationIssue(context.Background(), uw, "bd-durable", false)
	if !errors.Is(err, wispReadErr) {
		t.Fatalf("operationIssue() error = %v, want wisp read error", err)
	}
	if durableCalls != 0 {
		t.Fatalf("durable reads = %d, want 0", durableCalls)
	}
}

func TestUpdateSpecClassifiesMetadataValidationAndClearsReplacement(t *testing.T) {
	clearing, err := updateSpec(issueops.UpdateRequest{Patch: issueops.IssuePatch{
		Metadata: issueops.MetadataPatch{Replace: issueops.Field[json.RawMessage]{Set: true}},
	}})
	if err != nil {
		t.Fatalf("updateSpec() clear replacement error = %v", err)
	}
	if got := string(clearing.Fields["metadata"].(json.RawMessage)); got != "{}" {
		t.Fatalf("clear metadata = %q, want {}", got)
	}

	_, err = updateSpec(issueops.UpdateRequest{Patch: issueops.IssuePatch{
		Metadata: issueops.MetadataPatch{Merge: issueops.Field[json.RawMessage]{Set: true, Value: json.RawMessage("[]")}},
	}})
	if !errors.Is(err, issueops.ErrValidation) {
		t.Fatalf("merge array error = %v, want ErrValidation", err)
	}
}

func TestUpdateSpecLowersPersistenceMode(t *testing.T) {
	spec, err := updateSpec(issueops.UpdateRequest{Patch: issueops.IssuePatch{
		Persistence: issueops.Field[issueops.PersistenceMode]{Set: true, Value: issueops.PersistenceModeEphemeral},
	}})
	if err != nil {
		t.Fatalf("updateSpec() error = %v", err)
	}
	if spec.Persistence == nil || *spec.Persistence != types.PersistenceModeEphemeral {
		t.Fatalf("updateSpec().Persistence = %v, want ephemeral", spec.Persistence)
	}
}
