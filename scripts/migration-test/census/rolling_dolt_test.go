package main

import (
	"context"
	"fmt"
	"testing"
)

func TestGenerateRollingDoltLineageSeedsFreshFamiliesAndMergesFrontiers(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.47.2", "v0.49.1", "v0.49.2", "v0.55.4", "v0.63.0", "v0.63.1")
	legacy := lineageTestFamily(t, "dolt-legacy", "legacy")
	server := lineageTestFamily(t, "dolt-server", "server")
	serverOther := lineageTestFamily(t, "dolt-server", "server-other")
	embedded := lineageTestFamily(t, "dolt-embedded", "embedded")
	merged := lineageTestFamily(t, "dolt-server", "merged")
	fresh := lineageTestCensus(t, catalog, []observation{
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltLegacyScenario, legacy.ID),
		rollingDoltFreshObservation(catalog.Versions[1], freshDoltServerScenario, server.ID),
		rollingDoltFreshObservation(catalog.Versions[1], freshDoltLegacyScenario, legacy.ID),
		rollingDoltFreshObservation(catalog.Versions[2], freshDoltServerScenario, serverOther.ID),
		rollingDoltFreshObservation(catalog.Versions[2], freshDoltLegacyScenario, legacy.ID),
		rollingDoltFreshObservation(catalog.Versions[3], freshDoltLegacyScenario, legacy.ID),
		rollingDoltFreshObservation(catalog.Versions[3], freshDoltServerScenario, merged.ID),
		rollingDoltFreshObservation(catalog.Versions[4], freshDoltServerScenario, merged.ID),
		rollingDoltFreshObservation(catalog.Versions[4], freshDoltEmbeddedScenario, embedded.ID),
		rollingDoltFreshObservation(catalog.Versions[5], freshDoltServerScenario, merged.ID),
		rollingDoltFreshObservation(catalog.Versions[5], freshDoltEmbeddedScenario, embedded.ID),
	}, legacy, server, serverOther, embedded, merged)

	runtime := newFakeRollingDoltRuntime(t, map[string]map[string]family{
		"v0.49.1": {legacy.ID: legacy},
		"v0.49.2": {legacy.ID: legacy, server.ID: server},
		"v0.55.4": {legacy.ID: legacy, server.ID: merged, serverOther.ID: merged},
		"v0.63.0": {legacy.ID: legacy, merged.ID: merged},
		"v0.63.1": {legacy.ID: legacy, merged.ID: merged, embedded.ID: embedded},
	})
	got, rollingOnly, err := generateRollingDoltLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := runtime.acquired, []string{
		"v0.47.2/dolt-legacy", "v0.49.1/dolt-legacy", "v0.49.1/dolt-server",
		"v0.49.2/dolt-legacy", "v0.49.2/dolt-server", "v0.55.4/dolt-legacy",
		"v0.55.4/dolt-server", "v0.63.0/dolt-embedded", "v0.63.0/dolt-server", "v0.63.0/dolt-embedded",
		"v0.63.1/dolt-embedded", "v0.63.1/dolt-server", "v0.63.1/dolt-embedded",
	}; fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
		t.Fatalf("acquired = %q, want each needed target/mode: %q", got, want)
	}
	if got := runtime.retained[rollingServerScenario]; got != 2 {
		t.Fatalf("server retained workspaces = %d, want two distinct fresh families", got)
	}
	if got := runtime.advances["v0.63.1"]; got != 3 {
		t.Fatalf("v0.63.1 advances = %d, want legacy, merged server, and embedded frontiers", got)
	}
	if got := runtime.advances["v0.49.2"]; got != 2 {
		t.Fatalf("v0.49.2 advances = %d, want legacy and server frontiers", got)
	}

	want := []lineageTransition{
		{FromFamilyID: legacy.ID, TargetVersion: "v0.49.1", Scenario: rollingLegacyScenario, Mode: "dolt-legacy", ToFamilyID: legacy.ID},
		{FromFamilyID: legacy.ID, TargetVersion: "v0.49.2", Scenario: rollingLegacyScenario, Mode: "dolt-legacy", ToFamilyID: legacy.ID},
		{FromFamilyID: server.ID, TargetVersion: "v0.49.2", Scenario: rollingServerScenario, Mode: "dolt-server", ToFamilyID: server.ID},
		{FromFamilyID: legacy.ID, TargetVersion: "v0.55.4", Scenario: rollingLegacyScenario, Mode: "dolt-legacy", ToFamilyID: legacy.ID},
		{FromFamilyID: server.ID, TargetVersion: "v0.55.4", Scenario: rollingServerScenario, Mode: "dolt-server", ToFamilyID: merged.ID},
		{FromFamilyID: serverOther.ID, TargetVersion: "v0.55.4", Scenario: rollingServerScenario, Mode: "dolt-server", ToFamilyID: merged.ID},
		{FromFamilyID: legacy.ID, TargetVersion: "v0.63.0", Scenario: rollingLegacyScenario, Mode: "dolt-legacy", ToFamilyID: legacy.ID},
		{FromFamilyID: merged.ID, TargetVersion: "v0.63.0", Scenario: rollingServerScenario, Mode: "dolt-server", ToFamilyID: merged.ID},
		{FromFamilyID: legacy.ID, TargetVersion: "v0.63.1", Scenario: rollingLegacyScenario, Mode: "dolt-legacy", ToFamilyID: legacy.ID},
		{FromFamilyID: merged.ID, TargetVersion: "v0.63.1", Scenario: rollingServerScenario, Mode: "dolt-server", ToFamilyID: merged.ID},
		{FromFamilyID: embedded.ID, TargetVersion: "v0.63.1", Scenario: rollingEmbeddedScenario, Mode: "dolt-embedded", ToFamilyID: embedded.ID},
	}
	for index := range want {
		scenarios, err := lineageScenarioMap()
		if err != nil {
			t.Fatal(err)
		}
		runtimeMode, err := rollingTargetRuntimeMode(scenarios[want[index].Scenario], want[index].TargetVersion)
		if err != nil {
			t.Fatal(err)
		}
		want[index].RuntimeMode = runtimeMode
		for _, entry := range catalog.Versions {
			if entry.Version == want[index].TargetVersion {
				want[index].Acquisition = sourceBuildAcquisitionForTest(entry)
				break
			}
		}
	}
	sortLineageTransitions(want)
	if fmt.Sprintf("%#v", got.Transitions) != fmt.Sprintf("%#v", want) {
		t.Fatalf("transitions = %#v, want %#v", got.Transitions, want)
	}
	if len(rollingOnly) != 0 {
		t.Fatalf("rolling-only families = %#v, want none", rollingOnly)
	}
}

