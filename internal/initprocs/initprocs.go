// Package initprocs runs package initialization on a few Ps rather than every
// CPU the host has.
//
// bd links several thousand packages whose init functions allocate about
// 20 MB before main runs, enough for a handful of garbage collections. Each
// collection starts a mark worker and stops the world across every P, which
// on a large host (192 CPUs on the machine this was measured on) costs more
// than the collection itself: about 20 ms of a 110 ms `bd version`.
//
// The package imports only runtime and syscall so that it initializes ahead of
// the heavy packages, and main calls Done before doing anything else.
package initprocs

import (
	"runtime"
	"syscall"
)

const initProcs = 4

var restore int

// setProcs and setDefault are runtime.GOMAXPROCS and
// runtime.SetDefaultGOMAXPROCS, swappable so a test can tell which one Done
// used.
var (
	setProcs   = runtime.GOMAXPROCS
	setDefault = runtime.SetDefaultGOMAXPROCS
)

func init() {
	if runtime.GOMAXPROCS(0) > initProcs {
		restore = runtime.GOMAXPROCS(initProcs)
	}
}

// Done returns GOMAXPROCS to what the process would have had without this
// package: the value GOMAXPROCS asked for when it is set, else the runtime's
// own default, which keeps tracking the CPU limit.
func Done() {
	if restore == 0 {
		return
	}
	n := restore
	restore = 0
	if _, set := syscall.Getenv("GOMAXPROCS"); set {
		setProcs(n)
		return
	}
	setDefault()
}
