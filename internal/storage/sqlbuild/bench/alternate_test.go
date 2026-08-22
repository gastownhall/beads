package bench_test

import (
	"errors"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlbuild/bench"
)

// TestRunAlternating_StrictRoundRobinOrder pins the defining property of
// alternating A/B measurement: shapes must interleave every round (a, b, a,
// b, ...), never run as two back-to-back blocks. Block execution would let
// unrelated system warm-up/cooldown effects masquerade as a shape difference.
func TestRunAlternating_StrictRoundRobinOrder(t *testing.T) {
	var calls []string
	shapes := []bench.Shape{{Name: "a"}, {Name: "b"}}
	exec := func(shape bench.Shape, round int) (time.Duration, error) {
		calls = append(calls, shape.Name)
		return time.Millisecond, nil
	}
	results, err := bench.RunAlternating(3, shapes, exec)
	if err != nil {
		t.Fatalf("RunAlternating: %v", err)
	}
	wantOrder := []string{"a", "b", "a", "b", "a", "b"}
	if len(calls) != len(wantOrder) {
		t.Fatalf("got %d calls, want %d", len(calls), len(wantOrder))
	}
	for i, name := range wantOrder {
		if calls[i] != name {
			t.Errorf("call %d: got shape %q, want %q (order must alternate every round)", i, calls[i], name)
		}
	}
	if len(results) != 6 {
		t.Fatalf("got %d results, want 6", len(results))
	}
}

func TestRunAlternating_RecordsShapeRoundDuration(t *testing.T) {
	shapes := []bench.Shape{{Name: "only"}}
	exec := func(shape bench.Shape, round int) (time.Duration, error) {
		return time.Duration(round) * time.Millisecond, nil
	}
	results, err := bench.RunAlternating(2, shapes, exec)
	if err != nil {
		t.Fatalf("RunAlternating: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for i, r := range results {
		wantRound := i + 1
		if r.Round != wantRound {
			t.Errorf("result %d: Round = %d, want %d", i, r.Round, wantRound)
		}
		if r.Shape != "only" {
			t.Errorf("result %d: Shape = %q, want %q", i, r.Shape, "only")
		}
		if r.Duration != time.Duration(wantRound)*time.Millisecond {
			t.Errorf("result %d: Duration = %v, want %v", i, r.Duration, time.Duration(wantRound)*time.Millisecond)
		}
	}
}

func TestRunAlternating_PropagatesExecError(t *testing.T) {
	shapes := []bench.Shape{{Name: "a"}, {Name: "b"}}
	boom := errors.New("boom")
	calls := 0
	exec := func(shape bench.Shape, round int) (time.Duration, error) {
		calls++
		if shape.Name == "b" && round == 1 {
			return 0, boom
		}
		return time.Millisecond, nil
	}
	_, err := bench.RunAlternating(3, shapes, exec)
	if !errors.Is(err, boom) {
		t.Fatalf("RunAlternating error = %v, want wrapping %v", err, boom)
	}
	if calls != 2 {
		t.Errorf("exec called %d times, want 2 (must abort on first error rather than continuing)", calls)
	}
}

func TestRunAlternating_RejectsEmptyShapes(t *testing.T) {
	_, err := bench.RunAlternating(3, nil, func(bench.Shape, int) (time.Duration, error) {
		return 0, nil
	})
	if err == nil {
		t.Fatal("expected error for empty shapes slice")
	}
}
