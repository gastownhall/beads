//go:build !linux && !darwin

package doltserver

// SweepOrphanedTestServers is a no-op on platforms where process command
// lines and working directories cannot be inspected by an implementation in
// this package. The stub keeps callers (test TestMains) portable.
func SweepOrphanedTestServers(_ ...string) []int {
	return nil
}
