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

// auxRowRekeyMarkerVersion is the ignored (clone-local) migration that records
// completion of the one-time aux-row-id re-key. The backfill runs only while
// this version is still pending; ignoredSource.migrate then records it later
// in the same MigrateUp pass. The cursor table is dolt-ignored, so every clone
// performs its own convergence pass exactly once.
const auxRowRekeyMarkerVersion = 9

// auxRowRekeyShippedMainVersion is the main-source schema version that shipped
// in the same release as the re-key pass. A database whose main cursor already
// reached it BEFORE the current MigrateUp pass was migrated by a rekey-aware
// binary, so its lineage has already converged — this open is a fresh clone
// (or a post-pull adoption) whose dolt-ignored marker simply does not exist
// locally yet. The pass is skipped there: rewriting would churn post-backfill
// app-minted ids into a mass PK-rewrite commit from every new clone
// (bd-578h9.4). The marker is still recorded.
const auxRowRekeyShippedMainVersion = 51

// auxRowRekeyInProgressKey is the clone-local sentinel (local_metadata,
// dolt-ignored) set before the re-key's first UPDATE and cleared after the
// last table completes. A crashed pass leaves it set, and the next MigrateUp
// resumes the re-key even though the main cursor already advanced past
// auxRowRekeyShippedMainVersion in the crashed pass — without it the
// fresh-clone skip above reads the crashed lineage as already converged and
// strands the partially re-keyed rows forever (bd-578h9.16).
//
// It means "a rewrite is in flight", nothing else. MigrateUp reads it to exempt
// the aux tables from the pre-existing-dirty guards, because a crashed pass's
// own partial UPDATEs are sitting in the working set — so it must not outlive
// an actual in-flight rewrite. A drift skip (#4380) therefore clears it like
// any completed pass and records auxRowRekeyDriftedKey instead.
const auxRowRekeyInProgressKey = "aux_row_rekey_in_progress"

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

// readAuxRekeyState reads both records in one round trip. A missing
// local_metadata table means neither is set: a fresh clone lacks the table
// until something recreates it — setAuxRekeyInProgress creates it on demand.
func readAuxRekeyState(ctx context.Context, db DBConn) (auxRekeyState, error) {
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
	rows, err := db.QueryContext(ctx,
		"SELECT `key`, value FROM local_metadata WHERE `key` IN (?, ?)",
		auxRowRekeyInProgressKey, auxRowRekeyDriftedKey)
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
			return auxRekeyState{}, err
		}
		switch key {
		case auxRowRekeyInProgressKey:
			state.resume = true
		case auxRowRekeyDriftedKey:
			for _, name := range strings.Split(value, ",") {
				if name = strings.TrimSpace(name); name != "" {
					state.drifted = append(state.drifted, name)
				}
			}
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

func setAuxRekeyInProgress(ctx context.Context, db DBConn) error {
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
		auxRowRekeyInProgressKey)
	return err
}

func clearAuxRekeyInProgress(ctx context.Context, db DBConn) error {
	_, err := db.ExecContext(ctx,
		"DELETE FROM local_metadata WHERE `key` = ?",
		auxRowRekeyInProgressKey)
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
// primary keys across clones. The wisp_ twins (wisp_events, wisp_comments) are
// deliberately excluded: they are dolt-ignored, never merged, and promotion
// copies their rows without ids, so their random keys never escape the clone.
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
func rekeyAuxRowIDs(ctx context.Context, db DBConn, mainVersionBefore int) (bool, error) {
	pending, err := ignoredSource.pendingVersions(ctx, db)
	if err != nil {
		return false, fmt.Errorf("reading pending ignored migrations: %w", err)
	}
	markerPending := false
	for _, v := range pending {
		if v == auxRowRekeyMarkerVersion {
			markerPending = true
			break
		}
	}
	state, err := readAuxRekeyState(ctx, db)
	if err != nil {
		return false, fmt.Errorf("reading aux rekey state: %w", err)
	}
	// The marker gate is "once per clone" only for a pass that actually
	// converged every table. A pass that skipped one for #11131 drift completed
	// — so the marker was recorded in that same MigrateUp — and the marker
	// alone would then bar re-entry forever, making the skip permanent. The
	// drift record re-admits the pass so it can finish once the storage is
	// repaired.
	if !markerPending && !state.pending() {
		return false, nil
	}
	// The fresh-clone skip must not fire on a lineage whose previous pass
	// crashed mid-rekey: that pass already advanced the main cursor past the
	// shipped version, but its sentinel proves the rewrite never finished
	// (bd-578h9.16). A recorded drift skip says the same thing.
	if mainVersionBefore >= auxRowRekeyShippedMainVersion && !state.pending() {
		return false, nil
	}

	// Once the marker is recorded, the one-time pass has completed: MigrateUp
	// records it (ignoredSource.migrate) only after this function returns
	// without error, so a clone that still owes work past that point owes it for
	// exactly the tables the drift record names. Re-keying any other table there
	// would be wrong — those converged in the completed pass, and rows minted
	// since carry app-minted ids that are already consistent across clones (see
	// above), so rewriting them on this clone alone manufactures the divergence
	// the re-key exists to remove. Note this covers a drift-resume pass that
	// itself died on some unrelated error and left the sentinel set: it is still
	// a post-marker pass, so it still touches only the recorded tables.
	tables := auxRekeyTables
	if !markerPending {
		tables = nil
		for _, t := range auxRekeyTables {
			if slices.Contains(state.drifted, t.name) {
				tables = append(tables, t)
			}
		}
	}

	// Sentinel before the first UPDATE: a crash anywhere in the rewrite
	// leaves it set, so the next pass resumes (the rewrite is idempotent)
	// instead of recording the marker over partially re-keyed rows.
	if err := setAuxRekeyInProgress(ctx, db); err != nil {
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
	if err := clearAuxRekeyInProgress(ctx, db); err != nil {
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
