package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// sqliteFingerprint is the complete, canonical read-only observation of a
// historical SQLite database. Physical allocation details such as rootpage and
// freelist state are deliberately excluded.
type sqliteFingerprint struct {
	Objects          []sqliteSchemaObject    `json:"objects"`
	Tables           []sqliteTable           `json:"tables"`
	Pragmas          []sqlitePragma          `json:"pragmas"`
	MigrationLedgers []sqliteMigrationLedger `json:"migration_ledgers"`
}

type sqliteSchemaObject struct {
	Type  string  `json:"type"`
	Name  string  `json:"name"`
	Table string  `json:"table"`
	SQL   *string `json:"sql"`
}

type sqliteTable struct {
	Name        string             `json:"name"`
	Columns     []sqliteColumn     `json:"columns"`
	ForeignKeys []sqliteForeignKey `json:"foreign_keys"`
	Indexes     []sqliteIndex      `json:"indexes"`
}

type sqliteColumn struct {
	CID          int     `json:"cid"`
	Name         string  `json:"name"`
	DeclaredType string  `json:"declared_type"`
	NotNull      int     `json:"not_null"`
	Default      *string `json:"default"`
	PrimaryKey   int     `json:"primary_key"`
	Hidden       int     `json:"hidden"`
}

type sqliteForeignKey struct {
	ID       int     `json:"id"`
	Sequence int     `json:"sequence"`
	Table    string  `json:"table"`
	From     string  `json:"from"`
	To       *string `json:"to"`
	OnUpdate string  `json:"on_update"`
	OnDelete string  `json:"on_delete"`
	Match    string  `json:"match"`
}

type sqliteIndex struct {
	Sequence int               `json:"sequence"`
	Name     string            `json:"name"`
	Unique   int               `json:"unique"`
	Origin   string            `json:"origin"`
	Partial  int               `json:"partial"`
	Columns  []sqliteIndexInfo `json:"columns"`
}

type sqliteIndexInfo struct {
	Sequence  int     `json:"sequence"`
	CID       int     `json:"cid"`
	Name      *string `json:"name"`
	Desc      int     `json:"desc"`
	Collation *string `json:"collation"`
	Key       int     `json:"key"`
}

type sqlitePragma struct {
	Name  string      `json:"name"`
	Value sqliteValue `json:"value"`
}

type sqliteMigrationLedger struct {
	Table   string          `json:"table"`
	Columns []string        `json:"columns"`
	Rows    [][]sqliteValue `json:"rows"`
}

// sqliteValue keeps SQL NULL, numeric values, text, and blobs distinct while
// remaining stable under JSON encoding.
type sqliteValue struct {
	Type  string `json:"type"`
	Value string `json:"value,omitempty"`
}

var fingerprintPragmas = []string{
	"application_id",
	"auto_vacuum",
	"encoding",
	"journal_mode",
	"page_size",
	"user_version",
}

func collectSQLiteLayout(workspace string, topology freshTopology) (json.RawMessage, error) {
	active, err := collectSQLite(filepath.Join(workspace, topology.Database))
	if err != nil {
		return nil, err
	}
	backups, err := collectRetainedSQLiteBackupSchemas(workspace, topology.SQLiteBackups)
	if err != nil {
		return nil, err
	}
	layout, err := json.Marshal(struct {
		Topology              []string            `json:"topology"`
		Schema                sqliteFingerprint   `json:"schema"`
		RetainedBackupSchemas []sqliteFingerprint `json:"retained_backup_schemas,omitempty"`
	}{Topology: topology.Markers, Schema: active, RetainedBackupSchemas: backups})
	if err != nil {
		return nil, err
	}
	return canonicalJSON(layout)
}

