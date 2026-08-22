package bench_test

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage/sqlbuild/bench"
)

func TestSummarize_ComputesPerShapeStats(t *testing.T) {
	results := []bench.RoundResult{
		{Shape: "a", Round: 1, Duration: 10 * time.Millisecond},
		{Shape: "b", Round: 1, Duration: 30 * time.Millisecond},
		{Shape: "a", Round: 2, Duration: 20 * time.Millisecond},
		{Shape: "b", Round: 2, Duration: 34 * time.Millisecond},
		{Shape: "a", Round: 3, Duration: 30 * time.Millisecond},
		{Shape: "b", Round: 3, Duration: 32 * time.Millisecond},
	}
	stats := bench.Summarize(results)
	if len(stats) != 2 {
		t.Fatalf("got %d shape stats, want 2", len(stats))
	}
	byName := make(map[string]bench.ShapeStats, len(stats))
	for _, s := range stats {
		byName[s.Shape] = s
	}

	a, ok := byName["a"]
	if !ok {
		t.Fatal("missing stats for shape a")
	}
	if a.N != 3 {
		t.Errorf("a.N = %d, want 3", a.N)
	}
	if a.Min != 10*time.Millisecond {
		t.Errorf("a.Min = %v, want 10ms", a.Min)
	}
	if a.Max != 30*time.Millisecond {
		t.Errorf("a.Max = %v, want 30ms", a.Max)
	}
	if a.Mean != 20*time.Millisecond {
		t.Errorf("a.Mean = %v, want 20ms", a.Mean)
	}
	if a.Spread != 20*time.Millisecond {
		t.Errorf("a.Spread = %v, want 20ms (Max-Min)", a.Spread)
	}

	b, ok := byName["b"]
	if !ok {
		t.Fatal("missing stats for shape b")
	}
	if b.Min != 30*time.Millisecond || b.Max != 34*time.Millisecond {
		t.Errorf("b.Min/Max = %v/%v, want 30ms/34ms", b.Min, b.Max)
	}
}

func TestSummarize_EmptyInput(t *testing.T) {
	stats := bench.Summarize(nil)
	if len(stats) != 0 {
		t.Fatalf("got %d stats for empty input, want 0", len(stats))
	}
}

func TestSummarize_SingleSample_ZeroSpread(t *testing.T) {
	stats := bench.Summarize([]bench.RoundResult{
		{Shape: "solo", Round: 1, Duration: 5 * time.Millisecond},
	})
	if len(stats) != 1 {
		t.Fatalf("got %d stats, want 1", len(stats))
	}
	if stats[0].Spread != 0 {
		t.Errorf("single-sample Spread = %v, want 0", stats[0].Spread)
	}
}
