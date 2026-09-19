package schema

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/storage/rowid"
)

// auxRekeyPass names one clone-local convergence pass over the aux tables.
// Each pass is gated three ways (bd-578h9): markerVersion is the ignored
// (clone-local) migration that records the pass's completion — the rewrite
// runs only while it is still pending, and ignoredSource.migrate records it
// later in the same MigrateUp pass; shippedMainVersion is the main-source
// schema version that shipped in the same release, distinguishing the
// lineage's first pass-aware migration from a fresh clone of an
// already-converged lineage (skip the rewrite, record the marker —
// bd-578h9.4); sentinelKey is the clone-local in-progress sentinel
// (local_metadata, dolt-ignored) set before the first UPDATE and cleared
// after the last table, so a crashed pass resumes on the next MigrateUp even
// though the main cursor already advanced past shippedMainVersion in the
// crashed pass (bd-578h9.16).
type auxRekeyPass struct {
	markerVersion      int
	shippedMainVersion int
	sentinelKey        string
}

// auxRekeyPassInitial is the original bd-6dnrw.2 backfill: converge the
// primary keys that migration 0037 randomized per-clone.
var auxRekeyPassInitial = auxRekeyPass{
	markerVersion:      9,
	shippedMainVersion: 51,
	sentinelKey:        "aux_row_rekey_in_progress",
}

// auxRekeyPassDerivedInsert is the bd-ri8bd catch-up: between the initial
// backfill and the switch to content-derived ids at insert time
// (issueops.InsertDerivedEvent and friends), new rows minted random UUIDv7
// keys. Those are minted-once-then-merged (consistent across clones), which
// was fine under the versioned union merge, but the unversioned newest-wins
// replication of Protocol v0.1 §C converges only rows whose ids are functions
// of their content — so this pass re-derives the interim rows once. It reuses
// the initial pass's rewrite verbatim (the derivation is unchanged and
// idempotent: rows already holding a derived id keep it).
var auxRekeyPassDerivedInsert = auxRekeyPass{
	markerVersion:      18,
	shippedMainVersion: 61,
	sentinelKey:        "aux_row_rekey2_in_progress",
}

// auxRekeyPasses lists every convergence pass, in the order MigrateUp runs
// them. Every pass here runs the identical rekeyAuxRowTable rewrite over the
// identical auxRekeyTables set (see auxRowRekeyDriftedKey) — a future pass
// that rewrites a different table set or a different derivation must not be
// added to this list without also giving it its own drift record; sharing
// auxRowRekeyDriftedKey across passes with different table sets or
// derivations would let one pass clear a drift skip the other pass never
// actually resolved.
var auxRekeyPasses = []auxRekeyPass{auxRekeyPassInitial, auxRekeyPassDerivedInsert}

// A pass's sentinel means "a rewrite is in flight", nothing else. MigrateUp
// reads the sentinels to exempt the aux tables from the pre-existing-dirty
// guards, because a crashed pass's own partial UPDATEs are sitting in the
// working set — so a sentinel must not outlive an actual in-flight rewrite. A
// drift skip (#4380) therefore clears it like any completed pass and records
// auxRowRekeyDriftedKey instead.
//
// auxRowRekeyDriftedKey is the clone-local record (local_metadata,
// dolt-ignored) of tables the re-key had to skip because their storage carries
// dolthub/dolt#11131 encoding drift (#4380): a comma-separated table list,
// absent when there is nothing skipped.
//
// It exists because the skip completes the pass, so MigrateUp records the
// clone-local marker in that same pass and the marker gate can never re-admit
// the re-key again. This record is what re-admits it — and it names the
// affected tables so the resumed pass touches only those, leaving tables that
// already converged alone.
//
// The record is clone-local because each clone has to discover, retry and
// retire its own skip; the drift itself is not necessarily clone-local, since
// both halves of it (the column's storage-encoding tag and the row chunks it
// disagrees with) are committed data that travels on push/pull — migration 0057
// documents bd's own 0048 as one way a lineage acquires it.
//
// This one record is shared by every pass in auxRekeyPasses, which is only
// correct because every pass runs the identical rekeyAuxRowTable rewrite over
// the identical auxRekeyTables set (their gates — markerVersion,
// shippedMainVersion — are all that differ): whichever pass retries a skipped
// table first does the same work any other pass would have done, and clearing
// the record on success correctly makes every other pass's retry a no-op.
const auxRowRekeyDriftedKey = "aux_row_rekey_drifted"

