package uow

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

var errPreWriteHeld = errors.New("maintenance held")

type preWriteTestGate struct{ calls int }

func (g *preWriteTestGate) BeforeWrite(context.Context, string) error {
	g.calls++
	return errPreWriteHeld
}

type preWriteTestProvider struct{ unit *preWriteTestUOW }

func (p *preWriteTestProvider) NewUOW(context.Context) (UnitOfWork, error) { return p.unit, nil }
func (*preWriteTestProvider) Close(context.Context) error                  { return nil }

type preWriteTestUOW struct {
	UnitOfWork
	commits int
}

func (u *preWriteTestUOW) Commit(context.Context, string) error {
	u.commits++
	return nil
}

func TestPreWriteProviderRejectsCommitBeforePersistence(t *testing.T) {
	inner := &preWriteTestUOW{}
	gate := &preWriteTestGate{}
	provider := NewPreWriteProvider(&preWriteTestProvider{unit: inner}, gate)

	unit, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = unit.Commit(t.Context(), "bd: update issue")
	if !errors.Is(err, errPreWriteHeld) {
		t.Fatalf("Commit error = %v, want pre-write rejection", err)
	}
	if inner.commits != 0 {
		t.Fatalf("inner commits = %d, want 0", inner.commits)
	}
	if gate.calls != 1 {
		t.Fatalf("gate calls = %d, want 1", gate.calls)
	}
}

func TestPreWriteProviderDoesNotGateReadOnlyRollback(t *testing.T) {
	inner := &preWriteTestUOW{}
	gate := &preWriteTestGate{}
	provider := NewPreWriteProvider(&preWriteTestProvider{unit: inner}, gate)

	unit, err := provider.NewUOW(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := unit.Commit(t.Context(), ""); err != nil {
		t.Fatalf("empty Commit: %v", err)
	}
	if inner.commits != 1 {
		t.Fatalf("inner commits = %d, want 1", inner.commits)
	}
	if gate.calls != 0 {
		t.Fatalf("gate calls = %d, want 0", gate.calls)
	}
}

var _ storage.PreWriteGate = (*preWriteTestGate)(nil)
