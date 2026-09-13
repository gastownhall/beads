//go:build !linux && !darwin && !windows

package doltserver

import "errors"

// SupportsStrictLaunchRecovery is false where this platform cannot positively
// bind a crash-window listener to its durable strict launch intent.
func SupportsStrictLaunchRecovery() bool { return false }

// strictLaunchMatches refuses recovery on platforms without the required
// process identity evidence. Callers are expected to check
// SupportsStrictLaunchRecovery before they mutate anything, so this refusal
// cannot strand an operation that was already underway.
func strictLaunchMatches(pid int, options StartOptions, doltDir string) bool {
	return false
}

func strictLaunchCandidates(options StartOptions, doltDir string) ([]int, error) {
	return nil, errors.New("strict launch recovery is unsupported on this platform")
}
