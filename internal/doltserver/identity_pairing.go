package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Pairing: the only writer of the identity record (GH#4637). A record becomes
// trustworthy solely through pairServerIdentity — a launch bd can prove. See
// identity.go for the gate that consumes the record.

// pairingListenerOwnership reports whether the launched child (pid) positively
// owns the TCP listener at host:port, and whether that answer is known.
// Replaced by tests to force each outcome deterministically.
//
// The default uses the platform's listener-ownership machinery where it is
// positive (Linux /proc, darwin lsof); on Windows — where classifyListener
// cannot see the holder — it matches netstat's listener entries by address AND
// port against the launched PID, because the port-only, first-PID helper
// (findPIDOnPort) cannot authorize pairing: on a mirrored or forwarded port
// it names whatever answers first, which is exactly the foreign listener this
// check exists to reject.
var pairingListenerOwnership = defaultPairingListenerOwnership

func defaultPairingListenerOwnership(pid int, host string, port int) (owned, known bool) {
	if runtime.GOOS == "windows" {
		out, err := netstatOutput()
		if err != nil {
			return false, false
		}
		pids := netstatListenerPIDs(out, host, port)
		switch len(pids) {
		case 0:
			return false, false // netstat cannot see the holder: unknown
		case 1:
			return pids[0] == pid, true
		default:
			// More than one process listed on this exact address:port is an
			// ambiguity, not a proof.
			return false, true
		}
	}
	return listenerOwnership(pid, port)
}

// netstatOutput shells out to netstat; a var so tests can feed fixtures.
var netstatOutput = func() ([]byte, error) {
	return exec.Command("netstat", "-aon").Output() //nolint:gosec // G60: fixed binary name, no user input
}

// netstatListenerPIDs returns the PIDs that netstat -aon output shows
// listening exactly on host:port. The local address column must match the
// address the managed server was launched to bind (an IPv6 host in its
// bracketed spelling), never just the port: a wildcard or a mirrored listener
// on the same port must not authorize pairing. Pure parsing, so Windows
// netstat fixtures test on every platform.
func netstatListenerPIDs(out []byte, host string, port int) []int {
	bare := strings.Trim(strings.TrimSpace(host), "[]")
	locals := map[string]bool{
		net.JoinHostPort(bare, strconv.Itoa(port)): true,
	}
	// netstat spells an IPv6 local address bracketed.
	if strings.Contains(bare, ":") {
		locals[net.JoinHostPort("["+bare+"]", strconv.Itoa(port))] = true
	}
	var pids []int
	for _, raw := range strings.Split(string(out), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.Contains(line, "LISTENING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if !locals[fields[1]] {
			continue
		}
		if pid, err := strconv.Atoi(fields[4]); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// pairingServerUUID reads the freshly launched child's @@server_uuid over the
// connection with the managed server's own resolved credentials: Start launches
// dolt sql-server with no auth flags, so root with no password is what bd
// itself connects with. A var so tests can force the query outcome.
var pairingServerUUID = defaultPairingServerUUID

func defaultPairingServerUUID(host string, port int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pairingQueryTimeout)
	defer cancel()
	db, err := sql.Open("mysql", doltutil.ServerDSN{Host: host, Port: port, User: "root"}.String())
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(pairingQueryTimeout)
	var uuid sql.NullString
	if err := db.QueryRowContext(ctx, "SELECT @@server_uuid").Scan(&uuid); err != nil {
		return "", err
	}
	if !uuid.Valid || strings.TrimSpace(uuid.String) == "" {
		return "", errors.New("the server reported an empty @@server_uuid")
	}
	return strings.TrimSpace(uuid.String), nil
}

// writeServerUUIDRecord atomically establishes or replaces the identity record
// for dataDir's server. A var so tests can force write failures. The
// temporary file is created with 0600 permissions (os.CreateTemp), and the
// rename makes the record appear whole or not at all, so a failed write never
// replaces a prior record with a partial one.
var writeServerUUIDRecord = defaultWriteServerUUIDRecord

func defaultWriteServerUUIDRecord(path, uuid string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, config.BeadsDirPerm); err != nil {
		return fmt.Errorf("creating the data root directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+ServerIdentityRecordName+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op once the rename succeeds
	if _, err := tmp.WriteString(uuid); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// pairServerIdentity reads the identity of a server this process just
// launched and durably records it for dataDir. It is the ONLY writer of the
// record, and it runs only after the launch's own readiness wait succeeded.
// Pairing requires positive ownership of the intended listener by the child
// before AND after the identity query, plus the child's liveness throughout;
// foreign, ambiguous and unknown holders — including a listener whose only
// evidence is the readiness greeting — never pair. Every refusal is
// actionable and never signals (kills) the foreign listener; the caller
// cleans up the child bd launched and leaves any prior record untouched.
func pairServerIdentity(srv *startedServer, host string, port int, dataDir string) error {
	if srv == nil {
		return errors.New("no launched server to pair")
	}
	addr := doltutil.ServerAddr(host, port)
	if srv.hasExited() {
		return fmt.Errorf("the launched dolt sql-server (PID %d) exited before its identity could be recorded", srv.pid)
	}
	if err := requirePairingOwnership(srv.pid, host, port); err != nil {
		return err
	}
	uuid, err := pairingServerUUID(host, port)
	if err != nil {
		return fmt.Errorf("reading the identity of the dolt sql-server bd launched (PID %d) at %s: %w",
			srv.pid, remedy.PrintedValue(addr), err)
	}
	if srv.hasExited() {
		return fmt.Errorf("the launched dolt sql-server (PID %d) exited before its identity could be recorded", srv.pid)
	}
	if err := requirePairingOwnership(srv.pid, host, port); err != nil {
		return err
	}
	record := filepath.Join(dataDir, ServerIdentityRecordName)
	if err := writeServerUUIDRecord(record, uuid); err != nil {
		return fmt.Errorf("recording the started server's identity in %s: %w",
			remedy.PrintedValue(record), err)
	}
	return nil
}

// requirePairingOwnership is pairing's positive-ownership check: the child
// must provably own the listener at host:port. A refusal names what to do and
// never claims the foreign listener was bd's to stop.
func requirePairingOwnership(pid int, host string, port int) error {
	owned, known := pairingListenerOwnership(pid, host, port)
	if owned && known {
		return nil
	}
	addr := doltutil.ServerAddr(host, port)
	var why string
	switch {
	case known:
		why = "the listener on the port belongs to another process"
	default:
		why = "this platform or sandbox cannot prove who holds the port, and the readiness greeting alone is not proof"
	}
	return fmt.Errorf(
		"could not prove the dolt sql-server bd launched (PID %d) owns %s: %s.\n"+
			"bd will not record or trust the identity of a server it cannot prove it started, so opens of this workspace will refuse this endpoint.\n"+
			"Check what holds or forwards the port (%s), and start the server again once bd's own listener can answer on it.\n"+
			"To run bd's managed servers without identity verification, set dolt.verify-server-identity: false (or %s=0)",
		pid, remedy.PrintedValue(addr), why, fmt.Sprintf(portConflictHint, port), verifyIdentityEnv)
}
