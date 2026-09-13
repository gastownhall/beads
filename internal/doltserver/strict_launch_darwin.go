//go:build darwin

package doltserver

import "errors"

// SupportsStrictLaunchRecovery is false because Darwin lacks a kernel-bound
// process handle here. Direct handoff refuses before stopping GC rather than
// using verify-then-PID signaling.
func SupportsStrictLaunchRecovery() bool { return false }

func strictLaunchMatches(pid int, options StartOptions, doltDir string) bool { return false }

func strictLaunchCandidates(options StartOptions, doltDir string) ([]int, error) {
	return nil, errors.New("strict launch recovery is unsupported on darwin")
}
