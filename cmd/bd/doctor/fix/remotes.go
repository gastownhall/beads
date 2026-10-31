package fix

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// ErrSkipped marks a bd doctor --fix repair that did not run: SkipError
// wraps it with the reason. bd doctor --fix counts and reports such a fix as
// skipped, never as fixed, and does not count it as an error either.
var ErrSkipped = errors.New("fix skipped")

// SkipError is a fix that did not run, and Reason why. It matches ErrSkipped
// (errors.Is) and its reason.
type SkipError struct {
	Reason error
}

func (e *SkipError) Error() string { return "fix skipped: " + e.Reason.Error() }

// Unwrap is ErrSkipped and the reason.
func (e *SkipError) Unwrap() []error { return []error{ErrSkipped, e.Reason} }

// skipFix reports a fix as skipped for reason (see ErrSkipped).
func skipFix(reason error) error { return &SkipError{Reason: reason} }

// openFixDB opens the server plain bd doctor diagnoses over TCP: the port the
// store dials (doltserver.ResolveEndpoint), the legacy BEADS_DOLT_PORT
// included, which DefaultConfig leaves out, and the password keyed to that
// port. With DefaultConfig's port a workspace only BEADS_DOLT_PORT names had
// every SQL fix skipped as unreachable at 127.0.0.1:0, or run against a port
// file's server.
//
// It dials TCP at the resolved port even beside a live unix socket, as plain
// doctor's openDoltDB and its schema gate do, and as base did: every
// destructive fix then checks the server's project_id (guardFixTarget). It
// does not dial a socket: a server only a unix socket names (no port
// resolved) is refused, as base's failed dial of 127.0.0.1:0 refused it, and
// doctor's schema gate keeps withholding the DB fixes there. Admitting them
// waits on a separate data-safety fix to the Database fix. Each fix reports
// that refusal as skipped (ErrSkipped). Doctor's read-only scans read the
// server these fixes write, and only a socket-only server on its socket
// (openScanDB).
//
// It deliberately does not check the server against
// doltversion.RequiredMin, and neither does openServerCatalogDB: bd doctor
// --fix is exempt from the floor, so a user on a Dolt older than bd's
// minimum can still diagnose and repair the workspace before upgrading. bd
// doctor --server reports the floor as its "Dolt Version" check.
func openFixDB(beadsDir string, cfg *configfile.Config) (*sql.DB, error) {
	ep := doltserver.ResolveEndpoint(beadsDir, cfg)
	if !ep.Resolved() && ep.Socket != "" {
		return nil, fmt.Errorf("only the unix socket %s names this workspace's Dolt server (no port resolved), and bd doctor --fix does not dial a socket yet", remedy.PrintedValue(ep.Socket))
	}
	return openWorkspaceDB(ep, cfg, "")
}

// openScanDB opens the server a read-only doctor scan reads
// (CountCrossTableDuplicates, ScanSeveredCloneLocalFKs, and the checks that
// open OpenScanDB): the one its fix writes (openFixDB), TCP at the resolved
// port, even beside a live unix socket, so bd doctor --fix never repairs a
// server the scan did not read. The scans read the store's socket there, so a
// fix ran, and reported "Fixed", on the server at the port while the problem
// it was run for stayed on the socket's (#7874 review). A server only a unix
// socket names (no port resolved), which openFixDB refuses, is read on the
// store's transport, its socket: the scan writes nothing there, and its fix
// reports itself skipped. An endpoint with neither a port nor a socket is
// refused with the store's advice, never dialed at 127.0.0.1:0.
func openScanDB(beadsDir string, cfg *configfile.Config) (*sql.DB, error) {
	ep := doltserver.ResolveEndpoint(beadsDir, cfg)
	if err := ep.UnresolvedError(); err != nil {
		return nil, err
	}
	socket := ""
	if !ep.Resolved() {
		socket = ep.Transport()
	}
	return openWorkspaceDB(ep, cfg, socket)
}

// openWorkspaceDB opens cfg's database on the server at ep: over socket when
// it is set, else TCP at ep's host and port, as the configured user with the
// password keyed to that port.
func openWorkspaceDB(ep doltserver.Endpoint, cfg *configfile.Config, socket string) (*sql.DB, error) {
	allowCleartext, err := cfg.GetDoltServerAllowCleartextPasswordChecked()
	if err != nil {
		return nil, err
	}
	connStr := doltutil.ServerDSN{
		Socket:                  socket,
		Host:                    ep.Host,
		Port:                    ep.Port,
		User:                    cfg.GetDoltServerUser(),
		Password:                ep.Password(),
		Database:                cfg.GetDoltDatabase(),
		TLS:                     cfg.GetDoltServerTLS(),
		AllowCleartextPasswords: allowCleartext,
	}.String()
	return sql.Open("mysql", connStr)
}

