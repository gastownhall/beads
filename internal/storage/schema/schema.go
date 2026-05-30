package schema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/storage/dberrors"
)

type DBConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SchemaSkewError is returned when the DB schema version is ahead of the
// binary's known version (forward drift). Stale binary queries may fail with
// cryptic SQL errors like "column X could not be found in any table in scope".
type SchemaSkewError struct {
	DBVersion     int
	BinaryVersion int
}

func (e *SchemaSkewError) Error() string {
	delta := e.DBVersion - e.BinaryVersion
	unit := "migrations"
	if delta == 1 {
		unit = "migration"
	}
	return fmt.Sprintf("schema version mismatch: database is at v%d, binary knows up to v%d (%d %s ahead)",
		e.DBVersion, e.BinaryVersion, delta, unit)
}

// UserMessage returns the full multi-line error block for terminal output.
func (e *SchemaSkewError) UserMessage() string {
	return e.Error() + "\n" +
		"\n" +
		"  Your bd binary is stale. Queries for dropped or renamed columns will fail\n" +
		"  with cryptic SQL errors (e.g. \"column X could not be found in any table in scope\").\n" +
		"\n" +
		"  Rebuild from main:\n" +
		"    CGO_ENABLED=0 go build -tags gms_pure_go ./cmd/bd\n" +
		"\n" +
		"  Or install the latest release:\n" +
		"    CGO_ENABLED=0 go install -tags gms_pure_go github.com/steveyegge/beads/cmd/bd@latest\n" +
		"\n" +
		"  To proceed despite the risk (some read commands may still work):\n" +
		"    BD_IGNORE_SCHEMA_SKEW=1 bd <command>\n" +
		"    bd --ignore-schema-skew <command>\n"
}

// EscapeHint returns the escape-hatch string for JSON error output.
func (e *SchemaSkewError) EscapeHint() string {
	return "BD_IGNORE_SCHEMA_SKEW=1 bd <command>  or  bd --ignore-schema-skew <command>"
}

// IsSchemaSkewError reports whether err (or any error it wraps) is a
// *SchemaSkewError.
func IsSchemaSkewError(err error) bool {
	var e *SchemaSkewError
	return errors.As(err, &e)
}

// checkSchemaSkew queries the DB's current schema version and returns a
// *SchemaSkewError if the DB is ahead of the binary. Returns nil for a fresh
// DB (version=0) or when BD_IGNORE_SCHEMA_SKEW=1 (prints a warning instead).
func checkSchemaSkew(ctx context.Context, db DBConn) error {
	var currentVersion int
	if err := db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations",
	).Scan(&currentVersion); err != nil {
		return fmt.Errorf("schema skew check: %w", err)
	}
	if currentVersion == 0 || currentVersion <= LatestVersion() {
		return nil
	}
	if os.Getenv("BD_IGNORE_SCHEMA_SKEW") == "1" {
		fmt.Fprintf(os.Stderr,
			"Warning: schema skew ignored — database (v%d) is ahead of binary (v%d); some queries may fail\n",
			currentVersion, LatestVersion())
		return nil
	}
	return &SchemaSkewError{DBVersion: currentVersion, BinaryVersion: LatestVersion()}
}

// CheckForwardDrift checks for forward schema drift on an existing *sql.DB
// connection. Used by the read-only store path where MigrateUp is skipped.
func CheckForwardDrift(ctx context.Context, db *sql.DB) error {
	return checkSchemaSkew(ctx, db)
}

type dirtyTableState struct {
	staged bool
}

var doltStatusTableNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

//go:embed migrations/*.up.sql
var upMigrations embed.FS

//go:embed migrations/ignored/*.up.sql
var upIgnoredMigrations embed.FS

type migrationSource struct {
	files       embed.FS
	dir         string
	cursorTable string
}

var (
	mainSource = migrationSource{
		files:       upMigrations,
		dir:         "migrations",
		cursorTable: "schema_migrations",
	}
	ignoredSource = migrationSource{
		files:       upIgnoredMigrations,
		dir:         "migrations/ignored",
		cursorTable: "ignored_schema_migrations",
	}
)

var (
	latestOnce        sync.Once
	latestVer         int
	latestIgnoredOnce sync.Once
	latestIgnoredVer  int
)

func LatestVersion() int {
	latestOnce.Do(func() {
		latestVer = mainSource.latest()
	})
	return latestVer
}

func LatestIgnoredVersion() int {
	latestIgnoredOnce.Do(func() {
		latestIgnoredVer = ignoredSource.latest()
	})
	return latestIgnoredVer
}

