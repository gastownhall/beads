package doltversion

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// DoltBinEnvVar is the environment variable that lets an operator pin the
// exact dolt binary managed proxied-server mode should spawn, overriding
// whatever would otherwise be found via a clone-local sidecar setting or
// PATH. It is checked first specifically so it can be used as an emergency
// override (e.g. "the PATH dolt is broken, point at a known-good one for
// this one invocation") without editing any persisted config.
const DoltBinEnvVar = "BEADS_DOLT_BIN"

// Source identifies which resolution step produced the dolt binary path
// Resolve returned, so callers can report it in diagnostics (e.g. "using
// dolt from BEADS_DOLT_BIN" vs "using dolt from PATH") without the caller
// needing to re-derive it.
type Source int

const (
	// SourceUnknown is the zero value of Source. It is returned only when
	// there is no meaningful "which precedence level" to report (e.g. a
	// caller holding a zero-value Source that was never assigned by
	// Resolve) — Resolve itself always returns one of the other three
	// values, even on error, so callers can report where a failed
	// candidate came from. Zero-valuing this (rather than SourceEnv, the
	// prior first iota) avoids a zero Source silently misreporting itself
	// as "from BEADS_DOLT_BIN" if a caller forgets to check an error.
	SourceUnknown Source = iota
	// SourceEnv means the path came from the BEADS_DOLT_BIN environment
	// variable (or the equivalent ResolveOptions.EnvValue override).
	SourceEnv
	// SourceSidecar means the path came from a clone-local sidecar setting
	// (ResolveOptions.SidecarValue). The sidecar file itself is not read by
	// this package in this PR — callers pass "" today; PR-2 wires an
	// actual sidecar reader that populates ResolveOptions.SidecarValue.
	SourceSidecar
	// SourcePath means the path came from exec.LookPath("dolt") against
	// the process's PATH.
	SourcePath
)

// String renders Source for diagnostics and log messages.
func (s Source) String() string {
	switch s {
	case SourceEnv:
		return DoltBinEnvVar
	case SourceSidecar:
		return "sidecar config"
	case SourcePath:
		return "PATH"
	default:
		return "unknown"
	}
}

// ResolveOptions carries the resolution inputs Resolve needs. Callers supply
// EnvValue/SidecarValue explicitly (rather than Resolve reading os.Getenv
// itself) so that resolution is a pure function of its inputs and tests do
// not need to mutate process environment to exercise precedence — except
// where a caller specifically wants "whatever is in the environment right
// now", for which ReadEnvOverride is provided as a small helper.
type ResolveOptions struct {
	// EnvValue is the value of BEADS_DOLT_BIN, or "" if unset. Highest
	// precedence: an explicit, non-empty EnvValue that fails validation is
	// an error, not a signal to fall through to SidecarValue or PATH — an
	// operator who named a specific binary almost certainly wants an error
	// if that binary is broken, not a silent fallback to a different one.
	EnvValue string
	// SidecarValue is a clone-local dolt-binary setting, or "" if unset.
	// Second precedence, same broken-is-an-error semantics as EnvValue.
	// This PR only provides the hook point; PR-2 supplies real values by
	// reading the sidecar file.
	SidecarValue string
}

// ReadEnvOverride reads BEADS_DOLT_BIN from the process environment. It is a
// thin helper for callers that want Resolve's default env behavior without
// wiring os.Getenv at each call site; Resolve itself never reads the
// environment implicitly, to keep it testable as a pure function of
// ResolveOptions.
func ReadEnvOverride() string {
	return os.Getenv(DoltBinEnvVar)
}

