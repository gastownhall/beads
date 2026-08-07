package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/atomicfile"
)

const routeManifestVersion = 1
const routeManifestBasis = "topology-classification"
const routeManifestQualification = "not-executed"

const routeSummaryTitle = "# Topology-derived candidate recovery paths (not E2E-qualified)"
const routeSummaryDisclaimer = "These routes are topology-derived candidate paths, not E2E-qualified."

type routeManifest struct {
	SchemaVersion int          `json:"schema_version"`
	Basis         string       `json:"basis"`
	Qualification string       `json:"qualification"`
	CensusSHA256  string       `json:"census_sha256"`
	Groups        []routeGroup `json:"groups"`
}

type routeGroup struct {
	Route    string        `json:"route"`
	Families []familyRoute `json:"families"`
}

type familyRoute struct {
	FamilyID string   `json:"family_id"`
	Mode     string   `json:"mode"`
	Topology []string `json:"topology"`
}

func sealArtifacts(catalogPath, censusPath, evidencePath, routesPath, summaryPath string, requirePinned bool) error {
	result, raw, err := readValidCensus(catalogPath, censusPath, requirePinned)
	if err != nil {
		return err
	}
	manifest, err := routeManifestForValidatedCensus(result)
	if err != nil {
		return err
	}
	evidence, err := deterministicGzip(raw)
	if err != nil {
		return err
	}
	routes, err := encodeRouteManifest(manifest)
	if err != nil {
		return err
	}
	summary := routeSummary(result, manifest)
	for _, output := range []struct {
		path string
		data []byte
	}{{evidencePath, evidence}, {routesPath, routes}, {summaryPath, summary}} {
		if err := atomicfile.WriteFile(output.path, output.data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", output.path, err)
		}
	}
	return nil
}

func verifyArtifacts(catalogPath, evidencePath, routesPath, summaryPath string, requirePinned bool) error {
	raw, err := readGzip(evidencePath)
	if err != nil {
		return fmt.Errorf("read evidence: %w", err)
	}
	result, _, err := readValidCensusBytes(catalogPath, raw, requirePinned)
	if err != nil {
		return fmt.Errorf("validate evidence: %w", err)
	}
	manifest, err := routeManifestForValidatedCensus(result)
	if err != nil {
		return err
	}
	wantEvidence, err := deterministicGzip(raw)
	if err != nil {
		return err
	}
	if supplied, err := os.ReadFile(evidencePath); err != nil || !bytes.Equal(supplied, wantEvidence) { //nolint:gosec // evidencePath is an explicit artifact-verification CLI argument.
		return errors.New("evidence is not the deterministic sealed census")
	}
	wantRoutes, err := encodeRouteManifest(manifest)
	if err != nil {
		return err
	}
	if err := requireExactFile(routesPath, wantRoutes); err != nil {
		return fmt.Errorf("route manifest drift: %w", err)
	}
	if err := requireExactFile(summaryPath, routeSummary(result, manifest)); err != nil {
		return fmt.Errorf("summary drift: %w", err)
	}
	return nil
}

func readValidCensus(catalogPath, censusPath string, requirePinned bool) (census, []byte, error) {
	raw, err := os.ReadFile(censusPath) //nolint:gosec // census is an explicit CLI argument.
	if err != nil {
		return census{}, nil, err
	}
	return readValidCensusBytes(catalogPath, raw, requirePinned)
}

func readValidCensusBytes(catalogPath string, raw []byte, requirePinned bool) (census, []byte, error) {
	catalog, catalogRaw, err := readCatalog(catalogPath, requirePinned)
	if err != nil {
		return census{}, nil, err
	}
	result, err := decodeCensus(raw)
	if err != nil {
		return census{}, nil, err
	}
	if result.CatalogSHA256 != digest(catalogRaw) {
		return census{}, nil, errors.New("census catalog digest does not match catalog")
	}
	if err := validateCensus(result, catalog); err != nil {
		return census{}, nil, err
	}
	return result, raw, nil
}

