// Command census creates and validates a deterministic runtime schema census.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/atomicfile"
)

const (
	censusSchemaVersion       = 1
	fingerprintSpecVersion    = 1
	freshScenario             = "fresh-default"
	modulePath                = "github.com/steveyegge/beads"
	pinnedCatalogSHA256       = "298dd489a6274d80ac42e1fb14c993444159f3193a290f8da37afcb3e2eaf10d"
	goToolchain               = "go1.26.5"
	pinnedGCCVersion          = "gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0"
	pinnedLibc6Version        = "2.39-0ubuntu8.7"
	sourceBuildRecipeVersion  = 1
	localVersionMaxBytes      = 64
	storageMetadataMaxBytes   = 64 << 10
	maxReleaseBinaryBytes     = 256 << 20
	maxSourceZipExpandedBytes = 512 << 20
)

type catalog struct {
	SchemaVersion int            `json:"schema_version"`
	Module        string         `json:"module"`
	Versions      []catalogEntry `json:"versions"`
}

type catalogEntry struct {
	Version       string           `json:"version"`
	Sum           string           `json:"sum"`
	GoModSum      string           `json:"go_mod_sum"`
	Origin        catalogOrigin    `json:"origin"`
	SourceZip     catalogSourceZip `json:"source_zip"`
	GitHubRelease *catalogRelease  `json:"github_release,omitempty"`
}

type catalogOrigin struct {
	Hash string `json:"hash"`
	Ref  string `json:"ref"`
}

type catalogSourceZip struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type catalogRelease struct {
	SourceRelation  string        `json:"source_relation"`
	LinuxAMD64Asset *catalogAsset `json:"linux_amd64_asset,omitempty"`
}

type catalogAsset struct {
	Size   int64  `json:"size"`
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type census struct {
	SchemaVersion          int                 `json:"schema_version"`
	FingerprintSpecVersion int                 `json:"fingerprint_spec_version"`
	CatalogSHA256          string              `json:"catalog_sha256"`
	Observations           []observation       `json:"observations"`
	Families               []family            `json:"families"`
	Transitions            []lineageTransition `json:"transitions"`
	Outcomes               []lineageOutcome    `json:"outcomes"`
}

type observation struct {
	Version     string      `json:"version"`
	Scenario    string      `json:"scenario"`
	Provenance  provenance  `json:"provenance"`
	Acquisition acquisition `json:"acquisition"`
	FamilyID    string      `json:"family_id"`
}

type acquisition struct {
	Kind                string `json:"kind"`
	ExecutableSHA256    string `json:"executable_sha256,omitempty"`
	BuildIdentitySHA256 string `json:"build_identity_sha256,omitempty"`
	GoToolchain         string `json:"go_toolchain,omitempty"`
	AssetFallback       string `json:"asset_fallback,omitempty"`
}

// UnmarshalJSON keeps acquisition as a strict discriminated union. In
// particular, a source-build record never persists the digest of an output
// binary, which is not reproducible across otherwise equivalent builds.
func (value *acquisition) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for name := range fields {
		switch name {
		case "kind", "executable_sha256", "build_identity_sha256", "go_toolchain", "asset_fallback":
		default:
			return fmt.Errorf("unknown acquisition field %q", name)
		}
	}
	type wire acquisition
	var decoded wire
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*value = acquisition(decoded)
	if !strictAcquisitionFields(*value, fields) || !validAcquisition(*value) {
		return errors.New("invalid acquisition")
	}
	return nil
}

func strictAcquisitionFields(value acquisition, fields map[string]json.RawMessage) bool {
	has := func(name string) bool { _, ok := fields[name]; return ok }
	switch value.Kind {
	case "release-asset":
		return len(fields) == 2 && has("kind") && has("executable_sha256")
	case "source-build":
		return len(fields) >= 3 && len(fields) <= 4 && has("kind") && has("build_identity_sha256") && has("go_toolchain") &&
			(!has("asset_fallback") || value.AssetFallback != "")
	default:
		return false
	}
}

type historicalProcessExitError struct{ err error }

func (err historicalProcessExitError) Error() string { return err.err.Error() }
func (err historicalProcessExitError) Unwrap() error { return err.err }

func historicalProcessExit(err error) error { return historicalProcessExitError{err: err} }

func isHistoricalProcessExit(err error) bool {
	var exit historicalProcessExitError
	return errors.As(err, &exit)
}

type assetFallbackEligibleError struct{ err error }

func (err assetFallbackEligibleError) Error() string { return err.err.Error() }
func (err assetFallbackEligibleError) Unwrap() error { return err.err }

func assetFallbackEligible(err error) error { return assetFallbackEligibleError{err: err} }

func isAssetFallbackEligible(err error) bool {
	var eligible assetFallbackEligibleError
	return errors.As(err, &eligible)
}

func shouldFallbackToSource(acquired acquisition, err error) bool {
	return acquired.Kind == "release-asset" && isAssetFallbackEligible(err)
}

func eligibleAssetProcessFailure(acquired acquisition, ctx context.Context, err error) error {
	if acquired.Kind != "release-asset" || ctx.Err() != nil {
		return err
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return assetFallbackEligible(err)
	}
	return err
}

type provenance struct {
	Sum      string        `json:"sum"`
	GoModSum string        `json:"go_mod_sum"`
	Origin   catalogOrigin `json:"origin"`
}

// family deliberately excludes version and provenance. Its ID must continue to
// identify only observable schema/layout and storage mode.
type family struct {
	ID     string          `json:"id"`
	Mode   string          `json:"mode"`
	Layout json.RawMessage `json:"layout"`
}

type moduleDownload struct {
	Path     string
	Version  string
	Sum      string
	GoModSum string
	Zip      string
	Dir      string
	Origin   catalogOrigin
}

type freshBinary struct {
	path             string
	executableSHA256 string
	acquisition      acquisition
}

type freshBinaryKey struct {
	version string
	mode    string
}

type freshBinariesContextKey struct{}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: census validate <catalog.json> <census.json> | census generate <catalog.json> <census.json> [cache-dir] | census cache-validate <catalog.json> <cache-dir> | census promote-evidence <catalog.json> <raw.json> <trusted.json> <diagnostic.json> | census seal <catalog.json> <census.json> <evidence.gz> <routes.json> <summary.md> | census verify <catalog.json> <evidence.gz> <routes.json> <summary.md>")
	}
	switch args[0] {
	case "validate":
		if len(args) != 3 {
			return errors.New("usage: census validate <catalog.json> <census.json>")
		}
		return validateFiles(args[1], args[2], true)
	case "generate":
		if len(args) < 3 || len(args) > 4 {
			return errors.New("usage: census generate <catalog.json> <census.json> [cache-dir]")
		}
		cache, err := defaultCacheDir()
		if err != nil {
			return err
		}
		if len(args) == 4 {
			cache = args[3]
		}
		return generate(context.Background(), args[1], args[2], cache)
	case "cache-validate":
		if len(args) != 3 {
			return errors.New("usage: census cache-validate <catalog.json> <cache-dir>")
		}
		catalog, _, err := readCatalog(args[1], true)
		if err != nil {
			return err
		}
		return validateSourceBuildCache(args[2], catalog)
	case "promote-evidence":
		if len(args) != 5 {
			return errors.New("usage: census promote-evidence <catalog.json> <raw.json> <trusted.json> <diagnostic.json>")
		}
		return promoteEvidence(args[1], args[2], args[3], args[4])
	case "seal":
		if len(args) != 6 {
			return errors.New("usage: census seal <catalog.json> <census.json> <evidence.gz> <routes.json> <summary.md>")
		}
		return sealArtifacts(args[1], args[2], args[3], args[4], args[5], true)
	case "verify":
		if len(args) != 5 {
			return errors.New("usage: census verify <catalog.json> <evidence.gz> <routes.json> <summary.md>")
		}
		return verifyArtifacts(args[1], args[2], args[3], args[4], true)
	default:
		return fmt.Errorf("unknown census command %q", args[0])
	}
}

func defaultCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".cache", "beads-schema-census"), nil
}

func generate(ctx context.Context, catalogPath, outputPath, cache string) (err error) {
	catalog, raw, err := prepareFreshGeneration(catalogPath, cache)
	if err != nil {
		return err
	}
	observations := make([]observation, 0, len(catalog.Versions))
	families := map[string]family{}
	freshBinaries := make(map[freshBinaryKey]freshBinary, len(catalog.Versions))
	for _, entry := range catalog.Versions {
		entryObservations, entryFamilies, binariesByAcquisition, err := acquireEntryObservations(ctx, entry, cache)
		if err != nil {
			return err
		}
		if err := bindEntryFreshBinaries(freshBinaries, entry.Version, entryObservations, entryFamilies, binariesByAcquisition); err != nil {
			return err
		}
		observations = append(observations, entryObservations...)
		for _, observedFamily := range entryFamilies {
			families[observedFamily.ID] = observedFamily
		}
	}
	result := census{
		SchemaVersion: censusSchemaVersion, FingerprintSpecVersion: fingerprintSpecVersion,
		CatalogSHA256: digest(raw), Observations: observations, Families: make([]family, 0, len(families)),
		Transitions: []lineageTransition{},
		Outcomes:    []lineageOutcome{},
	}
	for _, observedFamily := range families {
		result.Families = append(result.Families, observedFamily)
	}
	sortObservations(result.Observations)
	sort.Slice(result.Families, func(i, j int) bool { return result.Families[i].ID < result.Families[j].ID })
	if err := validateFreshCensus(result, catalog); err != nil {
		return err
	}
	if err := generateRollingCensus(withFreshBinaries(ctx, freshBinaries), catalog, &result, cache); err != nil {
		return err
	}
	if err := validateCensus(result, catalog); err != nil {
		return err
	}
	raw, err = encodeCensus(result)
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(outputPath, raw, 0o644) //nolint:gosec // output is an explicit CLI argument.
}

// prepareFreshGeneration runs the census generation preflight guards and loads
// the pinned catalog, returning the parsed catalog and its raw bytes.
func prepareFreshGeneration(catalogPath, cache string) (catalog, []byte, error) {
	if err := requireCensusPlatform(runtime.GOOS, runtime.GOARCH); err != nil {
		return catalog{}, nil, err
	}
	if err := enableHistoricalProcessContainment(); err != nil {
		return catalog{}, nil, err
	}
	if err := requireHistoricalDoltAutoDetectPortsFree(3306, 3307); err != nil {
		return catalog{}, nil, err
	}
	parsed, raw, err := readCatalog(catalogPath, true)
	if err != nil {
		return catalog{}, nil, err
	}
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return catalog{}, nil, fmt.Errorf("create census cache: %w", err)
	}
	if err := validateSourceBuildCache(cache, parsed); err != nil {
		return catalog{}, nil, fmt.Errorf("validate census cache before generation: %w", err)
	}
	return parsed, raw, nil
}

// acquireEntryObservations observes one catalog entry's fresh release, falling
// back to an authenticated source build when a release-asset runtime fails. It
// returns the entry's observations and families plus the fresh binaries keyed by
// the acquisition that produced them.
func acquireEntryObservations(ctx context.Context, entry catalogEntry, cache string) ([]observation, []family, map[acquisition]freshBinary, error) {
	binary, acquired, err := acquireBinary(ctx, entry, cache)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: acquire binary: %w", entry.Version, err)
	}
	fresh, err := bindFreshBinary(binary, acquired)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: bind fresh binary: %w", entry.Version, err)
	}
	binariesByAcquisition := map[acquisition]freshBinary{acquired: fresh}
	binary, err = verifyFreshBinary(fresh)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: verify fresh binary: %w", entry.Version, err)
	}
	releaseContext := withHistoricalBinaryBinding(ctx, fresh)
	releaseObservations, releaseFamilies, failures, err := observeFreshRelease(releaseContext, binary, entry, acquired)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: fresh observations: %w", entry.Version, err)
	}
	if failures.any() && acquired.Kind == "release-asset" {
		fallback, err := retryEntrySourceBuild(ctx, entry, cache, failures, releaseObservations)
		if err != nil {
			return nil, nil, nil, err
		}
		binariesByAcquisition[fallback.acquired] = fallback.fresh
		releaseObservations = append(releaseObservations, fallback.observations...)
		releaseFamilies = append(releaseFamilies, fallback.families...)
	}
	return releaseObservations, releaseFamilies, binariesByAcquisition, nil
}