func CurrentVersion(ctx context.Context, db DBConn) (int, error) {
	return mainSource.currentVersion(ctx, db)
}

func CurrentIgnoredVersion(ctx context.Context, db DBConn) (int, error) {
	return ignoredSource.currentVersion(ctx, db)
}

func PendingVersions(ctx context.Context, db DBConn) ([]int, error) {
	return mainSource.pendingVersions(ctx, db)
}

func PendingIgnoredVersions(ctx context.Context, db DBConn) ([]int, error) {
	return ignoredSource.pendingVersions(ctx, db)
}

func AllMigrationsSQL() string {
	var b strings.Builder
	b.WriteString(mainSource.bootstrapSQL())
	b.WriteString(";\n")
	for _, f := range mainSource.list() {
		data, err := mainSource.files.ReadFile(mainSource.dir + "/" + f.name)
		if err != nil {
			continue
		}
		b.WriteString(cliCompatibleMigrationSQL(f.name, string(data)))
		fmt.Fprintf(&b, "\nINSERT IGNORE INTO %s (version) VALUES (%d);\n", mainSource.cursorTable, f.version)
	}
	return b.String()
}

func parseVersion(name string) (int, error) {
	parts := strings.SplitN(name, "_", 2)
	if len(parts) == 0 {
		return 0, fmt.Errorf("no version prefix")
	}
	return strconv.Atoi(parts[0])
}

func MigrateUp(ctx context.Context, db DBConn) (int, error) {
	needed, err := migrationWorkNeeded(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("checking schema migration work: %w", err)
	}
	if !needed {
		return 0, nil
	}

	dirtyBeforeAll, err := dirtyTables(ctx, db, false)
	if err != nil {
		return 0, fmt.Errorf("reading pre-migration status: %w", err)
	}
	if err := unstagePreExistingTables(ctx, db, dirtyBeforeAll); err != nil {
		return 0, fmt.Errorf("unstaging pre-migration tables: %w", err)
	}
	dirtyBefore, err := committableDirtyTables(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("reading pre-migration status: %w", err)
	}
	touchedDirtyTables, err := mainSource.pendingMigrationDirtyTables(ctx, db, dirtyBefore)
	if err != nil {
		return 0, fmt.Errorf("checking dirty tables against pending migrations: %w", err)
	}
	if len(touchedDirtyTables) > 0 {
		return 0, fmt.Errorf("pending schema migrations alter pre-existing dirty tables: %s", strings.Join(touchedDirtyTables, ", "))
	}
	dirtyBeforeSignatures, err := dirtyTableSignatures(ctx, db, dirtyBefore)
	if err != nil {
		return 0, fmt.Errorf("reading pre-migration dirty table diffs: %w", err)
	}
	applied, err := mainSource.migrate(ctx, db)
	if err != nil {
		return applied, err
	}

	backfilled, err := ensureBackfilledCustomStatusesCustomTypes(ctx, db)
	if err != nil {
		return applied, fmt.Errorf("backfill custom tables: %w", err)
	}

	// #4259: rewrite any per-clone-random dependency ids (minted by 0043's
	// DEFAULT (UUID()) before this fix) to the deterministic key, so independently
	// migrated clones converge to byte-identical, merge-safe dependencies. Runs
	// here, after the schema migrations (0050 has asserted the canonical schema),
	// and only on a pass where migration work was needed.
	rekeyed, err := rekeyDependencyIDs(ctx, db)
	if err != nil {
		return applied, fmt.Errorf("rekey dependency ids: %w", err)
	}
	backfilled = backfilled || rekeyed

	if _, err := db.ExecContext(ctx, "REPLACE INTO dolt_ignore VALUES ('ignored_schema_migrations', true)"); err != nil {
		return applied, fmt.Errorf("registering ignored_schema_migrations in dolt_ignore: %w", err)
	}

	touchedIgnoredDirtyTables, err := ignoredSource.pendingMigrationDirtyTables(ctx, db, dirtyBeforeAll)
	if err != nil {
		return applied, fmt.Errorf("checking dirty tables against pending ignored migrations: %w", err)
	}
	if len(touchedIgnoredDirtyTables) > 0 {
		return applied, fmt.Errorf("pending ignored schema migrations alter pre-existing dirty tables: %s", strings.Join(touchedIgnoredDirtyTables, ", "))
	}

	appliedIgnored, err := ignoredSource.migrate(ctx, db)
	if err != nil {
		return applied, fmt.Errorf("ignored migrations: %w", err)
	}
	if err := unstageIgnoredTables(ctx, db); err != nil {
		return applied, fmt.Errorf("unstaging ignored migration tables: %w", err)
	}

	if applied == 0 && !backfilled && appliedIgnored == 0 {
		return applied, nil
	}
	changedDirtyTables, err := changedDirtyTableSignatures(ctx, db, dirtyBeforeSignatures)
	if err != nil {
		return applied, fmt.Errorf("checking pre-existing dirty table diffs: %w", err)
	}
	if len(changedDirtyTables) > 0 {
		return applied, fmt.Errorf("pre-existing dirty tables changed during schema migration: %s", strings.Join(changedDirtyTables, ", "))
	}

	staged, err := stageSchemaTables(ctx, db, dirtyBefore)
	if err != nil {
		return applied, fmt.Errorf("staging migrations: %w", err)
	}
	if !staged {
		return applied, nil
	}
	if _, err := db.ExecContext(ctx, "CALL DOLT_COMMIT('-m', 'schema: apply migrations')"); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "nothing to commit") {
			return applied, fmt.Errorf("committing migrations: %w", err)
		}
	}

	return applied, nil
}

