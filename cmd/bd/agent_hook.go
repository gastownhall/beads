package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Shared helpers for the agent lifecycle hook commands (codex-hook, cursor-hook).
// Each agent keeps its own event names, input/output schemas, and test stubs, but
// the prime-runner and one-shot refresh-marker mechanics are identical and live
// here to avoid divergence.

// allowedPrimeArgs is the closed set of flags the agent hooks may pass to the
// re-executed bd binary. The hooks only ever run the fixed `bd prime`
// invocation (optionally --memories-only); anything else is a programming
// error and is refused before the subprocess is built.
var allowedPrimeArgs = map[string]bool{"--memories-only": true}

// errPrimeTestBinary is returned by resolvePrimeExecutable when the running
// binary is a test binary. Mirrors doctor/fix's ErrTestBinary: re-execing a
// test binary with `prime` fork-bombs the suite.
var errPrimeTestBinary = errors.New("running as test binary - cannot re-exec bd")

// primeExecutable resolves the re-exec target for runBdPrime. A var so tests
// can stub resolution and prove no subprocess is ever built from unexpected
// input.
var primeExecutable = resolvePrimeExecutable

// resolvePrimeExecutable is the production re-exec resolver. It restates
// getBdBinary (cmd/bd/doctor/fix/common.go), the repo's existing answer to "bd
// re-invokes bd": prefer the running binary, resolve symlinks so every re-exec
// site agrees on one path, refuse a test binary so an unstubbed test call
// fails loudly instead of fork-bombing, and fall back to a validated PATH
// lookup when the running binary cannot be resolved. getBdBinary is
// package-private to doctor/fix, so this restates the precedent rather than
// reusing it.
//
// One trade-off is kept deliberately, and it is narrower than it looks. Linux
// reports an unlinked image with a " (deleted)" suffix, but os.Executable
// strips that suffix (os/executable_procfs.go), so no "(deleted)" path ever
// reaches this resolver: a bd replaced in place mid-session (an ordinary
// `go install ./cmd/bd`) still resolves to its own path, EvalSymlinks succeeds
// against the new file, and the re-exec runs the replacement. The residual gap
// is a bd deleted without being replaced: EvalSymlinks fails, that failure is
// tolerated so the stale path is kept, and the PATH fallback is correctly not
// taken because os.Executable itself returned no error. Accepted: the target is
// PATH-independent because it contains a path separator (exec.Command calls
// LookPath only when filepath.Base(name) == name), and both hook handlers treat
// a prime failure as non-fatal, so the effect is a session primed without
// context.
func resolvePrimeExecutable() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		if resolved, linkErr := filepath.EvalSymlinks(exe); linkErr == nil {
			exe = resolved
		}
		// testing.Testing() is the authoritative signal and must come first:
		// rules_go links test binaries as <name>_test (cmd/bd's is bd_test),
		// which no ".test" name check can see, so under the Bazel lane the
		// name checks alone would let an unstubbed call re-exec the test
		// binary. The name checks remain for binaries built with `go test -c`
		// and run outside the harness. Mirrors doctor/fix's isTestBinary.
		base := filepath.Base(exe)
		if testing.Testing() || strings.HasSuffix(base, ".test") || strings.Contains(base, ".test.") {
			return "", errPrimeTestBinary
		}
		return exe, nil
	}

	bdPath, lookErr := exec.LookPath("bd")
	if lookErr != nil {
		return "", fmt.Errorf("bd binary not found in PATH: %w", lookErr)
	}
	return bdPath, nil
}

// validatePrimeArgs rejects any argument outside allowedPrimeArgs so the
// re-exec below can never be steered by caller-supplied strings.
func validatePrimeArgs(args []string) error {
	for _, arg := range args {
		if !allowedPrimeArgs[arg] {
			return fmt.Errorf("bd prime: unsupported hook argument %q", arg)
		}
	}
	return nil
}

// primeCommand validates args, resolves the re-exec target, and builds the
// `bd prime [args...]` command without running it. Split out from runBdPrime
// so a test can assert the exec target and argv — the thing this seam exists
// to guarantee — without launching a subprocess.
func primeCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	if err := validatePrimeArgs(args); err != nil {
		return nil, err
	}
	exe, err := primeExecutable()
	if err != nil {
		return nil, fmt.Errorf("bd prime: resolve executable: %w", err)
	}
	cmdArgs := append([]string{"prime"}, args...)
	// #nosec G204 - exe comes from primeExecutable (resolvePrimeExecutable in
	// production: this bd binary re-invoking itself); cmdArgs is the fixed
	// "prime" subcommand plus allowlisted internal flags, never
	// attacker-controlled input. Documentation only: .golangci.yml excludes
	// G204 repo-wide, so this annotation is not what keeps lint green.
	return exec.CommandContext(ctx, exe, cmdArgs...), nil
}

// runBdPrime shells out to `bd prime [args...]` and returns its combined output.
// The hooks exec a subprocess (rather than calling prime in process) to avoid
// re-entrant store initialization.
func runBdPrime(ctx context.Context, args ...string) (string, error) {
	cmd, err := primeCommand(ctx, args...)
	if err != nil {
		return "", err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("bd %s: %w: %s", strings.Join(cmd.Args[1:], " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// agentHookMarkerBaseDir returns the cache directory for one-shot
// post-compaction refresh markers for a given agent (subdir e.g. "codex-hooks").
// override redirects the location for tests.
func agentHookMarkerBaseDir(subdir, override string) string {
	if override != "" {
		return override
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "beads", subdir)
	}
	return filepath.Join(os.TempDir(), "beads-"+subdir)
}

// agentHookMarkerPath derives a per-session, per-workspace marker file under
// base so concurrent agent sessions don't clobber each other's state. Empty
// keys fall back to stable placeholders.
func agentHookMarkerPath(base, sessionKey, workspaceKey string) string {
	if sessionKey == "" {
		sessionKey = "unknown-session"
	}
	if workspaceKey == "" {
		workspaceKey = "unknown-workspace"
	}
	sum := sha256.Sum256([]byte(sessionKey + "\x00" + filepath.Clean(workspaceKey)))
	return filepath.Join(base, hex.EncodeToString(sum[:])+".refresh")
}

// writeAgentHookMarker creates the marker directory and writes the one-shot
// refresh marker file.
func writeAgentHookMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("1\n"), 0o600) // #nosec G306 -- user-private cache marker
}
