package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var repairMigrationIDDefaultInvariants = repairIDDefaultInvariants

const idDefaultInvariantRepairSentinel = "schema_id_default_invariant_repair_in_progress"

var uuidDefaultDDLRE = regexp.MustCompile(`(?i)\s+DEFAULT\s+\(UUID\(\)\)`)

var (
	mainIDDefaultInvariantTables = []string{
		"dependencies",
		"events",
		"comments",
		"issue_snapshots",
		"compaction_snapshots",
	}
	ignoredIDDefaultInvariantTables = []string{
		"wisp_events",
		"wisp_comments",
		"wisp_dependencies",
	}
	// events joined the clone-local ignored plane in migration 0062. The
	// remaining main-series tables are versioned and must be committed when an
	// at-latest repair changes their schema.
	versionedIDDefaultInvariantTables = map[string]struct{}{
		"dependencies":         {},
		"comments":             {},
		"issue_snapshots":      {},
		"compaction_snapshots": {},
	}
)

// migrationIDDefaultTables returns the no-default invariant introduced by one
// migration. Keeping this keyed to the frozen filename lets runMigrations
// enforce the DDL before recording that version without mistaking a different
// migration source that happens to reuse the same numeric version.
func migrationIDDefaultTables(filename string) []string {
	switch filename {
	case "0050_dependencies_deterministic_id.up.sql":
		return mainIDDefaultInvariantTables[:1]
	case "0051_drop_aux_id_defaults.up.sql":
		return mainIDDefaultInvariantTables[1:]
	case "0010_drop_wisp_id_defaults.up.sql":
		return ignoredIDDefaultInvariantTables
	default:
		return nil
	}
}

// idDefaultInvariantNeedsRepair reports whether any current-schema id column
// still has a default that its recorded migration claims to have dropped.
func idDefaultInvariantNeedsRepair(ctx context.Context, db DBConn) (bool, error) {
	resume, err := idDefaultInvariantRepairPending(ctx, db)
	if err != nil {
		return false, err
	}
	if resume {
		return true, nil
	}
	mainNeeds, err := idDefaultsNeedRepair(ctx, db, mainIDDefaultInvariantTables)
	if err != nil {
		return false, err
	}
	if mainNeeds {
		return true, nil
	}
	return idDefaultsNeedRepair(ctx, db, ignoredIDDefaultInvariantTables)
}

func idDefaultsNeedRepair(ctx context.Context, db DBConn, tables []string) (bool, error) {
	targets, err := idDefaultTablesNeedingRepair(ctx, db, tables)
	return len(targets) > 0, err
}

func idDefaultTablesNeedingRepair(ctx context.Context, db DBConn, tables []string) ([]string, error) {
	defaults, err := readIDColumnDefaults(ctx, db, tables)
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, table := range tables {
		columnDefault, ok := defaults[table]
		if !ok {
			return nil, fmt.Errorf("migration invariant: expected current-schema column %s.id is missing", table)
		}
		if columnDefault.Valid {
			targets = append(targets, table)
		}
	}
	return targets, nil
}

// repairIDDefaultInvariants reasserts the canonical no-default DDL and verifies
// the result. It is used both immediately after the migration body (before its
// cursor row is recorded) and on an at-latest database whose cursor contradicts
// its live DDL. The returned table names are the tables actually altered.
func repairIDDefaultInvariants(ctx context.Context, db DBConn, tables []string) ([]string, error) {
	if len(tables) == 0 {
		return nil, nil
	}
	targets, err := idDefaultTablesNeedingRepair(ctx, db, tables)
	if err != nil {
		return nil, err
	}
	for _, table := range targets {
		// table is selected only from the hard-coded invariant lists above.
		if _, err := db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE `%s` ALTER COLUMN id DROP DEFAULT", table)); err != nil {
			return nil, fmt.Errorf("reasserting migration invariant for %s.id: %w", table, err)
		}
	}

	if len(targets) == 0 {
		return nil, nil
	}
	stillNeedsRepair, err := idDefaultsNeedRepair(ctx, db, tables)
	if err != nil {
		return nil, err
	}
	if stillNeedsRepair {
		return nil, fmt.Errorf("migration invariant verification failed: id default remains on one of %s", strings.Join(tables, ", "))
	}
	return targets, nil
}

func idDefaultInvariantRepairPending(ctx context.Context, db DBConn) (bool, error) {
	return auxRekeyResumePending(ctx, db, idDefaultInvariantRepairSentinel)
}

func setIDDefaultInvariantRepairPending(ctx context.Context, db DBConn) error {
	return setAuxRekeyInProgress(ctx, db, idDefaultInvariantRepairSentinel)
}

func clearIDDefaultInvariantRepairPending(ctx context.Context, db DBConn) error {
	return clearAuxRekeyInProgress(ctx, db, idDefaultInvariantRepairSentinel)
}

