package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const (
	sealedCensusEvidence = "testdata/runtime-schema-census.json.gz"
	sealedRouteManifest  = "testdata/runtime-schema-routes.json"
	sealedRouteSummary   = "testdata/runtime-schema-routes.md"
)

// TestCheckedArtifactsTotality is the cheap release gate for the authoritative
// census. Fixtures are deliberately sealed by the expensive Docker census job;
// this test only verifies that they still describe every supported family.
func TestCheckedArtifactsTotality(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping complete-census validation in short mode")
	}

	catalogPath := filepath.Join("..", "release-catalog.json")
	if err := verifyArtifacts(catalogPath, sealedCensusEvidence, sealedRouteManifest, sealedRouteSummary, true); err != nil {
		t.Fatal(err)
	}
}

func TestReadValidCensusBytesReturnsValidatedRawAndRejectsNoncanonicalInput(t *testing.T) {
	dir := t.TempDir()
	catalog := artifactCatalog()
	result := artifactTestCensus(t, catalog)
	catalogPath, censusPath := writeArtifactInputs(t, dir, catalog, result)
	raw, err := os.ReadFile(censusPath)
	if err != nil {
		t.Fatal(err)
	}
	_, validated, err := readValidCensusBytes(catalogPath, raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if &validated[0] != &raw[0] {
		t.Fatal("validated census bytes were re-encoded instead of returned unchanged")
	}
	if _, _, err := readValidCensusBytes(catalogPath, append([]byte(" "), raw...), false); err == nil {
		t.Fatal("accepted semantically equivalent but noncanonical census bytes")
	}
}

func TestRouteManifestSeparatesValidatedAndUntrustedCensusEntryPoints(t *testing.T) {
	dir := t.TempDir()
	catalog := artifactCatalog()
	result := artifactTestCensus(t, catalog)
	catalogPath, censusPath := writeArtifactInputs(t, dir, catalog, result)
	validated, _, err := readValidCensus(catalogPath, censusPath, false)
	if err != nil {
		t.Fatal(err)
	}
	fromValidated, err := routeManifestForValidatedCensus(validated)
	if err != nil {
		t.Fatal(err)
	}
	fromUntrusted, err := routeManifestForCensus(validated)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromValidated, fromUntrusted) {
		t.Fatalf("validated and untrusted manifests differ: %#v != %#v", fromValidated, fromUntrusted)
	}

	untrusted := census{Families: []family{{
		ID: "untrusted", Mode: "sqlite",
		Layout: []byte(`{"schema":{},"topology":["database:.beads/beads.db"]}`),
	}}}
	if _, _, err := routeForFamily(untrusted.Families[0]); err == nil {
		t.Fatal("routeForFamily accepted an untrusted incomplete fingerprint")
	}
	if _, err := routeManifestForCensus(untrusted); err == nil {
		t.Fatal("routeManifestForCensus accepted an untrusted incomplete fingerprint")
	}
}

func TestSealArtifactsIsDeterministicAndRoutesEveryFamily(t *testing.T) {
	dir := t.TempDir()
	catalog := artifactCatalog()
	census := artifactTestCensus(t, catalog)
	catalogPath, censusPath := writeArtifactInputs(t, dir, catalog, census)
	firstEvidence, firstRoutes, firstSummary := filepath.Join(dir, "first.gz"), filepath.Join(dir, "first-routes.json"), filepath.Join(dir, "first.md")
	secondEvidence, secondRoutes, secondSummary := filepath.Join(dir, "second.gz"), filepath.Join(dir, "second-routes.json"), filepath.Join(dir, "second.md")

	if err := sealArtifacts(catalogPath, censusPath, firstEvidence, firstRoutes, firstSummary, false); err != nil {
		t.Fatal(err)
	}
	if err := sealArtifacts(catalogPath, censusPath, secondEvidence, secondRoutes, secondSummary, false); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][2]string{{firstEvidence, secondEvidence}, {firstRoutes, secondRoutes}, {firstSummary, secondSummary}} {
		left, err := os.ReadFile(names[0])
		if err != nil {
			t.Fatal(err)
		}
		right, err := os.ReadFile(names[1])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(left, right) {
			t.Fatalf("%s is nondeterministic", filepath.Base(names[0]))
		}
	}
	if err := verifyArtifacts(catalogPath, firstEvidence, firstRoutes, firstSummary, false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(firstRoutes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"basis":"topology-classification"`)) {
		t.Fatalf("route manifest does not identify its evidence basis: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"qualification":"not-executed"`)) {
		t.Fatalf("route manifest does not identify its qualification status: %s", raw)
	}
	summary, err := os.ReadFile(firstSummary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(summary, []byte("# Topology-derived candidate recovery paths (not E2E-qualified)")) ||
		!bytes.Contains(summary, []byte("These routes are topology-derived candidate paths, not E2E-qualified.")) {
		t.Fatalf("route summary lacks its qualification disclaimer: %s", summary)
	}
	if got := strings.Count(string(raw), `"family_id"`); got != len(census.Families) {
		t.Fatalf("route count = %d, want %d", got, len(census.Families))
	}
	for _, route := range []string{"sealed-copy-export-import", "head-init-from-jsonl", "sealed-copy-direct-head-migration", "dual-root-export-union-import"} {
		if !strings.Contains(string(raw), route) {
			t.Fatalf("route manifest lacks %q: %s", route, raw)
		}
	}
	var manifest routeManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, group := range manifest.Groups {
		if group.Route == "head-init-from-jsonl" && len(group.Families) == 1 {
			return
		}
	}
	t.Fatalf("head-init-from-jsonl families = %#v, want exactly one", manifest.Groups)
}

