package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCollectDoltCanonicalizesPublicSQLSurfaces(t *testing.T) {
	runner := fakeDoltRunner{catalogNullMarkers: true, responses: map[string][][]string{
		"SHOW FULL TABLES": {
			{"Tables_in_beads", "Table_type"},
			{"schema_migrations", "BASE TABLE"},
			{"child_view", "VIEW"},
			{"child", "BASE TABLE"},
		},
		"SHOW CREATE TABLE `child`": {
			{"Table", "Create Table"}, {"child", "CREATE TABLE `child` (`id` int)"},
		},
		"SHOW CREATE TABLE `schema_migrations`": {
			{"Table", "Create Table"}, {"schema_migrations", "CREATE TABLE `schema_migrations` (`version` int, `note` text)"},
		},
		"SHOW CREATE VIEW `child_view`": {
			{"View", "Create View"}, {"child_view", "CREATE VIEW `child_view` AS SELECT `id` FROM `child`"},
		},
		"information_schema.columns": {
			{"table_name", "column_name", "ordinal_position", "column_default", "is_nullable", "data_type", "column_type", "character_maximum_length", "numeric_precision", "numeric_scale", "datetime_precision", "character_set_name", "collation_name", "column_key", "extra", "generation_expression"},
			{"schema_migrations", "note", "2", "", "YES", "text", "text", "", "", "", "", "", "", "", "", ""},
			{"child", "id", "1", "", "NO", "int", "int", "", "10", "0", "", "", "", "PRI", "", ""},
			{"schema_migrations", "version", "1", "", "NO", "int", "int", "", "10", "0", "", "", "", "PRI", "", ""},
		},
		"information_schema.statistics": {
			{"table_name", "non_unique", "index_name", "seq_in_index", "column_name", "collation", "sub_part", "nullable", "index_type", "comment", "index_comment", "is_visible", "expression"},
			{"child", "0", "PRIMARY", "1", "id", "A", "", "", "BTREE", "", "", "YES", ""},
		},
		"information_schema.table_constraints": {
			{"constraint_name", "table_name", "constraint_type", "enforced"},
			{"PRIMARY", "child", "PRIMARY KEY", "YES"},
		},
		"information_schema.key_column_usage": {
			{"constraint_name", "table_name", "column_name", "ordinal_position", "position_in_unique_constraint", "referenced_table_name", "referenced_column_name"},
			{"PRIMARY", "child", "id", "1", "", "", ""},
		},
		"information_schema.referential_constraints": {
			{"constraint_name", "unique_constraint_name", "match_option", "update_rule", "delete_rule", "table_name", "referenced_table_name"},
		},
		"information_schema.triggers": {
			{"trigger_name", "event_manipulation", "event_object_table", "action_statement", "action_timing", "action_orientation", "action_condition", "action_reference_old_table", "action_reference_new_table", "action_reference_old_row", "action_reference_new_row", "sql_mode"},
		},
		"SELECT CASE WHEN `version` IS NULL": {
			{"c000_null", "c000_value", "c001_null", "c001_value"},
			{"0", "2", "1", ""},
			{"0", "1", "0", ""},
		},
	}}

	columns, err := collectDoltCatalog(context.Background(), &runner, doltCatalogQueries[0].name, doltCatalogQueries[0].sql())
	if err != nil {
		t.Fatalf("collect columns catalog: %v", err)
	}
	if !reflect.DeepEqual(columns.Columns[:3], []string{"table_name", "column_name", "ordinal_position"}) {
		t.Fatalf("catalog logical columns = %#v", columns.Columns)
	}

	got, err := collectDolt(context.Background(), &runner)
	if err != nil {
		t.Fatal(err)
	}
	again, err := collectDolt(context.Background(), &runner)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("repeated collection differed:\n%#v\n%#v", got, again)
	}

	if got.Objects[0].Name != "child" || got.Objects[1].Name != "child_view" || got.Objects[2].Name != "schema_migrations" {
		t.Fatalf("objects were not sorted: %#v", got.Objects)
	}
	if got.Objects[1].Create != "CREATE VIEW `child_view` AS SELECT `id` FROM `child`" {
		t.Fatalf("view SHOW CREATE result = %#v", got.Objects[1])
	}
	if !doltSnapshotSorted(got.Catalog) {
		t.Fatalf("catalog snapshots were not canonical: %#v", got.Catalog)
	}
	if len(got.MigrationLedgers) != 1 {
		t.Fatalf("migration ledgers = %#v", got.MigrationLedgers)
	}
	ledger := got.MigrationLedgers[0]
	if !reflect.DeepEqual(ledger.Columns, []string{"version", "note"}) || len(ledger.Rows) != 2 {
		t.Fatalf("migration ledger = %#v", ledger)
	}
	if ledger.Rows[0][0] != (doltValue{Value: "1"}) || ledger.Rows[0][1] != (doltValue{Value: ""}) ||
		ledger.Rows[1][1] != (doltValue{Null: true}) {
		t.Fatalf("migration rows did not preserve empty versus NULL: %#v", ledger.Rows)
	}
	for _, capability := range got.Capabilities {
		if !capability.Supported {
			t.Fatalf("unexpected unsupported capability: %#v", capability)
		}
	}
}

