package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateRollingSQLiteLineageUsesOneWorkspacePerFamilyFrontier(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1", "v0.9.2", "v0.9.3", "v0.9.4")
	first := lineageTestFamily(t, "sqlite", "first")
	second := lineageTestFamily(t, "sqlite", "second")
	merged := lineageTestFamily(t, "sqlite", "merged")
	result := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[1], second.ID),
		rollingSQLiteFreshObservation(catalog.Versions[2], merged.ID),
		rollingSQLiteFreshObservation(catalog.Versions[3], merged.ID),
	}, first, second, merged)

	runtime := newFakeRollingSQLiteRuntime(t, map[string]map[string]family{
		"v0.9.2": {first.ID: first},
		"v0.9.3": {first.ID: merged, second.ID: merged},
		"v0.9.4": {merged.ID: merged},
	})
	got, rollingOnly, err := generateRollingSQLiteLineageWithRuntime(context.Background(), catalog, result, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}

	if runtime.initialized != 2 {
		t.Fatalf("initialized %d retained workspaces, want one per newly introduced family (2)", runtime.initialized)
	}
	if got := runtime.reads["v0.9.4"]; got != 1 {
		t.Fatalf("v0.9.4 read workspaces = %d, want 1 after the family frontier merged", got)
	}
	if got := runtime.reads["v0.9.3"]; got != 2 {
		t.Fatalf("v0.9.3 read workspaces = %d, want 2 distinct prior families", got)
	}

	want := []lineageTransition{
		{FromFamilyID: first.ID, TargetVersion: "v0.9.2", Scenario: rollingSQLiteScenario, Mode: "sqlite", RuntimeMode: "sqlite", ToFamilyID: first.ID, Acquisition: sourceBuildAcquisitionForTest(catalog.Versions[1])},
		{FromFamilyID: first.ID, TargetVersion: "v0.9.3", Scenario: rollingSQLiteScenario, Mode: "sqlite", RuntimeMode: "sqlite", ToFamilyID: merged.ID, Acquisition: sourceBuildAcquisitionForTest(catalog.Versions[2])},
		{FromFamilyID: second.ID, TargetVersion: "v0.9.3", Scenario: rollingSQLiteScenario, Mode: "sqlite", RuntimeMode: "sqlite", ToFamilyID: merged.ID, Acquisition: sourceBuildAcquisitionForTest(catalog.Versions[2])},
		{FromFamilyID: merged.ID, TargetVersion: "v0.9.4", Scenario: rollingSQLiteScenario, Mode: "sqlite", RuntimeMode: "sqlite", ToFamilyID: merged.ID, Acquisition: sourceBuildAcquisitionForTest(catalog.Versions[3])},
	}
	sortLineageTransitions(want)
	if fmt.Sprintf("%#v", got.Transitions) != fmt.Sprintf("%#v", want) {
		t.Fatalf("transitions = %#v, want %#v", got.Transitions, want)
	}
	if len(rollingOnly) != 0 {
		t.Fatalf("rolling-only families = %#v, want none", rollingOnly)
	}
}

func TestGenerateRollingSQLiteLineageUsesSQLiteFreshAcquisition(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1")
	jsonl := lineageTestFamily(t, "jsonl", "asset-default")
	sqlite := lineageTestFamily(t, "sqlite", "source-sqlite")
	fresh := lineageTestCensus(t, catalog, []observation{
		{Version: catalog.Versions[0].Version, Scenario: freshScenario, Provenance: provenanceFromEntry(catalog.Versions[0]), Acquisition: acquisitionForReleaseAsset(), FamilyID: jsonl.ID},
		rollingSQLiteFreshObservation(catalog.Versions[0], sqlite.ID),
	}, jsonl, sqlite)
	runtime := newFakeRollingSQLiteRuntime(t, nil)

	if _, _, err := generateRollingSQLiteLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime()); err != nil {
		t.Fatalf("generate rolling SQLite lineage: %v", err)
	}
	if got, want := runtime.acquisitions, []acquisition{sourceBuildAcquisitionForTest(catalog.Versions[0])}; fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		t.Fatalf("acquisitions = %#v, want SQLite fresh acquisition %#v", got, want)
	}
}

