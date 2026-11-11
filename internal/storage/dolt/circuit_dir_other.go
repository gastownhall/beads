//go:build !unix

package dolt

import "os"

// circuitBreakerDirName names the live breaker directory under os.TempDir().
// Windows already resolves os.TempDir() to a per-user %TEMP%, so the name
// stays unsuffixed there.
func circuitBreakerDirName() string {
	return "beads-circuit"
}

// circuitDirOwnershipProblem has no ownership rule to apply here; the
// symlink and directory checks in circuitDirProblem still run.
func circuitDirOwnershipProblem(os.FileInfo) string {
	return ""
}
