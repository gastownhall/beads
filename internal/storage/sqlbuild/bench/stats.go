package bench

import "time"

// ShapeStats summarizes one shape's timings across an alternating run.
type ShapeStats struct {
	Shape  string
	N      int
	Min    time.Duration
	Max    time.Duration
	Mean   time.Duration
	Spread time.Duration // Max - Min
}

// Summarize groups results by Shape, in first-appearance order, and computes
// per-shape N/Min/Max/Mean/Spread. Spread (not just mean) is what the PR
// #5339 review actually distrusted about a single-round comparison — a shape
// with a wide spread needs more rounds before its mean means anything.
func Summarize(results []RoundResult) []ShapeStats {
	order := make([]string, 0)
	byShape := make(map[string][]time.Duration)
	for _, r := range results {
		if _, seen := byShape[r.Shape]; !seen {
			order = append(order, r.Shape)
		}
		byShape[r.Shape] = append(byShape[r.Shape], r.Duration)
	}

	stats := make([]ShapeStats, 0, len(order))
	for _, shape := range order {
		durations := byShape[shape]
		st := ShapeStats{Shape: shape, N: len(durations)}
		var sum time.Duration
		for i, d := range durations {
			sum += d
			if i == 0 || d < st.Min {
				st.Min = d
			}
			if i == 0 || d > st.Max {
				st.Max = d
			}
		}
		st.Mean = sum / time.Duration(len(durations))
		st.Spread = st.Max - st.Min
		stats = append(stats, st)
	}
	return stats
}
