//go:build !linux

package doltserver

// SweepOrphanedTestServers is a no-op on non-Linux platforms. The leaked
// dolt sql-server evidence for gastownhall/beads mybd-q6cz is Linux-only
// (/proc-based process/cwd inspection isn't portable), so this stub exists
// only so callers (test TestMains) compile unchanged everywhere.
func SweepOrphanedTestServers(_ ...string) []int {
	return nil
}