// entrySourceFallback carries the authenticated source-build retry results for a
// catalog entry whose release-asset runtime failed.
type entrySourceFallback struct {
	observations []observation
	families     []family
	acquired     acquisition
	fresh        freshBinary
}

// retryEntrySourceBuild acquires and verifies an authenticated source build for
// an entry whose release-asset runtime failed, then retries the failed fresh
// observations against it.
func retryEntrySourceBuild(ctx context.Context, entry catalogEntry, cache string, failures freshObservationFailures, releaseObservations []observation) (entrySourceFallback, error) {
	sourceBinary, err := acquireSourceBuild(ctx, entry, cache)
	if err != nil {
		return entrySourceFallback{}, fmt.Errorf("%s: acquire authenticated source fallback: %w", entry.Version, err)
	}
	sourceAcquired, err := recordAcquisition("source-build", entry, sourceBinary, "release-asset-runtime-failure")
	if err != nil {
		return entrySourceFallback{}, fmt.Errorf("%s: record authenticated source fallback: %w", entry.Version, err)
	}
	sourceFresh, err := bindFreshBinary(sourceBinary, sourceAcquired)
	if err != nil {
		return entrySourceFallback{}, fmt.Errorf("%s: bind authenticated source fallback: %w", entry.Version, err)
	}
	sourceBinary, err = verifyFreshBinary(sourceFresh)
	if err != nil {
		return entrySourceFallback{}, fmt.Errorf("%s: verify authenticated source fallback: %w", entry.Version, err)
	}
	sourceContext := withHistoricalBinaryBinding(ctx, sourceFresh)
	fallbackObservations, fallbackFamilies, err := retryFreshFailures(sourceContext, sourceBinary, entry, sourceAcquired, failures, releaseObservations)
	if err != nil {
		return entrySourceFallback{}, fmt.Errorf("%s: authenticated source fallback failed: %w", entry.Version, err)
	}
	return entrySourceFallback{
		observations: fallbackObservations,
		families:     fallbackFamilies,
		acquired:     sourceAcquired,
		fresh:        sourceFresh,
	}, nil
}

// bindEntryFreshBinaries records, for each of an entry's observations, the fresh
// binary that produced its family+mode, rejecting unbindable observations and
// cross-entry acquisition disagreements.
func bindEntryFreshBinaries(freshBinaries map[freshBinaryKey]freshBinary, entryVersion string, observations []observation, families []family, binariesByAcquisition map[acquisition]freshBinary) error {
	familyModes := make(map[string]string, len(families))
	for _, observedFamily := range families {
		familyModes[observedFamily.ID] = observedFamily.Mode
	}
	for _, observed := range observations {
		mode := familyModes[observed.FamilyID]
		fresh, found := binariesByAcquisition[observed.Acquisition]
		if mode == "" || !found {
			return fmt.Errorf("%s/%s: cannot bind fresh binary to observed family", entryVersion, observed.Scenario)
		}
		key := freshBinaryKey{version: observed.Version, mode: mode}
		if prior, exists := freshBinaries[key]; exists && prior.acquisition != observed.Acquisition {
			return fmt.Errorf("%s/%s: fresh binaries disagree on acquisition", observed.Version, mode)
		}
		freshBinaries[key] = fresh
	}
	return nil
}

func requireCensusPlatform(goos, goarch string) error {
	if goos != "linux" || goarch != "amd64" {
		return fmt.Errorf("census generation requires linux/amd64, got %s/%s", goos, goarch)
	}
	return nil
}

func withFreshBinaries(ctx context.Context, binaries map[freshBinaryKey]freshBinary) context.Context {
	copy := make(map[freshBinaryKey]freshBinary, len(binaries))
	bindings := make([]freshBinary, 0, len(binaries))
	for key, binary := range binaries {
		copy[key] = binary
		bindings = append(bindings, binary)
	}
	return withHistoricalBinaryBindings(context.WithValue(ctx, freshBinariesContextKey{}, copy), bindings)
}

func freshBinaryFor(ctx context.Context, version, mode string, acquired acquisition) (freshBinary, bool) {
	binaries, ok := ctx.Value(freshBinariesContextKey{}).(map[freshBinaryKey]freshBinary)
	if !ok {
		return freshBinary{}, false
	}
	binary, ok := binaries[freshBinaryKey{version: version, mode: mode}]
	if !ok || binary.acquisition != acquired {
		return freshBinary{}, false
	}
	return binary, true
}

// observeFreshRelease records all successful default and explicit scenarios.
// Failed scenarios remain eligible for an authenticated source fallback.
type freshObservationFailures struct {
	defaultFailed bool
	scenarios     []scenarioSpec
}

func (failures freshObservationFailures) any() bool {
	return failures.defaultFailed || len(failures.scenarios) != 0
}

func observeFreshRelease(ctx context.Context, binary string, entry catalogEntry, acquired acquisition) ([]observation, []family, freshObservationFailures, error) {
	required, err := freshScenariosForVersion(entry.Version)
	if err != nil {
		return nil, nil, freshObservationFailures{}, err
	}
	failures := freshObservationFailures{}
	observations := make([]observation, 0, len(required)+1)
	families := make([]family, 0, len(required)+1)
	if isFreshDefaultJSONLVersion(entry.Version) {
		defaultObservation, defaultFamily, defaultErr := observeFresh(ctx, binary, entry, acquired)
		if defaultErr != nil {
			if !shouldFallbackToSource(acquired, defaultErr) {
				return nil, nil, failures, defaultErr
			}
			failures.defaultFailed = true
		} else {
			if defaultFamily.Mode != "jsonl" {
				return nil, nil, failures, fmt.Errorf("default storage mode = %q, want jsonl", defaultFamily.Mode)
			}
			observations = append(observations, defaultObservation)
			families = append(families, defaultFamily)
		}
	}
	for _, scenario := range required {
		observed, observedFamily, err := observeFreshScenario(ctx, binary, entry, acquired, scenario)
		if err != nil {
			if !shouldFallbackToSource(acquired, err) {
				return nil, nil, failures, err
			}
			failures.scenarios = append(failures.scenarios, scenario)
			continue
		}
		observations = append(observations, observed)
		families = append(families, observedFamily)
	}
	return observations, families, failures, nil
}

func retryFreshFailures(ctx context.Context, binary string, entry catalogEntry, acquired acquisition, failures freshObservationFailures, existing []observation) ([]observation, []family, error) {
	seen := make(map[string]bool, len(existing))
	for _, observed := range existing {
		seen[observed.Scenario] = true
	}
	observations := make([]observation, 0, len(failures.scenarios)+1)
	families := make([]family, 0, len(failures.scenarios)+1)
	if failures.defaultFailed {
		observed, observedFamily, err := observeFresh(ctx, binary, entry, acquired)
		if err != nil {
			return nil, nil, fmt.Errorf("default: %w", err)
		}
		if observedFamily.Mode != "jsonl" {
			return nil, nil, fmt.Errorf("default storage mode = %q, want jsonl", observedFamily.Mode)
		}
		if !seen[observed.Scenario] {
			seen[observed.Scenario] = true
			observations = append(observations, observed)
			families = append(families, observedFamily)
		}
	}
	for _, scenario := range failures.scenarios {
		if seen[scenario.Name] {
			continue
		}
		observed, observedFamily, err := observeFreshScenario(ctx, binary, entry, acquired, scenario)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", scenario.Name, err)
		}
		seen[scenario.Name] = true
		observations = append(observations, observed)
		families = append(families, observedFamily)
	}
	return observations, families, nil
}

func validateFiles(catalogPath, censusPath string, requirePinnedCatalog bool) error {
	catalog, catalogRaw, err := readCatalog(catalogPath, requirePinnedCatalog)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(censusPath) //nolint:gosec // census is an explicit CLI argument.
	if err != nil {
		return err
	}
	result, err := decodeCensus(raw)
	if err != nil {
		return err
	}
	if result.CatalogSHA256 != digest(catalogRaw) {
		return errors.New("census catalog digest does not match catalog")
	}
	return validateCensus(result, catalog)
}

func readCatalog(path string, requirePinned bool) (catalog, []byte, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // catalog is an explicit CLI argument.
	if err != nil {
		return catalog{}, nil, err
	}
	if requirePinned && digest(raw) != pinnedCatalogSHA256 {
		return catalog{}, nil, fmt.Errorf("catalog identity digest = %s, want %s", digest(raw), pinnedCatalogSHA256)
	}
	var result catalog
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&result); err != nil {
		return catalog{}, nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return catalog{}, nil, errors.New("catalog has trailing JSON data")
	}
	if err := validateCatalog(result); err != nil {
		return catalog{}, nil, err
	}
	return result, raw, nil
}

func validateCatalog(c catalog) error {
	if c.SchemaVersion != 1 || c.Module != modulePath || len(c.Versions) == 0 {
		return errors.New("catalog header does not describe the beads release universe")
	}
	seen := make(map[string]bool, len(c.Versions))
	for _, entry := range c.Versions {
		if entry.Version == "" || entry.Sum == "" || entry.GoModSum == "" || entry.Origin.Hash == "" || entry.Origin.Ref == "" || seen[entry.Version] {
			return fmt.Errorf("invalid or duplicate catalog entry %q", entry.Version)
		}
		seen[entry.Version] = true
	}
	return nil
}