func deterministicGzip(raw []byte) ([]byte, error) {
	var output bytes.Buffer
	writer, err := gzip.NewWriterLevel(&output, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	writer.Header.ModTime = time.Unix(0, 0)
	writer.Header.OS = 255
	if _, err := writer.Write(raw); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func readGzip(path string) ([]byte, error) {
	input, err := os.Open(path) //nolint:gosec // evidence is an explicit CLI argument.
	if err != nil {
		return nil, err
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		return nil, err
	}
	raw, copyErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	return raw, closeErr
}

func routeManifestForCensus(result census) (routeManifest, error) {
	return routeManifestForFamilies(result, routeForFamily)
}

// routeManifestForValidatedCensus derives routes only after readValidCensus
// has independently validated every family fingerprint payload.
func routeManifestForValidatedCensus(result census) (routeManifest, error) {
	return routeManifestForFamilies(result, routeForValidatedFamily)
}

func routeManifestForFamilies(result census, routeFamily func(family) ([]string, string, error)) (routeManifest, error) {
	groups := map[string][]familyRoute{}
	for _, candidate := range result.Families {
		topology, route, err := routeFamily(candidate)
		if err != nil {
			return routeManifest{}, fmt.Errorf("family %s: %w", candidate.ID, err)
		}
		groups[route] = append(groups[route], familyRoute{FamilyID: candidate.ID, Mode: candidate.Mode, Topology: topology})
	}
	keys := make([]string, 0, len(groups))
	for route := range groups {
		keys = append(keys, route)
	}
	sort.Strings(keys)
	manifest := routeManifest{
		SchemaVersion: routeManifestVersion,
		Basis:         routeManifestBasis,
		Qualification: routeManifestQualification,
		CensusSHA256:  censusDigest(result),
		Groups:        make([]routeGroup, 0, len(keys)),
	}
	for _, route := range keys {
		families := groups[route]
		sort.Slice(families, func(i, j int) bool { return families[i].FamilyID < families[j].FamilyID })
		manifest.Groups = append(manifest.Groups, routeGroup{Route: route, Families: families})
	}
	total := 0
	for _, group := range manifest.Groups {
		total += len(group.Families)
	}
	if total != len(result.Families) {
		return routeManifest{}, fmt.Errorf("route manifest groups %d families do not equal census family count %d", total, len(result.Families))
	}
	return manifest, nil
}

func censusDigest(result census) string {
	raw, err := encodeCensus(result)
	if err != nil {
		panic(err)
	}
	return digest(raw)
}

func routeForFamily(candidate family) ([]string, string, error) {
	if err := validateFamilyFingerprintPayload(candidate); err != nil {
		return nil, "", err
	}
	return routeForValidatedFamily(candidate)
}

// routableLayout is the decoded fingerprint payload the router classifies. It
// mirrors the census family layout and, like the original inline decode, uses a
// permissive json.Unmarshal (no DisallowUnknownFields) so forward-compatible
// payloads still route.
type routableLayout struct {
	Topology               []string        `json:"topology"`
	Schema                 json.RawMessage `json:"schema"`
	Stores                 json.RawMessage `json:"stores"`
	Databases              json.RawMessage `json:"databases"`
	CoexistingSQLiteSchema json.RawMessage `json:"coexisting_sqlite_schema"`
	RetainedBackupSchemas  json.RawMessage `json:"retained_backup_schemas"`
}

// routeClassifier inspects a decoded family and either claims it (matched=true,
// returning a route or an error) or declines it (matched=false, err=nil) so the
// next classifier in priority order can try.
type routeClassifier func(candidate family, layout routableLayout, markers topologyMarkers) (topology []string, route string, matched bool, err error)

// routeForValidatedFamily classifies a family whose fingerprint payload was
// already validated at the census read boundary. It decodes the layout, then
// consults each topology classifier in priority order; the first that matches
// (or fails) is authoritative, and an unclaimed family is inconsistent.
func routeForValidatedFamily(candidate family) ([]string, string, error) {
	layout, markers, err := decodeRoutableLayout(candidate)
	if err != nil {
		return nil, "", err
	}
	classifiers := []routeClassifier{
		routeMetadataDoltDatabase,
		routeDualRootEmbedded,
		routeCoexistingSQLite,
		routeByProviderMode,
	}
	for _, classify := range classifiers {
		topology, route, matched, err := classify(candidate, layout, markers)
		if err != nil {
			return nil, "", err
		}
		if matched {
			return topology, route, nil
		}
	}
	return nil, "", errors.New("topology is inconsistent with provider mode")
}

// decodeRoutableLayout unmarshals and canonicality-checks the family layout and
// derives its topology markers, applying the cross-cutting invariants that hold
// before any per-topology classification runs.
func decodeRoutableLayout(candidate family) (routableLayout, topologyMarkers, error) {
	var layout routableLayout
	if err := json.Unmarshal(candidate.Layout, &layout); err != nil || len(layout.Topology) == 0 {
		return routableLayout{}, topologyMarkers{}, errors.New("layout has no valid topology")
	}
	if !sort.StringsAreSorted(layout.Topology) || hasDuplicate(layout.Topology) {
		return routableLayout{}, topologyMarkers{}, errors.New("topology is not canonical")
	}
	markers, err := classifyTopology(layout.Topology)
	if err != nil {
		return routableLayout{}, topologyMarkers{}, err
	}
	if !validCoexistingSQLiteSchema(layout.CoexistingSQLiteSchema, markers.sqliteCoexisting) {
		return routableLayout{}, topologyMarkers{}, errors.New("Dolt topology has inconsistent coexisting SQLite schema")
	}
	if !markers.sqliteCoexisting && markers.metadataDatabase != "" && markers.metadataDoltDatabase == "" {
		return routableLayout{}, topologyMarkers{}, errors.New("metadata database selector requires coexisting SQLite")
	}
	if markers.metadataDoltDatabase == "" && len(layout.Databases) != 0 {
		return routableLayout{}, topologyMarkers{}, errors.New("database fingerprints require a metadata-selected Dolt database")
	}
	return layout, markers, nil
}

// routeMetadataDoltDatabase classifies families that select a Dolt metadata
// database: either the canonical empty dual-root census family or a consistent
// legacy multi-database family. Selecting a metadata Dolt database is
// authoritative, so a non-canonical, non-consistent family is rejected here
// rather than falling through.
func routeMetadataDoltDatabase(candidate family, layout routableLayout, markers topologyMarkers) ([]string, string, bool, error) {
	if markers.metadataDoltDatabase == "" {
		return nil, "", false, nil
	}
	if markers.legacy && markers.embedded && isCanonicalMetadataDoltDualRoot(candidate, layout, markers) {
		if err := requireMultiDatabaseSchemas(layout.Databases, layout.Schema, markers.metadataDoltDatabase); err != nil {
			return nil, "", true, err
		}
		if err := requireDualRootStores(layout.Stores, layout.Schema); err != nil {
			return nil, "", true, err
		}
		return layout.Topology, "dual-root-export-union-import", true, nil
	}
	if !isConsistentMultiDatabaseLegacy(candidate, layout, markers) {
		return nil, "", true, errors.New("multi-database topology is inconsistent with legacy Dolt metadata")
	}
	if err := requireMultiDatabaseSchemas(layout.Databases, layout.Schema, markers.metadataDoltDatabase); err != nil {
		return nil, "", true, err
	}
	return layout.Topology, "multi-database-export-union-import", true, nil
}

// isCanonicalMetadataDoltDualRoot reports whether the family is the canonical
// empty dual-root census family selecting a Dolt metadata database.
func isCanonicalMetadataDoltDualRoot(candidate family, layout routableLayout, markers topologyMarkers) bool {
	expectedTopology := []string{
		"directory:.beads/dolt",
		"directory:.beads/embeddeddolt",
		"local-version:other-valid",
		"metadata-backend:dolt",
		"metadata-database:dolt",
		"metadata-dolt-database:beads_census",
	}
	return candidate.Mode == "dolt-legacy" && reflect.DeepEqual(layout.Topology, expectedTopology) &&
		!markers.sqliteCoexisting && !markers.database && !markers.data && !markers.sqliteBackups &&
		markers.provider == "dolt" && markers.metadataDatabase == "dolt" && !markers.doltModeSeen &&
		markers.localVersion == "local-version:other-valid" && len(layout.RetainedBackupSchemas) == 0
}

// isConsistentMultiDatabaseLegacy reports whether a metadata-Dolt-database
// family is a well-formed legacy multi-database layout eligible for
// multi-database-export-union-import.
func isConsistentMultiDatabaseLegacy(candidate family, layout routableLayout, markers topologyMarkers) bool {
	return !(markers.sqliteCoexisting || markers.embedded || markers.database || markers.data || len(layout.Stores) != 0 ||
		candidate.Mode != "dolt-legacy" || !markers.legacy || markers.provider != "dolt" ||
		markers.metadataDatabase != "dolt" || markers.doltModeSeen ||
		!markers.localVersionSeen || (markers.localVersion != "local-version:absent-or-invalid" && markers.localVersion != "local-version:legacy-server" && markers.localVersion != "local-version:other-valid") ||
		markers.sqliteBackups || len(layout.RetainedBackupSchemas) != 0)
}

// routeDualRootEmbedded classifies coexisting legacy+embedded dual-Dolt-root
// families. Presence of both roots is authoritative.
func routeDualRootEmbedded(candidate family, layout routableLayout, markers topologyMarkers) ([]string, string, bool, error) {
	if !(markers.legacy && markers.embedded) {
		return nil, "", false, nil
	}
	if markers.sqliteCoexisting || markers.database || markers.data {
		return nil, "", true, errors.New("dual-Dolt-root topology has conflicting primary storage markers")
	}
	if !validRetainedSQLiteBackupSchemas(layout.RetainedBackupSchemas, markers.sqliteBackups) {
		return nil, "", true, errors.New("Dolt topology has inconsistent retained SQLite backup schemas")
	}
	if err := requireDualRootStores(layout.Stores, layout.Schema); err != nil {
		return nil, "", true, err
	}
	if isDualRootProviderConsistent(candidate, markers) {
		return layout.Topology, "dual-root-export-union-import", true, nil
	}
	return nil, "", true, errors.New("dual-root topology is inconsistent with provider mode")
}

// isDualRootProviderConsistent reports whether a dual-root family's declared
// provider mode matches its recorded Dolt runtime mode.
func isDualRootProviderConsistent(candidate family, markers topologyMarkers) bool {
	return (candidate.Mode == "dolt-legacy" && markers.providerOK && (markers.doltMode == "" || markers.doltMode == "embedded")) ||
		(candidate.Mode == "dolt-server" && markers.providerOK && markers.doltMode == "server")
}

// routeCoexistingSQLite classifies Dolt families that retain a coexisting SQLite
// metadata store. Presence of coexisting SQLite is authoritative.
func routeCoexistingSQLite(candidate family, layout routableLayout, markers topologyMarkers) ([]string, string, bool, error) {
	if !markers.sqliteCoexisting {
		return nil, "", false, nil
	}
	if isCoexistingSQLiteConsistent(candidate, layout, markers) {
		return layout.Topology, "dual-root-export-union-import", true, nil
	}
	return nil, "", true, errors.New("coexisting SQLite topology is inconsistent with provider mode")
}

// isCoexistingSQLiteConsistent reports whether a coexisting-SQLite family's
// provider mode and metadata selector are mutually consistent.
func isCoexistingSQLiteConsistent(candidate family, layout routableLayout, markers topologyMarkers) bool {
	legacyMode := candidate.Mode == "dolt-legacy" && markers.doltMode == ""
	serverMode := candidate.Mode == "dolt-server" && markers.doltMode == "server"
	selectorOK := (legacyMode && (markers.metadataDatabase == "beads.db" || markers.metadataDatabase == "dolt")) ||
		(serverMode && markers.metadataDatabase == "dolt")
	return (legacyMode || serverMode) && len(layout.Stores) == 0 &&
		validRetainedSQLiteBackupSchemas(layout.RetainedBackupSchemas, markers.sqliteBackups) &&
		markers.legacy && !markers.embedded && !markers.database && !markers.data &&
		markers.provider == "dolt" && selectorOK
}

// routeByProviderMode classifies single-root families by their declared provider
// mode. A known mode whose invariants fail declines (matched=false) so the
// caller emits the final inconsistency error; an unknown mode is rejected here.
func routeByProviderMode(candidate family, layout routableLayout, markers topologyMarkers) ([]string, string, bool, error) {
	switch candidate.Mode {
	case "sqlite":
		if isSealedCopySQLiteFamily(layout, markers) {
			return layout.Topology, "sealed-copy-export-import", true, nil
		}
	case "jsonl":
		if isHeadInitFromJSONLFamily(layout, markers) {
			return layout.Topology, "head-init-from-jsonl", true, nil
		}
	case "dolt-legacy":
		if isSealedCopyDoltLegacyFamily(layout, markers) {
			return layout.Topology, "sealed-copy-export-import", true, nil
		}
	case "dolt-server":
		if isSealedCopyDoltServerFamily(layout, markers) {
			return layout.Topology, "sealed-copy-export-import", true, nil
		}
	case "dolt-embedded":
		if isSealedCopyDoltEmbeddedFamily(layout, markers) {
			return layout.Topology, "sealed-copy-direct-head-migration", true, nil
		}
	default:
		return nil, "", true, fmt.Errorf("unknown provider mode %q", candidate.Mode)
	}
	return nil, "", false, nil
}

// isSealedCopySQLiteFamily reports whether a sqlite-mode family is a plain sealed
// SQLite copy eligible for sealed-copy-export-import.
func isSealedCopySQLiteFamily(layout routableLayout, markers topologyMarkers) bool {
	providerOK := markers.provider == "" || markers.provider == "sqlite"
	backupsOK := validRetainedSQLiteBackupSchemas(layout.RetainedBackupSchemas, markers.sqliteBackups)
	return len(layout.Stores) == 0 && providerOK && backupsOK &&
		markers.database && !markers.data && !markers.legacy && !markers.embedded && markers.doltMode == ""
}

// isHeadInitFromJSONLFamily reports whether a jsonl-mode family is a data-only
// export eligible for head-init-from-jsonl.
func isHeadInitFromJSONLFamily(layout routableLayout, markers topologyMarkers) bool {
	return len(layout.Stores) == 0 && len(layout.RetainedBackupSchemas) == 0 &&
		!markers.sqliteBackups && markers.data && !markers.database && !markers.legacy && !markers.embedded && markers.provider == "" && markers.doltMode == ""
}

// isSealedCopyDoltLegacyFamily reports whether a dolt-legacy-mode single-root
// family is a sealed legacy Dolt copy eligible for sealed-copy-export-import.
func isSealedCopyDoltLegacyFamily(layout routableLayout, markers topologyMarkers) bool {
	return len(layout.Stores) == 0 && validRetainedSQLiteBackupSchemas(layout.RetainedBackupSchemas, markers.sqliteBackups) &&
		markers.legacy && !markers.embedded && !markers.database && !markers.data &&
		markers.providerOK && (markers.doltMode == "" || markers.doltMode == "embedded")
}

// isSealedCopyDoltServerFamily reports whether a dolt-server-mode single-root
// family is a sealed legacy Dolt copy eligible for sealed-copy-export-import.
func isSealedCopyDoltServerFamily(layout routableLayout, markers topologyMarkers) bool {
	return len(layout.Stores) == 0 && validRetainedSQLiteBackupSchemas(layout.RetainedBackupSchemas, markers.sqliteBackups) &&
		markers.legacy && !markers.embedded && !markers.database && !markers.data &&
		markers.providerOK && markers.doltMode == "server"
}

// isSealedCopyDoltEmbeddedFamily reports whether a dolt-embedded-mode single-root
// family is a sealed embedded Dolt copy eligible for
// sealed-copy-direct-head-migration.
func isSealedCopyDoltEmbeddedFamily(layout routableLayout, markers topologyMarkers) bool {
	return len(layout.Stores) == 0 && validRetainedSQLiteBackupSchemas(layout.RetainedBackupSchemas, markers.sqliteBackups) &&
		!markers.legacy && markers.embedded && !markers.database && !markers.data &&
		markers.providerOK && (markers.doltMode == "" || markers.doltMode == "embedded")
}

type doltFamilyLayout struct {
	CoexistingSQLiteSchema json.RawMessage `json:"coexisting_sqlite_schema"`
	RetainedBackupSchemas  json.RawMessage `json:"retained_backup_schemas"`
	Schema                 json.RawMessage `json:"schema"`
	Stores                 json.RawMessage `json:"stores"`
	Databases              json.RawMessage `json:"databases"`
	Topology               []string        `json:"topology"`
}

func validateFamilyFingerprintPayload(candidate family) error {
	switch candidate.Mode {
	case "sqlite":
		var layout struct {
			RetainedBackupSchemas json.RawMessage `json:"retained_backup_schemas"`
			Schema                json.RawMessage `json:"schema"`
			Topology              []string        `json:"topology"`
		}
		if err := decodeStrictJSON(candidate.Layout, &layout, '{'); err != nil {
			return fmt.Errorf("invalid SQLite layout: %w", err)
		}
		if _, err := decodeActiveSQLiteFingerprint(layout.Schema); err != nil {
			return fmt.Errorf("invalid SQLite fingerprint: %w", err)
		}
	case "jsonl":
		var layout struct {
			Dialect  json.RawMessage `json:"dialect"`
			Format   string          `json:"format"`
			Topology []string        `json:"topology"`
		}
		if err := decodeStrictJSON(candidate.Layout, &layout, '{'); err != nil {
			return fmt.Errorf("invalid JSONL layout: %w", err)
		}
		if layout.Format != "beads-jsonl" {
			return errors.New("invalid JSONL format")
		}
		if _, err := decodeJSONLDialect(layout.Dialect); err != nil {
			return fmt.Errorf("invalid JSONL dialect: %w", err)
		}
	case "dolt-legacy", "dolt-server", "dolt-embedded":
		var layout doltFamilyLayout
		if err := decodeStrictJSON(candidate.Layout, &layout, '{'); err != nil {
			return fmt.Errorf("invalid Dolt layout: %w", err)
		}
		schema, err := decodeDoltFingerprint(layout.Schema)
		if err != nil {
			return fmt.Errorf("invalid Dolt fingerprint: %w", err)
		}
		if !validActiveDoltFingerprint(schema) && !validCanonicalEmptyDoltWithCoexistingSQLite(candidate, layout, schema) {
			return errors.New("invalid Dolt fingerprint: fingerprint is neither active nor a canonical empty Dolt topology with coexisting SQLite")
		}
	default:
		return fmt.Errorf("unknown provider mode %q", candidate.Mode)
	}
	return nil
}

func validCanonicalEmptyDoltWithCoexistingSQLite(candidate family, layout doltFamilyLayout, schema doltFingerprint) bool {
	var expectedTopologies [][]string
	switch candidate.Mode {
	case "dolt-legacy":
		expectedTopologies = [][]string{{
			"directory:.beads/dolt",
			"local-version:absent-or-invalid",
			"metadata-backend:dolt",
			"metadata-database:dolt",
			"sqlite-coexisting:.beads/beads.db",
		}, {
			"directory:.beads/dolt",
			"local-version:legacy-server",
			"metadata-backend:dolt",
			"metadata-database:dolt",
			"sqlite-coexisting:.beads/beads.db",
		}, {
			"directory:.beads/dolt",
			"local-version:other-valid",
			"metadata-backend:dolt",
			"metadata-database:dolt",
			"sqlite-coexisting:.beads/beads.db",
		}}
	case "dolt-server":
		expectedTopologies = [][]string{{
			"directory:.beads/dolt",
			"local-version:absent-or-invalid",
			"metadata-backend:dolt",
			"metadata-database:dolt",
			"metadata-dolt-mode:server",
			"sqlite-coexisting:.beads/beads.db",
		}, {
			"directory:.beads/dolt",
			"local-version:legacy-server",
			"metadata-backend:dolt",
			"metadata-database:dolt",
			"metadata-dolt-mode:server",
			"sqlite-coexisting:.beads/beads.db",
		}, {
			"directory:.beads/dolt",
			"local-version:other-valid",
			"metadata-backend:dolt",
			"metadata-database:dolt",
			"metadata-dolt-mode:server",
			"sqlite-coexisting:.beads/beads.db",
		}}
	default:
		return false
	}
	validTopology := false
	for _, expected := range expectedTopologies {
		if reflect.DeepEqual(layout.Topology, expected) {
			validTopology = true
			break
		}
	}
	if !validTopology ||
		len(layout.Stores) != 0 || len(layout.Databases) != 0 || len(layout.RetainedBackupSchemas) != 0 ||
		!validCoexistingSQLiteSchema(layout.CoexistingSQLiteSchema, true) {
		return false
	}
	return validCanonicalEmptyDoltFingerprint(schema)
}

func validCanonicalEmptyDoltFingerprint(schema doltFingerprint) bool {
	if len(schema.Objects) != 0 || len(schema.MigrationLedgers) != 0 ||
		len(schema.Catalog) != len(doltCatalogQueries) || len(schema.Capabilities) != len(doltCatalogQueries) {
		return false
	}
	queries := make(map[string]doltCatalogQuery, len(doltCatalogQueries))
	for _, query := range doltCatalogQueries {
		queries[query.name] = query
	}
	for _, snapshot := range schema.Catalog {
		query, ok := queries[snapshot.Name]
		if !ok || snapshot.Rows == nil || len(snapshot.Rows) != 0 || !reflect.DeepEqual(snapshot.Columns, query.columns) {
			return false
		}
		delete(queries, snapshot.Name)
	}
	if len(queries) != 0 {
		return false
	}
	capabilities := make(map[string]bool, len(doltCatalogQueries))
	for _, query := range doltCatalogQueries {
		capabilities[query.name] = true
	}
	for _, capability := range schema.Capabilities {
		if !capability.Supported {
			return false
		}
		if !capabilities[capability.Name] {
			return false
		}
		delete(capabilities, capability.Name)
	}
	return len(capabilities) == 0
}

func validCoexistingSQLiteSchema(raw json.RawMessage, marker bool) bool {
	if !marker {
		return len(raw) == 0
	}
	if len(raw) == 0 {
		return false
	}
	_, err := decodeActiveSQLiteFingerprint(raw)
	return err == nil
}

func validRetainedSQLiteBackupSchemas(raw json.RawMessage, marker bool) bool {
	if !marker {
		return len(raw) == 0
	}
	if len(raw) == 0 {
		return false
	}
	var encoded []json.RawMessage
	if err := decodeStrictJSON(raw, &encoded, '['); err != nil || len(encoded) == 0 {
		return false
	}
	previous := ""
	for _, rawSchema := range encoded {
		schema, err := decodeActiveSQLiteFingerprint(rawSchema)
		if err != nil {
			return false
		}
		encoded, err := json.Marshal(schema)
		if err != nil || (previous != "" && previous >= string(encoded)) {
			return false
		}
		previous = string(encoded)
	}
	return true
}

func requireDualRootStores(rawStores, rawSchema json.RawMessage) error {
	stores, err := decodeLabeledDoltStores(rawStores)
	if err != nil || len(stores) != 2 || stores[0].Name != "dolt" || stores[1].Name != "embeddeddolt" {
		return errors.New("dual-root topology requires canonical dolt and embeddeddolt stores")
	}
	for _, store := range stores {
		if !validActiveDoltFingerprint(store.Schema) {
			return errors.New("dual-root topology contains an incomplete Dolt fingerprint")
		}
	}
	topLevel, err := decodeActiveDoltFingerprint(rawSchema)
	if err != nil || !reflect.DeepEqual(topLevel, stores[0].Schema) {
		return errors.New("dual-root topology requires top-level schema consistent with dolt store")
	}
	return nil
}

func requireMultiDatabaseSchemas(rawDatabases, rawSchema json.RawMessage, selector string) error {
	if selector != "beads_census" {
		return errors.New("multi-database topology has an invalid selected database")
	}
	var encoded []struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := decodeStrictJSON(rawDatabases, &encoded, '['); err != nil || len(encoded) != 2 {
		return errors.New("multi-database topology requires exactly two database fingerprints")
	}
	databases := make([]labeledDoltDatabase, len(encoded))
	for index, database := range encoded {
		schema, err := decodeActiveDoltFingerprint(database.Schema)
		if err != nil {
			return errors.New("multi-database topology contains an incomplete Dolt fingerprint")
		}
		databases[index] = labeledDoltDatabase{Name: database.Name, Schema: schema}
	}
	if databases[0].Name != "beads" || databases[1].Name != selector {
		return errors.New("multi-database topology requires canonical beads and selected databases")
	}
	topLevel, err := decodeActiveDoltFingerprint(rawSchema)
	if err != nil || !reflect.DeepEqual(topLevel, databases[1].Schema) {
		return errors.New("multi-database topology requires top-level schema consistent with selected database")
	}
	return nil
}

func decodeLabeledDoltStores(raw json.RawMessage) ([]labeledDoltStore, error) {
	var encoded []struct {
		Name   string          `json:"name"`
		Schema json.RawMessage `json:"schema"`
	}
	if err := decodeStrictJSON(raw, &encoded, '['); err != nil {
		return nil, err
	}
	stores := make([]labeledDoltStore, len(encoded))
	for index, store := range encoded {
		schema, err := decodeDoltFingerprint(store.Schema)
		if err != nil {
			return nil, err
		}
		stores[index] = labeledDoltStore{Name: store.Name, Schema: schema}
	}
	return stores, nil
}

func decodeDoltFingerprint(raw json.RawMessage) (doltFingerprint, error) {
	var encoded struct {
		Objects          json.RawMessage `json:"objects"`
		Catalog          json.RawMessage `json:"catalog"`
		MigrationLedgers json.RawMessage `json:"migration_ledgers"`
		Capabilities     json.RawMessage `json:"capabilities"`
	}
	if err := decodeStrictJSON(raw, &encoded, '{'); err != nil {
		return doltFingerprint{}, err
	}
	for _, collection := range []json.RawMessage{encoded.Objects, encoded.Catalog, encoded.MigrationLedgers, encoded.Capabilities} {
		collection = bytes.TrimSpace(collection)
		if len(collection) == 0 || collection[0] != '[' {
			return doltFingerprint{}, errors.New("Dolt fingerprint has incomplete collections")
		}
	}
	var schema doltFingerprint
	if err := decodeStrictJSON(raw, &schema, '{'); err != nil {
		return doltFingerprint{}, err
	}
	if !validDoltFingerprint(schema) {
		return doltFingerprint{}, errors.New("Dolt fingerprint is not canonical")
	}
	return schema, nil
}

func decodeActiveDoltFingerprint(raw json.RawMessage) (doltFingerprint, error) {
	schema, err := decodeDoltFingerprint(raw)
	if err != nil {
		return doltFingerprint{}, err
	}
	if !validActiveDoltFingerprint(schema) {
		return doltFingerprint{}, errors.New("Dolt fingerprint does not describe an active Beads schema")
	}
	return schema, nil
}

func validActiveDoltFingerprint(schema doltFingerprint) bool {
	if len(schema.Objects) == 0 || len(schema.Capabilities) != len(doltCatalogQueries) {
		return false
	}
	for _, object := range schema.Objects {
		if object.Type == "" || object.Create == "" {
			return false
		}
	}
	known := make(map[string][]string, len(doltCatalogQueries))
	for _, query := range doltCatalogQueries {
		known[query.name] = query.columns
	}
	snapshots := make(map[string]bool, len(schema.Catalog))
	for _, snapshot := range schema.Catalog {
		columns, ok := known[snapshot.Name]
		if !ok || !sameStringOrder(snapshot.Columns, columns) {
			return false
		}
		snapshots[snapshot.Name] = true
	}
	for _, capability := range schema.Capabilities {
		if _, ok := known[capability.Name]; !ok || capability.Supported != snapshots[capability.Name] {
			return false
		}
		delete(known, capability.Name)
	}
	return len(known) == 0
}

func validDoltFingerprint(schema doltFingerprint) bool {
	if schema.Objects == nil || schema.Catalog == nil || schema.MigrationLedgers == nil || schema.Capabilities == nil {
		return false
	}
	previous := ""
	for _, object := range schema.Objects {
		if object.Name == "" || previous >= object.Name {
			return false
		}
		previous = object.Name
	}
	knownCatalogColumns := make(map[string][]string, len(doltCatalogQueries))
	for _, query := range doltCatalogQueries {
		knownCatalogColumns[query.name] = query.columns
	}
	previous = ""
	for _, snapshot := range schema.Catalog {
		columns, known := knownCatalogColumns[snapshot.Name]
		if snapshot.Name == "" || previous >= snapshot.Name || snapshot.Columns == nil ||
			!known || !sameStringOrder(snapshot.Columns, columns) {
			return false
		}
		for _, row := range snapshot.Rows {
			if len(row) != len(snapshot.Columns) {
				return false
			}
		}
		previous = snapshot.Name
	}
	if !doltSnapshotSorted(schema.Catalog) {
		return false
	}
	columnsByTable := doltColumnsByTable(schema.Catalog)
	previous = ""
	expectedLedgers := make(map[string]bool)
	for _, object := range schema.Objects {
		if object.Type == "BASE TABLE" && isMigrationLedger(object.Name) {
			expectedLedgers[object.Name] = true
		}
	}
	for _, ledger := range schema.MigrationLedgers {
		if ledger.Table == "" || previous >= ledger.Table || ledger.Columns == nil {
			return false
		}
		if !expectedLedgers[ledger.Table] {
			return false
		}
		if columns, found := columnsByTable[ledger.Table]; !found || !sameStringOrder(ledger.Columns, columns) {
			return false
		}
		if !doltRowsSorted(ledger.Rows) {
			return false
		}
		for _, row := range ledger.Rows {
			if len(row) != len(ledger.Columns) {
				return false
			}
		}
		delete(expectedLedgers, ledger.Table)
		previous = ledger.Table
	}
	if len(expectedLedgers) != 0 {
		return false
	}
	previous = ""
	for _, capability := range schema.Capabilities {
		if capability.Name == "" || previous >= capability.Name {
			return false
		}
		previous = capability.Name
	}
	return true
}

func decodeActiveSQLiteFingerprint(raw json.RawMessage) (sqliteFingerprint, error) {
	var encoded struct {
		MigrationLedgers json.RawMessage `json:"migration_ledgers"`
		Objects          json.RawMessage `json:"objects"`
		Pragmas          json.RawMessage `json:"pragmas"`
		Tables           json.RawMessage `json:"tables"`
	}
	if err := decodeStrictJSON(raw, &encoded, '{'); err != nil {
		return sqliteFingerprint{}, err
	}
	for _, collection := range []json.RawMessage{encoded.MigrationLedgers, encoded.Objects, encoded.Pragmas, encoded.Tables} {
		collection = bytes.TrimSpace(collection)
		if len(collection) == 0 || collection[0] != '[' {
			return sqliteFingerprint{}, errors.New("SQLite fingerprint has incomplete collections")
		}
	}
	var schema sqliteFingerprint
	if err := decodeStrictJSON(raw, &schema, '{'); err != nil {
		return sqliteFingerprint{}, err
	}
	if !validActiveSQLiteFingerprint(schema) {
		return sqliteFingerprint{}, errors.New("SQLite fingerprint does not describe an active Beads schema")
	}
	return schema, nil
}

func validActiveSQLiteFingerprint(schema sqliteFingerprint) bool {
	if len(schema.Objects) == 0 || len(schema.Tables) == 0 ||
		len(schema.Pragmas) != len(fingerprintPragmas) || schema.MigrationLedgers == nil {
		return false
	}
	tableObjects := make(map[string]bool)
	previous := ""
	for _, object := range schema.Objects {
		key := object.Type + "\x00" + object.Name + "\x00" + object.Table
		if object.Name == "" || object.Table == "" || previous >= key {
			return false
		}
		switch object.Type {
		case "index", "table", "trigger", "view":
		default:
			return false
		}
		if object.Type == "table" {
			tableObjects[object.Name] = true
		}
		previous = key
	}
	previous = ""
	columnsByTable := make(map[string][]string, len(schema.Tables))
	for _, table := range schema.Tables {
		if table.Name == "" || previous >= table.Name || !tableObjects[table.Name] ||
			len(table.Columns) == 0 || table.ForeignKeys == nil || table.Indexes == nil {
			return false
		}
		if !sqliteColumnsSorted(table.Columns) || !sqliteForeignKeysSorted(table.ForeignKeys) || !sqliteIndexesSorted(table.Indexes) {
			return false
		}
		columns := make([]string, len(table.Columns))
		for index, column := range table.Columns {
			columns[index] = column.Name
		}
		columnsByTable[table.Name] = columns
		for _, index := range table.Indexes {
			if index.Name == "" || index.Columns == nil || !sqliteIndexColumnsSorted(index.Columns) {
				return false
			}
		}
		previous = table.Name
	}
	for index, pragma := range schema.Pragmas {
		if pragma.Name != fingerprintPragmas[index] || !validSQLiteFingerprintValue(pragma.Value) {
			return false
		}
	}
	previous = ""
	for _, ledger := range schema.MigrationLedgers {
		if ledger.Table == "" || previous >= ledger.Table || len(ledger.Columns) == 0 {
			return false
		}
		columns, found := columnsByTable[ledger.Table]
		if !found || !sameStringOrder(ledger.Columns, columns) || !sqliteRowsSorted(ledger.Rows) {
			return false
		}
		for _, row := range ledger.Rows {
			if len(row) != len(ledger.Columns) {
				return false
			}
			for _, value := range row {
				if !validSQLiteFingerprintValue(value) {
					return false
				}
			}
		}
		previous = ledger.Table
	}
	return true
}

func doltRowsSorted(rows [][]doltValue) bool {
	for index := 1; index < len(rows); index++ {
		if compareDoltRows(rows[index-1], rows[index]) > 0 {
			return false
		}
	}
	return true
}

func sqliteRowsSorted(rows [][]sqliteValue) bool {
	for index := 1; index < len(rows); index++ {
		if compareSQLiteRows(rows[index-1], rows[index]) > 0 {
			return false
		}
	}
	return true
}

func sameStringOrder(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sqliteColumnsSorted(columns []sqliteColumn) bool {
	for index := 1; index < len(columns); index++ {
		left, right := columns[index-1], columns[index]
		if left.CID > right.CID || left.CID == right.CID && left.Name >= right.Name {
			return false
		}
	}
	return true
}

func sqliteForeignKeysSorted(keys []sqliteForeignKey) bool {
	for index := 1; index < len(keys); index++ {
		left, right := keys[index-1], keys[index]
		if left.ID > right.ID ||
			left.ID == right.ID && (left.Sequence > right.Sequence ||
				left.Sequence == right.Sequence && (left.Table > right.Table ||
					left.Table == right.Table && left.From >= right.From)) {
			return false
		}
	}
	return true
}

func sqliteIndexesSorted(indexes []sqliteIndex) bool {
	for index := 1; index < len(indexes); index++ {
		left, right := indexes[index-1], indexes[index]
		if left.Name > right.Name || left.Name == right.Name && left.Sequence >= right.Sequence {
			return false
		}
	}
	return true
}

func sqliteIndexColumnsSorted(columns []sqliteIndexInfo) bool {
	for index := 1; index < len(columns); index++ {
		left, right := columns[index-1], columns[index]
		if left.Sequence > right.Sequence || left.Sequence == right.Sequence && left.CID >= right.CID {
			return false
		}
	}
	return true
}

func validSQLiteFingerprintValue(value sqliteValue) bool {
	switch value.Type {
	case "null":
		return value.Value == ""
	case "integer", "real", "text", "blob":
		return true
	default:
		return false
	}
}

func decodeJSONLDialect(raw json.RawMessage) (jsonlDialect, error) {
	var encoded struct {
		Records json.RawMessage `json:"records"`
	}
	if err := decodeStrictJSON(raw, &encoded, '{'); err != nil {
		return jsonlDialect{}, err
	}
	records := bytes.TrimSpace(encoded.Records)
	if len(records) == 0 || records[0] != '[' {
		return jsonlDialect{}, errors.New("JSONL dialect has no records collection")
	}
	var dialect jsonlDialect
	if err := decodeStrictJSON(raw, &dialect, '{'); err != nil {
		return jsonlDialect{}, err
	}
	if !validJSONLDialect(dialect) {
		return jsonlDialect{}, errors.New("JSONL dialect is empty or noncanonical")
	}
	return dialect, nil
}

func validJSONLDialect(dialect jsonlDialect) bool {
	if len(dialect.Records) == 0 {
		return false
	}
	previousRecord := ""
	for _, record := range dialect.Records {
		if len(record.Fields) == 0 {
			return false
		}
		switch record.Kind {
		case "schema-header":
			if record.Schema == "" || record.Type != "" {
				return false
			}
		case "typed-record":
			if record.Type == "" || record.Schema != "" {
				return false
			}
		case "flat-record":
			if record.Schema != "" || record.Type != "" {
				return false
			}
		default:
			return false
		}
		previousField := ""
		for _, field := range record.Fields {
			if field.Name == "" || previousField >= field.Name {
				return false
			}
			switch field.Type {
			case "null", "boolean", "number", "string", "array", "object":
			default:
				return false
			}
			previousField = field.Name
		}
		key := jsonlRecordDialectKey(record)
		if previousRecord >= key {
			return false
		}
		previousRecord = key
	}
	return true
}

func decodeStrictJSON(raw json.RawMessage, destination any, prefix byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != prefix {
		return errors.New("invalid JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

type topologyMarkers struct {
	database, data, legacy, embedded, sqliteBackups, sqliteCoexisting                                        bool
	localVersionSeen, providerOK, providerSeen, doltModeSeen, metadataDatabaseSeen, metadataDoltDatabaseSeen bool
	localVersion, provider, doltMode, metadataDatabase, metadataDoltDatabase                                 string
}

func classifyTopology(topology []string) (topologyMarkers, error) {
	markers := topologyMarkers{providerOK: true}
	for _, marker := range topology {
		switch {
		case strings.HasPrefix(marker, "local-version:"):
			if markers.localVersionSeen {
				return markers, errors.New("multiple local version markers")
			}
			if marker != "local-version:absent-or-invalid" && marker != "local-version:legacy-server" && marker != "local-version:other-valid" {
				return markers, fmt.Errorf("unknown topology marker %q", marker)
			}
			markers.localVersionSeen = true
			markers.localVersion = marker
		case strings.HasPrefix(marker, "database:.beads/"):
			if markers.database {
				return markers, errors.New("multiple database roots")
			}
			markers.database = true
		case marker == "data:.beads/issues.jsonl":
			markers.data = true
		case marker == "directory:.beads/dolt":
			markers.legacy = true
		case marker == "directory:.beads/embeddeddolt":
			markers.embedded = true
		case marker == "sqlite-backups:pre-dolt":
			markers.sqliteBackups = true
		case marker == "sqlite-coexisting:.beads/beads.db":
			markers.sqliteCoexisting = true
		case strings.HasPrefix(marker, "metadata-database:"):
			if markers.metadataDatabaseSeen {
				return markers, errors.New("multiple metadata database markers")
			}
			markers.metadataDatabaseSeen = true
			markers.metadataDatabase = strings.TrimPrefix(marker, "metadata-database:")
			if markers.metadataDatabase != "beads.db" && markers.metadataDatabase != "dolt" {
				return markers, fmt.Errorf("unknown metadata database marker %q", marker)
			}
		case strings.HasPrefix(marker, "metadata-dolt-database:"):
			if markers.metadataDoltDatabaseSeen {
				return markers, errors.New("multiple metadata Dolt database markers")
			}
			markers.metadataDoltDatabaseSeen = true
			markers.metadataDoltDatabase = strings.TrimPrefix(marker, "metadata-dolt-database:")
			if markers.metadataDoltDatabase == "" || isDoltSystemDatabase(markers.metadataDoltDatabase) {
				return markers, fmt.Errorf("invalid metadata Dolt database marker %q", marker)
			}
		case strings.HasPrefix(marker, "metadata-backend:"):
			if markers.providerSeen {
				return markers, errors.New("multiple metadata backend markers")
			}
			markers.providerSeen = true
			markers.provider = strings.TrimPrefix(marker, "metadata-backend:")
			markers.providerOK = markers.provider == "dolt"
		case strings.HasPrefix(marker, "metadata-dolt-mode:"):
			if markers.doltModeSeen {
				return markers, errors.New("multiple metadata Dolt mode markers")
			}
			markers.doltModeSeen = true
			markers.doltMode = strings.TrimPrefix(marker, "metadata-dolt-mode:")
		default:
			return markers, fmt.Errorf("unknown topology marker %q", marker)
		}
	}
	return markers, nil
}

func hasDuplicate(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i-1] == values[i] {
			return true
		}
	}
	return false
}

func encodeRouteManifest(manifest routeManifest) ([]byte, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func routeSummary(result census, manifest routeManifest) []byte {
	routes := map[string]string{}
	for _, group := range manifest.Groups {
		for _, entry := range group.Families {
			routes[entry.FamilyID] = group.Route
		}
	}
	fresh, rolling := map[string]int{}, map[string]int{}
	for _, observed := range result.Observations {
		if strings.HasPrefix(observed.Scenario, "fresh") {
			fresh[observed.FamilyID]++
		}
	}
	for _, transition := range result.Transitions {
		rolling[transition.FromFamilyID]++
		rolling[transition.ToFamilyID]++
	}
	for _, outcome := range result.Outcomes {
		rolling[outcome.FromFamilyID]++
		if outcome.ToFamilyID != "" {
			rolling[outcome.ToFamilyID]++
		}
	}
	var output strings.Builder
	output.WriteString(routeSummaryTitle + "\n\n" + routeSummaryDisclaimer + "\n\n| Family ID | Mode | Topology | Route | Fresh | Rolling |\n|---|---|---|---|---:|---:|\n")
	for _, candidate := range result.Families {
		fmt.Fprintf(&output, "| %s | %s | %s | %s | %d | %d |\n", candidate.ID, candidate.Mode, strings.Join(topologyForSummary(candidate), ", "), routes[candidate.ID], fresh[candidate.ID], rolling[candidate.ID]) //nolint:gosec // every rendered field has passed the strict census and route validators.
	}
	return []byte(output.String())
}

func topologyForSummary(candidate family) []string {
	var layout struct {
		Topology []string `json:"topology"`
	}
	_ = json.Unmarshal(candidate.Layout, &layout)
	return layout.Topology
}

func requireExactFile(path string, want []byte) error {
	got, err := os.ReadFile(path) //nolint:gosec // path is a caller-selected artifact path checked against regenerated bytes.
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return errors.New("does not match regenerated bytes")
	}
	return nil
}
