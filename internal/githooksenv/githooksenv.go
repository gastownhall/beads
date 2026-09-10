// Package githooksenv disables git's client-side hooks for the git commands
// Dolt runs inside bd's own process.
//
// Dolt transfers refs/dolt/data for a git-protocol remote by shelling out to
// git against its embedded cache-mirror, .dolt/git-remote-cache/<hash>/repo.git.
// That mirror is a bare-style repo with no work tree, but `git init` still
// honors the user's init.templateDir and copies template hooks into its
// hooks/ dir. A templated pre-push hook that runs `git diff` or `git status`
// — the pre-commit framework's staged_files_only setup, for instance — then
// dies with "fatal: this operation must be run in a work tree" and takes bd's
// push down with it (GH#3724, GH#4272).
//
// PR #3740 fixed this for the CLI shell-out fallback by decorating the
// exec.Cmd. The primary path is CALL DOLT_PUSH/DOLT_FETCH, which runs in the
// Dolt engine inside bd and spawns git itself — there is no exec.Cmd to
// decorate, so the override has to be on bd's own process environment for the
// duration of the call. That is what this package provides.
package githooksenv

import (
	"os"
	"strings"
	"sync"
)

// ParametersEnv is git's environment variable for injecting one-off config
// values into every git invocation in the process subtree.
const ParametersEnv = "GIT_CONFIG_PARAMETERS"

// NoHooksParam points core.hooksPath at /dev/null, i.e. tells git to find no
// hooks at all. Same intent as the --no-verify fix on the commit side
// (GH#3340 / GH#3598 / PR #3626).
const NoHooksParam = "'core.hooksPath=/dev/null'"

// AppendParameter adds param to an existing GIT_CONFIG_PARAMETERS value rather
// than replacing it, so a caller's own overrides survive. Git applies the
// entries left to right, which is also what makes an appended override win.
func AppendParameter(existing, param string) string {
	if existing == "" {
		return param
	}
	return existing + " " + param
}

// Extract returns the GIT_CONFIG_PARAMETERS value from an environment slice,
// using exec's last-wins semantics for duplicate keys.
func Extract(env []string) string {
	prefix := ParametersEnv + "="
	var val string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, prefix); ok {
			val = v
		}
	}
	return val
}

// DisabledEnv returns env with git client-side hooks disabled, for callers
// that hand a subprocess its environment explicitly. Existing
// GIT_CONFIG_PARAMETERS entries are collapsed (exec last-wins) into one entry
// with the no-hooks override appended. The input slice is not modified.
func DisabledEnv(env []string) []string {
	merged := AppendParameter(Extract(env), NoHooksParam)
	prefix := ParametersEnv + "="
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return append(out, prefix+merged)
}

// Process-environment state for WithDisabled. A refcount rather than a plain
// mutex held across fn: remote operations are network-bound and can take
// minutes, and serializing every one of them behind a single lock would be a
// real throughput regression for a change that only needs the variable to be
// set while any of them is running. Overlapping operations all want the exact
// same value, so sharing one activation is correct; the last one out restores.
// The refcount also makes nesting safe, which matters because the server-mode
// credential path and the shared versioncontrolops path can both wrap the same
// call.
var (
	mu       sync.Mutex
	depth    int
	saved    string
	hadSaved bool
)

// WithDisabled runs fn with git client-side hooks disabled process-wide,
// restoring the previous GIT_CONFIG_PARAMETERS afterwards.
func WithDisabled(fn func() error) error {
	acquire()
	defer release()
	return fn()
}

func acquire() {
	mu.Lock()
	defer mu.Unlock()
	if depth == 0 {
		saved, hadSaved = os.LookupEnv(ParametersEnv)
		// Best effort: Setenv failure is extremely rare, and a push that runs
		// hooks is better than no push at all.
		_ = os.Setenv(ParametersEnv, AppendParameter(saved, NoHooksParam))
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
	if hadSaved {
		_ = os.Setenv(ParametersEnv, saved)
	} else {
		_ = os.Unsetenv(ParametersEnv)
	}
	saved, hadSaved = "", false
}
