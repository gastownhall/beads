package versioncontrolops

import (
	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/githooksenv"
	"github.com/steveyegge/beads/internal/gittraceenv"
)

// withRemoteEnvGuards runs fn — an engine statement that can touch a
// git-protocol remote (CALL DOLT_PUSH/DOLT_FETCH/DOLT_CLONE) — with bd's
// process environment made safe for the git plumbing Dolt spawns in-process:
//
//   - client-side git hooks disabled (githooksenv, GH#3724/GH#4272): a
//     templated pre-push hook in the cache-mirror repo kills the transfer
//     with "fatal: this operation must be run in a work tree";
//   - stderr-directed git tracing removed (gittraceenv): Dolt parses object
//     ids out of combined stdout+stderr, so GIT_TRACE=1 corrupts every
//     captured value and the transfer dies with "failed to get remote db".
//     File-target tracing (GIT_TRACE=/abs/path) is preserved;
//   - git routing entries removed (gitenv): bd's own git surfaces select a
//     repository through GIT_DIR/GIT_WORK_TREE (the documented -C contract),
//     and a leaked GIT_WORK_TREE with no matching GIT_DIR makes the git
//     plumbing Dolt spawns refuse with "GIT_WORK_TREE not allowed without
//     specifying GIT_DIR", so every remote transfer under `bd -C` dies.
//
// Every remote-touching statement in this package must run inside this one
// guard rather than picking wrappers piecemeal — a call site that gets one
// protection and silently misses the other is exactly how GH#4272 shipped.
//
// Composition order is load-bearing: the routing scrub is the outermost
// boundary, removing the CALLER's inherited git selection (GIT_DIR,
// GIT_WORK_TREE, and every GIT_CONFIG* form including GIT_CONFIG_PARAMETERS)
// before anything else runs; the guards bd itself applies — tracing scrub,
// hooks disable — set their own controls inside that boundary. A routing-inner
// order would strip the very GIT_CONFIG_PARAMETERS the hooks guard installs,
// because githooksenv expresses hooks-disable through that variable.
func withRemoteEnvGuards(fn func() error) error {
	return gitenv.WithRoutingScrubbed(func() error {
		return gittraceenv.WithScrubbed(func() error {
			return githooksenv.WithDisabled(fn)
		})
	})
}
