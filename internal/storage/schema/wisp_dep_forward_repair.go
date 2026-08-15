package schema

import (
	"context"
	"fmt"
	"strings"
)

// Pre-0058 repair: march wisp_dependencies forward to the final (current 0021)
// shape so migration 0058 can apply.
//
// The failure. 0058 adds fk_wisp_dep_wisp_target and fk_wisp_dep_issue_target
// over depends_on_wisp_id and depends_on_issue_id. On every database 0058 was
// written to heal, those two columns are inputs to depends_on_id's COALESCE
// STORED generated column, and Dolt refuses:
//
//	Error 1105 (HY000): Cannot add foreign key on the base column of a stored
//	generated column.
//
// The two conditions share one cause. A database reaches 0047's delegate
// branch when wisps and wisp_dependencies both already exist; that branch adds
// the split target columns underneath the generated column and adds neither
// foreign key. So every database that needs 0058 has the shape that makes 0058
// fail, and the pass commits 0054..0057 and then aborts with the cursor at 57:
// past what an older binary will open, short of head. Recovery is a restore.
//
// Why this cannot be a migration file. 0058 is shipped and frozen
// (scripts/check-migration-hygiene.sh check C), and a new migration numbered
// above 58 is unreachable because 0058 aborts the pass before it. A repair
// keyed to the pending version is the only vehicle that runs before 0058 on an
// affected database.
//
// Why the destination is the FINAL shape rather than the pre-0058 shape.
// Restoring the composite-PK generated-column shape was tried twice and failed
// both times, for one root cause: that shape's rebuild has a keyless window
// indistinguishable from ignored/0005's own keyless window, so a resume after a
// crash cannot tell which actor it is resuming and can record 58 over a table
// on its way to the id-keyed shape. Aiming at the final shape removes the
// ambiguity, because the repair's destination IS 0005's destination: it does
// not matter which actor resumes, since both converge on the same table. It
// also dissolves the deduplication hazard (see rebuild step 4) and the
// PK-hostage error 1553, and it leaves ignored/0005's @needs_drop reading 0.
//
// Sequence, mirroring the shipped cliMigration0043DropDependenciesGeneratedColumn
// which performs exactly this operation for the `dependencies` table:
//
//  1. delete:    FK orphans and zero-target rows
//  2. drops:     idx_wisp_dep_type_target, each FK present, PRIMARY KEY, depends_on_id
//  3. add:       id CHAR(36) DEFAULT (UUID()) PRIMARY KEY FIRST
//  4. normalize: multi-target rows down to one target
//  5. dedup:     keep MIN(id) per natural-identity group
//  6. add:       the three uk_* unique keys and three idx_wisp_dep_type_* indexes
//  7. add:       all four constraints unconditionally
//
// Normalization sits AFTER the drop, and that placement is load-bearing rather
// than cosmetic. depends_on_id is COALESCE(issue, wisp, external), so nulling a
// higher-precedence column recomputes it -- and the legacy primary key is
// (issue_id, depends_on_id). Normalizing a row that carries both an external
// and a wisp target therefore rewrites its own key onto the value an
// external-only sibling row already holds:
//
//	duplicate primary key given: [w2,external:e1]
//
// The UPDATE aborts and the repair stalls on exactly the drifted store it
// exists to heal. Once the generated column and the composite key are gone the
// same statement is a plain column update that cannot collide, and step 5 then
// removes the duplicate it just created. (0058 carries this defect too: it runs
// the same normalization with the generated column and composite key still in
// place. It is unreachable there only because 0058's normalization is gated on
// ck_wisp_dep_one_target being absent, and a store that reaches that branch
// with a colliding pair hits the same abort.)
//
// Every step below re-verifies its own target state against the live schema
// rather than trusting a flag sniffed once at the top. That is what makes the
// repair resumable: a process killed anywhere in the sequence leaves a state
// with exactly one forward action, and the next open takes it. Both ignored/0003
// and ignored/0005 key all of their per-statement guards on a single upfront
// sniff and therefore no-op after a mid-run crash; a repair that inherited that
// would reintroduce the bug it exists to fix.
//
// Note on FOREIGN_KEY_CHECKS: this repair deliberately does NOT disable it.
// The cleanup in step 1 removes every row a constraint in step 6 could reject,
// so suppression buys nothing on the clean path -- and on a dirty one it is
// actively harmful, because a foreign key added over surviving orphans is the
// #4534 write-brick (Dolt then fails constraint validation on subsequent
// writes, so one legacy orphan bricks every create). With checks left on, a
// missed orphan aborts its own ADD instead, which under the forward-march
// ordering is recoverable: the drops are already done, so the table sits in the
// final shape minus one constraint -- a validly keyed table, not the mangled
// keyless one -- and the next open re-cleans and retries. It is also toggled
// per session, and DBConn is a pool.
const wispDepTable = "wisp_dependencies"