func decodeCensus(raw []byte) (census, error) {
	var result census
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return census{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return census{}, errors.New("census has trailing JSON data")
	}
	canonical, err := encodeCensus(result)
	if err != nil {
		return census{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return census{}, errors.New("census JSON is not canonical")
	}
	return result, nil
}

func encodeCensus(result census) ([]byte, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func encodeCatalog(c catalog) ([]byte, error) {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func validateCensus(result census, c catalog) error {
	if err := validateFreshCensus(result, c); err != nil {
		return err
	}
	familyByID := censusFamilyMap(result)
	if err := validateRollingLineageCoverage(result, c, familyByID); err != nil {
		return err
	}
	return validateRollingReferences(result, c, familyByID)
}

// validateFreshCensus verifies a newly generated fresh census before rolling
// has populated the required migration-attempt records. Offline validation
// must call validateCensus, which adds rolling-frontier totality checks.
func validateFreshCensus(result census, c catalog) error {
	if result.SchemaVersion != censusSchemaVersion ||
		result.FingerprintSpecVersion != fingerprintSpecVersion ||
		result.CatalogSHA256 == "" {
		return errors.New("census header is invalid")
	}
	if err := validateReleaseOrder(c); err != nil {
		return err
	}
	byVersion := make(map[string]catalogEntry, len(c.Versions))
	for _, entry := range c.Versions {
		byVersion[entry.Version] = entry
	}
	familyByID := make(map[string]family, len(result.Families))
	for i, candidate := range result.Families {
		if !validMode(candidate.Mode) || candidate.ID == "" || (i > 0 && result.Families[i-1].ID >= candidate.ID) {
			return errors.New("families are not canonically sorted")
		}
		canonicalLayout, err := canonicalJSON(candidate.Layout)
		if err != nil || !bytes.Equal(candidate.Layout, canonicalLayout) {
			return fmt.Errorf("family %s: layout is not canonical JSON", candidate.ID)
		}
		if err := validateFamilyFingerprintPayload(candidate); err != nil {
			return fmt.Errorf("family %s (mode %q; observations: %s): %w",
				candidate.ID, candidate.Mode, strings.Join(familyObservationReferences(result.Observations, candidate.ID), ", "), err)
		}
		id, err := familyIDFromCanonicalLayout(candidate.Mode, canonicalLayout)
		if err != nil || id != candidate.ID || familyByID[id].ID != "" {
			return fmt.Errorf("family %s: ID does not match semantic mode/layout", candidate.ID)
		}
		familyByID[id] = candidate
	}
	seen := make(map[string]bool, len(result.Observations))
	acquisitions := make(map[string]acquisition, len(c.Versions))
	for index, observation := range result.Observations {
		if index > 0 && compareObservations(result.Observations[index-1], observation) >= 0 {
			return errors.New("observations are not canonically sorted")
		}
		entry, ok := byVersion[observation.Version]
		key := observation.Version + "\x00" + observation.Scenario
		if !ok || observation.Scenario == "" || seen[key] {
			return fmt.Errorf("observation is not a unique catalog version/scenario: %q/%q", observation.Version, observation.Scenario)
		}
		seen[key] = true
		if observation.Provenance != provenanceFromEntry(entry) {
			return fmt.Errorf("%s: provenance differs from catalog", observation.Version)
		}
		if !validAcquisitionForEntry(observation.Acquisition, entry) ||
			(observation.Acquisition.Kind == "release-asset" && !releaseAssetMatchesProxyOrigin(entry)) {
			return fmt.Errorf("%s: invalid acquisition", observation.Version)
		}
		observedFamily, ok := familyByID[observation.FamilyID]
		if !ok {
			return fmt.Errorf("%s: observation references an unknown family", observation.Version)
		}
		if err := recordConsistentAcquisition(acquisitions, observation.Version, observedFamily.Mode, observation.Acquisition); err != nil {
			return err
		}
		if scenario, isFreshScenario := freshScenarioByName(observation.Scenario); isFreshScenario {
			if observedFamily.Mode != scenario.Mode {
				return fmt.Errorf("%s/%s: family mode %q differs from declared scenario mode %q",
					observation.Version, observation.Scenario, observedFamily.Mode, scenario.Mode)
			}
		} else if observation.Scenario != freshScenario || observedFamily.Mode != "jsonl" {
			return fmt.Errorf("%s/%s: observation scenario is not a declared fresh scenario or JSONL fresh-default", observation.Version, observation.Scenario)
		}
	}
	if err := validateFreshScenarioCoverage(result.Observations, c); err != nil {
		return err
	}
	return validateFreshDefaultJSONLCoverage(result.Observations, c)
}

func familyObservationReferences(observations []observation, familyID string) []string {
	references := make([]observation, 0)
	for _, observed := range observations {
		if observed.FamilyID == familyID {
			references = append(references, observed)
		}
	}
	sortObservations(references)
	result := make([]string, len(references))
	for index, reference := range references {
		result[index] = reference.Version + "/" + reference.Scenario
	}
	return result
}

func compareObservations(left, right observation) int {
	leftVersion, leftErr := parseReleaseVersion(left.Version)
	rightVersion, rightErr := parseReleaseVersion(right.Version)
	if leftErr == nil && rightErr == nil {
		for index := range leftVersion {
			if leftVersion[index] < rightVersion[index] { //nolint:gosec // both operands are the same fixed-length releaseVersion array.
				return -1
			}
			if leftVersion[index] > rightVersion[index] { //nolint:gosec // both operands are the same fixed-length releaseVersion array.
				return 1
			}
		}
	} else if left.Version < right.Version {
		return -1
	} else if left.Version > right.Version {
		return 1
	}
	if left.Scenario < right.Scenario {
		return -1
	}
	if left.Scenario > right.Scenario {
		return 1
	}
	return 0
}

func sortObservations(observations []observation) {
	sort.Slice(observations, func(i, j int) bool {
		return compareObservations(observations[i], observations[j]) < 0
	})
}

func censusFamilyMap(result census) map[string]family {
	families := make(map[string]family, len(result.Families))
	for _, candidate := range result.Families {
		families[candidate.ID] = candidate
	}
	return families
}

func validateRollingReferences(result census, c catalog, familyByID map[string]family) error {
	byVersion := make(map[string]catalogEntry, len(c.Versions))
	for _, entry := range c.Versions {
		byVersion[entry.Version] = entry
	}
	usedFamilies := make(map[string]bool, len(result.Families))
	acquisitions := make(map[string]acquisition, len(c.Versions))
	for _, observation := range result.Observations {
		usedFamilies[observation.FamilyID] = true
		family, ok := familyByID[observation.FamilyID]
		if !ok {
			return fmt.Errorf("%s: observation references an unknown family", observation.Version)
		}
		if err := recordConsistentAcquisition(acquisitions, observation.Version, family.Mode, observation.Acquisition); err != nil {
			return err
		}
	}
	_, doltAcquisitions, _, err := freshDoltFamiliesByVersion(result)
	if err != nil {
		return err
	}
	for _, transition := range result.Transitions {
		entry := byVersion[transition.TargetVersion]
		if !validAcquisitionForEntry(transition.Acquisition, entry) ||
			(transition.Acquisition.Kind == "release-asset" && !releaseAssetMatchesProxyOrigin(entry)) {
			return fmt.Errorf("%s: invalid rolling acquisition", transition.TargetVersion)
		}
		if transition.Mode == "sqlite" {
			err = requireRollingSQLiteTargetAcquisition(transition.TargetVersion, acquisitions, doltAcquisitions, transition.Acquisition)
		} else if isRollingDoltMode(transition.Mode) {
			err = requireRollingDoltTargetAcquisition(doltAcquisitions, transition.TargetVersion, transition.Scenario, transition.Acquisition)
		} else {
			err = requireFreshAcquisition(acquisitions, transition.TargetVersion, transition.Mode, transition.Acquisition)
		}
		if err != nil {
			return err
		}
		usedFamilies[transition.FromFamilyID] = true
		usedFamilies[transition.ToFamilyID] = true
	}
	for _, outcome := range result.Outcomes {
		entry := byVersion[outcome.TargetVersion]
		if !validAcquisitionForEntry(outcome.Acquisition, entry) ||
			(outcome.Acquisition.Kind == "release-asset" && !releaseAssetMatchesProxyOrigin(entry)) {
			return fmt.Errorf("%s: invalid rolling acquisition", outcome.TargetVersion)
		}
		if outcome.Mode == "sqlite" {
			err = requireRollingSQLiteTargetAcquisition(outcome.TargetVersion, acquisitions, doltAcquisitions, outcome.Acquisition)
		} else if isRollingDoltMode(outcome.Mode) {
			err = requireRollingDoltTargetAcquisition(doltAcquisitions, outcome.TargetVersion, outcome.Scenario, outcome.Acquisition)
		} else {
			err = requireFreshAcquisition(acquisitions, outcome.TargetVersion, outcome.Mode, outcome.Acquisition)
		}
		if err != nil {
			return err
		}
		usedFamilies[outcome.FromFamilyID] = true
		if outcome.ToFamilyID != "" {
			usedFamilies[outcome.ToFamilyID] = true
		}
	}
	if len(usedFamilies) != len(familyByID) {
		return errors.New("census contains an unreferenced family")
	}
	return nil
}

func requireRollingSQLiteTargetAcquisition(version string, all map[string]acquisition, dolt map[string]map[string]acquisition, candidate acquisition) error {
	if expected, exists := all[acquisitionKey(version, "sqlite")]; exists {
		if expected != candidate {
			return fmt.Errorf("%s/sqlite: rolling acquisition differs from fresh acquisition", version)
		}
		return nil
	}
	expected, err := rollingSQLiteTargetAcquisition(version, nil, dolt)
	if err != nil {
		return err
	}
	if expected != candidate {
		return fmt.Errorf("%s/sqlite: rolling acquisition differs from authenticated target acquisition", version)
	}
	return nil
}

func acquisitionKey(version, mode string) string { return version + "\x00" + mode }

func recordConsistentAcquisition(acquisitions map[string]acquisition, version, mode string, candidate acquisition) error {
	key := acquisitionKey(version, mode)
	if prior, exists := acquisitions[key]; exists && prior != candidate {
		return fmt.Errorf("%s/%s: census records disagree on acquisition", version, mode)
	}
	acquisitions[key] = candidate
	return nil
}

func requireFreshAcquisition(acquisitions map[string]acquisition, version, mode string, candidate acquisition) error {
	key := acquisitionKey(version, mode)
	prior, exists := acquisitions[key]
	if !exists {
		return fmt.Errorf("%s/%s: rolling record has no fresh acquisition", version, mode)
	}
	if prior != candidate {
		return fmt.Errorf("%s/%s: rolling acquisition differs from fresh acquisition", version, mode)
	}
	return nil
}

func provenanceFromEntry(entry catalogEntry) provenance {
	return provenance{Sum: entry.Sum, GoModSum: entry.GoModSum, Origin: entry.Origin}
}

func validMode(mode string) bool {
	switch mode {
	case "sqlite", "jsonl", "dolt-legacy", "dolt-server", "dolt-embedded":
		return true
	default:
		return false
	}
}

func validAcquisition(value acquisition) bool {
	switch value.Kind {
	case "release-asset":
		return validSHA256(value.ExecutableSHA256) && value.BuildIdentitySHA256 == "" &&
			value.GoToolchain == "" && value.AssetFallback == ""
	case "source-build":
		return value.ExecutableSHA256 == "" && validSHA256(value.BuildIdentitySHA256) && value.GoToolchain == goToolchain &&
			(value.AssetFallback == "" || value.AssetFallback == "release-asset-runtime-failure")
	default:
		return false
	}
}

func validAcquisitionForEntry(value acquisition, entry catalogEntry) bool {
	if !validAcquisition(value) {
		return false
	}
	if value.Kind != "source-build" {
		return true
	}
	identity, err := sourceBuildIdentity(entry)
	return err == nil && value.BuildIdentitySHA256 == identity
}

type sourceBuildRecipeSpec struct {
	Domain             string           `json:"domain"`
	BuildRecipeVersion int              `json:"build_recipe_version"`
	Module             string           `json:"module"`
	Version            string           `json:"version"`
	Sum                string           `json:"sum"`
	GoModSum           string           `json:"go_mod_sum"`
	Origin             catalogOrigin    `json:"origin"`
	SourceZip          catalogSourceZip `json:"source_zip"`
	GoToolchain        string           `json:"go_toolchain"`
	GCCVersion         string           `json:"gcc_version"`
	Libc6Version       string           `json:"libc6_version"`
	GOOS               string           `json:"goos"`
	GOARCH             string           `json:"goarch"`
	GOAMD64            string           `json:"goamd64"`
	CGOEnabled         string           `json:"cgo_enabled"`
	CC                 string           `json:"cc"`
	CXX                string           `json:"cxx"`
	AR                 string           `json:"ar"`
	GODEBUG            string           `json:"godebug"`
	GoBuild            struct {
		TrimPath bool     `json:"trimpath"`
		BuildVCS bool     `json:"buildvcs"`
		Tags     []string `json:"tags"`
		Mod      string   `json:"mod"`
		ModFile  string   `json:"modfile"`
		Package  string   `json:"package"`
	} `json:"go_build"`
}

func sourceBuildRecipe() sourceBuildRecipeSpec {
	value := sourceBuildRecipeSpec{
		Domain: "beads-census-source-build/v1", BuildRecipeVersion: sourceBuildRecipeVersion,
		Module: modulePath, GoToolchain: goToolchain, GCCVersion: pinnedGCCVersion, Libc6Version: pinnedLibc6Version,
		GOOS: "linux", GOARCH: "amd64", GOAMD64: "v1", CGOEnabled: "1", CC: "gcc", CXX: "g++", AR: "ar", GODEBUG: "goindex=0",
	}
	value.GoBuild.TrimPath = true
	value.GoBuild.BuildVCS = false
	value.GoBuild.Tags = []string{"gms_pure_go"}
	value.GoBuild.Mod = "mod"
	value.GoBuild.ModFile = "build.mod"
	value.GoBuild.Package = "./cmd/bd"
	return value
}

func sourceBuildIdentity(entry catalogEntry) (string, error) {
	value := sourceBuildRecipe()
	value.Version = entry.Version
	value.Sum = entry.Sum
	value.GoModSum = entry.GoModSum
	value.Origin = entry.Origin
	value.SourceZip = entry.SourceZip
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing layout JSON")
	}
	return json.Marshal(value)
}

func familyID(mode string, layout json.RawMessage) (string, error) {
	canonicalLayout, err := canonicalJSON(layout)
	if err != nil {
		return "", err
	}
	return familyIDFromCanonicalLayout(mode, canonicalLayout)
}

// familyIDFromCanonicalLayout hashes a layout whose canonical encoding has
// already been established by the caller.
func familyIDFromCanonicalLayout(mode string, canonicalLayout json.RawMessage) (string, error) {
	semantic := struct {
		Layout json.RawMessage `json:"layout"`
		Mode   string          `json:"mode"`
	}{Layout: canonicalLayout, Mode: mode}
	raw, err := json.Marshal(semantic)
	if err != nil {
		return "", err
	}
	return "sha256:" + digest(raw), nil
}

func digest(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

func acquireBinary(ctx context.Context, entry catalogEntry, cache string) (string, acquisition, error) {
	if releaseAssetMatchesProxyOrigin(entry) {
		binary, err := acquireReleaseAsset(ctx, entry, cache)
		if err != nil {
			return "", acquisition{}, err
		}
		acquired, err := recordAcquisition("release-asset", entry, binary, "")
		return binary, acquired, err
	}
	binary, err := acquireSourceBuild(ctx, entry, cache)
	if err != nil {
		return "", acquisition{}, err
	}
	acquired, err := recordAcquisition("source-build", entry, binary, "")
	return binary, acquired, err
}

func recordAcquisition(kind string, entry catalogEntry, binary, assetFallback string) (acquisition, error) {
	value := acquisition{Kind: kind, AssetFallback: assetFallback}
	if kind == "source-build" {
		value.GoToolchain = goToolchain
		identity, err := sourceBuildIdentity(entry)
		if err != nil {
			return acquisition{}, err
		}
		value.BuildIdentitySHA256 = identity
	} else if kind == "release-asset" {
		sha256, err := executableSHA256(binary)
		if err != nil {
			return acquisition{}, err
		}
		value.ExecutableSHA256 = sha256
	}
	if !validAcquisition(value) {
		return acquisition{}, errors.New("invalid acquisition")
	}
	return value, nil
}

// acquireRecordedBinary repeats the exact acquisition route recorded by the
// fresh census. Rolling states must not silently switch back to a release asset
// after fresh generation had to fall back to authenticated source.
func acquireRecordedBinary(ctx context.Context, entry catalogEntry, cache, mode string, recorded acquisition) (string, error) {
	if !validAcquisitionForEntry(recorded, entry) {
		return "", errors.New("invalid recorded acquisition")
	}
	if fresh, ok := freshBinaryFor(ctx, entry.Version, mode, recorded); ok {
		return verifyFreshBinary(fresh)
	}
	switch recorded.Kind {
	case "release-asset":
		if !releaseAssetMatchesProxyOrigin(entry) {
			return "", errors.New("recorded release asset does not match catalog origin")
		}
		binary, err := acquireReleaseAsset(ctx, entry, cache)
		if err != nil {
			return "", err
		}
		return verifyRecordedBinary(binary, recorded)
	case "source-build":
		return "", errors.New("recorded source build has no in-generation fresh binary binding")
	default:
		return "", errors.New("unknown recorded acquisition")
	}
}

func verifyRecordedBinary(binary string, recorded acquisition) (string, error) {
	if recorded.Kind != "release-asset" {
		return "", errors.New("only release assets have a serialized executable digest")
	}
	actual, err := executableSHA256(binary)
	if err != nil {
		return "", err
	}
	if actual != recorded.ExecutableSHA256 {
		return "", fmt.Errorf("recorded executable digest does not match acquired binary: got %s, want %s", actual, recorded.ExecutableSHA256)
	}
	return binary, nil
}

func bindFreshBinary(binary string, acquired acquisition) (freshBinary, error) {
	actual, err := executableSHA256(binary)
	if err != nil {
		return freshBinary{}, err
	}
	return freshBinary{path: binary, executableSHA256: actual, acquisition: acquired}, nil
}

func verifyFreshBinary(binary freshBinary) (string, error) {
	actual, err := executableSHA256(binary.path)
	if err != nil {
		return "", err
	}
	if actual != binary.executableSHA256 {
		return "", errors.New("fresh binary digest does not match its in-generation binding")
	}
	return binary.path, nil
}

func executableSHA256(binary string) (string, error) {
	info, err := os.Lstat(binary)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return "", errors.New("binary is not a regular non-symlink executable")
	}
	raw, err := os.ReadFile(binary) //nolint:gosec // binary was authenticated by the acquisition route.
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}

func releaseAssetMatchesProxyOrigin(entry catalogEntry) bool {
	return entry.GitHubRelease != nil && entry.GitHubRelease.SourceRelation == "matches_proxy_origin" && entry.GitHubRelease.LinuxAMD64Asset != nil
}

func acquireReleaseAsset(ctx context.Context, entry catalogEntry, cache string) (binary string, err error) {
	asset := entry.GitHubRelease.LinuxAMD64Asset
	dir := filepath.Join(cache, "assets", entry.Version, strings.TrimPrefix(asset.Digest, "sha256:"))
	archive := filepath.Join(dir, asset.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := verifyReleaseAsset(entry, archive); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet,
			"https://github.com/gastownhall/beads/releases/download/"+entry.Version+"/"+asset.Name, nil)
		if requestErr != nil {
			return "", requestErr
		}
		response, requestErr := http.DefaultClient.Do(request) //nolint:gosec // host is fixed and version/asset are strict authenticated catalog fields.
		if requestErr != nil {
			return "", requestErr
		}
		defer func() {
			if closeErr := response.Body.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close release asset response body: %w", closeErr))
			}
		}()
		if response.StatusCode != http.StatusOK {
			return "", fmt.Errorf("download asset: %s", response.Status)
		}
		temporary := archive + ".tmp"
		file, createErr := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // temporary is the catalog-bound asset staging path under the authenticated version and digest cache directory.
		if createErr != nil {
			return "", createErr
		}
		_, copyErr := copyReleaseAssetBody(file, response.Body, asset.Size)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(temporary)
			return "", errors.Join(copyErr, closeErr)
		}
		if err := verifyReleaseAsset(entry, temporary); err != nil {
			_ = os.Remove(temporary)
			return "", err
		}
		if err := os.Rename(temporary, archive); err != nil {
			return "", err
		}
	}
	output, binary, err := freshBinaryPath()
	if err != nil {
		return "", err
	}
	if err := extractBinary(archive, binary); err != nil {
		_ = os.RemoveAll(output)
		return "", err
	}
	return binary, nil
}