func TestGenerateRollingSQLiteLineageReseedsAReappearingFreshFamily(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1", "v0.9.2", "v0.9.3")
	first := lineageTestFamily(t, "sqlite", "first")
	second := lineageTestFamily(t, "sqlite", "second")
	fresh := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[1], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[2], first.ID),
	}, first)
	runtime := newFakeRollingSQLiteRuntime(t, map[string]map[string]family{
		"v0.9.2": {first.ID: second},
		"v0.9.3": {first.ID: first, second.ID: second},
	})

	got, rollingOnly, err := generateRollingSQLiteLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.reads["v0.9.3"] != 2 {
		t.Fatalf("v0.9.3 reads = %d, want reappearing first and retained second frontiers", runtime.reads["v0.9.3"])
	}
	if err := mergeRollingLineages(&fresh, []lineageSet{got}, [][]family{rollingOnly}); err != nil {
		t.Fatal(err)
	}
	if err := validateCensus(fresh, catalog); err != nil {
		t.Fatalf("full census validation lost the reappearing SQLite frontier: %v", err)
	}
}

func TestGenerateRollingSQLiteLineageRejectsFreshWorkspaceWithUnexpectedFamily(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1")
	want := lineageTestFamily(t, "sqlite", "expected")
	got := lineageTestFamily(t, "sqlite", "actual")
	result := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], want.ID),
	}, want)
	runtime := newFakeRollingSQLiteRuntime(t, nil)
	runtime.initialFamily = got

	_, _, err := generateRollingSQLiteLineageWithRuntime(context.Background(), catalog, result, "cache", runtime.runtime())
	if err == nil {
		t.Fatal("accepted a retained workspace whose observed family differs from the fresh census")
	}
}

func TestGenerateRollingSQLiteLineageKeepsUntouchedFrontierAfterFailedRead(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1", "v0.9.2", "v0.9.3")
	first := lineageTestFamily(t, "sqlite", "first")
	second := lineageTestFamily(t, "sqlite", "second")
	result := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[1], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[2], first.ID),
	}, first)
	runtime := newFakeRollingSQLiteRuntime(t, map[string]map[string]family{
		"v0.9.3": {first.ID: second},
	})
	runtime.failures["v0.9.2"] = true

	got, rollingOnly, err := generateRollingSQLiteLineageWithRuntime(context.Background(), catalog, result, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Transitions) != 1 || got.Transitions[0].TargetVersion != "v0.9.3" || got.Transitions[0].FromFamilyID != first.ID || got.Transitions[0].ToFamilyID != second.ID {
		t.Fatalf("transitions = %#v, want only the later reachable edge", got.Transitions)
	}
	if fmt.Sprintf("%#v", rollingOnly) != fmt.Sprintf("%#v", []family{second}) {
		t.Fatalf("rolling-only families = %#v, want %#v", rollingOnly, []family{second})
	}
}

func TestGenerateRollingSQLiteLineageRecordsMutatingFailureAndRetainsItsFamily(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1", "v0.9.2", "v0.9.3")
	first := lineageTestFamily(t, "sqlite", "first")
	partial := lineageTestFamily(t, "sqlite", "partial")
	result := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[1], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[2], first.ID),
	}, first)
	runtime := newFakeRollingSQLiteRuntime(t, map[string]map[string]family{
		"v0.9.2": {first.ID: partial},
		"v0.9.3": {first.ID: first, partial.ID: partial},
	})
	runtime.mutatingFailures["v0.9.2"] = true

	got, rollingOnly, err := generateRollingSQLiteLineageWithRuntime(context.Background(), catalog, result, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Transitions) != 2 || got.Transitions[0].TargetVersion != "v0.9.3" || got.Transitions[1].TargetVersion != "v0.9.3" {
		t.Fatalf("transitions = %#v, want advances from the partial and reappearing fresh frontiers", got.Transitions)
	}
	if len(got.Outcomes) != 1 || got.Outcomes[0].Outcome != lineageOutcomeMutatingFailure ||
		got.Outcomes[0].FromFamilyID != first.ID || got.Outcomes[0].ToFamilyID != partial.ID ||
		got.Outcomes[0].TargetVersion != "v0.9.2" {
		t.Fatalf("outcomes = %#v, want deterministic partial-migration record", got.Outcomes)
	}
	if fmt.Sprintf("%#v", rollingOnly) != fmt.Sprintf("%#v", []family{partial}) {
		t.Fatalf("rolling-only families = %#v, want retained partial family %#v", rollingOnly, []family{partial})
	}
}