func commitIDDefaultInvariantRepairs(ctx context.Context, db DBConn, changed []string) error {
	pending, err := pendingVersionedIDDefaultSchemaRepairs(ctx, db)
	if err != nil {
		return err
	}
	tableSet := make(map[string]struct{}, len(changed)+len(pending))
	for _, table := range append(changed, pending...) {
		if _, ok := versionedIDDefaultInvariantTables[table]; ok {
			tableSet[table] = struct{}{}
		}
	}
	versioned := make([]string, 0, len(tableSet))
	for table := range tableSet {
		versioned = append(versioned, table)
	}
	if len(versioned) == 0 {
		return clearIDDefaultInvariantRepairPending(ctx, db)
	}
	sort.Strings(versioned)
	for _, table := range versioned {
		if err := DrainCall(ctx, db, "CALL DOLT_ADD('-f', ?)", table); err != nil {
			return fmt.Errorf("staging migration invariant repair for %s: %w", table, err)
		}
	}
	if err := DrainCall(ctx, db, "CALL DOLT_COMMIT('-m', ?)", "schema: repair recorded migration invariants"); err != nil {
		return fmt.Errorf("committing migration invariant repairs: %w", err)
	}

	dirty, err := dirtyTables(ctx, db, false)
	if err != nil {
		return fmt.Errorf("verifying committed migration invariant repairs: %w", err)
	}
	for _, table := range versioned {
		if _, ok := dirty[table]; ok {
			return fmt.Errorf("migration invariant repair for %s remains uncommitted", table)
		}
	}
	return clearIDDefaultInvariantRepairPending(ctx, db)
}

func pendingVersionedIDDefaultSchemaRepairs(ctx context.Context, db DBConn) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT to_table_name, data_change
FROM dolt_diff_summary('HEAD', 'WORKING')
WHERE schema_change = 1`)
	if err != nil {
		return nil, fmt.Errorf("reading pending migration invariant schema changes: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var table sql.NullString
		var dataChange bool
		if err := rows.Scan(&table, &dataChange); err != nil {
			return nil, fmt.Errorf("scanning pending migration invariant schema change: %w", err)
		}
		if _, ok := versionedIDDefaultInvariantTables[table.String]; !table.Valid || !ok {
			continue
		}
		if dataChange {
			return nil, fmt.Errorf("refusing to commit migration invariant repair for %s: table also has uncommitted row changes", table.String)
		}
		tables = append(tables, table.String)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading pending migration invariant schema changes: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("closing pending migration invariant schema changes: %w", err)
	}
	for _, table := range tables {
		if err := verifyOnlyUUIDDefaultWasDropped(ctx, db, table); err != nil {
			return nil, err
		}
	}
	sort.Strings(tables)
	return tables, nil
}

func verifyOnlyUUIDDefaultWasDropped(ctx context.Context, db DBConn, table string) error {
	rows, err := db.QueryContext(ctx, "SELECT from_create_statement, to_create_statement FROM dolt_schema_diff('HEAD', 'WORKING', ?)", table)
	if err != nil {
		return fmt.Errorf("reading migration invariant schema diff for %s: %w", table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return fmt.Errorf("migration invariant repair for %s has no schema diff", table)
	}
	var fromDDL, toDDL string
	if err := rows.Scan(&fromDDL, &toDDL); err != nil {
		return fmt.Errorf("scanning migration invariant schema diff for %s: %w", table, err)
	}
	if rows.Next() {
		return fmt.Errorf("migration invariant repair for %s has multiple schema diffs", table)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("reading migration invariant schema diff for %s: %w", table, err)
	}
	if uuidDefaultDDLRE.ReplaceAllString(fromDDL, "") != toDDL {
		return fmt.Errorf("refusing to commit migration invariant repair for %s: schema diff contains changes beyond dropping DEFAULT (UUID())", table)
	}
	return nil
}

func readIDColumnDefaults(ctx context.Context, db DBConn, tables []string) (map[string]sql.NullString, error) {
	placeholders := make([]string, len(tables))
	args := make([]any, len(tables))
	for i, table := range tables {
		placeholders[i] = "?"
		args[i] = table
	}
	rows, err := db.QueryContext(ctx, `
SELECT TABLE_NAME, COLUMN_DEFAULT
FROM INFORMATION_SCHEMA.COLUMNS
WHERE TABLE_SCHEMA = DATABASE()
  AND COLUMN_NAME = 'id'
  AND TABLE_NAME IN (`+strings.Join(placeholders, ", ")+")", args...)
	if err != nil {
		return nil, fmt.Errorf("reading migration id-default invariants: %w", err)
	}
	defer rows.Close()

	defaults := make(map[string]sql.NullString, len(tables))
	for rows.Next() {
		var table string
		var columnDefault sql.NullString
		if err := rows.Scan(&table, &columnDefault); err != nil {
			return nil, fmt.Errorf("scanning migration id-default invariant: %w", err)
		}
		defaults[table] = columnDefault
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading migration id-default invariants: %w", err)
	}
	return defaults, nil
}