func verifyReleaseAsset(entry catalogEntry, path string) error {
	if !releaseAssetMatchesProxyOrigin(entry) {
		return nil
	}
	asset := entry.GitHubRelease.LinuxAMD64Asset
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() != asset.Size {
		return errors.New("release asset metadata does not match catalog")
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path is an acquired cache entry.
	if err != nil {
		return err
	}
	if "sha256:"+digest(raw) != asset.Digest {
		return errors.New("release asset digest does not match catalog")
	}
	return nil
}

// copyReleaseAssetBody writes exactly the catalog-declared asset size. Reading
// one extra byte lets it reject a server response that exceeds that bound.
func copyReleaseAssetBody(destination io.Writer, source io.Reader, size int64) (int64, error) {
	if size < 0 {
		return 0, errors.New("release asset has negative catalog size")
	}
	limit := size
	if size < int64(^uint64(0)>>1) {
		limit++
	}
	written, err := io.Copy(destination, io.LimitReader(source, limit))
	if err != nil {
		return written, err
	}
	if written != size {
		return written, fmt.Errorf("release asset size does not match catalog: got %d, want %d", written, size)
	}
	return written, nil
}

func acquireSourceBuild(ctx context.Context, entry catalogEntry, cache string) (binary string, err error) {
	// Build state is attacker-writable through the historical source build. A
	// fresh root per release resets GOCACHE, GOMODCACHE, GOPATH/VCS state, and
	// extracted work before the next release can consume any of it.
	root, err := os.MkdirTemp("", "beads-schema-census-build-")
	if err != nil {
		return "", err
	}
	defer func() {
		if removeErr := os.RemoveAll(root); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary source build cache: %w", removeErr))
		}
	}()
	output, binary, err := freshBinaryPath()
	if err != nil {
		return "", err
	}
	if err := buildVerifiedSource(ctx, entry, cache, root, binary); err != nil {
		_ = os.RemoveAll(output)
		return "", err
	}
	return binary, nil
}

func freshBinaryPath() (directory, binary string, err error) {
	directory, err = os.MkdirTemp("", "beads-schema-census-binary-")
	if err != nil {
		return "", "", err
	}
	return directory, filepath.Join(directory, "bd"), nil
}

