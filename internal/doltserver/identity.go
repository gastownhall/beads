package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Server identity verification (GH#4637): a successful TCP dial to a managed
// endpoint is not proof that the answering process is the server bd runs for
// the workspace's data root. WSL2's mirrored networking, a docker or ssh port
// forward, or a stale port record can all put a foreign dolt server behind the
// configured host:port, and without an identity check bd silently reads and
// writes the wrong project's database.
//
// The mechanism is the server's own @@server_uuid — already stable across
// restarts and persisted in the data directory — paired with a bd-owned
// record at <data root>/.bd-server-uuid. The record is written ONLY by a
// launch bd can prove: a child this process started that positively owns the
// listener it answers on (see pairServerIdentity). Every managed open then
// refuses before discovery or mutation when the record is missing, invalid
// or mismatched, so a foreign server cannot silently receive traffic. An
// unpaired server (started before this change, or by something else) is
// deliberately not adopted: the operator inspects it and restarts it through
// bd, or explicitly disables the gate.

// ServerIdentityRecordName is the record file bd keeps inside the resolved
// dolt data root. It lives with the served data, not with the PID/port state
// files, so recreating the data directory drops it too and a fresh data root
// never inherits a stale identity.
const ServerIdentityRecordName = ".bd-server-uuid"

// verifyIdentityEnv explicitly enables or disables the identity gate for this
// process. Only a recognized boolean spelling disables verification; an
// invalid value never does.
const verifyIdentityEnv = "BEADS_DOLT_VERIFY_SERVER_IDENTITY"

// pairingQueryTimeout bounds the identity query against a freshly launched
// child: the listener already answered a greeting, so a slow answer here is a
// sick child, not a cold start.
const pairingQueryTimeout = 5 * time.Second

// ErrServerIdentityMismatch is wrapped by every identity-gate refusal: a
// mismatched, missing, malformed, blank or unreadable record, or an answering
// server whose identity could not be read. Callers treat it as terminal —
// never a signal to adopt, re-pair or delete anything.
var ErrServerIdentityMismatch = errors.New("dolt server identity mismatch")

// ServerIdentityExpectation is the plain, driver-free description of the
// managed server an open or init is about to use: which data root's record
// applies, which endpoint answers, and where the state files for diagnostics
// live. A nil *ServerIdentityExpectation exempts the endpoint from the gate.
type ServerIdentityExpectation struct {
	// DataDir is the resolved dolt data root the server serves; the record
	// lives inside it (ServerIdentityRecordName).
	DataDir string
	// ServerDir is the server state directory (PID/port files), used only for
	// best-effort diagnostics in refusal messages.
	ServerDir string
	// Host and Port are the endpoint the open dials.
	Host string
	Port int
	// Shared marks a shared-server-mode endpoint.
	Shared bool
}

// RecordPath is where the identity record for the expectation's data root
// lives.
func (e ServerIdentityExpectation) RecordPath() string {
	return filepath.Join(e.DataDir, ServerIdentityRecordName)
}

// Addr is the endpoint as errors and diagnostics name it.
func (e ServerIdentityExpectation) Addr() string {
	return doltutil.ServerAddr(e.Host, e.Port)
}

// SharedServerIdentityExpectation returns the identity expectation for the
// shared server at host:port, or nil when verification is disabled for
// beadsDir's configuration. It does not check mode or host eligibility; the
// caller does (the shared server is the one endpoint shared mode always
// manages, whatever resolved the port).
func SharedServerIdentityExpectation(beadsDir, host string, port int) *ServerIdentityExpectation {
	if VerifyServerIdentityDisabled(beadsDir) {
		return nil
	}
	dataDir, err := SharedDoltDir()
	if err != nil {
		return nil
	}
	serverDir, err := SharedServerDir()
	if err != nil {
		return nil
	}
	return &ServerIdentityExpectation{
		DataDir:   dataDir,
		ServerDir: serverDir,
		Host:      host,
		Port:      port,
		Shared:    true,
	}
}

// WorkspaceIdentityExpectation returns the identity expectation for beadsDir's
// own managed server at host:port, or nil when verification is disabled. The
// caller is responsible for mode and port-source eligibility.
func WorkspaceIdentityExpectation(beadsDir, host string, port int) *ServerIdentityExpectation {
	if VerifyServerIdentityDisabled(beadsDir) {
		return nil
	}
	return &ServerIdentityExpectation{
		DataDir:   ResolveDoltDir(beadsDir),
		ServerDir: ResolveServerDir(beadsDir),
		Host:      host,
		Port:      port,
	}
}

// parseVerifyBool is the boolean spelling the identity knob accepts: anything
// strconv.ParseBool recognizes, plus "on"/"off" case-insensitively, with
// surrounding whitespace trimmed — the same spellings dolt.auto-start reads.
func parseVerifyBool(raw string) (value, valid bool) {
	s := strings.TrimSpace(raw)
	switch {
	case strings.EqualFold(s, "on"):
		return true, true
	case strings.EqualFold(s, "off"):
		return false, true
	}
	b, err := strconv.ParseBool(s)
	return b, err == nil
}

// VerifyServerIdentityDisabled reports whether the operator explicitly
// disabled server identity verification: the BEADS_DOLT_VERIFY_SERVER_IDENTITY
// env var, then the loaded configuration's dolt.verify-server-identity,
// falling back to the target workspace's own config.yaml like the store's
// other target-aware settings. Verification is on by default; only a
// recognized false spelling of the env var or the config setting disables it.
// An invalid value never disables the gate.
func VerifyServerIdentityDisabled(beadsDir string) bool {
	if raw, ok := os.LookupEnv(verifyIdentityEnv); ok && strings.TrimSpace(raw) != "" {
		if disabled, valid := parseVerifyBool(raw); valid {
			return !disabled
		}
		// An invalid env value is not an opt-out; fall through to the
		// configuration rather than disabling the gate.
	}
	setting := config.GetString("dolt.verify-server-identity")
	if setting == "" && beadsDir != "" {
		setting = config.GetStringFromDir(beadsDir, "dolt.verify-server-identity")
	}
	return isFalsyBool(setting)
}

// serverUUIDRecordProblem classifies why a record could not be used, so the
// refusal can tell the operator which half is broken — the record or the
// answering server — and never blames one for the other.
type serverUUIDRecordProblem string

const (
	recordOK         serverUUIDRecordProblem = ""
	recordAbsent     serverUUIDRecordProblem = "absent"
	recordBlank      serverUUIDRecordProblem = "blank"
	recordMalformed  serverUUIDRecordProblem = "malformed"
	recordUnreadable serverUUIDRecordProblem = "unreadable"
)

// readServerUUIDRecord reads the identity record at path. It is a package
// var so tests can force read failures deterministically where filesystem
// permissions cannot.
var readServerUUIDRecord = defaultReadServerUUIDRecord

func defaultReadServerUUIDRecord(path string) (uuid string, problem serverUUIDRecordProblem, err error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from the workspace's data root
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", recordAbsent, nil
		}
		return "", recordUnreadable, err
	}
	u := strings.TrimSpace(string(data))
	switch {
	case u == "":
		return "", recordBlank, nil
	case strings.ContainsAny(u, " \t\n\r\x00") || len(u) > 72:
		// A UUID is one whitespace-free token; anything else in the record
		// (or an absurd length) means it was not written by this gate.
		return "", recordMalformed, nil
	}
	return u, recordOK, nil
}