func TestCollectDoltCatalogPreservesNullAndEmptyValues(t *testing.T) {
	runner := fakeDoltRunner{responses: map[string][][]string{
		"FROM information_schema.example": {
			{"c000_null", "optional_value"},
			{"1", ""},
			{"0", ""},
		},
	}}

	snapshot, err := collectDoltCatalog(context.Background(), &runner, "information_schema.example", "SELECT CASE WHEN `optional_value` IS NULL THEN 1 ELSE 0 END AS `c000_null`, `optional_value` FROM information_schema.example")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Columns, []string{"optional_value"}) {
		t.Fatalf("catalog columns = %#v", snapshot.Columns)
	}
	if got, want := snapshot.Rows, [][]doltValue{{{Value: ""}}, {{Null: true}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog rows did not preserve NULL versus empty: got %#v, want %#v", got, want)
	}
}

func TestCollectDoltRecordsUnsupportedOptionalCapabilities(t *testing.T) {
	runner := fakeDoltRunner{responses: map[string][][]string{
		"SHOW FULL TABLES": {{"Tables_in_beads", "Table_type"}},
	}, failures: map[string]error{
		"information_schema.columns":                 errors.New("table not found: information_schema.columns"),
		"information_schema.statistics":              errors.New("table not found: information_schema.statistics"),
		"information_schema.table_constraints":       errors.New("table not found: information_schema.table_constraints"),
		"information_schema.key_column_usage":        errors.New("table not found: information_schema.key_column_usage"),
		"information_schema.referential_constraints": errors.New("table not found: information_schema.referential_constraints"),
		"information_schema.triggers":                errors.New("table not found: information_schema.triggers"),
	}}

	got, err := collectDolt(context.Background(), &runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Catalog) != 0 || len(got.Capabilities) != 6 {
		t.Fatalf("optional capability failures not recorded: %#v", got)
	}
	for _, capability := range got.Capabilities {
		if capability.Supported {
			t.Fatalf("capability failure = %#v", capability)
		}
	}
}

func TestDoltCatalogCapabilityErrorsAreExactAndFailClosed(t *testing.T) {
	source := doltCatalogQueries[1] // information_schema.statistics
	tests := []struct {
		name        string
		failure     error
		unsupported bool
	}{
		{
			name:        "missing relation",
			failure:     errors.New("table not found: information_schema.statistics"),
			unsupported: true,
		},
		{
			name:        "missing selected column",
			failure:     errors.New(`column "is_visible" could not be found in any table in scope`),
			unsupported: true,
		},
		{
			name:    "timeout",
			failure: context.DeadlineExceeded,
		},
		{
			name:    "permission denied",
			failure: errors.New("permission denied"),
		},
		{
			name:    "arbitrary process error",
			failure: errors.New("exit status 1: server disconnected"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isMissingDoltCatalogCapability(test.failure, source); got != test.unsupported {
				t.Fatalf("is missing catalog capability = %t, want %t for %v", got, test.unsupported, test.failure)
			}
		})
	}
}

func TestCollectDoltRejectsMalformedOptionalCatalogCSV(t *testing.T) {
	runner := doltRunnerFunc(func(_ context.Context, query string) ([]byte, error) {
		if query == "SHOW FULL TABLES" {
			return []byte("Tables_in_beads,Table_type\n"), nil
		}
		if strings.Contains(query, "information_schema.statistics") {
			return []byte("unterminated\"\n"), nil
		}
		return []byte("\n"), nil
	})
	if _, err := collectDolt(context.Background(), runner); err == nil {
		t.Fatal("accepted malformed optional catalog CSV as unsupported")
	}
}

