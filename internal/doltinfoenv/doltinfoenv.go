// Package doltinfoenv disables publication of Dolt's visible remote info
// branch for the Dolt git remotes bd constructs.
//
// Dolt's git blobstore force-publishes a visible info branch —
// __dolt_remote_info__ by default, a single DOLT_REMOTE.md marker — on the
// git remote after every successful write (dolthub/dolt store/blobstore:
// pushInfoBranch). Nothing reads the branch back; it exists only to tell a
// human that the repo doubles as a Dolt remote. In bd's default layout the
// git-hosted Dolt remote is the user's code repo, so that branch becomes a
// checkout-able HEAD trap in the working clone: any fetch materializes
// origin/__dolt_remote_info__, and one stray checkout of it strips every
// tracked file from the working tree (GH#6484).
//
// Dolt resolves the branch name from the environment at blobstore
// construction — DOLT_REMOTE_INFO_BRANCH, where a set-to-empty value means
// "do not publish" — and caches blobstores per process, so every
// bd-spawned construction of a git remote must see the off switch. This
// package provides that, in the same shape as internal/githooksenv and
// internal/gittraceenv. A value the user set is always left alone: it is the
// documented opt-in.
package doltinfoenv

import (
	"os"
	"sync"

	"github.com/steveyegge/beads/internal/execenv"
)

// EnvVar is Dolt's environment override for the visible remote info branch
// name. Pinned locally — the same policy as the ref pins in
// cmd/bd/dolt_remote_reset_data.go — so production code does not import
// Dolt's blobstore; TestEnvVarMatchesDolt keeps the pin honest against a
// go.mod bump.
const EnvVar = "DOLT_REMOTE_INFO_BRANCH"

// DisabledEnv returns env with Dolt's remote info branch disabled, for
// callers that hand a subprocess its environment explicitly. An entry the
// user set — any value, including empty, which is Dolt's opt-out form —
// wins and env is returned unchanged. Otherwise the result is a copy of env
// with EnvVar set to empty. The input slice is not modified.
func DisabledEnv(env []string) []string {
	if _, ok := execenv.Lookup(env, EnvVar); ok {
		return env
	}
	out := make([]string, len(env), len(env)+1)
	copy(out, env)
	return append(out, EnvVar+"=")
}

// Process-environment state for WithDisabled. A refcount rather than a plain
// mutex held across fn: remote operations are network-bound and can take
// minutes, and serializing every one of them behind a single lock would be a
// real throughput regression for a change that only needs the variable to be
// set while any of them is running. Overlapping operations all want the
// exact same value, so sharing one activation is correct; the last one out
// restores. The refcount also makes nesting safe, which matters because the
// server-mode credential path and the shared versioncontrolops path can both
// wrap the same call.
var (
	mu       sync.Mutex
	depth    int
	hadSaved bool
)

// WithDisabled runs fn with Dolt's remote info branch disabled process-wide,
// restoring the previous EnvVar state afterwards. A value the user set
// before the call is never changed, inside fn or after it.
func WithDisabled(fn func() error) error {
	acquire()
	defer release()
	return fn()
}

func acquire() {
	mu.Lock()
	defer mu.Unlock()
	if depth == 0 {
		_, hadSaved = os.LookupEnv(EnvVar)
		// Best effort, same policy as githooksenv: a published info branch
		// is better than no remote write at all.
		if !hadSaved {
			_ = os.Setenv(EnvVar, "")
		}
	}
	depth++
}

func release() {
	mu.Lock()
	defer mu.Unlock()
	depth--
	if depth > 0 {
		return
	}
	if !hadSaved {
		_ = os.Unsetenv(EnvVar)
	}
	hadSaved = false
}