// collectRetainedSQLiteBackupSchemas observes retained migration backups using
// the same public, read-only SQLite surface as active SQLite storage. Backup
// names are deliberately excluded: timestamps and collision suffixes are
// operational details, not layout differences.
func collectRetainedSQLiteBackupSchemas(workspace string, paths []string) ([]sqliteFingerprint, error) {
	distinct := make(map[string]sqliteFingerprint, len(paths))
	for _, path := range paths {
		fingerprint, err := collectSQLite(filepath.Join(workspace, path))
		if err != nil {
			return nil, fmt.Errorf("fingerprint retained pre-Dolt backup %s: %w", path, err)
		}
		raw, err := json.Marshal(fingerprint)
		if err != nil {
			return nil, err
		}
		distinct[string(raw)] = fingerprint
	}
	keys := make([]string, 0, len(distinct))
	for key := range distinct {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	backups := make([]sqliteFingerprint, 0, len(keys))
	for _, key := range keys {
		backups = append(backups, distinct[key])
	}
	return backups, nil
}

// attachSQLiteEvidenceToDoltLayout fingerprints every recognized SQLite root
// alongside the metadata-selected Dolt store.
func attachSQLiteEvidenceToDoltLayout(workspace string, topology freshTopology, layout json.RawMessage) (json.RawMessage, error) {
	if topology.CoexistingSQLite == "" && len(topology.SQLiteBackups) == 0 {
		return layout, nil
	}
	backups, err := collectRetainedSQLiteBackupSchemas(workspace, topology.SQLiteBackups)
	if err != nil {
		return nil, err
	}
	var coexisting *sqliteFingerprint
	if topology.CoexistingSQLite != "" {
		fingerprint, err := collectSQLite(filepath.Join(workspace, topology.CoexistingSQLite))
		if err != nil {
			return nil, fmt.Errorf("fingerprint coexisting SQLite schema %s: %w", topology.CoexistingSQLite, err)
		}
		coexisting = &fingerprint
	}
	var observed struct {
		Topology  []string        `json:"topology"`
		Schema    json.RawMessage `json:"schema"`
		Stores    json.RawMessage `json:"stores"`
		Databases json.RawMessage `json:"databases"`
	}
	if err := json.Unmarshal(layout, &observed); err != nil {
		return nil, fmt.Errorf("decode Dolt layout before attaching retained SQLite backups: %w", err)
	}
	combined, err := json.Marshal(struct {
		CoexistingSQLiteSchema *sqliteFingerprint  `json:"coexisting_sqlite_schema,omitempty"`
		RetainedBackupSchemas  []sqliteFingerprint `json:"retained_backup_schemas,omitempty"`
		Schema                 json.RawMessage     `json:"schema"`
		Stores                 json.RawMessage     `json:"stores,omitempty"`
		Databases              json.RawMessage     `json:"databases,omitempty"`
		Topology               []string            `json:"topology"`
	}{
		CoexistingSQLiteSchema: coexisting,
		RetainedBackupSchemas:  backups,
		Schema:                 observed.Schema,
		Stores:                 observed.Stores,
		Databases:              observed.Databases,
		Topology:               observed.Topology,
	})
	if err != nil {
		return nil, err
	}
	return canonicalJSON(combined)
}

func collectSQLite(path string) (sqliteFingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return sqliteFingerprint{}, fmt.Errorf("stat SQLite database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return sqliteFingerprint{}, fmt.Errorf("SQLite database is not a regular file: %s", path)
	}
	dsn, err := sqliteReadOnlyDSN(path)
	if err != nil {
		return sqliteFingerprint{}, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return sqliteFingerprint{}, fmt.Errorf("open SQLite database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return sqliteFingerprint{}, fmt.Errorf("open SQLite database read-only: %w", err)
	}
	var queryOnly int
	if err := db.QueryRow(`PRAGMA query_only`).Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = db.Close()
		if err != nil {
			return sqliteFingerprint{}, fmt.Errorf("verify SQLite query_only: %w", err)
		}
		return sqliteFingerprint{}, errors.New("SQLite connection is not query-only")
	}

	fingerprint, collectErr := collectSQLiteDB(db)
	closeErr := db.Close()
	if collectErr != nil {
		return sqliteFingerprint{}, collectErr
	}
	if closeErr != nil {
		return sqliteFingerprint{}, fmt.Errorf("close SQLite database: %w", closeErr)
	}
	return fingerprint, nil
}

func sqliteReadOnlyDSN(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite database path: %w", err)
	}
	query := url.Values{}
	query.Set("_query_only", "1")
	query.Set("mode", "ro")
	return (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(absolute),
		RawQuery: query.Encode(),
	}).String(), nil
}

