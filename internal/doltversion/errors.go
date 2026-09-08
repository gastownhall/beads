package doltversion

import "errors"

// Canonical error sentinels for this package. All errors returned by
// Resolve/Probe/ProbeWithPolicy wrap one of these with %w, so callers can
// use errors.Is to branch on failure category (e.g. to decide whether to
// print an install hint vs a "check your BEADS_DOLT_BIN setting" hint)
// without parsing message text.
var (
	// ErrNotFound means no candidate dolt binary could be located at all:
	// an explicit env/sidecar path did not exist, or exec.LookPath found
	// nothing on PATH.
	ErrNotFound = errors.New("dolt binary not found")

	// ErrNotExecutable means a candidate path exists but is not usable as
	// an executable: it is a directory, or it is a regular file lacking
	// the executable bit. This is distinct from ErrNotFound because the
	// remediation is different — the path is real, but the file itself is
	// wrong (wrong permissions, wrong kind of file), which is more likely
	// a misconfiguration than a missing install.
	ErrNotExecutable = errors.New("dolt path is not executable")

	// ErrProbeFailed covers everything that can go wrong actually running
	// `<path> version`: the exec call itself failing (including exec
	// format errors from architecture/loader mismatches), the process
	// timing out, or the process exiting non-zero. These are grouped
	// together because callers generally respond to all of them the same
	// way (probe failed, do not proceed) even though the underlying causes
	// differ; the wrapped error's message still carries the specific cause.
	ErrProbeFailed = errors.New("dolt version probe failed")

	// ErrUnparseableVersion means the probe ran and produced output, but
	// ParseVersion could not extract a dotted version number from it —
	// most commonly the signature of a dev/custom build whose `dolt
	// version` output doesn't follow the usual pattern, not of a broken or
	// missing binary. Probe itself still returns this as a hard error (the
	// caller asked for a parsed version and didn't get one), but
	// ProbeWithPolicy demotes it to a *Warning rather than propagating it
	// as an error — see ProbeWithPolicy's doc comment for why.
	ErrUnparseableVersion = errors.New("dolt version output is unparseable")
)
