package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/ui"
)

// initGuardDBCheck holds the result of checking whether a database exists on a
// Dolt server. Extracted from checkExistingBeadsDataAt for testability.
type initGuardDBCheck struct {
	Exists    bool // database found via SHOW DATABASES
	Reachable bool // server responded to ping
	Err       error
}

// checkDatabaseOnServer opens a temporary connection to the Dolt server and
// checks whether the named database exists via SHOW DATABASES. The connection
// is closed before returning.
//
// Returns Reachable=false when the server cannot be reached (FR-030), so the
// caller can fall through to existing "already initialized" behavior.
func checkDatabaseOnServer(host string, port int, user, password, dbName string, tls bool) initGuardDBCheck {
	// init promotes explicit --server-* flags into these getters before the
	// legacy guard runs, so the qualified identity includes an explicit user
	// and TLS choice rather than silently falling back to persisted metadata.
	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		TLS:      tls,
	}.String()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return initGuardDBCheck{Reachable: false, Err: err}
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ping first to verify reachability — sql.Open is lazy.
	if err := db.PingContext(ctx); err != nil {
		return initGuardDBCheck{Reachable: false, Err: err}
	}

	// Iterate SHOW DATABASES (not LIKE, to avoid underscore wildcard issues).
	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		// Server reachable but query failed — treat as unreachable to avoid
		// false negatives on permissions issues.
		return initGuardDBCheck{Reachable: true, Err: err}
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return initGuardDBCheck{Reachable: true, Err: err}
		}
		if name == dbName {
			return initGuardDBCheck{Exists: true, Reachable: true}
		}
	}
	if err := rows.Err(); err != nil {
		return initGuardDBCheck{Reachable: true, Err: err}
	}

	return initGuardDBCheck{Exists: false, Reachable: true}
}

// emptyServerInitWitnessInput describes the only server-init shape that may
// pass the legacy guard without an existing version witness. It is deliberately
// narrower than normal server init: the caller must name both the server and
// database, and must request a local re-init explicitly.
type emptyServerInitWitnessInput struct {
	ExplicitServer   bool
	ExplicitDatabase bool
	Database         string
	ReinitLocal      bool
	ProxiedServer    bool
	SharedServer     bool
	ExplicitHost     bool
	ServerHost       string
	ExplicitPort     bool
	ServerPort       int
}

func (in emptyServerInitWitnessInput) qualifies() bool {
	if !in.ExplicitServer || !in.ExplicitDatabase || in.Database == "" || !in.ReinitLocal || in.ProxiedServer || in.SharedServer {
		return false
	}
	return in.ExplicitHost && in.ServerHost != "" && in.ExplicitPort && in.ServerPort > 0
}

// emptyServerInitWitnessQualification is an immutable, explicit server
// endpoint plus credentials. It is obtained with read-only SQL before init's
// normal safety checks, then proved again under the mutation gate immediately
// before schema initialization.
type emptyServerInitWitnessQualification struct {
	beadsDir string
	dsn      doltutil.ServerDSN
}

