package uow

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/steveyegge/beads/internal/storage/dberrors"
	db "github.com/steveyegge/beads/internal/storage/domain/db"
	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// checkTeamServerSchema verifies that a bts-managed database's schema version
// matches this binary's. The connection must already have the database selected.
func checkTeamServerSchema(ctx context.Context, conn schema.DBConn, database string) error {
	current, err := schema.CurrentVersion(ctx, conn)
	if err != nil {
		return fmt.Errorf("uow: team-server schema check: %w", err)
	}
	latest := schema.LatestVersion()
	switch {
	case current == 0:
		return fmt.Errorf(
			"uow: database %q has no beads schema — the schema is managed by beads-team-server; ask your operator to run 'bts init' first",
			database)
	case current > latest:
		return schema.CheckForwardDrift(ctx, conn)
	case current < latest:
		// No BD_IGNORE_SCHEMA_SKEW hatch here: it would let a newer bd write
		// against an older bts schema. Not SchemaBehindError either: its "run
		// `bd migrate schema`" advice is wrong for a bts-owned schema.
		return fmt.Errorf(
			"uow: database %q is at schema v%d, this bd expects v%d; the schema is managed by beads-team-server — ask your operator to run 'bts migrate', or use a bd built against schema v%d",
			database, current, latest, current)
	}
	return nil
}

// checkTeamServerIdentity verifies that the bts-managed database this
// invocation is about to open really belongs to the calling workspace's
// project. The connection must already have the database selected.
//
// The proxied/team-server path never constructs a DoltStore, so
// DoltStore.verifyProjectIdentity — which guards every non-CreateIfMissing
// gateway open — is unreachable here. `bd init --team-server` ADOPTS whatever
// identity the shared database carries (it has no expected value to assert
// against), and before this check nothing re-asserted it on any later open.
// That was tolerable while proxied-server meant a per-workspace database bd
// created itself; --team-server points bd at a long-lived operator-managed
// database selectable per invocation via --database, which is exactly the
// "serving a DIFFERENT project" case the gateway guard exists to catch.
//
// expectedProjectID is empty for the paths that legitimately have no
// assertion to make (`bd init`, which adopts, and server-wide database
// maintenance); those skip the check.
func checkTeamServerIdentity(ctx context.Context, conn schema.DBConn, database, expectedProjectID string) error {
	// Soft-skip semantics deliberately mirror DoltStore.verifyProjectIdentity:
	// an empty id on EITHER side means "no assertion available", not
	// "mismatch", so workspaces and databases that predate project identity
	// keep working.
	if expectedProjectID == "" {
		return nil
	}
	// A read error is surfaced rather than skipped: checkTeamServerSchema has
	// already proven the beads schema is present at this binary's version, so
	// the metadata table exists and a failure here is a real fault, not the
	// legacy-database case verifyProjectIdentity tolerates.
	dbProjectID, err := issueops.GetMetadataInTx(ctx, conn, "_project_id")
	if err != nil {
		return fmt.Errorf("uow: team-server identity check on database %q: %w", database, err)
	}
	// Unlike verifyProjectIdentity, an absent stored identity is NOT tolerated
	// here. adoptTeamServerIdentity refuses to attach to a bts database that
	// has no metadata._project_id at all, so for a team-server database this
	// is an already-invalid state rather than a legacy one — and soft-skipping
	// it would mean deleting one row from the shared database silently
	// disables this guard for every client.
	if dbProjectID == "" {
		return fmt.Errorf(
			"uow: database %q has no project identity (metadata._project_id) — the schema is managed by beads-team-server; ask your operator to provision it with 'bts init' (or heal an older bts database with 'bts migrate')",
			database)
	}
	if dbProjectID != expectedProjectID {
		return fmt.Errorf(
			"PROJECT IDENTITY MISMATCH — refusing to connect\n\n"+
				"  Local project ID (metadata.json):  %s\n"+
				"  Database %q project ID:            %s\n\n"+
				"The team server is serving a DIFFERENT project's database.\n"+
				"This can happen when:\n"+
				"  - --database (or a --db name override) points at another project\n"+
				"  - the configured dolt_database was repointed after 'bd init'\n"+
				"  - the operator re-provisioned this database for another project\n\n"+
				"Check dolt_database in .beads/metadata.json and any --database/--db\n"+
				"override. 'bd init --team-server' re-adopts the provisioned identity\n"+
				"and never writes to the shared database, so it is safe to re-run.",
			expectedProjectID, database, dbProjectID)
	}
	return nil
}