func TestGenerateRollingDoltLineageUsesEachModeFreshAcquisition(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.63.0")
	jsonl := lineageTestFamily(t, "jsonl", "asset-default")
	server := lineageTestFamily(t, "dolt-server", "source-server")
	embedded := lineageTestFamily(t, "dolt-embedded", "source-embedded")
	fresh := lineageTestCensus(t, catalog, []observation{
		{Version: catalog.Versions[0].Version, Scenario: freshScenario, Provenance: provenanceFromEntry(catalog.Versions[0]), Acquisition: acquisitionForReleaseAsset(), FamilyID: jsonl.ID},
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltServerScenario, server.ID),
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltEmbeddedScenario, embedded.ID),
	}, jsonl, server, embedded)
	runtime := newFakeRollingDoltRuntime(t, nil)

	if _, _, err := generateRollingDoltLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime()); err != nil {
		t.Fatalf("generate rolling Dolt lineage: %v", err)
	}
	if got, want := runtime.acquisitions, []acquisition{sourceBuildAcquisitionForTest(catalog.Versions[0]), sourceBuildAcquisitionForTest(catalog.Versions[0])}; fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("acquisitions = %#v, want each Dolt mode's fresh acquisition %#v", got, want)
	}
}

func TestGenerateRollingDoltLineageAcquiresLegacyTargetWithoutFreshLegacyObservation(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.49.1", "v0.49.5", "v0.49.6")
	legacy := lineageTestFamily(t, "dolt-legacy", "legacy")
	server := lineageTestFamily(t, "dolt-server", "server")
	fresh := lineageTestCensus(t, catalog, []observation{
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltLegacyScenario, legacy.ID),
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltServerScenario, server.ID),
		rollingDoltFreshObservation(catalog.Versions[1], freshDoltServerScenario, server.ID),
		rollingDoltFreshObservation(catalog.Versions[2], freshDoltLegacyScenario, legacy.ID),
		rollingDoltFreshObservation(catalog.Versions[2], freshDoltServerScenario, server.ID),
	}, legacy, server)
	runtime := newFakeRollingDoltRuntime(t, map[string]map[string]family{
		"v0.49.5": {legacy.ID: legacy, server.ID: server},
		"v0.49.6": {legacy.ID: legacy, server.ID: server},
	})

	got, rollingOnly, err := generateRollingDoltLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	foundServerProfile := false
	for index, target := range runtime.acquired {
		if target == "v0.49.5/dolt-server" {
			foundServerProfile = true
			if runtime.acquisitions[index] != sourceBuildAcquisitionForTest(catalog.Versions[1]) {
				t.Fatalf("v0.49.5 legacy acquisition = %#v, want authenticated source target", runtime.acquisitions[index])
			}
		}
	}
	if !foundServerProfile {
		t.Fatalf("acquired targets = %q, want v0.49.5/dolt-server", runtime.acquired)
	}
	if err := mergeRollingLineages(&fresh, []lineageSet{got}, [][]family{rollingOnly}); err != nil {
		t.Fatal(err)
	}
	sortObservations(fresh.Observations)
	if err := validateRollingLineageCoverage(fresh, catalog, censusFamilyMap(fresh)); err != nil {
		t.Fatalf("rolling frontier validation rejected the legacy target: %v", err)
	}
	if err := validateRollingReferences(fresh, catalog, censusFamilyMap(fresh)); err != nil {
		t.Fatalf("rolling acquisition validation rejected the legacy target: %v", err)
	}
}

