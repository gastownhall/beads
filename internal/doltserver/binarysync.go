package doltserver

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/doltversion"
)

// GH#6064: a dolt sql-server outlives the binary it was exec'd with, so a
// dolt upgrade leaves old-generation servers serving stores that
// new-generation clients then write to. SyncServerBinary is the store-open
// hook that closes that window for servers bd tracks: it decides, from the
// binary witness sidecar (or, for a legacy server without one, a bounded
// version probe), whether the tracked server still runs the dolt the next
// start would launch — and, when this open may mutate the workspace's
// lifecycle, replaces a stale server before the application's queries run.
//
// The hook never acts on a server bd does not track: a successful dial alone
// says nothing about who owns the listener, so every decision below requires
// the tracked port/live-PID proof of ManagesLiveServerOnPort, and an
// untracked endpoint gets no comparison, no mutation and no output.
//
// For a legacy tracked server — one launched before the sidecar existed —
// there is no launch evidence on disk, so classification falls back to a
// bounded server version probe and compares its answer with the launch
// candidate.

// BinarySyncRequest is what SyncServerBinary needs to know about the open
// that is about to use the server.
type BinarySyncRequest struct {
	// BeadsDir is the workspace whose store is opening (the server state
	// files it consults live in ResolveServerDir(BeadsDir)).
	BeadsDir string
	// Host and Port are the endpoint the open resolved and dialed. Port must
	// be the tracked server's port; the hook does nothing for port 0.
	Host string
	Port int
	// User and Password are the open's configured server credentials, used
	// only for the legacy version probe.
	User     string
	Password string //nolint:gosec // G117: server credential passed through to the probe's DSN, never serialized
	// AllowRestart reports whether this open may mutate the workspace's
	// server lifecycle: replace a stale server, or refresh the witness
	// sidecar. The store layer computes it from its own open options
	// (preview, strict readonly, migration freeze, test mode and
	// auto-start eligibility); when false the hook is advisory only.
	AllowRestart bool
}

// binaryVerdictKind is what classification proved about a tracked server's
// dolt executable.
type binaryVerdictKind int

const (
	// binaryCurrent: the recorded executable is unchanged on disk, or it was
	// replaced by an equal-version binary. The server is not stale; its PID
	// stays.
	binaryCurrent binaryVerdictKind = iota
	// binaryStale: the recorded executable was replaced by a known-version
	// dolt that differs from the one the server runs.
	binaryStale
	// binaryUnknown: skew could not be proven or excluded (an unknown
	// version on either side, or an unreachable server). Never a reason to
	// restart or warn.
	binaryUnknown
	// binaryCannotReplace: the recorded executable was replaced, but the
	// replacement cannot serve as the next launch candidate (missing,
	// broken, or below the required floor).
	binaryCannotReplace
)

// binaryVerdict is classification's result for one tracked server.
type binaryVerdict struct {
	kind binaryVerdictKind
	// trackedPID is the PID the verdict was reached for.
	trackedPID int
	// serverVersion is the version evidence for the running server: the
	// record's version, or the SQL probe's answer on the legacy path.
	serverVersion string
	// candidate is the launch candidate the replacement would use, when one
	// was resolved.
	candidate binaryIdentity
	// problem explains binaryCannotReplace.
	problem string
	// refresh is set when the server is current but the record should be
	// refreshed: an equal-version replacement, so the next open does not
	// re-probe it.
	refresh bool
}

// SyncServerBinary is the store-open lifecycle hook (GH#6064). It classifies
// the tracked server on req.Port against the dolt executable the next start
// would launch, and replaces it under the lifecycle lock when it is stale
// and this open may mutate the lifecycle.
//
// It returns nil for every verdict that is not actionable; an error is the
// open's to fail on, and means bd will not query the tracked stale server
// — the old server is preserved, and its evidence stays so the next open
// cannot forget the refusal.
func SyncServerBinary(req BinarySyncRequest) error {
	if req.BeadsDir == "" || req.Port <= 0 {
		return nil
	}
	serverDir := resolveServerDir(req.BeadsDir)
	// Untracked endpoint (AC-7): no comparison, no mutation, no output. This
	// cheap proof is deliberately not authority to signal anything; the only
	// process ever signaled is the one the PID file names and the existing
	// stop path verifies.
	if !ManagesLiveServerOnPort(serverDir, req.Port) {
		return nil
	}
	trackedPID := readTrackedPID(serverDir)
	if trackedPID <= 0 {
		return nil
	}
	verdict := classifyServerBinary(serverDir, trackedPID, req)
	switch verdict.kind {
	case binaryCurrent:
		if verdict.refresh {
			recordObservedBinary(serverDir, trackedPID, verdict.candidate, req)
		}
		return nil
	case binaryStale:
		if !binaryMutationPermitted(req) {
			return nil
		}
		return replaceStaleServer(serverDir, req, verdict)
	default:
		// Unknown and cannot-replace leave the connection behavior
		// unchanged.
		return nil
	}
}

