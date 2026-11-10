package main

import (
	"testing"
)

// GH#5922: the hook lanes re-execute bd as a child — `bd export` on the
// pre-commit lane (exportJSONLForCommit) and `bd import` on the post-merge and
// post-checkout lanes (importJSONLForSync). Both resolved the child through
// PATH, which re-runs the selection the hook shim already made and can come
// out differently for the child than for the parent: a bd started by absolute
// path — a version-manager shim, a wrapper, an install shadowed by an older
// copy earlier on PATH — hands the commit's export or import to that other
// bd, and a stale one there cannot open the database the current bd wrote
// ("Error 1105: table has unknown fields"), leaving the committed JSONL
// silently stale. The children therefore re-execute this process's own
// executable, resolved the same way the agent-hook prime re-exec resolves
// itself (resolveSelfExecutable in agent_hook.go), so parent and child always
// agree on which bd they are. The shims stay portable: no host-specific path
// is pinned into tracked hook files.

// hookSubprocessBinary is the argv[0] of the bd child a hook lane re-executes.
// A var so a test can pin the seam; production never overrides it.
var hookSubprocessBinary = defaultHookSubprocessBinary

// defaultHookSubprocessBinary resolves the child binary from the running
// process. In a test process the resolver hands back the bare PATH lookup so
// the stub-on-PATH fixtures keep standing in for bd, and so the test
// executable is never launched recursively.
func defaultHookSubprocessBinary() string {
	exe, err := resolveSelfExecutable()
	return hookSubprocessBinaryFor(exe, err, testing.Testing())
}

// hookSubprocessBinaryFor applies the child-selection rule to one
// self-resolution outcome: run self by absolute path, with symlinks
// canonicalized so an install reached through a version-manager shim re-execs
// the real image (and an uncanonicalizable path is kept as-is, matching
// resolvePrimeExecutable's tolerated gap), and fall back to the historical
// bare "bd" PATH lookup only when the running binary cannot be resolved at
// all or when the running binary is a test binary — re-execing one would
// fork-bomb the suite, and the PATH fallback is what lets the hook tests keep
// their stub. The Bazel lane links cmd/bd's test binary as bd_test, which the
// name check covers even outside the harness.
func hookSubprocessBinaryFor(exe string, exeErr error, inTestHarness bool) string {
	if exeErr != nil {
		return "bd"
	}
	exe = canonicalizeExecutablePath(exe)
	if inTestHarness || isTestBinaryPath(exe) {
		return "bd"
	}
	return exe
}