func TestVerifyArtifactsRejectsManifestDriftAndUnknownTopology(t *testing.T) {
	dir := t.TempDir()
	catalog := artifactCatalog()
	census := artifactTestCensus(t, catalog)
	catalogPath, censusPath := writeArtifactInputs(t, dir, catalog, census)
	evidence, routes, summary := filepath.Join(dir, "evidence.gz"), filepath.Join(dir, "routes.json"), filepath.Join(dir, "summary.md")
	if err := sealArtifacts(catalogPath, censusPath, evidence, routes, summary, false); err != nil {
		t.Fatal(err)
	}
	rawRoutes, err := os.ReadFile(routes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(routes, bytes.Replace(rawRoutes, []byte(`"qualification":"not-executed"`), []byte(`"qualification":"e2e-qualified"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyArtifacts(catalogPath, evidence, routes, summary, false); err == nil {
		t.Fatal("accepted a drifted route manifest qualification")
	}
	if err := sealArtifacts(catalogPath, censusPath, evidence, routes, summary, false); err != nil {
		t.Fatal(err)
	}
	rawSummary, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(summary, bytes.Replace(rawSummary, []byte("not E2E-qualified"), []byte("E2E-qualified"), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyArtifacts(catalogPath, evidence, routes, summary, false); err == nil {
		t.Fatal("accepted a drifted route summary qualification disclaimer")
	}
	raw, err := os.ReadFile(censusPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(censusPath, append([]byte(" "), raw...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sealArtifacts(catalogPath, censusPath, evidence, routes, summary, false); err == nil {
		t.Fatal("sealed semantically equivalent but noncanonical census bytes")
	}

	for i := range census.Families {
		if census.Families[i].Mode == "sqlite" {
			census.Families[i].Layout = canonicalTestJSON(t, struct {
				Schema   sqliteFingerprint `json:"schema"`
				Topology []string          `json:"topology"`
			}{
				Schema:   testSQLiteFingerprint("unknown-topology"),
				Topology: []string{"provider:unknown"},
			})
			id, err := familyID(census.Families[i].Mode, census.Families[i].Layout)
			if err != nil {
				t.Fatal(err)
			}
			old := census.Families[i].ID
			census.Families[i].ID = id
			for j := range census.Observations {
				if census.Observations[j].FamilyID == old {
					census.Observations[j].FamilyID = id
				}
			}
			break
		}
	}
	_, err = routeManifestForCensus(census)
	if err == nil || !strings.Contains(err.Error(), "topology") {
		t.Fatalf("unknown topology error = %v", err)
	}
}

func TestRouteManifestRejectsMissingOrEmptyFingerprintPayloads(t *testing.T) {
	for _, test := range []struct {
		name   string
		mode   string
		layout string
	}{
		{
			name:   "SQLite missing collections",
			mode:   "sqlite",
			layout: `{"schema":{},"topology":["database:.beads/beads.db"]}`,
		},
		{
			name:   "SQLite empty active schema",
			mode:   "sqlite",
			layout: `{"schema":{"migration_ledgers":[],"objects":[],"pragmas":[],"tables":[]},"topology":["database:.beads/beads.db"]}`,
		},
		{
			name:   "JSONL missing records",
			mode:   "jsonl",
			layout: `{"dialect":{},"format":"beads-jsonl","topology":["data:.beads/issues.jsonl"]}`,
		},
		{
			name:   "JSONL empty records",
			mode:   "jsonl",
			layout: `{"dialect":{"records":[]},"format":"beads-jsonl","topology":["data:.beads/issues.jsonl"]}`,
		},
		{
			name:   "Dolt missing collections",
			mode:   "dolt-legacy",
			layout: `{"schema":{},"topology":["directory:.beads/dolt"]}`,
		},
		{
			name:   "Dolt empty active schema",
			mode:   "dolt-legacy",
			layout: `{"schema":{"capabilities":[],"catalog":[],"migration_ledgers":[],"objects":[]},"topology":["directory:.beads/dolt"]}`,
		},
		{
			name:   "unknown layout field",
			mode:   "sqlite",
			layout: `{"ignored":true,"schema":{},"topology":["database:.beads/beads.db"]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := routeForFamily(family{Mode: test.mode, Layout: []byte(test.layout)}); err == nil {
				t.Fatal("accepted incomplete fingerprint payload")
			}
		})
	}
}

func TestRouteManifestRejectsInconsistentDoltStoresAndMetadata(t *testing.T) {
	dualTopology := `"topology":["directory:.beads/dolt","directory:.beads/embeddeddolt","metadata-backend:dolt","metadata-dolt-mode:embedded"]`
	cases := []struct {
		name   string
		mode   string
		layout string
	}{
		{"dual missing stores", "dolt-legacy", `{"schema":{},` + dualTopology + `}`},
		{"single root carries stores", "dolt-legacy", `{"schema":{},"stores":[{"name":"dolt"}],"topology":["directory:.beads/dolt","metadata-backend:dolt"]}`},
		{"distinct backend markers", "dolt-legacy", `{"schema":{},"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-backend:mysql"]}`},
		{"distinct mode markers", "dolt-legacy", `{"schema":{},"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-dolt-mode:embedded","metadata-dolt-mode:server"]}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := routeForFamily(family{Mode: test.mode, Layout: []byte(test.layout)})
			if err == nil {
				t.Fatal("accepted inconsistent Dolt layout")
			}
		})
	}
}

func TestRouteManifestRejectsPrimarySQLiteOrJSONLMarkersOnDolt(t *testing.T) {
	schema := string(canonicalTestJSON(t, testDoltFingerprint("primary")))
	stores := `[{"name":"dolt","schema":` + schema + `},{"name":"embeddeddolt","schema":` + schema + `}]`
	for _, test := range []struct {
		name   string
		mode   string
		layout string
	}{
		{"legacy SQLite", "dolt-legacy", `{"schema":` + schema + `,"topology":["database:.beads/beads.db","directory:.beads/dolt"]}`},
		{"server JSONL", "dolt-server", `{"schema":` + schema + `,"topology":["data:.beads/issues.jsonl","directory:.beads/dolt","metadata-dolt-mode:server"]}`},
		{"embedded SQLite", "dolt-embedded", `{"schema":` + schema + `,"topology":["database:.beads/beads.db","directory:.beads/embeddeddolt"]}`},
		{"dual SQLite", "dolt-legacy", `{"schema":` + schema + `,"stores":` + stores + `,"topology":["database:.beads/beads.db","directory:.beads/dolt","directory:.beads/embeddeddolt"]}`},
		{"dual JSONL", "dolt-server", `{"schema":` + schema + `,"stores":` + stores + `,"topology":["data:.beads/issues.jsonl","directory:.beads/dolt","directory:.beads/embeddeddolt","metadata-dolt-mode:server"]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := routeForFamily(family{Mode: test.mode, Layout: []byte(test.layout)}); err == nil {
				t.Fatal("accepted a Dolt layout with a conflicting primary storage marker")
			}
		})
	}
}

func TestRequireDualRootStoresRequiresCompleteConsistentSchemas(t *testing.T) {
	validSchema := string(canonicalTestJSON(t, testDoltFingerprint("valid")))
	validStores := `[{"name":"dolt","schema":` + validSchema + `},{"name":"embeddeddolt","schema":` + validSchema + `}]`
	if err := requireDualRootStores([]byte(validStores), []byte(validSchema)); err != nil {
		t.Fatalf("rejected valid canonical stores: %v", err)
	}

	for _, test := range []struct {
		name   string
		stores string
		schema string
	}{
		{
			name:   "unknown store field",
			stores: `[{"name":"dolt","schema":` + validSchema + `,"ignored":true},{"name":"embeddeddolt","schema":` + validSchema + `}]`,
			schema: validSchema,
		},
		{
			name:   "invalid store schema",
			stores: `[{"name":"dolt","schema":[]},{"name":"embeddeddolt","schema":` + validSchema + `}]`,
			schema: validSchema,
		},
		{
			name:   "missing fingerprint fields",
			stores: `[{"name":"dolt","schema":{}},{"name":"embeddeddolt","schema":{}}]`,
			schema: `{}`,
		},
		{
			name:   "nil fingerprint fields",
			stores: `[{"name":"dolt","schema":{"objects":null,"catalog":null,"migration_ledgers":null,"capabilities":null}},{"name":"embeddeddolt","schema":{"objects":null,"catalog":null,"migration_ledgers":null,"capabilities":null}}]`,
			schema: `{"objects":null,"catalog":null,"migration_ledgers":null,"capabilities":null}`,
		},
		{
			name:   "unordered object names",
			stores: `[{"name":"dolt","schema":{"objects":[{"name":"issues","type":"BASE TABLE","create":"CREATE TABLE issues"},{"name":"comments","type":"BASE TABLE","create":"CREATE TABLE comments"}],"catalog":[],"migration_ledgers":[],"capabilities":[]}},{"name":"embeddeddolt","schema":` + validSchema + `}]`,
			schema: `{"objects":[{"name":"issues","type":"BASE TABLE","create":"CREATE TABLE issues"},{"name":"comments","type":"BASE TABLE","create":"CREATE TABLE comments"}],"catalog":[],"migration_ledgers":[],"capabilities":[]}`,
		},
		{
			name:   "catalog row width differs from columns",
			stores: `[{"name":"dolt","schema":{"objects":[],"catalog":[{"name":"information_schema.columns","columns":["table_name"],"rows":[[]]}],"migration_ledgers":[],"capabilities":[]}},{"name":"embeddeddolt","schema":` + validSchema + `}]`,
			schema: `{"objects":[],"catalog":[{"name":"information_schema.columns","columns":["table_name"],"rows":[[]]}],"migration_ledgers":[],"capabilities":[]}`,
		},
		{
			name:   "unordered catalog names",
			stores: `[{"name":"dolt","schema":{"objects":[],"catalog":[{"name":"z","columns":[],"rows":[]},{"name":"a","columns":[],"rows":[]}],"migration_ledgers":[],"capabilities":[]}},{"name":"embeddeddolt","schema":` + validSchema + `}]`,
			schema: `{"objects":[],"catalog":[{"name":"z","columns":[],"rows":[]},{"name":"a","columns":[],"rows":[]}],"migration_ledgers":[],"capabilities":[]}`,
		},
		{
			name:   "duplicate ledger tables",
			stores: `[{"name":"dolt","schema":{"objects":[],"catalog":[],"migration_ledgers":[{"table":"schema_migrations","columns":[],"rows":[]},{"table":"schema_migrations","columns":[],"rows":[]}],"capabilities":[]}},{"name":"embeddeddolt","schema":` + validSchema + `}]`,
			schema: `{"objects":[],"catalog":[],"migration_ledgers":[{"table":"schema_migrations","columns":[],"rows":[]},{"table":"schema_migrations","columns":[],"rows":[]}],"capabilities":[]}`,
		},
		{
			name:   "unordered capability names",
			stores: `[{"name":"dolt","schema":{"objects":[],"catalog":[],"migration_ledgers":[],"capabilities":[{"name":"z","supported":true},{"name":"a","supported":true}]}},{"name":"embeddeddolt","schema":` + validSchema + `}]`,
			schema: `{"objects":[],"catalog":[],"migration_ledgers":[],"capabilities":[{"name":"z","supported":true},{"name":"a","supported":true}]}`,
		},
		{
			name:   "inconsistent top level schema",
			stores: validStores,
			schema: `{"objects":[{"name":"issues","type":"table","create":"CREATE TABLE issues (id text)"}],"catalog":[],"migration_ledgers":[],"capabilities":[]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := requireDualRootStores([]byte(test.stores), []byte(test.schema)); err == nil {
				t.Fatal("accepted incomplete or inconsistent dual-root stores")
			}
		})
	}
}

func TestRouteManifestAcceptsOnlyConsistentRetainedSQLiteBackups(t *testing.T) {
	schema := string(canonicalTestJSON(t, testSQLiteFingerprint("retained")))
	valid := family{Mode: "sqlite", Layout: []byte(`{"retained_backup_schemas":[` + schema + `],"schema":` + schema + `,"topology":["database:.beads/beads.db","local-version:legacy-server","metadata-backend:sqlite","sqlite-backups:pre-dolt"]}`)}
	_, route, err := routeForFamily(valid)
	if err != nil {
		t.Fatal(err)
	}
	if route != "sealed-copy-export-import" {
		t.Fatalf("route = %q", route)
	}
	for _, layout := range []string{
		`{"schema":{},"topology":["database:.beads/beads.db","sqlite-backups:pre-dolt"]}`,
		`{"retained_backup_schemas":[{}],"schema":{},"topology":["database:.beads/beads.db"]}`,
		`{"retained_backup_schemas":[{},{}],"schema":{},"topology":["database:.beads/beads.db","sqlite-backups:pre-dolt"]}`,
		`{"retained_backup_schemas":[{}],"schema":{},"topology":["database:.beads/beads.db","metadata-backend:mysql","sqlite-backups:pre-dolt"]}`,
	} {
		if _, _, err := routeForFamily(family{Mode: "sqlite", Layout: []byte(layout)}); err == nil {
			t.Fatalf("accepted inconsistent retained-backup layout: %s", layout)
		}
	}
}

func TestRouteForFamilyAcceptsOnlyCanonicalMetadataSelectedMultiDatabaseDolt(t *testing.T) {
	primary := string(canonicalTestJSON(t, testDoltFingerprint("primary")))
	selected := string(canonicalTestJSON(t, testDoltFingerprint("selected")))
	for _, localVersion := range []string{"local-version:absent-or-invalid", "local-version:legacy-server", "local-version:other-valid"} {
		t.Run(localVersion, func(t *testing.T) {
			topology := `["directory:.beads/dolt","` + localVersion + `","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_census"]`
			layout := `{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"schema":` + selected + `,"topology":` + topology + `}`

			_, route, err := routeForFamily(family{Mode: "dolt-legacy", Layout: []byte(layout)})
			if err != nil || route != "multi-database-export-union-import" {
				t.Fatalf("route = %q, err = %v", route, err)
			}
		})
	}
	topology := `["directory:.beads/dolt","local-version:absent-or-invalid","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_census"]`
	for _, invalid := range []string{
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"schema":` + selected + `,"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt"]}`,
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_arbitrary","schema":` + selected + `}],"schema":` + selected + `,"topology":["directory:.beads/dolt","local-version:absent-or-invalid","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_arbitrary"]}`,
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"schema":` + selected + `,"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_census"]}`,
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"schema":` + selected + `,"topology":["directory:.beads/dolt","local-version:absent-or-invalid","local-version:legacy-server","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_census"]}`,
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"schema":` + selected + `,"topology":["directory:.beads/dolt","local-version:absent-or-invalid","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_census","metadata-dolt-mode:"]}`,
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"retained_backup_schemas":[` + string(canonicalTestJSON(t, testSQLiteFingerprint("backup"))) + `],"schema":` + selected + `,"topology":["directory:.beads/dolt","local-version:absent-or-invalid","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_census","sqlite-backups:pre-dolt"]}`,
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"schema":` + primary + `,"topology":` + topology + `}`,
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"schema":` + selected + `,"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_census","sqlite-coexisting:.beads/beads.db"]}`,
		`{"databases":[{"name":"beads","schema":` + primary + `},{"name":"beads_census","schema":` + selected + `}],"schema":` + selected + `,"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-database:beads_"]}`,
	} {
		if _, _, err := routeForFamily(family{Mode: "dolt-legacy", Layout: []byte(invalid)}); err == nil {
			t.Fatalf("accepted invalid multi-database layout: %s", invalid)
		}
	}
}

