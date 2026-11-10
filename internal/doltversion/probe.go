package doltversion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// defaultProbeTimeout bounds how long Probe waits for `<path> version` to
// return when the caller's context carries no deadline of its own. Ten
// seconds is generous for a subprocess that should print one line and
// exit immediately, but conservative enough that a genuinely hung or
// wedged dolt binary (e.g. blocked on a lock file, or waiting on stdin it
// will never get) does not stall startup indefinitely.
const defaultProbeTimeout = 10 * time.Second

// probeOutputCap bounds how much of stdout/stderr Probe reads from the
// child process, independently for each stream. A well-behaved `dolt
// version` prints a couple of short lines; this cap exists so that a
// misbehaving or malicious binary at the configured path (garbage output,
// or an infinite writer) cannot balloon this process's memory just from
// being probed — 64KiB is far more than any real version string needs
// while still cheap to allocate unconditionally.
const probeOutputCap = 64 * 1024

// Identity captures everything Probe learns about a specific on-disk dolt
// binary at the moment it was probed: its resolved version, the raw output
// that version was parsed from, and file-identity fields (size, mtime,
// resolved real path). The file-identity fields exist so a caller that
// caches a Probe result (as this package's callers are expected to) can
// later detect that the binary at GivenPath has changed on disk since it
// was probed — the file could have been upgraded, downgraded, or replaced
// by a different binary entirely between probe time and use time (TOCTOU).
// This PR only captures the identity; re-validating it at launch time
// against a cached Identity is PR-2's concern.
type Identity struct {
	// GivenPath is the path Probe was called with.
	GivenPath string
	// RealPath is GivenPath with any symlink resolved. Equal to GivenPath
	// when GivenPath is not a symlink.
	RealPath string
	// Version is the parsed result of RawOutput. Zero value if parsing
	// failed (Probe still returns Identity alongside ErrUnparseableVersion
	// in that case, so callers/tests can inspect RawOutput for diagnosis).
	Version Version
	// RawOutput is the first line of the probe's stdout, trimmed of
	// leading/trailing whitespace.
	RawOutput string
	// FileSize is RealPath's size in bytes at probe time.
	FileSize int64
	// FileModTime is RealPath's modification time at probe time.
	FileModTime time.Time
}

