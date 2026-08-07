package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFamilyIDUsesOnlyModeAndLayout(t *testing.T) {
	layout := json.RawMessage(`{"objects":[{"name":"issues"}]}`)
	first, err := familyID("sqlite", layout)
	if err != nil {
		t.Fatal(err)
	}
	second, err := familyID("sqlite", layout)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("family IDs differ: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("family ID = %q", first)
	}
}

func TestFamilyIDFromCanonicalLayoutMatchesFamilyID(t *testing.T) {
	canonicalLayout, err := canonicalJSON(json.RawMessage(`{"objects":[{"name":"issues"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	fromValidated, err := familyIDFromCanonicalLayout("sqlite", canonicalLayout)
	if err != nil {
		t.Fatal(err)
	}
	fromGeneric, err := familyID("sqlite", canonicalLayout)
	if err != nil {
		t.Fatal(err)
	}
	if fromValidated != fromGeneric {
		t.Fatalf("family ID from canonical layout = %q, want %q", fromValidated, fromGeneric)
	}
}

func TestFinishObservationStoresCanonicalLayout(t *testing.T) {
	entry := testCatalog().Versions[0]
	got, observedFamily, err := finishObservation(
		entry,
		sourceBuildAcquisitionForTest(),
		"sqlite",
		json.RawMessage(`{"topology":[],"schema":{"objects":[]}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(observedFamily.Layout) != `{"schema":{"objects":[]},"topology":[]}` {
		t.Fatalf("layout = %s", observedFamily.Layout)
	}
	if got.FamilyID != observedFamily.ID {
		t.Fatalf("observation family = %q, family ID = %q", got.FamilyID, observedFamily.ID)
	}
}

func TestAssetFallbackEligibilityRequiresAHealthyHistoricalProcessExit(t *testing.T) {
	asset := acquisitionForReleaseAsset()
	exit := eligibleAssetProcessFailure(asset, context.Background(), &exec.ExitError{})
	if !isAssetFallbackEligible(exit) {
		t.Fatal("healthy release-asset process exit is not fallback eligible")
	}
	if got := eligibleAssetProcessFailure(sourceBuildAcquisitionForTest(), context.Background(), &exec.ExitError{}); isAssetFallbackEligible(got) {
		t.Fatal("source-build process exit became an asset fallback")
	}
	missingFlag := assetFallbackEligible(errors.New("required init flag is unavailable"))
	if !isAssetFallbackEligible(missingFlag) {
		t.Fatal("proven missing init capability is not fallback eligible")
	}
	timedOut, cancel := context.WithCancel(context.Background())
	cancel()
	if got := eligibleAssetProcessFailure(asset, timedOut, &exec.ExitError{}); isAssetFallbackEligible(got) {
		t.Fatal("timed-out process exit became fallback eligible")
	}
	if got := eligibleAssetProcessFailure(asset, context.Background(), errors.New("collector failed")); isAssetFallbackEligible(got) {
		t.Fatal("collector error became fallback eligible")
	}
}

func TestShouldFallbackToSource(t *testing.T) {
	asset := acquisitionForReleaseAsset()
	timedOut, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name     string
		acquired acquisition
		err      error
		want     bool
	}{
		{
			name:     "eligible release-asset failure",
			acquired: asset,
			err:      assetFallbackEligible(errors.New("requested mode mismatch")),
			want:     true,
		},
		{
			name:     "eligible source-build failure",
			acquired: sourceBuildAcquisitionForTest(),
			err:      assetFallbackEligible(errors.New("requested mode mismatch")),
			want:     false,
		},
		{
			name:     "release-asset timeout",
			acquired: asset,
			err:      eligibleAssetProcessFailure(asset, timedOut, &exec.ExitError{}),
			want:     false,
		},
		{
			name:     "release-asset collector failure",
			acquired: asset,
			err:      errors.New("collector failed"),
			want:     false,
		},
		{
			name:     "release-asset server failure",
			acquired: asset,
			err:      errors.New("start pinned server failed"),
			want:     false,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldFallbackToSource(test.acquired, test.err); got != test.want {
				t.Fatalf("shouldFallbackToSource() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestMarshalLegacyDoltLayoutSeparatesMixedEmbeddedStoreSchemas(t *testing.T) {
	markers := []string{"directory:.beads/dolt", "directory:.beads/embeddeddolt"}
	primary := doltFingerprint{Objects: []doltObject{{Name: "issues", Type: "table", Create: "CREATE TABLE issues (id text)"}}}
	firstEmbedded := doltFingerprint{Objects: []doltObject{{Name: "issues", Type: "table", Create: "CREATE TABLE issues (id text, state text)"}}}
	secondEmbedded := doltFingerprint{Objects: []doltObject{{Name: "issues", Type: "table", Create: "CREATE TABLE issues (id text, state text, actor text)"}}}

	firstLayout, err := marshalLegacyDoltLayout(markers, primary, &firstEmbedded)
	if err != nil {
		t.Fatal(err)
	}
	secondLayout, err := marshalLegacyDoltLayout(markers, primary, &secondEmbedded)
	if err != nil {
		t.Fatal(err)
	}
	firstID, err := familyID("dolt-legacy", firstLayout)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := familyID("dolt-legacy", secondLayout)
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatal("different embedded-store schemas collapsed into one legacy family")
	}
	if !bytes.Contains(firstLayout, []byte(`"stores":[{"name":"dolt"`)) || !bytes.Contains(firstLayout, []byte(`"name":"embeddeddolt"`)) {
		t.Fatalf("mixed legacy layout lacks canonical labeled stores: %s", firstLayout)
	}
	single, err := marshalLegacyDoltLayout(markers[:1], primary, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := marshalDoltLayout(markers[:1], primary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(single, legacy) {
		t.Fatalf("single-root legacy layout changed: %s != %s", single, legacy)
	}
}

func TestValidateCensusRequiresEveryCatalogVersionExactlyOnce(t *testing.T) {
	catalog := testCatalog()
	census := testCensus(t, catalog)
	if err := validateCensus(census, catalog); err != nil {
		t.Fatal(err)
	}

	missing := census
	missing.Observations = missing.Observations[:1]
	if err := validateCensus(missing, catalog); err == nil {
		t.Fatal("accepted missing catalog version")
	}

	duplicate := census
	duplicate.Observations = append(duplicate.Observations, duplicate.Observations[0])
	if err := validateCensus(duplicate, catalog); err == nil {
		t.Fatal("accepted duplicate catalog version")
	}

	unexpected := census
	unexpected.Observations = append(unexpected.Observations, observation{
		Version: catalog.Versions[0].Version, Scenario: "rolling-boundary", Provenance: provenanceFromEntry(catalog.Versions[0]),
		Acquisition: sourceBuildAcquisitionForTest(catalog.Versions[0]), FamilyID: census.Families[0].ID,
	})
	if err := validateCensus(unexpected, catalog); err == nil {
		t.Fatal("accepted an undeclared observation scenario")
	}
}

func TestValidateCensusAllowsDifferentAcquisitionsForDifferentStorageModes(t *testing.T) {
	catalog := testCatalog()
	catalog.Versions[1].GitHubRelease = &catalogRelease{SourceRelation: "matches_proxy_origin", LinuxAMD64Asset: &catalogAsset{}}
	result := testCensus(t, catalog)
	changed := false
	for i := range result.Observations {
		if result.Observations[i].Version == catalog.Versions[1].Version {
			result.Observations[i].Acquisition = acquisitionForReleaseAsset()
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("test census needs an observation for the second release")
	}
	if err := validateCensus(result, catalog); err != nil {
		t.Fatalf("rejected mixed per-mode fresh acquisitions: %v", err)
	}
}

func TestValidateRollingReferencesRequiresFreshAcquisitionForEveryRecord(t *testing.T) {
	catalog := testCatalog()
	result := testCensus(t, catalog)
	family := censusTestFamilyByMode(t, result, "dolt-server")
	target := catalog.Versions[1].Version
	conflicting := sourceBuildAcquisitionForTest(catalog.Versions[1])
	conflicting.BuildIdentitySHA256 = strings.Repeat("c", sha256.Size*2)

	for name, add := range map[string]func(*census){
		"transition": func(c *census) {
			c.Transitions = []lineageTransition{{
				FromFamilyID: family.ID, TargetVersion: target, Scenario: rollingServerScenario,
				Mode: "dolt-server", RuntimeMode: "dolt-server", ToFamilyID: family.ID, Acquisition: conflicting,
			}}
		},
		"outcome": func(c *census) {
			c.Outcomes = []lineageOutcome{{
				FromFamilyID: family.ID, TargetVersion: target, Scenario: rollingServerScenario,
				Mode: "dolt-server", RuntimeMode: "dolt-server", Outcome: lineageOutcomeUnchangedRefusal, Acquisition: conflicting,
			}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := result
			add(&candidate)
			if err := validateRollingReferences(candidate, catalog, censusFamilyMap(candidate)); err == nil {
				t.Fatalf("accepted a %s acquisition that differs from the fresh binary", name)
			}
		})
	}
}

func TestValidateRollingReferencesAllowsPerModeFreshAcquisitions(t *testing.T) {
	catalog := testCatalog()
	catalog.Versions[1].GitHubRelease = &catalogRelease{SourceRelation: "matches_proxy_origin", LinuxAMD64Asset: &catalogAsset{}}
	result := testCensus(t, catalog)
	target := catalog.Versions[1].Version
	var doltFamily family
	for _, candidate := range result.Families {
		switch candidate.Mode {
		case "dolt-server":
			doltFamily = candidate
		}
	}
	if doltFamily.ID == "" {
		t.Fatal("test census needs a Dolt family")
	}
	jsonlFamily := lineageTestFamily(t, "jsonl", "asset-jsonl")
	result.Families = append(result.Families, jsonlFamily)
	result.Observations = append(result.Observations, observation{
		Version: target, Scenario: freshScenario, Provenance: provenanceFromEntry(catalog.Versions[1]),
		Acquisition: acquisitionForReleaseAsset(), FamilyID: jsonlFamily.ID,
	})
	result.Transitions = []lineageTransition{{
		FromFamilyID: doltFamily.ID, TargetVersion: target, Scenario: rollingServerScenario,
		Mode: "dolt-server", RuntimeMode: "dolt-server", ToFamilyID: doltFamily.ID, Acquisition: sourceBuildAcquisitionForTest(catalog.Versions[1]),
	}}
	if err := validateRollingReferences(result, catalog, censusFamilyMap(result)); err != nil {
		t.Fatalf("rejected rolling acquisition matching its target mode: %v", err)
	}
}

func TestValidateCensusChecksPostSQLiteEraTargetAcquisition(t *testing.T) {
	catalog := testCatalog()
	result := testCensus(t, catalog)
	if err := validateCensus(result, catalog); err != nil {
		t.Fatalf("rejected valid post-SQLite-era target acquisition: %v", err)
	}
	changed := false
	for index := range result.Transitions {
		transition := &result.Transitions[index]
		if transition.Mode == "sqlite" && transition.TargetVersion == "v1.1.2" {
			transition.Acquisition.BuildIdentitySHA256 = strings.Repeat("c", sha256.Size*2)
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("test census needs a post-SQLite-era transition")
	}
	if err := validateCensus(result, catalog); err == nil {
		t.Fatal("accepted a tampered post-SQLite-era target acquisition")
	}
}

func TestValidateFreshCensusRequiresCanonicalObservationOrder(t *testing.T) {
	catalog := testCatalog()
	result := testCensus(t, catalog)
	if len(result.Observations) < 2 {
		t.Fatal("test census needs multiple observations")
	}
	result.Observations[0], result.Observations[1] = result.Observations[1], result.Observations[0]
	if err := validateFreshCensus(result, catalog); err == nil {
		t.Fatal("accepted observations outside canonical order")
	}
}

func TestValidateFreshCensusReportsSortedReferencesForInvalidFamilyPayload(t *testing.T) {
	catalog := testCatalog()
	result := testCensus(t, catalog)
	candidate := censusTestFamilyByMode(t, result, "dolt-server")
	for index := range result.Families {
		if result.Families[index].ID == candidate.ID {
			result.Families[index].Layout = json.RawMessage(`{"schema":{}}`)
			break
		}
	}
	result.Observations = append(result.Observations,
		observation{Version: "v1.1.2", Scenario: "zzz", FamilyID: candidate.ID},
		observation{Version: "v0.9.1", Scenario: "aaa", FamilyID: candidate.ID},
	)

	err := validateFreshCensus(result, catalog)
	if err == nil {
		t.Fatal("accepted an invalid Dolt family payload")
	}
	message := err.Error()
	if !strings.Contains(message, `mode "dolt-server"`) {
		t.Fatalf("error = %q, want mode", message)
	}
	first := strings.Index(message, "v0.9.1/aaa")
	second := strings.Index(message, "v1.1.2/zzz")
	if first < 0 || second < 0 || first >= second {
		t.Fatalf("error = %q, want canonically sorted observation references", message)
	}
}

func TestValidateFreshCensusAcceptsCanonicalEmptyDoltWithCoexistingSQLite(t *testing.T) {
	for _, mode := range []string{"dolt-legacy", "dolt-server"} {
		t.Run(mode, func(t *testing.T) {
			catalog := testCatalog()
			if mode == "dolt-legacy" {
				catalog.Versions = append(catalog.Versions[:1], append([]catalogEntry{{
					Version: "v0.49.1", Sum: "h1:middle", GoModSum: "h1:middlemod",
					Origin:    catalogOrigin{Hash: strings.Repeat("c", 40), Ref: "refs/tags/v0.49.1"},
					SourceZip: catalogSourceZip{SHA256: strings.Repeat("3", sha256.Size*2), Size: 150},
				}}, catalog.Versions[1:]...)...)
			}
			result := testCensus(t, catalog)
			original := censusTestFamilyByMode(t, result, mode)
			layout := testDoltLayout(t,
				testCanonicalEmptyDoltFingerprint(),
				ptr(testSQLiteFingerprint("coexisting")),
				testCanonicalEmptyDoltTopology(mode),
				nil, nil, nil,
			)
			id, err := familyID(mode, layout)
			if err != nil {
				t.Fatal(err)
			}
			for index := range result.Families {
				if result.Families[index].ID == original.ID {
					result.Families[index] = family{ID: id, Mode: mode, Layout: layout}
				}
			}
			for index := range result.Observations {
				if result.Observations[index].FamilyID == original.ID {
					result.Observations[index].FamilyID = id
				}
			}
			sort.Slice(result.Families, func(i, j int) bool { return result.Families[i].ID < result.Families[j].ID })
			if err := validateFreshCensus(result, catalog); err != nil {
				t.Fatalf("rejected canonical empty %s family: %v", mode, err)
			}
		})
	}
}

func TestValidateFreshCensusBoundsFreshDefaultJSONLObservations(t *testing.T) {
	catalog := testCatalog()
	catalog.Versions = []catalogEntry{
		catalog.Versions[0],
		{Version: "v0.50.1", Sum: "h1:before", GoModSum: "h1:beforemod", Origin: catalogOrigin{Hash: strings.Repeat("c", 40), Ref: "refs/tags/v0.50.1"}, SourceZip: catalogSourceZip{SHA256: strings.Repeat("3", sha256.Size*2), Size: 303}},
		{Version: "v0.50.2", Sum: "h1:first-default", GoModSum: "h1:first-defaultmod", Origin: catalogOrigin{Hash: strings.Repeat("d", 40), Ref: "refs/tags/v0.50.2"}, SourceZip: catalogSourceZip{SHA256: strings.Repeat("4", sha256.Size*2), Size: 404}},
		{Version: "v0.50.3", Sum: "h1:second-default", GoModSum: "h1:second-defaultmod", Origin: catalogOrigin{Hash: strings.Repeat("e", 40), Ref: "refs/tags/v0.50.3"}, SourceZip: catalogSourceZip{SHA256: strings.Repeat("5", sha256.Size*2), Size: 505}},
		catalog.Versions[1],
	}
	result := testCensus(t, catalog)
	id := censusTestFamilyByMode(t, result, "jsonl").ID
	if err := validateFreshCensus(result, catalog); err != nil {
		t.Fatalf("rejected exact bounded JSONL fresh-default pair: %v", err)
	}

	missing := result
	missing.Observations = append([]observation(nil), result.Observations...)
	for i := range missing.Observations {
		if missing.Observations[i].Version == "v0.50.3" && missing.Observations[i].Scenario == freshScenario {
			missing.Observations = append(missing.Observations[:i], missing.Observations[i+1:]...)
			break
		}
	}
	if err := validateFreshCensus(missing, catalog); err == nil {
		t.Fatal("accepted a census missing the v0.50.3 JSONL fresh-default observation")
	}

	extra := result
	extra.Observations = append(extra.Observations, observation{
		Version: "v0.50.1", Scenario: freshScenario, Provenance: provenanceFromEntry(catalog.Versions[1]),
		Acquisition: sourceBuildAcquisitionForTest(catalog.Versions[1]), FamilyID: id,
	})
	sortObservations(extra.Observations)
	if err := validateFreshCensus(extra, catalog); err == nil {
		t.Fatal("accepted a JSONL fresh-default observation outside the allowed releases")
	}

	split := result
	split.Families = append([]family(nil), result.Families...)
	split.Observations = append([]observation(nil), result.Observations...)
	secondLayout := testFamilyLayout(t, "jsonl", "fresh-default-second")
	secondID, err := familyID("jsonl", secondLayout)
	if err != nil {
		t.Fatal(err)
	}
	split.Families = append(split.Families, family{ID: secondID, Mode: "jsonl", Layout: secondLayout})
	sort.Slice(split.Families, func(i, j int) bool { return split.Families[i].ID < split.Families[j].ID })
	for i := range split.Observations {
		if split.Observations[i].Version == "v0.50.3" && split.Observations[i].Scenario == freshScenario {
			split.Observations[i].FamilyID = secondID
			break
		}
	}
	if err := validateFreshCensus(split, catalog); err == nil {
		t.Fatal("accepted bounded JSONL fresh-default observations in separate families")
	}
}

func TestValidateCensusRejectsProvenanceAndFamilyTampering(t *testing.T) {
	catalog := testCatalog()

	provenance := testCensus(t, catalog)
	provenance.Observations[0].Provenance.Sum = "h1:tampered"
	if err := validateCensus(provenance, catalog); err == nil {
		t.Fatal("accepted provenance not present in catalog")
	}

	family := testCensus(t, catalog)
	family.Observations[0].FamilyID = "sha256:tampered"
	if err := validateCensus(family, catalog); err == nil {
		t.Fatal("accepted invalid family ID")
	}

	acquisition := testCensus(t, catalog)
	acquisition.Observations[0].Acquisition = acquisitionForReleaseAsset()
	if err := validateCensus(acquisition, catalog); err == nil {
		t.Fatal("accepted release-asset acquisition for a source-only catalog entry")
	}
}

func TestValidateFreshCensusRejectsFamilyModeDifferentFromFreshScenario(t *testing.T) {
	catalog := testCatalog()
	census := testCensus(t, catalog)

	var doltFamilyID string
	for _, candidate := range census.Families {
		if candidate.Mode == "dolt-server" {
			doltFamilyID = candidate.ID
			break
		}
	}
	if doltFamilyID == "" {
		t.Fatal("test census has no Dolt family")
	}
	for i := range census.Observations {
		if census.Observations[i].Scenario == freshSQLiteScenario {
			census.Observations[i].FamilyID = doltFamilyID
			break
		}
	}

	if err := validateFreshCensus(census, catalog); err == nil {
		t.Fatal("accepted fresh SQLite scenario mapped to a Dolt family")
	}
}

func TestValidateCensusRejectsOrphanRollingTransition(t *testing.T) {
	catalog := testCatalog()
	result := testCensus(t, catalog)
	source := censusTestFamilyByMode(t, result, "dolt-server")
	rolling := lineageTestFamily(t, source.Mode, "rolling-only")
	result.Families = append(result.Families, rolling)
	sort.Slice(result.Families, func(i, j int) bool { return result.Families[i].ID < result.Families[j].ID })
	result.Transitions = []lineageTransition{{
		FromFamilyID:  source.ID,
		TargetVersion: catalog.Versions[len(catalog.Versions)-1].Version,
		Scenario:      rollingScenarioForMode(t, source.Mode),
		Mode:          source.Mode,
		RuntimeMode:   source.Mode,
		ToFamilyID:    rolling.ID,
		Acquisition:   sourceBuildAcquisitionForTest(catalog.Versions[len(catalog.Versions)-1]),
	}}

	if err := validateCensus(result, catalog); err == nil {
		t.Fatal("accepted a transition without a prior rolling frontier")
	}
}

func TestValidateCensusRejectsInvalidRollingTransition(t *testing.T) {
	catalog := testCatalog()
	base := testCensus(t, catalog)
	source := censusTestFamilyByMode(t, base, "dolt-server")
	valid := lineageTransition{
		FromFamilyID:  source.ID,
		TargetVersion: catalog.Versions[len(catalog.Versions)-1].Version,
		Scenario:      rollingScenarioForMode(t, source.Mode),
		Mode:          source.Mode,
		RuntimeMode:   source.Mode,
		ToFamilyID:    source.ID,
		Acquisition:   sourceBuildAcquisitionForTest(catalog.Versions[len(catalog.Versions)-1]),
	}
	for name, mutate := range map[string]func(*lineageTransition){
		"unknown endpoint": func(edge *lineageTransition) { edge.ToFamilyID = "sha256:unknown" },
		"mode mismatch":    func(edge *lineageTransition) { edge.Mode = "jsonl" },
		"bad acquisition":  func(edge *lineageTransition) { edge.Acquisition = acquisition{} },
	} {
		t.Run(name, func(t *testing.T) {
			result := testCensus(t, catalog)
			edge := valid
			mutate(&edge)
			result.Transitions = []lineageTransition{edge}
			if err := validateCensus(result, catalog); err == nil {
				t.Fatalf("accepted invalid transition %#v", edge)
			}
		})
	}

	duplicate := base
	duplicate.Transitions = []lineageTransition{valid, valid}
	if err := validateCensus(duplicate, catalog); err == nil {
		t.Fatal("accepted duplicate rolling transition")
	}
}

func TestValidateCensusRejectsMissingRollingAttempt(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1", "v0.9.2")
	sqlite := lineageTestFamily(t, "sqlite", "sqlite")
	result := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], sqlite.ID),
		rollingSQLiteFreshObservation(catalog.Versions[1], sqlite.ID),
	}, sqlite)
	if err := validateCensus(result, catalog); err == nil {
		t.Fatal("accepted a fresh-only corpus with an unrecorded rolling attempt")
	}
	if err := validateFreshCensus(result, catalog); err != nil {
		t.Fatalf("rejected a pre-rolling fresh census: %v", err)
	}
}

func TestValidateCensusRequiresFingerprintSpecVersion(t *testing.T) {
	catalog := testCatalog()
	result := testCensus(t, catalog)
	result.FingerprintSpecVersion = 0
	if err := validateCensus(result, catalog); err == nil {
		t.Fatal("accepted census without fingerprint semantics version")
	}
}

func TestMergeRollingLineagesAddsCanonicalFamiliesAndTransitions(t *testing.T) {
	result := testCensus(t, testCatalog())
	result.Transitions = nil
	source := censusTestFamilyByMode(t, result, "dolt-server")
	first := lineageTestFamily(t, "dolt-server", "rolling-first")
	second := lineageTestFamily(t, "dolt-server", "rolling-second")
	acquired := sourceBuildAcquisitionForTest()
	sets := []lineageSet{
		{SchemaVersion: lineageSchemaVersion, Transitions: []lineageTransition{{
			FromFamilyID: source.ID, TargetVersion: "v1.1.2", Scenario: rollingServerScenario,
			Mode: "dolt-server", RuntimeMode: "dolt-server", ToFamilyID: second.ID, Acquisition: acquired,
		}}},
		{SchemaVersion: lineageSchemaVersion, Transitions: []lineageTransition{{
			FromFamilyID: source.ID, TargetVersion: "v1.1.1", Scenario: rollingServerScenario,
			Mode: "dolt-server", RuntimeMode: "dolt-server", ToFamilyID: first.ID, Acquisition: acquired,
		}}},
	}
	if err := mergeRollingLineages(&result, sets, [][]family{{second}, {first, second}}); err != nil {
		t.Fatal(err)
	}
	if len(result.Families) < 2 ||
		result.Families[len(result.Families)-2].ID >= result.Families[len(result.Families)-1].ID {
		t.Fatalf("families are not canonical: %#v", result.Families)
	}
	if len(result.Transitions) != 2 ||
		compareLineageTransitions(result.Transitions[0], result.Transitions[1]) >= 0 {
		t.Fatalf("transitions are not canonical: %#v", result.Transitions)
	}

	conflict := first
	conflict.Layout = json.RawMessage(`{"different":true}`)
	if err := mergeRollingLineages(&result, nil, [][]family{{conflict}}); err == nil {
		t.Fatal("accepted conflicting definitions for one rolling family ID")
	}
}

func TestCensusJSONIsCanonicalAndOfflineValidatable(t *testing.T) {
	catalog := testCatalog()
	catalogRaw, err := encodeCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	census := testCensus(t, catalog)
	raw, err := encodeCensus(census)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(raw), "\n") {
		t.Fatal("canonical JSON lacks final newline")
	}

	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "catalog.json")
	censusPath := filepath.Join(dir, "census.json")
	if err := os.WriteFile(catalogPath, catalogRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(censusPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFiles(catalogPath, censusPath, false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(censusPath, []byte(strings.TrimSuffix(string(raw), "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFiles(catalogPath, censusPath, false); err == nil {
		t.Fatal("accepted non-canonical census JSON")
	}
}

func TestVerifyReleaseAssetOnlyForMatchingProxyOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beads_linux_amd64.tar.gz")
	content := []byte("release asset")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	asset := catalogAsset{Name: filepath.Base(path), Size: int64(len(content)), Digest: "sha256:" + hexDigest(content)}
	matching := catalogEntry{GitHubRelease: &catalogRelease{SourceRelation: "matches_proxy_origin", LinuxAMD64Asset: &asset}}
	if err := verifyReleaseAsset(matching, path); err != nil {
		t.Fatal(err)
	}
	drifting := matching
	drifting.GitHubRelease = &catalogRelease{SourceRelation: "tag_drift", LinuxAMD64Asset: &asset}
	if err := verifyReleaseAsset(drifting, ""); err != nil {
		t.Fatalf("tag-drift asset should be skipped: %v", err)
	}
}

func TestValidAcquisitionBoundsAssetFallbackReason(t *testing.T) {
	if !validAcquisition(acquisition{
		Kind: "source-build", BuildIdentitySHA256: strings.Repeat("a", sha256.Size*2), GoToolchain: goToolchain,
		AssetFallback: "release-asset-runtime-failure",
	}) {
		t.Fatal("rejected the bounded authenticated-source fallback")
	}
	if validAcquisition(acquisition{
		Kind: "source-build", BuildIdentitySHA256: strings.Repeat("a", sha256.Size*2), GoToolchain: goToolchain,
		AssetFallback: "hand-authored-exception",
	}) {
		t.Fatal("accepted an unbounded asset fallback reason")
	}
}

func TestValidAcquisitionRequiresKindSpecificDigest(t *testing.T) {
	if validAcquisition(acquisition{Kind: "source-build", GoToolchain: goToolchain}) {
		t.Fatal("accepted a source acquisition without a build identity digest")
	}
	if validAcquisition(acquisition{Kind: "release-asset"}) {
		t.Fatal("accepted a release acquisition without an executable digest")
	}
}

func TestAcquisitionJSONIsAStrictDiscriminatedUnion(t *testing.T) {
	entry := testCatalog().Versions[0]
	identity, err := sourceBuildIdentity(entry)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"release asset", `{"kind":"release-asset","executable_sha256":"` + strings.Repeat("a", sha256.Size*2) + `"}`, true},
		{"source build", `{"kind":"source-build","build_identity_sha256":"` + identity + `","go_toolchain":"` + goToolchain + `"}`, true},
		{"mixed digests", `{"kind":"source-build","executable_sha256":"` + strings.Repeat("a", sha256.Size*2) + `","build_identity_sha256":"` + identity + `","go_toolchain":"` + goToolchain + `"}`, false},
		{"missing source identity", `{"kind":"source-build","go_toolchain":"` + goToolchain + `"}`, false},
		{"release toolchain", `{"kind":"release-asset","executable_sha256":"` + strings.Repeat("a", sha256.Size*2) + `","go_toolchain":"` + goToolchain + `"}`, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var got acquisition
			err := json.Unmarshal([]byte(test.raw), &got)
			if (err == nil) != test.want {
				t.Fatalf("json.Unmarshal() error = %v, want success=%t", err, test.want)
			}
		})
	}
}

func TestSourceBuildIdentityIsStableAndSensitiveOnlyToBuildInputs(t *testing.T) {
	entry := testCatalog().Versions[0]
	got, err := sourceBuildIdentity(entry)
	if err != nil {
		t.Fatal(err)
	}
	const want = "4faae9efcd27ef8bdde4498c249292fed2df3ee9cb0947d06136cf75c0863b71"
	if got != want {
		t.Fatalf("sourceBuildIdentity() = %q, want %q", got, want)
	}
	changed := entry
	changed.SourceZip.SHA256 = strings.Repeat("f", sha256.Size*2)
	other, err := sourceBuildIdentity(changed)
	if err != nil {
		t.Fatal(err)
	}
	if other == got {
		t.Fatal("source build identity ignored source zip digest")
	}
	if sourceBuildRecipe().GoBuild.ModFile != "build.mod" {
		t.Fatalf("normalized modfile = %q", sourceBuildRecipe().GoBuild.ModFile)
	}
}

func TestAcquireReleaseAssetUsesFreshExecutableAndCachesOnlyArchive(t *testing.T) {
	cache := t.TempDir()
	binaryContent := []byte("authenticated executable")
	entry := releaseAssetEntryForTest(t, cache, binaryContent)

	first, err := acquireReleaseAsset(context.Background(), entry, cache)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("tampered executable"), 0o755); err != nil {
		t.Fatal(err)
	}

	second, err := acquireReleaseAsset(context.Background(), entry, cache)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("release executable was reused at %q", first)
	}
	got, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(binaryContent) {
		t.Fatalf("reused executable = %q, want archive-extracted %q", got, binaryContent)
	}
	if err := validateSourceBuildCache(cache, catalog{Versions: []catalogEntry{entry}}); err != nil {
		t.Fatalf("validate archive-only cache: %v", err)
	}
}

func TestVerifyReleaseAssetRejectsSymlink(t *testing.T) {
	cache := t.TempDir()
	entry := releaseAssetEntryForTest(t, cache, []byte("authenticated executable"))
	asset := entry.GitHubRelease.LinuxAMD64Asset
	archive := filepath.Join(cache, "assets", entry.Version, strings.TrimPrefix(asset.Digest, "sha256:"), asset.Name)
	target := filepath.Join(t.TempDir(), asset.Name)
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, archive); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseAsset(entry, archive); err == nil {
		t.Fatal("accepted symlinked release archive")
	}
}

func TestVerifyReleaseAssetAcceptsAuthenticatedStagingName(t *testing.T) {
	cache := t.TempDir()
	entry := releaseAssetEntryForTest(t, cache, []byte("authenticated executable"))
	asset := entry.GitHubRelease.LinuxAMD64Asset
	archive := filepath.Join(cache, "assets", entry.Version, strings.TrimPrefix(asset.Digest, "sha256:"), asset.Name)
	staging := archive + ".tmp"
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staging, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyReleaseAsset(entry, staging); err != nil {
		t.Fatalf("rejected authenticated staging archive: %v", err)
	}
}

func TestExtractReleaseBinaryRejectsUnsafeBDEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []releaseArchiveEntry
		limit   int64
	}{
		{
			name: "duplicate bd",
			entries: []releaseArchiveEntry{
				{header: tar.Header{Name: "bd", Mode: 0o755}, body: []byte("first")},
				{header: tar.Header{Name: "nested/bd", Mode: 0o755}, body: []byte("second")},
			},
			limit: 16,
		},
		{
			name: "symlink bd",
			entries: []releaseArchiveEntry{
				{header: tar.Header{Name: "bd", Typeflag: tar.TypeSymlink, Linkname: "outside"}},
			},
			limit: 16,
		},
		{
			name: "hardlink bd",
			entries: []releaseArchiveEntry{
				{header: tar.Header{Name: "bd", Typeflag: tar.TypeLink, Linkname: "outside"}},
			},
			limit: 16,
		},
		{
			name: "special bd",
			entries: []releaseArchiveEntry{
				{header: tar.Header{Name: "bd", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}},
			},
			limit: 16,
		},
		{
			name: "oversized bd",
			entries: []releaseArchiveEntry{
				{header: tar.Header{Name: "bd", Mode: 0o755}, body: []byte("too large")},
			},
			limit: 8,
		},
		{
			name: "traversal named bd",
			entries: []releaseArchiveEntry{
				{header: tar.Header{Name: "../../bd", Mode: 0o755}, body: []byte("executable")},
			},
			limit: 16,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "bd")
			if err := os.WriteFile(destination, []byte("existing binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := extractReleaseBinary(bytes.NewReader(releaseArchiveForTest(t, test.entries)), destination, test.limit); err == nil {
				t.Fatal("accepted unsafe release archive")
			}
			got, err := os.ReadFile(destination)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "existing binary" {
				t.Fatalf("destination changed after rejected archive: %q", got)
			}
		})
	}
}

