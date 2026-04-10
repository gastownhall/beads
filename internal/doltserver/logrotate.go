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
// CAVEAT: This is a startup-only rotation. A long-running server whose log
// crosses the threshold while it is up will not be rotated until the next
// restart. Implementing true runtime rotation would require either
// (a) a streaming goroutine owning the pipe, or (b) copy-truncate, which
// races with the child's append writes. Both are out of scope for this fix.

package doltserver

import (
	"fmt"
	"os"
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

// maybeRotateLog is the convenience wrapper used by Start(). It rotates the
// dolt-server log if it is oversized and emits a debug message on both
// rotation and error paths. It never returns an error — rotation is
// best-effort and must not block server startup.
func maybeRotateLog(beadsDir string) {
	primary := logPath(beadsDir)
	max := maxLogBytes()
	rotated, err := rotateLogIfOversized(primary, max)
	if err != nil {
		debug.Logf("doltserver: log rotation failed for %s: %v", primary, err)
		return
	}
	if rotated {
		debug.Logf("doltserver: rotated %s -> %s (exceeded %d bytes)", primary, rotatedLogPath(primary), max)
	}
}
