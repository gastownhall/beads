package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

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
// expects the server to serve, a server UUID of that data root's own, starts
// the dolt sql-server it launches there with that @@server_uuid
// (pinServerIdentity) and checks that the launched server reports it and
// holds its listener (pairServerIdentity), and a store open or shared init
// against a managed endpoint compares the answering server's @@server_uuid
// with that record before it lists, creates or reads any database
// (VerifyServerIdentity). Dolt's own @@server_uuid does not name a data
// root: a value persisted in the user's global Dolt config
// (sqlserver.global.server_uuid, which one multi-database dolt sql-server run
// writes) wins over each data root's, so every server on such a host reports
// the same UUID.

// ServerIdentityFileName is the record, inside the server's data root, of the
// @@server_uuid of the dolt sql-server bd launches there.
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
		b.WriteString("  - If it should be this machine's own bd server, stop it and start it again so it runs with the identity bd recorded: " +
			"bd dolt stop && bd dolt start\n")
	}
	b.WriteString("  - If something other than bd runs this server (Docker, systemd, an orchestrator), say so: " +
		"dolt.auto-start: false (BEADS_DOLT_AUTO_START=0) or external mode (BEADS_DOLT_SERVER_MODE=1), " +
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
// off or declares its server managed outside bd (ServerManagedOutsideBD),
// the host is not one bd runs a server at, or there is no shared directory.
func SharedServerIdentityExpectation(beadsDir, host string, port int) *ServerIdentityExpectation {
	if port <= 0 || !configfile.BdRunsServerAt(host) || !ServerIdentityVerificationEnabled(beadsDir) ||
		ServerManagedOutsideBD(beadsDir) {
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

// ServerManagedOutsideBD reports whether beadsDir's workspace declares that
// something other than bd runs its Dolt server, the shared server included:
// external mode (BEADS_DOLT_SERVER_MODE=1), or auto-start off
// (BEADS_DOLT_AUTO_START, dolt.auto-start in beadsDir's config.yaml or the
// loaded configuration), which bd dolt start already reads as a server
// managed outside bd. Docker, systemd and orchestrators such as Gas City run
// such a server, so bd has no launch of its own to compare it with and the
// server identity check does not apply to it.
func ServerManagedOutsideBD(beadsDir string) bool {
	if os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" || isFalsyBool(os.Getenv("BEADS_DOLT_AUTO_START")) {
		return true
	}
	if beadsDir != "" && isFalsyBool(config.GetStringFromDir(beadsDir, "dolt.auto-start")) {
		return true
	}
	return isFalsyBool(config.GetString("dolt.auto-start"))
}

// newServerUUID mints a data root's server UUID; tests replace it.
var newServerUUID = uuid.NewString

// pinServerIdentity is the server UUID startLocked gives the dolt sql-server
// it launches for dataDir (the server_uuid system variable of the server's
// config), or "" when it gives none. It is the UUID dataDir's record names,
// so every restart keeps it, with the check on or off for beadsDir: a server
// started with the check off (for one command, or by a project on the shared
// server whose config turns it off) still passes the check of every later
// open. When there is no usable record it is a new one, recorded before the
// launch so that a store open reaching the server while it starts finds it
// there, but only with the check on; with it off it is "" and dataDir is left
// alone. A launch that passes CLI flags (configLaunch false) cannot set it,
// so it is "" there.
func pinServerIdentity(beadsDir, dataDir string, configLaunch bool) (string, error) {
	if !configLaunch {
		return "", nil
	}
	if recorded, problem := readServerIdentityRecord(dataDir); problem == "" {
		return recorded, nil
	}
	if !ServerIdentityVerificationEnabled(beadsDir) {
		return "", nil
	}
	id := newServerUUID()
	if err := writeServerIdentityFile(dataDir, id); err != nil {
		return "", fmt.Errorf("recording the server identity of the server bd starts: writing %s: %w",
			remedy.PrintedValue(ServerIdentityRecordPath(dataDir)), err)
	}
	return id, nil
}

// pairTimeout bounds each step of pairing: an ownership lookup and the UUID
// query.
const pairTimeout = 10 * time.Second

// Pairing seams; tests replace them.
var (
	// pairListenerOwnership is who holds the TCP listener at host:port
	// relative to pid.
	pairListenerOwnership = listenerOwnershipAt
	// pairQueryServerUUID reads @@server_uuid from the server at host:port.
	pairQueryServerUUID = queryServerUUIDAt
	// writeServerIdentityFile records uuid in dataDir.
	writeServerIdentityFile = writeServerIdentityRecord
	// pairWarnings receives the warning of a pairing the host could not
	// prove.
	pairWarnings io.Writer = os.Stderr
)

// pairServerIdentity checks that srv, the dolt sql-server startLocked has
// just launched for dataDir and found ready on host:port, is the server
// dataDir's record names, recording its @@server_uuid there when the record
// does not name it yet. It accepts srv only if srv is alive and no lookup
// refutes that it holds that listener, both before and after the UUID is
// read: another process's listener, or no listener listed on a host that
// lists this process's own (ListenerOwnershipProvable), refutes it, so a
// foreign server that answers on the port is never trusted where the host can
// tell. When startLocked pinned the UUID (pinned, from pinServerIdentity), srv
// must also report that one: the record already names it, and a server that
// reports another is not one bd started for dataDir. A launch with nothing
// pinned (the CLI-flag launch) records the UUID srv reports; where the host
// cannot show which process holds a listener (a platform with no lookup, such
// as FreeBSD; WSL1; a sandbox whose /proc lists no sockets), it does so on the
// evidence the startup wait accepted (awaitOwnedListener) and prints a
// warning. Pairing never changes a record on failure.
func pairServerIdentity(beadsDir string, srv *startedServer, host string, port int, dataDir, pinned string) error {
	// ownership is whether srv holds the listener: shown (true, nil),
	// refuted (an error), or not shown by this host (false, nil).
	ownership := func() (bool, error) {
		if srv.hasExited() {
			return false, fmt.Errorf("it exited")
		}
		switch pairListenerOwnership(srv.pid, host, port) {
		case OwnershipOwned:
			return true, nil
		case OwnershipForeign:
			return false, fmt.Errorf("another process holds the listener")
		case OwnershipUnlisted:
			if listenerOwnershipProvable() {
				return false, fmt.Errorf("no listener on the port is listed for any process on this host")
			}
		}
		return false, nil
	}
	fail := func(reason error) error {
		return fmt.Errorf("bd started dolt sql-server (PID %d) at %s but could not verify its server identity: %w; "+
			"bd stopped the server it started.\n\n"+
			"To proceed:\n"+
			"  - Find what holds the port: %s\n"+
			"  - Give bd's server a free port (BEADS_DOLT_SERVER_PORT or dolt.port) and start it again: bd dolt start\n"+
			"  - To start it without the check, turn the check off: %s=false or %s: false",
			srv.pid, doltutil.PrintedServerAddr(host, port), reason,
			fmt.Sprintf(portConflictHint, port), VerifyServerIdentityEnv, VerifyServerIdentityKey)
	}
	shown, err := ownership()
	if err != nil {
		return fail(fmt.Errorf("could not prove it holds the listener: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout)
	defer cancel()
	reported, err := pairQueryServerUUID(ctx, beadsDir, host, port)
	if err != nil {
		return fail(err)
	}
	stillShown, err := ownership()
	if err == nil && shown && !stillShown {
		err = fmt.Errorf("this host no longer shows which process holds the listener")
	}
	if err != nil {
		return fail(fmt.Errorf("could not prove it still holds the listener after reading its UUID: %w", err))
	}
	if pinned != "" && reported != pinned {
		return fail(fmt.Errorf("it reports server UUID %s, not the %s bd started it with", reported, pinned))
	}
	if recorded, _ := readServerIdentityRecord(dataDir); recorded != reported {
		if err := writeServerIdentityFile(dataDir, reported); err != nil {
			return fail(fmt.Errorf("writing %s: %w", remedy.PrintedValue(ServerIdentityRecordPath(dataDir)), err))
		}
	}
	if !shown && pinned == "" {
		_, _ = fmt.Fprintf(pairWarnings, "Warning: this host cannot show which process holds the listener at %s, so bd recorded "+
			"the server identity of the dolt sql-server it started (PID %d) in %s on the evidence of its startup alone.\n",
			doltutil.PrintedServerAddr(host, port), srv.pid, remedy.PrintedValue(ServerIdentityRecordPath(dataDir)))
	}
	return nil
}

// listenerOwnershipAt is who holds the TCP listener at host:port relative to
// pid: the listeners on port that receive connections dialed to host
// (receivingListeners), from /proc on linux, lsof on darwin and netstat on
// Windows. Unlike classifyListener, which asks only whether pid holds some
// listener on the port, every listener that receives the endpoint's
// connections must be pid's or a descendant's.
func listenerOwnershipAt(pid int, host string, port int) Ownership {
	if runtime.GOOS != "windows" {
		return classifyListenerAt(pid, host, port)
	}
	ctx, cancel := context.WithTimeout(context.Background(), pairTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "netstat", "-aon").Output()
	if err != nil {
		return OwnershipUnknown
	}
	return classifyNetstatListeners(string(out), pid, host, port)
}

// classifyNetstatListeners classifies `netstat -aon` output: the TCP
// listeners on port that receive connections dialed to host
// (receivingListeners), each held by pid or a descendant of it
// (classifyHolders). A listener is told by its foreign address, the
// unspecified address with port 0 (a connection's is its peer's), not by the
// state column, which netstat prints in the system's language (LISTENING,
// ABHÖREN, ...), in more than one word in some, before the PID column. It is
// OwnershipUnknown when a holder's PID does not parse. Unlike
// findPIDOnPort it matches the address, not just a ":port" suffix, and it
// does not stop at the first PID.
func classifyNetstatListeners(out string, pid int, host string, port int) Ownership {
	want, ok := endpointAddr(host)
	if !ok {
		return OwnershipUnknown
	}
	var addrs []netip.Addr
	var holders []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !netstatListener(fields[2]) {
			continue
		}
		h, p, err := net.SplitHostPort(fields[1])
		if err != nil || p != strconv.Itoa(port) {
			continue
		}
		addr, err := netip.ParseAddr(h)
		if err != nil {
			continue
		}
		addrs, holders = append(addrs, addr.Unmap()), append(holders, fields[len(fields)-1])
	}
	var receivers []int
	for i, recv := range receivingListeners(addrs, want) {
		if !recv {
			continue
		}
		holder, err := strconv.Atoi(holders[i])
		if err != nil {
			return OwnershipUnknown
		}
		receivers = append(receivers, holder)
	}
	return classifyHolders(receivers, pid)
}

// netstatListener reports whether a netstat TCP row's foreign address is a
// listener's: the unspecified address with port 0.
func netstatListener(foreign string) bool {
	h, p, err := net.SplitHostPort(foreign)
	if err != nil || p != "0" {
		return false
	}
	addr, err := netip.ParseAddr(h)
	return err == nil && addr.IsUnspecified()
}

// bdServerAddr is the address bd's own Dolt server binds (DefaultConfig's
// host).
var bdServerAddr = netip.MustParseAddr(configfile.DefaultDoltServerHost)

// endpointAddr is the address a dial to host reaches, to match against
// listeners' local addresses: bd's server address for every spelling of it
// (configfile.BdRunsServerAt), else host's own address when it is one. ok
// is false for a name.
func endpointAddr(host string) (addr netip.Addr, ok bool) {
	if configfile.BdRunsServerAt(host) {
		return bdServerAddr, true
	}
	addr, err := netip.ParseAddr(configfile.UnbracketHost(host))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// receivingListeners reports which of addrs, the unmapped local addresses of
// the TCP listeners on one port, receive connections dialed to want there:
// those bound to want or to the unspecified address of want's family, and,
// for an IPv4 want, those bound to the IPv6 unspecified address only when no
// IPv4 listener does. An IPv4 listener takes an IPv4 connection before an
// IPv6 wildcard does, and an IPv6-only wildcard (IPV6_V6ONLY), which /proc,
// lsof and netstat do not tell from a dual-stack one, takes none, so beside
// an IPv4 listener an IPv6 wildcard receives nothing of want's.
func receivingListeners(addrs []netip.Addr, want netip.Addr) []bool {
	recv := make([]bool, len(addrs))
	ipv4 := false
	for i, a := range addrs {
		if a == want || (a.IsUnspecified() && a.Is4() == want.Is4()) {
			recv[i] = true
			ipv4 = ipv4 || a.Is4()
		}
	}
	if want.Is4() && !ipv4 {
		for i, a := range addrs {
			if a.Is6() && a.IsUnspecified() {
				recv[i] = true
			}
		}
	}
	return recv
}

// classifyHolders is who holds an endpoint's listeners, the PIDs holders,
// relative to pid: OwnershipUnlisted when there are none, OwnershipOwned when
// each is pid or one of its descendants, OwnershipForeign when one is not,
// and OwnershipUnknown when the process tree a holder other than pid needs
// cannot be read (processParents).
func classifyHolders(holders []int, pid int) Ownership {
	if len(holders) == 0 {
		return OwnershipUnlisted
	}
	var parents map[int]int
	for _, holder := range holders {
		if holder == pid {
			continue
		}
		if parents == nil {
			var err error
			if parents, err = processParents(); err != nil || len(parents) == 0 {
				return OwnershipUnknown
			}
		}
		if !descendsFrom(holder, pid, parents) {
			return OwnershipForeign
		}
	}
	return OwnershipOwned
}

// descendsFrom reports whether p is ancestor itself or one of its
// descendants, walking p's parent chain through parents (child -> parent).
// A chain that reaches a pid the snapshot does not list, pid 0, or a cycle
// (a stale snapshot) is not a match.
func descendsFrom(p, ancestor int, parents map[int]int) bool {
	seen := map[int]bool{}
	for p > 0 && !seen[p] {
		if p == ancestor {
			return true
		}
		seen[p] = true
		next, ok := parents[p]
		if !ok {
			return false
		}
		p = next
	}
	return false
}

// listenerLookupTimeout bounds each lsof or ps run, so a hung tool cannot
// stall startup.
const listenerLookupTimeout = 10 * time.Second

// processParents snapshots every process's parent, child to parent, for the
// descendant walks of the darwin and Windows listener lookups
// (processParentPIDs: ps on Unix, a Toolhelp snapshot on Windows); tests
// replace it.
var processParents = processParentPIDs

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