func TestCollectDoltNormalizesMigrationApplicationTimestamps(t *testing.T) {
	collect := func(t *testing.T, appliedAt string) doltFingerprint {
		t.Helper()
		runner := fakeDoltRunner{catalogNullMarkers: true, responses: map[string][][]string{
			"SHOW FULL TABLES": {
				{"Tables_in_beads", "Table_type"},
				{"schema_migrations", "BASE TABLE"},
			},
			"SHOW CREATE TABLE": {
				{"Table", "Create Table"},
				{"schema_migrations", "CREATE TABLE `schema_migrations` (`version` int, `applied_at` datetime, `content_hash` text)"},
			},
			"information_schema.columns": {
				{"table_name", "column_name", "ordinal_position"},
				{"schema_migrations", "version", "1"},
				{"schema_migrations", "applied_at", "2"},
				{"schema_migrations", "content_hash", "3"},
			},
			"information_schema.statistics":              {{"table_name"}},
			"information_schema.table_constraints":       {{"constraint_name"}},
			"information_schema.key_column_usage":        {{"constraint_name"}},
			"information_schema.referential_constraints": {{"constraint_name"}},
			"information_schema.triggers":                {{"trigger_name"}},
			"SELECT CASE WHEN": {
				{"c000_null", "c000_value", "c001_null", "c001_value", "c002_null", "c002_value"},
				{"0", "1", "0", appliedAt, "0", "sha256:stable"},
			},
		}}
		fingerprint, err := collectDolt(context.Background(), &runner)
		if err != nil {
			t.Fatal(err)
		}
		return fingerprint
	}

	first := collect(t, "2026-07-28T01:59:21Z")
	second := collect(t, "2026-07-28 02:13:27")
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("migration wall-clock time split one semantic schema:\nfirst: %#v\nsecond: %#v", first, second)
	}
	ledger := first.MigrationLedgers[0]
	if got := ledger.Rows[0][1]; got.Null || got.Value != "<applied>" {
		t.Fatalf("normalized applied_at = %#v, want stable non-null marker", got)
	}
	if got := ledger.Rows[0][2].Value; got != "sha256:stable" {
		t.Fatalf("content hash was not retained: %q", got)
	}
}

func TestRunDoltCSVNormalizesPresentationOnlyHeaderCasing(t *testing.T) {
	runner := fakeDoltRunner{responses: map[string][][]string{
		"SELECT": {{"TABLE_NAME", "Column_Name"}, {"issues", "id"}},
	}}
	records, err := runDoltCSV(context.Background(), &runner, "SELECT")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(records.columns, []string{"table_name", "column_name"}) {
		t.Fatalf("columns = %v", records.columns)
	}
}

func TestDoltIdentifierQuotesUnsafeNames(t *testing.T) {
	if got, want := doltIdentifier("odd`name"), "`odd``name`"; got != want {
		t.Fatalf("doltIdentifier = %q, want %q", got, want)
	}
}

func TestDoltFallbackServerCommandUsesPinnedLoopbackLifecycle(t *testing.T) {
	workspace := t.TempDir()
	fallback := doltServerFallback{
		workspace: workspace,
		doltBin:   "/configured/dolt",
		port:      45123,
	}
	command, err := fallback.serverCommand()
	if err != nil {
		t.Fatal(err)
	}
	if command.Dir != filepath.Join(workspace, ".beads", "dolt") {
		t.Fatalf("server cwd = %q", command.Dir)
	}
	if got, want := command.Args, []string{"/configured/dolt", "sql-server", "-H", "127.0.0.1", "-P", "45123", "--loglevel=warning"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("server args = %q, want %q", got, want)
	}
	if !hasEnv(command.Env, "BEADS_NO_DAEMON=1") || !hasEnv(command.Env, "BEADS_DOLT_AUTO_START=0") {
		t.Fatalf("server environment omitted lifecycle guards: %q", command.Env)
	}
}

func TestDoltFallbackCloseAllowsFailedRestartCleanup(t *testing.T) {
	var fallback *doltServerFallback
	if err := fallback.Close(); err != nil {
		t.Fatalf("close nil fallback after failed restart: %v", err)
	}
}

func TestPinnedDoltRunnerUsesPublicLocalSQL(t *testing.T) {
	workspace := t.TempDir()
	runner := pinnedDoltRunner{
		binary: "/pinned/dolt", workspace: workspace,
		dataDir: filepath.Join(workspace, ".beads", "dolt"), database: "beads",
		environment: []string{"PATH=/usr/bin"},
	}
	command := runner.command(context.Background(), "SELECT 1")
	if command.Dir != workspace {
		t.Fatalf("Dolt cwd = %q, want %q", command.Dir, workspace)
	}
	want := []string{
		"/pinned/dolt", "--data-dir=" + filepath.Join(workspace, ".beads", "dolt"),
		"sql", "-r", "csv", "-q", "USE `beads`; SELECT 1",
	}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("Dolt arguments = %q, want %q", command.Args, want)
	}
	if !hasEnv(command.Env, "DOLT_DISABLE_EVENT_FLUSH=1") {
		t.Fatalf("Dolt environment omitted event guard: %q", command.Env)
	}
}

