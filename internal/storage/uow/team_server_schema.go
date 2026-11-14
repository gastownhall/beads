package uow

import (
	"context"
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

// excursionUseDatabaseRemedy guides the failure shapes a plain excursion's
// database selection can hit (classifyUseDatabaseError). The missing arm is
// unreachable from checkExcursionIdentity — an absent target skips the
// identity comparison there and lets the open path decide — so only the
// unclassified arm's text matters; it says where in the open the failure
// happened rather than prescribing a provisioning remedy that only a server
// administrator could act on.
var excursionUseDatabaseRemedy = useDatabaseRemedy{
	missing:      "a plain proxied open creates an absent database itself",
	unclassified: "this happened while reading the target database's project identity, before the open itself ran",
}

// checkExcursionIdentity asserts the project identity of the database a plain
// (non-team-server) proxied open was explicitly pointed at with --database
// (or a name-form --db value) — the bd-rgvivq guard (gastownhall/beads#5572).
// The connection arrives with no database selected: this check selects the
// target, proves the session really landed on it, and reads the stored
// identity itself, so the refusal lands before migration locks, database
// creation, migrations, version reconciliation, or any command runs.
//
// It is deliberately narrower than checkTeamServerIdentity, which runs on
// every team-server open:
//
//   - expectedProjectID is empty for every open but a genuine excursion: the
//     CLI topology resolver (resolveProxiedServerUOWTopology) clears it for
//     plain workspaces unless an explicit override names a database other
//     than the workspace's configured one and BEADS_SKIP_IDENTITY_CHECK is
//     unset. An empty value means "no assertion available" and skips the
//     check without any SQL.
//   - A target with no beads identity — no metadata table (a non-beads
//     database, such as the widgets excursion TestProxiedServerSQLDatabaseFlag
//     pins), a missing _project_id row, or an empty stored value — passes
//     without minting, changing, or adopting anything: there is nothing to
//     compare against, and the create/preview/migrate path below the check
//     decides what happens next.
//   - A denial selecting the target takes precedence over absence and fails
//     the open: a server hides database existence from a credential it has
//     not granted, so a denial must not be read as "no identity" (see
//     isDatabaseNotFoundError's caveat).
//   - A positive mismatch is refused: the override selected another beads
//     project's database, which is exactly the silent wrong-project access
//     the reported bug is about. The remedy names the plain-mode escape
//     hatch, not team-server provisioning.
//
// Unlike the team-server path there is no schema check that already proved
// the metadata table exists, so its absence is classified (dberrors) rather
// than assumed, and every other read error fails the open: permission,
// connection, and malformed-table errors must not be read as "no identity".
// A database whose schema carries a _project_id but no migration history is
// still compared — identity is the fact being asserted, not schema version.
func checkExcursionIdentity(ctx context.Context, conn schema.DBConn, database, expectedProjectID string) error {
	if expectedProjectID == "" {
		return nil
	}
	if err := db.NewDDLSQLRepository(conn).UseDatabase(ctx, database); err != nil {
		switch {
		case isDatabaseNotFoundError(err):
			// A genuinely absent target has no identity to compare. The open
			// path below owns what happens next: the migrating open creates
			// the database, a preview open refuses it with its own remedy —
			// exactly the behavior that predates this check.
			return nil
		case isSerializationError(err):
			return err
		default:
			// classifyUseDatabaseError answers denial before anything else,
			// so an access-denied selection is reported as a denial, not as
			// an absence it must not claim to know.
			return classifyUseDatabaseError(err, database, excursionUseDatabaseRemedy)
		}
	}
	// A USE that returns no error is not proof the session moved: a front
	// door that scopes connections by credential can accept the name and
	// serve its own database (see assertSessionDatabase), and the identity
	// read below is unqualified — it would read the SESSION's database while
	// bd attributes it to the requested one.
	if err := assertSessionDatabase(ctx, conn, database); err != nil {
		return err
	}
	dbProjectID, err := issueops.GetMetadataInTx(ctx, conn, "_project_id")
	if err != nil {
		if dberrors.IsMissingTable(err, "metadata") {
			// No metadata table: the target is not a beads database, so it
			// carries no identity to assert. The excursion stays available.
			return nil
		}
		return fmt.Errorf("uow: excursion identity check on database %q: %w", database, err)
	}
	if dbProjectID == "" {
		// Missing row or empty stored value: no identity on the target side.
		// Soft-skip, mirroring DoltStore.verifyProjectIdentity's treatment of
		// databases that predate project identity.
		return nil
	}
	if dbProjectID != expectedProjectID {
		return fmt.Errorf(
			"PROJECT IDENTITY MISMATCH — refusing to connect\n\n"+
				"  Local project ID (metadata.json):  %s\n"+
				"  Database %q project ID:            %s\n\n"+
				"--database (or a --db name override) selected a database that\n"+
				"belongs to a DIFFERENT beads project. Remove the override, or run\n"+
				"the command from that project's own workspace. To deliberately\n"+
				"access another project's database, set BEADS_SKIP_IDENTITY_CHECK=1.",
			expectedProjectID, database, dbProjectID)
	}
	return nil
}
