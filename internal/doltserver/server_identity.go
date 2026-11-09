package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Server identity (GH#4637). A successful dial to a managed endpoint's port
// does not prove that the server answering is the one serving this machine's
// data root: under WSL2's mirrored networking a Windows bd dials a WSL
// server on the same loopback port and, without a check, reads and writes
// the wrong project's databases. bd therefore keeps, inside the data root it
// expects the server to serve, the @@server_uuid of the dolt sql-server it
// last launched there and proved owned its listener (pairServerIdentity),
// and a store open or shared init against a managed endpoint compares the
// answering server's @@server_uuid with that record before it lists,
// creates or reads any database (VerifyServerIdentity).

// ServerIdentityFileName is the record, inside the server's data root, of the
// @@server_uuid of the dolt sql-server bd last launched there.
const ServerIdentityFileName = ".bd-server-uuid"

// VerifyServerIdentityKey is the config.yaml setting that turns the server
// identity check off (false) for managed endpoints; it is on by default.
const VerifyServerIdentityKey = "dolt.verify-server-identity"

// VerifyServerIdentityEnv overrides VerifyServerIdentityKey.
const VerifyServerIdentityEnv = "BEADS_DOLT_VERIFY_SERVER_IDENTITY"

// ErrServerIdentityMismatch is wrapped by every refusal of the server
// identity check: the answering server's identity could not be shown to be
// the recorded one, because it differs (a foreign server), or because there
// is no usable record or no identity to compare it with.
var ErrServerIdentityMismatch = errors.New("dolt server identity not verified")

// ServerIdentityExpectation is what a managed endpoint is expected to be: the
// data root whose record names the server bd launched there, and where that
// server is reached and tracked. It is plain data so the store can carry it
// without depending on the driver.
type ServerIdentityExpectation struct {
	// DataDir is the data root the server is expected to serve; it holds
	// the identity record (ServerIdentityFileName).
	DataDir string
	// ServerDir is where the server's PID file lives, for diagnostics.
	ServerDir string
	// Host and Port are the endpoint dialed.
	Host string
	Port int
}

// RecordPath is the identity record's path.
func (e *ServerIdentityExpectation) RecordPath() string {
	return ServerIdentityRecordPath(e.DataDir)
}

// ServerIdentityRecordPath is the identity record's path in dataDir.
func ServerIdentityRecordPath(dataDir string) string {
	return filepath.Join(dataDir, ServerIdentityFileName)
}

// uuidPattern is the shape of a dolt @@server_uuid.
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// normalizeServerUUID returns s trimmed and lower-cased, and whether it is a
// UUID.
func normalizeServerUUID(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	return s, uuidPattern.MatchString(s)
}

// maxIdentityRecordSize bounds the record read: a UUID and a newline.
const maxIdentityRecordSize = 256

// readServerIdentityFile reads the record; tests replace it.
var readServerIdentityFile = os.ReadFile

// readServerIdentityRecord returns the UUID recorded in dataDir, or a
// description of why there is none usable.
func readServerIdentityRecord(dataDir string) (uuid string, problem string) {
	data, err := readServerIdentityFile(ServerIdentityRecordPath(dataDir))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", "does not exist: no server bd launched has been paired with this data directory"
	case err != nil:
		return "", "could not be read: " + err.Error()
	case strings.TrimSpace(string(data)) == "":
		return "", "is empty"
	case len(data) > maxIdentityRecordSize:
		return "", "is malformed (not a server UUID)"
	}
	uuid, ok := normalizeServerUUID(string(data))
	if !ok {
		return "", "is malformed (not a server UUID)"
	}
	return uuid, ""
}

// ServerIdentityError is the server identity check's refusal. It wraps
// ErrServerIdentityMismatch.
type ServerIdentityError struct {
	// Endpoint is the address dialed, printed, and Port its port.
	Endpoint   string
	Port       int
	RecordPath string
	// Expected is the recorded UUID, or "" when RecordProblem says why
	// there is none.
	Expected      string
	RecordProblem string
	// Actual is the answering server's UUID, or "" when ActualErr says why
	// it could not be read.
	Actual    string
	ActualErr error
	// RecordedPID is the PID in the server's PID file, ListenerPID the PID
	// holding the port; 0 when unknown. Both are best effort.
	RecordedPID int
	ListenerPID int
}

func (e *ServerIdentityError) Unwrap() error { return ErrServerIdentityMismatch }