func collectSQLiteDB(db *sql.DB) (sqliteFingerprint, error) {
	objects, err := collectSQLiteObjects(db)
	if err != nil {
		return sqliteFingerprint{}, err
	}
	tables := make([]sqliteTable, 0)
	for _, object := range objects {
		if object.Type != "table" {
			continue
		}
		table, err := collectSQLiteTable(db, object.Name)
		if err != nil {
			return sqliteFingerprint{}, err
		}
		tables = append(tables, table)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })

	pragmas := make([]sqlitePragma, 0, len(fingerprintPragmas))
	for _, name := range fingerprintPragmas {
		var raw any
		if err := db.QueryRow("PRAGMA " + name).Scan(&raw); err != nil { //nolint:gosec // name comes from the fixed list above.
			return sqliteFingerprint{}, fmt.Errorf("read PRAGMA %s: %w", name, err)
		}
		value, err := canonicalSQLiteValue(raw)
		if err != nil {
			return sqliteFingerprint{}, fmt.Errorf("read PRAGMA %s: %w", name, err)
		}
		pragmas = append(pragmas, sqlitePragma{Name: name, Value: value})
	}
	sort.Slice(pragmas, func(i, j int) bool { return pragmas[i].Name < pragmas[j].Name })

	ledgers := make([]sqliteMigrationLedger, 0)
	for _, table := range tables {
		if !isMigrationLedger(table.Name) {
			continue
		}
		ledger, err := collectSQLiteMigrationLedger(db, table)
		if err != nil {
			return sqliteFingerprint{}, err
		}
		ledgers = append(ledgers, ledger)
	}
	sort.Slice(ledgers, func(i, j int) bool { return ledgers[i].Table < ledgers[j].Table })

	return sqliteFingerprint{
		Objects:          objects,
		Tables:           tables,
		Pragmas:          pragmas,
		MigrationLedgers: ledgers,
	}, nil
}

func collectSQLiteObjects(db *sql.DB) ([]sqliteSchemaObject, error) {
	rows, err := db.Query(`
		SELECT type, name, tbl_name, sql
		FROM main.sqlite_schema
		WHERE type IN ('index', 'table', 'trigger', 'view')`)
	if err != nil {
		return nil, fmt.Errorf("read sqlite_schema: %w", err)
	}
	defer rows.Close()
	objects := make([]sqliteSchemaObject, 0)
	for rows.Next() {
		var object sqliteSchemaObject
		var statement sql.NullString
		if err := rows.Scan(&object.Type, &object.Name, &object.Table, &statement); err != nil {
			return nil, fmt.Errorf("scan sqlite_schema: %w", err)
		}
		object.SQL = nullableString(statement)
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read sqlite_schema: %w", err)
	}
	sort.Slice(objects, func(i, j int) bool {
		left, right := objects[i], objects[j]
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		return left.Table < right.Table
	})
	return objects, nil
}

func collectSQLiteTable(db *sql.DB, name string) (sqliteTable, error) {
	columns, err := collectSQLiteColumns(db, name)
	if err != nil {
		return sqliteTable{}, err
	}
	foreignKeys, err := collectSQLiteForeignKeys(db, name)
	if err != nil {
		return sqliteTable{}, err
	}
	indexes, err := collectSQLiteIndexes(db, name)
	if err != nil {
		return sqliteTable{}, err
	}
	return sqliteTable{
		Name:        name,
		Columns:     columns,
		ForeignKeys: foreignKeys,
		Indexes:     indexes,
	}, nil
}

