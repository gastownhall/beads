package schema

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// CloneLocalBehindError is returned when a database is opened on a path that
// cannot migrate it (read-only opens, gateway opens) and its clone-local
// migration series — the dolt-ignored lane: leases, wisps and the other
// per-clone tables — is behind this binary's and lacks a column the caller's
// issue reads select. Clone-local DDL never replicates: `bd dolt pull` brings
// the shared (main-series) schema only, so after following the documented
// pull-then-upgrade recipe a consumer clone can sit at the latest shared
// schema with outdated clone-local tables, and without this check its reads
// fail on a raw unknown-column SQL error instead of a named refusal with a
// remedy (gastownhall/beads#7038).
type CloneLocalBehindError struct {
	// DBIgnoredVersion is the effective clone-local cursor: the raw
	// ignored_schema_migrations value clamped by the sentinel probes, never a
	// raw read a contradicted cursor could inflate.
	DBIgnoredVersion int
	// LatestIgnoredVersion is this binary's latest clone-local migration.
	LatestIgnoredVersion int
	// MissingColumns are the projected clone-local columns the database
	// lacks, each qualified by its table (leases.granted_node,
	// wisps.arrival_seq).
	MissingColumns []string
	// MainVersion is the main-series cursor as this check read it, and
	// LatestMainVersion this binary's latest main migration, so the message
	// can tell "the shared schema is current; only the local series lags"
	// apart from "finish the shared-schema procedure first" — the remedy for
	// a main-current database is one local command, while a main-behind one
	// still owes the designated-migrator/pull procedure.
	MainVersion       int
	LatestMainVersion int
	// Gateway marks a database a gateway server serves. The gateway owns its
	// schema and bd never migrates it, so the remedy is the server
	// administrator's rather than `bd migrate schema` — the same line
	// SchemaBehindError.Gateway draws.
	Gateway bool
}

// cloneLocalBehindMessage renders the shared core of the refusal: what is
// behind, what is missing, and why pull cannot fix it. gatewayVariant and the
// main-series posture are layered on by Error and UserMessage.
func (e *CloneLocalBehindError) cloneLocalBehindMessage() string {
	return fmt.Sprintf("clone-local schema mismatch: this database's clone-local migration series (leases, wisps and the other per-clone tables) is at v%d of v%d and lacks column(s) %s, which this binary's issue reads select",
		e.DBIgnoredVersion, e.LatestIgnoredVersion, strings.Join(e.MissingColumns, ", "))
}

// mainPostureClause names where the shared (main-series) schema stands, so
// the remedy never claims a main-current database it does not have: a database
// still behind on the main series owes the designated-migrator/pull procedure
// first, and only then the one local migration command.
func (e *CloneLocalBehindError) mainPostureClause() string {
	if e.MainVersion >= e.LatestMainVersion {
		return "The shared schema is current, so this step needs no further shared-schema migration consent"
	}
	return fmt.Sprintf("The shared schema is also behind (v%d of v%d) — finish the designated-migrator/pull procedure for it first (see the multi-clone recipe in docs/getting-started/upgrading.md)", e.MainVersion, e.LatestMainVersion)
}

func (e *CloneLocalBehindError) Error() string {
	if e.Gateway {
		return e.cloneLocalBehindMessage() +
			". Clone-local migrations never arrive with `bd dolt pull` — the pull brings only the shared schema — and the gateway server owns this database's schema and bd never migrates it. Ask the server administrator to apply the database's clone-local migrations, or keep using a bd release that matches clone-local v" + fmt.Sprint(e.DBIgnoredVersion)
	}
	return e.cloneLocalBehindMessage() +
		". Clone-local migrations never arrive with `bd dolt pull` — the pull brings only the shared schema. " +
		e.mainPostureClause() +
		"; once the shared schema is synchronized and any maintenance freeze is lifted, run `bd migrate schema` once for this database, then retry. Or set BD_IGNORE_SCHEMA_SKEW=1 to read anyway (queries touching the missing column(s) will fail)"
}

// UserMessage is the CLI's human-output block, following SchemaSkewError's
// framing convention: the one-line refusal, the remedy as an indented
// command, and the escape hatch with its risk spelled out.
func (e *CloneLocalBehindError) UserMessage() string {
	msg := e.cloneLocalBehindMessage() + ".\n" +
		"\n" +
		"  Clone-local migrations (the ignored series: leases, wisps and the other\n" +
		"  per-clone tables) never arrive with `bd dolt pull`; the pull brings only the\n" +
		"  shared schema.\n"
	if e.Gateway {
		msg += "\n" +
			"  The gateway server owns this database's schema and bd never migrates it.\n" +
			"  Ask the server administrator to apply the database's clone-local migrations.\n"
	} else {
		msg += "\n" +
			"  Once the shared schema is synchronized and any maintenance freeze is lifted,\n" +
			"  run the local migration once for this database, then retry:\n" +
			"    bd migrate schema\n" +
			"\n" +
			"  " + e.mainPostureClause() + ".\n" +
			"\n" +
			"  To proceed despite the risk (reads touching the missing column(s) will fail):\n" +
			"    BD_IGNORE_SCHEMA_SKEW=1 bd <command>\n"
	}
	return msg
}