func (e *ServerIdentityError) Error() string {
	var b strings.Builder
	switch {
	case e.RecordProblem == "" && e.Actual != "":
		fmt.Fprintf(&b, "refusing the Dolt server at %s: it is not the server bd launched for this data directory "+
			"(expected server UUID %s, recorded in %s; the answering server reports %s)",
			e.Endpoint, e.Expected, remedy.PrintedValue(e.RecordPath), e.Actual)
	case e.RecordProblem == "":
		fmt.Fprintf(&b, "refusing the Dolt server at %s: could not read its server UUID to compare with %s, recorded in %s: %s",
			e.Endpoint, e.Expected, remedy.PrintedValue(e.RecordPath), remedy.PrintedTextError(e.ActualErr))
	default:
		fmt.Fprintf(&b, "refusing the Dolt server at %s: its identity cannot be verified because the server identity record %s %s",
			e.Endpoint, remedy.PrintedValue(e.RecordPath), e.RecordProblem)
		switch {
		case e.Actual != "":
			fmt.Fprintf(&b, " (the answering server reports server UUID %s)", e.Actual)
		case e.ActualErr != nil:
			fmt.Fprintf(&b, " (the answering server's UUID could not be read: %s)", remedy.PrintedTextError(e.ActualErr))
		}
	}
	b.WriteString(".")
	if e.RecordedPID > 0 || e.ListenerPID > 0 {
		b.WriteString("\n")
		if e.RecordedPID > 0 {
			fmt.Fprintf(&b, "\nbd's server PID file records PID %d.", e.RecordedPID)
		}
		if e.ListenerPID > 0 {
			fmt.Fprintf(&b, "\nThe port is held by PID %d.", e.ListenerPID)
		}
	}
	b.WriteString("\n\nbd read and wrote nothing on this server. To proceed:\n" +
		"  - Find what answers on the port; another bd (such as one in WSL2, whose loopback ports Windows shares), " +
		"a forward or a container may hold it: " + fmt.Sprintf(portConflictHint, e.Port) + "\n" +
		"  - Give the two servers separate ports (BEADS_DOLT_SERVER_PORT or dolt.port)\n")
	if e.RecordProblem != "" && e.Actual != "" {
		b.WriteString("  - If this is this machine's own bd server, started before bd verified server identity, " +
			"stop it and start it again so bd records its identity: bd dolt stop && bd dolt start\n")
	} else {
		b.WriteString("  - If it should be this machine's own bd server, stop it and start it again so bd records its identity: " +
			"bd dolt stop && bd dolt start\n")
	}
	b.WriteString("  - If the server is meant to be one bd does not manage, use external mode (BEADS_DOLT_SERVER_MODE=1), " +
		"or turn the check off: " + VerifyServerIdentityEnv + "=false or " + VerifyServerIdentityKey + ": false")
	return b.String()
}

// QueryServerUUID reads the answering server's @@server_uuid.
func QueryServerUUID(ctx context.Context, q doltversion.RowQuerier) (string, error) {
	var raw sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&raw); err != nil {
		return "", fmt.Errorf("reading @@server_uuid: %w", err)
	}
	uuid, ok := normalizeServerUUID(raw.String)
	if !raw.Valid || !ok {
		return "", fmt.Errorf("server reported @@server_uuid %q, which is not a server UUID", raw.String)
	}
	return uuid, nil
}