func collectSQLiteColumns(db *sql.DB, table string) ([]sqliteColumn, error) {
	rows, err := db.Query("PRAGMA main.table_xinfo(" + sqliteStringLiteral(table) + ")") //nolint:gosec // table is safely quoted as a SQLite string literal.
	if err != nil {
		return nil, fmt.Errorf("read table_xinfo(%q): %w", table, err)
	}
	defer rows.Close()
	columns := make([]sqliteColumn, 0)
	for rows.Next() {
		var column sqliteColumn
		var defaultValue sql.NullString
		if err := rows.Scan(&column.CID, &column.Name, &column.DeclaredType,
			&column.NotNull, &defaultValue, &column.PrimaryKey, &column.Hidden); err != nil {
			return nil, fmt.Errorf("scan table_xinfo(%q): %w", table, err)
		}
		column.Default = nullableString(defaultValue)
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read table_xinfo(%q): %w", table, err)
	}
	sort.Slice(columns, func(i, j int) bool {
		if columns[i].CID != columns[j].CID {
			return columns[i].CID < columns[j].CID
		}
		return columns[i].Name < columns[j].Name
	})
	return columns, nil
}

func collectSQLiteForeignKeys(db *sql.DB, table string) ([]sqliteForeignKey, error) {
	rows, err := db.Query("PRAGMA main.foreign_key_list(" + sqliteStringLiteral(table) + ")") //nolint:gosec // table is safely quoted as a SQLite string literal.
	if err != nil {
		return nil, fmt.Errorf("read foreign_key_list(%q): %w", table, err)
	}
	defer rows.Close()
	foreignKeys := make([]sqliteForeignKey, 0)
	for rows.Next() {
		var foreignKey sqliteForeignKey
		var target sql.NullString
		if err := rows.Scan(&foreignKey.ID, &foreignKey.Sequence, &foreignKey.Table,
			&foreignKey.From, &target, &foreignKey.OnUpdate, &foreignKey.OnDelete,
			&foreignKey.Match); err != nil {
			return nil, fmt.Errorf("scan foreign_key_list(%q): %w", table, err)
		}
		foreignKey.To = nullableString(target)
		foreignKeys = append(foreignKeys, foreignKey)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read foreign_key_list(%q): %w", table, err)
	}
	sort.Slice(foreignKeys, func(i, j int) bool {
		left, right := foreignKeys[i], foreignKeys[j]
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		if left.Sequence != right.Sequence {
			return left.Sequence < right.Sequence
		}
		if left.Table != right.Table {
			return left.Table < right.Table
		}
		return left.From < right.From
	})
	return foreignKeys, nil
}

func collectSQLiteIndexes(db *sql.DB, table string) ([]sqliteIndex, error) {
	rows, err := db.Query("PRAGMA main.index_list(" + sqliteStringLiteral(table) + ")") //nolint:gosec // table is safely quoted as a SQLite string literal.
	if err != nil {
		return nil, fmt.Errorf("read index_list(%q): %w", table, err)
	}
	defer rows.Close()
	indexes := make([]sqliteIndex, 0)
	for rows.Next() {
		var index sqliteIndex
		if err := rows.Scan(&index.Sequence, &index.Name, &index.Unique,
			&index.Origin, &index.Partial); err != nil {
			return nil, fmt.Errorf("scan index_list(%q): %w", table, err)
		}
		indexes = append(indexes, index)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read index_list(%q): %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close index_list(%q): %w", table, err)
	}
	for i := range indexes {
		columns, err := collectSQLiteIndexInfo(db, indexes[i].Name)
		if err != nil {
			return nil, err
		}
		indexes[i].Columns = columns
	}
	sort.Slice(indexes, func(i, j int) bool {
		if indexes[i].Name != indexes[j].Name {
			return indexes[i].Name < indexes[j].Name
		}
		return indexes[i].Sequence < indexes[j].Sequence
	})
	return indexes, nil
}

func collectSQLiteIndexInfo(db *sql.DB, index string) ([]sqliteIndexInfo, error) {
	rows, err := db.Query("PRAGMA main.index_xinfo(" + sqliteStringLiteral(index) + ")") //nolint:gosec // index is safely quoted as a SQLite string literal.
	if err != nil {
		return nil, fmt.Errorf("read index_xinfo(%q): %w", index, err)
	}
	defer rows.Close()
	columns := make([]sqliteIndexInfo, 0)
	for rows.Next() {
		var column sqliteIndexInfo
		var name, collation sql.NullString
		if err := rows.Scan(&column.Sequence, &column.CID, &name, &column.Desc,
			&collation, &column.Key); err != nil {
			return nil, fmt.Errorf("scan index_xinfo(%q): %w", index, err)
		}
		column.Name = nullableString(name)
		column.Collation = nullableString(collation)
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read index_xinfo(%q): %w", index, err)
	}
	sort.Slice(columns, func(i, j int) bool {
		if columns[i].Sequence != columns[j].Sequence {
			return columns[i].Sequence < columns[j].Sequence
		}
		return columns[i].CID < columns[j].CID
	})
	return columns, nil
}

