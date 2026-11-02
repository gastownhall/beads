package doltserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/doltversion"
)

// BinaryRecordFileName is the sidecar beside the PID file that names the dolt
// executable a tracked sql-server runs (GH#6064). A sql-server keeps running
// the binary it was exec'd with, so after a package manager replaces dolt the
// server bd started keeps serving the store that clients of the new release
// then write to. The record lets an open tell that server from a current one
// without asking it anything: the recorded executable's file identity still
// matches, or it does not.
const BinaryRecordFileName = "dolt-server.binary"

// binaryRecordSchema is the record format version; a record with any other
// value is ignored, as if absent.
const binaryRecordSchema = 1

// Record provenance: launched is the binary Start exec'd; observed is a
// binary whose version matched the running server's when bd compared them,
// which says nothing about what the server was launched from.
const (
	binaryProvenanceLaunched = "launched"
	binaryProvenanceObserved = "observed"
)

func binaryRecordPath(beadsDir string) string { return filepath.Join(beadsDir, BinaryRecordFileName) }

// binaryIdentity identifies an executable on disk: the path bd was given, the
// file it resolves to, and that file's size and modification time. A package
// manager upgrade either repoints the given path (a symlink into a versioned
// directory) or rewrites the file in place; either changes the identity.
type binaryIdentity struct {
	Path      string `json:"path"`
	Resolved  string `json:"resolved"`
	Size      int64  `json:"size"`
	ModTimeNS int64  `json:"mtime_ns"`
}

func (a binaryIdentity) same(b binaryIdentity) bool {
	return a.Resolved == b.Resolved && a.Size == b.Size && a.ModTimeNS == b.ModTimeNS
}

func identifyBinary(path string) (binaryIdentity, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return binaryIdentity{}, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return binaryIdentity{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return binaryIdentity{}, err
	}
	return binaryIdentity{Path: abs, Resolved: resolved, Size: info.Size(), ModTimeNS: info.ModTime().UnixNano()}, nil
}

// binaryRecord is the content of BinaryRecordFileName.
type binaryRecord struct {
	Schema int `json:"schema"`
	PID    int `json:"pid"`
	binaryIdentity
	// Version is the dolt version the binary reported, "" when unknown.
	Version    string `json:"version,omitempty"`
	Provenance string `json:"provenance"`
}

func readBinaryRecord(beadsDir string) (*binaryRecord, bool) {
	data, err := os.ReadFile(binaryRecordPath(beadsDir)) //nolint:gosec // G304: path derives from the workspace's beads dir
	if err != nil {
		return nil, false
	}
	var rec binaryRecord
	if json.Unmarshal(data, &rec) != nil || rec.Schema != binaryRecordSchema || rec.PID <= 0 || rec.Path == "" {
		return nil, false
	}
	return &rec, true
}

// writeBinaryRecord publishes rec atomically (a temp file renamed over the
// record) with mode 0600, so a concurrent reader sees the old record or the
// new one, never a torn write. Callers hold the lifecycle lock.
func writeBinaryRecord(beadsDir string, rec binaryRecord) error {
	rec.Schema = binaryRecordSchema
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", BinaryRecordFileName, err)
	}
	tmp, err := os.CreateTemp(beadsDir, "."+BinaryRecordFileName+"-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", BinaryRecordFileName, err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmpName, 0o600)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", BinaryRecordFileName, err)
	}
	if err := os.Rename(tmpName, binaryRecordPath(beadsDir)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("writing %s: %w", BinaryRecordFileName, err)
	}
	return nil
}

// binaryVersion is the version doltBin reports, from the memoized probe Start
// already runs for the version floor. ok is false when the probe failed (the
// binary is missing or broken); v is "" when it ran but printed no version.
func binaryVersion(doltBin string) (v string, ok bool) {
	out, ok := probeDoltVersionOutput(doltBin)
	if !ok {
		return "", false
	}
	parsed, err := doltversion.ParseVersion(out)
	if err != nil {
		return "", true
	}
	return parsed.String(), true
}

// writeLaunchRecord records doltBin as the executable of the server Start just
// launched as pid.
func writeLaunchRecord(beadsDir string, pid int, doltBin string) error {
	id, err := identifyBinary(doltBin)
	if err != nil {
		return fmt.Errorf("writing %s: %w", BinaryRecordFileName, err)
	}
	v, _ := binaryVersion(doltBin)
	return writeBinaryRecord(beadsDir, binaryRecord{PID: pid, binaryIdentity: id, Version: v, Provenance: binaryProvenanceLaunched})
}

