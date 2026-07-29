package linear

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	lastPullFileName      = "last_pull"
	DefaultStaleThreshold = 20 * time.Minute
	debounceThreshold     = 5 * time.Minute
)

// WriteLastPullTimestamp writes the current time as ISO 8601 to .beads/last_pull.
func WriteLastPullTimestamp(beadsDir string) error {
	if beadsDir == "" {
		return fmt.Errorf("beadsDir must not be empty")
	}
	path := filepath.Join(beadsDir, lastPullFileName)
	ts := time.Now().UTC().Format(time.RFC3339)
	return os.WriteFile(path, []byte(ts+"\n"), 0600)
}

// ReadLastPullTimestamp reads the last pull timestamp from .beads/last_pull.
// Returns the zero time if the file doesn't exist or is unreadable.
func ReadLastPullTimestamp(beadsDir string) (time.Time, error) {
	if beadsDir == "" {
		return time.Time{}, fmt.Errorf("beadsDir must not be empty")
	}
	path := filepath.Join(beadsDir, lastPullFileName)
	data, err := os.ReadFile(path) // #nosec G304 -- path is constrained to the beads directory.
	if err != nil {
		if os.IsNotExist(err) {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("reading last_pull: %w", err)
	}
	ts := strings.TrimSpace(string(data))
	if ts == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing last_pull timestamp %q: %w", ts, err)
	}
	return t, nil
}

// IsPullStale returns true if the last pull is older than the given threshold,
// or if no pull has ever been recorded.
func IsPullStale(beadsDir string, threshold time.Duration) bool {
	lastPull, err := ReadLastPullTimestamp(beadsDir)
	if err != nil || lastPull.IsZero() {
		return true
	}
	return time.Since(lastPull) > threshold
}

// StalenessInfo holds computed staleness details for display purposes.
type StalenessInfo struct {
	LastPull    time.Time
	Age         time.Duration
	IsFresh     bool
	IsStale     bool
	NeverPulled bool
}

// GetStalenessInfo returns detailed staleness information for display and logic.
func GetStalenessInfo(beadsDir string, threshold time.Duration) StalenessInfo {
	lastPull, err := ReadLastPullTimestamp(beadsDir)
	if err != nil || lastPull.IsZero() {
		return StalenessInfo{NeverPulled: true, IsStale: true}
	}
	age := time.Since(lastPull)
	return StalenessInfo{
		LastPull: lastPull,
		Age:      age,
		IsFresh:  age <= threshold,
		IsStale:  age > threshold,
	}
}

// IsWithinDebounce returns true if the last pull completed within the
// debounce window (5 minutes), preventing agent loops.
func IsWithinDebounce(beadsDir string) bool {
	lastPull, err := ReadLastPullTimestamp(beadsDir)
	if err != nil || lastPull.IsZero() {
		return false
	}
	return time.Since(lastPull) <= debounceThreshold
}

// FormatAge formats a duration as a human-friendly string like "5m" or "2h30m".
func FormatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if mins == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}