func TestGenerateRollingSQLiteLineageRecordsSemanticNoopFailureAsUnchanged(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1", "v0.9.2", "v0.9.3")
	first := lineageTestFamily(t, "sqlite", "first")
	second := lineageTestFamily(t, "sqlite", "second")
	fresh := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[1], first.ID),
		rollingSQLiteFreshObservation(catalog.Versions[2], first.ID),
	}, first)
	runtime := newFakeRollingSQLiteRuntime(t, map[string]map[string]family{
		"v0.9.2": {first.ID: first},
		"v0.9.3": {first.ID: second},
	})
	runtime.mutatingFailures["v0.9.2"] = true
	runtime.snapshotChanges["v0.9.2"] = true

	got, _, err := generateRollingSQLiteLineageWithRuntime(context.Background(), catalog, fresh, "cache", runtime.runtime())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Outcomes) != 1 || got.Outcomes[0].Outcome != lineageOutcomeUnchangedRefusal || got.Outcomes[0].ToFamilyID != "" {
		t.Fatalf("outcomes = %#v, want one semantic unchanged refusal", got.Outcomes)
	}
	if len(got.Transitions) != 1 || got.Transitions[0].TargetVersion != "v0.9.3" || got.Transitions[0].FromFamilyID != first.ID || got.Transitions[0].ToFamilyID != second.ID {
		t.Fatalf("transitions = %#v, want later advance from retained semantic family", got.Transitions)
	}
}