func buildVerifiedSource(ctx context.Context, entry catalogEntry, cache, buildCache, binary string) (err error) {
	source, err := os.MkdirTemp("", "beads-schema-census-source-")
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.RemoveAll(source); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary source directory: %w", removeErr))
		}
	}()
	environment := sourceBuildEnvironment(os.Environ(), buildCache)
	if err := verifySourceBuildEnvironment(ctx, environment); err != nil {
		return err
	}
	zipPath, err := acquireSourceZip(cache, entry, func(destination string) error {
		download, err := downloadVerifiedSource(ctx, entry, source, environment)
		if err != nil {
			return err
		}
		return copyFile(destination, download.Zip)
	})
	if err != nil {
		return err
	}
	if err := extractVerifiedSourceZip(zipPath, entry, source); err != nil {
		return err
	}
	modFile := filepath.Join(source, "build.mod")
	sumFile := filepath.Join(source, "build.sum")
	if err := copyBuildModuleFile(filepath.Join(source, "go.mod"), modFile, true); err != nil {
		return err
	}
	if err := copyBuildModuleFile(filepath.Join(source, "go.sum"), sumFile, false); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "go", "build",
		"-trimpath", "-buildvcs=false", "-tags", "gms_pure_go",
		"-mod=mod", "-modfile="+modFile, "-o", binary, "./cmd/bd")
	command.Dir = source
	command.Env = environment
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("build verified source with %s: %w: %s", goToolchain, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func sourceBuildEnvironment(base []string, scratch string) []string {
	inheritedNames := []string{
		"HOME", "PATH",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR",
	}
	inherited := make(map[string]string, len(inheritedNames))
	allowed := make(map[string]bool, len(inheritedNames))
	for _, name := range inheritedNames {
		allowed[name] = true
	}
	for _, entry := range base {
		name, value, found := strings.Cut(entry, "=")
		if found && allowed[name] {
			inherited[name] = value
		}
	}
	values := map[string]string{
		"GOENV":       "off",
		"GODEBUG":     "goindex=0",
		"GO111MODULE": "on",
		"GOFLAGS":     "-modcacherw",
		"GOWORK":      "off",
		"GOTOOLCHAIN": goToolchain,
		"GOPROXY":     "https://proxy.golang.org,direct",
		"GOSUMDB":     "sum.golang.org",
		"GOPRIVATE":   "",
		"GONOSUMDB":   "",
		"GONOPROXY":   "",
		"GOMODCACHE":  filepath.Join(scratch, "mod"),
		"GOCACHE":     filepath.Join(scratch, "cache"),
		"GOPATH":      filepath.Join(scratch, "gopath"),
		"GOOS":        "linux",
		"GOARCH":      "amd64",
		"GOAMD64":     "v1",
		"CGO_ENABLED": "1",
		"CC":          "gcc",
		"CXX":         "g++",
		"AR":          "ar",
	}
	orderedValues := []string{
		"GOENV", "GODEBUG", "GO111MODULE", "GOFLAGS", "GOWORK", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOPRIVATE", "GONOSUMDB", "GONOPROXY",
		"GOMODCACHE", "GOCACHE", "GOPATH", "GOOS", "GOARCH", "GOAMD64", "CGO_ENABLED", "CC", "CXX", "AR",
	}
	result := make([]string, 0, len(inherited)+len(values))
	for _, name := range inheritedNames {
		if value, found := inherited[name]; found {
			result = append(result, name+"="+value)
		}
	}
	for _, name := range orderedValues {
		result = append(result, name+"="+values[name])
	}
	return result
}

func verifyGCCVersion(ctx context.Context, environment []string) error {
	command := exec.CommandContext(ctx, "gcc", "--version")
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("check GCC version: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return validateGCCVersion(output)
}

func verifySourceBuildEnvironment(ctx context.Context, environment []string) error {
	command := exec.CommandContext(ctx, "go", "version")
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("check Go toolchain: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := validateGoToolchain(output); err != nil {
		return err
	}
	if err := verifyGCCVersion(ctx, environment); err != nil {
		return err
	}
	command = exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Version}", "libc6:amd64")
	command.Env = environment
	output, err = command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("check libc6 version: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return validateLibc6Version(output)
}

func validateGoToolchain(output []byte) error {
	if fields := strings.Fields(string(output)); len(fields) != 4 || fields[0] != "go" || fields[1] != "version" || fields[2] != goToolchain || fields[3] != "linux/amd64" {
		return fmt.Errorf("Go version = %q, want %s linux/amd64", strings.TrimSpace(string(output)), goToolchain)
	}
	return nil
}

func validateLibc6Version(output []byte) error {
	if got := strings.TrimSpace(string(output)); got != pinnedLibc6Version {
		return fmt.Errorf("libc6 version = %q, want %q", got, pinnedLibc6Version)
	}
	return nil
}

func validateGCCVersion(output []byte) error {
	firstLine, _, _ := strings.Cut(string(output), "\n")
	if firstLine != pinnedGCCVersion {
		return fmt.Errorf("GCC version = %q, want %q", firstLine, pinnedGCCVersion)
	}
	return nil
}

func copyBuildModuleFile(source, destination string, required bool) error {
	raw, err := os.ReadFile(source) //nolint:gosec // source is inside the verified module directory.
	if errors.Is(err, os.ErrNotExist) && !required {
		raw = nil
	} else if err != nil {
		return err
	}
	if err := os.WriteFile(destination, raw, 0o600); err != nil {
		return err
	}
	return nil
}

func downloadVerifiedSource(ctx context.Context, entry catalogEntry, scratch string, environment []string) (moduleDownload, error) {
	command := exec.CommandContext(ctx, "go", "mod", "download", "-json", modulePath+"@"+entry.Version)
	command.Dir = scratch
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return moduleDownload{}, fmt.Errorf("go mod download: %w", err)
	}
	var download moduleDownload
	if err := json.Unmarshal(output, &download); err != nil {
		return moduleDownload{}, err
	}
	if download.Path != modulePath || download.Version != entry.Version || download.Sum != entry.Sum || download.GoModSum != entry.GoModSum || download.Origin != entry.Origin {
		return moduleDownload{}, errors.New("downloaded module provenance does not match catalog")
	}
	info, err := os.Stat(download.Zip)
	if err != nil {
		return moduleDownload{}, err
	}
	raw, err := os.ReadFile(download.Zip) //nolint:gosec // path is returned by go mod download.
	if err != nil {
		return moduleDownload{}, err
	}
	if info.Size() != entry.SourceZip.Size || digest(raw) != entry.SourceZip.SHA256 {
		return moduleDownload{}, errors.New("downloaded source zip does not match catalog")
	}
	return download, nil
}

func extractBinary(archive, destination string) error {
	file, err := os.Open(archive) //nolint:gosec // archive is a verified cache entry.
	if err != nil {
		return err
	}
	defer file.Close()
	return extractReleaseBinary(file, destination, maxReleaseBinaryBytes)
}

// extractReleaseBinary scans the gzip/tar stream without materializing archive
// paths. A release asset must contain exactly one bounded, non-empty regular
// file named bd; the archive's path is rejected if it is not normalized.
func extractReleaseBinary(compressed io.Reader, destination string, maxSize int64) (err error) {
	if maxSize <= 0 {
		return errors.New("release binary size limit must be positive")
	}
	gzipReader, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("open release asset gzip stream: %w", err)
	}
	defer func() {
		if closeErr := gzipReader.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close release asset gzip stream: %w", closeErr))
		}
	}()

	reader := tar.NewReader(gzipReader)
	var writer *atomicfile.Writer
	defer func() {
		if writer != nil {
			_ = writer.Abort()
		}
	}()

	found := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("scan release asset: %w", err)
		}
		if path.Base(header.Name) != "bd" {
			continue
		}
		if !validReleaseBinaryPath(header.Name) {
			return errors.New("release asset contains a non-normalized bd path")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return errors.New("release asset bd entry is not a regular file")
		}
		if found {
			return errors.New("release asset contains multiple bd binaries")
		}
		if header.Size <= 0 || header.Size > maxSize {
			return errors.New("release asset bd binary has invalid size")
		}
		writer, err = atomicfile.Create(destination, 0o755)
		if err != nil {
			return err
		}
		copied, err := io.Copy(writer, io.LimitReader(reader, header.Size))
		if err != nil {
			return fmt.Errorf("copy release asset bd binary: %w", err)
		}
		if copied != header.Size {
			return errors.New("release asset bd binary is truncated")
		}
		found = true
	}
	if !found {
		return errors.New("release asset contains no bd binary")
	}
	if err := writer.Close(); err != nil {
		return err
	}
	writer = nil
	return nil
}

func validReleaseBinaryPath(name string) bool {
	return name != "" && !path.IsAbs(name) && !strings.HasPrefix(name, "../") && path.Clean(name) == name
}

func observeFresh(ctx context.Context, binary string, entry catalogEntry, acquired acquisition) (observed observation, observedFamily family, err error) {
	workspace, err := os.MkdirTemp("", "beads-schema-census-")
	if err != nil {
		return observation{}, family{}, err
	}
	defer func() {
		if removeErr := os.RemoveAll(workspace); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove fresh observation workspace: %w", removeErr))
		}
	}()
	if err := initializeCensusRepository(ctx, workspace); err != nil {
		return observation{}, family{}, err
	}
	environment, err := censusEnvironment(workspace)
	if err != nil {
		return observation{}, family{}, err
	}
	initContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if output, err := runHistoricalCommandIn(initContext, binary, workspace, environment, "init", "-p", "census"); err != nil {
		initErr := eligibleAssetProcessFailure(acquired, initContext, fmt.Errorf("initialize fresh workspace: %w: %s", err, strings.TrimSpace(string(output))))
		if topology, topologyErr := recognizeFreshTopology(workspace); topologyErr == nil && topology.Mode == "dolt-server" {
			return observeFreshWithExternalServer(ctx, binary, entry, acquired, initErr, scenarioSpec{})
		}
		if acquired.Kind == "source-build" {
			supported, supportErr := binarySupportsExternalServerInit(ctx, binary, workspace, environment)
			if supportErr == nil && supported {
				return observeFreshWithExternalServer(ctx, binary, entry, acquired, initErr, scenarioSpec{})
			}
		}
		return observation{}, family{}, initErr
	}
	if err := seedJSONLDialectFixture(ctx, binary, workspace, environment); err != nil {
		return observation{}, family{}, err
	}
	mode, layout, err := probeFreshLayout(initContext, workspace, environment)
	if err != nil {
		if topology, topologyErr := recognizeFreshTopology(workspace); topologyErr == nil && topology.Mode == "dolt-server" {
			return observeFreshWithExternalServer(ctx, binary, entry, acquired, err, scenarioSpec{})
		}
		return observation{}, family{}, err
	}
	return finishObservation(entry, acquired, mode, layout)
}

func observeFreshScenario(
	ctx context.Context,
	binary string,
	entry catalogEntry,
	acquired acquisition,
	scenario scenarioSpec,
) (observed observation, observedFamily family, err error) {
	if scenario.Name == freshDoltServerScenario {
		observed, observedFamily, err = observeFreshWithExternalServer(
			ctx, binary, entry, acquired, fmt.Errorf("explicit %s initialization", scenario.Name), scenario,
		)
		if err == nil {
			observed.Scenario = scenario.Name
		}
		return observed, observedFamily, err
	}

	workspace, err := os.MkdirTemp("", "beads-schema-census-"+scenario.Mode+"-")
	if err != nil {
		return observation{}, family{}, err
	}
	defer func() {
		if removeErr := os.RemoveAll(workspace); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove fresh scenario workspace: %w", removeErr))
		}
	}()
	if err := initializeCensusRepository(ctx, workspace); err != nil {
		return observation{}, family{}, err
	}
	environment, err := censusEnvironment(workspace)
	if err != nil {
		return observation{}, family{}, err
	}
	helpContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	help, helpErr := runHistoricalCommandIn(helpContext, binary, workspace, environment, "init", "--help")
	if helpErr != nil {
		err := eligibleAssetProcessFailure(acquired, helpContext, fmt.Errorf("inspect init capabilities: %w: %s", helpErr, strings.TrimSpace(string(help))))
		cancel()
		return observation{}, family{}, err
	}
	cancel()
	if !scenario.supportedByInitHelp(entry.Version, help) {
		err := fmt.Errorf("historical binary does not expose %s initialization", scenario.Name)
		if acquired.Kind == "release-asset" {
			err = assetFallbackEligible(err)
		}
		return observation{}, family{}, err
	}
	args, err := scenario.initArgs(entry.Version, "census", 0)
	if err != nil {
		return observation{}, family{}, err
	}
	initContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	output, initErr := runHistoricalCommandIn(initContext, binary, workspace, environment, args...)
	if initErr != nil {
		err := eligibleAssetProcessFailure(acquired, initContext, fmt.Errorf("initialize %s: %w: %s", scenario.Name, initErr, strings.TrimSpace(string(output))))
		cancel()
		return observation{}, family{}, err
	}
	cancel()
	if err := seedJSONLDialectFixture(ctx, binary, workspace, environment); err != nil {
		return observation{}, family{}, err
	}
	probeContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	mode, layout, err := probeFreshLayout(probeContext, workspace, environment)
	if err != nil {
		return observation{}, family{}, err
	}
	if mode != scenario.Mode {
		err := fmt.Errorf("%s produced storage mode %q, want %q", scenario.Name, mode, scenario.Mode)
		if acquired.Kind == "release-asset" {
			err = assetFallbackEligible(err)
		}
		return observation{}, family{}, err
	}
	observed, observedFamily, err = finishObservation(entry, acquired, mode, layout)
	if err == nil {
		observed.Scenario = scenario.Name
	}
	return observed, observedFamily, err
}