// lookPathDolt finds the dolt Start would launch next. A var so tests can
// point it elsewhere.
var lookPathDolt = func() (string, error) { return exec.LookPath("dolt") }

// BinaryVerdict is CheckServerBinary's answer.
type BinaryVerdict int

const (
	// BinaryNotTracked: no tracked live server of this workspace answers on
	// the port, so there is nothing to compare.
	BinaryNotTracked BinaryVerdict = iota
	// BinaryCurrent: the server runs the dolt release bd would launch now.
	BinaryCurrent
	// BinaryStale: the server runs a different, known dolt release than the
	// supported one bd would launch now.
	BinaryStale
	// BinaryCannotReplace: the recorded executable changed, but the dolt bd
	// would launch now is missing, broken or below bd's minimum, so the
	// server cannot be replaced with it.
	BinaryCannotReplace
	// BinaryUnknown: a version needed for the comparison could not be
	// determined. That is not evidence of skew; nothing is done.
	BinaryUnknown
)

// BinaryCheck is what CheckServerBinary found about a tracked server.
type BinaryCheck struct {
	Verdict BinaryVerdict
	// PID and Port are the tracked server's.
	PID  int
	Port int
	// RunningVersion is the dolt version the server runs, "" when unknown.
	RunningVersion string
	// CandidatePath and CandidateVersion name the dolt bd would launch now.
	CandidatePath    string
	CandidateVersion string
	// Reason says why the verdict is BinaryCannotReplace or BinaryUnknown.
	Reason error
	// Refresh is set on a current server whose record no longer names the
	// executable bd would launch (the same release was reinstalled):
	// recording that executable lets later opens skip the version probe.
	Refresh bool

	candidate binaryIdentity
}

