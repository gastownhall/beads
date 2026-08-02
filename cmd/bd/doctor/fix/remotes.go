package fix

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

func openFixDB(beadsDir string, cfg *configfile.Config) (*sql.DB, error) {
	host := cfg.GetDoltServerHost()
	user := cfg.GetDoltServerUser()
	database := cfg.GetDoltDatabase()
	password := cfg.GetDoltServerPassword()
	port := doltserver.DefaultConfig(beadsDir).Port

	connStr := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		Database: database,
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
// openFixDB dials fresh on every fix call, re-resolving the port from
// doltserver.DefaultConfig(beadsDir) independently of whatever connection
// doctor's read-only checks used. Two different projects commonly share the
// same database name ("beads" by default), so a stale port pointing at an
// unrelated project's server would otherwise connect successfully and
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