func TestGenerateRollingDoltLineageRecordsUnchangedRefusalAndRetainsFrontier(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.63.0", "v0.63.1", "v0.63.2")
	before := lineageTestFamily(t, "dolt-embedded", "before")
	after := lineageTestFamily(t, "dolt-embedded", "after")
	fresh := lineageTestCensus(t, catalog, []observation{
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltEmbeddedScenario, before.ID),
		rollingDoltFreshObservation(catalog.Versions[1], freshDoltEmbeddedScenario, before.ID),
		rollingDoltFreshObservation(catalog.Versions[2], freshDoltEmbeddedScenario, before.ID),
	}, before)
	runtime := newFakeRollingDoltRuntime(t, map[string]map[string]family{
		"v0.63.1": {before.ID: before},
		"v0.63.2": {before.ID: after},
	})
	runtime.failures["v0.63.1"] = true

	got, rollingOnly, err := generateRollingDoltLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outcomes) != 1 || got.Outcomes[0].Outcome != lineageOutcomeUnchangedRefusal ||
		got.Outcomes[0].FromFamilyID != before.ID || got.Outcomes[0].TargetVersion != "v0.63.1" || got.Outcomes[0].ToFamilyID != "" {
		t.Fatalf("outcomes = %#v, want deterministic unchanged refusal", got.Outcomes)
	}
	if len(got.Transitions) != 1 || got.Transitions[0].FromFamilyID != before.ID ||
		got.Transitions[0].TargetVersion != "v0.63.2" || got.Transitions[0].ToFamilyID != after.ID {
		t.Fatalf("transitions = %#v, want later advance from the retained source family", got.Transitions)
	}
	if fmt.Sprintf("%#v", rollingOnly) != fmt.Sprintf("%#v", []family{after}) {
		t.Fatalf("rolling-only families = %#v, want %#v", rollingOnly, []family{after})
	}
}