// identityListenerPID and identityRecordedPID are the best-effort PID
// diagnostics of a refusal; tests replace them. They run only when the
// check refuses, never on a matching open.
var (
	identityListenerPID = findPIDOnPort
	identityRecordedPID = func(serverDir string) int {
		data, err := os.ReadFile(pidPath(serverDir)) //nolint:gosec // G304: bd's own state directory
		if err != nil {
			return 0
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 {
			return 0
		}
		return pid
	}
)

// VerifyServerIdentity checks that the server q is connected to is the one
// exp's record names, and returns a *ServerIdentityError (wrapping
// ErrServerIdentityMismatch) when it is not, or cannot be shown to be. A nil
// exp, an exempt endpoint, checks nothing. It only reads the record and the
// server's @@server_uuid; it never writes the record, so a foreign server
// answering never replaces what bd recorded.
func VerifyServerIdentity(ctx context.Context, q doltversion.RowQuerier, exp *ServerIdentityExpectation) error {
	if exp == nil {
		return nil
	}
	expected, problem := readServerIdentityRecord(exp.DataDir)
	actual, actualErr := QueryServerUUID(ctx, q)
	if problem == "" && actualErr == nil && actual == expected {
		return nil
	}
	ierr := &ServerIdentityError{
		Endpoint:      doltutil.PrintedServerAddr(exp.Host, exp.Port),
		Port:          exp.Port,
		RecordPath:    exp.RecordPath(),
		Expected:      expected,
		RecordProblem: problem,
		Actual:        actual,
		ActualErr:     actualErr,
	}
	if exp.ServerDir != "" {
		ierr.RecordedPID = identityRecordedPID(exp.ServerDir)
	}
	if exp.Port > 0 {
		ierr.ListenerPID = identityListenerPID(exp.Port)
	}
	return ierr
}

// SharedServerIdentityExpectation is what the shared server at host:port is
// expected to be for beadsDir's workspace: the server bd launched for the
// shared data directory. It is nil, no check, when beadsDir turns the check
// off, the host is not one bd runs a server at, or there is no shared
// directory.
func SharedServerIdentityExpectation(beadsDir, host string, port int) *ServerIdentityExpectation {
	if port <= 0 || !configfile.BdRunsServerAt(host) || !ServerIdentityVerificationEnabled(beadsDir) {
		return nil
	}
	serverDir, err := SharedServerPath()
	if err != nil {
		return nil
	}
	dataDir, err := SharedDoltPath()
	if err != nil {
		return nil
	}
	return &ServerIdentityExpectation{DataDir: dataDir, ServerDir: serverDir, Host: host, Port: port}
}

// ServerIdentityVerificationEnabled reports whether the server identity check
// applies to beadsDir's managed endpoints: BEADS_DOLT_VERIFY_SERVER_IDENTITY,
// else dolt.verify-server-identity in beadsDir's config.yaml, else the loaded
// configuration's, else on. Only a recognized false spelling (false, 0, f,
// off, ...) turns it off; a value that does not parse leaves it on.
func ServerIdentityVerificationEnabled(beadsDir string) bool {
	if raw, ok := os.LookupEnv(VerifyServerIdentityEnv); ok && strings.TrimSpace(raw) != "" {
		return !isFalsyBool(raw)
	}
	if beadsDir != "" {
		if raw := strings.TrimSpace(config.GetStringFromDir(beadsDir, VerifyServerIdentityKey)); raw != "" {
			return !isFalsyBool(raw)
		}
	}
	return !isFalsyBool(config.GetString(VerifyServerIdentityKey))
}

// pairTimeout bounds each step of pairing: an ownership lookup and the UUID
// query.
const pairTimeout = 10 * time.Second

// Pairing seams; tests replace them.
var (
	// pairListenerOwnership is who holds the TCP listener at host:port
	// relative to pid. Only OwnershipOwned lets bd pair.
	pairListenerOwnership = listenerOwnershipAt
	// pairQueryServerUUID reads @@server_uuid from the server at host:port.
	pairQueryServerUUID = queryServerUUIDAt
	// writeServerIdentityFile records uuid in dataDir.
	writeServerIdentityFile = writeServerIdentityRecord
)

// pairServerIdentity records, in dataDir, the @@server_uuid of srv, the dolt
// sql-server startLocked has just launched and found ready on host:port. It
// records nothing unless srv is alive and positively owns that listener both
// before and after the UUID is read: a greeting alone, an unknown or unlisted
// holder, or another process's listener does not count, so a foreign server
// that answers on the port is never trusted. On failure the previous record
// is left as it was.
func pairServerIdentity(beadsDir string, srv *startedServer, host string, port int, dataDir string) error {
	owned := func() error {
		if srv.hasExited() {
			return fmt.Errorf("it exited")
		}
		switch o := pairListenerOwnership(srv.pid, host, port); o {
		case OwnershipOwned:
			return nil
		case OwnershipForeign:
			return fmt.Errorf("another process holds the listener")
		case OwnershipUnlisted:
			return fmt.Errorf("no listener on the port is listed for any process on this host")
		default:
			return fmt.Errorf("this host cannot show which process holds the listener")
		}
	}
	fail := func(reason error) error {
		return fmt.Errorf("bd started dolt sql-server (PID %d) at %s but did not record its server identity: %w; "+
			"bd stopped the server it started and left %s unchanged.\n\n"+
			"To proceed:\n"+
			"  - Find what holds the port: %s\n"+
			"  - Give bd's server a free port (BEADS_DOLT_SERVER_PORT or dolt.port) and start it again: bd dolt start\n"+
			"  - If this host cannot show which process holds a port, turn the check off: %s=false or %s: false",
			srv.pid, doltutil.PrintedServerAddr(host, port), reason, remedy.PrintedValue(ServerIdentityRecordPath(dataDir)),
			fmt.Sprintf(portConflictHint, port), VerifyServerIdentityEnv, VerifyServerIdentityKey)
	}
	if err := owned(); err != nil {
		return fail(fmt.Errorf("could not prove it holds the listener: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout)
	defer cancel()
	uuid, err := pairQueryServerUUID(ctx, beadsDir, host, port)
	if err != nil {
		return fail(err)
	}
	if err := owned(); err != nil {
		return fail(fmt.Errorf("could not prove it still holds the listener after reading its UUID: %w", err))
	}
	if err := writeServerIdentityFile(dataDir, uuid); err != nil {
		return fail(fmt.Errorf("writing %s: %w", remedy.PrintedValue(ServerIdentityRecordPath(dataDir)), err))
	}
	return nil
}

// listenerOwnershipAt is who holds the TCP listener at host:port relative to
// pid: classifyListener on linux and darwin, and on Windows netstat's
// listeners on host's address (or a wildcard) and port.
func listenerOwnershipAt(pid int, host string, port int) Ownership {
	if runtime.GOOS != "windows" {
		return classifyListener(pid, port)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "netstat", "-aon").Output()
	if err != nil {
		return OwnershipUnknown
	}
	return classifyNetstatListeners(string(out), pid, host, port)
}

// classifyNetstatListeners classifies `netstat -aon` output: the LISTENING
// TCP sockets on port whose local address is host's, or a wildcard that also
// receives host's connections. Owned when there is at least one and every
// one is pid's, Foreign when any is another process's, Unlisted when there is
// none. Unlike findPIDOnPort it matches the address, not just a ":port"
// suffix, and it does not stop at the first PID.
func classifyNetstatListeners(out string, pid int, host string, port int) Ownership {
	want := normalizedListenHost(host)
	seen, foreign := false, false
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 5 || !strings.EqualFold(fields[0], "TCP") || fields[3] != "LISTENING" {
			continue
		}
		h, p, err := net.SplitHostPort(fields[1])
		if err != nil || p != strconv.Itoa(port) {
			continue
		}
		h = normalizedListenHost(h)
		if h != want && !receivesFor(h, want) {
			continue
		}
		holder, err := strconv.Atoi(fields[4])
		if err != nil {
			return OwnershipUnknown
		}
		seen = true
		if holder != pid {
			foreign = true
		}
	}
	switch {
	case foreign:
		return OwnershipForeign
	case seen:
		return OwnershipOwned
	}
	return OwnershipUnlisted
}

// normalizedListenHost is host as a canonical IP string where it is one,
// with localhost as 127.0.0.1 and an empty host as 127.0.0.1 (bd's default
// bind address).
func normalizedListenHost(host string) string {
	host = strings.Trim(host, "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(host)
}

// receivesFor reports whether a listener bound to the wildcard address
// listen also receives connections to want.
func receivesFor(listen, want string) bool {
	wantIP := net.ParseIP(want)
	if wantIP == nil {
		return false
	}
	switch listen {
	case "0.0.0.0", "*":
		return wantIP.To4() != nil
	case "::":
		return true
	}
	return false
}

// queryServerUUIDAt reads @@server_uuid from the server at host:port as the
// managed server's user.
func queryServerUUIDAt(ctx context.Context, beadsDir, host string, port int) (string, error) {
	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     loadEndpointConfig(beadsDir).GetDoltServerUser(),
		Password: endpointPassword(host, port),
		Timeout:  pairTimeout,
	}.String()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return "", fmt.Errorf("connecting to read its server UUID: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return QueryServerUUID(ctx, db)
}

// writeServerIdentityRecord atomically replaces dataDir's identity record
// with uuid, private to this user. A failed write leaves the previous record.
func writeServerIdentityRecord(dataDir, uuid string) error {
	tmp, err := os.CreateTemp(dataDir, ServerIdentityFileName+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(uuid + "\n"); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, ServerIdentityRecordPath(dataDir))
}