func TestExtractReleaseBinaryWritesAnExecutableAtomically(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "bd")
	entries := []releaseArchiveEntry{{
		header: tar.Header{Name: "bd", Mode: 0o755}, body: []byte("safe executable"),
	}}
	if err := extractReleaseBinary(bytes.NewReader(releaseArchiveForTest(t, entries)), destination, 32); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "safe executable" {
		t.Fatalf("extracted binary = %q", got)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode = %o, want 755", info.Mode().Perm())
	}
}

func TestCopyReleaseAssetBodyRequiresExactCatalogSize(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		size int64
		want bool
	}{
		{name: "exact", body: "1234", size: 4, want: true},
		{name: "short", body: "123", size: 4},
		{name: "oversized", body: "12345", size: 4},
	} {
		t.Run(test.name, func(t *testing.T) {
			var destination bytes.Buffer
			_, err := copyReleaseAssetBody(&destination, strings.NewReader(test.body), test.size)
			if (err == nil) != test.want {
				t.Fatalf("copyReleaseAssetBody() error = %v, want success=%t", err, test.want)
			}
		})
	}
}

type releaseArchiveEntry struct {
	header tar.Header
	body   []byte
}

func releaseArchiveForTest(t *testing.T, entries []releaseArchiveEntry) []byte {
	t.Helper()
	var raw bytes.Buffer
	compressed := gzip.NewWriter(&raw)
	writer := tar.NewWriter(compressed)
	for _, entry := range entries {
		header := entry.header
		header.Size = int64(len(entry.body))
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestAcquireRecordedReleaseAssetMayReacquireAndVerifiesItsSerializedDigest(t *testing.T) {
	cache := t.TempDir()
	entry := releaseAssetEntryForTest(t, cache, []byte("authenticated executable"))
	_, recorded, err := acquireBinary(context.Background(), entry, cache)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := acquireRecordedBinary(context.Background(), entry, cache, "sqlite", recorded)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := executableSHA256(binary); err != nil || got != recorded.ExecutableSHA256 {
		t.Fatalf("reacquired release digest = %q, error = %v", got, err)
	}
}

func TestAcquireRecordedSourceBuildRequiresAnUntamperedFreshBinding(t *testing.T) {
	entry := testCatalog().Versions[0]
	recorded := sourceBuildAcquisitionForTest(entry)
	binary := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(binary, []byte("source output one"), 0o755); err != nil {
		t.Fatal(err)
	}
	binding, err := bindFreshBinary(binary, recorded)
	if err != nil {
		t.Fatal(err)
	}
	missing := context.Background()
	if _, err := acquireRecordedBinary(missing, entry, t.TempDir(), "sqlite", recorded); err == nil {
		t.Fatal("source build without a fresh binding was accepted")
	}
	ctx := withFreshBinaries(context.Background(), map[freshBinaryKey]freshBinary{{version: entry.Version, mode: "sqlite"}: binding})
	if _, err := acquireRecordedBinary(ctx, entry, t.TempDir(), "sqlite", recorded); err != nil {
		t.Fatalf("valid fresh source binding rejected: %v", err)
	}
	if err := os.WriteFile(binary, []byte("source output mutated"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRecordedBinary(ctx, entry, t.TempDir(), "sqlite", recorded); err == nil {
		t.Fatal("mutated fresh source binding was accepted")
	}
}

func TestFreshSourceBindingRejectsSymlinkAndNonRegularOutputs(t *testing.T) {
	entry := testCatalog().Versions[0]
	recorded := sourceBuildAcquisitionForTest(entry)
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("target"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "bd-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := bindFreshBinary(link, recorded); err == nil {
		t.Fatal("symlinked source binary was accepted")
	}
	if _, err := bindFreshBinary(t.TempDir(), recorded); err == nil {
		t.Fatal("non-regular source binary was accepted")
	}
}

func TestSourceBuildIdentityDoesNotSerializeOutputDigest(t *testing.T) {
	entry := testCatalog().Versions[0]
	recorded := sourceBuildAcquisitionForTest(entry)
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	if err := os.WriteFile(first, []byte("first output"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second output"), 0o755); err != nil {
		t.Fatal(err)
	}
	firstBinding, err := bindFreshBinary(first, recorded)
	if err != nil {
		t.Fatal(err)
	}
	secondBinding, err := bindFreshBinary(second, recorded)
	if err != nil {
		t.Fatal(err)
	}
	if firstBinding.executableSHA256 == secondBinding.executableSHA256 {
		t.Fatal("test outputs unexpectedly have the same digest")
	}
	raw, err := json.Marshal(recorded)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), firstBinding.executableSHA256) || strings.Contains(string(raw), secondBinding.executableSHA256) {
		t.Fatalf("source acquisition serialized an output digest: %s", raw)
	}
}

func TestSourceBuildEnvironmentPinsAReproducibleLinuxAMD64Build(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch")
	environment := sourceBuildEnvironment([]string{
		"HOME=/home/census", "PATH=/toolchain/bin", "HTTPS_PROXY=https://proxy.example.test",
		"SSL_CERT_FILE=/certs/ca.pem", "GOENV=/user/config", "GOOS=darwin", "GOARCH=arm64",
		"GOAMD64=v3", "GOWORK=/workspace/go.work", "GOFLAGS=-race", "GOTOOLCHAIN=auto",
		"CC=host-clang", "CGO_CFLAGS=-host-flag", "GOEXPERIMENT=host-experiment", "UNRELATED=value",
	}, scratch)
	values := environmentValues(environment)
	for name, want := range map[string]string{
		"HOME": "/home/census", "PATH": "/toolchain/bin", "HTTPS_PROXY": "https://proxy.example.test",
		"SSL_CERT_FILE": "/certs/ca.pem",
		"GOENV":         "off", "GODEBUG": "goindex=0", "GO111MODULE": "on", "GOFLAGS": "-modcacherw", "GOWORK": "off",
		"GOTOOLCHAIN": goToolchain, "GOPROXY": "https://proxy.golang.org,direct", "GOSUMDB": "sum.golang.org",
		"GOPRIVATE": "", "GONOSUMDB": "", "GONOPROXY": "",
		"GOMODCACHE": filepath.Join(scratch, "mod"), "GOCACHE": filepath.Join(scratch, "cache"), "GOPATH": filepath.Join(scratch, "gopath"),
		"GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1",
		"CGO_ENABLED": "1", "CC": "gcc", "CXX": "g++", "AR": "ar",
	} {
		if values[name] != want {
			t.Fatalf("%s = %q, want %q", name, values[name], want)
		}
	}
	allowed := map[string]bool{
		"HOME": true, "PATH": true, "HTTPS_PROXY": true, "SSL_CERT_FILE": true,
	}
	for name := range map[string]string{
		"GOENV": "", "GODEBUG": "", "GO111MODULE": "", "GOFLAGS": "", "GOWORK": "", "GOTOOLCHAIN": "", "GOPROXY": "", "GOSUMDB": "",
		"GOPRIVATE": "", "GONOSUMDB": "", "GONOPROXY": "", "GOMODCACHE": "", "GOCACHE": "", "GOPATH": "",
		"GOOS": "", "GOARCH": "", "GOAMD64": "", "CGO_ENABLED": "", "CC": "", "CXX": "", "AR": "",
	} {
		allowed[name] = true
	}
	for name := range values {
		if !allowed[name] {
			t.Fatalf("source-build environment leaked %s=%q", name, values[name])
		}
	}
}

func TestValidateGCCVersionRequiresThePinnedCompiler(t *testing.T) {
	if err := validateGCCVersion([]byte("gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.0\nCopyright")); err != nil {
		t.Fatalf("rejected pinned GCC: %v", err)
	}
	if err := validateGCCVersion([]byte("gcc (Ubuntu 13.3.0-6ubuntu2~24.04.1) 13.3.1\n")); err == nil {
		t.Fatal("accepted an unpinned GCC version")
	}
}

func TestAcquireRecordedBinaryReusesTheFreshInMemoryBinary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(binary, []byte("fresh source-built executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	entry := testCatalog().Versions[0]
	acquired, err := recordAcquisition("source-build", entry, binary, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := withFreshBinaries(context.Background(), map[freshBinaryKey]freshBinary{
		{version: entry.Version, mode: "sqlite"}: {path: binary, executableSHA256: hexDigest([]byte("fresh source-built executable")), acquisition: acquired},
	})
	got, err := acquireRecordedBinary(ctx, entry, t.TempDir(), "sqlite", acquired)
	if err != nil {
		t.Fatal(err)
	}
	if got != binary {
		t.Fatalf("binary = %q, want fresh binary %q", got, binary)
	}
}

func TestFreshBinaryForSeparatesReleaseModes(t *testing.T) {
	entry := testCatalog().Versions[0]
	asset := acquisitionForReleaseAsset()
	source := sourceBuildAcquisitionForTest()
	ctx := withFreshBinaries(context.Background(), map[freshBinaryKey]freshBinary{
		{version: entry.Version, mode: "jsonl"}:       {path: "/cache/asset-bd", executableSHA256: strings.Repeat("a", sha256.Size*2), acquisition: asset},
		{version: entry.Version, mode: "dolt-server"}: {path: "/cache/source-bd", executableSHA256: strings.Repeat("b", sha256.Size*2), acquisition: source},
	})

	jsonl, ok := freshBinaryFor(ctx, entry.Version, "jsonl", asset)
	if !ok || jsonl.path != "/cache/asset-bd" {
		t.Fatalf("JSONL binary = %#v, found=%t", jsonl, ok)
	}
	dolt, ok := freshBinaryFor(ctx, entry.Version, "dolt-server", source)
	if !ok || dolt.path != "/cache/source-bd" {
		t.Fatalf("Dolt binary = %#v, found=%t", dolt, ok)
	}
	if _, ok := freshBinaryFor(ctx, entry.Version, "jsonl", source); ok {
		t.Fatal("reused source fallback for the release-asset JSONL mode")
	}
}

func TestRequireCensusPlatformRejectsUnsupportedRuntime(t *testing.T) {
	if err := requireCensusPlatform("linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	if err := requireCensusPlatform("darwin", "arm64"); err == nil {
		t.Fatal("accepted an unsupported generation runtime")
	}
}

func TestCensusEnvironmentDisablesHistoricalDaemons(t *testing.T) {
	environment, err := censusEnvironment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	for name, want := range map[string]string{
		"BEADS_NO_DAEMON":       "1",
		"BEADS_DOLT_AUTO_START": "0",
	} {
		if values[name] != want {
			t.Fatalf("%s = %q, want %s", name, values[name], want)
		}
	}
}

func TestExternalServerInitArgsPinTheObservedEndpoint(t *testing.T) {
	got, err := externalServerInitArgs(45123)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"init", "-p", "census", "--server",
		"--server-host", "127.0.0.1",
		"--server-port", "45123",
		"--server-user", "root",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if _, err := externalServerInitArgs(0); err == nil {
		t.Fatal("accepted invalid server port")
	}
}

func TestSupportsExternalServerInitRequiresTheCompletePublicFlagSet(t *testing.T) {
	help := []byte(`
      --server
      --server-host string
      --server-port int
      --server-user string
`)
	if !supportsExternalServerInit(help) {
		t.Fatal("rejected complete external-server init capability")
	}
	if supportsExternalServerInit([]byte("--server\n--server-host\n")) {
		t.Fatal("accepted incomplete external-server init capability")
	}
}

func TestReadStorageMetadataRejectsNonRegularFiles(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(t *testing.T, path string)
	}{
		{
			name: "symlink",
			create: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "metadata-target.json")
				if err := os.WriteFile(target, []byte(`{"backend":"dolt"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "metadata.json")
			test.create(t, path)
			if _, err := readStorageMetadata(path); err == nil {
				t.Fatal("accepted non-regular storage metadata")
			}
		})
	}
}

func TestReadStorageMetadataRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := readStorageMetadata(path)
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("accepted FIFO storage metadata")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("blocked while opening FIFO storage metadata")
	}
}

func TestReadStorageMetadataRejectsOversizedRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	raw := []byte(`{"unknown":"` + strings.Repeat("x", 128<<10) + `"}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readStorageMetadata(path); err == nil {
		t.Fatal("accepted oversized storage metadata")
	}
}

func TestReadStorageMetadataRejectsNonStringRecognizedFields(t *testing.T) {
	values := []struct {
		name string
		raw  string
	}{
		{name: "null", raw: "null"},
		{name: "boolean", raw: "true"},
		{name: "number", raw: "42"},
		{name: "array", raw: "[]"},
		{name: "object", raw: "{}"},
	}
	for _, field := range []string{"backend", "dolt_mode", "dolt_database", "database"} {
		for _, value := range values {
			t.Run(field+"/"+value.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "metadata.json")
				raw := fmt.Sprintf(`{%q:%s,"unknown":{"nested":true}}`, field, value.raw)
				if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := readStorageMetadata(path); err == nil {
					t.Fatalf("accepted %s %q field", value.name, field)
				}
			})
		}
	}
}

func TestReadStorageMetadataRejectsNullDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readStorageMetadata(path); err == nil {
		t.Fatal("accepted null storage metadata document")
	}
}

func TestReadStorageMetadataAllowsMissingFileAndUnknownFields(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "metadata.json")
	if metadata, err := readStorageMetadata(missing); err != nil || metadata != (storageMetadata{}) {
		t.Fatalf("missing metadata = %#v, error = %v", metadata, err)
	}

	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(path, []byte(`{"backend":" DOLT ","unknown":{"nested":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := readStorageMetadata(path)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Backend != "dolt" {
		t.Fatalf("backend = %q, want dolt", metadata.Backend)
	}
}

func TestRecognizeFreshTopologyDoesNotTreatEphemeralSQLiteAsSQLiteStorage(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "ephemeral.sqlite3"), []byte("not storage"), 0o600); err != nil {
		t.Fatal(err)
	}
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Mode != "dolt-legacy" {
		t.Fatalf("mode = %q, markers = %v", topology.Mode, topology.Markers)
	}
	if topology.CoexistingSQLite != "" {
		t.Fatalf("pure Dolt topology has coexisting SQLite %q", topology.CoexistingSQLite)
	}
}

func TestRecognizeFreshTopologyRetainsMetadataSelectedDoltSQLiteCoexistence(t *testing.T) {
	for _, test := range []struct {
		name             string
		databaseSelector string
		doltMode         string
		wantMode         string
	}{
		{name: "legacy backward-compatible selector", databaseSelector: "beads.db", wantMode: "dolt-legacy"},
		{name: "legacy directory selector", databaseSelector: "dolt", wantMode: "dolt-legacy"},
		{name: "server directory selector", databaseSelector: "dolt", doltMode: "server", wantMode: "dolt-server"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
				t.Fatal(err)
			}
			metadata := fmt.Sprintf(`{"backend":"dolt","database":%q,"dolt_mode":%q}`, test.databaseSelector, test.doltMode)
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			createSQLiteSchemaForTest(t, filepath.Join(beadsDir, "beads.db"), "id TEXT PRIMARY KEY")

			topology, err := recognizeFreshTopology(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if topology.Mode != test.wantMode {
				t.Fatalf("mode = %q, want %q", topology.Mode, test.wantMode)
			}
			if got, want := topology.CoexistingSQLite, ".beads/beads.db"; got != want {
				t.Fatalf("CoexistingSQLite = %q, want %q", got, want)
			}
			if !hasTopologyMarker(topology.Markers, "sqlite-coexisting:.beads/beads.db") {
				t.Fatalf("markers = %v, want metadata-selected SQLite coexistence", topology.Markers)
			}
			if !hasTopologyMarker(topology.Markers, "metadata-database:"+test.databaseSelector) {
				t.Fatalf("markers = %v, want metadata database selector", topology.Markers)
			}
		})
	}
}

func TestRecognizeFreshTopologyRejectsActiveSQLiteAlongsideDoltRoot(t *testing.T) {
	for _, test := range []struct {
		name        string
		directories []string
		databases   []string
		metadata    string
	}{
		{name: "unlabeled", directories: []string{"dolt"}, databases: []string{"beads.db"}},
		{name: "unknown database", directories: []string{"dolt"}, databases: []string{"unknown-history.db"}, metadata: `{"backend":"dolt","database":"unknown-history.db"}`},
		{name: "missing database selector", directories: []string{"dolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"dolt"}`},
		{name: "wrong database selector", directories: []string{"dolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"dolt","database":"other.db"}`},
		{name: "wrong backend", directories: []string{"dolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"sqlite","database":"beads.db"}`},
		{name: "server mode with legacy database selector", directories: []string{"dolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"dolt","database":"beads.db","dolt_mode":"server"}`},
		{name: "embedded root", directories: []string{"dolt", "embeddeddolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"dolt","database":"beads.db"}`},
		{name: "multiple databases", directories: []string{"dolt"}, databases: []string{"beads.db", "other.db"}, metadata: `{"backend":"dolt","database":"beads.db"}`},
		{name: "server missing legacy root", databases: []string{"beads.db"}, metadata: `{"backend":"dolt","database":"dolt","dolt_mode":"server"}`},
		{name: "server embedded root", directories: []string{"embeddeddolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"dolt","database":"dolt","dolt_mode":"server"}`},
		{name: "server dual roots", directories: []string{"dolt", "embeddeddolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"dolt","database":"dolt","dolt_mode":"server"}`},
		{name: "server missing backend", directories: []string{"dolt"}, databases: []string{"beads.db"}, metadata: `{"database":"dolt","dolt_mode":"server"}`},
		{name: "server missing database selector", directories: []string{"dolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"dolt","dolt_mode":"server"}`},
		{name: "server wrong database selector", directories: []string{"dolt"}, databases: []string{"beads.db"}, metadata: `{"backend":"dolt","database":"other","dolt_mode":"server"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, directory := range test.directories {
				if err := os.MkdirAll(filepath.Join(beadsDir, directory), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, database := range test.databases {
				createSQLiteSchemaForTest(t, filepath.Join(beadsDir, database), "id TEXT PRIMARY KEY")
			}
			if test.metadata != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(test.metadata), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			_, err := recognizeFreshTopology(workspace)
			if err == nil || !strings.Contains(err.Error(), "active SQLite and Dolt") {
				t.Fatalf("recognizeFreshTopology error = %v, want active SQLite/Dolt ambiguity", err)
			}
		})
	}
}

func TestRecognizeFreshTopologyRejectsNonRegularSQLiteCandidatesAlongsideDoltRoot(t *testing.T) {
	for _, test := range []struct {
		name   string
		create func(t *testing.T, path string)
	}{
		{
			name: "symlink",
			create: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
				t.Fatal(err)
			}
			test.create(t, filepath.Join(beadsDir, "beads.db"))

			_, err := recognizeFreshTopology(workspace)
			if err == nil || !strings.Contains(err.Error(), "non-regular SQLite root") {
				t.Fatalf("recognizeFreshTopology error = %v, want non-regular SQLite root", err)
			}
		})
	}
}

func TestRecognizeFreshTopologyRetainsPreDoltSQLiteBackupsAlongsideDoltRoots(t *testing.T) {
	for _, test := range []struct {
		name        string
		directories []string
		wantMode    string
	}{
		{name: "legacy root", directories: []string{"dolt"}, wantMode: "dolt-legacy"},
		{name: "dual roots", directories: []string{"dolt", "embeddeddolt"}, wantMode: "dolt-legacy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			for _, directory := range test.directories {
				if err := os.MkdirAll(filepath.Join(beadsDir, directory), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			createSQLiteSchemaForTest(t, filepath.Join(beadsDir, "beads.backup-pre-dolt-20260728-120000.db"), "id TEXT PRIMARY KEY")

			topology, err := recognizeFreshTopology(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if topology.Mode != test.wantMode {
				t.Fatalf("mode = %q, want %q", topology.Mode, test.wantMode)
			}
			if strings.Join(topology.SQLiteBackups, ",") != ".beads/beads.backup-pre-dolt-20260728-120000.db" {
				t.Fatalf("SQLiteBackups = %v", topology.SQLiteBackups)
			}
			if !hasTopologyMarker(topology.Markers, "sqlite-backups:pre-dolt") {
				t.Fatalf("markers = %v, want retained-backup marker", topology.Markers)
			}
		})
	}
}

func TestRecognizeFreshTopologyAllowsNonStorageDoltAdjacentFiles(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ephemeral.sqlite3", "beads.db.migrated"} {
		if err := os.WriteFile(filepath.Join(beadsDir, name), []byte("not active SQLite storage"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Mode != "dolt-legacy" {
		t.Fatalf("mode = %q, markers = %v", topology.Mode, topology.Markers)
	}
}

func TestRecognizeFreshTopologySQLiteWithoutMetadataRecordsOnlyObservedDatabaseMarker(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Mode != "sqlite" {
		t.Fatalf("mode = %q, want sqlite", topology.Mode)
	}
	want := []string{"database:.beads/beads.db", "local-version:absent-or-invalid"}
	if strings.Join(topology.Markers, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("markers = %v, want %v", topology.Markers, want)
	}
}

func TestRecognizeFreshTopologySQLiteDoesNotDuplicateMetadataBackendMarker(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"sqlite"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Mode != "sqlite" {
		t.Fatalf("mode = %q, want sqlite", topology.Mode)
	}
	want := []string{"database:.beads/beads.db", "local-version:absent-or-invalid", "metadata-backend:sqlite"}
	if strings.Join(topology.Markers, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("markers = %v, want %v", topology.Markers, want)
	}
}

func TestRecognizeFreshTopologyHonorsServerModeMetadata(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Mode != "dolt-server" {
		t.Fatalf("mode = %q, markers = %v", topology.Mode, topology.Markers)
	}
}

func TestRecognizeFreshTopologyNormalizesLocalVersionMarkers(t *testing.T) {
	tests := []struct {
		name    string
		version string
		setup   func(t *testing.T, path string)
		want    string
	}{
		{name: "legacy server cohort", version: "0.55.4\n", want: "local-version:legacy-server"},
		{name: "legacy server final cohort", version: "v0.62.9\n", want: "local-version:legacy-server"},
		{name: "other bounded version", version: "1.1.2\n", want: "local-version:other-valid"},
		{name: "before bounded releases", version: "0.8.9\n", want: "local-version:absent-or-invalid"},
		{name: "after bounded releases", version: "1.1.3\n", want: "local-version:absent-or-invalid"},
		{name: "malformed", version: "not-a-version\n", want: "local-version:absent-or-invalid"},
		{name: "oversized", version: strings.Repeat("1", 65), want: "local-version:absent-or-invalid"},
		{name: "symlink", setup: func(t *testing.T, path string) {
			t.Helper()
			target := filepath.Join(filepath.Dir(path), "version-target")
			if err := os.WriteFile(target, []byte("0.55.4\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}, want: "local-version:absent-or-invalid"},
		{name: "absent", want: "local-version:absent-or-invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"server"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			versionPath := filepath.Join(beadsDir, ".local_version")
			if test.setup != nil {
				test.setup(t, versionPath)
			} else if test.version != "" {
				if err := os.WriteFile(versionPath, []byte(test.version), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			topology, err := recognizeFreshTopology(workspace)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains("\x00"+strings.Join(topology.Markers, "\x00")+"\x00", "\x00"+test.want+"\x00") {
				t.Fatalf("markers = %v, want %q", topology.Markers, test.want)
			}
		})
	}
}

func TestRecognizeFreshTopologyTreatsDualRootWithoutModeAsLegacy(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	for _, directory := range []string{"dolt", "embeddeddolt"} {
		if err := os.MkdirAll(filepath.Join(beadsDir, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Mode != "dolt-legacy" {
		t.Fatalf("mode = %q, markers = %v", topology.Mode, topology.Markers)
	}
}

func TestRecognizeFreshTopologyTreatsOldEmbeddedModeInDoltRootAsLegacy(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"embedded"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Mode != "dolt-legacy" {
		t.Fatalf("mode = %q, markers = %v", topology.Mode, topology.Markers)
	}
}

func TestRecognizeFreshTopologyDistinguishesCurrentEmbeddedRoot(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "embeddeddolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"dolt","dolt_mode":"embedded"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Mode != "dolt-embedded" {
		t.Fatalf("mode = %q, markers = %v", topology.Mode, topology.Markers)
	}
}

func TestRecognizeFreshTopologyRejectsInvalidReservedDoltRoots(t *testing.T) {
	tests := []struct {
		name      string
		directory string
		metadata  string
		create    func(t *testing.T, path string)
		wantMode  string
		wantErr   bool
	}{
		{
			name: "legacy directory", directory: "dolt",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantMode: "dolt-legacy",
		},
		{
			name: "embedded directory", directory: "embeddeddolt",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantMode: "dolt-embedded",
		},
		{
			name: "legacy regular file", directory: "dolt", metadata: `{"backend":"dolt"}`,
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "embedded regular file", directory: "embeddeddolt", metadata: `{"backend":"dolt","dolt_mode":"embedded"}`,
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "legacy symlink", directory: "dolt",
			create: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(filepath.Dir(path)), "dolt-target")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "embedded symlink", directory: "embeddeddolt",
			create: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(filepath.Dir(path)), "embeddeddolt-target")
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			test.create(t, filepath.Join(beadsDir, test.directory))
			if test.metadata != "" {
				if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(test.metadata), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			topology, err := recognizeFreshTopology(workspace)
			if test.wantErr {
				if err == nil {
					t.Fatalf("accepted invalid reserved Dolt root: %#v", topology)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if topology.Mode != test.wantMode {
				t.Fatalf("mode = %q, want %q", topology.Mode, test.wantMode)
			}
		})
	}
}

func TestRecognizeFreshTopologyIncludesShippedJSONLOnlyFallback(t *testing.T) {
	tests := []struct {
		name    string
		create  func(t *testing.T, path string)
		wantErr bool
	}{
		{
			name: "regular file",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			create: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(filepath.Dir(path)), "issues-target.jsonl")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
		{
			name: "directory",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			test.create(t, filepath.Join(beadsDir, "issues.jsonl"))

			topology, err := recognizeFreshTopology(workspace)
			if test.wantErr {
				if err == nil {
					t.Fatalf("accepted invalid issues.jsonl entry: %#v", topology)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if topology.Mode != "jsonl" {
				t.Fatalf("mode = %q, markers = %v", topology.Mode, topology.Markers)
			}
		})
	}
}

func TestRecognizeFreshTopologyRejectsInvalidJSONLAlongsideStorageRoots(t *testing.T) {
	tests := []struct {
		name          string
		createJSONL   func(t *testing.T, path string)
		createStorage func(t *testing.T, beadsDir string)
	}{
		{
			name: "symlink alongside Dolt directory",
			createJSONL: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(filepath.Dir(path)), "issues-target.jsonl")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
			createStorage: func(t *testing.T, beadsDir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory alongside SQLite database",
			createJSONL: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			createStorage: func(t *testing.T, beadsDir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			beadsDir := filepath.Join(workspace, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatal(err)
			}
			test.createStorage(t, beadsDir)
			test.createJSONL(t, filepath.Join(beadsDir, "issues.jsonl"))

			if topology, err := recognizeFreshTopology(workspace); err == nil {
				t.Fatalf("accepted invalid issues.jsonl alongside storage root: %#v", topology)
			}
		})
	}
}

func TestProbeFreshJSONLLayoutDistinguishesPublicRecordDialects(t *testing.T) {
	classicMode, classicLayout := probeJSONLLayoutForTest(t, `{"id":"bd-1","title":"Classic issue","description":"fixture","status":"open","priority":2,"issue_type":"task","created_at":"2025-10-12T00:00:00Z","updated_at":"2025-10-12T00:00:00Z"}`+"\n")
	typedMode, typedLayout := probeJSONLLayoutForTest(t, `{"_schema":"beads-jsonl/1","_dolt_branch":"main","_sort":"stable-v1"}`+"\n"+
		`{"_type":"issue","id":"bd-1","title":"Typed issue","description":"fixture","status":"open","priority":2,"issue_type":"task","created_at":"2025-10-12T00:00:00Z","updated_at":"2025-10-12T00:00:00Z"}`+"\n")

	classicID, err := familyID(classicMode, classicLayout)
	if err != nil {
		t.Fatal(err)
	}
	typedID, err := familyID(typedMode, typedLayout)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(classicLayout, typedLayout) {
		t.Fatalf("JSONL dialect layouts are identical: %s", classicLayout)
	}
	if classicID == typedID {
		t.Fatalf("JSONL dialect family IDs are identical: %s", classicID)
	}
}

func TestProbeFreshJSONLLayoutDistinguishesPublicRecordShapes(t *testing.T) {
	legacyMode, legacyLayout := probeJSONLLayoutForTest(t, `{"id":"bd-1","title":"Legacy issue","description":"fixture","status":"open","priority":2,"issue_type":"task","created_at":"2025-10-12T00:00:00Z","updated_at":"2025-10-12T00:00:00Z"}`+"\n")
	createdByMode, createdByLayout := probeJSONLLayoutForTest(t, `{"id":"bd-1","title":"Created-by issue","description":"fixture","status":"open","priority":2,"issue_type":"task","created_at":"2025-10-12T00:00:00Z","created_by":"census","updated_at":"2025-10-12T00:00:00Z"}`+"\n")

	legacyID, err := familyID(legacyMode, legacyLayout)
	if err != nil {
		t.Fatal(err)
	}
	createdByID, err := familyID(createdByMode, createdByLayout)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(legacyLayout, createdByLayout) {
		t.Fatalf("JSONL record field shapes are identical: %s", legacyLayout)
	}
	if legacyID == createdByID {
		t.Fatalf("JSONL record field-shape family IDs are identical: %s", legacyID)
	}
}

func TestProbeFreshJSONLLayoutIgnoresRecordFixtureValues(t *testing.T) {
	firstMode, firstLayout := probeJSONLLayoutForTest(t, `{"id":"bd-1","title":"First fixture","description":"first payload","status":"open","priority":1,"issue_type":"task","created_at":"2025-10-12T00:00:00Z","updated_at":"2025-10-12T00:00:00Z"}`+"\n")
	secondMode, secondLayout := probeJSONLLayoutForTest(t, `{"id":"bd-999","title":"Second fixture","description":"different payload","status":"closed","priority":4,"issue_type":"bug","created_at":"2026-07-28T12:34:56Z","updated_at":"2026-07-28T12:35:56Z"}`+"\n")

	firstID, err := familyID(firstMode, firstLayout)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := familyID(secondMode, secondLayout)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstLayout, secondLayout) {
		t.Fatalf("fixture values changed JSONL layout: first=%s second=%s", firstLayout, secondLayout)
	}
	if firstID != secondID {
		t.Fatalf("fixture values changed JSONL family ID: first=%s second=%s", firstID, secondID)
	}
}

func TestSeedJSONLDialectFixtureUsesPublicCreate(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(workspace, "bd")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > .census-create-args\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	binding, err := bindFreshBinary(binary, sourceBuildAcquisitionForTest(testCatalog().Versions[0]))
	if err != nil {
		t.Fatal(err)
	}
	ctx := withHistoricalBinaryBinding(context.Background(), binding)
	if err := seedJSONLDialectFixture(ctx, binary, workspace, nil); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(workspace, ".census-create-args"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(args), "create\ncensus-jsonl-dialect-fixture\n"; got != want {
		t.Fatalf("public fixture command = %q, want %q", got, want)
	}
}

func probeJSONLLayoutForTest(t *testing.T, records string) (string, json.RawMessage) {
	t.Helper()
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte(records), 0o600); err != nil {
		t.Fatal(err)
	}
	mode, layout, err := probeFreshLayout(context.Background(), workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "jsonl" {
		t.Fatalf("mode = %q, want jsonl", mode)
	}
	return mode, layout
}

func acquisitionForReleaseAsset() acquisition {
	return acquisition{Kind: "release-asset", ExecutableSHA256: strings.Repeat("b", sha256.Size*2)}
}

func sourceBuildAcquisitionForTest(entries ...catalogEntry) acquisition {
	entry := testCatalog().Versions[0]
	if len(entries) != 0 {
		entry = entries[0]
	}
	identity, err := sourceBuildIdentity(entry)
	if err != nil {
		panic(err)
	}
	return acquisition{Kind: "source-build", BuildIdentitySHA256: identity, GoToolchain: goToolchain}
}

func releaseAssetEntryForTest(t *testing.T, cache string, binary []byte) catalogEntry {
	t.Helper()
	const version = "v0.9.1"
	const assetName = "beads_linux_amd64.tar.gz"
	archive := filepath.Join(t.TempDir(), assetName)
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: "bd", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	asset := &catalogAsset{Name: assetName, Size: int64(len(raw)), Digest: "sha256:" + hexDigest(raw)}
	assetDir := filepath.Join(cache, "assets", version, strings.TrimPrefix(asset.Digest, "sha256:"))
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(archive, filepath.Join(assetDir, assetName)); err != nil {
		t.Fatal(err)
	}
	return catalogEntry{
		Version: version,
		GitHubRelease: &catalogRelease{
			SourceRelation:  "matches_proxy_origin",
			LinuxAMD64Asset: asset,
		},
	}
}

func rollingScenarioForMode(t *testing.T, mode string) string {
	t.Helper()
	for _, scenario := range rollingLineageScenarios {
		if scenario.Mode == mode {
			return scenario.Name
		}
	}
	t.Fatalf("no rolling scenario for mode %q", mode)
	return ""
}

func censusTestFamilyByMode(t *testing.T, result census, mode string) family {
	t.Helper()
	for _, candidate := range result.Families {
		if candidate.Mode == mode {
			return candidate
		}
	}
	t.Fatalf("no census family for mode %q", mode)
	return family{}
}

func testCatalog() catalog {
	return catalog{SchemaVersion: 1, Module: "github.com/steveyegge/beads", Versions: []catalogEntry{
		{Version: "v0.9.1", Sum: "h1:first", GoModSum: "h1:firstmod", Origin: catalogOrigin{Hash: strings.Repeat("a", 40), Ref: "refs/tags/v0.9.1"}, SourceZip: catalogSourceZip{SHA256: strings.Repeat("1", sha256.Size*2), Size: 101}},
		{Version: "v1.1.2", Sum: "h1:second", GoModSum: "h1:secondmod", Origin: catalogOrigin{Hash: strings.Repeat("b", 40), Ref: "refs/tags/v1.1.2"}, SourceZip: catalogSourceZip{SHA256: strings.Repeat("2", sha256.Size*2), Size: 202}},
	}}
}

func testCensus(t *testing.T, catalog catalog) census {
	t.Helper()
	familiesByMode := make(map[string]family)
	for _, scenario := range freshScenarioRules {
		if _, exists := familiesByMode[scenario.Mode]; exists {
			continue
		}
		layout := testFamilyLayout(t, scenario.Mode, "fresh")
		id, err := familyID(scenario.Mode, layout)
		if err != nil {
			t.Fatal(err)
		}
		familiesByMode[scenario.Mode] = family{ID: id, Mode: scenario.Mode, Layout: layout}
	}

	observations := make([]observation, 0)
	defaultJSONLVersions := expectedFreshDefaultJSONLVersions(catalog)
	if len(defaultJSONLVersions) != 0 {
		layout := testFamilyLayout(t, "jsonl", "fresh-default")
		id, err := familyID("jsonl", layout)
		if err != nil {
			t.Fatal(err)
		}
		familiesByMode["jsonl"] = family{ID: id, Mode: "jsonl", Layout: layout}
	}
	for _, entry := range catalog.Versions {
		scenarios, err := freshScenariosForVersion(entry.Version)
		if err != nil {
			t.Fatal(err)
		}
		for _, scenario := range scenarios {
			observations = append(observations, observation{
				Version: entry.Version, Scenario: scenario.Name, Provenance: provenanceFromEntry(entry),
				Acquisition: sourceBuildAcquisitionForTest(entry),
				FamilyID:    familiesByMode[scenario.Mode].ID,
			})
		}
		if defaultJSONLVersions[entry.Version] {
			observations = append(observations, observation{
				Version: entry.Version, Scenario: freshScenario, Provenance: provenanceFromEntry(entry),
				Acquisition: sourceBuildAcquisitionForTest(entry), FamilyID: familiesByMode["jsonl"].ID,
			})
		}
	}
	sortObservations(observations)
	families := make([]family, 0, len(familiesByMode))
	used := make(map[string]bool)
	for _, observed := range observations {
		if used[observed.FamilyID] {
			continue
		}
		used[observed.FamilyID] = true
		for _, candidate := range familiesByMode {
			if candidate.ID == observed.FamilyID {
				families = append(families, candidate)
				break
			}
		}
	}
	sort.Slice(families, func(i, j int) bool { return families[i].ID < families[j].ID })
	lineageScenarios, err := lineageScenarioMap()
	if err != nil {
		t.Fatal(err)
	}
	frontiers := make(map[string]map[string]bool)
	transitions := make([]lineageTransition, 0)
	for _, entry := range catalog.Versions {
		next := make(map[string]map[string]bool)
		for name, scenario := range lineageScenarios {
			if !scenario.compatible(entry.Version) {
				continue
			}
			next[name] = make(map[string]bool)
			for familyID := range frontiers[name] {
				next[name][familyID] = true
				runtimeMode, err := rollingTargetRuntimeMode(scenario, entry.Version)
				if err != nil {
					t.Fatal(err)
				}
				transitions = append(transitions, lineageTransition{
					FromFamilyID: familyID, TargetVersion: entry.Version, Scenario: name,
					Mode: scenario.Mode, RuntimeMode: runtimeMode, ToFamilyID: familyID, Acquisition: sourceBuildAcquisitionForTest(entry),
				})
			}
		}
		for _, observed := range observations {
			if observed.Version != entry.Version {
				continue
			}
			if name, ok := rollingScenarioForFreshScenario(observed.Scenario); ok && lineageScenarios[name].compatible(entry.Version) {
				next[name][observed.FamilyID] = true
			}
		}
		frontiers = next
	}
	sortLineageTransitions(transitions)
	return census{
		SchemaVersion: censusSchemaVersion, FingerprintSpecVersion: fingerprintSpecVersion,
		CatalogSHA256: catalogDigestForTest(t, catalog), Observations: observations, Families: families, Transitions: transitions,
	}
}

func testFamilyLayout(t *testing.T, mode, discriminator string) json.RawMessage {
	t.Helper()
	switch mode {
	case "sqlite":
		return canonicalTestJSON(t, struct {
			Schema   sqliteFingerprint `json:"schema"`
			Topology []string          `json:"topology"`
		}{
			Schema:   testSQLiteFingerprint(discriminator),
			Topology: []string{"database:.beads/beads.db"},
		})
	case "jsonl":
		return canonicalTestJSON(t, struct {
			Dialect  jsonlDialect `json:"dialect"`
			Format   string       `json:"format"`
			Topology []string     `json:"topology"`
		}{
			Dialect: jsonlDialect{Records: []jsonlRecordDialect{{
				Kind: "flat-record", Fields: []jsonlDialectField{{Name: "id", Type: "string"}},
			}}},
			Format:   "beads-jsonl",
			Topology: []string{"data:.beads/issues.jsonl"},
		})
	case "dolt-legacy", "dolt-server", "dolt-embedded":
		topology := []string{"directory:.beads/dolt"}
		if mode == "dolt-server" {
			topology = append(topology, "metadata-dolt-mode:server")
		}
		if mode == "dolt-embedded" {
			topology = []string{"directory:.beads/embeddeddolt"}
		}
		return canonicalTestJSON(t, struct {
			Schema   doltFingerprint `json:"schema"`
			Topology []string        `json:"topology"`
		}{
			Schema:   testDoltFingerprint(discriminator),
			Topology: topology,
		})
	default:
		t.Fatalf("unsupported test family mode %q", mode)
		return nil
	}
}

func testSQLiteFingerprint(discriminator string) sqliteFingerprint {
	name := "issues_" + discriminator
	statement := "CREATE TABLE " + name + " (id TEXT PRIMARY KEY)"
	pragmas := make([]sqlitePragma, len(fingerprintPragmas))
	for index, pragma := range fingerprintPragmas {
		pragmas[index] = sqlitePragma{Name: pragma, Value: sqliteValue{Type: "integer", Value: "0"}}
	}
	return sqliteFingerprint{
		Objects: []sqliteSchemaObject{{Type: "table", Name: name, Table: name, SQL: &statement}},
		Tables: []sqliteTable{{
			Name: name, Columns: []sqliteColumn{{CID: 0, Name: "id", DeclaredType: "TEXT", PrimaryKey: 1}},
			ForeignKeys: []sqliteForeignKey{}, Indexes: []sqliteIndex{},
		}},
		Pragmas:          pragmas,
		MigrationLedgers: []sqliteMigrationLedger{},
	}
}

func testDoltFingerprint(discriminator string) doltFingerprint {
	name := "issues_" + discriminator
	capabilities := make([]doltCapability, 0, len(doltCatalogQueries))
	for _, query := range doltCatalogQueries {
		capabilities = append(capabilities, doltCapability{Name: query.name})
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })
	return doltFingerprint{
		Objects:          []doltObject{{Name: name, Type: "BASE TABLE", Create: "CREATE TABLE " + name + " (id TEXT PRIMARY KEY)"}},
		Catalog:          []doltCatalogSnapshot{},
		MigrationLedgers: []doltMigrationLedger{},
		Capabilities:     capabilities,
	}
}

func canonicalTestJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func familyByIDMode(families []family, id string) string {
	for _, candidate := range families {
		if candidate.ID == id {
			return candidate.Mode
		}
	}
	return ""
}

func catalogDigestForTest(t *testing.T, catalog catalog) string {
	t.Helper()
	raw, err := encodeCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	return hexDigest(raw)
}

func hexDigest(raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func environmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}
