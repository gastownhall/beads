// Rotation for dolt-server.log.
//
// Background: dolt sql-server writes stdout/stderr straight into
// .beads/dolt-server.log. There is no streaming goroutine — the child process
// owns the file descriptor directly — so we cannot interpose a size-limiting
// writer without fundamentally changing how the log is captured. This file
// implements the simplest thing that works: a startup-time size check that
// rotates the file if it exceeds a configurable ceiling.
//
// Policy:
//   - At Start() time, if .beads/dolt-server.log is larger than maxLogBytes,
//     rename it to .beads/dolt-server.log.1 (overwriting any existing .log.1)
//     and allow the subsequent OpenFile to create a fresh empty file.
//   - The threshold defaults to DefaultMaxLogBytes (50 MB) and may be
//     overridden with the BEADS_DOLT_LOG_MAX_BYTES env var (bytes).
//   - Rotation failures are non-fatal: if we can't rotate, we log a debug
//     message and fall through to the existing open path so the server still
//     starts. Running with an oversized log is better than refusing to start.
//
// CAVEAT: For dolt-server.log this is a startup-only rotation. A
// long-running server whose log crosses the threshold while it is up will not
// be rotated until the next restart.
//
// Proxied-server mode applies the same startup rename to .beads/dolt/proxy.log
// and the default proxied server.log (never an operator-chosen log path)
// through MaybeRotateLogFile, called from dbproxy/proxy's forkExecChild
// (under proxy.lock, before the child's stdout fd is opened) and
// ListenAndServe (before proxy.log is opened). Because a proxy can run for
// weeks, it also bounds both files while it runs, with
// CopyTruncateLogIfOversized: every writer of those files holds them open
// with O_APPEND, so the file is copied to <path>.1 and emptied in place
// rather than renamed, and the writers' next appends land at the start of the
// emptied file. Lines written between the copy and the truncate are lost;
// that window is one file copy wide and is the price of not reopening fds
// the proxy does not own (its own stdout, inherited from the spawning bd).

package doltserver

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/steveyegge/beads/internal/debug"
)

// DefaultMaxLogBytes is the default size ceiling for dolt-server.log before
// rotation kicks in. 50 MB matches the issue requirements and is large enough
// to hold a meaningful history while far below the 379 MB field report.
const DefaultMaxLogBytes int64 = 50 * 1024 * 1024

// EnvMaxLogBytes is the environment variable that overrides DefaultMaxLogBytes.
// Value is interpreted as a decimal integer number of bytes.
const EnvMaxLogBytes = "BEADS_DOLT_LOG_MAX_BYTES"

// maxLogBytes returns the effective size ceiling, honoring the env override.
// An unset, empty, or unparseable value falls back to the default. A value
// <= 0 disables rotation (returns 0).
func maxLogBytes() int64 {
	v := os.Getenv(EnvMaxLogBytes)
	if v == "" {
		return DefaultMaxLogBytes
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return DefaultMaxLogBytes
	}
	return n
}

// rotatedLogPath returns the path of the single retained rotated log.
// We keep exactly one rotation slot — prior .log.1 is overwritten on each
// rotation. Field reports don't warrant multi-generation retention.
func rotatedLogPath(primary string) string {
	return primary + ".1"
}

// rotateLogIfOversized checks primary's size and, if it is strictly greater
// than maxBytes, renames it to <primary>.1 (overwriting any existing file).
// It returns (rotated, err) where rotated is true only when a rename actually
// happened.
//
// Behavior:
//   - maxBytes <= 0: rotation disabled, returns (false, nil).
//   - primary missing: returns (false, nil). Nothing to rotate.
//   - primary size <= maxBytes: returns (false, nil). Leave alone.
//   - primary size > maxBytes: rename to <primary>.1 and return (true, nil).
//   - rename fails: returns (false, err).
//
// The caller should treat errors as non-fatal and proceed to open the log.
func rotateLogIfOversized(primary string, maxBytes int64) (bool, error) {
	if maxBytes <= 0 {
		return false, nil
	}
	info, err := os.Stat(primary)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", primary, err)
	}
	if info.Size() <= maxBytes {
		return false, nil
	}
	rotated := rotatedLogPath(primary)
	// os.Rename overwrites the destination on both Unix and Windows (Go's
	// Rename wraps MoveFileEx with MOVEFILE_REPLACE_EXISTING on Windows).
	if err := os.Rename(primary, rotated); err != nil {
		return false, fmt.Errorf("rotating %s -> %s: %w", primary, rotated, err)
	}
	return true, nil
}

// maybeRotateLog is the convenience wrapper used by Start() for
// dolt-server.log under beadsDir.
func maybeRotateLog(beadsDir string) {
	MaybeRotateLogFile(logPath(beadsDir))
}

// MaybeRotateLogFile applies the startup rotation policy above to the log at
// path: if it is larger than the effective ceiling it is renamed to
// <path>.1. It emits a debug message on both rotation and error paths and
// never returns an error — rotation is best-effort and must not block server
// startup. Callers must invoke it before any writer has the file open.
func MaybeRotateLogFile(path string) {
	max := maxLogBytes()
	rotated, err := rotateLogIfOversized(path, max)
	if err != nil {
		debug.Logf("doltserver: log rotation failed for %s: %v", path, err)
		return
	}
	if rotated {
		debug.Logf("doltserver: rotated %s -> %s (exceeded %d bytes)", path, rotatedLogPath(path), max)
	}
}

// CopyTruncateLogIfOversized bounds a log that writers still hold open: if
// the file at path is larger than the effective ceiling (DefaultMaxLogBytes,
// or BEADS_DOLT_LOG_MAX_BYTES; <= 0 disables), its contents are copied to
// <path>.1 (through a temp file and a rename, so .1 is never partial and an
// older generation is replaced) and path is then truncated to zero in place.
// Writers must have opened path with O_APPEND, so their next write lands at
// the new end of file instead of leaving a hole. Lines appended between the
// copy and the truncate are lost.
//
// It returns rotated=true only when the file was copied and truncated. When
// the .1 slot cannot be written the primary is left untouched.
func CopyTruncateLogIfOversized(path string) (rotated bool, err error) {
	maxBytes := maxLogBytes()
	if maxBytes <= 0 {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() <= maxBytes {
		return false, nil
	}
	if err := copyToRotatedSlot(path); err != nil {
		return false, err
	}
	if err := os.Truncate(path, 0); err != nil {
		return false, fmt.Errorf("truncating %s after copying it to %s: %w", path, rotatedLogPath(path), err)
	}
	return true, nil
}

// copyToRotatedSlot copies path's current contents to rotatedLogPath(path)
// through a temp file in the same directory, renamed into place.
func copyToRotatedSlot(path string) (retErr error) {
	dst := rotatedLogPath(path)
	src, err := os.Open(path) //nolint:gosec // G304: path is a bd-owned log path, not request input
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = src.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp for %s: %w", dst, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy %s -> %s: %w", path, tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("rotating %s -> %s: %w", path, dst, err)
	}
	return nil
}