func TestRouteForFamilyAcceptsCanonicalMultiDatabaseDualDoltRoot(t *testing.T) {
	primary := testDoltFingerprint("primary")
	selected := testDoltFingerprint("selected")
	embedded := testDoltFingerprint("embedded")
	topology := []string{
		"directory:.beads/dolt",
		"directory:.beads/embeddeddolt",
		"local-version:other-valid",
		"metadata-backend:dolt",
		"metadata-database:dolt",
		"metadata-dolt-database:beads_census",
	}
	layout := func(schema doltFingerprint, stores []labeledDoltStore, databases []labeledDoltDatabase, markers []string) json.RawMessage {
		return testDoltLayout(t, schema, nil, markers, stores, databases, nil)
	}
	validStores := []labeledDoltStore{{Name: "dolt", Schema: selected}, {Name: "embeddeddolt", Schema: embedded}}
	validDatabases := []labeledDoltDatabase{{Name: "beads", Schema: primary}, {Name: "beads_census", Schema: selected}}
	valid := layout(selected, validStores, validDatabases, topology)
	_, route, err := routeForFamily(family{Mode: "dolt-legacy", Layout: valid})
	if err != nil || route != "dual-root-export-union-import" {
		t.Fatalf("route = %q, err = %v", route, err)
	}

	for _, test := range []struct {
		name   string
		layout json.RawMessage
	}{
		{name: "database cardinality", layout: layout(selected, validStores, append(validDatabases, labeledDoltDatabase{Name: "other", Schema: primary}), topology)},
		{name: "database name", layout: layout(selected, validStores, []labeledDoltDatabase{{Name: "other", Schema: primary}, {Name: "beads_census", Schema: selected}}, topology)},
		{name: "selector", layout: layout(selected, validStores, validDatabases, []string{"directory:.beads/dolt", "directory:.beads/embeddeddolt", "local-version:other-valid", "metadata-backend:dolt", "metadata-database:dolt", "metadata-dolt-database:beads"})},
		{name: "topology", layout: layout(selected, validStores, validDatabases, []string{"directory:.beads/dolt", "local-version:other-valid", "metadata-backend:dolt", "metadata-database:dolt", "metadata-dolt-database:beads_census"})},
		{name: "inactive top-level schema", layout: layout(doltFingerprint{Objects: []doltObject{}, Catalog: []doltCatalogSnapshot{}, MigrationLedgers: []doltMigrationLedger{}, Capabilities: []doltCapability{}}, validStores, validDatabases, topology)},
		{name: "store cardinality", layout: layout(selected, validStores[:1], validDatabases, topology)},
		{name: "missing databases", layout: layout(selected, validStores, nil, topology)},
		{name: "top-level mismatch", layout: layout(primary, validStores, validDatabases, topology)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := routeForFamily(family{Mode: "dolt-legacy", Layout: test.layout}); err == nil {
				t.Fatal("accepted a non-canonical multi-database dual-root topology")
			}
		})
	}
}