func TestPinnedDoltRunnerUsesPublicLoopbackSQLWithoutPrompt(t *testing.T) {
	runner := pinnedDoltRunner{
		binary: "/pinned/dolt", workspace: "/workspace",
		host: "127.0.0.1", port: 45123, database: "odd`name",
	}
	command := runner.command(context.Background(), "SHOW FULL TABLES")
	want := []string{
		"/pinned/dolt", "--host=127.0.0.1", "--port=45123", "--no-tls",
		"sql", "-r", "csv", "-q", "USE `odd``name`; SHOW FULL TABLES",
	}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("Dolt arguments = %q, want %q", command.Args, want)
	}
	for _, forbidden := range []string{"--user", "--password"} {
		if strings.Contains(strings.Join(command.Args, "\x00"), forbidden) {
			t.Fatalf("Dolt arguments may prompt for credentials: %q", command.Args)
		}
	}
}

func TestPinnedDoltServerRunnerUsesTheActiveLoopbackEndpoint(t *testing.T) {
	runner := pinnedDoltServerRunner("/pinned/dolt", "/workspace", "beads", []string{"PATH=/bin"}, 45123)
	if runner.dataDir != "" || runner.host != "127.0.0.1" || runner.port != 45123 {
		t.Fatalf("server runner = %#v, want the active loopback endpoint without a data dir", runner)
	}
}