// readTrackedPID returns the PID the PID file names, or 0 when there is no
// usable record. Read-only: classification must not mutate state the way
// IsRunning repairs it.
func readTrackedPID(beadsDir string) int {
	data, err := os.ReadFile(pidPath(beadsDir)) //nolint:gosec // G304: fixed file name in the server state dir
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(trimSpaceBytes(data))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func trimSpaceBytes(b []byte) string {
	s := string(b)
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\n' || s[0] == '\t' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 {
		last := s[len(s)-1]
		if last == ' ' || last == '\n' || last == '\t' || last == '\r' {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// classifyServerBinary decides whether the tracked server on the port this
// open dialed still runs the dolt the next launch would use. It never
// mutates anything and never signals anything; its caller decides what a
// verdict warrants.
//
// With a usable record (present, parses, and naming this tracked PID):
//
//   - the recorded executable still on disk with its recorded identity means
//     current, whatever PATH would resolve now — the server's binary never
//     changed, so no opener's PATH matters;
//   - a changed or deleted executable means the machine's dolt was replaced
//     since the launch, so the candidate the next start would launch decides:
//     a known version that differs is stale, an equal version is current, a
//     missing or broken candidate cannot replace, and anything else is
//     unknown.
//
// Without a usable record (a legacy server, or one whose record names
// another PID) there is no launch evidence, so a bounded version probe
// against the server — with the open's configured credentials — is compared
// with the launch candidate: different versions are stale, equal versions are
// current with the observed identity recorded, and a failed or unparseable
// probe is unknown.
func classifyServerBinary(serverDir string, trackedPID int, req BinarySyncRequest) binaryVerdict {
	base := binaryVerdict{trackedPID: trackedPID}
	rec := readBinaryRecord(serverDir)
	if rec == nil || rec.PID != trackedPID {
		// Legacy tracked server: no launch evidence on disk (or a record
		// left behind by a previous generation). Ask the server itself,
		// bounded, and compare its answer with the launch candidate.
		// Different versions are the same skew a replaced executable is;
		// equal versions retain the PID and record the observed identity
		// (provenance observed: the probe proves correlation, not which
		// binary started the process); anything else — an unreachable
		// server, an unparseable version, no usable candidate — is
		// unknown, never actionable.
		serverVersion, ok := probeServerDoltVersion(req)
		if !ok {
			base.kind = binaryUnknown
			return base
		}
		base.serverVersion = serverVersion
		cand, err := resolveLaunchCandidate()
		if err != nil || !cand.known {
			base.kind = binaryUnknown
			return base
		}
		base.candidate = cand
		if cand.version != serverVersion {
			base.kind = binaryStale
			return base
		}
		base.kind, base.refresh = binaryCurrent, true
		return base
	}
	base.serverVersion = rec.DoltVersion
	if fileIdentityMatches(rec.RealPath, rec.Size, rec.ModTimeNanos) {
		base.kind = binaryCurrent
		return base
	}
	cand, err := resolveLaunchCandidate()
	if err != nil {
		base.kind, base.problem = binaryCannotReplace, err.Error()
		return base
	}
	base.candidate = cand
	if !cand.known || !rec.KnownVersion {
		base.kind = binaryUnknown
		return base
	}
	if cand.version != rec.DoltVersion {
		base.kind = binaryStale
		return base
	}
	// Equal version, new file identity: retain the PID, and refresh the
	// record so the next open does not re-probe the replacement.
	base.kind, base.refresh = binaryCurrent, true
	return base
}

// binaryProbeTimeout bounds the legacy version probe's SQL query (and its
// connection), so a wedged server leaves classification "unknown" quickly
// instead of stalling the open.
const binaryProbeTimeout = 5 * time.Second

// probeServerDoltVersion asks the running server for its dolt version with
// the open's configured credentials, bounded by binaryProbeTimeout. A var so
// tests can stub the wire probe without a SQL server.
var probeServerDoltVersion = func(req BinarySyncRequest) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), binaryProbeTimeout)
	defer cancel()
	user := req.User
	if user == "" {
		user = "root"
	}
	dsn := doltutil.ServerDSN{
		Host:     req.Host,
		Port:     req.Port,
		User:     user,
		Password: req.Password,
		Timeout:  binaryProbeTimeout,
	}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return "", false
	}
	defer db.Close() //nolint:errcheck // best-effort close of a probe connection
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(binaryProbeTimeout)
	v, err := doltversion.ServerVersion(ctx, db)
	if err != nil {
		return "", false
	}
	return v.String(), true
}