// serverIdentityListenerPID is the best-effort "which process holds the port"
// lookup used only in refusal diagnostics. It is a package var so tests can
// prove the matching path never performs a listener lookup (the normal open
// must stay lookup-free, AC-5).
var serverIdentityListenerPID = findPIDOnPort

// ServerIdentityMismatchError is the identity gate's refusal. It wraps
// ErrServerIdentityMismatch and names the endpoint, the record (with its data
// root), the expected and actual identities as far as each is known, and
// best-effort PIDs. Its advice never suggests deleting or reinitializing a
// database.
type ServerIdentityMismatchError struct {
	Addr string
	// DataRoot and RecordPath locate the expected identity.
	DataRoot   string
	RecordPath string
	// RecordedUUID is the record's UUID; empty when the record itself is the
	// problem (Problem != "").
	RecordedUUID string
	// ActualUUID is the answering server's @@server_uuid; empty when it could
	// not be read (QueryErr != "").
	ActualUUID string
	// Problem classifies a record that could not be used: absent, blank,
	// malformed or unreadable. Empty for a clean mismatch.
	Problem serverUUIDRecordProblem
	// ReadErr and QueryErr name the underlying failures for unreadable
	// records and unreadable server identities.
	ReadErr  error
	QueryErr error
	// RecordedPID and ListenerPID are best-effort diagnostics.
	RecordedPID int
	ListenerPID int
}