func TestRouteForFamilyAcceptsCanonicalEmptyDoltWithCoexistingSQLite(t *testing.T) {
	for _, test := range []struct {
		mode         string
		localVersion string
	}{
		{mode: "dolt-legacy", localVersion: "local-version:absent-or-invalid"},
		{mode: "dolt-legacy", localVersion: "local-version:legacy-server"},
		{mode: "dolt-legacy", localVersion: "local-version:other-valid"},
		{mode: "dolt-server", localVersion: "local-version:absent-or-invalid"},
		{mode: "dolt-server", localVersion: "local-version:legacy-server"},
		{mode: "dolt-server", localVersion: "local-version:other-valid"},
	} {
		t.Run(test.mode+"/"+test.localVersion, func(t *testing.T) {
			topology := testCanonicalEmptyDoltTopology(test.mode)
			for index, marker := range topology {
				if marker == "local-version:absent-or-invalid" {
					topology[index] = test.localVersion
				}
			}
			layout := testDoltLayout(t,
				testCanonicalEmptyDoltFingerprint(),
				ptr(testSQLiteFingerprint("coexisting")),
				topology,
				nil, nil, nil,
			)
			if err := validateFamilyFingerprintPayload(family{Mode: test.mode, Layout: layout}); err != nil {
				t.Fatalf("payload validation: %v", err)
			}
			_, route, err := routeForFamily(family{Mode: test.mode, Layout: layout})
			if err != nil {
				t.Fatal(err)
			}
			if route != "dual-root-export-union-import" {
				t.Fatalf("route = %q, want dual-root-export-union-import", route)
			}
		})
	}
}