func TestGenerateRollingDoltLineageRecordsMutatingFailureAndRetainsPartialFrontier(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.63.0", "v0.63.1", "v0.63.2")
	before := lineageTestFamily(t, "dolt-embedded", "before")
	partial := lineageTestFamily(t, "dolt-embedded", "partial")
	fresh := lineageTestCensus(t, catalog, []observation{
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltEmbeddedScenario, before.ID),
		rollingDoltFreshObservation(catalog.Versions[1], freshDoltEmbeddedScenario, before.ID),
		rollingDoltFreshObservation(catalog.Versions[2], freshDoltEmbeddedScenario, before.ID),
	}, before)
	runtime := newFakeRollingDoltRuntime(t, map[string]map[string]family{
		"v0.63.1": {before.ID: partial},
		"v0.63.2": {before.ID: before, partial.ID: partial},
	})
	runtime.mutatingFailures["v0.63.1"] = true

	got, rollingOnly, err := generateRollingDoltLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outcomes) != 1 || got.Outcomes[0].Outcome != lineageOutcomeMutatingFailure ||
		got.Outcomes[0].FromFamilyID != before.ID || got.Outcomes[0].ToFamilyID != partial.ID ||
		got.Outcomes[0].TargetVersion != "v0.63.1" {
		t.Fatalf("outcomes = %#v, want deterministic partial-migration record", got.Outcomes)
	}
	foundPartial := false
	for _, transition := range got.Transitions {
		if transition.FromFamilyID == partial.ID && transition.TargetVersion == "v0.63.2" {
			foundPartial = true
		}
	}
	if !foundPartial {
		t.Fatalf("transitions = %#v, want later advance from the partial family", got.Transitions)
	}
	if fmt.Sprintf("%#v", rollingOnly) != fmt.Sprintf("%#v", []family{partial}) {
		t.Fatalf("rolling-only families = %#v, want %#v", rollingOnly, []family{partial})
	}
}

func TestGenerateRollingDoltLineagePreservesDistinctPostTargetFrontiers(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.63.0", "v0.63.1", "v0.63.2", "v0.63.3", "v0.63.4")
	first := lineageTestFamily(t, "dolt-embedded", "first")
	second := lineageTestFamily(t, "dolt-embedded", "second")
	third := lineageTestFamily(t, "dolt-embedded", "third")
	server := lineageTestFamily(t, "dolt-server", "server")
	fresh := lineageTestCensus(t, catalog, []observation{
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltEmbeddedScenario, first.ID),
		rollingDoltFreshObservation(catalog.Versions[0], freshDoltServerScenario, server.ID),
		rollingDoltFreshObservation(catalog.Versions[1], freshDoltEmbeddedScenario, second.ID),
		rollingDoltFreshObservation(catalog.Versions[1], freshDoltServerScenario, server.ID),
		rollingDoltFreshObservation(catalog.Versions[2], freshDoltEmbeddedScenario, first.ID),
		rollingDoltFreshObservation(catalog.Versions[2], freshDoltServerScenario, server.ID),
		rollingDoltFreshObservation(catalog.Versions[3], freshDoltEmbeddedScenario, second.ID),
		rollingDoltFreshObservation(catalog.Versions[3], freshDoltServerScenario, server.ID),
		rollingDoltFreshObservation(catalog.Versions[4], freshDoltEmbeddedScenario, second.ID),
		rollingDoltFreshObservation(catalog.Versions[4], freshDoltServerScenario, server.ID),
	}, first, second, server)
	runtime := newFakeRollingDoltRuntime(t, map[string]map[string]family{
		"v0.63.1": {first.ID: second, server.ID: server},
		"v0.63.2": {second.ID: second, server.ID: server},
		"v0.63.3": {first.ID: second, second.ID: third, server.ID: server},
		"v0.63.4": {second.ID: second, third.ID: third, server.ID: server},
	})
	runtime.mutatingFailuresByFamily["v0.63.3"] = map[string]bool{first.ID: true}

	got, rollingOnly, err := generateRollingDoltLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.advances["v0.63.4"] != 3 {
		t.Fatalf("v0.63.4 advances = %d, want distinct second, third, and server frontiers", runtime.advances["v0.63.4"])
	}
	if got, want := rollingOnly, []family{third}; fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("rolling-only families = %#v, want %#v", got, want)
	}
	if err := mergeRollingLineages(&fresh, []lineageSet{got}, [][]family{rollingOnly}); err != nil {
		t.Fatal(err)
	}
	sortObservations(fresh.Observations)
	if err := validateCensus(fresh, catalog); err != nil {
		t.Fatalf("full census validation lost a post-target frontier: %v", err)
	}
}

func rollingDoltFreshObservation(entry catalogEntry, scenario, familyID string) observation {
	return observation{Version: entry.Version, Scenario: scenario, Provenance: provenanceFromEntry(entry), Acquisition: sourceBuildAcquisitionForTest(entry), FamilyID: familyID}
}