// wispDepFinalConstraints are the four constraints the final 0021 shape
// carries. They are added unconditionally at the end of the rebuild, unlike
// ignored/0005's restore-only-what-was-present-before behavior: their absence
// is the disease being cured, and a legacy store never had the two target
// foreign keys to begin with (their columns did not exist yet), which is
// exactly why 0005 leaves those databases unhealed and 0058 has to exist.
var wispDepFinalConstraints = []struct{ name, definition string }{
	{"fk_wisp_dep_issue", "FOREIGN KEY (issue_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE"},
	{"fk_wisp_dep_wisp_target", "FOREIGN KEY (depends_on_wisp_id) REFERENCES wisps(id) ON DELETE CASCADE ON UPDATE CASCADE"},
	{"fk_wisp_dep_issue_target", "FOREIGN KEY (depends_on_issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE"},
	{"ck_wisp_dep_one_target", "CHECK ((depends_on_issue_id IS NOT NULL) + (depends_on_wisp_id IS NOT NULL) + (depends_on_external IS NOT NULL) = 1)"},
}

// wispDepFinalKeys are the unique keys and secondary indexes of the final
// shape, in the order the shipped 0043 analog adds them.
var wispDepFinalKeys = []struct{ name, definition string }{
	{"uk_wisp_dep_issue_target", "ADD UNIQUE KEY uk_wisp_dep_issue_target (issue_id, depends_on_issue_id)"},
	{"uk_wisp_dep_wisp_target", "ADD UNIQUE KEY uk_wisp_dep_wisp_target (issue_id, depends_on_wisp_id)"},
	{"uk_wisp_dep_external_target", "ADD UNIQUE KEY uk_wisp_dep_external_target (issue_id, depends_on_external)"},
	{"idx_wisp_dep_type_issue", "ADD INDEX idx_wisp_dep_type_issue (type, depends_on_issue_id)"},
	{"idx_wisp_dep_type_wisp", "ADD INDEX idx_wisp_dep_type_wisp (type, depends_on_wisp_id)"},
	{"idx_wisp_dep_type_external", "ADD INDEX idx_wisp_dep_type_external (type, depends_on_external)"},
}

// repairWispDependenciesForwardShape is the pre-0058 repair. It no-ops on every
// population except the one 0058 cannot serve: no-ops when the table or either
// referenced table is absent, when the table is already in the final shape, and
// on a fresh store (which never had the generated column, so 0058 applies to it
// cleanly).
func repairWispDependenciesForwardShape(ctx context.Context, db DBConn) error {
	// Both referenced tables must exist before any foreign key can be added.
	// 0058 carries the same @has_wisps/@has_issues guards; a database missing
	// one is left untouched rather than assumed into a shape it may not have.
	for _, t := range []string{wispDepTable, "wisps", "issues"} {
		exists, err := schemaTableExists(ctx, db, t)
		if err != nil {
			return fmt.Errorf("checking %s for the pre-0058 repair: %w", t, err)
		}
		if !exists {
			return nil
		}
	}

	needed, err := wispDepNeedsForwardRepair(ctx, db)
	if err != nil {
		return err
	}
	if !needed {
		return nil
	}

	if err := deleteWispDepRowsRejectedByFinalShape(ctx, db); err != nil {
		return err
	}
	if err := dropWispDepLegacyShape(ctx, db); err != nil {
		return err
	}
	if err := ensureWispDepSurrogateKey(ctx, db); err != nil {
		return err
	}
	// Only now, with the generated column and composite key gone, can a
	// multi-target row be normalized without rewriting its own primary key
	// onto a sibling's.
	if err := normalizeWispDepMultiTargetRows(ctx, db); err != nil {
		return err
	}
	if err := dedupeWispDepNaturalIdentity(ctx, db); err != nil {
		return err
	}
	return ensureWispDepFinalKeysAndConstraints(ctx, db)
}

