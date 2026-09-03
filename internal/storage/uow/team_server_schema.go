package uow

import (
	"context"
	"fmt"

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
		// any bd write command to migrate" advice is wrong for a bts-owned schema.
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
