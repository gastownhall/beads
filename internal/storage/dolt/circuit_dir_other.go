//go:build !unix

package dolt

// circuitBreakerDirName names the live breaker directory under os.TempDir().
// Windows already resolves os.TempDir() to a per-user %TEMP%, so the name
// stays unsuffixed there.
func circuitBreakerDirName() string {
	return "beads-circuit"
}