func TestSnapshotRetainedSQLiteSourceCoversSidecarsAndMetadata(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	retained := &retainedSQLiteWorkspace{path: workspace}
	before, err := snapshotRetainedSQLiteSource(retained)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db-wal"), []byte("new sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	afterSidecar, err := snapshotRetainedSQLiteSource(retained)
	if err != nil {
		t.Fatal(err)
	}
	if before == afterSidecar {
		t.Fatal("snapshot ignored a new SQLite sidecar")
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(`{"backend":"sqlite"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	afterMetadata, err := snapshotRetainedSQLiteSource(retained)
	if err != nil {
		t.Fatal(err)
	}
	if afterSidecar == afterMetadata {
		t.Fatal("snapshot ignored SQLite metadata")
	}
}

func rollingSQLiteFreshObservation(entry catalogEntry, familyID string) observation {
	return observation{
		Version: entry.Version, Scenario: freshSQLiteScenario, Provenance: provenanceFromEntry(entry),
		Acquisition: sourceBuildAcquisitionForTest(entry), FamilyID: familyID,
	}
}

func freshDefaultObservation(entry catalogEntry, familyID string) observation {
	return observation{
		Version: entry.Version, Scenario: freshScenario, Provenance: provenanceFromEntry(entry),
		Acquisition: sourceBuildAcquisitionForTest(entry), FamilyID: familyID,
	}
}

func TestGenerateRollingSQLiteLineageRejectsMutatedRetainedFrontierBeforeHistoricalRead(t *testing.T) {
	catalog := lineageTestCatalog(t, "v0.9.1", "v0.9.2")
	before := lineageTestFamily(t, "sqlite", "before")
	corrupted := lineageTestFamily(t, "sqlite", "corrupted-out-of-band")
	after := lineageTestFamily(t, "sqlite", "after")
	fresh := lineageTestCensus(t, catalog, []observation{
		rollingSQLiteFreshObservation(catalog.Versions[0], before.ID),
		rollingSQLiteFreshObservation(catalog.Versions[1], before.ID),
	}, before)
	runtime := newFakeRollingSQLiteRuntime(t, map[string]map[string]family{
		"v0.9.2": {before.ID: after},
	})
	configured := runtime.runtime()
	acquire := configured.acquire
	configured.acquire = func(ctx context.Context, entry catalogEntry, cache, mode string, acquired acquisition) (string, error) {
		binary, err := acquire(ctx, entry, cache, mode, acquired)
		if entry.Version == "v0.9.2" {
			// Simulate a sibling historical command modifying the retained state.
			runtime.states["workspace-1"] = corrupted
		}
		return binary, err
	}

	lineage, rollingOnly, err := generateRollingSQLiteLineageWithRuntime(context.Background(), catalog, fresh, "cache", configured)
	if err == nil {
		t.Fatal("accepted a retained SQLite frontier mutated out of band")
	}
	if isHistoricalProcessExit(err) {
		t.Fatalf("frontier-integrity error must not be a historical process exit: %v", err)
	}
	if got := runtime.reads["v0.9.2"]; got != 0 {
		t.Fatalf("historical SQLite read count = %d, want 0 after frontier mutation", got)
	}
	if len(lineage.Transitions) != 0 || len(lineage.Outcomes) != 0 || len(rollingOnly) != 0 {
		t.Fatalf("lineage after rejected frontier = %#v, rolling-only = %#v; want no records", lineage, rollingOnly)
	}
}

type fakeRollingSQLiteRuntime struct {
	t                 *testing.T
	transitions       map[string]map[string]family
	states            map[string]family
	reads             map[string]int
	acquisitions      []acquisition
	failures          map[string]bool
	mutatingFailures  map[string]bool
	snapshotChanges   map[string]bool
	snapshotRevisions map[string]int
	initialized       int
	initialFamily     family
	nextWorkspace     int
}

func newFakeRollingSQLiteRuntime(t *testing.T, transitions map[string]map[string]family) *fakeRollingSQLiteRuntime {
	t.Helper()
	return &fakeRollingSQLiteRuntime{
		t: t, transitions: transitions, states: make(map[string]family), reads: make(map[string]int),
		failures: make(map[string]bool), mutatingFailures: make(map[string]bool), snapshotChanges: make(map[string]bool), snapshotRevisions: make(map[string]int),
	}
}

func (fake *fakeRollingSQLiteRuntime) runtime() rollingSQLiteRuntime {
	return rollingSQLiteRuntime{
		acquire: func(_ context.Context, entry catalogEntry, _ string, _ string, acquired acquisition) (string, error) {
			fake.acquisitions = append(fake.acquisitions, acquired)
			return entry.Version, nil
		},
		initialize: func(_ context.Context, _ string, _ catalogEntry, _ acquisition, expected family) (*retainedSQLiteWorkspace, family, error) {
			fake.initialized++
			fake.nextWorkspace++
			workspace := &retainedSQLiteWorkspace{path: fmt.Sprintf("workspace-%d", fake.nextWorkspace)}
			observed := fake.initialFamily
			if observed.ID == "" {
				observed = expected
			}
			fake.states[workspace.path] = observed
			return workspace, observed, nil
		},
		read: func(_ context.Context, binary string, _ catalogEntry, workspace *retainedSQLiteWorkspace) error {
			fake.reads[binary]++
			from := fake.states[workspace.path]
			if fake.failures[binary] {
				return historicalProcessExit(fmt.Errorf("historical binary rejected the workspace"))
			}
			next, ok := fake.transitions[binary][from.ID]
			if !ok {
				return fmt.Errorf("no fake transition for %s from %s", binary, from.ID)
			}
			fake.states[workspace.path] = next
			if fake.snapshotChanges[binary] {
				fake.snapshotRevisions[workspace.path]++
			}
			if fake.mutatingFailures[binary] {
				return historicalProcessExit(fmt.Errorf("historical binary failed after changing the workspace"))
			}
			return nil
		},
		snapshot: func(workspace *retainedSQLiteWorkspace) (string, error) {
			return fmt.Sprintf("%s/%d", fake.states[workspace.path].ID, fake.snapshotRevisions[workspace.path]), nil
		},
		fingerprint: func(workspace *retainedSQLiteWorkspace) (family, error) {
			return fake.states[workspace.path], nil
		},
		remove: func(_ *retainedSQLiteWorkspace) {},
	}
}
