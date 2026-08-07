package main

import (
	"crypto/sha256"
	"sort"
	"strings"
	"testing"
)

func TestRollingLineageScenarioIntervals(t *testing.T) {
	tests := []struct {
		name     string
		before   string
		first    string
		last     string
		after    string
		scenario string
	}{
		{name: "sqlite", first: "v0.9.1", last: "v1.1.2", after: "v1.1.3", scenario: rollingSQLiteScenario},
		{name: "legacy", before: "v0.47.1", first: "v0.47.2", last: "v1.1.2", after: "v1.1.3", scenario: rollingLegacyScenario},
		{name: "server", before: "v0.49.0", first: "v0.49.1", last: "v1.1.2", after: "v1.1.3", scenario: rollingServerScenario},
		{name: "embedded", before: "v0.62.0", first: "v0.63.0", last: "v1.1.2", after: "v1.1.3", scenario: rollingEmbeddedScenario},
	}
	scenarios, err := lineageScenarioMap()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scenario := scenarios[test.scenario]
			if test.before != "" && scenario.compatible(test.before) {
				t.Fatalf("%s unexpectedly accepts %s", test.scenario, test.before)
			}
			for _, version := range []string{test.first, test.last} {
				if !scenario.compatible(version) {
					t.Fatalf("%s rejects boundary %s", test.scenario, version)
				}
			}
			if scenario.compatible(test.after) {
				t.Fatalf("%s unexpectedly accepts %s", test.scenario, test.after)
			}
		})
	}
}

func TestFreshDefaultJSONLDoesNotExpandRollingLineages(t *testing.T) {
	if _, ok := rollingScenarioForFreshScenario(freshScenario); ok {
		t.Fatal("fresh-default JSONL unexpectedly expands a rolling lineage")
	}
	for _, scenario := range rollingLineageScenarios {
		if scenario.Mode == "jsonl" {
			t.Fatal("JSONL unexpectedly has a rolling lineage scenario")
		}
	}
}

func TestRollingDoltLegacyTargetRuntimeProfiles(t *testing.T) {
	scenarios, err := lineageScenarioMap()
	if err != nil {
		t.Fatal(err)
	}
	legacy := scenarios[rollingLegacyScenario]
	for version, want := range map[string]string{
		"v0.55.4": rollingLegacyScenario,
		"v0.56.0": rollingServerScenario,
		"v0.62.0": rollingServerScenario,
		"v0.63.0": rollingEmbeddedScenario,
	} {
		got, err := rollingDoltTargetRuntime(legacy, version)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != want {
			t.Fatalf("%s runtime = %s, want %s", version, got.Name, want)
		}
	}
}

func TestValidateRollingLineageCoverageRequiresCrossEraSQLiteAttempt(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.50.3", "v0.51.0")
	sqlite := lineageTestFamily(t, "sqlite", "sqlite")
	result := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], sqlite.ID),
	}, sqlite)
	if err := validateRollingLineageCoverage(result, catalog, censusFamilyMap(result)); err == nil {
		t.Fatal("accepted a census without the cross-era SQLite target attempt")
	}
}

func TestValidateOutcomeRecordsRejectsSelfMutatingFailure(t *testing.T) {
	sqlite := lineageTestFamily(t, "sqlite", "sqlite")
	scenarios, err := lineageScenarioMap()
	if err != nil {
		t.Fatal(err)
	}
	outcomes := []lineageOutcome{{
		FromFamilyID: sqlite.ID, TargetVersion: "v0.9.1", Scenario: rollingSQLiteScenario,
		Mode: "sqlite", RuntimeMode: "sqlite", Outcome: lineageOutcomeMutatingFailure, ToFamilyID: sqlite.ID,
	}}
	if err := validateOutcomeRecords(outcomes, map[string]family{sqlite.ID: sqlite}, map[string]int{"v0.9.1": 0}, scenarios); err == nil {
		t.Fatal("accepted a mutating failure whose semantic family is unchanged")
	}
}