// trackedLivePID returns the PID of the live server beadsDir tracks on port,
// or 0. It is ManagesLiveServerOnPort's check, returning the PID.
func trackedLivePID(beadsDir string, port int) int {
	if !ManagesLiveServerOnPort(beadsDir, port) {
		return 0
	}
	data, err := os.ReadFile(pidPath(beadsDir))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// ServerVersionFunc asks the running server for the dolt version it runs
// (SELECT dolt_version()).
type ServerVersionFunc func() (string, error)

// CheckServerBinary classifies the dolt sql-server beadsDir tracks on port
// against the dolt bd would launch now. It writes no file, takes no lock and
// signals no process, so any open may call it.
//
// Only a tracked live server is compared (ManagesLiveServerOnPort). With a
// launch record for that PID, an unchanged recorded executable means current,
// whatever dolt the caller's PATH finds; a changed or deleted one means the
// next launch candidate is probed and its version compared with the
// recorded one. A server with no record for its PID (started by a bd that
// wrote none) is asked its version with serverVersion instead; with a nil
// serverVersion its verdict is unknown.
func CheckServerBinary(beadsDir string, port int, serverVersion ServerVersionFunc) BinaryCheck {
	pid := trackedLivePID(beadsDir, port)
	if pid <= 0 {
		return BinaryCheck{Verdict: BinaryNotTracked}
	}
	check := BinaryCheck{PID: pid, Port: port}
	rec, ok := readBinaryRecord(beadsDir)
	if !ok || rec.PID != pid {
		return checkUnrecordedServer(check, serverVersion)
	}
	check.RunningVersion = rec.Version
	if cur, err := identifyBinary(rec.Path); err == nil && cur.same(rec.binaryIdentity) {
		check.Verdict = BinaryCurrent
		check.CandidatePath, check.CandidateVersion = rec.Path, rec.Version
		return check
	}
	if !classifyCandidate(&check) {
		return check
	}
	if rec.Version == "" {
		check.Verdict = BinaryUnknown
		check.Reason = fmt.Errorf("the version of the dolt executable this server was started from (%s) is unknown", rec.Path)
		return check
	}
	if rec.Version != check.CandidateVersion {
		check.Verdict = BinaryStale
		return check
	}
	check.Verdict = BinaryCurrent
	check.Refresh = true
	return check
}

// checkUnrecordedServer classifies a tracked server bd holds no launch record
// for by the version it reports. Without a record nothing says its executable
// changed, so a dolt bd cannot launch makes the verdict unknown, unless that
// dolt reports a release other than the server's.
func checkUnrecordedServer(check BinaryCheck, serverVersion ServerVersionFunc) BinaryCheck {
	if serverVersion == nil {
		check.Verdict = BinaryUnknown
		check.Reason = errors.New("no record of the dolt executable this server runs")
		return check
	}
	raw, err := serverVersion()
	if err != nil {
		check.Verdict = BinaryUnknown
		check.Reason = fmt.Errorf("asking the server for its dolt version: %w", err)
		return check
	}
	running, err := doltversion.ParseVersion(raw)
	if err != nil {
		check.Verdict = BinaryUnknown
		check.Reason = fmt.Errorf("asking the server for its dolt version: %w", err)
		return check
	}
	check.RunningVersion = running.String()
	if !classifyCandidate(&check) {
		if check.Verdict == BinaryCannotReplace && check.CandidateVersion != "" && check.CandidateVersion != check.RunningVersion {
			return check
		}
		check.Verdict = BinaryUnknown
		return check
	}
	if check.CandidateVersion != check.RunningVersion {
		check.Verdict = BinaryStale
		return check
	}
	check.Verdict = BinaryCurrent
	check.Refresh = true
	return check
}

// classifyCandidate fills check's candidate fields from the dolt bd would
// launch now. It returns false, with the verdict set, when that dolt cannot
// replace the server: missing, broken or below bd's minimum (cannot
// replace), or reporting no version (unknown).
func classifyCandidate(check *BinaryCheck) bool {
	doltBin, err := lookPathDolt()
	if err != nil {
		check.Verdict = BinaryCannotReplace
		check.Reason = fmt.Errorf("dolt is not installed (not found in PATH): %w", err)
		return false
	}
	check.CandidatePath = doltBin
	id, err := identifyBinary(doltBin)
	if err != nil {
		check.Verdict = BinaryCannotReplace
		check.Reason = fmt.Errorf("cannot read dolt executable %s: %w", doltBin, err)
		return false
	}
	check.candidate = id
	v, ran := binaryVersion(doltBin)
	if !ran {
		check.Verdict = BinaryCannotReplace
		check.Reason = fmt.Errorf("dolt executable %s does not run (`dolt version` failed)", doltBin)
		return false
	}
	if v == "" {
		check.Verdict = BinaryUnknown
		check.Reason = fmt.Errorf("dolt executable %s reports no version", doltBin)
		return false
	}
	check.CandidateVersion = v
	if err := RequireDoltBinaryVersion(doltBin); err != nil {
		check.Verdict = BinaryCannotReplace
		check.Reason = err
		return false
	}
	return true
}

// BinaryReplacement is what ReplaceStaleServer did.
type BinaryReplacement struct {
	// Replaced is true when the server was stopped and a new one started.
	Replaced bool
	// Check is the classification made under the lifecycle lock.
	Check BinaryCheck
	// State is the replacement server's state when Replaced.
	State *State
}

// ReplaceStaleServer replaces the dolt sql-server beadsDir tracks on port
// when, re-checked under the lifecycle lock, it is still stale. The stop and
// start are Restart's: the SQL port is kept, and stopping first attempts to
// commit every served database's working set (FlushWorkingSet).
//
// A server that is no longer stale under the lock (another bd replaced it
// first) is left alone, so one stale generation is replaced at most once. A
// server that cannot be replaced is left running and the reason returned. A
// current server whose record needs a refresh (BinaryCheck.Refresh) keeps
// running, and the executable it was compared with is recorded as observed.
//
// serverVersion is CheckServerBinary's.
func ReplaceStaleServer(beadsDir string, port int, serverVersion ServerVersionFunc) (BinaryReplacement, error) {
	lockF, err := acquireLifecycleLock(beadsDir)
	if err != nil {
		return BinaryReplacement{}, err
	}
	defer releaseLifecycleLock(lockF)

	check := CheckServerBinary(beadsDir, port, serverVersion)
	res := BinaryReplacement{Check: check}
	switch check.Verdict {
	case BinaryStale:
	case BinaryCurrent:
		if check.Refresh {
			// Best effort: without the refresh the next open probes again.
			_ = writeBinaryRecord(beadsDir, binaryRecord{
				PID: check.PID, binaryIdentity: check.candidate,
				Version: check.CandidateVersion, Provenance: binaryProvenanceObserved,
			})
		}
		return res, nil
	case BinaryCannotReplace:
		return res, check.Reason
	default:
		return res, nil
	}
	state, err := restartLocked(beadsDir, &check.candidate)
	if err != nil {
		return res, err
	}
	res.Replaced, res.State = true, state
	return res, nil
}
