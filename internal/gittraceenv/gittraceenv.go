// Package gittraceenv scrubs stderr-directed git tracing variables around the
// git commands Dolt runs inside bd's own process.
//
// Dolt transfers refs/dolt/data for a git-protocol remote by shelling out to
// git plumbing against its cache-mirror, .dolt/git-remote-cache/<hash>/repo.git,
// capturing stdout and stderr into ONE buffer and parsing object ids out of it
// (dolt store/blobstore/internal/git/runner.go, Run). With GIT_TRACE=1 in the
// environment every captured value gains "trace: ..." lines, and the next
// plumbing call receives the polluted string as an object name:
//
//	git ls-tree -r -t '23:45:19 git.c:476 trace: built-in: git rev-parse ...
//	<oid>^{tree}'
//	fatal: Not a valid object name ...
//
// So an operator who exports GIT_TRACE=1 to debug a failing sync breaks every
// embedded push/pull/fetch/clone outright — the diagnostic kills the thing
// being diagnosed, and the failure surfaces as a baffling "failed to get
// remote db ... could not be accessed".
//
// The scrub is value-aware: git's file-target forms (an absolute path, or
// af_unix:... for GIT_TRACE2) never touch stderr, are harmless to Dolt's
// parsing, and are the supported way to trace bd's git remote plumbing — those
// values are left alone. Only the forms git directs at stderr (1/true/yes/on),
// at an inherited file descriptor (2..9), or rejects with a warning printed to
// stderr (a relative path) are removed for the duration of the call.
//
// Same shape as package githooksenv (GH#3724/GH#4272): the primary path is
// CALL DOLT_PUSH/DOLT_FETCH running in the Dolt engine inside bd — there is no
// exec.Cmd to decorate, so the override has to be on bd's own process
// environment for the duration of the call.
package gittraceenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// pathCapableVars are git tracing variables that accept a file target (an
// absolute path; GIT_TRACE2 family also accepts af_unix:...). Their
// stderr/fd/invalid forms are scrubbed; file targets are kept.
var pathCapableVars = []string{
	"GIT_TRACE",
	"GIT_TRACE_CURL",
	"GIT_TRACE_FSMONITOR",
	"GIT_TRACE_PACKET",
	"GIT_TRACE_PACKFILE",
	"GIT_TRACE_PACK_ACCESS",
	"GIT_TRACE_PERFORMANCE",
	"GIT_TRACE_REFS",
	"GIT_TRACE_SETUP",
	"GIT_TRACE_SHALLOW",
	"GIT_TRACE2",
	"GIT_TRACE2_EVENT",
	"GIT_TRACE2_PERF",
}

// alwaysStderrVars have no file-target form: git enables them on mere
// presence and writes to stderr. Scrubbed whenever set.
var alwaysStderrVars = []string{
	"GIT_CURL_VERBOSE",
}

// Vars returns every variable this package may scrub, for callers that build
// a subprocess environment (see ScrubEnv). The returned slice is a copy.
func Vars() []string {
	out := make([]string, 0, len(pathCapableVars)+len(alwaysStderrVars))
	out = append(out, pathCapableVars...)
	return append(out, alwaysStderrVars...)
}

// stderrDirected reports whether value is a form git directs at stderr (or an
// inherited fd, or rejects with a stderr warning) rather than at a file, for
// the trace variable name.
//
// Git's rule (trace.c): "" / "0" / "false" disable tracing; "1", "2".."9",
// "true" write to that file descriptor (1 and true mean stderr); an absolute
// path appends to that file; anything else draws "warning: unknown trace
// value" on stderr. Only the GIT_TRACE2 family additionally accepts
// af_unix:[<mode>:]<path> — for the classic GIT_TRACE vars that value is
// "anything else", i.e. a per-plumbing-call stderr warning.
func stderrDirected(name, value string) bool {
	switch strings.ToLower(value) {
	case "", "0", "false":
		return false // disabled — harmless
	}
	if filepath.IsAbs(value) {
		return false // file target — never touches stderr
	}
	// Git's is_absolute_path on Windows accepts a leading dir separator
	// (a Git-Bash-style /c/temp/git.trace), which filepath.IsAbs rejects.
	if runtime.GOOS == "windows" && (value[0] == '/' || value[0] == '\\') {
		return false // file target for git — never touches stderr
	}
	if strings.HasPrefix(value, "af_unix:") && strings.HasPrefix(name, "GIT_TRACE2") {
		return false // trace2 socket target — never touches stderr
	}
	return true
}

// envNameEquals compares environment variable names: exact on POSIX,
// case-insensitive on Windows to match Win32 (and git's) env lookup.
func envNameEquals(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// Process-environment state for WithScrubbed. A refcount rather than a plain
// mutex held across fn, for the same reason as githooksenv: remote operations
// are network-bound and can take minutes, overlapping operations all want the
// same environment, and the last one out restores.
var (
	mu    sync.Mutex
	depth int
	saved map[string]string
)

// WithScrubbed runs fn with stderr-directed git tracing variables removed from
// the process environment, restoring the previous values afterwards.
func WithScrubbed(fn func() error) error {
	acquire()
	defer release()
	return fn()
}

func acquire() {
	mu.Lock()
	defer mu.Unlock()
	depth++
	if depth > 1 {
		return
	}
	saved = map[string]string{}
	for _, name := range pathCapableVars {
		if v, ok := os.LookupEnv(name); ok && stderrDirected(name, v) {
			saved[name] = v
			// Best effort, matching githooksenv: an unscrubbed push that
			// may still succeed beats no push at all.
			_ = os.Unsetenv(name)
		}
	}
	for _, name := range alwaysStderrVars {
		if v, ok := os.LookupEnv(name); ok {
			saved[name] = v
			_ = os.Unsetenv(name)
		}
	}
}

func release() {
	mu.Lock()
	defer mu.Unlock()
	depth--
	if depth > 0 {
		return
	}
	for name, v := range saved {
		_ = os.Setenv(name, v)
	}
	saved = nil
}

// ScrubEnv returns env with stderr-directed git tracing entries removed, for
// callers that hand a subprocess its environment explicitly (the CLI `dolt
// push/pull` route). Later duplicates win in exec env semantics, so every
// entry of a scrubbed name is dropped, not just the last. The input slice is
// not modified.
func ScrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if ok && shouldScrub(name, value) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func shouldScrub(name, value string) bool {
	for _, n := range alwaysStderrVars {
		if envNameEquals(name, n) {
			return true
		}
	}
	for _, n := range pathCapableVars {
		if envNameEquals(name, n) {
			return stderrDirected(n, value)
		}
	}
	return false
}