func TestValidateTransitionRecordsAllowsCrossModeDoltFamilyAndBindsTargetRuntime(t *testing.T) {
	legacy := lineageTestFamily(t, "dolt-legacy", "legacy")
	embedded := lineageTestFamily(t, "dolt-embedded", "embedded")
	scenarios, err := lineageScenarioMap()
	if err != nil {
		t.Fatal(err)
	}
	transition := lineageTransition{
		FromFamilyID: legacy.ID, TargetVersion: "v0.63.0", Scenario: rollingLegacyScenario,
		Mode: "dolt-legacy", RuntimeMode: "dolt-embedded", ToFamilyID: embedded.ID,
	}
	families := map[string]family{legacy.ID: legacy, embedded.ID: embedded}
	versions := map[string]int{"v0.63.0": 0}
	if err := validateTransitionRecords([]lineageTransition{transition}, families, versions, scenarios); err != nil {
		t.Fatalf("rejected valid cross-mode transition: %v", err)
	}

	wrongRuntime := transition
	wrongRuntime.RuntimeMode = "dolt-server"
	if err := validateTransitionRecords([]lineageTransition{wrongRuntime}, families, versions, scenarios); err == nil {
		t.Fatal("accepted transition with runtime mode that does not match its target release")
	}
	if err := validateTransitionRecords([]lineageTransition{transition, transition}, families, versions, scenarios); err == nil {
		t.Fatal("accepted duplicate cross-mode transition")
	}
}

func TestValidateTransitionRecordsRejectsFamiliesOutsideRollingLineage(t *testing.T) {
	sqlite := lineageTestFamily(t, "sqlite", "sqlite")
	legacy := lineageTestFamily(t, "dolt-legacy", "legacy")
	jsonl := lineageTestFamily(t, "jsonl", "jsonl")
	scenarios, err := lineageScenarioMap()
	if err != nil {
		t.Fatal(err)
	}
	families := map[string]family{sqlite.ID: sqlite, legacy.ID: legacy, jsonl.ID: jsonl}
	tests := []struct {
		name       string
		transition lineageTransition
	}{
		{
			name: "sqlite source is dolt",
			transition: lineageTransition{
				FromFamilyID: legacy.ID, TargetVersion: "v0.9.1", Scenario: rollingSQLiteScenario,
				Mode: "sqlite", RuntimeMode: "sqlite", ToFamilyID: sqlite.ID,
			},
		},
		{
			name: "sqlite target is jsonl",
			transition: lineageTransition{
				FromFamilyID: sqlite.ID, TargetVersion: "v0.9.1", Scenario: rollingSQLiteScenario,
				Mode: "sqlite", RuntimeMode: "sqlite", ToFamilyID: jsonl.ID,
			},
		},
		{
			name: "dolt source is sqlite",
			transition: lineageTransition{
				FromFamilyID: sqlite.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario,
				Mode: "dolt-legacy", RuntimeMode: "dolt-legacy", ToFamilyID: legacy.ID,
			},
		},
		{
			name: "dolt source is jsonl",
			transition: lineageTransition{
				FromFamilyID: jsonl.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario,
				Mode: "dolt-legacy", RuntimeMode: "dolt-legacy", ToFamilyID: legacy.ID,
			},
		},
		{
			name: "dolt target is sqlite",
			transition: lineageTransition{
				FromFamilyID: legacy.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario,
				Mode: "dolt-legacy", RuntimeMode: "dolt-legacy", ToFamilyID: sqlite.ID,
			},
		},
		{
			name: "dolt target is jsonl",
			transition: lineageTransition{
				FromFamilyID: legacy.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario,
				Mode: "dolt-legacy", RuntimeMode: "dolt-legacy", ToFamilyID: jsonl.ID,
			},
		},
	}
	versions := map[string]int{"v0.9.1": 0, "v0.49.1": 1}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateTransitionRecords([]lineageTransition{test.transition}, families, versions, scenarios); err == nil {
				t.Fatal("accepted a family outside the rolling lineage mode")
			}
		})
	}
}

