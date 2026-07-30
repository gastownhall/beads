package issueops

import (
	"errors"
	"testing"
)

// stubSweeps turns a scripted list of per-call results into a run closure for
// DisarmAutoLeasesWith, recording the flip argument of every call so the
// protocol (exactly one flip, first, then sweep-only) is checkable.
func stubSweeps(results []struct {
	n   int64
	err error
}) (func(bool) (int64, error), *[]bool) {
	var flips []bool
	i := 0
	return func(flip bool) (int64, error) {
		flips = append(flips, flip)
		if i >= len(results) {
			// Not a t.Fatalf: the driver is the thing under test, and an
			// over-run is reported by the call-count assertions.
			return 0, errors.New("run called more times than the script allows")
		}
		r := results[i]
		i++
		return r.n, r.err
	}, &flips
}

type sweepResult = struct {
	n   int64
	err error
}

// TestDisarmAutoLeasesWithConverges: the flip sweeps 2, the first re-sweep
// finds 1 more (a claim that read lease.auto before the flip committed), the
// second finds nothing and the driver stops there rather than burning its
// remaining budget.
func TestDisarmAutoLeasesWithConverges(t *testing.T) {
	run, flips := stubSweeps([]sweepResult{{n: 2}, {n: 1}, {n: 0}})

	total, err := DisarmAutoLeasesWith(run)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 (2 + 1 + 0)", total)
	}
	if got := *flips; len(got) != 3 {
		t.Fatalf("run called %d times, want 3 (flip + two sweeps, stopping on the zero)", len(got))
	}
	if !(*flips)[0] || (*flips)[1] || (*flips)[2] {
		t.Errorf("flip sequence = %v, want [true false false] (exactly one flip, first)", *flips)
	}
}

// TestDisarmAutoLeasesWithBoundsResweeps: a store that keeps producing lease
// rows must not spin. The driver runs the flip plus at most three re-sweeps and
// returns what it cleared, even though the last one was nonzero.
func TestDisarmAutoLeasesWithBoundsResweeps(t *testing.T) {
	run, flips := stubSweeps([]sweepResult{{n: 5}, {n: 4}, {n: 3}, {n: 2}, {n: 1}})

	total, err := DisarmAutoLeasesWith(run)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := len(*flips); got != 4 {
		t.Fatalf("run called %d times, want 4 (flip + %d re-sweeps, never a 4th re-sweep)", got, maxDisarmResweeps)
	}
	if total != 14 {
		t.Errorf("total = %d, want 14 (5 + 4 + 3 + 2)", total)
	}
}

// TestDisarmAutoLeasesWithFlipError: nothing is known to have been swept when
// the flip transaction itself fails, so the count is 0 and the error surfaces.
func TestDisarmAutoLeasesWithFlipError(t *testing.T) {
	boom := errors.New("flip failed")
	run, flips := stubSweeps([]sweepResult{{n: 7, err: boom}})

	total, err := DisarmAutoLeasesWith(run)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the flip error", err)
	}
	if total != 0 {
		t.Errorf("total = %d, want 0 (the flip transaction did not land)", total)
	}
	if got := len(*flips); got != 1 {
		t.Errorf("run called %d times, want 1 (no re-sweeps after a failed flip)", got)
	}
}

// TestDisarmAutoLeasesWithResweepError: a re-sweep failing mid-loop must report
// the partial total alongside the error — those rows really were cleared, and a
// caller told "0" would misreport the exposure it just removed.
func TestDisarmAutoLeasesWithResweepError(t *testing.T) {
	boom := errors.New("resweep failed")
	run, flips := stubSweeps([]sweepResult{{n: 6}, {n: 2}, {n: 0, err: boom}})

	total, err := DisarmAutoLeasesWith(run)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the re-sweep error", err)
	}
	if total != 8 {
		t.Errorf("total = %d, want 8 (the 6 + 2 already cleared before the failure)", total)
	}
	if got := len(*flips); got != 3 {
		t.Errorf("run called %d times, want 3 (the loop stops at the failure)", got)
	}
}