// qualifyEmptyServerInitWitness performs no filesystem mutation. It admits
// only an explicit native server re-init whose selected database is proven
// empty by read-only SQL. An ambient socket is never used for an explicit TCP
// endpoint, so stale workspace configuration cannot redirect this proof.
func qualifyEmptyServerInitWitness(beadsDir string, in emptyServerInitWitnessInput) (emptyServerInitWitnessQualification, bool) {
	if !in.qualifies() {
		return emptyServerInitWitnessQualification{}, false
	}

	cfg, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil {
		return emptyServerInitWitnessQualification{}, false
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if !strings.EqualFold(cfg.DoltMode, configfile.DoltModeServer) || !hasLegacyDoltRoot(beadsDir) {
		return emptyServerInitWitnessQualification{}, false
	}
	// The exception is only for a genuinely missing witness. A historical,
	// malformed, non-regular, or otherwise pre-existing path must stay on the
	// ordinary guard path; the later O_EXCL check closes the race after this
	// read-only qualification.
	if _, err := os.Lstat(filepath.Join(beadsDir, localVersionFile)); !os.IsNotExist(err) {
		return emptyServerInitWitnessQualification{}, false
	}
	// Never execute a credential command merely to make a legacy workspace
	// admissible. Gateway/proxied initialization stays on the ordinary guard.
	if cfg.GetDoltCredentialCommand() != "" {
		return emptyServerInitWitnessQualification{}, false
	}

	dsn := doltutil.ServerDSN{
		Host:     in.ServerHost,
		Port:     in.ServerPort,
		User:     cfg.GetDoltServerUser(),
		Database: in.Database,
		TLS:      cfg.GetDoltServerTLS(),
	}
	// Explicit TCP intentionally bypasses both persisted and ambient sockets.
	// Only user/password/TLS come from configuration or env.
	dsn.Password = configfile.LookupCredentialsPassword(dsn.Host, dsn.Port)
	if password := os.Getenv("BEADS_DOLT_PASSWORD"); password != "" {
		dsn.Password = password
	}

	empty, err := selectedServerDatabaseIsEmpty(dsn)
	if err != nil || !empty {
		return emptyServerInitWitnessQualification{}, false
	}
	return emptyServerInitWitnessQualification{beadsDir: beadsDir, dsn: dsn}, true
}

// createEmptyServerInitWitness records that the current binary has begun
// initializing this workspace after re-proving the exact database empty. The
// version witness identifies the release era; it is not an init-completion
// marker. Keeping the current version after a later store-open failure is safe
// and lets ordinary current-era recovery retry instead of misclassifying the
// partial workspace as pre-1.0 data.
//
// O_EXCL is essential: a historical witness created between the two proofs
// must be left byte-for-byte intact and cause the original guard refusal to
// win.
func createEmptyServerInitWitness(qualification emptyServerInitWitnessQualification) error {
	witnessPath := filepath.Join(qualification.beadsDir, localVersionFile)
	// #nosec G304 -- witnessPath is the fixed version file beneath the selected workspace.
	f, err := os.OpenFile(witnessPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(Version + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(witnessPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(witnessPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(witnessPath)
		return err
	}
	return nil
}

// pinEmptyServerInitWitness keeps schema creation on precisely the endpoint
// qualified above. It runs after normal config and gateway setup, and prevents
// any later defaulting from selecting a stale socket or a different server.
func pinEmptyServerInitWitness(cfg *dolt.Config, qualification emptyServerInitWitnessQualification) {
	dsn := qualification.dsn
	cfg.ServerSocket = ""
	cfg.ServerHost = dsn.Host
	cfg.ServerPort = dsn.Port
	cfg.ServerUser = dsn.User
	cfg.ServerPassword = dsn.Password
	cfg.ServerTLS = dsn.TLS
	cfg.Database = dsn.Database
	cfg.AutoStart = false
}

// selectedServerDatabaseIsEmpty verifies both that the DSN selected the exact
// requested database and that it has no tables. It never creates a database,
// table, or schema; callers treat every error as a failed qualification.
func selectedServerDatabaseIsEmpty(dsn doltutil.ServerDSN) (bool, error) {
	db, err := sql.Open("mysql", dsn.String())
	if err != nil {
		return false, err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return false, err
	}

	var selected string
	if err := db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&selected); err != nil {
		return false, err
	}
	if selected != dsn.Database {
		return false, nil
	}

	var tables int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&tables); err != nil {
		return false, err
	}
	return tables == 0, nil
}

// initGuardServerMessage builds the error message for the init guard when the
// server is reachable but the database does not exist (FR-010, FR-011).
// Extracted as a pure function for unit testing without a real database.
//
// GH#2363: The message deliberately avoids suggesting `bd init --force` because
// that command destroys all existing issue data.  An AI agent running inside a
// git hook blindly followed the previous suggestion and wiped a production
// database.  Instead we guide the user toward safe diagnostic commands.
func initGuardServerMessage(dbName, host string, port int, prefix, syncRemote string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s Database %q not found on server at %s:%d.\n", ui.RenderWarn("⚠"), dbName, host, port)
	b.WriteString("The server is running but this database hasn't been created yet.\n")

	b.WriteString("\nDiagnose with:\n")
	b.WriteString("  bd doctor          # check project health\n")
	b.WriteString("  bd dolt status     # inspect Dolt server state\n")

	b.WriteString("\nIf this is an existing project, fresh clone, or shared-server recovery, run:\n")
	b.WriteString("  bd bootstrap\n")
	b.WriteString("This is the safe entry point for existing-project recovery and may recover or initialize depending on detected state.\n")

	if syncRemote != "" {
		fmt.Fprintf(&b, "\nTip: sync.remote is configured (%s).\n", syncRemote)
		b.WriteString("Run bd bootstrap to recover from the configured remote, or use --dry-run to inspect the plan first.\n")
	} else {
		b.WriteString("\nIf this is a brand-new project, create the database with:\n")
		fmt.Fprintf(&b, "  bd init --prefix %s\n", prefix)
		b.WriteString("\nIf bd bootstrap cannot find the expected remote automatically, set sync.remote\n")
		b.WriteString("in .beads/config.yaml and re-run bd bootstrap.\n")
	}

	b.WriteString("\n⚠  Caution: bd init --force destroys ALL existing issues. Do not\n")
	b.WriteString("use --force unless you are certain the database should be recreated.\n")

	b.WriteString("\nAborting.")
	return errors.New(b.String())
}
