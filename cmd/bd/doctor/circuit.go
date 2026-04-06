package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CheckCircuitBreaker checks for stale circuit breaker state files that may
// block all bd operations. Returns a fixable DoctorCheck if stale files exist.
func CheckCircuitBreaker() DoctorCheck {
	dir := "/tmp/beads-circuit"
	pattern := filepath.Join(dir, "beads-dolt-circuit-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return DoctorCheck{
			Name:     "Circuit Breaker",
			Status:   StatusOK,
			Message:  "No stale circuit breaker files",
			Category: CategoryRuntime,
		}
	}

	staleCount := 0
	for _, path := range matches {
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is from filepath.Glob with controlled pattern
		if err != nil {
			continue
		}
		var state struct {
			State     string    `json:"state"`
			TrippedAt time.Time `json:"tripped_at,omitempty"`
			LastFail  time.Time `json:"last_failure,omitempty"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			staleCount++ // corrupt file counts as stale
			continue
		}
		if state.State == "open" || state.State == "half-open" {
			// Only flag as stale if the breaker has been tripped for longer
			// than the staleness TTL (5 minutes). A recently-tripped breaker
			// during a real outage should not be cleared.
			ref := state.TrippedAt
			if ref.IsZero() {
				ref = state.LastFail
			}
			if ref.IsZero() || time.Since(ref) > 5*time.Minute {
				staleCount++
			}
		}
	}

	if staleCount == 0 {
		return DoctorCheck{
			Name:     "Circuit Breaker",
			Status:   StatusOK,
			Message:  "No stale circuit breaker files",
			Category: CategoryRuntime,
		}
	}

	return DoctorCheck{
		Name:     "Circuit Breaker",
		Status:   StatusWarning,
		Message:  fmt.Sprintf("%d stale circuit breaker file(s) found in %s", staleCount, dir),
		Fix:      "Run 'bd doctor --fix' to clear stale circuit breaker files",
		Category: CategoryRuntime,
	}
}