func TestDiscoverDoltServerDatabaseRequiresExactlyOneUserSchema(t *testing.T) {
	for _, test := range []struct {
		name    string
		csv     string
		want    string
		wantErr string
	}{
		{
			name: "one user schema excludes only exact system schemas",
			csv:  "Database\ninformation_schema\nmysql\ndolt\ndolt_workspace\n",
			want: "dolt_workspace",
		},
		{
			name:    "zero user schemas",
			csv:     "Database\ninformation_schema\nmysql\ndolt\n",
			wantErr: "exactly one user database",
		},
		{
			name:    "multiple user schemas",
			csv:     "Database\ninformation_schema\nbeads\nother\n",
			wantErr: "exactly one user database",
		},
		{
			name:    "malformed CSV",
			csv:     "Database\nunterminated\"\n",
			wantErr: "parse CSV",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := doltRunnerFunc(func(_ context.Context, query string) ([]byte, error) {
				if query != "SHOW DATABASES" {
					t.Fatalf("query = %q, want SHOW DATABASES", query)
				}
				return []byte(test.csv), nil
			})
			got, err := discoverDoltServerDatabase(context.Background(), runner)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("discover error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("database = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDiscoverMixedDoltDatabasesUsesPublicSingleDatabaseDiscoveryForEachRoot(t *testing.T) {
	queries := make([]string, 0, 2)
	runner := func(name, database string) doltSQLRunner {
		return doltRunnerFunc(func(_ context.Context, query string) ([]byte, error) {
			queries = append(queries, name+":"+query)
			return []byte("Database\ninformation_schema\n" + database + "\n"), nil
		})
	}

	legacy, embedded, err := discoverMixedDoltDatabases(context.Background(), runner("legacy", "legacy_db"), runner("embedded", "embedded_db"))
	if err != nil {
		t.Fatalf("discover mixed databases: %v", err)
	}
	if legacy != "legacy_db" || embedded != "embedded_db" {
		t.Fatalf("mixed databases = (%q, %q), want one public user database per root", legacy, embedded)
	}
	if got, want := strings.Join(queries, ","), "legacy:SHOW DATABASES,embedded:SHOW DATABASES"; got != want {
		t.Fatalf("public discovery queries = %q, want %q", got, want)
	}
}

func TestDiscoverMixedDoltDatabasesPropagatesRootDiscoveryErrors(t *testing.T) {
	rootErr := errors.New("embedded SHOW DATABASES failed")
	legacy := doltRunnerFunc(func(_ context.Context, _ string) ([]byte, error) {
		return []byte("Database\nlegacy_db\n"), nil
	})
	embedded := doltRunnerFunc(func(_ context.Context, _ string) ([]byte, error) {
		return nil, rootErr
	})

	_, _, err := discoverMixedDoltDatabases(context.Background(), legacy, embedded)
	if !errors.Is(err, rootErr) {
		t.Fatalf("mixed discovery error = %v, want propagated root error", err)
	}
}

func TestCollectServerDoltLayoutIncludesActiveEmbeddedRoot(t *testing.T) {
	primary := emptyDoltFingerprintRunner(false)
	embeddedQueries := []string{}
	embedded := func(database string) doltSQLRunner {
		if database == "" {
			return doltRunnerFunc(func(_ context.Context, query string) ([]byte, error) {
				embeddedQueries = append(embeddedQueries, query)
				return []byte("Database\ninformation_schema\nembedded_db\n"), nil
			})
		}
		if database != "embedded_db" {
			t.Fatalf("embedded database = %q, want embedded_db", database)
		}
		return emptyDoltFingerprintRunner(true)
	}

	layout, err := collectServerDoltLayout(
		context.Background(),
		[]string{"directory:.beads/dolt", "directory:.beads/embeddeddolt", "metadata-dolt-mode:server"},
		primary,
		embedded,
	)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Schema doltFingerprint    `json:"schema"`
		Stores []labeledDoltStore `json:"stores"`
	}
	if err := json.Unmarshal(layout, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Stores) != 2 || decoded.Stores[0].Name != "dolt" || decoded.Stores[1].Name != "embeddeddolt" {
		t.Fatalf("stores = %#v, want canonical dolt and embeddeddolt fingerprints", decoded.Stores)
	}
	if reflect.DeepEqual(decoded.Stores[0].Schema, decoded.Stores[1].Schema) {
		t.Fatalf("root fingerprints are not distinct: %#v", decoded.Stores)
	}
	if !reflect.DeepEqual(decoded.Schema, decoded.Stores[0].Schema) {
		t.Fatalf("top-level schema = %#v, want active server store %#v", decoded.Schema, decoded.Stores[0].Schema)
	}
	if !reflect.DeepEqual(embeddedQueries, []string{"SHOW DATABASES"}) {
		t.Fatalf("embedded discovery queries = %q, want one public SHOW DATABASES", embeddedQueries)
	}
}

func TestCollectServerDoltLayoutPropagatesEitherRootFailure(t *testing.T) {
	primaryErr := errors.New("active server fingerprint failed")
	if _, err := collectServerDoltLayout(
		context.Background(),
		[]string{"directory:.beads/dolt"},
		doltRunnerFunc(func(context.Context, string) ([]byte, error) { return nil, primaryErr }),
		nil,
	); !errors.Is(err, primaryErr) {
		t.Fatalf("primary error = %v, want %v", err, primaryErr)
	}

	embeddedErr := errors.New("embedded fingerprint failed")
	embedded := func(database string) doltSQLRunner {
		if database == "" {
			return doltRunnerFunc(func(context.Context, string) ([]byte, error) {
				return []byte("Database\ninformation_schema\nembedded_db\n"), nil
			})
		}
		return doltRunnerFunc(func(context.Context, string) ([]byte, error) { return nil, embeddedErr })
	}
	if _, err := collectServerDoltLayout(
		context.Background(),
		[]string{"directory:.beads/dolt", "directory:.beads/embeddeddolt"},
		emptyDoltFingerprintRunner(false),
		embedded,
	); !errors.Is(err, embeddedErr) {
		t.Fatalf("embedded error = %v, want %v", err, embeddedErr)
	}
}

func TestCollectMetadataSelectedMultiDatabaseDoltLayoutRetainsEveryDatabase(t *testing.T) {
	queries := []string{}
	runner := func(database string) doltSQLRunner {
		if database == "" {
			return doltRunnerFunc(func(_ context.Context, query string) ([]byte, error) {
				queries = append(queries, query)
				return []byte("Database\ninformation_schema\nbeads_census\nbeads\n"), nil
			})
		}
		switch database {
		case "beads":
			return emptyDoltFingerprintRunner(false)
		case "beads_census":
			return emptyDoltFingerprintRunner(true)
		default:
			t.Fatalf("unexpected database %q", database)
			return nil
		}
	}

	layout, err := collectMetadataSelectedMultiDatabaseDoltLayout(
		context.Background(),
		[]string{"directory:.beads/dolt", "metadata-backend:dolt", "metadata-database:dolt"},
		"beads_census",
		"dolt",
		runner,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Databases []labeledDoltDatabase `json:"databases"`
		Schema    doltFingerprint       `json:"schema"`
		Topology  []string              `json:"topology"`
	}
	if err := json.Unmarshal(layout, &observed); err != nil {
		t.Fatal(err)
	}
	if got, want := len(observed.Databases), 2; got != want {
		t.Fatalf("databases = %#v, want both user schemas", observed.Databases)
	}
	if observed.Databases[0].Name != "beads" || observed.Databases[1].Name != "beads_census" {
		t.Fatalf("databases = %#v, want name-sorted entries", observed.Databases)
	}
	if !reflect.DeepEqual(observed.Schema, observed.Databases[1].Schema) {
		t.Fatalf("top-level schema = %#v, want metadata-selected schema %#v", observed.Schema, observed.Databases[1].Schema)
	}
	if !hasTopologyMarker(observed.Topology, "metadata-dolt-database:beads_census") {
		t.Fatalf("topology = %v, want metadata-selected database marker", observed.Topology)
	}
	if !reflect.DeepEqual(queries, []string{"SHOW DATABASES"}) {
		t.Fatalf("discovery queries = %v, want one SHOW DATABASES", queries)
	}
}

func TestCollectMetadataSelectedMultiDatabaseDualDoltRootLayoutFingerprintsEveryNamedStore(t *testing.T) {
	called := []string{}
	legacy := func(database string) doltSQLRunner {
		if database == "" {
			return doltRunnerFunc(func(context.Context, string) ([]byte, error) {
				called = append(called, "legacy:discover")
				return []byte("Database\ninformation_schema\nbeads\nbeads_census\n"), nil
			})
		}
		return doltRunnerFunc(func(ctx context.Context, query string) ([]byte, error) {
			called = append(called, "legacy:"+database)
			return emptyDoltFingerprintRunner(database == "beads_census").SQLCSV(ctx, query)
		})
	}
	embedded := func(database string) doltSQLRunner {
		if database == "" {
			return doltRunnerFunc(func(context.Context, string) ([]byte, error) {
				called = append(called, "embedded:discover")
				return []byte("Database\ninformation_schema\nbeads_census\n"), nil
			})
		}
		return doltRunnerFunc(func(ctx context.Context, query string) ([]byte, error) {
			called = append(called, "embedded:"+database)
			return emptyDoltFingerprintRunner(false).SQLCSV(ctx, query)
		})
	}

	layout, err := collectMetadataSelectedMultiDatabaseDualDoltRootLayout(
		context.Background(),
		[]string{"directory:.beads/dolt", "directory:.beads/embeddeddolt", "local-version:other-valid", "metadata-backend:dolt"},
		"beads_census", "dolt", legacy, embedded,
	)
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Databases []labeledDoltDatabase `json:"databases"`
		Schema    doltFingerprint       `json:"schema"`
		Stores    []labeledDoltStore    `json:"stores"`
		Topology  []string              `json:"topology"`
	}
	if err := json.Unmarshal(layout, &observed); err != nil {
		t.Fatal(err)
	}
	if got := []string{observed.Databases[0].Name, observed.Databases[1].Name}; !reflect.DeepEqual(got, []string{"beads", "beads_census"}) {
		t.Fatalf("legacy databases = %v, want canonical beads and beads_census", got)
	}
	if got := []string{observed.Stores[0].Name, observed.Stores[1].Name}; !reflect.DeepEqual(got, []string{"dolt", "embeddeddolt"}) {
		t.Fatalf("stores = %v, want canonical dolt and embeddeddolt", got)
	}
	if !reflect.DeepEqual(observed.Schema, observed.Databases[1].Schema) || !reflect.DeepEqual(observed.Schema, observed.Stores[0].Schema) {
		t.Fatalf("top-level schema is not the metadata-selected legacy schema")
	}
	if reflect.DeepEqual(observed.Stores[0].Schema, observed.Stores[1].Schema) {
		t.Fatal("legacy and embedded store fingerprints were not collected independently")
	}
	if !reflect.DeepEqual(observed.Topology, []string{"directory:.beads/dolt", "directory:.beads/embeddeddolt", "local-version:other-valid", "metadata-backend:dolt", "metadata-database:dolt", "metadata-dolt-database:beads_census"}) {
		t.Fatalf("topology = %v, want canonical mixed-root metadata topology", observed.Topology)
	}
	for _, prefix := range []string{"legacy:beads", "legacy:beads_census", "embedded:beads_census"} {
		if !slices.Contains(called, prefix) {
			t.Fatalf("calls = %v, missing independent fingerprint runner %q", called, prefix)
		}
	}
}

func TestCollectMetadataSelectedMultiDatabaseDoltLayoutFingerprintsBeforeRejectingUnexpectedDatabase(t *testing.T) {
	fingerprinted := []string{}
	runner := func(database string) doltSQLRunner {
		if database == "" {
			return doltRunnerFunc(func(context.Context, string) ([]byte, error) {
				return []byte("Database\nbeads\nbeads_census\nother\n"), nil
			})
		}
		return doltRunnerFunc(func(ctx context.Context, query string) ([]byte, error) {
			fingerprinted = append(fingerprinted, database)
			return emptyDoltFingerprintRunner(database == "beads_census").SQLCSV(ctx, query)
		})
	}

	_, err := collectMetadataSelectedMultiDatabaseDoltLayout(
		context.Background(),
		[]string{"directory:.beads/dolt", "metadata-backend:dolt", "metadata-database:dolt"},
		"beads_census",
		"dolt",
		runner,
	)
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("error = %v, want rejected unexpected database", err)
	}
	for _, database := range []string{"beads", "beads_census", "other"} {
		if !slices.Contains(fingerprinted, database) {
			t.Fatalf("fingerprinted databases = %v, missing %q", fingerprinted, database)
		}
	}
}

func TestCollectMetadataSelectedMultiDatabaseDoltLayoutFingerprintsBeforeRejectingMissingSelector(t *testing.T) {
	fingerprinted := []string{}
	runner := func(database string) doltSQLRunner {
		if database == "" {
			return doltRunnerFunc(func(context.Context, string) ([]byte, error) {
				return []byte("Database\nbeads\nbeads_census\n"), nil
			})
		}
		return doltRunnerFunc(func(ctx context.Context, query string) ([]byte, error) {
			fingerprinted = append(fingerprinted, database)
			return emptyDoltFingerprintRunner(database == "beads_census").SQLCSV(ctx, query)
		})
	}

	_, err := collectMetadataSelectedMultiDatabaseDoltLayout(
		context.Background(),
		[]string{"directory:.beads/dolt", "metadata-backend:dolt"},
		"",
		"",
		runner,
	)
	if err == nil || !strings.Contains(err.Error(), "selector") {
		t.Fatalf("error = %v, want missing selector rejection", err)
	}
	for _, database := range []string{"beads", "beads_census"} {
		if !slices.Contains(fingerprinted, database) {
			t.Fatalf("fingerprinted databases = %v, missing %q", fingerprinted, database)
		}
	}
}

func emptyDoltFingerprintRunner(catalogSupported bool) doltSQLRunner {
	responses := map[string][][]string{
		"SHOW FULL TABLES": {{"Tables_in_beads", "Table_type"}},
	}
	failures := make(map[string]error)
	for _, source := range doltCatalogQueries {
		if catalogSupported {
			responses[source.relation] = [][]string{source.columns}
		} else {
			failures[source.relation] = fmt.Errorf("table not found: %s", source.relation)
		}
	}
	return &fakeDoltRunner{responses: responses, failures: failures, catalogNullMarkers: true}
}

func TestPinnedDoltRunnerRemoteSQLCSVExecutesPublicLoopbackCommand(t *testing.T) {
	workspace := t.TempDir()
	binary := filepath.Join(workspace, "dolt")
	script := "#!/bin/sh\nprintf 'answer\\n1\\n'\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := pinnedDoltRunner{
		binary: binary, workspace: workspace,
		host: "127.0.0.1", port: 45123, database: "beads",
	}
	got, err := runner.SQLCSV(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "answer\n1\n"; string(got) != want {
		t.Fatalf("remote public SQL output = %q, want %q", got, want)
	}
}

func TestRequireHistoricalDoltAutoDetectPortsFreeFailsClosed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := requireHistoricalDoltAutoDetectPortsFree(port); err == nil {
		_ = listener.Close()
		t.Fatal("accepted a loopback port visible to historical auto-detection")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := requireHistoricalDoltAutoDetectPortsFree(port); err != nil {
		t.Fatalf("rejected released loopback port: %v", err)
	}
}

func TestDoltFallbackReadinessRejectsAnUnrelatedLoopbackListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	fallback := &doltServerFallback{
		workspace: t.TempDir(),
		doltBin:   "/bin/false",
		port:      listener.Addr().(*net.TCPAddr).Port,
		done:      make(chan error),
	}
	if err := fallback.waitReady(ctx); err == nil {
		t.Fatal("accepted an unrelated TCP listener without pinned Dolt SQL identity")
	}
}