// Probe runs `<path> version` under hardened conditions and parses the
// result. It is "hardened" in three ways that matter for probing an
// externally-supplied, potentially untrusted or broken binary path rather
// than a binary this process built itself:
//
//  1. Bounded time: if ctx has no deadline, one is added
//     (defaultProbeTimeout); combined with cmd.WaitDelay, a child that
//     ignores the initial signal on context cancellation is force-killed
//     rather than left to hang the caller indefinitely.
//  2. Bounded memory: stdout and stderr are each capped at probeOutputCap
//     bytes, so a binary that (accidentally or not) writes unbounded
//     output cannot exhaust memory just from being probed.
//  3. Pre-exec validation: the path is checked for existence and the
//     executable bit before exec is attempted, so the common failure modes
//     (typo'd path, non-executable file, directory) produce the specific
//     ErrNotFound/ErrNotExecutable errors from this package instead of a
//     raw, harder-to-branch-on *exec.Error.
func Probe(ctx context.Context, path string) (Identity, error) {
	if err := validateExplicitPath(path); err != nil {
		return Identity{}, err
	}

	// Resolve symlinks unconditionally, not just when the leaf path itself
	// is a symlink: a symlinked PARENT directory (e.g. /usr/local/bin ->
	// /opt/homebrew/bin) also makes GivenPath differ from the file's real
	// location, and EvalSymlinks handles that case as well as a symlinked
	// leaf. Fall back to path unresolved on error (e.g. a dangling
	// component) — the os.Stat right below will surface the real problem.
	realPath := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		realPath = resolved
	}

	info, err := os.Stat(realPath)
	if err != nil {
		// Same taxonomy split as validateExplicitPath: a missing file is
		// ErrNotFound; any other stat failure (most commonly EACCES) means
		// the path may exist but is unusable from here — ErrNotExecutable.
		if os.IsNotExist(err) {
			return Identity{}, fmt.Errorf("%w: stat %s: %v", ErrNotFound, realPath, err)
		}
		return Identity{}, fmt.Errorf("%w: stat %s: %v", ErrNotExecutable, realPath, err)
	}

	id := Identity{
		GivenPath:   path,
		RealPath:    realPath,
		FileSize:    info.Size(),
		FileModTime: info.ModTime(),
	}

	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultProbeTimeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, path, "version") //nolint:gosec // G204: path is caller-resolved and pre-exec-validated above, not user-request input
	// WaitDelay bounds how long Wait() waits for the child's I/O pipes to
	// close after the context is done / the process is signaled. Without
	// it, a child that ignores SIGKILL's effect on its own goroutines (or
	// whose stdout pipe is held open by a grandchild) can make Wait()
	// block past the context timeout. A short delay relative to the probe
	// timeout is enough — this is cleanup slop, not a second timeout.
	cmd.WaitDelay = 2 * time.Second

	var stdout, stderr capBuffer
	stdout.limit = probeOutputCap
	stderr.limit = probeOutputCap
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	id.RawOutput = firstLineTrimmed(stdout.buf.String())

	if runErr != nil {
		if isExecFormatError(runErr) {
			return id, fmt.Errorf(
				"%w: %s: exec format error — this usually means the dolt binary's architecture/loader doesn't match this machine (running %s/%s): %v",
				ErrProbeFailed, path, runtime.GOOS, runtime.GOARCH, runErr,
			)
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return id, fmt.Errorf("%w: %s: timed out running `%s version`: %v", ErrProbeFailed, path, path, runErr)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			// Distinct from every other branch here: the process wasn't
			// killed because dolt is broken, it was killed because the
			// CALLER's context was canceled (e.g. the user hit Ctrl-C, or
			// an outer operation gave up). Wrapping context.Canceled
			// (rather than ErrProbeFailed) lets a caller tell "the user
			// aborted this" apart from "dolt itself is unusable" via
			// errors.Is, which matters if it wants to skip printing a
			// misleading "dolt is broken, reinstall it" hint.
			return id, fmt.Errorf("%s: version probe canceled: %w", path, ctx.Err())
		}
		return id, fmt.Errorf("%w: %s: %v (stderr: %s)", ErrProbeFailed, path, runErr, firstLineTrimmed(stderr.buf.String()))
	}

	ver, err := ParseVersion(stdout.buf.String())
	if err != nil {
		return id, fmt.Errorf("%w: %s: output %q: %v", ErrUnparseableVersion, path, id.RawOutput, err)
	}
	id.Version = ver
	return id, nil
}

// firstLineTrimmed returns the first line of s, trimmed of surrounding
// whitespace. Used for RawOutput and for condensing stderr into a
// one-line diagnostic.
func firstLineTrimmed(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// windowsBadExeFormat is ERROR_BAD_EXE_FORMAT, Windows's equivalent of
// ENOEXEC — returned (wrapped in a *fs.PathError, same as ENOEXEC on
// Unix) when CreateProcess refuses to load a file as an executable at
// all. Named here rather than imported from golang.org/x/sys/windows to
// keep this package stdlib-only (see package doc comment).
const windowsBadExeFormat = 193

// isExecFormatError reports whether err (as returned from cmd.Run) is an
// ENOEXEC (Unix) / ERROR_BAD_EXE_FORMAT (Windows) — the OS refusing to run
// the file as an executable at all, which is the signature of an
// architecture or loader mismatch (e.g. an arm64 dolt binary on an amd64
// host, or a binary built for a different OS). Matched via errors.As
// against the concrete syscall.Errno (works through the *exec.Error /
// *fs.PathError wrapping cmd.Run's error normally carries) rather than
// substring-matching err.Error(), so this doesn't depend on the OS's
// exact, potentially-localized error string.
func isExecFormatError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.ENOEXEC || (runtime.GOOS == "windows" && errno == windowsBadExeFormat)
}

// capBuffer is an io.Writer that discards writes past a byte limit while
// still reporting a successful write count for every byte offered (so the
// child process is never blocked or given a short-write error just because
// this process stopped caring about further output — the child should be
// allowed to run to completion or timeout normally, not fail because Probe
// stopped listening).
type capBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - c.buf.Len(); remaining > 0 {
		n := len(p)
		if n > remaining {
			n = remaining
		}
		c.buf.Write(p[:n])
	}
	return len(p), nil
}

var _ io.Writer = (*capBuffer)(nil)