func collectSQLiteMigrationLedger(db *sql.DB, table sqliteTable) (sqliteMigrationLedger, error) {
	columns := make([]string, len(table.Columns))
	quoted := make([]string, len(table.Columns))
	for i, column := range table.Columns {
		columns[i] = column.Name
		quoted[i] = sqliteIdentifier(column.Name)
	}
	if len(columns) == 0 {
		return sqliteMigrationLedger{}, fmt.Errorf("migration ledger %q has no columns", table.Name)
	}
	query := "SELECT " + strings.Join(quoted, ", ") + " FROM " + sqliteIdentifier(table.Name) //nolint:gosec // every identifier is safely double-quoted.
	rows, err := db.Query(query)
	if err != nil {
		return sqliteMigrationLedger{}, fmt.Errorf("read migration ledger %q: %w", table.Name, err)
	}
	defer rows.Close()
	result := make([][]sqliteValue, 0)
	for rows.Next() {
		raw := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range raw {
			destinations[i] = &raw[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return sqliteMigrationLedger{}, fmt.Errorf("scan migration ledger %q: %w", table.Name, err)
		}
		values := make([]sqliteValue, len(raw))
		for i, value := range raw {
			canonical, err := canonicalSQLiteValue(value)
			if err != nil {
				return sqliteMigrationLedger{}, fmt.Errorf(
					"scan migration ledger %q column %q: %w", table.Name, columns[i], err)
			}
			values[i] = canonical
		}
		result = append(result, values)
	}
	if err := rows.Err(); err != nil {
		return sqliteMigrationLedger{}, fmt.Errorf("read migration ledger %q: %w", table.Name, err)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return compareSQLiteRows(result[i], result[j]) < 0
	})
	return sqliteMigrationLedger{Table: table.Name, Columns: columns, Rows: result}, nil
}

func canonicalSQLiteValue(value any) (sqliteValue, error) {
	switch typed := value.(type) {
	case nil:
		return sqliteValue{Type: "null"}, nil
	case int64:
		return sqliteValue{Type: "integer", Value: strconv.FormatInt(typed, 10)}, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return sqliteValue{}, fmt.Errorf("non-finite SQLite real %v", typed)
		}
		return sqliteValue{Type: "real", Value: strconv.FormatFloat(typed, 'g', -1, 64)}, nil
	case bool:
		if typed {
			return sqliteValue{Type: "integer", Value: "1"}, nil
		}
		return sqliteValue{Type: "integer", Value: "0"}, nil
	case string:
		return sqliteValue{Type: "text", Value: typed}, nil
	case []byte:
		return sqliteValue{Type: "blob", Value: base64.StdEncoding.EncodeToString(typed)}, nil
	case time.Time:
		return sqliteValue{Type: "text", Value: typed.UTC().Format(time.RFC3339Nano)}, nil
	default:
		return sqliteValue{}, fmt.Errorf("unsupported SQLite value type %T", value)
	}
}

func compareSQLiteRows(left, right []sqliteValue) int {
	count := min(len(left), len(right))
	for i := 0; i < count; i++ {
		leftValue := left[i].Type + "\x00" + left[i].Value
		rightValue := right[i].Type + "\x00" + right[i].Value
		if compared := strings.Compare(leftValue, rightValue); compared != 0 {
			return compared
		}
	}
	return len(left) - len(right)
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func isMigrationLedger(table string) bool {
	name := strings.ToLower(table)
	return name == "migrations" || name == "schema_version" ||
		name == "schema_versions" || strings.HasSuffix(name, "_migrations")
}

func sqliteStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func sqliteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
