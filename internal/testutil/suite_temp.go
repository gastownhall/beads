package testutil

import (
	"os"
	"path/filepath"
	"strings"
)

// PinSuiteTempRoot creates a suite-owned temp directory and points
// GOTMPDIR, TMPDIR, TMP, and TEMP at it so testing.T.TempDir (Go 1.26+
// uses GOTMPDIR) and os.MkdirTemp land under a root
// SweepSuiteTestServers can reap.
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

// PathUnderSuiteRoot reports whether dir is root or nested under it,
// comparing symlink-resolved paths (macOS hands out /var paths that resolve
// to /private/var).
//
// It backs the TestTempDirLandsUnderSuiteSweepRoot guard that every suite
// pinning a root with PinSuiteTempRoot installs: if t.TempDir() ever stops
// landing under the pinned root, the post-run sweep silently loses its scope
// and leaked sql-servers become unreapable again. The guard lives with each
// suite (it needs that suite's package-level root), but this predicate is
// shared, because it is the piece most likely to need a synchronized fix
// later and per-suite copies drift silently (wy-j2zc8q).
func PathUnderSuiteRoot(dir, root string) bool {
	dir = evalOrSelf(dir)
	root = evalOrSelf(root)
	if dir == root {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(dir, strings.TrimRight(root, sep)+sep)
}

// evalOrSelf resolves symlinks in p, falling back to p itself when the path
// cannot be resolved (it may not exist yet, or a parent may be unreadable).
func evalOrSelf(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