// binaryMutationPermitted reports whether this open may mutate the
// workspace's server lifecycle: it carries the store's restart permission,
// auto-start is not disabled, and the workspace's topology is one bd owns
// (not shared-server mode, which serves many workspaces, and not an
// externally declared mode). Topology is re-derived here rather than trusted
// from the caller so an advisory can explain it, and so an external
// declaration cannot be missed by a caller that forgot to check.
func binaryMutationPermitted(req BinarySyncRequest) bool {
	if !req.AllowRestart || IsAutoStartDisabled() {
		return false
	}
	if IsSharedServerModeForDir(req.BeadsDir) {
		return false
	}
	return ResolveServerModeIgnoringPortEnv(req.BeadsDir) == ServerModeOwned
}

// recordObservedBinary refreshes the witness sidecar for a server that was
// classified current through a fresh observation: an equal-version
// replacement of the executable. Under the lifecycle lock, the tracked
// port/PID is revalidated before the write, so the record never describes a
// server that was replaced while the observation was being made. A write
// failure is a warning, not an error: the observation is an optimization,
// and the next open re-derives it.
func recordObservedBinary(serverDir string, trackedPID int, cand binaryIdentity, req BinarySyncRequest) {
	if !binaryMutationPermitted(req) {
		return
	}
	lockF, err := acquireLifecycleLock(serverDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not record the current dolt binary for the tracked server: %v\n", err)
		return
	}
	defer releaseLifecycleLock(lockF)
	if !ManagesLiveServerOnPort(serverDir, req.Port) || readTrackedPID(serverDir) != trackedPID {
		return // replaced while classifying; the new generation writes its own record
	}
	if err := writeBinaryRecord(serverDir, recordForIdentity(trackedPID, cand, BinaryProvenanceObserved)); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not record the current dolt binary for the tracked server: %v\n", err)
	}
}

// replaceStaleServer replaces the stale-generation server under the
// lifecycle lock, revalidating the tracked state and the classification
// itself under the lock so concurrent detections of one stale generation
// produce at most one replacement. Failures return an error for the open
// to fail on (AC-5): a stop whose exit is unconfirmed preserves the tracked
// evidence, and a failed start leaves the server stopped with the port
// restored, exactly as Restart reports.
func replaceStaleServer(serverDir string, req BinarySyncRequest, stale binaryVerdict) error {
	lockF, err := acquireLifecycleLock(serverDir)
	if err != nil {
		return fmt.Errorf("replacing the stale dolt sql-server on port %d: %w", req.Port, err)
	}
	defer releaseLifecycleLock(lockF)

	// Everything this caller decided is revalidated under the lock: another
	// bd may have replaced this generation while this caller waited for it.
	// The tracked port and PID must still be the ones the verdict was
	// reached against — a concurrent replacement rewrites both.
	if !ManagesLiveServerOnPort(serverDir, req.Port) {
		return nil
	}
	if pid := readTrackedPID(serverDir); pid != stale.trackedPID {
		return nil
	}
	// And the classification itself must still say stale. A caller whose
	// verdict went obsolete finds the server current here and stands down:
	// the concurrent caller's restart is the only one, so a stale
	// generation is replaced at most once no matter how many opens
	// detected it.
	verdict := classifyServerBinary(serverDir, stale.trackedPID, req)
	if verdict.kind != binaryStale {
		return nil
	}

	state, err := restartLocked(serverDir)
	if err != nil {
		return fmt.Errorf("replacing the stale dolt sql-server on port %d (running dolt %s, replaced by dolt %s): %w\n"+
			"bd will not query the old server after a failed replacement; resolve the failure, then retry",
			req.Port, verdict.serverVersion, verdict.candidate.version, err)
	}
	fmt.Fprintf(os.Stderr,
		"Info: replaced the tracked dolt sql-server on port %d (PID %d, running dolt %s) with dolt %s (PID %d): the binary it was started from was replaced by an upgrade.\n"+
			"The stop commits each database's uncommitted working set when it can reach them, so no finished work is lost (see any flush warnings above).\n",
		req.Port, stale.trackedPID, verdict.serverVersion, verdict.candidate.version, state.PID)
	return nil
}

// ServerBinaryPath returns the path of the binary witness sidecar for
// beadsDir, for diagnostics and tests.
func ServerBinaryPath(beadsDir string) string {
	return binaryPath(beadsDir)
}