// EscapeHint is the non-runnable directive the JSON error carries. The
// command itself lives here rather than only in prose because the main series
// is what the consent gates guard, and a database this refusal names is either
// main-current (the verb applies the local series with no extra consent) or
// main-behind (the clause says to finish that procedure first).
func (e *CloneLocalBehindError) EscapeHint() string {
	if e.Gateway {
		return "ask the server administrator to apply this database's clone-local migrations"
	}
	return "run `bd migrate schema` once for this database once the shared schema is synchronized and any maintenance freeze is lifted"
}

// IsCloneLocalBehindError reports whether err (or any error it wraps) is a
// *CloneLocalBehindError.
func IsCloneLocalBehindError(err error) bool {
	var e *CloneLocalBehindError
	return errors.As(err, &e)
}

// CloneLocalProjection is one clone-local table the caller's issue reads
// select from, and the columns they select there. Optional marks a table a
// database may legitimately lack — no wisps table reads as no wisps
// (sqlbuild.OptionalWispTable) — so its absence refuses nothing; a required
// table that is absent reads as every one of its columns missing.
type CloneLocalProjection struct {
	Table    string
	Columns  []string
	Optional bool
}

// CheckCloneLocalReadable is CheckReadableDrift's clone-local counterpart: it
// guards an open that reads a database at its current clone-local schema
// instead of migrating it (read-only and gateway opens). From one effective
// cursor read (CurrentIgnoredVersion, sentinel-clamped) it refuses nothing
// while the cursor is at this binary's latest clone-local version; a behind
// cursor is only believed to matter once one of projections' tables lacks a
// column the caller's reads select there. A behind cursor with all of them
// present stays readable — the same tolerance CheckReadableDrift gives a
// behind main series, and for the same reason: reads keep working through
// the window until the operator migrates. Only a missing column refuses,
// with CloneLocalBehindError's named remedy instead of the raw unknown-column
// error the reads that select it would otherwise fail on
// (gastownhall/beads#7038). BD_IGNORE_SCHEMA_SKEW=1 downgrades the refusal
// to a warning, mirroring behindDriftError — without widening what it
// bypasses: the open still does not migrate, and reads touching the missing
// column still fail.
//
// Every table name is a hardcoded identifier, never user input.
func CheckCloneLocalReadable(ctx context.Context, db DBConn, projections []CloneLocalProjection) error {
	current, err := CurrentIgnoredVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("clone-local readable-schema check: %w", err)
	}
	if current >= LatestIgnoredVersion() {
		// At latest, and the effective cursor is already sentinel-clamped:
		// believing it costs no extra probe, unlike a raw cursor read a
		// contradicted schema could inflate.
		return nil
	}
	missing, err := missingProjectedColumns(ctx, db, projections)
	if err != nil {
		return fmt.Errorf("clone-local readable-schema check: %w", err)
	}
	if len(missing) == 0 {
		return nil
	}
	if os.Getenv("BD_IGNORE_SCHEMA_SKEW") == "1" {
		fmt.Fprintf(os.Stderr,
			"Warning: clone-local schema skew ignored — database (clone-local v%d, binary v%d) is opened without migrating; queries touching %s may fail\n",
			current, LatestIgnoredVersion(), strings.Join(missing, ", "))
		return nil
	}
	mainVersion, err := CurrentVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("clone-local readable-schema check: %w", err)
	}
	return &CloneLocalBehindError{
		DBIgnoredVersion:     current,
		LatestIgnoredVersion: LatestIgnoredVersion(),
		MissingColumns:       missing,
		MainVersion:          mainVersion,
		LatestMainVersion:    LatestVersion(),
	}
}

// missingProjectedColumns returns the columns projections select that the
// database lacks, each qualified by its table, in projection order. An
// optional table that does not exist contributes none.
func missingProjectedColumns(ctx context.Context, db DBConn, projections []CloneLocalProjection) ([]string, error) {
	var missing []string
	for _, p := range projections {
		exists, err := tableInCurrentDatabase(ctx, db, p.Table)
		if err != nil {
			return nil, fmt.Errorf("checking table %s: %w", p.Table, err)
		}
		if !exists && p.Optional {
			continue
		}
		columns, err := missingColumnsIn(ctx, db, p.Table, exists, p.Columns)
		if err != nil {
			return nil, err
		}
		for _, column := range columns {
			missing = append(missing, p.Table+"."+column)
		}
	}
	return missing, nil
}
