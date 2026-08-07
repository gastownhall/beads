package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestSQLiteLayoutRetainsNormalizedDistinctPreDoltBackups(t *testing.T) {
	first := sqliteBackupWorkspaceForTest(t,
		[]string{
			"beads.backup-pre-dolt-20260728-010203.db",
			"beads.backup-pre-dolt-20260728-010204-2.db",
			"beads.backup-pre-dolt-20260728-010205.db",
		},
		[]string{"id TEXT PRIMARY KEY, title TEXT", "id TEXT PRIMARY KEY, title TEXT", "id TEXT PRIMARY KEY, title TEXT, status TEXT"},
	)
	second := sqliteBackupWorkspaceForTest(t,
		[]string{
			"beads.backup-pre-dolt-20250101-111111-9.db",
			"beads.backup-pre-dolt-20251231-235959.db",
		},
		[]string{"id TEXT PRIMARY KEY, title TEXT, status TEXT", "id TEXT PRIMARY KEY, title TEXT"},
	)

	var firstLayout, secondLayout json.RawMessage
	for index, workspace := range []string{first, second} {
		mode, layout, err := probeFreshLayout(context.Background(), workspace, nil)
		if err != nil {
			t.Fatal(err)
		}
		if mode != "sqlite" {
			t.Fatalf("mode = %q, want sqlite", mode)
		}
		var observed struct {
			Topology              []string            `json:"topology"`
			RetainedBackupSchemas []sqliteFingerprint `json:"retained_backup_schemas"`
		}
		if err := json.Unmarshal(layout, &observed); err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(strings.Join(observed.Topology, "\x00"), "sqlite-backups:pre-dolt"); got != 1 {
			t.Fatalf("backup topology marker count = %d, topology = %v", got, observed.Topology)
		}
		if len(observed.RetainedBackupSchemas) != 2 {
			t.Fatalf("retained backup schemas = %d, want two distinct schemas", len(observed.RetainedBackupSchemas))
		}
		rolled, err := fingerprintRetainedSQLiteWorkspace(&retainedSQLiteWorkspace{path: workspace})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(rolled.Layout, layout) {
			t.Fatal("fresh and rolling SQLite paths produced different layouts")
		}
		if index == 0 {
			firstLayout = layout
		} else {
			secondLayout = layout
		}
	}
	if !bytes.Equal(firstLayout, secondLayout) {
		t.Fatalf("backup timestamp/count changed the family:\nfirst: %s\nsecond: %s", firstLayout, secondLayout)
	}
}

func TestDoltLayoutRetainsNormalizedPreDoltBackupSchemas(t *testing.T) {
	first := doltBackupWorkspaceForTest(t,
		[]string{"beads.backup-pre-dolt-20260728-010203.db", "beads.backup-pre-dolt-20260728-010204-2.db"},
		[]string{"id TEXT PRIMARY KEY, title TEXT", "id TEXT PRIMARY KEY, title TEXT"},
	)
	second := doltBackupWorkspaceForTest(t,
		[]string{"beads.backup-pre-dolt-20250101-111111-9.db"},
		[]string{"id TEXT PRIMARY KEY, title TEXT"},
	)
	different := doltBackupWorkspaceForTest(t,
		[]string{"beads.backup-pre-dolt-20250101-111111.db"},
		[]string{"id TEXT PRIMARY KEY, title TEXT, status TEXT"},
	)

	var layouts []json.RawMessage
	for _, workspace := range []string{first, second, different} {
		topology, err := recognizeFreshTopology(workspace)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := attachSQLiteEvidenceToDoltLayout(workspace, topology, json.RawMessage(`{"schema":{},"topology":`+mustJSON(t, topology.Markers)+`}`))
		if err != nil {
			t.Fatal(err)
		}
		layouts = append(layouts, layout)
	}
	if !bytes.Equal(layouts[0], layouts[1]) {
		t.Fatalf("backup timestamp/copy suffix changed Dolt layout:\nfirst: %s\nsecond: %s", layouts[0], layouts[1])
	}
	if bytes.Equal(layouts[0], layouts[2]) {
		t.Fatalf("different retained backup schema did not change Dolt layout: %s", layouts[0])
	}
}