func observeFreshWithExternalServer(
	ctx context.Context,
	binary string,
	entry catalogEntry,
	acquired acquisition,
	defaultErr error,
	scenario scenarioSpec,
) (observed observation, observedFamily family, err error) {
	workspace, err := os.MkdirTemp("", "beads-schema-census-server-")
	if err != nil {
		return observation{}, family{}, err
	}
	defer func() {
		if removeErr := os.RemoveAll(workspace); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove fresh server observation workspace: %w", removeErr))
		}
	}()
	if err := initializeCensusRepository(ctx, workspace); err != nil {
		return observation{}, family{}, err
	}
	environment, err := censusEnvironment(workspace)
	if err != nil {
		return observation{}, family{}, err
	}
	requestedPort := 0
	if scenario.Name != "" {
		requestedPort, err = scenario.bootstrapServerPort(entry.Version)
		if err != nil {
			return observation{}, family{}, err
		}
	}
	server, err := startDoltServerFallback(ctx, workspace, "", environment, requestedPort)
	if err != nil {
		return observation{}, family{}, fmt.Errorf("default server observation failed (%v); start pinned server: %w", defaultErr, err)
	}
	defer func() { _ = server.Close() }()
	var args []string
	if scenario.Name == "" {
		args, err = externalServerInitArgs(server.port)
	} else {
		args, err = scenario.initArgs(entry.Version, "census", server.port)
	}
	if err != nil {
		return observation{}, family{}, err
	}
	initContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	output, initErr := runHistoricalCommandIn(initContext, binary, workspace, environment, args...)
	if initErr != nil {
		initFailure := eligibleAssetProcessFailure(acquired, initContext, fmt.Errorf(
			"default server observation failed (%v); initialize against pinned server: %w: %s",
			defaultErr, initErr, strings.TrimSpace(string(output)),
		))
		cancel()
		return observation{}, family{}, initFailure
	}
	cancel()
	// Early server-mode releases created the Dolt database through their
	// embedded factory after the external server had already loaded its
	// database catalog. Refresh the census-owned server so public SQL sees the
	// quiescent database that init actually left on disk.
	port := server.port
	doltBin := server.doltBin
	_ = server.Close()
	server, err = startDoltServerFallback(ctx, workspace, doltBin, environment, port)
	if err != nil {
		return observation{}, family{}, fmt.Errorf(
			"default server observation failed (%v); refresh pinned server after initialization: %w",
			defaultErr, err,
		)
	}
	probeContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		return observation{}, family{}, fmt.Errorf(
			"default server observation failed (%v); probe pinned server initialization: %w",
			defaultErr, err,
		)
	}
	if topology.Mode != "dolt-server" {
		err := fmt.Errorf("pinned server initialization produced storage mode %q", topology.Mode)
		if acquired.Kind == "release-asset" {
			err = assetFallbackEligible(err)
		}
		return observation{}, family{}, err
	}
	database, err := discoverActiveDoltServerDatabase(
		probeContext, server.doltBin, workspace, environment, "127.0.0.1", server.port,
	)
	if err != nil {
		return observation{}, family{}, fmt.Errorf(
			"default server observation failed (%v); discover active server database: %w",
			defaultErr, err,
		)
	}
	fingerprint, err := collectDolt(probeContext, pinnedDoltServerRunner(
		server.doltBin, workspace, database, environment, server.port,
	))
	if err != nil {
		return observation{}, family{}, fmt.Errorf(
			"default server observation failed (%v); query pinned server initialization: %w",
			defaultErr, err,
		)
	}
	layout, err := marshalDoltLayout(topology.Markers, fingerprint)
	if err != nil {
		return observation{}, family{}, err
	}
	layout, err = attachSQLiteEvidenceToDoltLayout(workspace, topology, layout)
	if err != nil {
		return observation{}, family{}, err
	}
	return finishObservation(entry, acquired, topology.Mode, layout)
}

func externalServerInitArgs(port int) ([]string, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid external Dolt server port %d", port)
	}
	return []string{
		"init", "-p", "census", "--server",
		"--server-host", "127.0.0.1",
		"--server-port", strconv.Itoa(port),
		"--server-user", "root",
	}, nil
}

