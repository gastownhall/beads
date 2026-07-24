package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/schema"
)

// FixGate reports whether recommending or running `bd doctor --fix` is safe
// given the relationship between the database schema and this binary (GH#4993).
//
// --fix is not a purely cosmetic repair: opening the store can apply pending
// schema migrations, and a DB ahead of the binary can resume write migrations
// the running binary does not understand. Operators who follow a printed
// "run bd doctor --fix" tip during a change-freeze have been burned by this.
type FixGate struct {
	// Safe is true when --fix may be recommended / applied for general repair.
	Safe bool
	// DBVersion is the max applied schema_migrations version (0 if unknown / unavailable).
	DBVersion int
	// BinaryVersion is schema.LatestVersion() for this binary.
	BinaryVersion int
	// Reason is non-empty when Safe is false (or when a soft caution applies).
	Reason string
	// Ahead is true when the database schema is newer than this binary.
	Ahead bool
	// Pending is true when the binary knows migrations the DB has not applied.
	Pending bool
}

// AssessSchemaFixGate probes the Dolt SQL server for schema_migrations and
// compares to this binary. If the DB cannot be opened (no server, no dolt),
// returns Safe=true with empty reason so filesystem-only doctor paths still work.
func AssessSchemaFixGate(path string) FixGate {
	binary := schema.LatestVersion()
	gate := FixGate{Safe: true, BinaryVersion: binary}

	beadsDir := ResolveBeadsDirForRepo(path)
	db, _, err := openDoltDB(beadsDir)
	if err != nil {
		return gate
	}
	defer db.Close()

	ctx := context.Background()
	dbVer, err := schema.CurrentVersion(ctx, db)
	if err != nil {
		return gate
	}
	gate.DBVersion = dbVer
	if dbVer == 0 {
		return gate
	}

	if dbVer > binary {
		gate.Safe = false
		gate.Ahead = true
		delta := dbVer - binary
		unit := "migrations"
		if delta == 1 {
			unit = "migration"
		}
		gate.Reason = fmt.Sprintf(
			"database schema is at v%d, this binary knows up to v%d (%d %s ahead). "+
				"`bd doctor --fix` can write/migrate and is unsafe on a newer schema — upgrade bd first",
			dbVer, binary, delta, unit,
		)
		return gate
	}

	if dbVer < binary {
		gate.Pending = true
		// Soft gate: still allow --fix for filesystem fixes, but do not
		// recommend bare --fix as a catch-all (it may apply schema migrations).
		gate.Safe = false
		delta := binary - dbVer
		unit := "migrations"
		if delta == 1 {
			unit = "migration"
		}
		gate.Reason = fmt.Sprintf(
			"database schema is at v%d, this binary expects v%d (%d pending %s). "+
				"`bd doctor --fix` may apply write migrations — upgrade/migrate deliberately, not as a cosmetic tip",
			dbVer, binary, delta, unit,
		)
		return gate
	}

	return gate
}

// SanitizeFixRecommendation rewrites Fix text that steers operators into
// `bd doctor --fix` when the schema gate says that is unsafe (GH#4993).
func SanitizeFixRecommendation(fix string, gate FixGate) string {
	if gate.Safe || fix == "" {
		return fix
	}
	if !strings.Contains(fix, "doctor --fix") && !strings.Contains(fix, "bd doctor --fix") {
		return fix
	}
	// Keep any non-fix alternative already present (e.g. "or bd init").
	alt := fix
	// Prefer a clear refuse over a tip that sounds optional.
	if gate.Ahead {
		return fmt.Sprintf(
			"Do NOT run 'bd doctor --fix' until bd is upgraded (schema gate: %s). Original tip was: %s",
			gate.Reason, alt,
		)
	}
	if gate.Pending {
		return fmt.Sprintf(
			"Avoid 'bd doctor --fix' for cosmetic repair while schema migrations are pending (%s). Prefer targeted fixes or an intentional migrate. Original tip was: %s",
			gate.Reason, alt,
		)
	}
	return fix
}

// RefuseDoctorFix reports whether applyFixes should hard-abort (schema ahead only).
// Pending migrations still allow filesystem-only repairs; applyFixes should warn.
func (g FixGate) RefuseDoctorFix() bool {
	return g.Ahead && !g.Safe
}
