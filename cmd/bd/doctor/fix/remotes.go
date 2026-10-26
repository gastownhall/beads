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

// openFixDB opens the server plain bd doctor diagnoses over TCP: the port the
// store dials (doltserver.ResolveEndpoint), the legacy BEADS_DOLT_PORT
// included, which DefaultConfig leaves out, and the password keyed to that
// port. With DefaultConfig's port a workspace only BEADS_DOLT_PORT names had
// every SQL fix skipped as unreachable at 127.0.0.1:0, or run against a port
// file's server.
//
// It deliberately does not dial a configured unix socket, and neither does
// plain doctor's openDoltDB, whose failure keeps the schema gate withholding
// the DB fixes for a socket-only server. Admitting them there waits on a
// separate data-safety fix to the Database fix. Wherever the store dials a
// socket (doltserver.Endpoint.Transport: a live one, even beside a resolved
// port), it refuses rather than dial TCP: the server at that port may not be
// the one bd list opens, and every SQL fix would write there. Doctor's
// read-only scans dial that socket instead (openScanDB).
//
// It deliberately does not check the server against
// doltversion.RequiredMin, and neither does openServerCatalogDB: bd doctor
// --fix is exempt from the floor, so a user on a Dolt older than bd's
// minimum can still diagnose and repair the workspace before upgrading. bd
// doctor --server reports the floor as its "Dolt Version" check.
func openFixDB(beadsDir string, cfg *configfile.Config) (*sql.DB, error) {
	ep := doltserver.ResolveEndpoint(beadsDir, cfg)
	if socket := ep.Transport(); socket != "" {
		return nil, fmt.Errorf("the store dials this workspace's Dolt server on the unix socket %s, which bd doctor --fix does not dial yet", remedy.PrintedValue(socket))
	}
	return openWorkspaceDB(ep, cfg, "")
}

// openScanDB opens the server a read-only doctor scan reads
// (CountCrossTableDuplicates, ScanSeveredCloneLocalFKs): the one the store
// dials, over its transport, a live unix socket first, else TCP
// (doltserver.Endpoint.Transport), as doctor's Fresh Clone check does. A scan
// writes nothing, so where openFixDB refuses the socket a scan dials it:
// sharing that refusal turned each scan into an "N/A" pass, with no query of
// any server, wherever the store dials a socket.
func openScanDB(beadsDir string, cfg *configfile.Config) (*sql.DB, error) {
	ep := doltserver.ResolveEndpoint(beadsDir, cfg)
	return openWorkspaceDB(ep, cfg, ep.Transport())
}

// openWorkspaceDB opens cfg's database on the server at ep: over socket when
// it is set, else TCP at ep's host and port, as the configured user with the
// password keyed to that port.
func openWorkspaceDB(ep doltserver.Endpoint, cfg *configfile.Config, socket string) (*sql.DB, error) {
	connStr := doltutil.ServerDSN{
		Socket:   socket,
		Host:     ep.Host,
		Port:     ep.Port,
		User:     cfg.GetDoltServerUser(),
		Password: ep.Password(),
		Database: cfg.GetDoltDatabase(),
		TLS:      cfg.GetDoltServerTLS(),
	}.String()
	return sql.Open("mysql", connStr)
}

// errUnverifiableFixTarget marks a verifyFixTargetIdentity failure that is
// not a confirmed identity mismatch — the project_id needed to compare is
// missing on one side, or couldn't be read. Callers treat this as a soft
// skip (see guardFixTarget), the same convention already used for "database
// unreachable" at each destructive fix's call site. It is deliberately not
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
// skip (printed, then treated as "nothing to fix" — matching the existing
// "database unreachable" convention at each call site), while a confirmed
// PROJECT IDENTITY MISMATCH is a hard abort. Call sites use it as:
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
		fmt.Printf("  %s skipped: cannot verify target database identity (no project_id); run 'bd doctor --fix' to backfill project identity, then re-run\n", fixName)
		return true, nil
	}
	return true, verr
}
