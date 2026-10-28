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
//     File-target tracing (GIT_TRACE=/abs/path) is preserved;
//   - Dolt's __dolt_remote_info__ branch disabled (doltinfoenv, GH#6484):
//     otherwise every successful write force-pushes a one-file branch into
//     the git remote, which by default is the user's code repo.
//
// Every remote-touching statement in this package must run inside this one
// guard rather than picking wrappers piecemeal — a call site that gets some
// protections and silently misses another is exactly how GH#4272 shipped.
func withRemoteEnvGuards(fn func() error) error {
	return gittraceenv.WithScrubbed(func() error {
		return githooksenv.WithDisabled(func() error {
			return doltinfoenv.WithDisabled(fn)
		})
	})
}
