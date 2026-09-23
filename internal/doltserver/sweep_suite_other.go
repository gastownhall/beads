//go:build !linux && !darwin

package doltserver

import "os"

// rootOwnedBySelf accepts every root on platforms with no POSIX ownership in
// os.FileInfo.Sys(). It costs nothing: processAlive below reports every owner
// alive here, so SweepDeadSuiteRoots never removes anything anyway.
func rootOwnedBySelf(_ os.FileInfo) bool { return true }

// processAlive reports every owner as alive on platforms where this package
// has no liveness probe, which makes SweepDeadSuiteRoots a no-op there: it
// can never conclude a root's owner is dead, so it never removes anything.
// Same posture as SweepOrphanedTestServers in sweep_other.go — the stub keeps
// callers (test TestMains) portable without giving them a destructive
// best-guess.
func processAlive(_ int) bool { return true }