func (e *ServerIdentityMismatchError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to use the Dolt server at %s: ",
		remedy.PrintedValue(e.Addr))
	switch {
	case e.Problem == recordAbsent:
		fmt.Fprintf(&b, "bd has no recorded identity for the data root %s (%s does not exist).\n",
			remedy.PrintedValue(e.DataRoot), remedy.PrintedValue(e.RecordPath))
		b.WriteString("bd only records a server's identity when it proves it started that server for this data root, so a server\n" +
			"that was already listening — whatever started it — is deliberately not trusted here.\n")
	case e.Problem == recordBlank:
		fmt.Fprintf(&b, "the recorded identity at %s is blank and cannot be trusted.\n",
			remedy.PrintedValue(e.RecordPath))
	case e.Problem == recordMalformed:
		fmt.Fprintf(&b, "the recorded identity at %s is malformed and cannot be trusted.\n",
			remedy.PrintedValue(e.RecordPath))
	case e.Problem == recordUnreadable:
		fmt.Fprintf(&b, "the recorded identity at %s could not be read: %v\n",
			remedy.PrintedValue(e.RecordPath), e.ReadErr)
	case e.QueryErr != nil:
		fmt.Fprintf(&b, "the identity of the answering server could not be read: %v\n", e.QueryErr)
	default:
		fmt.Fprintf(&b, "it is not the server bd recorded for the data root %s.\n",
			remedy.PrintedValue(e.DataRoot))
		fmt.Fprintf(&b, "  expected identity (server UUID) %s from %s\n",
			e.RecordedUUID, remedy.PrintedValue(e.RecordPath))
		fmt.Fprintf(&b, "  the server reported identity (server UUID) %s\n", e.ActualUUID)
	}
	if e.RecordedPID > 0 || e.ListenerPID > 0 {
		fmt.Fprintf(&b, "  recorded server PID %d; the port is currently held by PID %d (both best-effort)\n",
			e.RecordedPID, e.ListenerPID)
	}
	b.WriteString("A connection that answers on the configured port is not proof it is your server: a mirrored\n" +
		"WSL2 listener, a docker or ssh port forward, or a stale port record can all answer here.\n\n" +
		"To proceed:\n" +
		"  - diagnose what holds or forwards the port: " + fmt.Sprintf(portConflictHint, portOf(e.Addr)) + "\n" +
		"  - run the intended server on a separate port, or stop the process that answers on this one\n" +
		"  - if this is the intended server, stop and start it again with 'bd dolt start' so bd can pair with it\n" +
		"  - to trust this endpoint deliberately, set dolt.verify-server-identity: false (or " + verifyIdentityEnv + "=0)")
	return b.String()
}

func (e *ServerIdentityMismatchError) Unwrap() error { return ErrServerIdentityMismatch }

// portOf extracts the port from an addr as ServerAddr built it; used only in
// advice text. 0 when the address names none.
func portOf(addr string) int {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 {
		if p, err := strconv.Atoi(addr[i+1:]); err == nil {
			return p
		}
	}
	return 0
}

// VerifyServerIdentity checks that the server q is connected to is the one
// recorded for exp's data root, before any discovery, creation or project
// statement runs through q. It never writes or replaces the record: a normal
// open may only read. Every refusal is a *ServerIdentityMismatchError, and
// the failure-path diagnostics (recorded/listener PIDs) never run on the
// matching path.
func VerifyServerIdentity(ctx context.Context, q doltversion.RowQuerier, exp ServerIdentityExpectation) error {
	uuid, problem, err := readServerUUIDRecord(exp.RecordPath())
	if problem != recordOK || err != nil {
		// AC-1 (this commit) refuses only a proven mismatch; a record that is
		// missing, blank, malformed or unreadable is soft-skipped so workspaces
		// without a paired record keep opening. The next commit (AC-2) flips
		// these states to refusals: an unprovable record cannot authorize a
		// server either.
		return nil
	}
	var actual sql.NullString
	queryErr := q.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&actual)
	if queryErr != nil {
		return (&ServerIdentityMismatchError{
			Addr:         exp.Addr(),
			DataRoot:     exp.DataDir,
			RecordPath:   exp.RecordPath(),
			RecordedUUID: uuid,
			QueryErr:     queryErr,
		}).withBestEffortPIDs(exp)
	}
	reported := strings.TrimSpace(actual.String)
	if !actual.Valid || reported == "" {
		return (&ServerIdentityMismatchError{
			Addr:         exp.Addr(),
			DataRoot:     exp.DataDir,
			RecordPath:   exp.RecordPath(),
			RecordedUUID: uuid,
			QueryErr:     errors.New("the server reported an empty @@server_uuid"),
		}).withBestEffortPIDs(exp)
	}
	if reported != uuid {
		return (&ServerIdentityMismatchError{
			Addr:         exp.Addr(),
			DataRoot:     exp.DataDir,
			RecordPath:   exp.RecordPath(),
			RecordedUUID: uuid,
			ActualUUID:   reported,
		}).withBestEffortPIDs(exp)
	}
	return nil
}

// withBestEffortPIDs adds the recorded PID (the server-dir PID file) and the
// listener PID (who holds the port right now) to a refusal. Both are
// diagnostics on the failure path only; the matching path never looks any
// process up.
func (e *ServerIdentityMismatchError) withBestEffortPIDs(exp ServerIdentityExpectation) *ServerIdentityMismatchError {
	if data, err := os.ReadFile(filepath.Join(exp.ServerDir, PIDFileName)); err == nil {
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
			e.RecordedPID = pid
		}
	}
	if pid := serverIdentityListenerPID(exp.Port); pid > 0 {
		e.ListenerPID = pid
	}
	return e
}
