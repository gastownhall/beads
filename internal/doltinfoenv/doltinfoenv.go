// Package doltinfoenv turns off the __dolt_remote_info__ branch Dolt
// publishes on every git-hosted remote it writes to.
//
// After each successful write to a git remote, Dolt force-pushes a one-file
// branch, refs/heads/__dolt_remote_info__ (tree: DOLT_REMOTE.md), next to
// refs/dolt/data (dolt store/blobstore/git_blobstore.go, pushInfoBranch).
// Nothing in Dolt or bd reads it back. bd's default layout aims the Dolt
// remote at the git origin, so the branch lands in the user's code repo, and
// after a `git fetch` a plain `git checkout __dolt_remote_info__` replaces the
// working tree with DOLT_REMOTE.md (GH#6484).
//
// Dolt reads DOLT_REMOTE_INFO_BRANCH once, when it builds the blobstore for a
// remote: a name overrides the branch, an empty value disables it, unset
// means the default. bd sets it to empty for the dolt work it starts unless
// the variable is already present (with any value, empty included): then the
// user's choice wins, which is how a user opts back in.
//
// Same shape as packages githooksenv and gittraceenv: the primary path is
// CALL DOLT_PUSH/DOLT_FETCH running in the Dolt engine inside bd, so the
// override has to be on bd's own process environment for the duration of
// the call.
package doltinfoenv

import (
	"os"
	"slices"

	"github.com/steveyegge/beads/internal/execenv"
)

// EnvVar is Dolt's environment variable for the info branch name
// (blobstore.InfoBranchEnvVar), pinned here so production code does not
// import Dolt's blobstore; a test keeps the pin honest.
const EnvVar = "DOLT_REMOTE_INFO_BRANCH"

// DisabledEnv returns env with the info branch disabled, for callers that
// hand a dolt subprocess its environment explicitly. An env that already
// carries DOLT_REMOTE_INFO_BRANCH is returned unchanged. The input slice is
// not modified.
func DisabledEnv(env []string) []string {
	if _, present := execenv.Lookup(env, EnvVar); present {
		return env
	}
	return append(slices.Clip(env), EnvVar+"=")
}

// WithDisabled runs fn with the info branch disabled process-wide, unsetting
// DOLT_REMOTE_INFO_BRANCH again afterwards. A variable that is already
// present is never touched.
func WithDisabled(fn func() error) error {
	if _, present := os.LookupEnv(EnvVar); present {
		return fn()
	}
	// Best effort: Setenv failure is extremely rare, and a push that also
	// publishes the info branch is better than no push at all.
	_ = os.Setenv(EnvVar, "")
	defer func() { _ = os.Unsetenv(EnvVar) }()
	return fn()
}