func TestCanonicalEmptyDoltWithCoexistingSQLiteRejectsNearMisses(t *testing.T) {
	canonical := testCanonicalEmptyDoltFingerprint()
	activeSQLite := testSQLiteFingerprint("coexisting")
	inactiveSQLite := sqliteFingerprint{
		Objects: []sqliteSchemaObject{}, Tables: []sqliteTable{}, Pragmas: []sqlitePragma{}, MigrationLedgers: []sqliteMigrationLedger{},
	}
	missingCapability := testCanonicalEmptyDoltFingerprint()
	missingCapability.Capabilities = missingCapability.Capabilities[1:]
	unsupportedCapability := testCanonicalEmptyDoltFingerprint()
	unsupportedCapability.Capabilities[0].Supported = false
	wrongCatalogColumns := testCanonicalEmptyDoltFingerprint()
	wrongCatalogColumns.Catalog[0].Columns = append(wrongCatalogColumns.Catalog[0].Columns, "unexpected")
	nonemptyCatalog := testCanonicalEmptyDoltFingerprint()
	nonemptyCatalog.Catalog[0].Rows = [][]doltValue{make([]doltValue, len(nonemptyCatalog.Catalog[0].Columns))}
	nonemptyLedger := testCanonicalEmptyDoltFingerprint()
	nonemptyLedger.MigrationLedgers = []doltMigrationLedger{{Table: "schema_migrations", Columns: []string{"version"}, Rows: [][]doltValue{{{Value: "1"}}}}}
	malformedObject := testCanonicalEmptyDoltFingerprint()
	malformedObject.Objects = []doltObject{{}}

	for _, test := range []struct {
		name      string
		mode      string
		schema    doltFingerprint
		sqlite    *sqliteFingerprint
		topology  []string
		stores    []labeledDoltStore
		databases []labeledDoltDatabase
		backups   []sqliteFingerprint
	}{
		{name: "missing topology marker", mode: "dolt-server", schema: canonical, sqlite: &activeSQLite, topology: []string{"directory:.beads/dolt", "local-version:absent-or-invalid", "metadata-backend:dolt", "metadata-database:dolt", "sqlite-coexisting:.beads/beads.db"}},
		{name: "changed topology marker", mode: "dolt-server", schema: canonical, sqlite: &activeSQLite, topology: []string{"directory:.beads/dolt", "local-version:absent-or-invalid", "metadata-backend:dolt", "metadata-database:dolt", "metadata-dolt-mode:embedded", "sqlite-coexisting:.beads/beads.db"}},
		{name: "extra topology marker", mode: "dolt-server", schema: canonical, sqlite: &activeSQLite, topology: []string{"directory:.beads/dolt", "local-version:absent-or-invalid", "metadata-backend:dolt", "metadata-database:dolt", "metadata-dolt-database:beads", "metadata-dolt-mode:server", "sqlite-coexisting:.beads/beads.db"}},
		{name: "legacy with server topology", mode: "dolt-legacy", schema: canonical, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "server with legacy topology", mode: "dolt-server", schema: canonical, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-legacy")},
		{name: "legacy missing local-version marker", mode: "dolt-legacy", schema: canonical, sqlite: &activeSQLite, topology: []string{"directory:.beads/dolt", "metadata-backend:dolt", "metadata-database:dolt", "sqlite-coexisting:.beads/beads.db"}},
		{name: "legacy legacy-server with Dolt mode marker", mode: "dolt-legacy", schema: canonical, sqlite: &activeSQLite, topology: []string{"directory:.beads/dolt", "local-version:legacy-server", "metadata-backend:dolt", "metadata-database:dolt", "metadata-dolt-mode:server", "sqlite-coexisting:.beads/beads.db"}},
		{name: "legacy other-valid with Dolt mode marker", mode: "dolt-legacy", schema: canonical, sqlite: &activeSQLite, topology: []string{"directory:.beads/dolt", "local-version:other-valid", "metadata-backend:dolt", "metadata-database:dolt", "metadata-dolt-mode:server", "sqlite-coexisting:.beads/beads.db"}},
		{name: "server legacy-server without Dolt mode marker", mode: "dolt-server", schema: canonical, sqlite: &activeSQLite, topology: []string{"directory:.beads/dolt", "local-version:legacy-server", "metadata-backend:dolt", "metadata-database:dolt", "sqlite-coexisting:.beads/beads.db"}},
		{name: "legacy beads.db selector", mode: "dolt-legacy", schema: canonical, sqlite: &activeSQLite, topology: []string{"directory:.beads/dolt", "local-version:absent-or-invalid", "metadata-backend:dolt", "metadata-database:beads.db", "sqlite-coexisting:.beads/beads.db"}},
		{name: "embedded mode", mode: "dolt-embedded", schema: canonical, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "missing coexisting SQLite", mode: "dolt-server", schema: canonical, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "inactive coexisting SQLite", mode: "dolt-server", schema: canonical, sqlite: &inactiveSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "incomplete capabilities", mode: "dolt-server", schema: missingCapability, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "unsupported capability", mode: "dolt-server", schema: unsupportedCapability, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "wrong catalog columns", mode: "dolt-server", schema: wrongCatalogColumns, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "nonempty catalog", mode: "dolt-server", schema: nonemptyCatalog, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "nonempty migration ledger", mode: "dolt-server", schema: nonemptyLedger, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "malformed object", mode: "dolt-server", schema: malformedObject, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
		{name: "stores", mode: "dolt-server", schema: canonical, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server"), stores: []labeledDoltStore{{Name: "dolt", Schema: canonical}}},
		{name: "databases", mode: "dolt-server", schema: canonical, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server"), databases: []labeledDoltDatabase{{Name: "beads", Schema: canonical}}},
		{name: "retained backup", mode: "dolt-server", schema: canonical, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server"), backups: []sqliteFingerprint{testSQLiteFingerprint("backup")}},
		{name: "empty capabilities", mode: "dolt-server", schema: doltFingerprint{Objects: []doltObject{}, Catalog: []doltCatalogSnapshot{}, MigrationLedgers: []doltMigrationLedger{}, Capabilities: []doltCapability{}}, sqlite: &activeSQLite, topology: testCanonicalEmptyDoltTopology("dolt-server")},
	} {
		t.Run(test.name, func(t *testing.T) {
			layout := testDoltLayout(t, test.schema, test.sqlite, test.topology, test.stores, test.databases, test.backups)
			if err := validateFamilyFingerprintPayload(family{Mode: test.mode, Layout: layout}); err == nil {
				t.Fatal("accepted a non-canonical empty server Dolt layout")
			}
		})
	}
}

func TestActiveDoltFingerprintRequiresKnownCatalogProjections(t *testing.T) {
	t.Run("canonical catalog", func(t *testing.T) {
		schema := testActiveDoltFingerprint()
		if !validDoltFingerprint(schema) || !validActiveDoltFingerprint(schema) {
			t.Fatal("rejected canonical active Dolt catalog")
		}
	})
	t.Run("unavailable known catalog", func(t *testing.T) {
		schema := testActiveDoltFingerprint()
		missing := schema.Catalog[0].Name
		schema.Catalog = append([]doltCatalogSnapshot(nil), schema.Catalog[1:]...)
		for index := range schema.Capabilities {
			if schema.Capabilities[index].Name == missing {
				schema.Capabilities[index].Supported = false
			}
		}
		if !validDoltFingerprint(schema) || !validActiveDoltFingerprint(schema) {
			t.Fatal("rejected a known unavailable Dolt catalog capability")
		}
	})
	t.Run("unknown sorted catalog", func(t *testing.T) {
		schema := testActiveDoltFingerprint()
		schema.Catalog = append(schema.Catalog, doltCatalogSnapshot{
			Name: "information_schema.unknown", Columns: []string{}, Rows: [][]doltValue{},
		})
		sort.Slice(schema.Catalog, func(i, j int) bool { return schema.Catalog[i].Name < schema.Catalog[j].Name })
		if validDoltFingerprint(schema) {
			t.Fatal("generic validator accepted an unknown sorted Dolt catalog snapshot")
		}
	})
	t.Run("consistently permuted catalog projection", func(t *testing.T) {
		schema := testActiveDoltFingerprint()
		var snapshot *doltCatalogSnapshot
		for index := range schema.Catalog {
			if schema.Catalog[index].Name == "information_schema.columns" {
				snapshot = &schema.Catalog[index]
				break
			}
		}
		if snapshot == nil || len(snapshot.Columns) < 2 {
			t.Fatal("test setup lacks information_schema.columns")
		}
		first := make([]doltValue, len(snapshot.Columns))
		second := make([]doltValue, len(snapshot.Columns))
		first[0], first[1] = doltValue{Value: "a"}, doltValue{Value: "z"}
		second[0], second[1] = doltValue{Value: "b"}, doltValue{Value: "a"}
		snapshot.Rows = [][]doltValue{first, second}
		snapshot.Columns[0], snapshot.Columns[1] = snapshot.Columns[1], snapshot.Columns[0]
		for _, row := range snapshot.Rows {
			row[0], row[1] = row[1], row[0]
		}
		sort.SliceStable(snapshot.Rows, func(i, j int) bool {
			return compareDoltRows(snapshot.Rows[i], snapshot.Rows[j]) < 0
		})
		if validDoltFingerprint(schema) {
			t.Fatal("generic validator accepted a consistently permuted and re-sorted Dolt catalog projection")
		}
	})
	t.Run("missing supported catalog", func(t *testing.T) {
		schema := testActiveDoltFingerprint()
		schema.Catalog = append([]doltCatalogSnapshot(nil), schema.Catalog[1:]...)
		if validActiveDoltFingerprint(schema) {
			t.Fatal("accepted a missing supported Dolt catalog snapshot")
		}
	})
	t.Run("unknown capability", func(t *testing.T) {
		schema := testActiveDoltFingerprint()
		schema.Capabilities[0].Name = "information_schema.unknown"
		sort.Slice(schema.Capabilities, func(i, j int) bool {
			return schema.Capabilities[i].Name < schema.Capabilities[j].Name
		})
		if validActiveDoltFingerprint(schema) {
			t.Fatal("accepted an unknown Dolt catalog capability")
		}
	})
}

func TestFingerprintValidatorsRejectCollectorOrderPermutations(t *testing.T) {
	t.Run("Dolt catalog rows", func(t *testing.T) {
		schema := testOrderedDoltFingerprint()
		schema.Catalog[0].Rows[0], schema.Catalog[0].Rows[1] = schema.Catalog[0].Rows[1], schema.Catalog[0].Rows[0]
		if validDoltFingerprint(schema) {
			t.Fatal("accepted Dolt catalog rows outside collector order")
		}
	})
	t.Run("Dolt migration ledger rows", func(t *testing.T) {
		schema := testOrderedDoltFingerprint()
		schema.MigrationLedgers[0].Rows[0], schema.MigrationLedgers[0].Rows[1] = schema.MigrationLedgers[0].Rows[1], schema.MigrationLedgers[0].Rows[0]
		if validDoltFingerprint(schema) {
			t.Fatal("accepted Dolt migration ledger rows outside collector order")
		}
	})
	t.Run("Dolt migration ledger projection", func(t *testing.T) {
		schema := testOrderedDoltFingerprint()
		schema.MigrationLedgers[0].Columns[0], schema.MigrationLedgers[0].Columns[1] = schema.MigrationLedgers[0].Columns[1], schema.MigrationLedgers[0].Columns[0]
		for _, row := range schema.MigrationLedgers[0].Rows {
			row[0], row[1] = row[1], row[0]
		}
		if validDoltFingerprint(schema) {
			t.Fatal("accepted Dolt migration ledger projection outside collector order")
		}
	})
	t.Run("Dolt migration ledger without catalog projection", func(t *testing.T) {
		schema := testOrderedDoltFingerprint()
		schema.Catalog = []doltCatalogSnapshot{}
		if validDoltFingerprint(schema) {
			t.Fatal("accepted Dolt migration ledger without an information_schema.columns projection")
		}
	})
	t.Run("Dolt migration ledger payloads exactly match ledger tables", func(t *testing.T) {
		schema := testOrderedDoltFingerprint()
		if !validDoltFingerprint(schema) {
			t.Fatal("rejected the complete Dolt migration ledger payload")
		}

		schema.MigrationLedgers = []doltMigrationLedger{}
		if validDoltFingerprint(schema) {
			t.Fatal("accepted a Dolt migration ledger table without its payload")
		}

		schema = testOrderedDoltFingerprint()
		extraRow := append([]doltValue(nil), schema.Catalog[0].Rows[0]...)
		extraRow[columnPositions(schema.Catalog[0].Columns)["table_name"]] = doltValue{Value: "other_migrations"}
		schema.Catalog[0].Rows = append(schema.Catalog[0].Rows, extraRow)
		sort.SliceStable(schema.Catalog[0].Rows, func(i, j int) bool {
			return compareDoltRows(schema.Catalog[0].Rows[i], schema.Catalog[0].Rows[j]) < 0
		})
		schema.MigrationLedgers = append([]doltMigrationLedger{{
			Table: "other_migrations", Columns: []string{"version", "applied_at"}, Rows: [][]doltValue{
				{{Value: "1"}, {Value: "2024-01-01"}},
			},
		}}, schema.MigrationLedgers...)
		if validDoltFingerprint(schema) {
			t.Fatal("accepted a Dolt migration ledger payload without its ledger table")
		}
	})
	t.Run("Dolt duplicate migration ledger rows", func(t *testing.T) {
		schema := testOrderedDoltFingerprint()
		schema.MigrationLedgers[0].Rows = [][]doltValue{
			schema.MigrationLedgers[0].Rows[0], schema.MigrationLedgers[0].Rows[0],
		}
		if !validDoltFingerprint(schema) {
			t.Fatal("rejected duplicate Dolt migration ledger rows in collector order")
		}
	})

	for _, test := range []struct {
		name   string
		mutate func(*sqliteFingerprint)
	}{
		{
			name: "SQLite table columns",
			mutate: func(schema *sqliteFingerprint) {
				schema.Tables[0].Columns[0], schema.Tables[0].Columns[1] = schema.Tables[0].Columns[1], schema.Tables[0].Columns[0]
			},
		},
		{
			name: "SQLite duplicate table column sort keys",
			mutate: func(schema *sqliteFingerprint) {
				columns := schema.Tables[0].Columns
				schema.Tables[0].Columns = []sqliteColumn{columns[0], columns[0], columns[1]}
				schema.MigrationLedgers = []sqliteMigrationLedger{}
			},
		},
		{
			name: "SQLite foreign keys",
			mutate: func(schema *sqliteFingerprint) {
				schema.Tables[0].ForeignKeys[0], schema.Tables[0].ForeignKeys[1] = schema.Tables[0].ForeignKeys[1], schema.Tables[0].ForeignKeys[0]
			},
		},
		{
			name: "SQLite duplicate foreign key sort keys",
			mutate: func(schema *sqliteFingerprint) {
				keys := schema.Tables[0].ForeignKeys
				schema.Tables[0].ForeignKeys = []sqliteForeignKey{keys[0], keys[0], keys[1]}
			},
		},
		{
			name: "SQLite indexes",
			mutate: func(schema *sqliteFingerprint) {
				schema.Tables[0].Indexes[0], schema.Tables[0].Indexes[1] = schema.Tables[0].Indexes[1], schema.Tables[0].Indexes[0]
			},
		},
		{
			name: "SQLite duplicate index sort keys",
			mutate: func(schema *sqliteFingerprint) {
				indexes := schema.Tables[0].Indexes
				schema.Tables[0].Indexes = []sqliteIndex{indexes[0], indexes[0], indexes[1]}
			},
		},
		{
			name: "SQLite index columns",
			mutate: func(schema *sqliteFingerprint) {
				schema.Tables[0].Indexes[0].Columns[0], schema.Tables[0].Indexes[0].Columns[1] = schema.Tables[0].Indexes[0].Columns[1], schema.Tables[0].Indexes[0].Columns[0]
			},
		},
		{
			name: "SQLite duplicate index column sort keys",
			mutate: func(schema *sqliteFingerprint) {
				columns := schema.Tables[0].Indexes[0].Columns
				schema.Tables[0].Indexes[0].Columns = []sqliteIndexInfo{columns[0], columns[0], columns[1]}
			},
		},
		{
			name: "SQLite migration ledger rows",
			mutate: func(schema *sqliteFingerprint) {
				schema.MigrationLedgers[0].Rows[0], schema.MigrationLedgers[0].Rows[1] = schema.MigrationLedgers[0].Rows[1], schema.MigrationLedgers[0].Rows[0]
			},
		},
		{
			name: "SQLite migration ledger projection",
			mutate: func(schema *sqliteFingerprint) {
				schema.MigrationLedgers[0].Columns[0], schema.MigrationLedgers[0].Columns[1] = schema.MigrationLedgers[0].Columns[1], schema.MigrationLedgers[0].Columns[0]
				for _, row := range schema.MigrationLedgers[0].Rows {
					row[0], row[1] = row[1], row[0]
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema := testOrderedSQLiteFingerprint()
			test.mutate(&schema)
			if validActiveSQLiteFingerprint(schema) {
				t.Fatal("accepted SQLite data outside collector order")
			}
		})
	}
	t.Run("SQLite duplicate migration ledger rows", func(t *testing.T) {
		schema := testOrderedSQLiteFingerprint()
		schema.MigrationLedgers[0].Rows = [][]sqliteValue{
			schema.MigrationLedgers[0].Rows[0], schema.MigrationLedgers[0].Rows[0],
		}
		if !validActiveSQLiteFingerprint(schema) {
			t.Fatal("rejected duplicate SQLite migration ledger rows in collector order")
		}
	})
}

func testOrderedDoltFingerprint() doltFingerprint {
	columns := append([]string(nil), doltCatalogQueries[0].columns...)
	positions := columnPositions(columns)
	catalogRow := func(column, ordinal string) []doltValue {
		row := make([]doltValue, len(columns))
		row[positions["table_name"]] = doltValue{Value: "schema_migrations"}
		row[positions["column_name"]] = doltValue{Value: column}
		row[positions["ordinal_position"]] = doltValue{Value: ordinal}
		return row
	}
	return doltFingerprint{
		Objects: []doltObject{{
			Name: "schema_migrations", Type: "BASE TABLE", Create: "CREATE TABLE schema_migrations (version TEXT, applied_at TEXT)",
		}},
		Catalog: []doltCatalogSnapshot{{
			Name:    "information_schema.columns",
			Columns: columns,
			Rows: [][]doltValue{
				catalogRow("applied_at", "2"),
				catalogRow("version", "1"),
			},
		}},
		MigrationLedgers: []doltMigrationLedger{{
			Table:   "schema_migrations",
			Columns: []string{"version", "applied_at"},
			Rows: [][]doltValue{
				{{Value: "1"}, {Value: "2024-01-01"}},
				{{Value: "2"}, {Value: "2024-01-02"}},
			},
		}},
		Capabilities: []doltCapability{},
	}
}

func testOrderedSQLiteFingerprint() sqliteFingerprint {
	statement := "CREATE TABLE schema_migrations (zeta INTEGER, alpha TEXT)"
	pragmas := make([]sqlitePragma, len(fingerprintPragmas))
	for index, name := range fingerprintPragmas {
		pragmas[index] = sqlitePragma{Name: name, Value: sqliteValue{Type: "integer", Value: "0"}}
	}
	return sqliteFingerprint{
		Objects: []sqliteSchemaObject{{Type: "table", Name: "schema_migrations", Table: "schema_migrations", SQL: &statement}},
		Tables: []sqliteTable{{
			Name: "schema_migrations",
			Columns: []sqliteColumn{
				{CID: 0, Name: "zeta", DeclaredType: "INTEGER"},
				{CID: 1, Name: "alpha", DeclaredType: "TEXT"},
			},
			ForeignKeys: []sqliteForeignKey{
				{ID: 0, Sequence: 0, Table: "parent_a", From: "zeta"},
				{ID: 1, Sequence: 0, Table: "parent_b", From: "alpha"},
			},
			Indexes: []sqliteIndex{
				{Name: "alpha_index", Sequence: 1, Columns: []sqliteIndexInfo{{Sequence: 0, CID: 0}, {Sequence: 1, CID: 1}}},
				{Name: "zeta_index", Sequence: 0, Columns: []sqliteIndexInfo{}},
			},
		}},
		Pragmas: pragmas,
		MigrationLedgers: []sqliteMigrationLedger{{
			Table:   "schema_migrations",
			Columns: []string{"zeta", "alpha"},
			Rows: [][]sqliteValue{
				{{Type: "integer", Value: "1"}, {Type: "text", Value: "b"}},
				{{Type: "integer", Value: "2"}, {Type: "text", Value: "a"}},
			},
		}},
	}
}

func testCanonicalEmptyDoltFingerprint() doltFingerprint {
	catalog := make([]doltCatalogSnapshot, 0, len(doltCatalogQueries))
	capabilities := make([]doltCapability, 0, len(doltCatalogQueries))
	for _, query := range doltCatalogQueries {
		catalog = append(catalog, doltCatalogSnapshot{Name: query.name, Columns: append([]string(nil), query.columns...), Rows: [][]doltValue{}})
		capabilities = append(capabilities, doltCapability{Name: query.name, Supported: true})
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })
	return doltFingerprint{Objects: []doltObject{}, Catalog: catalog, MigrationLedgers: []doltMigrationLedger{}, Capabilities: capabilities}
}

func testActiveDoltFingerprint() doltFingerprint {
	schema := testCanonicalEmptyDoltFingerprint()
	schema.Objects = []doltObject{{
		Name: "issues", Type: "BASE TABLE", Create: "CREATE TABLE issues (id TEXT PRIMARY KEY)",
	}}
	return schema
}

func testCanonicalEmptyDoltTopology(mode string) []string {
	topology := []string{
		"directory:.beads/dolt",
		"local-version:absent-or-invalid",
		"metadata-backend:dolt",
		"metadata-database:dolt",
		"sqlite-coexisting:.beads/beads.db",
	}
	if mode == "dolt-server" {
		topology = append(topology, "metadata-dolt-mode:server")
		sort.Strings(topology)
	}
	return topology
}

func testDoltLayout(t *testing.T, schema doltFingerprint, sqlite *sqliteFingerprint, topology []string, stores []labeledDoltStore, databases []labeledDoltDatabase, backups []sqliteFingerprint) json.RawMessage {
	t.Helper()
	return canonicalTestJSON(t, struct {
		CoexistingSQLiteSchema *sqliteFingerprint    `json:"coexisting_sqlite_schema,omitempty"`
		Databases              []labeledDoltDatabase `json:"databases,omitempty"`
		RetainedBackupSchemas  []sqliteFingerprint   `json:"retained_backup_schemas,omitempty"`
		Schema                 doltFingerprint       `json:"schema"`
		Stores                 []labeledDoltStore    `json:"stores,omitempty"`
		Topology               []string              `json:"topology"`
	}{
		CoexistingSQLiteSchema: sqlite,
		Databases:              databases,
		RetainedBackupSchemas:  backups,
		Schema:                 schema,
		Stores:                 stores,
		Topology:               topology,
	})
}

func ptr[T any](value T) *T { return &value }

func TestRouteManifestAcceptsRetainedSQLiteBackupsForDoltFamilies(t *testing.T) {
	backup := `[` + string(canonicalTestJSON(t, testSQLiteFingerprint("backup"))) + `]`
	for _, test := range []struct {
		mode     string
		topology string
		route    string
		dual     bool
	}{
		{"dolt-legacy", `"topology":["directory:.beads/dolt","sqlite-backups:pre-dolt"]`, "sealed-copy-export-import", false},
		{"dolt-server", `"topology":["directory:.beads/dolt","metadata-dolt-mode:server","sqlite-backups:pre-dolt"]`, "sealed-copy-export-import", false},
		{"dolt-embedded", `"topology":["directory:.beads/embeddeddolt","sqlite-backups:pre-dolt"]`, "sealed-copy-direct-head-migration", false},
		{"dolt-legacy", `"topology":["directory:.beads/dolt","directory:.beads/embeddeddolt","sqlite-backups:pre-dolt"]`, "dual-root-export-union-import", true},
	} {
		t.Run(test.mode+test.route, func(t *testing.T) {
			schema := string(canonicalTestJSON(t, testDoltFingerprint("retained")))
			stores := ""
			if test.dual {
				stores = `,"stores":[{"name":"dolt","schema":` + schema + `},{"name":"embeddeddolt","schema":` + schema + `}]`
			}
			layout := `{"retained_backup_schemas":` + backup + `,"schema":` + schema + stores + `,` + test.topology + `}`
			_, route, err := routeForFamily(family{Mode: test.mode, Layout: []byte(layout)})
			if err != nil {
				t.Fatal(err)
			}
			if route != test.route {
				t.Fatalf("route = %q, want %q", route, test.route)
			}
		})
	}
	for _, layout := range []string{
		`{"schema":{},"topology":["directory:.beads/dolt","sqlite-backups:pre-dolt"]}`,
		`{"retained_backup_schemas":[{"objects":[],"tables":[],"pragmas":[],"migration_ledgers":[]}],"schema":{},"topology":["directory:.beads/dolt"]}`,
	} {
		if _, _, err := routeForFamily(family{Mode: "dolt-legacy", Layout: []byte(layout)}); err == nil {
			t.Fatalf("accepted inconsistent Dolt retained-backup layout: %s", layout)
		}
	}
}

func TestRouteManifestRequiresAndUnionsCoexistingSQLite(t *testing.T) {
	doltSchema := string(canonicalTestJSON(t, testDoltFingerprint("primary")))
	sqliteSchema := string(canonicalTestJSON(t, testSQLiteFingerprint("coexisting")))
	topology := `"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","sqlite-coexisting:.beads/beads.db"]`
	layout := `{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,` + topology + `}`

	_, route, err := routeForFamily(family{Mode: "dolt-legacy", Layout: []byte(layout)})
	if err != nil {
		t.Fatal(err)
	}
	if route != "dual-root-export-union-import" {
		t.Fatalf("route = %q, want dual-root-export-union-import", route)
	}
	backwardTopology := `"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:beads.db","sqlite-coexisting:.beads/beads.db"]`
	backwardLayout := `{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,` + backwardTopology + `}`
	if _, route, err = routeForFamily(family{Mode: "dolt-legacy", Layout: []byte(backwardLayout)}); err != nil || route != "dual-root-export-union-import" {
		t.Fatalf("backward-compatible selector route = %q, error = %v", route, err)
	}
	serverTopology := `"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-mode:server","sqlite-coexisting:.beads/beads.db"]`
	serverLayout := `{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,` + serverTopology + `}`
	_, route, err = routeForFamily(family{Mode: "dolt-server", Layout: []byte(serverLayout)})
	if err != nil {
		t.Fatal(err)
	}
	if route != "dual-root-export-union-import" {
		t.Fatalf("server route = %q, want dual-root-export-union-import", route)
	}

	for _, invalid := range []string{
		`{"schema":` + doltSchema + `,` + topology + `}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["directory:.beads/dolt","metadata-backend:dolt"]}`,
		`{"coexisting_sqlite_schema":{},"schema":` + doltSchema + `,` + topology + `}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","sqlite-coexisting:.beads/other.db"]}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["directory:.beads/dolt","metadata-backend:sqlite","metadata-database:dolt","sqlite-coexisting:.beads/beads.db"]}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","metadata-dolt-mode:server","sqlite-coexisting:.beads/beads.db"]}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["directory:.beads/embeddeddolt","metadata-backend:dolt","metadata-database:dolt","sqlite-coexisting:.beads/beads.db"]}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["directory:.beads/dolt","metadata-backend:dolt","sqlite-coexisting:.beads/beads.db"]}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["directory:.beads/dolt","metadata-backend:dolt","metadata-database:other","sqlite-coexisting:.beads/beads.db"]}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["database:.beads/beads.db","directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","sqlite-coexisting:.beads/beads.db"]}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"topology":["data:.beads/issues.jsonl","directory:.beads/dolt","metadata-backend:dolt","metadata-database:dolt","sqlite-coexisting:.beads/beads.db"]}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"schema":` + doltSchema + `,"stores":[{"name":"dolt","schema":` + doltSchema + `}],` + topology + `}`,
		`{"coexisting_sqlite_schema":` + sqliteSchema + `,"ignored":true,"schema":` + doltSchema + `,` + topology + `}`,
	} {
		if _, _, err := routeForFamily(family{Mode: "dolt-legacy", Layout: []byte(invalid)}); err == nil {
			t.Fatalf("accepted inconsistent coexisting SQLite layout: %s", invalid)
		}
	}
}

func writeArtifactInputs(t *testing.T, dir string, c catalog, result census) (string, string) {
	t.Helper()
	catalogPath, censusPath := filepath.Join(dir, "catalog.json"), filepath.Join(dir, "census.json")
	catalogRaw, err := encodeCatalog(c)
	if err != nil {
		t.Fatal(err)
	}
	censusRaw, err := encodeCensus(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, catalogRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(censusPath, censusRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	return catalogPath, censusPath
}

func artifactTestCensus(t *testing.T, c catalog) census {
	t.Helper()
	result := testCensus(t, c)
	for i := range result.Families {
		old := result.Families[i].ID
		result.Families[i].Layout = testFamilyLayout(t, result.Families[i].Mode, "artifact")
		id, err := familyID(result.Families[i].Mode, result.Families[i].Layout)
		if err != nil {
			t.Fatal(err)
		}
		result.Families[i].ID = id
		for j := range result.Observations {
			if result.Observations[j].FamilyID == old {
				result.Observations[j].FamilyID = id
			}
		}
		for j := range result.Transitions {
			if result.Transitions[j].FromFamilyID == old {
				result.Transitions[j].FromFamilyID = id
			}
			if result.Transitions[j].ToFamilyID == old {
				result.Transitions[j].ToFamilyID = id
			}
		}
	}
	// Replace the single-root legacy family with the valid historic dual-root
	// state, which is also represented as legacy.
	dualSchema := string(canonicalTestJSON(t, testDoltFingerprint("dual")))
	dual := family{Mode: "dolt-legacy", Layout: []byte(`{"schema":` + dualSchema + `,"stores":[{"name":"dolt","schema":` + dualSchema + `},{"name":"embeddeddolt","schema":` + dualSchema + `}],"topology":["directory:.beads/dolt","directory:.beads/embeddeddolt","metadata-backend:dolt","metadata-dolt-mode:embedded"]}`)}
	var err error
	dual.ID, err = familyID(dual.Mode, dual.Layout)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range result.Families {
		if candidate.Mode != "dolt-legacy" {
			continue
		}
		for j := range result.Observations {
			if result.Observations[j].FamilyID == candidate.ID {
				result.Observations[j].FamilyID = dual.ID
			}
		}
		for j := range result.Transitions {
			if result.Transitions[j].FromFamilyID == candidate.ID {
				result.Transitions[j].FromFamilyID = dual.ID
			}
			if result.Transitions[j].ToFamilyID == candidate.ID {
				result.Transitions[j].ToFamilyID = dual.ID
			}
		}
	}
	filtered := make([]family, 0, len(result.Families))
	for _, candidate := range result.Families {
		if candidate.Mode != "dolt-legacy" {
			filtered = append(filtered, candidate)
		}
	}
	result.Families = append(filtered, dual)
	sort.Slice(result.Families, func(i, j int) bool { return result.Families[i].ID < result.Families[j].ID })
	sortObservations(result.Observations)
	sortLineageTransitions(result.Transitions)
	return result
}

func artifactCatalog() catalog {
	c := testCatalog()
	c.Versions = append(c.Versions[:1], append([]catalogEntry{{Version: "v0.50.2", Sum: "h1:middle", GoModSum: "h1:middlemod", Origin: catalogOrigin{Hash: strings.Repeat("c", 40), Ref: "refs/tags/v0.50.2"}, SourceZip: catalogSourceZip{SHA256: strings.Repeat("3", sha256.Size*2), Size: 150}}}, c.Versions[1:]...)...)
	return c
}