// errUnverifiableFixTarget marks a verifyFixTargetIdentity failure that is
// not a confirmed identity mismatch — the project_id needed to compare is
// missing on one side, or couldn't be read. Callers treat this as a soft
// skip, reported as skipped rather than fixed (see guardFixTarget). It is
// deliberately not
// a hard abort: shared-server-mode workspaces, projects that predate the
// project_id metadata table, and workspaces where a user has declined the
// interactive "Project Identity" fix are all real, common, and otherwise
// have no self-service way to unblock every other doctor fix.
var errUnverifiableFixTarget = errors.New("cannot verify target database identity")

// verifyFixTargetIdentity guards every destructive doctor --fix path against
// a stale or wrong dolt-server.port file (or the shared-mode default port)
// silently redirecting DELETE/UPDATE statements at a different project's
// database than the one doctor diagnosed (mybd-2qegi).
//
// openFixDB dials fresh on every fix call, re-resolving the port through
// doltserver.ResolveEndpoint(beadsDir, cfg) independently of whatever
// connection doctor's read-only checks used. Two different projects commonly
// share the same database name ("beads" by default), so a stale port pointing
// at an unrelated project's server would otherwise connect successfully and
// destructively mutate the wrong data with no error.
//
// This compares the freshly-dialed connection's `_project_id` (from the
// dolt-synced metadata table, the same key internal/storage/dolt/store.go's
// verifyProjectIdentity checks) against the local metadata.json project_id
// for beadsDir. cfg may be nil, in which case metadata.json is loaded fresh;
// callers that already loaded it (e.g. via openDoltDB) should pass it
// through so the two loads cannot disagree.
//
// Returns a non-nil error wrapping errUnverifiableFixTarget when the
// comparison can't be made (see errUnverifiableFixTarget's doc). Returns a
// plain (non-wrapped) error only for a confirmed project_id MISMATCH, which
// callers must always treat as a hard abort.
func verifyFixTargetIdentity(db *sql.DB, beadsDir string, cfg *configfile.Config) error {
	metaCfg := cfg
	if metaCfg == nil {
		loaded, err := configfile.Load(beadsDir)
		if err != nil {
			return fmt.Errorf("%w: failed to load local metadata.json: %v", errUnverifiableFixTarget, err)
		}
		metaCfg = loaded
	}
	if metaCfg == nil {
		return fmt.Errorf("%w: no local metadata.json found", errUnverifiableFixTarget)
	}
	localID := metaCfg.ProjectID
	if localID == "" {
		return fmt.Errorf("%w: local metadata.json has no project_id", errUnverifiableFixTarget)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// GetMetadataInTx returns ("", nil) when the row is absent, so there's no
	// separate sql.ErrNoRows branch to handle here.
	dbID, err := issueops.GetMetadataInTx(ctx, db, "_project_id")
	if err != nil {
		return fmt.Errorf("%w: failed to read database project_id: %v", errUnverifiableFixTarget, err)
	}
	if dbID == "" {
		return fmt.Errorf("%w: connected database has no project_id in its metadata table (possible stale dolt-server.port or wrong server)", errUnverifiableFixTarget)
	}

	if localID != dbID {
		return fmt.Errorf(
			"PROJECT IDENTITY MISMATCH — refusing destructive fix\n\n"+
				"  Local project ID (metadata.json):  %s\n"+
				"  Connected database project ID:     %s\n\n"+
				"The resolved dolt server connection is serving a DIFFERENT project's\n"+
				"database than the one doctor diagnosed. This can happen with a stale\n"+
				"dolt-server.port file or the shared-mode default port pointing at\n"+
				"another project's server.\n\n"+
				"To diagnose: bd dolt status",
			localID, dbID)
	}
	return nil
}

// guardFixTarget wraps verifyFixTargetIdentity with the convention every
// destructive fix entry point must follow: an unverifiable target is a soft
// skip, reported as skipped (a SkipError, see ErrSkipped) with the reason and
// how to backfill project identity, so bd doctor --fix counts it as neither
// fixed nor an error; a confirmed PROJECT IDENTITY MISMATCH is a hard abort.
// Call sites use it as:
//
//	if skip, err := guardFixTarget("<Fix Name>", db, beadsDir, cfg); skip {
//		return err
//	}
func guardFixTarget(fixName string, db *sql.DB, beadsDir string, cfg *configfile.Config) (skip bool, err error) {
	verr := verifyFixTargetIdentity(db, beadsDir, cfg)
	if verr == nil {
		return false, nil
	}
	if errors.Is(verr, errUnverifiableFixTarget) {
		return true, skipFix(fmt.Errorf("%s: %w; run 'bd doctor --fix' to backfill project identity, then re-run", fixName, verr))
	}
	return true, verr
}