func TestDoltFallbackReadinessChecksChildLivenessBeforeAcceptingEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	done := make(chan error, 1)
	done <- errors.New("server failed")
	fallback := &doltServerFallback{
		workspace: t.TempDir(),
		doltBin:   "/bin/false",
		port:      listener.Addr().(*net.TCPAddr).Port,
		done:      done,
	}
	if err := fallback.waitReady(context.Background()); err == nil {
		t.Fatal("accepted an endpoint after the spawned server exited")
	}
}

func TestObserveFreshV0492ServerRefreshesTheOwnedEndpoint(t *testing.T) {
	binary := os.Getenv("BEADS_CENSUS_V0492_BINARY")
	if binary == "" {
		t.Skip("set BEADS_CENSUS_V0492_BINARY to the authenticated source-built v0.49.2 executable")
	}
	catalog, _, err := readCatalog("../release-catalog.json", false)
	if err != nil {
		t.Fatal(err)
	}
	var entry catalogEntry
	for _, candidate := range catalog.Versions {
		if candidate.Version == "v0.49.2" {
			entry = candidate
			break
		}
	}
	if entry.Version == "" {
		t.Fatal("release catalog lacks v0.49.2")
	}
	acquired, err := recordAcquisition("source-build", entry, binary, "")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := bindFreshBinary(binary, acquired)
	if err != nil {
		t.Fatal(err)
	}
	scenario, ok := freshScenarioByName(freshDoltServerScenario)
	if !ok {
		t.Fatal("fresh server scenario is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctx = withHistoricalBinaryBinding(ctx, binding)
	observed, family, err := observeFreshScenario(ctx, binary, entry, acquired, scenario)
	if err != nil {
		t.Fatal(err)
	}
	if observed.FamilyID == "" || family.ID != observed.FamilyID || family.Mode != "dolt-server" {
		t.Fatalf("observation = %#v, family = %#v", observed, family)
	}
}

func TestDoltFallbackRequiresPinnedRuntimeVersion(t *testing.T) {
	if err := verifyDoltRuntimeVersion("dolt version 2.1.8"); err != nil {
		t.Fatal(err)
	}
	if err := verifyDoltRuntimeVersion("dolt version 2.1.7"); err == nil {
		t.Fatal("accepted unpinned Dolt runtime")
	}
}

func TestPinnedDoltRuntimeRequiresLinuxAMD64AndKnownExecutableDigest(t *testing.T) {
	if err := verifyPinnedDoltRuntimePlatform("linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	if err := verifyPinnedDoltRuntimePlatform("darwin", "arm64"); err == nil {
		t.Fatal("accepted an unsupported Dolt runtime platform")
	}
	if err := verifyPinnedDoltRuntimeDigest(pinnedDoltRuntimeSHA256); err != nil {
		t.Fatal(err)
	}
	if err := verifyPinnedDoltRuntimeDigest("0" + pinnedDoltRuntimeSHA256[1:]); err == nil {
		t.Fatal("accepted an executable with the wrong SHA-256")
	}
}

func TestResolveDoltRuntimeRejectsCorrectVersionWithWrongExecutableDigest(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "dolt")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'dolt version 2.1.8\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveDoltRuntime(context.Background(), binary); err == nil {
		t.Fatal("resolved a correctly-versioned but unpinned executable")
	} else if !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("resolve error = %v, want executable digest rejection", err)
	}
}