func binarySupportsExternalServerInit(
	ctx context.Context,
	binary string,
	workspace string,
	environment []string,
) (bool, error) {
	helpContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := runHistoricalCommandIn(helpContext, binary, workspace, environment, "init", "--help")
	if err != nil {
		return false, fmt.Errorf("inspect init capabilities: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return supportsExternalServerInit(output), nil
}

func supportsExternalServerInit(help []byte) bool {
	text := string(help)
	for _, flag := range []string{"--server", "--server-host", "--server-port", "--server-user"} {
		if !strings.Contains(text, flag) {
			return false
		}
	}
	return true
}

func finishObservation(entry catalogEntry, acquired acquisition, mode string, layout json.RawMessage) (observation, family, error) {
	canonicalLayout, err := canonicalJSON(layout)
	if err != nil {
		return observation{}, family{}, err
	}
	familyIDValue, err := familyIDFromCanonicalLayout(mode, canonicalLayout)
	if err != nil {
		return observation{}, family{}, err
	}
	observedFamily := family{ID: familyIDValue, Mode: mode, Layout: canonicalLayout}
	return observation{Version: entry.Version, Scenario: freshScenario, Provenance: provenanceFromEntry(entry), Acquisition: acquired, FamilyID: familyIDValue}, observedFamily, nil
}

func censusEnvironment(workspace string) ([]string, error) {
	directories := []string{
		filepath.Join(workspace, "home"),
		filepath.Join(workspace, "xdg-config"),
		filepath.Join(workspace, "xdg-cache"),
		filepath.Join(workspace, "xdg-data"),
		filepath.Join(workspace, "xdg-state"),
		filepath.Join(workspace, "tmp"),
	}
	for _, directory := range directories {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + directories[0],
		"XDG_CONFIG_HOME=" + directories[1],
		"XDG_CACHE_HOME=" + directories[2],
		"XDG_DATA_HOME=" + directories[3],
		"XDG_STATE_HOME=" + directories[4],
		"TMPDIR=" + directories[5],
		"USER=census",
		"LOGNAME=census",
		"LC_ALL=C",
		"TZ=UTC",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"BD_DISABLE_METRICS=1",
		"BD_DISABLE_EVENT_FLUSH=1",
		"BD_NON_INTERACTIVE=1",
		"BEADS_NO_DAEMON=1",
		"BEADS_DOLT_AUTO_START=0",
	}, nil
}

func initializeCensusRepository(ctx context.Context, workspace string) error {
	command := exec.CommandContext(ctx, "git", "init", "-q", workspace) //nolint:gosec // executable and arguments are fixed; workspace is a process-created temporary directory.
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("initialize isolated git repository: %w: %s", err, strings.TrimSpace(string(output)))
	}
	command = exec.CommandContext(ctx, "git", "-C", workspace, "config", "core.hooksPath", ".git/hooks") //nolint:gosec // executable and arguments are fixed; workspace is a process-created temporary directory.
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("configure repository-local hooks: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

const jsonlDialectFixtureTitle = "census-jsonl-dialect-fixture"

// seedJSONLDialectFixture creates one public representative record only for
// JSONL workspaces. Fresh JSONL files are empty, so their on-disk presence
// alone cannot identify the record dialect that the historical binary writes.
func seedJSONLDialectFixture(ctx context.Context, binary, workspace string, environment []string) error {
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		return err
	}
	if topology.Mode != "jsonl" {
		return nil
	}
	createContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if output, err := runHistoricalCommandIn(createContext, binary, workspace, environment, "create", jsonlDialectFixtureTitle); err != nil {
		return fmt.Errorf("create JSONL dialect fixture: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// probeFreshLayout identifies SQLite through its documented SQL/PRAGMA surface.
// Other profiles fail explicitly until their public SQL collectors are added;
// it intentionally never reaches into private Dolt files or internals.
func probeFreshLayout(ctx context.Context, workspace string, environment []string) (string, json.RawMessage, error) {
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		return "", nil, err
	}
	switch topology.Mode {
	case "sqlite":
		layout, err := collectSQLiteLayout(workspace, topology)
		if err != nil {
			return "", nil, err
		}
		return topology.Mode, layout, nil
	case "jsonl":
		dialect, err := collectJSONLDialect(filepath.Join(workspace, topology.JSONL))
		if err != nil {
			return "", nil, err
		}
		layout, err := json.Marshal(struct {
			Topology []string     `json:"topology"`
			Format   string       `json:"format"`
			Dialect  jsonlDialect `json:"dialect"`
		}{Topology: topology.Markers, Format: "beads-jsonl", Dialect: dialect})
		if err != nil {
			return "", nil, err
		}
		return topology.Mode, layout, nil
	case "dolt-legacy", "dolt-server":
		doltBinary, err := resolveDoltRuntime(ctx, "")
		if err != nil {
			return "", nil, err
		}
		layout, err := collectLegacyDoltLayout(ctx, workspace, doltBinary, environment, topology.Markers, topology.DoltDatabase, topology.MetadataDatabase)
		if err != nil {
			return "", nil, err
		}
		layout, err = attachSQLiteEvidenceToDoltLayout(workspace, topology, layout)
		if err != nil {
			return "", nil, err
		}
		return topology.Mode, layout, nil
	case "dolt-embedded":
		metadata, err := readStorageMetadata(filepath.Join(workspace, ".beads", "metadata.json"))
		if err != nil {
			return "", nil, err
		}
		if metadata.DoltDatabase == "" {
			return "", nil, errors.New("embedded Dolt metadata has no database name")
		}
		doltBinary, err := resolveDoltRuntime(ctx, "")
		if err != nil {
			return "", nil, err
		}
		fingerprint, err := collectDolt(ctx, pinnedDoltRunner{
			binary: doltBinary, workspace: workspace,
			dataDir:  filepath.Join(workspace, ".beads", "embeddeddolt"),
			database: metadata.DoltDatabase, environment: environment,
		})
		if err != nil {
			return "", nil, err
		}
		layout, err := marshalDoltLayout(topology.Markers, fingerprint)
		if err != nil {
			return "", nil, err
		}
		layout, err = attachSQLiteEvidenceToDoltLayout(workspace, topology, layout)
		if err != nil {
			return "", nil, err
		}
		return topology.Mode, layout, nil
	default:
		return "", nil, fmt.Errorf("unsupported fresh storage profile %q", topology.Mode)
	}
}

func marshalDoltLayout(markers []string, fingerprint doltFingerprint) (json.RawMessage, error) {
	return json.Marshal(struct {
		Topology []string        `json:"topology"`
		Schema   doltFingerprint `json:"schema"`
	}{Topology: markers, Schema: fingerprint})
}

type labeledDoltStore struct {
	Name   string          `json:"name"`
	Schema doltFingerprint `json:"schema"`
}

type labeledDoltDatabase struct {
	Name   string          `json:"name"`
	Schema doltFingerprint `json:"schema"`
}

func collectMetadataSelectedMultiDatabaseDoltLayout(
	ctx context.Context,
	markers []string,
	selector string,
	metadataDatabase string,
	runner func(string) doltSQLRunner,
) (json.RawMessage, error) {
	databases, err := listDoltUserDatabases(ctx, runner(""))
	if err != nil {
		return nil, err
	}
	observed := make([]labeledDoltDatabase, 0, len(databases))
	for _, name := range databases {
		fingerprint, err := collectDolt(ctx, runner(name))
		if err != nil {
			return nil, fmt.Errorf("collect Dolt database %q: %w", name, err)
		}
		observed = append(observed, labeledDoltDatabase{Name: name, Schema: fingerprint})
	}
	if len(observed) == 1 {
		if selector != "" && observed[0].Name != selector {
			return nil, fmt.Errorf("metadata-selected Dolt database %q does not match sole user database %q", selector, observed[0].Name)
		}
		return marshalDoltLayout(markers, observed[0].Schema)
	}
	if selector == "" || isDoltSystemDatabase(selector) {
		return nil, errors.New("multi-database Dolt selector is empty or system-owned")
	}
	if len(observed) != 2 || selector != "beads_census" ||
		observed[0].Name != "beads" || observed[1].Name != selector {
		return nil, fmt.Errorf("multi-database Dolt topology has unexpected user databases %q and %q", databaseNames(observed, 0), databaseNames(observed, 1))
	}
	if !hasTopologyMarker(markers, "directory:.beads/dolt") || hasTopologyMarker(markers, "directory:.beads/embeddeddolt") ||
		!hasTopologyMarker(markers, "metadata-backend:dolt") || metadataDatabase != "dolt" {
		return nil, errors.New("multi-database Dolt topology lacks the required legacy root metadata")
	}
	withSelector := append([]string(nil), markers...)
	if !hasTopologyMarker(withSelector, "metadata-database:"+metadataDatabase) {
		withSelector = append(withSelector, "metadata-database:"+metadataDatabase)
	}
	withSelector = append(withSelector, "metadata-dolt-database:"+selector)
	sort.Strings(withSelector)
	return json.Marshal(struct {
		Databases []labeledDoltDatabase `json:"databases"`
		Schema    doltFingerprint       `json:"schema"`
		Topology  []string              `json:"topology"`
	}{Databases: observed, Schema: observed[1].Schema, Topology: withSelector})
}

func databaseNames(databases []labeledDoltDatabase, index int) string {
	if index < 0 || index >= len(databases) {
		return ""
	}
	return databases[index].Name
}

func collectMetadataSelectedMultiDatabaseDualDoltRootLayout(
	ctx context.Context,
	markers []string,
	selector string,
	metadataDatabase string,
	legacy func(string) doltSQLRunner,
	embedded func(string) doltSQLRunner,
) (json.RawMessage, error) {
	if !isCanonicalMultiDatabaseDualDoltRootTopology(markers, selector, metadataDatabase) {
		return nil, errors.New("multi-database dual-Dolt-root topology does not match the canonical legacy metadata")
	}
	databases, err := listDoltUserDatabases(ctx, legacy(""))
	if err != nil {
		return nil, err
	}
	if len(databases) != 2 || databases[0] != "beads" || databases[1] != selector {
		return nil, fmt.Errorf("multi-database dual-Dolt-root topology has unexpected legacy databases %q and %q", databaseName(databases, 0), databaseName(databases, 1))
	}
	observed := make([]labeledDoltDatabase, 0, len(databases))
	for _, name := range databases {
		fingerprint, err := collectDolt(ctx, legacy(name))
		if err != nil {
			return nil, fmt.Errorf("collect legacy Dolt database %q: %w", name, err)
		}
		observed = append(observed, labeledDoltDatabase{Name: name, Schema: fingerprint})
	}
	embeddedDatabase, err := discoverDoltServerDatabase(ctx, embedded(""))
	if err != nil {
		return nil, fmt.Errorf("discover embedded Dolt database: %w", err)
	}
	if embeddedDatabase != selector {
		return nil, fmt.Errorf("embedded Dolt database %q does not match metadata selector %q", embeddedDatabase, selector)
	}
	embeddedFingerprint, err := collectDolt(ctx, embedded(embeddedDatabase))
	if err != nil {
		return nil, fmt.Errorf("collect embedded Dolt database %q: %w", embeddedDatabase, err)
	}
	withSelector := append([]string(nil), markers...)
	withSelector = append(withSelector, "metadata-database:"+metadataDatabase)
	withSelector = append(withSelector, "metadata-dolt-database:"+selector)
	sort.Strings(withSelector)
	return json.Marshal(struct {
		Databases []labeledDoltDatabase `json:"databases"`
		Schema    doltFingerprint       `json:"schema"`
		Stores    []labeledDoltStore    `json:"stores"`
		Topology  []string              `json:"topology"`
	}{
		Databases: observed,
		Schema:    observed[1].Schema,
		Stores: []labeledDoltStore{
			{Name: "dolt", Schema: observed[1].Schema},
			{Name: "embeddeddolt", Schema: embeddedFingerprint},
		},
		Topology: withSelector,
	})
}

func isCanonicalMultiDatabaseDualDoltRootTopology(markers []string, selector, metadataDatabase string) bool {
	expected := []string{
		"directory:.beads/dolt",
		"directory:.beads/embeddeddolt",
		"local-version:other-valid",
		"metadata-backend:dolt",
	}
	if selector != "beads_census" || metadataDatabase != "dolt" || len(markers) != len(expected) {
		return false
	}
	for index, marker := range expected {
		if markers[index] != marker {
			return false
		}
	}
	return true
}

func databaseName(databases []string, index int) string {
	if index < 0 || index >= len(databases) {
		return ""
	}
	return databases[index]
}

func marshalLegacyDoltLayout(markers []string, primary doltFingerprint, embedded *doltFingerprint) (json.RawMessage, error) {
	if embedded == nil {
		return marshalDoltLayout(markers, primary)
	}
	stores := []labeledDoltStore{{Name: "dolt", Schema: primary}, {Name: "embeddeddolt", Schema: *embedded}}
	return json.Marshal(struct {
		Topology []string           `json:"topology"`
		Schema   doltFingerprint    `json:"schema"`
		Stores   []labeledDoltStore `json:"stores"`
	}{Topology: markers, Schema: primary, Stores: stores})
}

func collectServerDoltLayout(
	ctx context.Context,
	markers []string,
	server doltSQLRunner,
	embedded func(string) doltSQLRunner,
) (json.RawMessage, error) {
	primary, err := collectDolt(ctx, server)
	if err != nil {
		return nil, err
	}
	if embedded == nil {
		return marshalLegacyDoltLayout(markers, primary, nil)
	}
	database, err := discoverDoltServerDatabase(ctx, embedded(""))
	if err != nil {
		return nil, fmt.Errorf("discover embedded Dolt database: %w", err)
	}
	secondary, err := collectDolt(ctx, embedded(database))
	if err != nil {
		return nil, fmt.Errorf("collect embedded Dolt store: %w", err)
	}
	return marshalLegacyDoltLayout(markers, primary, &secondary)
}

func collectLegacyDoltLayout(ctx context.Context, workspace, binary string, environment []string, markers []string, selector, metadataDatabase string) (json.RawMessage, error) {
	legacyDir := filepath.Join(workspace, ".beads", "dolt")
	embeddedDir := filepath.Join(workspace, ".beads", "embeddeddolt")
	embeddedInfo, statErr := os.Stat(embeddedDir)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect embedded Dolt store: %w", statErr)
	}
	if statErr == nil && !embeddedInfo.IsDir() {
		return nil, errors.New("embedded Dolt store is not a directory")
	}
	legacyRunner := func(database string) pinnedDoltRunner {
		return pinnedDoltRunner{binary: binary, workspace: workspace, dataDir: legacyDir, database: database, environment: environment}
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return collectMetadataSelectedMultiDatabaseDoltLayout(ctx, markers, selector, metadataDatabase, func(database string) doltSQLRunner {
			return legacyRunner(database)
		})
	}
	embeddedRunner := func(database string) pinnedDoltRunner {
		return pinnedDoltRunner{binary: binary, workspace: workspace, dataDir: embeddedDir, database: database, environment: environment}
	}
	if isCanonicalMultiDatabaseDualDoltRootTopology(markers, selector, metadataDatabase) {
		return collectMetadataSelectedMultiDatabaseDualDoltRootLayout(ctx, markers, selector, metadataDatabase, func(database string) doltSQLRunner {
			return legacyRunner(database)
		}, func(database string) doltSQLRunner {
			return embeddedRunner(database)
		})
	}
	legacyDatabase, embeddedDatabase, err := discoverMixedDoltDatabases(ctx, legacyRunner(""), embeddedRunner(""))
	if err != nil {
		return nil, err
	}
	primary, err := collectDolt(ctx, legacyRunner(legacyDatabase))
	if err != nil {
		return nil, err
	}
	embedded, err := collectDolt(ctx, embeddedRunner(embeddedDatabase))
	if err != nil {
		return nil, err
	}
	return marshalLegacyDoltLayout(markers, primary, &embedded)
}

type freshTopology struct {
	Mode             string
	Database         string
	DoltDatabase     string
	MetadataDatabase string
	JSONL            string
	CoexistingSQLite string
	SQLiteBackups    []string
	Markers          []string
}

type jsonlDialect struct {
	Records []jsonlRecordDialect `json:"records"`
}

// jsonlRecordDialect records only public interchange controls. Issue fields
// and their values are user data rather than storage layout, so they must not
// split a family merely because a fixture differs.
type jsonlRecordDialect struct {
	Kind   string              `json:"kind"`
	Schema string              `json:"schema,omitempty"`
	Type   string              `json:"type,omitempty"`
	Fields []jsonlDialectField `json:"fields"`
}

type jsonlDialectField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func collectJSONLDialect(path string) (jsonlDialect, error) {
	file, err := os.Open(path) //nolint:gosec // fixed JSONL artifact in an isolated workspace.
	if err != nil {
		return jsonlDialect{}, err
	}
	defer file.Close()

	seen := make(map[string]jsonlRecordDialect)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record map[string]json.RawMessage
		if err := json.Unmarshal(line, &record); err != nil || record == nil {
			if err == nil {
				err = errors.New("JSONL record is not an object")
			}
			return jsonlDialect{}, fmt.Errorf("read JSONL record dialect: %w", err)
		}
		dialect, err := classifyJSONLRecordDialect(record)
		if err != nil {
			return jsonlDialect{}, err
		}
		seen[jsonlRecordDialectKey(dialect)] = dialect
	}
	if err := scanner.Err(); err != nil {
		return jsonlDialect{}, err
	}

	result := jsonlDialect{Records: make([]jsonlRecordDialect, 0, len(seen))}
	for _, dialect := range seen {
		result.Records = append(result.Records, dialect)
	}
	sort.Slice(result.Records, func(i, j int) bool {
		return jsonlRecordDialectKey(result.Records[i]) < jsonlRecordDialectKey(result.Records[j])
	})
	return result, nil
}

func classifyJSONLRecordDialect(record map[string]json.RawMessage) (jsonlRecordDialect, error) {
	fields := make([]jsonlDialectField, 0, len(record))
	for name, raw := range record {
		valueType, err := jsonlValueType(raw)
		if err != nil {
			return jsonlRecordDialect{}, fmt.Errorf("read JSONL field %q type: %w", name, err)
		}
		fields = append(fields, jsonlDialectField{Name: name, Type: valueType})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })

	if raw, ok := record["_schema"]; ok {
		var schema string
		if err := json.Unmarshal(raw, &schema); err != nil || schema == "" {
			if err == nil {
				err = errors.New("JSONL schema marker is empty")
			}
			return jsonlRecordDialect{}, fmt.Errorf("read JSONL schema marker: %w", err)
		}
		return jsonlRecordDialect{Kind: "schema-header", Schema: schema, Fields: fields}, nil
	}
	if raw, ok := record["_type"]; ok {
		var recordType string
		if err := json.Unmarshal(raw, &recordType); err != nil || recordType == "" {
			if err == nil {
				err = errors.New("JSONL record type is empty")
			}
			return jsonlRecordDialect{}, fmt.Errorf("read JSONL record type: %w", err)
		}
		return jsonlRecordDialect{Kind: "typed-record", Type: recordType, Fields: fields}, nil
	}
	return jsonlRecordDialect{Kind: "flat-record", Fields: fields}, nil
}

func jsonlValueType(raw json.RawMessage) (string, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	switch value.(type) {
	case nil:
		return "null", nil
	case bool:
		return "boolean", nil
	case json.Number:
		return "number", nil
	case string:
		return "string", nil
	case []any:
		return "array", nil
	case map[string]any:
		return "object", nil
	default:
		return "", fmt.Errorf("unsupported JSON value type %T", value)
	}
}

func jsonlRecordDialectKey(value jsonlRecordDialect) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

type storageMetadata struct {
	Backend      string
	DoltMode     string
	DoltDatabase string
	Database     string
}

// recognizeFreshTopology inspects only public workspace topology. In
// particular, it does not mistake Dolt's ephemeral.sqlite3 implementation
// detail for a historical SQLite storage database.
func recognizeFreshTopology(workspace string) (freshTopology, error) {
	beadsDir := filepath.Join(workspace, ".beads")
	markers := []string{localVersionTopologyMarker(filepath.Join(beadsDir, ".local_version"))}
	doltMarkers, hasLegacyDoltDirectory, hasEmbeddedDoltDirectory, err := collectFreshDoltRoots(beadsDir)
	if err != nil {
		return freshTopology{}, err
	}
	markers = append(markers, doltMarkers...)
	hasJSONL, err := recognizeFreshJSONLRoot(beadsDir)
	if err != nil {
		return freshTopology{}, err
	}
	sqliteRoots, err := inventorySQLiteRoots(beadsDir)
	if err != nil {
		return freshTopology{}, err
	}
	metadata, err := readStorageMetadata(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return freshTopology{}, err
	}
	if metadata.Backend != "" {
		markers = append(markers, "metadata-backend:"+metadata.Backend)
	}
	if metadata.DoltMode != "" {
		markers = append(markers, "metadata-dolt-mode:"+metadata.DoltMode)
	}
	coexistingSQLite, coexistingMarkers, err := resolveCoexistingFreshSQLite(metadata, sqliteRoots, hasLegacyDoltDirectory, hasEmbeddedDoltDirectory)
	if err != nil {
		return freshTopology{}, err
	}
	markers = append(markers, coexistingMarkers...)
	backupPaths := make([]string, 0, len(sqliteRoots.backups))
	for _, backup := range sqliteRoots.backups {
		backupPaths = append(backupPaths, filepath.Join(".beads", filepath.Base(backup)))
	}
	if len(backupPaths) > 0 {
		markers = append(markers, "sqlite-backups:pre-dolt")
	}
	return classifyFreshTopologyMode(metadata, sqliteRoots, hasLegacyDoltDirectory, hasEmbeddedDoltDirectory, hasJSONL, coexistingSQLite, backupPaths, markers)
}

// collectFreshDoltRoots inspects the legacy and embedded Dolt roots under
// beadsDir, returning their topology markers and which roots are present.
func collectFreshDoltRoots(beadsDir string) (markers []string, hasLegacy, hasEmbedded bool, err error) {
	for _, directory := range []string{"dolt", "embeddeddolt"} {
		info, lstatErr := os.Lstat(filepath.Join(beadsDir, directory))
		if errors.Is(lstatErr, os.ErrNotExist) {
			continue
		}
		if lstatErr != nil {
			return nil, false, false, fmt.Errorf("inspect Dolt root %q: %w", directory, lstatErr)
		}
		if !info.IsDir() {
			return nil, false, false, fmt.Errorf("non-directory Dolt root %q", directory)
		}
		markers = append(markers, "directory:.beads/"+directory)
		if directory == "dolt" {
			hasLegacy = true
		} else {
			hasEmbedded = true
		}
	}
	return markers, hasLegacy, hasEmbedded, nil
}

// recognizeFreshJSONLRoot reports whether a regular issues.jsonl root exists
// under beadsDir, rejecting a present-but-non-regular JSONL root.
func recognizeFreshJSONLRoot(beadsDir string) (bool, error) {
	jsonlInfo, lstatErr := os.Lstat(filepath.Join(beadsDir, "issues.jsonl"))
	if lstatErr != nil {
		if errors.Is(lstatErr, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect JSONL root: %w", lstatErr)
	}
	if !jsonlInfo.Mode().IsRegular() {
		return false, errors.New("non-regular JSONL root")
	}
	return true, nil
}

// resolveCoexistingFreshSQLite validates whether an active SQLite database may
// coexist with a Dolt storage profile, returning the coexisting SQLite path and
// any topology markers it contributes.
func resolveCoexistingFreshSQLite(metadata storageMetadata, sqliteRoots sqliteRootInventory, hasLegacyDoltDirectory, hasEmbeddedDoltDirectory bool) (string, []string, error) {
	hasDoltProfile := hasLegacyDoltDirectory || hasEmbeddedDoltDirectory ||
		metadata.Backend == "dolt" || metadata.DoltMode == "server" || metadata.DoltMode == "embedded"
	if len(sqliteRoots.active) == 0 || !hasDoltProfile {
		return "", nil, nil
	}
	validMetadataSelector := metadata.Backend == "dolt" &&
		((metadata.DoltMode == "" && (metadata.Database == "beads.db" || metadata.Database == "dolt")) ||
			(metadata.DoltMode == "server" && metadata.Database == "dolt"))
	if len(sqliteRoots.active) != 1 || filepath.Base(sqliteRoots.active[0]) != "beads.db" ||
		!validMetadataSelector || !hasLegacyDoltDirectory || hasEmbeddedDoltDirectory {
		return "", nil, fmt.Errorf("ambiguous fresh storage profile: active SQLite and Dolt roots coexist (%s)", strings.Join(sqliteRoots.active, ", "))
	}
	markers := []string{
		"metadata-database:" + metadata.Database,
		"sqlite-coexisting:.beads/beads.db",
	}
	return filepath.Join(".beads", "beads.db"), markers, nil
}

// classifyFreshTopologyMode resolves the final fresh storage mode from the
// collected roots, metadata, and accumulated markers.
func classifyFreshTopologyMode(metadata storageMetadata, sqliteRoots sqliteRootInventory, hasLegacyDoltDirectory, hasEmbeddedDoltDirectory, hasJSONL bool, coexistingSQLite string, backupPaths, markers []string) (freshTopology, error) {
	sort.Strings(markers)
	doltMode := func(mode string) freshTopology {
		return freshTopology{
			Mode:             mode,
			DoltDatabase:     metadata.DoltDatabase,
			MetadataDatabase: metadata.Database,
			CoexistingSQLite: coexistingSQLite,
			SQLiteBackups:    backupPaths,
			Markers:          markers,
		}
	}
	if metadata.DoltMode == "server" {
		return doltMode("dolt-server"), nil
	}
	// Before v0.63, "embedded" Dolt lived under .beads/dolt. Keep that
	// historical root in the legacy family; current embedded storage is
	// distinguished by .beads/embeddeddolt.
	if hasLegacyDoltDirectory {
		return doltMode("dolt-legacy"), nil
	}
	if metadata.DoltMode == "embedded" {
		return doltMode("dolt-embedded"), nil
	}
	if metadata.Backend == "dolt" {
		return doltMode("dolt-legacy"), nil
	}
	if hasEmbeddedDoltDirectory {
		return doltMode("dolt-embedded"), nil
	}
	if len(sqliteRoots.active) != 1 {
		if len(sqliteRoots.candidates) == 0 && hasJSONL {
			markers = append(markers, "data:.beads/issues.jsonl")
			sort.Strings(markers)
			return freshTopology{Mode: "jsonl", JSONL: filepath.Join(".beads", "issues.jsonl"), Markers: markers}, nil
		}
		return freshTopology{}, fmt.Errorf("unsupported fresh storage profile: expected exactly one active .beads/*.db SQLite database, found %d", len(sqliteRoots.active))
	}
	name := filepath.Base(sqliteRoots.active[0])
	for _, backup := range sqliteRoots.backups {
		if sqliteRoots.backupBases[backup] != name {
			return freshTopology{}, fmt.Errorf("unsupported pre-Dolt SQLite backup %q for active database %q", filepath.Base(backup), name)
		}
	}
	markers = append(markers, "database:.beads/"+name)
	sort.Strings(markers)
	return freshTopology{
		Mode: "sqlite", Database: filepath.Join(".beads", name),
		SQLiteBackups: backupPaths, Markers: markers,
	}, nil
}

type sqliteRootInventory struct {
	candidates  []string
	active      []string
	backups     []string
	backupBases map[string]string
}

func inventorySQLiteRoots(beadsDir string) (sqliteRootInventory, error) {
	candidates, err := filepath.Glob(filepath.Join(beadsDir, "*.db"))
	if err != nil {
		return sqliteRootInventory{}, err
	}
	result := sqliteRootInventory{
		candidates:  candidates,
		active:      make([]string, 0, len(candidates)),
		backups:     make([]string, 0, len(candidates)),
		backupBases: make(map[string]string),
	}
	for _, candidate := range candidates {
		info, err := os.Lstat(candidate)
		if err != nil {
			return sqliteRootInventory{}, fmt.Errorf("inspect SQLite root %q: %w", filepath.Base(candidate), err)
		}
		if !info.Mode().IsRegular() {
			return sqliteRootInventory{}, fmt.Errorf("non-regular SQLite root %q", filepath.Base(candidate))
		}
		name := filepath.Base(candidate)
		if activeName, ok := preDoltSQLiteBackupActiveName(name); ok {
			result.backups = append(result.backups, candidate)
			result.backupBases[candidate] = activeName
		} else if strings.Contains(name, "backup-pre-dolt") {
			return sqliteRootInventory{}, fmt.Errorf("unsupported pre-Dolt SQLite backup-like name %q", name)
		} else {
			result.active = append(result.active, candidate)
		}
	}
	return result, nil
}

func preDoltSQLiteBackupActiveName(name string) (string, bool) {
	const marker = ".backup-pre-dolt-"
	if !strings.HasSuffix(name, ".db") {
		return "", false
	}
	withoutExtension := strings.TrimSuffix(name, ".db")
	markerIndex := strings.LastIndex(withoutExtension, marker)
	if markerIndex <= 0 {
		return "", false
	}
	suffix := withoutExtension[markerIndex+len(marker):]
	if len(suffix) < len("20060102-150405") {
		return "", false
	}
	timestamp := suffix[:len("20060102-150405")]
	parsed, err := time.Parse("20060102-150405", timestamp)
	if err != nil || parsed.Format("20060102-150405") != timestamp {
		return "", false
	}
	collision := suffix[len(timestamp):]
	if collision != "" {
		if !strings.HasPrefix(collision, "-") || len(collision) == 1 || collision[1] == '0' {
			return "", false
		}
		number, err := strconv.ParseUint(collision[1:], 10, 64)
		if err != nil || number == 0 {
			return "", false
		}
	}
	return withoutExtension[:markerIndex] + ".db", true
}

// localVersionTopologyMarker identifies only the bounded historical cohort
// relevant to the server-layout transition. It deliberately does not include
// the exact version, which would turn an advisory local witness into one
// census family per release.
func localVersionTopologyMarker(path string) string {
	const invalid = "local-version:absent-or-invalid"
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > localVersionMaxBytes {
		return invalid
	}
	raw, err := os.ReadFile(path) //nolint:gosec // fixed path in an isolated workspace, lstat-checked above.
	if err != nil || len(raw) > localVersionMaxBytes {
		return invalid
	}
	version, err := parseLocalVersionWitness(strings.TrimSpace(string(raw)))
	if err != nil || !localVersionIsCensusBounded(version) {
		return invalid
	}
	if version.major == 0 && version.minor >= 55 && version.minor <= 62 {
		return "local-version:legacy-server"
	}
	return "local-version:other-valid"
}

func parseLocalVersionWitness(raw string) (stableVersion, error) {
	if !strings.HasPrefix(raw, "v") {
		raw = "v" + raw
	}
	return parseStableVersion(raw)
}

func localVersionIsCensusBounded(version stableVersion) bool {
	first, err := parseStableVersion("v0.9.1")
	if err != nil {
		panic(err)
	}
	last, err := parseStableVersion("v1.1.2")
	if err != nil {
		panic(err)
	}
	return first.compare(version) <= 0 && version.compare(last) <= 0
}

func readStorageMetadata(path string) (storageMetadata, error) {
	initialInfo, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return storageMetadata{}, nil
	}
	if err != nil {
		return storageMetadata{}, err
	}
	if !initialInfo.Mode().IsRegular() {
		return storageMetadata{}, errors.New("non-regular storage metadata")
	}
	if initialInfo.Size() > storageMetadataMaxBytes {
		return storageMetadata{}, errors.New("storage metadata exceeds maximum size")
	}
	file, err := os.Open(path) //nolint:gosec // fixed metadata path in the isolated workspace, lstat-checked above.
	if err != nil {
		return storageMetadata{}, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return storageMetadata{}, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return storageMetadata{}, err
	}
	if !openedInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		return storageMetadata{}, errors.New("non-regular storage metadata")
	}
	if openedInfo.Size() > storageMetadataMaxBytes || pathInfo.Size() > storageMetadataMaxBytes {
		return storageMetadata{}, errors.New("storage metadata exceeds maximum size")
	}
	raw, err := io.ReadAll(io.LimitReader(file, storageMetadataMaxBytes+1))
	if err != nil {
		return storageMetadata{}, err
	}
	if int64(len(raw)) > storageMetadataMaxBytes {
		return storageMetadata{}, errors.New("storage metadata exceeds maximum size")
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return storageMetadata{}, fmt.Errorf("read storage metadata: %w", err)
	}
	if metadata == nil {
		return storageMetadata{}, errors.New("read storage metadata: expected JSON object")
	}
	backend, err := storageMetadataString(metadata, "backend")
	if err != nil {
		return storageMetadata{}, err
	}
	doltMode, err := storageMetadataString(metadata, "dolt_mode")
	if err != nil {
		return storageMetadata{}, err
	}
	doltDatabase, err := storageMetadataString(metadata, "dolt_database")
	if err != nil {
		return storageMetadata{}, err
	}
	database, err := storageMetadataString(metadata, "database")
	if err != nil {
		return storageMetadata{}, err
	}
	return storageMetadata{
		Backend:      strings.ToLower(strings.TrimSpace(backend)),
		DoltMode:     strings.ToLower(strings.TrimSpace(doltMode)),
		DoltDatabase: strings.TrimSpace(doltDatabase),
		Database:     strings.TrimSpace(database),
	}, nil
}

func storageMetadataString(metadata map[string]any, field string) (string, error) {
	value, present := metadata[field]
	if !present {
		return "", nil
	}
	result, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("read storage metadata: field %q must be a string", field)
	}
	return result, nil
}
