package testutil

import (
	"os"
)

// PinSuiteTempRoot creates a suite-owned temp directory and points
// GOTMPDIR, TMPDIR, TMP, and TEMP at it so testing.T.TempDir (Go 1.24+
// uses GOTMPDIR) and os.MkdirTemp land under a root
// SweepOrphanedTestServers can reap.
//
// Call from TestMain before m.Run(). The caller owns cleanup: sweep
// first, then RemoveAll the returned path. Never pass os.TempDir() as
// a sweep root — that would reap other suites' live servers
// (scripts/test.sh -p N).
func PinSuiteTempRoot(pattern string) (string, error) {
	root, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", err
	}
	for _, key := range []string{"GOTMPDIR", "TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(key, root); err != nil {
			_ = os.RemoveAll(root)
			return "", err
		}
	}
	return root, nil
}