func TestDoltLayoutFingerprintsMetadataSelectedCoexistingSQLite(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"backend":"dolt","database":"beads.db"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(beadsDir, "beads.db")
	createSQLiteSchemaForTest(t, database, "id TEXT PRIMARY KEY, title TEXT")
	createSQLiteSchemaForTest(
		t,
		filepath.Join(beadsDir, "beads.backup-pre-dolt-20260728-010203.db"),
		"id TEXT PRIMARY KEY, title TEXT, status TEXT",
	)

	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := attachSQLiteEvidenceToDoltLayout(
		workspace,
		topology,
		json.RawMessage(`{"databases":[{"name":"beads","schema":{}},{"name":"beads_census","schema":{}}],"schema":{},"topology":`+mustJSON(t, topology.Markers)+`}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		CoexistingSQLiteSchema *sqliteFingerprint  `json:"coexisting_sqlite_schema"`
		RetainedBackupSchemas  []sqliteFingerprint `json:"retained_backup_schemas"`
		Databases              []json.RawMessage   `json:"databases"`
	}
	if err := json.Unmarshal(layout, &observed); err != nil {
		t.Fatal(err)
	}
	want, err := collectSQLite(database)
	if err != nil {
		t.Fatal(err)
	}
	if observed.CoexistingSQLiteSchema == nil || !reflect.DeepEqual(*observed.CoexistingSQLiteSchema, want) {
		t.Fatalf("coexisting SQLite schema = %#v, want %#v", observed.CoexistingSQLiteSchema, want)
	}
	if len(observed.RetainedBackupSchemas) != 1 {
		t.Fatalf("retained backup schemas = %d, want 1", len(observed.RetainedBackupSchemas))
	}
	if len(observed.Databases) != 2 {
		t.Fatalf("databases = %s, want preserved multi-database evidence", layout)
	}
}

func TestRecognizeFreshTopologyRejectsUnknownOrBackupOnlySQLiteShapes(t *testing.T) {
	for _, test := range []struct {
		name  string
		files []string
	}{
		{name: "multiple active databases", files: []string{"beads.db", "other.db"}},
		{name: "invalid date", files: []string{"beads.db", "beads.backup-pre-dolt-20260230-010203.db"}},
		{name: "malformed timestamp", files: []string{"beads.db", "beads.backup-pre-dolt-20260228-0102.db"}},
		{name: "invalid collision suffix", files: []string{"beads.db", "beads.backup-pre-dolt-20260228-010203-0.db"}},
		{name: "wrong active basename", files: []string{"beads.db", "other.backup-pre-dolt-20260228-010203.db"}},
		{name: "backup only", files: []string{"beads.backup-pre-dolt-20260228-010203.db"}},
		{name: "unknown backup-like only", files: []string{"beads.backup-pre-dolt-invalid.db"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range test.files {
				if err := os.WriteFile(filepath.Join(beadsDir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := recognizeFreshTopology(workspace); err == nil {
				t.Fatal("accepted an ambiguous or unknown SQLite shape")
			}
		})
	}
}

func sqliteBackupWorkspaceForTest(t *testing.T, backups, definitions []string) string {
	t.Helper()
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	createSQLiteSchemaForTest(t, filepath.Join(beadsDir, "beads.db"), "id TEXT PRIMARY KEY")
	for index, name := range backups {
		createSQLiteSchemaForTest(t, filepath.Join(beadsDir, name), definitions[index])
	}
	return workspace
}

func doltBackupWorkspaceForTest(t *testing.T, backups, definitions []string) string {
	t.Helper()
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	for index, name := range backups {
		createSQLiteSchemaForTest(t, filepath.Join(beadsDir, name), definitions[index])
	}
	return workspace
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func createSQLiteSchemaForTest(t *testing.T, path, definition string) {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE issues (" + definition + ")"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCollectSQLiteCanonicalCompleteAndReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical schema ? #.db")
	db, err := sql.Open("sqlite3", (&url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(path),
		RawQuery: "mode=rwc",
	}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`PRAGMA application_id = 4242`,
		`PRAGMA user_version = 7`,
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE parent (
			id INTEGER PRIMARY KEY,
			code TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE child (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			parent_id INTEGER,
			payload TEXT DEFAULT 'x',
			calculated TEXT GENERATED ALWAYS AS (payload || '!') STORED,
			FOREIGN KEY (parent_id) REFERENCES parent(id)
				ON UPDATE RESTRICT ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX idx_child_payload
			ON child(payload COLLATE NOCASE DESC)
			WHERE payload IS NOT NULL`,
		`CREATE VIEW child_view AS SELECT id, payload FROM child`,
		`CREATE TRIGGER child_insert AFTER INSERT ON child
			BEGIN UPDATE child SET payload = NEW.payload WHERE id = NEW.id; END`,
		`CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			dirty BOOLEAN NOT NULL DEFAULT 0,
			note TEXT
		)`,
		`INSERT INTO schema_migrations(version, dirty, note) VALUES (2, 0, NULL)`,
		`INSERT INTO schema_migrations(version, dirty, note) VALUES (1, 1, 'first')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	beforeRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	got, err := collectSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := collectSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, again) {
		t.Fatal("repeated SQLite collection was not deterministic")
	}

	afterRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(afterRaw) != sha256.Sum256(beforeRaw) ||
		!afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatal("read-only SQLite collection changed the database")
	}

	if !isSortedObjects(got.Objects) {
		t.Fatalf("schema objects are not canonical: %+v", got.Objects)
	}
	for _, want := range []sqliteObjectKey{
		{Type: "index", Name: "idx_child_payload"},
		{Type: "table", Name: "child"},
		{Type: "trigger", Name: "child_insert"},
		{Type: "view", Name: "child_view"},
	} {
		if !hasSQLiteObject(got.Objects, want) {
			t.Errorf("missing sqlite_schema object %+v", want)
		}
	}

	child := findSQLiteTable(t, got, "child")
	if len(child.Columns) != 4 {
		t.Fatalf("child columns = %+v", child.Columns)
	}
	if column := child.Columns[3]; column.Name != "calculated" || column.Hidden != 3 {
		t.Fatalf("generated column = %+v, want hidden=3", column)
	}
	if len(child.ForeignKeys) != 1 {
		t.Fatalf("child foreign keys = %+v", child.ForeignKeys)
	}
	fk := child.ForeignKeys[0]
	if fk.Table != "parent" || fk.From != "parent_id" || fk.To == nil ||
		*fk.To != "id" || fk.OnUpdate != "RESTRICT" || fk.OnDelete != "CASCADE" {
		t.Fatalf("child foreign key = %+v", fk)
	}
	if len(child.Indexes) != 1 {
		t.Fatalf("child indexes = %+v", child.Indexes)
	}
	index := child.Indexes[0]
	if index.Name != "idx_child_payload" || index.Unique != 1 || index.Partial != 1 ||
		len(index.Columns) < 2 || index.Columns[0].Name == nil ||
		*index.Columns[0].Name != "payload" || index.Columns[0].Desc != 1 ||
		index.Columns[0].Collation == nil || *index.Columns[0].Collation != "NOCASE" {
		t.Fatalf("child partial index = %+v", index)
	}

	if pragmaValue(t, got, "application_id") != "4242" ||
		pragmaValue(t, got, "user_version") != "7" {
		t.Fatalf("selected pragmas = %+v", got.Pragmas)
	}
	if !isSortedPragmas(got.Pragmas) {
		t.Fatalf("pragmas are not canonical: %+v", got.Pragmas)
	}

	if len(got.MigrationLedgers) != 1 {
		t.Fatalf("migration ledgers = %+v", got.MigrationLedgers)
	}
	ledger := got.MigrationLedgers[0]
	if ledger.Table != "schema_migrations" ||
		!reflect.DeepEqual(ledger.Columns, []string{"version", "dirty", "note"}) ||
		len(ledger.Rows) != 2 {
		t.Fatalf("migration ledger = %+v", ledger)
	}
	wantRows := [][]sqliteValue{
		{{Type: "integer", Value: "1"}, {Type: "integer", Value: "1"}, {Type: "text", Value: "first"}},
		{{Type: "integer", Value: "2"}, {Type: "integer", Value: "0"}, {Type: "null"}},
	}
	if !reflect.DeepEqual(ledger.Rows, wantRows) {
		t.Fatalf("migration rows = %#v, want %#v", ledger.Rows, wantRows)
	}
}

func TestCollectSQLiteMissingPathDoesNotCreateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := collectSQLite(path); err == nil {
		t.Fatal("collectSQLite accepted a missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}
}

func TestCollectSQLiteIgnoresMutableSchemaVersionCounter(t *testing.T) {
	create := func(t *testing.T, path string, withDiscardedDDL bool) sqliteFingerprint {
		t.Helper()
		db, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		if withDiscardedDDL {
			if _, err := db.Exec(`CREATE TABLE discarded (id INTEGER); DROP TABLE discarded`); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(`CREATE TABLE issues (id TEXT PRIMARY KEY, title TEXT NOT NULL)`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		fingerprint, err := collectSQLite(path)
		if err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}

	direct := create(t, filepath.Join(t.TempDir(), "direct.db"), false)
	withDiscardedDDL := create(t, filepath.Join(t.TempDir(), "discarded-ddl.db"), true)
	if !reflect.DeepEqual(direct, withDiscardedDDL) {
		t.Fatalf("mutable SQLite schema_version split one semantic schema:\ndirect: %#v\nwith discarded DDL: %#v", direct, withDiscardedDDL)
	}
}

func TestSQLiteReadOnlyDSNEscapesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db ? #.sqlite")
	dsn, err := sqliteReadOnlyDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dsn, "file:") ||
		!strings.Contains(dsn, "mode=ro") ||
		!strings.Contains(dsn, "_query_only=1") ||
		strings.Contains(dsn, "db ? #.sqlite") {
		t.Fatalf("read-only DSN = %q", dsn)
	}
}

type sqliteObjectKey struct {
	Type string
	Name string
}

func hasSQLiteObject(objects []sqliteSchemaObject, want sqliteObjectKey) bool {
	for _, object := range objects {
		if object.Type == want.Type && object.Name == want.Name {
			return true
		}
	}
	return false
}

func isSortedObjects(objects []sqliteSchemaObject) bool {
	for i := 1; i < len(objects); i++ {
		previous := objects[i-1].Type + "\x00" + objects[i-1].Name + "\x00" + objects[i-1].Table
		current := objects[i].Type + "\x00" + objects[i].Name + "\x00" + objects[i].Table
		if previous > current {
			return false
		}
	}
	return true
}

func isSortedPragmas(pragmas []sqlitePragma) bool {
	for i := 1; i < len(pragmas); i++ {
		if pragmas[i-1].Name >= pragmas[i].Name {
			return false
		}
	}
	return true
}

func findSQLiteTable(t *testing.T, fingerprint sqliteFingerprint, name string) sqliteTable {
	t.Helper()
	for _, table := range fingerprint.Tables {
		if table.Name == name {
			return table
		}
	}
	t.Fatalf("missing table %q in %+v", name, fingerprint.Tables)
	return sqliteTable{}
}

func pragmaValue(t *testing.T, fingerprint sqliteFingerprint, name string) string {
	t.Helper()
	for _, pragma := range fingerprint.Pragmas {
		if pragma.Name == name {
			if pragma.Value.Type != "integer" && pragma.Value.Type != "text" {
				t.Fatalf("pragma %q has value %+v", name, pragma.Value)
			}
			return pragma.Value.Value
		}
	}
	t.Fatalf("missing pragma %q in %+v", name, fingerprint.Pragmas)
	return ""
}