// wispDepNeedsForwardRepair reports whether this database is in the legacy
// lineage the repair serves.
//
// Two states qualify. The first is the untouched legacy shape: depends_on_id
// present as a STORED generated column. The second is a crash state -- neither
// depends_on_id nor id, which no lineage produces on purpose and which only a
// process killed between this repair's DROP COLUMN and its ADD COLUMN can
// reach. Detecting the second is what makes the repair resumable rather than
// leaving a keyless table behind for 0058 to complete over.
//
// A fresh store has id and no generated column and is not repaired: 0058
// applies to it cleanly, and the constraints are already there from creation.
func wispDepNeedsForwardRepair(ctx context.Context, db DBConn) (bool, error) {
	generated, err := wispDepHasStoredGeneratedDependsOnID(ctx, db)
	if err != nil {
		return false, err
	}
	if generated {
		return true, nil
	}

	hasID, err := schemaColumnExists(ctx, db, wispDepTable, "id")
	if err != nil {
		return false, fmt.Errorf("checking %s.id for the pre-0058 repair: %w", wispDepTable, err)
	}
	if hasID {
		// Final lineage. Still finish the job if a prior pass was killed
		// between adding the surrogate key and adding every constraint: the
		// steps below are individually guarded, so this is a no-op once the
		// shape is complete.
		return wispDepMissingAnyFinalConstraint(ctx, db)
	}

	// Neither depends_on_id nor id: a mid-rebuild crash. Resume.
	return true, nil
}

func wispDepMissingAnyFinalConstraint(ctx context.Context, db DBConn) (bool, error) {
	for _, c := range wispDepFinalConstraints {
		present, err := schemaConstraintExists(ctx, db, wispDepTable, c.name)
		if err != nil {
			return false, err
		}
		if !present {
			return true, nil
		}
	}
	return false, nil
}

// wispDepHasStoredGeneratedDependsOnID reports whether depends_on_id is a
// STORED generated column.
//
// This reads SHOW CREATE TABLE because Dolt does not surface generated-column
// metadata through INFORMATION_SCHEMA at all: COLUMNS.EXTRA and
// COLUMNS.GENERATION_EXPRESSION both come back empty for a generated column,
// and IS_GENERATED does not exist as a column ("could not be found in any table
// in scope"). A guard written against those compiles, runs, and never fires.
//
// The backtick-delimited name is what makes the match safe: "`depends_on_id`"
// cannot match the `depends_on_issue_id` definition line, whereas an unquoted
// substring search would.
func wispDepHasStoredGeneratedDependsOnID(ctx context.Context, db DBConn) (bool, error) {
	ddl, err := schemaShowCreateTable(ctx, db, wispDepTable)
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(ddl, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "`depends_on_id`") {
			continue
		}
		upper := strings.ToUpper(trimmed)
		return strings.Contains(upper, "GENERATED ALWAYS AS") && strings.Contains(upper, "STORED"), nil
	}
	return false, nil
}

// deleteWispDepRowsRejectedByFinalShape removes exactly the rows the constraints
// added at the end of the rebuild would otherwise reject. It ports 0058's own
// cleanup, which cites the #4534 class verbatim: the delegate path's
// unconstrained window leaves target columns pointing at rows that no longer
// exist, and a foreign key added over them bricks every subsequent write.
//
// Three orphan deletes, not 0058's two. 0058 adds only the two target foreign
// keys, but this repair also (re-)adds fk_wisp_dep_issue over issue_id, which a
// legacy store may equally have accumulated orphans against while it was
// dropped. Deleting the whole row is what ON DELETE CASCADE would have done had
// the constraint been in force when the target went away, so it matches the
// state the table would be in had the window never existed.
//
// A zero-target row names nothing to be blocked on and is deleted outright, as
// 0058 does. Deletes are safe here, before the drop, because removing a row can
// never collide on the legacy key -- unlike the multi-target normalization,
// which must wait (see normalizeWispDepMultiTargetRows).
func deleteWispDepRowsRejectedByFinalShape(ctx context.Context, db DBConn) error {
	statements := []string{
		// FK orphans.
		"DELETE wd FROM wisp_dependencies wd LEFT JOIN wisps w ON w.id = wd.issue_id WHERE w.id IS NULL",
		"DELETE wd FROM wisp_dependencies wd LEFT JOIN wisps w ON w.id = wd.depends_on_wisp_id WHERE wd.depends_on_wisp_id IS NOT NULL AND w.id IS NULL",
		"DELETE wd FROM wisp_dependencies wd LEFT JOIN issues i ON i.id = wd.depends_on_issue_id WHERE wd.depends_on_issue_id IS NOT NULL AND i.id IS NULL",
		// ck_wisp_dep_one_target: zero-target rows.
		"DELETE FROM wisp_dependencies WHERE depends_on_issue_id IS NULL AND depends_on_wisp_id IS NULL AND depends_on_external IS NULL",
	}
	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("deleting wisp_dependencies rows rejected by the final shape: %w", err)
		}
	}
	return nil
}