// Resolve locates the dolt binary to use for managed proxied-server mode,
// applying precedence env (opts.EnvValue) > sidecar (opts.SidecarValue) >
// PATH (exec.LookPath("dolt")), and returns the resolved path along with
// the Source it came from.
//
// An explicit, non-empty EnvValue or SidecarValue that fails validation
// (does not exist, is not a regular file, or lacks the executable bit)
// returns an error immediately rather than falling through to the next
// precedence level: the whole point of naming a specific binary is to pin
// which one is used, so silently substituting a different one on failure
// would defeat that intent and could mask a real misconfiguration.
//
// Explicit env/sidecar values are absolutized (filepath.Abs against the
// process's current directory) before validation, and the absolute form is
// what gets returned and probed. This matters for a bare, separator-free
// value like BEADS_DOLT_BIN=dolt-next: os.Stat/exec on a bare name without
// a path separator resolve it two DIFFERENT ways — Go's os/exec (and the
// OS exec syscalls on Unix) treat a name with no separator as a PATH
// lookup, while os.Stat treats it as cwd-relative. Without absolutizing
// first, validateExplicitPath's os.Stat would silently validate a
// cwd-relative file while Probe's later exec.CommandContext(ctx, path,
// ...) would instead search PATH for a same-named binary — approving one
// binary and launching a different one. filepath.Abs forces both steps to
// agree on the same cwd-relative file in that case.
func Resolve(opts ResolveOptions) (string, Source, error) {
	if opts.EnvValue != "" {
		abs, err := filepath.Abs(opts.EnvValue)
		if err != nil {
			return "", SourceEnv, fmt.Errorf("%s=%q: resolving absolute path: %w", DoltBinEnvVar, opts.EnvValue, err)
		}
		abs = completeExecutableExt(abs)
		if err := validateExplicitPath(abs); err != nil {
			return "", SourceEnv, fmt.Errorf("%s=%q: %w", DoltBinEnvVar, opts.EnvValue, err)
		}
		return abs, SourceEnv, nil
	}

	if opts.SidecarValue != "" {
		abs, err := filepath.Abs(opts.SidecarValue)
		if err != nil {
			return "", SourceSidecar, fmt.Errorf("sidecar dolt binary %q: resolving absolute path: %w", opts.SidecarValue, err)
		}
		abs = completeExecutableExt(abs)
		if err := validateExplicitPath(abs); err != nil {
			return "", SourceSidecar, fmt.Errorf("sidecar dolt binary %q: %w", opts.SidecarValue, err)
		}
		return abs, SourceSidecar, nil
	}

	path, err := exec.LookPath("dolt")
	if err != nil {
		return "", SourcePath, fmt.Errorf("%w: dolt not found on PATH: %v", ErrNotFound, err)
	}
	return path, SourcePath, nil
}

// completeExecutableExt completes a Windows executable extension on an
// explicitly-named path spelled without one: BEADS_DOLT_BIN=C:\tools\dolt
// should find C:\tools\dolt.exe the same way cmd.exe or exec.LookPath
// would. os.Lstat has no PATHEXT concept, so without this the explicit
// path reported "not found" while dolt.exe sat right there. The exact
// spelled path wins when it exists (an operator who names an extensionless
// file gets that file); only when it does not is exec.LookPath consulted —
// on Windows, LookPath applies PATHEXT completion to paths that contain a
// separator without searching PATH. Non-Windows returns path unchanged:
// there, Unix has no implied-extension convention and LookPath's extra
// checks would only duplicate validateExplicitPath with a different error
// taxonomy.
func completeExecutableExt(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	if _, err := os.Lstat(path); err == nil {
		return path
	}
	if completed, err := exec.LookPath(path); err == nil {
		return completed
	}
	return path
}

// validateExplicitPath checks an explicitly-named binary path (from env or
// sidecar, never from PATH — exec.LookPath already validates executability
// on the platforms that matter). It requires the path to exist, resolve to
// a regular file (following symlinks — a symlink to a valid binary is
// fine, a symlink to a directory or to nothing is not), and have at least
// one executable bit set.
func validateExplicitPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		// A missing path is genuinely ErrNotFound. Anything else (most
		// commonly EACCES on a parent directory) means the path may well
		// exist but this process can't tell — that is a "broken", not
		// "absent", candidate, so it maps to ErrNotExecutable with the raw
		// stat error preserved rather than being folded into the same
		// not-found bucket.
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %v", ErrNotFound, err)
		}
		return fmt.Errorf("%w: stat failed: %v", ErrNotExecutable, err)
	}

	realPath := path
	if info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("%w: resolving symlink: %v", ErrNotFound, err)
		}
		realPath = resolved
		info, err = os.Stat(realPath)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrNotFound, err)
		}
	}

	if info.IsDir() {
		return fmt.Errorf("%w: %s is a directory", ErrNotExecutable, realPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s is not a regular file", ErrNotExecutable, realPath)
	}
	// Windows has no meaningful executable-bit concept in os.FileMode (the
	// Go runtime derives permission bits there from the read-only
	// attribute, not from any per-file "executable" flag), so the bit
	// check below is Unix-only; a regular file is treated as sufficient on
	// Windows and letting the actual exec attempt in Probe fail closed if
	// it turns out not to be runnable.
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return fmt.Errorf("%w: %s has no executable bit set", ErrNotExecutable, realPath)
	}
	return nil
}
