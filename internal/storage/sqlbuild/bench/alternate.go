package bench

import (
	"fmt"
	"time"
)

// RoundResult is one shape's timing for one round of an alternating run.
type RoundResult struct {
	Shape    string
	Round    int
	Duration time.Duration
}

// RunAlternating runs exec for every shape, strictly interleaved round by
// round (round 1: shape[0], shape[1], ...; round 2: shape[0], shape[1], ...;
// and so on) rather than all rounds of one shape followed by all rounds of
// the next. Strict alternation spreads any environmental drift (cache
// warmth, background load) evenly across shapes instead of letting it favor
// whichever shape happens to run first — the same discipline the PR #5339
// review used (8 alternating rounds) to trust its measurement.
//
// On the first error from exec, RunAlternating stops immediately and returns
// the results gathered so far alongside a wrapped error identifying the
// failing shape and round.
func RunAlternating(rounds int, shapes []Shape, exec func(shape Shape, round int) (time.Duration, error)) ([]RoundResult, error) {
	if len(shapes) == 0 {
		return nil, fmt.Errorf("bench: RunAlternating requires at least one shape")
	}

	var results []RoundResult
	for round := 1; round <= rounds; round++ {
		for _, shape := range shapes {
			d, err := exec(shape, round)
			if err != nil {
				return results, fmt.Errorf("bench: shape %s round %d: %w", shape.Name, round, err)
			}
			results = append(results, RoundResult{Shape: shape.Name, Round: round, Duration: d})
		}
	}
	return results, nil
}