type fakeRollingDoltRuntime struct {
	transitions              map[string]map[string]family
	states                   map[*retainedDoltFrontier]family
	acquired                 []string
	acquisitions             []acquisition
	retained                 map[string]int
	advances                 map[string]int
	failures                 map[string]bool
	mutatingFailures         map[string]bool
	mutatingFailuresByFamily map[string]map[string]bool
}

func newFakeRollingDoltRuntime(t *testing.T, transitions map[string]map[string]family) *fakeRollingDoltRuntime {
	return &fakeRollingDoltRuntime{transitions: transitions, states: make(map[*retainedDoltFrontier]family), retained: make(map[string]int), advances: make(map[string]int), failures: make(map[string]bool), mutatingFailures: make(map[string]bool), mutatingFailuresByFamily: make(map[string]map[string]bool)}
}

func (fake *fakeRollingDoltRuntime) runtime() rollingDoltRuntime {
	return rollingDoltRuntime{
		acquire: func(_ context.Context, entry catalogEntry, _ string, mode string, acquired acquisition) (string, error) {
			fake.acquired = append(fake.acquired, entry.Version+"/"+mode)
			fake.acquisitions = append(fake.acquisitions, acquired)
			return entry.Version, nil
		},
		newExecutor: func() rollingDoltFrontierExecutor { return fake },
	}
}

func (fake *fakeRollingDoltRuntime) Retain(_ context.Context, source rollingDoltSource) (*retainedDoltFrontier, error) {
	fake.retained[source.Scenario.Name]++
	frontier := &retainedDoltFrontier{Scenario: source.Scenario, Version: source.Version, FamilyID: source.FamilyID}
	fake.states[frontier] = family{ID: source.FamilyID, Mode: source.Scenario.Mode}
	return frontier, nil
}

func (fake *fakeRollingDoltRuntime) Advance(_ context.Context, frontier *retainedDoltFrontier, target rollingDoltTarget) (lineageTransition, family, error) {
	fake.advances[target.Version]++
	from := fake.states[frontier]
	observed, ok := fake.transitions[target.Version][from.ID]
	if !ok {
		return lineageTransition{}, family{}, fmt.Errorf("no fake transition for %s from %s", target.Version, from.ID)
	}
	if fake.failures[target.Version] {
		return lineageTransition{}, observed, historicalProcessExit(fmt.Errorf("historical target failed"))
	}
	frontier.Version, frontier.FamilyID = target.Version, observed.ID
	fake.states[frontier] = observed
	if fake.mutatingFailures[target.Version] || fake.mutatingFailuresByFamily[target.Version][from.ID] {
		return lineageTransition{FromFamilyID: from.ID, TargetVersion: target.Version, Scenario: frontier.Scenario.Name, Mode: frontier.Scenario.Mode, ToFamilyID: observed.ID}, observed, historicalProcessExit(fmt.Errorf("historical target failed"))
	}
	return lineageTransition{FromFamilyID: from.ID, TargetVersion: target.Version, Scenario: frontier.Scenario.Name, Mode: frontier.Scenario.Mode, ToFamilyID: observed.ID}, observed, nil
}

func (*fakeRollingDoltRuntime) Close() error { return nil }

func (*fakeRollingDoltRuntime) BeginBatch() {}

func (*fakeRollingDoltRuntime) EndBatch() map[string]*retainedDoltFrontier { return nil }

func TestFreshDoltFamiliesByVersionUsesFreshScenarioInsteadOfFamilyMode(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.63.0")
	legacy := lineageTestFamily(t, "dolt-legacy", "legacy")
	fresh := lineageTestCensus(t, catalog, []observation{rollingDoltFreshObservation(catalog.Versions[0], freshDoltEmbeddedScenario, legacy.ID)}, legacy)
	byVersion, _, _, err := freshDoltFamiliesByVersion(fresh)
	if err != nil {
		t.Fatalf("fresh scenario should seed its rolling frontier even when topology differs: %v", err)
	}
	if got := byVersion[catalog.Versions[0].Version][rollingEmbeddedScenario]; len(got) != 1 || got[0].ID != legacy.ID {
		t.Fatalf("embedded rolling frontier = %#v, want legacy topology family from fresh embedded scenario", got)
	}
}

var _ rollingDoltFrontierExecutor = (*fakeRollingDoltRuntime)(nil)