func TestCommandCSVOutputExcludesDiagnosticStderr(t *testing.T) {
	command := exec.Command("sh", "-c", `printf 'warning\n' >&2; printf 'name\nvalue\n'`)
	raw, err := commandCSVOutput(command)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), "name\nvalue\n"; got != want {
		t.Fatalf("CSV output = %q, want %q", got, want)
	}
}

func hasEnv(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type fakeDoltRunner struct {
	responses          map[string][][]string
	failures           map[string]error
	catalogNullMarkers bool
}

type doltRunnerFunc func(context.Context, string) ([]byte, error)

func (f doltRunnerFunc) SQLCSV(ctx context.Context, query string) ([]byte, error) {
	return f(ctx, query)
}

func (f *fakeDoltRunner) SQLCSV(_ context.Context, query string) ([]byte, error) {
	for needle, err := range f.failures {
		if strings.Contains(query, needle) {
			return nil, err
		}
	}
	var match string
	for needle := range f.responses {
		if strings.Contains(query, needle) && len(needle) > len(match) {
			match = needle
		}
	}
	if match != "" {
		records := f.responses[match]
		if f.catalogNullMarkers && strings.Contains(query, "information_schema.") && strings.HasPrefix(query, "SELECT CASE WHEN") {
			records = doltCatalogNullMarkerRecords(records)
		}
		var output strings.Builder
		writer := csv.NewWriter(&output)
		if err := writer.WriteAll(records); err != nil {
			return nil, err
		}
		return []byte(output.String()), nil
	}
	return nil, errors.New("unexpected public SQL query: " + query)
}

func doltCatalogNullMarkerRecords(records [][]string) [][]string {
	if len(records) == 0 {
		return nil
	}
	result := make([][]string, len(records))
	result[0] = make([]string, len(records[0])*2)
	for i, column := range records[0] {
		result[0][i*2] = fmt.Sprintf("c%03d_null", i)
		result[0][i*2+1] = column
	}
	for i, record := range records[1:] {
		result[i+1] = make([]string, len(record)*2)
		for j, value := range record {
			result[i+1][j*2] = "0"
			result[i+1][j*2+1] = value
		}
	}
	return result
}