func TestValidateOutcomeRecordsRejectsFamiliesOutsideRollingLineage(t *testing.T) {
	sqlite := lineageTestFamily(t, "sqlite", "sqlite")
	legacy := lineageTestFamily(t, "dolt-legacy", "legacy")
	jsonl := lineageTestFamily(t, "jsonl", "jsonl")
	scenarios, err := lineageScenarioMap()
	if err != nil {
		t.Fatal(err)
	}
	families := map[string]family{sqlite.ID: sqlite, legacy.ID: legacy, jsonl.ID: jsonl}
	tests := []struct {
		name    string
		outcome lineageOutcome
	}{
		{
			name: "sqlite source is dolt",
			outcome: lineageOutcome{
				FromFamilyID: legacy.ID, TargetVersion: "v0.9.1", Scenario: rollingSQLiteScenario,
				Mode: "sqlite", RuntimeMode: "sqlite", Outcome: lineageOutcomeUnchangedRefusal,
			},
		},
		{
			name: "sqlite target is jsonl",
			outcome: lineageOutcome{
				FromFamilyID: sqlite.ID, TargetVersion: "v0.9.1", Scenario: rollingSQLiteScenario,
				Mode: "sqlite", RuntimeMode: "sqlite", Outcome: lineageOutcomeMutatingFailure, ToFamilyID: jsonl.ID,
			},
		},
		{
			name: "dolt source is sqlite",
			outcome: lineageOutcome{
				FromFamilyID: sqlite.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario,
				Mode: "dolt-legacy", RuntimeMode: "dolt-legacy", Outcome: lineageOutcomeUnchangedRefusal,
			},
		},
		{
			name: "dolt source is jsonl",
			outcome: lineageOutcome{
				FromFamilyID: jsonl.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario,
				Mode: "dolt-legacy", RuntimeMode: "dolt-legacy", Outcome: lineageOutcomeUnchangedRefusal,
			},
		},
		{
			name: "dolt target is sqlite",
			outcome: lineageOutcome{
				FromFamilyID: legacy.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario,
				Mode: "dolt-legacy", RuntimeMode: "dolt-legacy", Outcome: lineageOutcomeMutatingFailure, ToFamilyID: sqlite.ID,
			},
		},
		{
			name: "dolt target is jsonl",
			outcome: lineageOutcome{
				FromFamilyID: legacy.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario,
				Mode: "dolt-legacy", RuntimeMode: "dolt-legacy", Outcome: lineageOutcomeMutatingFailure, ToFamilyID: jsonl.ID,
			},
		},
	}
	versions := map[string]int{"v0.9.1": 0, "v0.49.1": 1}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateOutcomeRecords([]lineageOutcome{test.outcome}, families, versions, scenarios); err == nil {
				t.Fatal("accepted a family outside the rolling lineage mode")
			}
		})
	}
}

func lineageTestCatalog(t *testing.T, versions ...string) catalog {
	t.Helper()
	entries := make([]catalogEntry, 0, len(versions))
	for index, version := range versions {
		entries = append(entries, catalogEntry{
			Version: version, Sum: "h1:" + version, GoModSum: "h1:mod" + version,
			Origin:    catalogOrigin{Hash: strings.Repeat(string(rune('a'+index)), 40), Ref: "refs/tags/" + version},
			SourceZip: catalogSourceZip{SHA256: strings.Repeat(string(rune('1'+index)), sha256.Size*2), Size: int64(index + 1)},
		})
	}
	return catalog{SchemaVersion: 1, Module: modulePath, Versions: entries}
}

func lineageTestFamily(t *testing.T, mode, name string) family {
	t.Helper()
	layout := testFamilyLayout(t, mode, name)
	id, err := familyID(mode, layout)
	if err != nil {
		t.Fatal(err)
	}
	return family{ID: id, Mode: mode, Layout: layout}
}

func lineageTestCensus(t *testing.T, catalog catalog, observations []observation, families ...family) census {
	t.Helper()
	sort.Slice(families, func(i, j int) bool { return families[i].ID < families[j].ID })
	return census{
		SchemaVersion: censusSchemaVersion, FingerprintSpecVersion: fingerprintSpecVersion,
		CatalogSHA256: catalogDigestForTest(t, catalog),
		Observations:  observations, Families: families,
	}
}