// normalizeWispDepMultiTargetRows reduces a row naming more than one target to
// exactly one, so ck_wisp_dep_one_target can be added at the end.
//
// The precedence (external > wisp > issue) is 0058's and is not a choice: it is
// fixed by the delegate backfill's statement order in
// wispDependenciesSplitTargetBackfillSQL and pinned by
// TestWispDependenciesSplitTargetBackfillPrefersWispOverIssueThroughDoltCLI.
// Matching it keeps (repair -> 0058) equivalent to (0058 alone) on every
// population, which is the invariant that makes the repair auditable. Unlike a
// zero-target row, a multi-target row names real, resolvable targets -- just
// more than one -- so the lower-precedence columns are nulled and the row
// survives rather than being discarded.
//
// This must run after the generated column and composite primary key are gone;
// see the ordering note in the file header for the collision it otherwise
// causes. It also manufactures duplicates by design -- a normalized row can
// land on a sibling's natural identity -- which is why the dedup step follows
// it rather than preceding it.
func normalizeWispDepMultiTargetRows(ctx context.Context, db DBConn) error {
	statements := []string{
		"UPDATE wisp_dependencies SET depends_on_wisp_id = NULL, depends_on_issue_id = NULL WHERE depends_on_external IS NOT NULL AND (depends_on_wisp_id IS NOT NULL OR depends_on_issue_id IS NOT NULL)",
		"UPDATE wisp_dependencies SET depends_on_issue_id = NULL WHERE depends_on_external IS NULL AND depends_on_wisp_id IS NOT NULL AND depends_on_issue_id IS NOT NULL",
	}
	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("normalizing multi-target wisp_dependencies rows for the 0058 repair: %w", err)
		}
	}
	return nil
}

// dropWispDepLegacyShape removes the generated column and everything built on
// it, in the order the shipped 0043 analog uses.
//
// The order is load-bearing in two places. idx_wisp_dep_type_target is indexed
// on depends_on_id and must go before the column. Every foreign key must go
// before DROP PRIMARY KEY, because the primary key is the only issue_id-leading
// index on this shape and fk_wisp_dep_issue holds it hostage:
//
//	Error 1553 (HY000): can't drop index 'PRIMARY': needed in foreign key
//	constraint fk_wisp_dep_issue
//
// Each drop is guarded on the live schema, so a resume after a crash mid-drop
// skips what is already gone rather than failing on a missing object.
func dropWispDepLegacyShape(ctx context.Context, db DBConn) error {
	hasIndex, err := schemaIndexExists(ctx, db, wispDepTable, "idx_wisp_dep_type_target")
	if err != nil {
		return err
	}
	if hasIndex {
		if _, err := db.ExecContext(ctx, "ALTER TABLE wisp_dependencies DROP INDEX idx_wisp_dep_type_target"); err != nil {
			return fmt.Errorf("dropping idx_wisp_dep_type_target for the 0058 repair: %w", err)
		}
	}

	for _, c := range wispDepFinalConstraints {
		if !strings.HasPrefix(c.name, "fk_") {
			continue
		}
		present, err := schemaConstraintExists(ctx, db, wispDepTable, c.name)
		if err != nil {
			return err
		}
		if !present {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE wisp_dependencies DROP FOREIGN KEY "+c.name); err != nil {
			return fmt.Errorf("dropping %s for the 0058 repair: %w", c.name, err)
		}
	}

	hasPK, err := schemaHasPrimaryKey(ctx, db, wispDepTable)
	if err != nil {
		return err
	}
	if hasPK {
		if _, err := db.ExecContext(ctx, "ALTER TABLE wisp_dependencies DROP PRIMARY KEY"); err != nil {
			return fmt.Errorf("dropping the wisp_dependencies primary key for the 0058 repair: %w", err)
		}
	}

	hasGenerated, err := schemaColumnExists(ctx, db, wispDepTable, "depends_on_id")
	if err != nil {
		return err
	}
	if hasGenerated {
		if _, err := db.ExecContext(ctx, "ALTER TABLE wisp_dependencies DROP COLUMN depends_on_id"); err != nil {
			return fmt.Errorf("dropping wisp_dependencies.depends_on_id for the 0058 repair: %w", err)
		}
	}
	return nil
}

