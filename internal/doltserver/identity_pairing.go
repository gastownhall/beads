package doltserver

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage/doltutil"
)

// Pairing: the only writer of the identity record (GH#4637). A record becomes
// trustworthy solely through pairServerIdentity — a launch bd can prove. See
// identity.go for the gate that consumes the record.

// pairingListenerOwnership reports whether the launched child (pid) owns the
// TCP listener at host:port. A package var so tests can force each outcome
// deterministically.
var pairingListenerOwnership = defaultPairingListenerOwnership

func defaultPairingListenerOwnership(pid int, host string, port int) bool {
	owned, _ := listenerOwnership(pid, port)
	return owned
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
// for dataDir's server. A var so tests can force write failures. The temporary
// file is created with 0600 permissions (os.CreateTemp), and the rename makes
// the record appear whole or not at all, so a failed write never replaces a
// prior record with a partial one.
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
// The ownership and liveness refusals land with AC-4; this commit establishes
// the pairing path itself, so the gate has a writer at all.
func pairServerIdentity(srv *startedServer, host string, port int, dataDir string) error {
	if srv == nil {
		return errors.New("no launched server to pair")
	}
	if srv.hasExited() {
		return fmt.Errorf("the launched dolt sql-server (PID %d) exited before its identity could be recorded", srv.pid)
	}
	if !pairingListenerOwnership(srv.pid, host, port) {
		return fmt.Errorf("could not prove the dolt sql-server bd launched (PID %d) owns %s",
			srv.pid, doltutil.ServerAddr(host, port))
	}
	uuid, err := pairingServerUUID(host, port)
	if err != nil {
		return fmt.Errorf("reading the identity of the dolt sql-server bd launched (PID %d) at %s: %w",
			srv.pid, doltutil.ServerAddr(host, port), err)
	}
	record := filepath.Join(dataDir, ServerIdentityRecordName)
	if err := writeServerUUIDRecord(record, uuid); err != nil {
		return fmt.Errorf("recording the started server's identity in %s: %w", record, err)
	}
	return nil
}
