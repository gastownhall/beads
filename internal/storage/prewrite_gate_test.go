package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

var errMaintenanceHeld = errors.New("maintenance held")

type recordingPreWriteGate struct {
	denyAfter int
	calls     []string
}

func (g *recordingPreWriteGate) BeforeWrite(_ context.Context, operation string) error {
	g.calls = append(g.calls, operation)
	if g.denyAfter > 0 && len(g.calls) >= g.denyAfter {
		return errMaintenanceHeld
	}
	return nil
}

type preWriteTestStore struct {
	DoltStorage
	creates   int
	committed bool
}

func (s *preWriteTestStore) CreateIssue(_ context.Context, _ *types.Issue, _ string) error {
	s.creates++
	return nil
}

func (s *preWriteTestStore) RunInTransaction(ctx context.Context, _ string, fn func(Transaction) error) error {
	staged := 0
	err := fn(&preWriteTestTransaction{staged: &staged})
	if err == nil {
		s.creates += staged
		s.committed = true
	}
	return err
}

type preWriteTestTransaction struct {
	Transaction
	staged *int
}

func (t *preWriteTestTransaction) CreateIssue(_ context.Context, _ *types.Issue, _ string) error {
	*t.staged = *t.staged + 1
	return nil
}

func TestPreWriteGateBlocksDirectCreateBeforeStorage(t *testing.T) {
	inner := &preWriteTestStore{}
	gate := &recordingPreWriteGate{denyAfter: 1}
	store := NewPreWriteGateStore(inner, gate)

	err := store.CreateIssue(t.Context(), &types.Issue{ID: "bd-test"}, "tester")
	if !errors.Is(err, errMaintenanceHeld) {
		t.Fatalf("CreateIssue error = %v, want maintenance rejection", err)
	}
	if inner.creates != 0 {
		t.Fatalf("inner creates = %d, want 0", inner.creates)
	}
	if got := gate.calls; len(got) != 1 || got[0] != PreWriteIssueCreate {
		t.Fatalf("gate calls = %v, want [%s]", got, PreWriteIssueCreate)
	}
}

func TestPreWriteGateRollsBackTransactionWhenLaterWriteIsRejected(t *testing.T) {
	inner := &preWriteTestStore{}
	gate := &recordingPreWriteGate{denyAfter: 2}
	store := NewPreWriteGateStore(inner, gate)

	err := store.RunInTransaction(t.Context(), "test", func(tx Transaction) error {
		if err := tx.CreateIssue(t.Context(), &types.Issue{ID: "bd-one"}, "tester"); err != nil {
			return err
		}
		return tx.CreateIssue(t.Context(), &types.Issue{ID: "bd-two"}, "tester")
	})
	if !errors.Is(err, errMaintenanceHeld) {
		t.Fatalf("RunInTransaction error = %v, want maintenance rejection", err)
	}
	if inner.committed || inner.creates != 0 {
		t.Fatalf("transaction committed=%v creates=%d, want rollback without writes", inner.committed, inner.creates)
	}
}

type lifecycleTestStore struct {
	DoltStorage
	inner *lifecycleTestRole
}

func (s *lifecycleTestStore) IssueLifecycle() (issueops.Lifecycle, error) { return s.inner, nil }

type lifecycleTestRole struct{ creates int }

func (r *lifecycleTestRole) Create(_ context.Context, _ issueops.CreateRequest) (issueops.CreateResult, error) {
	r.creates++
	return issueops.CreateResult{}, nil
}

func (r *lifecycleTestRole) Update(context.Context, issueops.UpdateRequest) (issueops.UpdateResult, error) {
	return issueops.UpdateResult{}, nil
}

func (r *lifecycleTestRole) Close(context.Context, issueops.CloseRequest) (issueops.CloseResult, error) {
	return issueops.CloseResult{}, nil
}

func (r *lifecycleTestRole) Reopen(context.Context, issueops.ReopenRequest) (issueops.ReopenResult, error) {
	return issueops.ReopenResult{}, nil
}

func TestPreWriteGateCoversLifecycleAPI(t *testing.T) {
	role := &lifecycleTestRole{}
	gate := &recordingPreWriteGate{denyAfter: 1}
	store := NewPreWriteGateStore(&lifecycleTestStore{inner: role}, gate)

	lifecycle, err := store.IssueLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	_, err = lifecycle.Create(t.Context(), issueops.CreateRequest{})
	if !errors.Is(err, errMaintenanceHeld) {
		t.Fatalf("Lifecycle.Create error = %v, want maintenance rejection", err)
	}
	if role.creates != 0 {
		t.Fatalf("inner lifecycle create calls = %d, want 0", role.creates)
	}
}
