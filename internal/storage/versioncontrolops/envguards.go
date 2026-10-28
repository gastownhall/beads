package versioncontrolops

import (
	"github.com/steveyegge/beads/internal/doltinfoenv"
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
//     File-target tracing (GIT_TRACE=/abs/path) is preserved.
//   - Dolt's visible remote info branch disabled (doltinfoenv, GH#6484):
//     Dolt force-publishes __dolt_remote_info__ on the git remote after
//     every successful write, and in the default layout that remote is the
//     user's code repo, where the branch is a checkout-able HEAD trap.
//     DOLT_REMOTE_INFO_BRANCH set to empty is Dolt's opt-out; a value the
//     user set is the documented opt-in and is never touched.
//
// Every remote-touching statement in this package must run inside this one
// guard rather than picking wrappers piecemeal — a call site that gets two
// protections and silently misses the third is exactly how GH#4272 shipped.
func withRemoteEnvGuards(fn func() error) error {
	return gittraceenv.WithScrubbed(func() error {
		return githooksenv.WithDisabled(func() error {
			return doltinfoenv.WithDisabled(fn)
		})
	})
}
