//go:build windows

package doltserver

import "errors"

// SupportsStrictLaunchRecovery reports whether this platform can bind a
// crash-window listener to the durable strict launch intent. Windows has no
// kernel-backed working-directory identity through the existing helpers, so
// the direct handoff refuses before stopping GC rather than claim a weaker
// executable-and-argv proof is sufficient.
func SupportsStrictLaunchRecovery() bool { return false }

func strictLaunchMatches(pid int, options StartOptions, doltDir string) bool {
	return false
}

func strictLaunchCandidates(options StartOptions, doltDir string) ([]int, error) {
	return nil, errors.New("strict launch recovery is unsupported on windows")
}