// plainExcursionUseDatabaseRemedy is the advice for a plain proxied excursion
// whose USE failed for a reason the server did not classify. An absent
// database is not reported through it: checkExcursionIdentity leaves that case
// to the open path that follows (the ordinary open creates the database, the
// preview open refuses it with its own remedy).
var plainExcursionUseDatabaseRemedy = useDatabaseRemedy{
	missing:      "check the --database/--db override for this invocation",
	unclassified: "check the --database/--db override for this invocation and that the server is reachable",
}

// checkExcursionIdentity is the plain (non-team-server) proxied counterpart of
// checkTeamServerIdentity, run before the preview or migrating open touches
// the database (gastownhall/beads#5572). The CLI passes a non-empty
// expectedProjectID to a plain provider only when the invocation's
// --database/--db override names a database other than the configured one,
// so this guards the explicit excursion: an override that selects ANOTHER
// beads project's database is refused before anything is created, migrated or
// read on it.
//
// It is deliberately lenient where checkTeamServerIdentity is strict. A plain
// excursion to a database that carries no identity — a non-beads database
// (`bd sql --database other`), one with a metadata table but no _project_id
// row, or one that does not exist yet — is the documented escape hatch, so
// "no identity available" passes. Only a positive, different identity is a
// refusal. Nothing is written: no identity is minted, changed or adopted.
func checkExcursionIdentity(ctx context.Context, conn *sql.Conn, database, expectedProjectID string) error {
	if expectedProjectID == "" {
		return nil
	}
	// Probe existence with a statement that always succeeds before any USE: a
	// Dolt session that issues a FAILING statement stays pinned to its
	// pre-statement catalog snapshot (be-bv7x), and this pooled connection is
	// the one the open path below goes on to CREATE and migrate on.
	var exists int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", database,
	).Scan(&exists); err != nil {
		if isSerializationError(err) {
			return err
		}
		return fmt.Errorf("uow: identity check: probing database %q existence: %w", database, err)
	}
	if exists == 0 {
		// Nothing there to carry an identity. The open path that follows
		// decides what an absent database means (the ordinary open creates
		// it, the preview open refuses it).
		return nil
	}
	if err := db.NewDDLSQLRepository(conn).UseDatabase(ctx, database); err != nil {
		if isSerializationError(err) {
			return fmt.Errorf("uow: switching to database: %w", err)
		}
		// Denial is classified first: a server hides existence from a
		// credential it has not granted, so a denial must never read as
		// "absent".
		return classifyUseDatabaseError(err, database, plainExcursionUseDatabaseRemedy)
	}
	// A USE that returns no error is not proof the session moved, and the
	// metadata read below is unqualified: see assertSessionDatabase.
	if err := assertSessionDatabase(ctx, conn, database); err != nil {
		return err
	}
	// No metadata table means a non-beads database (or one never
	// initialized): no identity to compare. Probed rather than read-and-
	// failed, for the same be-bv7x reason as the existence probe above.
	var hasMetadata int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ? AND table_name = 'metadata'", database,
	).Scan(&hasMetadata); err != nil {
		if isSerializationError(err) {
			return err
		}
		return fmt.Errorf("uow: identity check: probing database %q for a metadata table: %w", database, err)
	}
	if hasMetadata == 0 {
		return nil
	}
	dbProjectID, err := issueops.GetMetadataInTx(ctx, conn, "_project_id")
	if err != nil {
		if isSerializationError(err) {
			return err
		}
		if dberrors.IsTableNotExist(err) {
			// Dropped between the probe and the read.
			return nil
		}
		return fmt.Errorf("uow: identity check on database %q: %w", database, err)
	}
	if dbProjectID == "" || dbProjectID == expectedProjectID {
		return nil
	}
	return fmt.Errorf(
		"PROJECT IDENTITY MISMATCH — refusing to connect\n\n"+
			"  Local project ID (metadata.json):  %s\n"+
			"  Database %q project ID:            %s\n\n"+
			"The --database (or --db name) override for this invocation selected\n"+
			"a DIFFERENT beads project's database. Remove the override to use this\n"+
			"workspace's configured database, or run the command from that\n"+
			"project's workspace. To access it from here deliberately, set\n"+
			"BEADS_SKIP_IDENTITY_CHECK=1.",
		expectedProjectID, database, dbProjectID)
}