func migrationWorkNeeded(ctx context.Context, db DBConn) (bool, error) {
	if !mainSource.atLatest(ctx, db) || !ignoredSource.atLatest(ctx, db) {
		return true, nil
	}
	return needsBackfilledCustomStatusesCustomTypes(ctx, db)
}

func committableDirtyTables(ctx context.Context, db DBConn) (map[string]dirtyTableState, error) {
	tables, err := dirtyTables(ctx, db, true)
	if err != nil {
		return nil, err
	}
	delete(tables, mainSource.cursorTable)
	delete(tables, ignoredSource.cursorTable)
	return tables, nil
}

func stagedDirtyTables(tables map[string]dirtyTableState) []string {
	var staged []string
	for table, state := range tables {
		if state.staged {
			staged = append(staged, table)
		}
	}
	sort.Strings(staged)
	return staged
}

func unstagePreExistingTables(ctx context.Context, db DBConn, tables map[string]dirtyTableState) error {
	staged := stagedDirtyTables(tables)
	if len(staged) > 0 {
		log.Printf("schema migration unstaging pre-existing staged tables: %s", strings.Join(staged, ", "))
	}
	for _, table := range staged {
		if _, err := db.ExecContext(ctx, "CALL DOLT_RESET(?)", table); err != nil {
			return fmt.Errorf("dolt reset %s: %w", table, err)
		}
	}
	return nil
}

func unstageIgnoredTables(ctx context.Context, db DBConn) error {
	tables, err := existingIgnoredTables(ctx, db)
	if err != nil {
		return err
	}
	return unstagePreExistingTables(ctx, db, tables)
}

func dirtyTableSignatures(ctx context.Context, db DBConn, tables map[string]dirtyTableState) (map[string]string, error) {
	signatures := make(map[string]string, len(tables))
	names := sortedDirtyTableNames(tables)
	for _, table := range names {
		signature, err := dirtyTableSignature(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		signatures[table] = signature
	}
	return signatures, nil
}

func changedDirtyTableSignatures(ctx context.Context, db DBConn, before map[string]string) ([]string, error) {
	var changed []string
	names := sortedSignatureTableNames(before)
	for _, table := range names {
		signature, err := dirtyTableSignature(ctx, db, table)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", table, err)
		}
		if signature != before[table] {
			changed = append(changed, table)
		}
	}
	return changed, nil
}

func sortedDirtyTableNames(tables map[string]dirtyTableState) []string {
	names := make([]string, 0, len(tables))
	for table := range tables {
		names = append(names, table)
	}
	sort.Strings(names)
	return names
}

func sortedSignatureTableNames(signatures map[string]string) []string {
	names := make([]string, 0, len(signatures))
	for table := range signatures {
		names = append(names, table)
	}
	sort.Strings(names)
	return names
}