// ensureWispDepSurrogateKey adds the final shape's id column and primary key.
//
// It is added BEFORE deduplication on purpose. The natural identity of a row
// here is (issue_id, target), and two rows identical in every column have no
// deterministic survivor -- created_at has second resolution and created_by and
// type commonly take defaults, so ordinary retry inserts reach that state. Once
// every row carries a distinct UUID a delete can pick MIN(id) deterministically.
// The final primary key being the UUID also means ADD PRIMARY KEY can never
// fail on duplicates, which is the state that made the previous designs fatal.
//
// A legacy store categorically has no id column: the original 0021 created the
// composite-keyed shape without one, and ignored/0005 -- the only migration that
// adds id to a legacy store -- drops the generated column in the same guarded
// run. There is no state carrying both, so this never has to reconcile one.
//
// DEFAULT (UUID()) is correct here even though a fully migrated table carries
// no default: ignored/0010 drops it, and this repair can only reach the ADD
// COLUMN branch on a store whose ignored cursor is still behind 0005 (that is
// what "the generated column is still present" means), hence behind 0010. The
// default is therefore always dropped downstream in the same pass, which is why
// ignored/0005 mints the column the same way rather than special-casing it.
func ensureWispDepSurrogateKey(ctx context.Context, db DBConn) error {
	hasID, err := schemaColumnExists(ctx, db, wispDepTable, "id")
	if err != nil {
		return err
	}
	if !hasID {
		if _, err := db.ExecContext(ctx,
			"ALTER TABLE wisp_dependencies ADD COLUMN id CHAR(36) NOT NULL DEFAULT (UUID()) PRIMARY KEY FIRST"); err != nil {
			return fmt.Errorf("adding wisp_dependencies.id for the 0058 repair: %w", err)
		}
		return nil
	}

	// id survived a crash but its key did not (or the column predates the
	// key): add the key alone rather than re-adding the column.
	hasPK, err := schemaHasPrimaryKey(ctx, db, wispDepTable)
	if err != nil {
		return err
	}
	if !hasPK {
		if _, err := db.ExecContext(ctx, "ALTER TABLE wisp_dependencies ADD PRIMARY KEY (id)"); err != nil {
			return fmt.Errorf("adding the wisp_dependencies id primary key for the 0058 repair: %w", err)
		}
	}
	return nil
}

// dedupeWispDepNaturalIdentity removes rows that would collide on the three
// uk_* unique keys added next. Duplicates are reachable because the legacy
// shape's composite primary key covers (issue_id, depends_on_id) -- so two rows
// differing only in a column the COALESCE did not select were legal -- and
// because the keyless window leaves writes unconstrained.
//
// The comparison is null-safe (<=>): the target columns are NULL for every
// target kind a row does not use, and ordinary = would treat two identical
// wisp-target rows as distinct because their NULL issue targets never compare
// equal. MIN(id) is an arbitrary but deterministic survivor, which is the
// property that matters -- it makes a resumed run pick the same row.
func dedupeWispDepNaturalIdentity(ctx context.Context, db DBConn) error {
	if _, err := db.ExecContext(ctx, `
		DELETE wd FROM wisp_dependencies wd
		JOIN (
			SELECT MIN(id) AS keep_id, issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external
			FROM wisp_dependencies
			GROUP BY issue_id, depends_on_issue_id, depends_on_wisp_id, depends_on_external
			HAVING COUNT(*) > 1
		) dup
		  ON wd.issue_id = dup.issue_id
		 AND wd.depends_on_issue_id <=> dup.depends_on_issue_id
		 AND wd.depends_on_wisp_id <=> dup.depends_on_wisp_id
		 AND wd.depends_on_external <=> dup.depends_on_external
		WHERE wd.id <> dup.keep_id
	`); err != nil {
		return fmt.Errorf("deduplicating wisp_dependencies before the 0058 repair: %w", err)
	}
	return nil
}

// ensureWispDepFinalKeysAndConstraints completes the final shape. Each object
// is added only if absent, so this finishes a partially-rebuilt table rather
// than failing on a duplicate key name -- and re-running it on a converged
// database does nothing at all.
func ensureWispDepFinalKeysAndConstraints(ctx context.Context, db DBConn) error {
	for _, k := range wispDepFinalKeys {
		present, err := schemaIndexExists(ctx, db, wispDepTable, k.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE wisp_dependencies "+k.definition); err != nil {
			return fmt.Errorf("adding %s for the 0058 repair: %w", k.name, err)
		}
	}

	for _, c := range wispDepFinalConstraints {
		present, err := schemaConstraintExists(ctx, db, wispDepTable, c.name)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err := db.ExecContext(ctx,
			"ALTER TABLE wisp_dependencies ADD CONSTRAINT "+c.name+" "+c.definition); err != nil {
			return fmt.Errorf("adding %s for the 0058 repair: %w", c.name, err)
		}
	}
	return nil
}