// auxRekeyState is what this clone still owes the re-key: a rewrite that was in
// flight and never finished, and tables skipped for storage drift. Both live in
// local_metadata, which is dolt-ignored and therefore clone-local — and, per
// migration 0030, ephemeral: it is recreated empty by a working-set reset, so
// "no record" is always a legitimate reading.
type auxRekeyState struct {
	resume  bool
	drifted []string
}

func (s auxRekeyState) pending() bool { return s.resume || len(s.drifted) > 0 }

// readAuxRekeyState reads the given crash sentinels and the #4380 drift record
// in one round trip. A missing local_metadata table means nothing is set: the
// table is dolt-ignored and therefore clone-local, so a fresh clone lacks it
// until something recreates it — setAuxRekeyInProgress creates it on demand.
//
// state.drifted is filtered against auxRekeyTables before it is returned: the
// drift record is a free-form comma-separated cell, so this is the one place
// that guarantees every name a caller sees is actually one of the re-keyed
// tables. Callers rely on that — MigrateUp uses state.drifted directly to
// exempt tables from the pre-existing-dirty guard, and an unfiltered name
// there would let a non-aux table bypass those guards and get force-staged
// into the migration commit.
func readAuxRekeyState(ctx context.Context, db DBConn, sentinelKeys ...string) (auxRekeyState, error) {
	var state auxRekeyState
	var tableCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM INFORMATION_SCHEMA.TABLES
		 WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'local_metadata'`,
	).Scan(&tableCount); err != nil {
		return state, err
	}
	if tableCount == 0 {
		return state, nil
	}
	placeholders := make([]string, 0, len(sentinelKeys)+1)
	args := make([]any, 0, len(sentinelKeys)+1)
	for _, key := range sentinelKeys {
		placeholders = append(placeholders, "?")
		args = append(args, key)
	}
	placeholders = append(placeholders, "?")
	args = append(args, auxRowRekeyDriftedKey)
	//nolint:gosec // G201: only ? placeholders are interpolated; every value is bound.
	rows, err := db.QueryContext(ctx,
		fmt.Sprintf("SELECT `key`, value FROM local_metadata WHERE `key` IN (%s)", strings.Join(placeholders, ", ")),
		args...)
	if err != nil {
		// This is the one place the re-key reads a TEXT cell of local_metadata,
		// so it is the one place a drifted local_metadata could panic the very
		// function meant to survive drift. No migration re-encodes that table
		// today, but degrade to "nothing recorded" rather than re-break the
		// open path if that ever changes.
		if isSchemaEncodingDriftErr(err) {
			return state, nil
		}
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			// Same drift degradation as the QueryContext and rows.Err() siblings
			// that bracket this loop: this is the one place the re-key reads a
			// TEXT cell of local_metadata, so a #11131 decode panic surfacing
			// here — were a future migration ever to re-encode the table — must
			// degrade to "nothing recorded" rather than re-break the open path
			// this defense exists to protect. No reachable input triggers it
			// today (a Dolt decode panic surfaces via Next/Err, not the Scan of
			// already-streamed values), so this is purely defensive symmetry.
			if isSchemaEncodingDriftErr(err) {
				return auxRekeyState{}, nil
			}
			return auxRekeyState{}, err
		}
		switch key {
		case auxRowRekeyDriftedKey:
			for _, name := range strings.Split(value, ",") {
				if name = strings.TrimSpace(name); name != "" && auxRekeyTableNames[name] {
					state.drifted = append(state.drifted, name)
				}
			}
		default:
			// The query only asks for the requested sentinel keys and the drift
			// record, so any other returned key is a present sentinel.
			state.resume = true
		}
	}
	if err := rows.Err(); err != nil {
		if isSchemaEncodingDriftErr(err) {
			return auxRekeyState{}, nil
		}
		return auxRekeyState{}, err
	}
	return state, nil
}

// auxRekeyExemptTables reports which aux tables MigrateUp must drop from its
// dirtyBefore set before running the re-key: exactly the tables the upcoming
// rekeyAuxRowIDsAllPasses will rewrite, unioned across every pass.
//
// It is deliberately derived from the same per-pass tablesToRekey selection the
// rewrite itself uses, so the exemption can never be wider than the rewrite.
// That matters because a post-marker resume re-keys only the recorded drifted
// subset (bd-578h9 / #4380): exempting all four aux tables there — as a single
// collapsed "some rewrite is in flight" bit would — drops a non-drifted aux
// table's pre-existing user edits out of dirtyBefore, and stageSchemaTables
// then sweeps them into the "schema: apply migrations" commit. Keying the
// exemption off the exact rewrite set closes that asymmetry by construction.
func auxRekeyExemptTables(ctx context.Context, db DBConn, mainVersionBefore int) (map[string]bool, error) {
	pending, err := ignoredSource.pendingVersions(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("reading pending ignored migrations: %w", err)
	}
	exempt := make(map[string]bool)
	for _, pass := range auxRekeyPasses {
		state, err := readAuxRekeyState(ctx, db, pass.sentinelKey)
		if err != nil {
			return nil, fmt.Errorf("reading aux rekey state: %w", err)
		}
		tables, _ := pass.tablesToRekey(mainVersionBefore, pending, state)
		for _, t := range tables {
			exempt[t.name] = true
		}
	}
	return exempt, nil
}

func setAuxRekeyInProgress(ctx context.Context, db DBConn, sentinelKey string) error {
	// local_metadata is dolt-ignored, hence clone-local: a fresh clone whose
	// main cursor is already past 0029 does not have the table (the migration
	// will not re-run) until EnsureIgnoredTables recreates it. Create it here
	// with 0029's DDL — its dolt_ignore pattern is committed history, so the
	// sentinel stays clone-local.
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS local_metadata (`key` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL DEFAULT '')"); err != nil {
		return fmt.Errorf("ensuring local_metadata: %w", err)
	}
	_, err := db.ExecContext(ctx,
		"REPLACE INTO local_metadata (`key`, value) VALUES (?, '1')",
		sentinelKey)
	return err
}

func clearAuxRekeyInProgress(ctx context.Context, db DBConn, sentinelKey string) error {
	_, err := db.ExecContext(ctx,
		"DELETE FROM local_metadata WHERE `key` = ?",
		sentinelKey)
	return err
}

func setAuxRekeyDrifted(ctx context.Context, db DBConn, tables []string) error {
	// Same on-demand create as the sentinel: local_metadata is dolt-ignored, so
	// a clone that arrived past 0029 may not have it yet.
	if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS local_metadata (`key` VARCHAR(255) PRIMARY KEY, value TEXT NOT NULL DEFAULT '')"); err != nil {
		return fmt.Errorf("ensuring local_metadata: %w", err)
	}
	_, err := db.ExecContext(ctx,
		"REPLACE INTO local_metadata (`key`, value) VALUES (?, ?)",
		auxRowRekeyDriftedKey, strings.Join(tables, ","))
	return err
}

func clearAuxRekeyDrifted(ctx context.Context, db DBConn) error {
	_, err := db.ExecContext(ctx,
		"DELETE FROM local_metadata WHERE `key` = ?",
		auxRowRekeyDriftedKey)
	return err
}

// auxRekeyTable describes one table covered by the re-key. columns is the
// frozen SELECT list of every non-id column, in creation order, with datetime
// columns CAST to CHAR server-side so the scanned text is identical across
// drivers and connection settings.
//
// These lists are part of the id derivation and are FROZEN: they must keep
// naming exactly the columns the tables had when this backfill shipped, even
// if later migrations add columns. Clones upgrade at different binary
// versions; only a version-independent column set makes two clones derive the
// same id for the same ancestral row.
type auxRekeyTable struct {
	name    string
	columns string
}

// auxRekeyTables covers the four synced tables whose 0037 backfill randomized
// primary keys across clones. The wisp_ twins (wisp_events, wisp_comments)
// are deliberately excluded: they are dolt-ignored and never merged, so a
// wisp row's id only matters once promotion copies the row (id and all) into
// the synced table — where it is minted-once-then-replicated, i.e. random but
// consistent everywhere, exactly like any other single-origin row.
var auxRekeyTables = []auxRekeyTable{
	{
		name:    "events",
		columns: "issue_id, event_type, actor, old_value, new_value, comment, CAST(created_at AS CHAR)",
	},
	{
		name:    "comments",
		columns: "issue_id, author, text, CAST(created_at AS CHAR)",
	},
	{
		name:    "issue_snapshots",
		columns: "issue_id, CAST(snapshot_time AS CHAR), compaction_level, original_size, compressed_size, original_content, archived_events",
	},
	{
		name:    "compaction_snapshots",
		columns: "issue_id, compaction_level, snapshot_json, CAST(created_at AS CHAR)",
	},
}

// auxRekeyTableNames is auxRekeyTables' name set, used by readAuxRekeyState to
// filter the local_metadata drift record: that cell is a free-form
// comma-separated string, not validated at write time by anything but this
// package's own writer, so a stale or corrupted entry must not reach a caller
// that trusts every name in state.drifted to be one of the four re-keyed
// tables (schema.go's MigrateUp uses it unfiltered to exempt tables from the
// pre-existing-dirty guard).
var auxRekeyTableNames = func() map[string]bool {
	names := make(map[string]bool, len(auxRekeyTables))
	for _, t := range auxRekeyTables {
		names[t.name] = true
	}
	return names
}()

// rekeyAuxRowIDs converges the primary keys that migration 0037 randomized
// (bd-6dnrw.2). 0037 backfilled the CHAR(36) ids of events, comments,
// issue_snapshots and compaction_snapshots with per-clone-random UUID()s, so
// legacy clones that migrated independently hold the same logical rows under
// different keys and their merges duplicate or refuse. This rewrites every
// row's id to the deterministic content-derived value (internal/storage/rowid)
// so independently-upgraded clones converge to byte-identical tables.
//
// It runs from MigrateUp after the schema migrations, gated on the clone-local
// marker (see auxRowRekeyMarkerVersion): once per clone, not on every later
// migration pass — rows inserted after the pass carry ids that are random but
// already consistent across clones (minted once, then merged), so re-keying
// them would churn synced tables for no convergence benefit. Changes are
// staged and committed by MigrateUp like any other backfill.
//
// mainVersionBefore is the main-source cursor as it stood before this pass's
// migrations ran; at or past auxRowRekeyShippedMainVersion the rewrite is
// skipped (see that constant).
//
// A table whose storage carries dolthub/dolt#11131 encoding drift cannot be
// scanned at all, so it is skipped and recorded (auxRowRekeyDriftedKey) rather
// than failing the pass; a later pass re-keys just that table, and succeeds
// once the drift is repaired. Like the crash-resume path it re-keys the whole
// table, including rows minted since the skip, so the sooner the retry lands
// the fewer of those there are — which is why it is re-attempted on every
// subsequent migration pass rather than waiting to be asked.
func rekeyAuxRowIDs(ctx context.Context, db DBConn, mainVersionBefore int, pass auxRekeyPass) (bool, error) {
	pending, err := ignoredSource.pendingVersions(ctx, db)
	if err != nil {
		return false, fmt.Errorf("reading pending ignored migrations: %w", err)
	}
	return rekeyAuxRowIDsPending(ctx, db, mainVersionBefore, pass, pending)
}

// rekeyAuxRowIDsAllPasses runs every convergence pass off a single read of
// the ignored cursor.
func rekeyAuxRowIDsAllPasses(ctx context.Context, db DBConn, mainVersionBefore int) (bool, error) {
	pending, err := ignoredSource.pendingVersions(ctx, db)
	if err != nil {
		return false, fmt.Errorf("reading pending ignored migrations: %w", err)
	}
	wrote := false
	for _, pass := range auxRekeyPasses {
		w, err := rekeyAuxRowIDsPending(ctx, db, mainVersionBefore, pass, pending)
		wrote = wrote || w
		if err != nil {
			return wrote, err
		}
	}
	return wrote, nil
}

// tablesToRekey reports which aux tables this pass rewrites on the current
// MigrateUp, and whether it runs at all. It is the single source of truth for
// that selection: rekeyAuxRowIDsPending drives its own rewrite from it, and
// MigrateUp's dirtyBefore exemption (auxRekeyExemptTables) unions it across
// passes — so the set of tables exempted from the pre-existing-dirty guards can
// never diverge from the set the rewrite actually touches (#4380).
//
// runs is false only when neither gate admits the pass. It stays true for a
// post-marker resume whose table set is empty — a sentinel left set after the
// drift record was already cleared — so the caller still proceeds to clear that
// stale sentinel instead of leaking it.
func (pass auxRekeyPass) tablesToRekey(mainVersionBefore int, pending []int, state auxRekeyState) ([]auxRekeyTable, bool) {
	markerPending := slices.Contains(pending, pass.markerVersion)

	// The marker gate is "once per clone" only for a pass that actually
	// converged every table. A pass that skipped one for #11131 drift completed
	// — so the marker was recorded in that same MigrateUp — and the marker alone
	// would then bar re-entry forever, making the skip permanent. A crash
	// sentinel or a drift record (state.pending()) re-admits the pass so it can
	// finish once the storage is repaired.
	if !markerPending && !state.pending() {
		return nil, false
	}
	// The fresh-clone skip (record the marker, skip the rewrite — bd-578h9.4)
	// must not fire on a lineage whose previous pass crashed mid-rekey: that
	// pass already advanced the main cursor past the shipped version, but its
	// sentinel — or a recorded drift skip — proves the rewrite never finished
	// (bd-578h9.16).
	if mainVersionBefore >= pass.shippedMainVersion && !state.pending() {
		return nil, false
	}

	// A markerPending pass is the one-time convergence and covers all four
	// tables. Once the marker is recorded, MigrateUp records it
	// (ignoredSource.migrate) only after the rewrite returns without error, so a
	// clone that still owes work past that point owes it for exactly the tables
	// the drift record names. Re-keying any other table there would be wrong —
	// those converged in the completed pass, and rows minted since carry
	// app-minted ids already consistent across clones, so rewriting them on this
	// clone alone manufactures the divergence the re-key exists to remove. This
	// also covers a drift-resume pass that itself died on an unrelated error and
	// left the sentinel set: still a post-marker pass, so still only the
	// recorded tables.
	if markerPending {
		return auxRekeyTables, true
	}
	// This is a selection, not a validation filter: state.drifted is already
	// guaranteed a subset of auxRekeyTables' names (readAuxRekeyState filters
	// it), so slices.Contains here is what maps those names to the full
	// auxRekeyTable structs (with their frozen columns) this pass actually needs.
	var tables []auxRekeyTable
	for _, t := range auxRekeyTables {
		if slices.Contains(state.drifted, t.name) {
			tables = append(tables, t)
		}
	}
	return tables, true
}

func rekeyAuxRowIDsPending(ctx context.Context, db DBConn, mainVersionBefore int, pass auxRekeyPass, pending []int) (bool, error) {
	state, err := readAuxRekeyState(ctx, db, pass.sentinelKey)
	if err != nil {
		return false, fmt.Errorf("reading aux rekey state: %w", err)
	}
	tables, runs := pass.tablesToRekey(mainVersionBefore, pending, state)
	if !runs {
		return false, nil
	}

	// Sentinel before the first UPDATE: a crash anywhere in the rewrite
	// leaves it set, so the next pass resumes (the rewrite is idempotent)
	// instead of recording the marker over partially re-keyed rows.
	if err := setAuxRekeyInProgress(ctx, db, pass.sentinelKey); err != nil {
		return false, fmt.Errorf("recording aux rekey sentinel: %w", err)
	}

	wrote := false
	var skipped []string
	for _, t := range tables {
		w, err := rekeyAuxRowTable(ctx, db, t)
		wrote = wrote || w
		if err != nil {
			// #4380: a table carrying dolthub/dolt#11131 schema-encoding drift
			// cannot be re-keyed at all — every read of an affected cell panics
			// server-side, and no SQL write can repair or remove the row. Before
			// this, that aborted the whole MigrateUp pass, which also made the
			// database unopenable by any rekey-aware binary: the migration is
			// re-attempted on open, so the panic recurred on every start and the
			// only build that could read the data was the pre-re-key one. Skip
			// the table loudly and let the rest of the pass complete instead.
			if isSchemaEncodingDriftErr(err) {
				skipped = append(skipped, t.name)
				// log, not the TTY-gated progress writer: a piped or CI caller
				// discards that writer, and these three lines are the only
				// notice that a table's ids stayed divergent.
				log.Printf("schema migration: aux row id re-key skipped %q — the table holds rows Dolt cannot decode (schema-encoding drift, gastownhall/beads#4380): %v",
					t.name, err)
				continue
			}
			return wrote, fmt.Errorf("%s: %w", t.name, err)
		}
	}
	// The tables this pass covered are exactly the ones the record should now
	// name: a full pass covered all four, a post-marker pass covered precisely
	// the previously-recorded ones, so in both cases what is still skipped is
	// what failed just now.
	switch {
	case len(skipped) > 0:
		if err := setAuxRekeyDrifted(ctx, db, skipped); err != nil {
			return wrote, fmt.Errorf("recording aux rekey drift: %w", err)
		}
		log.Printf("schema migration: %d table(s) kept their old row ids: %s — merges with other clones of this database may duplicate those rows",
			len(skipped), strings.Join(skipped, ", "))
		log.Printf("schema migration: repair the storage drift (see gastownhall/beads#4380); the re-key is re-attempted on each later pass that applies schema migrations")
	case len(state.drifted) > 0:
		if err := clearAuxRekeyDrifted(ctx, db); err != nil {
			return wrote, fmt.Errorf("clearing aux rekey drift record: %w", err)
		}
		// Names what this pass actually covered, which is the recorded set
		// minus any entry no longer in auxRekeyTables — a stale name is dropped
		// by clearing the record, not by claiming it converged.
		if len(tables) > 0 {
			covered := make([]string, 0, len(tables))
			for _, t := range tables {
				covered = append(covered, t.name)
			}
			log.Printf("schema migration: aux row id re-key completed for previously skipped table(s): %s",
				strings.Join(covered, ", "))
		}
	}
	if err := clearAuxRekeyInProgress(ctx, db, pass.sentinelKey); err != nil {
		return wrote, fmt.Errorf("clearing aux rekey sentinel: %w", err)
	}
	return wrote, nil
}

// isSchemaEncodingDriftErr reports whether err carries the dolthub/dolt#11131
// schema-encoding-drift signature: a TEXT/LONGTEXT column whose on-disk
// storage-encoding tag was re-derived without rewriting rows, so decoding a
// cell reads an address of the wrong width and panics inside the storage
// engine. The engine recovers it per query and surfaces
// "Error 1105 (HY000): panic recovered: invalid hash length: 19". Only this
// class is tolerated by the re-key; every other failure still aborts the pass.
//
// Deliberately matched on the bare hash-length phrase, not on ": 19" and not
// on the "panic recovered" wrapper:
//
//   - the width varies with the direction of the tag flip — migration 0057
//     documents this same drift from bd's own side (0048 re-widening an
//     already-LONGTEXT column under a pre-#11126 embedded engine) and records
//     both "invalid hash length: 19" on read and "invalid hash length: 1" on
//     insert/merge;
//   - the wrapper is added by the SQL engine, so a path that surfaced the
//     panic unwrapped would fall through to the abort that leaves affected
//     databases unopenable — the failure this whole change exists to end.
//
// The cost of the wider match is bounded: a non-#11131 "invalid hash length"
// (dolt raises it from hash.New generally) means storage this re-key cannot
// read either, so skipping the table and recording it is the same correct
// answer. The pass still aborts on every error that does not carry the phrase.
//
// What it cannot catch: the same drift also produces cells that decode to
// garbage instead of panicking (#4380 reports ~10% of affected cells returning
// a zero payload). Those scan without error, so their rows are re-keyed from
// corrupt content and converge to an id no healthy clone derives. Nothing here
// can detect that — it is a reason the storage repair is still required, not a
// reason to widen the match further.
func isSchemaEncodingDriftErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "invalid hash length")
}

// rekeyAuxRowTable re-derives the ids of one table. The whole table is grouped
// by content digest; each digest's rows take the deterministic ids for
// ordinals 0..n-1. A row already holding one of its group's target ids keeps
// it (idempotence: re-running never swaps ids within a group), and the
// remaining rows take the remaining targets in sorted-current-id order. Across
// clones that assignment may permute within a group of exact-duplicate rows,
// but duplicates are interchangeable and the id set is identical, so the
// merged result still converges.
func rekeyAuxRowTable(ctx context.Context, db DBConn, t auxRekeyTable) (bool, error) {
	// Skip cleanly if the table or its id column isn't present (older or partial
	// schema): nothing to re-key. After MigrateUp's main pass the id column is
	// CHAR(36) on any schema this runs against (0037 precedes the marker).
	hasID, err := columnExists(ctx, db, t.name, "id")
	if err != nil {
		return false, err
	}
	if !hasID {
		return false, nil
	}

	//nolint:gosec // G201: name/columns come from the hardcoded auxRekeyTables, never user input.
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT id, %s FROM %s`, t.columns, t.name))
	if err != nil {
		return false, err
	}
	nFields := strings.Count(t.columns, ",") + 1
	groups := make(map[string][]string)
	for rows.Next() {
		var id string
		fields := make([]sql.NullString, nFields)
		dests := make([]any, 0, nFields+1)
		dests = append(dests, &id)
		for i := range fields {
			dests = append(dests, &fields[i])
		}
		if err := rows.Scan(dests...); err != nil {
			_ = rows.Close()
			return false, err
		}
		digest := rowid.Digest(fields)
		groups[digest] = append(groups[digest], id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}

	type rekey struct{ oldID, newID string }
	var todo []rekey
	for digest, ids := range groups {
		targets := make([]string, len(ids))
		targetSet := make(map[string]bool, len(ids))
		for i := range ids {
			targets[i] = rowid.New(t.name, i, digest)
			targetSet[targets[i]] = true
		}
		held := make(map[string]bool, len(ids))
		var free []string
		for _, id := range ids {
			if targetSet[id] {
				held[id] = true
			} else {
				free = append(free, id)
			}
		}
		if len(free) == 0 {
			continue
		}
		sort.Strings(free)
		i := 0
		for _, target := range targets {
			if held[target] {
				continue
			}
			todo = append(todo, rekey{oldID: free[i], newID: target})
			i++
		}
	}
	// Deterministic UPDATE order (groups is a map) so runs are reproducible.
	sort.Slice(todo, func(i, j int) bool { return todo[i].oldID < todo[j].oldID })

	for _, r := range todo {
		//nolint:gosec // G201: table name is a hardcoded constant, never user input.
		if _, err := db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET id = ? WHERE id = ?`, t.name),
			r.newID, r.oldID); err != nil {
			return true, fmt.Errorf("re-key id %s -> %s: %w", r.oldID, r.newID, err)
		}
	}
	return len(todo) > 0, nil
}
