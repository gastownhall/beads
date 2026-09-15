//go:build darwin

package doltserver

import "errors"

// SupportsStrictLaunchRecovery is false because Darwin lacks a kernel-bound
// process handle here. Callers are expected to refuse up front rather than
// fall back to verify-then-PID signaling, which cannot rule out PID reuse.
func SupportsStrictLaunchRecovery() bool { return false }

func strictLaunchMatches(pid int, options StartOptions, doltDir string) bool { return false }

func strictLaunchCandidates(options StartOptions, doltDir string) ([]int, error) {
	return nil, errors.New("strict launch recovery is unsupported on darwin")
}
