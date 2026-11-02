package doltserver

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/steveyegge/beads/internal/doltversion"
)

// BinaryFileName is the lifecycle sidecar that records which dolt executable
// a tracked sql-server was launched from (GH#6064). A dolt sql-server runs
// the binary it was exec'd with for its whole lifetime, so after a
// `brew upgrade` (or any replacement of the dolt on PATH) an old server
// keeps serving the store that new-version clients then write to — the
// mixed-generation pairing that corrupted a chunk journal in the issue. The
// record lets a later bd tell, from local state alone, whether the tracked
// server's executable is still the one on disk.
//
// It is JSON, deliberately: the PID file stays a bare integer (its contract),
// and this file is only ever read by bd. Mode 0600, written atomically
// (temp-then-rename) like the port file, always under the lifecycle lock.
const BinaryFileName = "dolt-server.binary"

// Binary provenance values: what bd was doing when it recorded the
// executable a tracked server runs.
const (
	// BinaryProvenanceLaunched: bd launched the server from this executable.
	// The record is launch evidence — the server's process image came from
	// this exact file identity.
	BinaryProvenanceLaunched = "launched"
	// BinaryProvenanceObserved: bd verified the running server's dolt version
	// (via SELECT dolt_version()) and found it equal to this executable's,
	// and recorded the executable as the observed launch candidate. This is
	// correlation evidence, not launch evidence.
	BinaryProvenanceObserved = "observed"
)

// BinaryRecord is the on-disk content of the dolt-server.binary sidecar.
// PID is the tracked server the record describes, so a record left behind by
// a previous generation is ignored rather than trusted.
type BinaryRecord struct {
	PID          int    `json:"pid"`
	GivenPath    string `json:"given_path"`
	RealPath     string `json:"real_path"`
	Size         int64  `json:"size"`
	ModTimeNanos int64  `json:"mtime_nanos"`
	DoltVersion  string `json:"dolt_version,omitempty"`
	KnownVersion bool   `json:"known_version"`
	Provenance   string `json:"provenance"`
}

func binaryPath(beadsDir string) string { return filepath.Join(beadsDir, BinaryFileName) }

// readBinaryRecord returns the recorded binary identity of the tracked
// server, or nil when there is no record or it does not parse. A record that
// does not parse is indistinguishable from a pre-sidecar (legacy) workspace
// for every caller: classification falls back to the bounded server version
// probe instead.
func readBinaryRecord(beadsDir string) *BinaryRecord {
	data, err := os.ReadFile(binaryPath(beadsDir)) //nolint:gosec // G304: fixed file name in the server state dir
	if err != nil {
		return nil
	}
	var rec BinaryRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	if rec.PID <= 0 || rec.RealPath == "" || rec.Provenance == "" {
		return nil
	}
	return &rec
}

// writeBinaryRecord stores rec atomically (temp-then-rename, so a concurrent
// reader never sees a partial file) with mode 0600, like the other server
// state files.
func writeBinaryRecord(beadsDir string, rec *BinaryRecord) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(beadsDir, BinaryFileName+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op after successful rename
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, binaryPath(beadsDir))
}

func removeBinaryRecord(beadsDir string) {
	_ = os.Remove(binaryPath(beadsDir))
}

// binaryIdentity is one on-disk dolt executable as a launch would see it:
// its resolved path and file identity (size and nanosecond mtime — enough to
// distinguish a replaced binary without hashing it, the same identity the
// archive-level probe memoizes by) plus the dolt version it reports.
type binaryIdentity struct {
	givenPath    string // absolute path as resolved for launch (PATH lookup)
	realPath     string // givenPath with symlinks resolved
	size         int64
	modTimeNanos int64
	version      string // "" when unknown
	known        bool   // whether `dolt version` parsed
}

// fileIdentityMatches reports whether the file at path still has the
// recorded size and mtime. An unchanged identity means the executable was
// never replaced, whatever else changed on the machine.
func fileIdentityMatches(path string, size, modTimeNanos int64) bool {
	info, err := os.Stat(path) //nolint:gosec // G304: path comes from the sidecar record
	if err != nil {
		return false
	}
	return info.Size() == size && info.ModTime().UnixNano() == modTimeNanos
}

// captureBinaryIdentity stats and probes the dolt executable at givenPath.
// The version probe reuses probeDoltVersionOutput's memoized `dolt version`
// run, so a binary RequireDoltBinaryVersion already probed costs no second
// subprocess. A version that fails to run or parse is not an error here: the
// identity is still useful (file identity works without it), and an unknown
// version simply cannot prove skew.
func captureBinaryIdentity(givenPath string) (binaryIdentity, error) {
	id := binaryIdentity{givenPath: givenPath}
	abs, err := filepath.Abs(givenPath)
	if err != nil {
		return id, fmt.Errorf("resolving dolt path %q: %w", givenPath, err)
	}
	id.givenPath = abs
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return id, fmt.Errorf("resolving symlinks of %q: %w", abs, err)
	}
	id.realPath = real
	info, err := os.Stat(real) //nolint:gosec // G304: id.realPath is the resolved binary
	if err != nil {
		return id, fmt.Errorf("statting %q: %w", real, err)
	}
	id.size = info.Size()
	id.modTimeNanos = info.ModTime().UnixNano()
	if out, ok := probeDoltVersionOutput(abs); ok {
		if v, perr := doltversion.ParseVersion(out); perr == nil {
			id.version, id.known = v.String(), true
		}
	}
	return id, nil
}

// resolveLaunchCandidate resolves the dolt executable the next Start would
// launch (PATH lookup, like startLocked) and captures its identity. An error
// means there is no usable launch candidate: no dolt on PATH, or a file that
// is not an executable.
//
// A var so tests can hand classification a candidate whose PATH resolution
// they control.
var resolveLaunchCandidate = func() (binaryIdentity, error) {
	doltBin, err := exec.LookPath("dolt")
	if err != nil {
		return binaryIdentity{}, fmt.Errorf("dolt is not installed (not found in PATH)")
	}
	if info, err := os.Stat(doltBin); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return binaryIdentity{}, fmt.Errorf("the dolt at %s is not an executable file", doltBin)
	}
	return captureBinaryIdentity(doltBin)
}

// recordForIdentity builds a BinaryRecord for the tracked server pid from a
// captured executable identity, with the given provenance.
func recordForIdentity(pid int, id binaryIdentity, provenance string) *BinaryRecord {
	return &BinaryRecord{
		PID:          pid,
		GivenPath:    id.givenPath,
		RealPath:     id.realPath,
		Size:         id.size,
		ModTimeNanos: id.modTimeNanos,
		DoltVersion:  id.version,
		KnownVersion: id.known,
		Provenance:   provenance,
	}
}

// writeLaunchedBinaryRecord publishes the launch evidence for a server
// startLocked just launched from doltBin. startLocked calls it under the
// lifecycle lock, right after the PID and port files, with the same failure
// handling: a record that cannot be written fails the start, because a
// server bd cannot later classify is exactly the stale-generation gap GH#6064
// describes.
func writeLaunchedBinaryRecord(beadsDir, doltBin string, pid int) error {
	id, err := captureBinaryIdentity(doltBin)
	if err != nil {
		return fmt.Errorf("capturing dolt binary identity of %q: %w", doltBin, err)
	}
	if err := writeBinaryRecord(beadsDir, recordForIdentity(pid, id, BinaryProvenanceLaunched)); err != nil {
		return fmt.Errorf("writing %s: %w", BinaryFileName, err)
	}
	return nil
}