package storage

import (
	"context"
	"errors"
	"testing"
)

type localRecomputeStore struct {
	DoltStorage
	changed int
	err     error
	calls   int
}

func (s *localRecomputeStore) RecomputeAllBlocked(context.Context) (int, error) {
	s.calls++
	return s.changed, s.err
}

// countingRecomputeStore offers both repairs. RecomputeBlocked must ask the
// counting one, or the abstained count is lost on the way to the command.
type countingRecomputeStore struct {
	localRecomputeStore
	counts     BlockedRecomputeCounts
	countCalls int
}

func (s *countingRecomputeStore) RecomputeAllBlockedCounts(context.Context) (BlockedRecomputeCounts, error) {
	s.countCalls++
	return s.counts, s.err
}

type serverMaintainedStore struct {
	localRecomputeStore
}

func (*serverMaintainedStore) BlockedStateMaintainedByServer() bool { return true }

type plainStore struct{ DoltStorage }

func TestRecomputeBlockedRunsTheLocalRepair(t *testing.T) {
	s := &localRecomputeStore{changed: 3}
	got, err := RecomputeBlocked(context.Background(), s)
	if err != nil || got != (RecomputeBlockedResult{RowsCorrected: 3}) || s.calls != 1 {
		t.Fatalf("RecomputeBlocked = (%+v, %v) after %d repairs, want ({3 0 \"\"}, nil) after 1", got, err, s.calls)
	}
	s.err = errors.New("dirty graph")
	if _, err := RecomputeBlocked(context.Background(), s); !errors.Is(err, s.err) {
		t.Fatalf("RecomputeBlocked error = %v, want the repair's own error", err)
	}
}

func TestRecomputeBlockedCarriesTheAbstainedCount(t *testing.T) {
	s := &countingRecomputeStore{counts: BlockedRecomputeCounts{Corrected: 2, Abstained: 5}}
	got, err := RecomputeBlocked(context.Background(), s)
	if err != nil || got != (RecomputeBlockedResult{RowsCorrected: 2, RowsAbstained: 5}) {
		t.Fatalf("RecomputeBlocked = (%+v, %v), want ({2 5 \"\"}, nil)", got, err)
	}
	if s.countCalls != 1 || s.calls != 0 {
		t.Fatalf("repairs run: counting %d, plain %d; want 1 and 0 (one repair, through the counting interface)", s.countCalls, s.calls)
	}
	s.err = errors.New("dirty graph")
	if _, err := RecomputeBlocked(context.Background(), s); !errors.Is(err, s.err) {
		t.Fatalf("RecomputeBlocked error = %v, want the repair's own error", err)
	}
}

// TestRecomputeBlockedAnswersServerMaintainedWithoutRepairing pins S25: a store
// whose server maintains the column recomputes nothing, even if it ALSO offers
// a local repair, and says who maintains it.
func TestRecomputeBlockedAnswersServerMaintainedWithoutRepairing(t *testing.T) {
	s := &serverMaintainedStore{localRecomputeStore{changed: 7}}
	got, err := RecomputeBlocked(context.Background(), s)
	if err != nil || got != (RecomputeBlockedResult{MaintainedBy: BlockedMaintainedByServer}) {
		t.Fatalf("RecomputeBlocked = (%+v, %v), want ({0 0 server}, nil)", got, err)
	}
	if s.calls != 0 {
		t.Fatalf("the local repair ran %d times for a server-maintained store, want 0", s.calls)
	}
}

func TestRecomputeBlockedRefusesAStoreWithNeither(t *testing.T) {
	_, err := RecomputeBlocked(context.Background(), plainStore{})
	var unsup *ErrUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("RecomputeBlocked error = %v, want *ErrUnsupported", err)
	}
}