func dirtyTableSignature(ctx context.Context, db DBConn, table string) (string, error) {
	if !doltStatusTableNameRE.MatchString(table) {
		return "", fmt.Errorf("unsafe dolt status table name %q", table)
	}
	//nolint:gosec // table comes from dolt_status; dolt_diff requires a literal table argument.
	rows, err := db.QueryContext(ctx, "SELECT * FROM dolt_diff('HEAD', 'WORKING', "+sqlStringLiteral(table)+")")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return "", err
	}

	var rowSignatures []string
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return "", err
		}

		var b strings.Builder
		for i, column := range columns {
			if isDiffMetadataColumn(column) {
				continue
			}
			b.WriteString(column)
			b.WriteByte('=')
			writeSignatureValue(&b, values[i])
			b.WriteByte(0)
		}
		rowSignatures = append(rowSignatures, b.String())
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	sort.Strings(rowSignatures)

	h := sha256.New()
	for _, row := range rowSignatures {
		_, _ = h.Write([]byte(row))
		_, _ = h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func isDiffMetadataColumn(column string) bool {
	switch strings.ToLower(column) {
	case "from_commit", "to_commit", "from_commit_date", "to_commit_date":
		return true
	default:
		return false
	}
}

func sqlStringLiteral(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'"
}

func writeSignatureValue(b *strings.Builder, v any) {
	switch typed := v.(type) {
	case nil:
		b.WriteString("<nil>")
	case []byte:
		b.Write(typed)
	default:
		b.WriteString(fmt.Sprintf("%v", typed))
	}
}

func stageSchemaTables(ctx context.Context, db DBConn, dirtyBefore map[string]dirtyTableState) (bool, error) {
	dirtyAfter, err := dirtyTables(ctx, db, true)
	if err != nil {
		return false, err
	}

	tableSet := make(map[string]struct{})
	for table := range dirtyAfter {
		if _, wasDirty := dirtyBefore[table]; wasDirty {
			continue
		}
		tableSet[table] = struct{}{}
	}
	tablesAfter, err := existingCommittableTables(ctx, db)
	if err != nil {
		return false, err
	}
	for table := range tablesAfter {
		if _, wasDirty := dirtyBefore[table]; wasDirty {
			continue
		}
		tableSet[table] = struct{}{}
	}

	tables := make([]string, 0, len(tableSet))
	for table := range tableSet {
		tables = append(tables, table)
	}
	sort.Strings(tables)

	for _, table := range tables {
		if _, err := db.ExecContext(ctx, "CALL DOLT_ADD('-f', ?)", table); err != nil {
			return false, fmt.Errorf("dolt add %s: %w", table, err)
		}
	}
	return len(tables) > 0, nil
}

func existingCommittableTables(ctx context.Context, db DBConn) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT t.TABLE_NAME
		FROM INFORMATION_SCHEMA.TABLES t
		WHERE t.TABLE_SCHEMA = DATABASE()
		  AND t.TABLE_TYPE = 'BASE TABLE'
		  AND NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			  AND t.TABLE_NAME LIKE di.pattern
		  )
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]struct{})
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables[table] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func existingIgnoredTables(ctx context.Context, db DBConn) (map[string]dirtyTableState, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.table_name, s.staged
		FROM dolt_status s
		WHERE EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			  AND s.table_name LIKE di.pattern
		)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]dirtyTableState)
	for rows.Next() {
		var table string
		var staged bool
		if err := rows.Scan(&table, &staged); err != nil {
			return nil, err
		}
		tables[table] = dirtyTableState{staged: staged}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

func dirtyTables(ctx context.Context, db DBConn, excludeIgnored bool) (map[string]dirtyTableState, error) {
	query := `
		SELECT s.table_name, s.staged
		FROM dolt_status s
	`
	if excludeIgnored {
		query += `
		WHERE NOT EXISTS (
			SELECT 1 FROM dolt_ignore di
			WHERE di.ignored = 1
			AND s.table_name LIKE di.pattern
		)
		`
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]dirtyTableState)
	for rows.Next() {
		var table string
		var staged bool
		if err := rows.Scan(&table, &staged); err != nil {
			return nil, err
		}
		state := tables[table]
		state.staged = state.staged || staged
		tables[table] = state
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

type migrationFile struct {
	version int
	name    string
}

func (m migrationSource) bootstrapSQL() string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	version INT PRIMARY KEY,
	applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)`, m.cursorTable)
}

func checkNoDuplicateVersions(files []migrationFile) {
	seen := make(map[int]string, len(files))
	for _, m := range files {
		if prior, ok := seen[m.version]; ok {
			panic(fmt.Sprintf(
				"schema: duplicate migration version %d: %q and %q — renumber one before commit",
				m.version, prior, m.name,
			))
		}
		seen[m.version] = m.name
	}
}

func (m migrationSource) list() []migrationFile {
	entries, err := fs.ReadDir(m.files, m.dir)
	if err != nil {
		panic(fmt.Sprintf("schema: failed to read embedded %s: %v", m.dir, err))
	}
	var files []migrationFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		v, err := parseVersion(e.Name())
		if err != nil {
			panic(fmt.Sprintf("schema: invalid migration filename %q: %v", e.Name(), err))
		}
		files = append(files, migrationFile{version: v, name: e.Name()})
	}
	checkNoDuplicateVersions(files)
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files
}

func (m migrationSource) latest() int {
	files := m.list()
	if len(files) == 0 {
		return 0
	}
	return files[len(files)-1].version
}

func (m migrationSource) atLatest(ctx context.Context, db DBConn) bool {
	current, err := m.currentVersion(ctx, db)
	if err != nil {
		return false
	}
	return current >= m.latest()
}

func (m migrationSource) currentVersion(ctx context.Context, db DBConn) (int, error) {
	var current int
	err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM "+m.cursorTable).Scan(&current)
	if err == nil || err == sql.ErrNoRows {
		return current, nil
	}
	if dberrors.IsTableNotExist(err) {
		return 0, nil
	}
	return 0, fmt.Errorf("reading %s version: %w", m.cursorTable, err)
}

func (m migrationSource) pendingVersions(ctx context.Context, db DBConn) ([]int, error) {
	current, err := m.currentVersion(ctx, db)
	if err != nil {
		return nil, err
	}
	files := m.list()
	pending := make([]int, 0, len(files))
	for _, mf := range files {
		if mf.version > current {
			pending = append(pending, mf.version)
		}
	}
	return pending, nil
}

func (m migrationSource) pendingMigrationDirtyTables(ctx context.Context, db DBConn, dirtyBefore map[string]dirtyTableState) ([]string, error) {
	if len(dirtyBefore) == 0 {
		return nil, nil
	}
	current, err := m.currentVersion(ctx, db)
	if err != nil {
		return nil, err
	}

	dirtyNames := sortedDirtyTableNames(dirtyBefore)
	touched := make(map[string]struct{})
	for _, mf := range m.list() {
		if mf.version <= current {
			continue
		}
		data, err := m.files.ReadFile(m.dir + "/" + mf.name)
		if err != nil {
			return nil, fmt.Errorf("reading migration %s: %w", mf.name, err)
		}
		sqlText := string(data)
		for _, table := range dirtyNames {
			if migrationSQLTouchesTable(sqlText, table) {
				touched[table] = struct{}{}
			}
		}
	}

	names := make([]string, 0, len(touched))
	for table := range touched {
		names = append(names, table)
	}
	sort.Strings(names)
	return names, nil
}

func migrationSQLTouchesTable(sqlText, table string) bool {
	tableRef := "`?" + regexp.QuoteMeta(table) + "`?"
	// This intentionally scans raw migration text so PREPARE strings that run
	// DDL/DML are treated as real table touches.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:alter\s+table|update|delete\s+from|insert(?:\s+ignore)?\s+into|replace\s+into|truncate\s+table|drop\s+table|create\s+table(?:\s+if\s+not\s+exists)?|rename\s+table)\s+` + tableRef + `\b`),
		regexp.MustCompile(`(?i)\brename\s+table\b[^;]*\bto\s+` + tableRef + `\b`),
		regexp.MustCompile(`(?i)\bcreate\s+(?:unique\s+)?index\b[^;]*\bon\s+` + tableRef + `\b`),
		regexp.MustCompile(`(?i)\b(?:create\s+(?:or\s+replace\s+)?view|alter\s+view)\s+` + tableRef + `\b`),
	}
	for _, pattern := range patterns {
		if pattern.MatchString(sqlText) {
			return true
		}
	}
	return false
}

func (m migrationSource) migrate(ctx context.Context, db DBConn) (int, error) {
	if _, err := db.ExecContext(ctx, m.bootstrapSQL()); err != nil {
		return 0, fmt.Errorf("creating %s: %w", m.cursorTable, err)
	}

	var current int
	err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM "+m.cursorTable).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("reading %s version: %w", m.cursorTable, err)
	}

	if current >= m.latest() {
		return 0, nil
	}

	count := 0
	for _, mf := range m.list() {
		if mf.version <= current {
			continue
		}
		data, err := m.files.ReadFile(m.dir + "/" + mf.name)
		if err != nil {
			return count, fmt.Errorf("reading migration %s: %w", mf.name, err)
		}
		if _, err := db.ExecContext(ctx, string(data)); err != nil {
			return count, fmt.Errorf("migration %s: %w", mf.name, err)
		}
		if _, err := db.ExecContext(ctx, "INSERT IGNORE INTO "+m.cursorTable+" (version) VALUES (?)", mf.version); err != nil {
			return count, fmt.Errorf("recording %s in %s: %w", mf.name, m.cursorTable, err)
		}
		count++
	}
	return count, nil
}
