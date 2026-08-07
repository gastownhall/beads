package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFinishRollingDoltFamilyCanonicalizesFinalLayout(t *testing.T) {
	raw := json.RawMessage(`{"topology":["directory:.beads/dolt"],"schema":{"objects":[],"capabilities":[],"migration_ledgers":[],"catalog":[]}}`)
	wantLayout, err := canonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantID, err := familyID("dolt-legacy", wantLayout)
	if err != nil {
		t.Fatal(err)
	}
	got, err := finishRollingDoltFamily("dolt-legacy", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Layout, wantLayout) || got.ID != wantID {
		t.Fatalf("family = %#v, want canonical layout and ID %q", got, wantID)
	}
}

func TestCombineRollingDoltServerSessionResultTreatsCloseFailureAsInfrastructure(t *testing.T) {
	actionErr := historicalProcessExit(errors.New("historical target failed after mutation"))
	closeErr := errors.New("server cleanup timed out")

	err := combineRollingDoltServerSessionResult(actionErr, closeErr)

	if isHistoricalProcessExit(err) {
		t.Fatalf("close failure must not be classified as a historical process exit: %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("close failure = %v, want errors.Is(_, %v)", err, closeErr)
	}
	for _, text := range []string{actionErr.Error(), closeErr.Error()} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("error %q does not retain diagnostic text %q", err, text)
		}
	}
}

func TestCombineRollingDoltServerSessionResultPreservesSingleFailureBehavior(t *testing.T) {
	actionErr := historicalProcessExit(errors.New("historical target failed"))
	if err := combineRollingDoltServerSessionResult(actionErr, nil); !isHistoricalProcessExit(err) {
		t.Fatalf("action-only failure = %v, want historical process exit", err)
	}

	closeErr := errors.New("server cleanup timed out")
	err := combineRollingDoltServerSessionResult(nil, closeErr)
	if isHistoricalProcessExit(err) {
		t.Fatalf("close-only failure must not be classified as a historical process exit: %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("close-only failure = %v, want errors.Is(_, %v)", err, closeErr)
	}
}

func TestRollingDoltExecutorRetainsOneWorkspacePerFamilyFrontier(t *testing.T) {
	root := t.TempDir()
	calls := []string{}
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root: root,
		InitializeRepository: func(_ context.Context, workspace string) error {
			calls = append(calls, "initialize:"+filepath.Base(workspace))
			return nil
		},
		Environment: func(_ string) ([]string, error) { return []string{"PATH=/bin"}, nil },
		Run: func(_ context.Context, binary, workspace string, _ []string, args ...string) error {
			calls = append(calls, fmt.Sprintf("run:%s:%s:%v", binary, filepath.Base(workspace), args))
			return nil
		},
		Observe: func(_ context.Context, _ string, scenario lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			return lineageTestFamily(t, scenario.Mode, "frontier"), nil
		},
	})
	defer executor.Close()

	scenario := lineageScenario{Name: rollingLegacyScenario, Mode: "dolt-legacy", Start: "v0.47.2", End: "v0.55.4"}
	claimed := lineageTestFamily(t, scenario.Mode, "frontier")
	first, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.50.3", Binary: "/historical/first", FamilyID: claimed.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.50.3", Binary: "/historical/other", FamilyID: claimed.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("the same family frontier allocated more than one retained workspace")
	}
	if got, want := calls, []string{
		"initialize:" + filepath.Base(first.Workspace),
		"run:/historical/first:" + filepath.Base(first.Workspace) + ":[init --backend dolt --prefix census]",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

func TestRollingDoltAdvancePreservesScenarioProvenanceAcrossTopologyModes(t *testing.T) {
	legacy := lineageTestFamily(t, "dolt-legacy", "legacy")
	embedded := lineageTestFamily(t, "dolt-embedded", "embedded")
	state := legacy
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, _ string, _ string, _ []string, args ...string) error {
			if reflect.DeepEqual(args, []string{"list", "--json"}) {
				state = embedded
			}
			return nil
		},
		Observe: func(context.Context, string, lineageScenario, []string, doltObservationEndpoint) (family, error) {
			return state, nil
		},
	})
	defer executor.Close()
	scenario := lineageScenario{Name: rollingLegacyScenario, Mode: "dolt-legacy", Start: "v0.47.2", End: "v1.1.2"}
	frontier, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.55.4", Binary: "/historical/source", FamilyID: legacy.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	transition, target, err := executor.Advance(context.Background(), frontier, rollingDoltTarget{
		Version: "v0.55.5", Binary: "/historical/target",
	})
	if err != nil {
		t.Fatalf("advance across intrinsic topology modes: %v", err)
	}
	if target.Mode != "dolt-embedded" || transition.Mode != "dolt-legacy" || transition.RuntimeMode != "dolt-legacy" {
		t.Fatalf("transition = %#v, target = %#v; want legacy provenance and embedded topology", transition, target)
	}
	intrinsicID, err := familyID(target.Mode, target.Layout)
	if err != nil {
		t.Fatal(err)
	}
	lineageID, err := familyID(transition.Mode, target.Layout)
	if err != nil {
		t.Fatal(err)
	}
	if target.ID != intrinsicID || target.ID == lineageID {
		t.Fatalf("target ID = %q, intrinsic = %q, lineage-derived = %q", target.ID, intrinsicID, lineageID)
	}
}

func TestRollingDoltObserverDeduplicatesIdenticalTopologyAcrossLineageOrigins(t *testing.T) {
	legacyEntry := testCatalog().Versions[0]
	embeddedEntry := legacyEntry
	embeddedEntry.Origin = catalogOrigin{Hash: "embedded-origin", Ref: "refs/tags/v0.63.1"}
	layout := []byte(`{"schema":{"objects":[]},"topology":["directory:.beads/embeddeddolt"]}`)
	legacyObservation, observed, err := finishObservation(legacyEntry, sourceBuildAcquisitionForTest(), "dolt-embedded", layout)
	if err != nil {
		t.Fatal(err)
	}
	embeddedObservation, embeddedObserved, err := finishObservation(embeddedEntry, sourceBuildAcquisitionForTest(), "dolt-embedded", layout)
	if err != nil {
		t.Fatal(err)
	}
	if legacyObservation.Provenance == embeddedObservation.Provenance {
		t.Fatal("test setup did not produce distinct lineage provenance")
	}
	if observed.ID != embeddedObserved.ID {
		t.Fatalf("identical observable topology produced family IDs %q and %q", observed.ID, embeddedObserved.ID)
	}

	legacy := lineageTestFamily(t, "dolt-legacy", "legacy-origin")
	embedded := lineageTestFamily(t, "dolt-embedded", "embedded-origin")
	initialFamilies := []family{legacy, embedded}
	states := make(map[string]family)
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, _ string, workspace string, _ []string, args ...string) error {
			if reflect.DeepEqual(args, []string{"list", "--json"}) {
				states[workspace] = observed
			}
			return nil
		},
		Observe: func(_ context.Context, workspace string, _ lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			if state, ok := states[workspace]; ok {
				return state, nil
			}
			state := initialFamilies[0]
			initialFamilies = initialFamilies[1:]
			states[workspace] = state
			return state, nil
		},
	})
	defer executor.Close()

	legacyScenario := lineageScenario{Name: rollingLegacyScenario, Mode: "dolt-legacy", Start: "v0.47.2", End: "v1.1.2"}
	embeddedScenario := lineageScenario{Name: rollingEmbeddedScenario, Mode: "dolt-embedded", Start: "v0.63.0", End: "v1.1.2"}
	legacyFrontier, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: legacyScenario, Version: "v0.55.4", Binary: "/historical/legacy", FamilyID: legacy.ID})
	if err != nil {
		t.Fatal(err)
	}
	embeddedFrontier, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: embeddedScenario, Version: "v0.63.0", Binary: "/historical/embedded", FamilyID: embedded.ID})
	if err != nil {
		t.Fatal(err)
	}

	legacyTransition, legacyTarget, err := executor.Advance(context.Background(), legacyFrontier, rollingDoltTarget{Version: "v0.63.1", Binary: "/historical/target", Runtime: embeddedScenario})
	if err != nil {
		t.Fatal(err)
	}
	embeddedTransition, embeddedTarget, err := executor.Advance(context.Background(), embeddedFrontier, rollingDoltTarget{Version: "v0.63.1", Binary: "/historical/target", Runtime: embeddedScenario})
	if err != nil {
		t.Fatal(err)
	}
	if legacyTarget.ID != embeddedTarget.ID || legacyTarget.ID != observed.ID {
		t.Fatalf("identical observable topology produced families %q and %q, want %q", legacyTarget.ID, embeddedTarget.ID, observed.ID)
	}
	if legacyTransition.Mode != "dolt-legacy" || legacyTransition.RuntimeMode != "dolt-embedded" ||
		embeddedTransition.Mode != "dolt-embedded" || embeddedTransition.RuntimeMode != "dolt-embedded" {
		t.Fatalf("transitions lost lineage provenance: legacy=%#v embedded=%#v", legacyTransition, embeddedTransition)
	}
}

func TestRollingDoltAdvanceObservesServerClassifiedDualRootsThroughEndpoint(t *testing.T) {
	legacy := lineageTestFamily(t, "dolt-legacy", "legacy")
	server := lineageTestFamily(t, "dolt-server", "dual-server")
	endpoints := []doltObservationEndpoint{}
	afterTarget := false
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, _ string, workspace string, _ []string, args ...string) error {
			if err := os.MkdirAll(filepath.Join(workspace, ".beads", "dolt"), 0o755); err != nil {
				return err
			}
			if reflect.DeepEqual(args, []string{"list", "--json"}) {
				afterTarget = true
				if err := os.MkdirAll(filepath.Join(workspace, ".beads", "embeddeddolt"), 0o755); err != nil {
					return err
				}
				return os.WriteFile(
					filepath.Join(workspace, ".beads", "metadata.json"),
					[]byte(`{"backend":"dolt","dolt_mode":"server"}`),
					0o600,
				)
			}
			return nil
		},
		WithServer: func(_ context.Context, _ string, _ string, _ lineageScenario, _ string, environment []string, action func(int, []string) error) error {
			return action(45123, environment)
		},
		Observe: func(_ context.Context, _ string, _ lineageScenario, _ []string, endpoint doltObservationEndpoint) (family, error) {
			endpoints = append(endpoints, endpoint)
			if afterTarget {
				return server, nil
			}
			return legacy, nil
		},
	})
	defer executor.Close()
	scenario := lineageScenarioMapMust(rollingLegacyScenario)
	frontier, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.55.4", Binary: "/source", FamilyID: legacy.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	transition, observed, err := executor.Advance(context.Background(), frontier, rollingDoltTarget{
		Version: "v0.63.0", Binary: "/target", Runtime: lineageScenarioMapMust(rollingEmbeddedScenario),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := endpoints, []doltObservationEndpoint{{}, {}, {host: "127.0.0.1", port: 45123}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("observation endpoints = %#v, want %#v", got, want)
	}
	if transition.Mode != "dolt-legacy" || transition.RuntimeMode != "dolt-embedded" || observed.Mode != "dolt-server" {
		t.Fatalf("transition = %#v, observed = %#v", transition, observed)
	}
}

func TestRollingDoltExecutorRetainRejectsObservedFamilyThatDiffersFromFreshClaim(t *testing.T) {
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run:                  func(context.Context, string, string, []string, ...string) error { return nil },
		Observe: func(_ context.Context, _ string, scenario lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			return lineageTestFamily(t, scenario.Mode, "observed"), nil
		},
	})
	defer executor.Close()

	_, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: lineageScenario{Name: rollingEmbeddedScenario, Mode: "dolt-embedded", Start: "v0.63.0", End: "v1.1.2"},
		Version:  "v0.63.0", Binary: "/historical/source", FamilyID: lineageTestFamily(t, "dolt-embedded", "claimed").ID,
	})
	if err == nil || !strings.Contains(err.Error(), "initialized Dolt family") {
		t.Fatalf("Retain error = %v, want observed fresh-family mismatch", err)
	}
	if got := executor.WorkspaceCount(); got != 0 {
		t.Fatalf("retained workspace count = %d, want 0 after rejected observation", got)
	}
}

func TestRollingDoltAdvanceRejectsMutatedRetainedFrontierBeforeHistoricalTarget(t *testing.T) {
	before := lineageTestFamily(t, "dolt-legacy", "before")
	corrupted := lineageTestFamily(t, "dolt-legacy", "corrupted-out-of-band")
	targetRuns := 0
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root: t.TempDir(),
		InitializeRepository: func(_ context.Context, workspace string) error {
			return os.WriteFile(filepath.Join(workspace, "frontier-state"), []byte("before"), 0o600)
		},
		Environment: func(string) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, _ string, _ string, _ []string, args ...string) error {
			if len(args) > 0 && args[0] == "list" {
				targetRuns++
			}
			return nil
		},
		Observe: func(_ context.Context, workspace string, _ lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			state, err := os.ReadFile(filepath.Join(workspace, "frontier-state"))
			if err != nil {
				return family{}, err
			}
			if string(state) == "corrupted" {
				return corrupted, nil
			}
			return before, nil
		},
	})
	defer executor.Close()

	scenario := lineageScenario{Name: rollingLegacyScenario, Mode: "dolt-legacy", Start: "v0.47.2", End: "v1.1.2"}
	frontier, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.55.4", Binary: "/historical/source", FamilyID: before.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontier.Workspace, "frontier-state"), []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}

	transition, observed, err := executor.Advance(context.Background(), frontier, rollingDoltTarget{Version: "v0.55.5", Binary: "/historical/target"})
	if err == nil {
		t.Fatal("accepted a retained Dolt frontier mutated out of band")
	}
	if isHistoricalProcessExit(err) {
		t.Fatalf("frontier-integrity error must not be a historical process exit: %v", err)
	}
	if targetRuns != 0 {
		t.Fatalf("historical Dolt target runs = %d, want 0 after frontier mutation", targetRuns)
	}
	if transition != (lineageTransition{}) || observed.ID != "" || observed.Mode != "" || len(observed.Layout) != 0 {
		t.Fatalf("rejected frontier returned transition %#v and observed family %#v, want neither", transition, observed)
	}
}

func TestRollingDoltExecutorServerObservationsUseTheActiveEndpoint(t *testing.T) {
	before := lineageTestFamily(t, "dolt-server", "before")
	after := lineageTestFamily(t, "dolt-server", "after")
	var endpoints []doltObservationEndpoint
	state := before
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return []string{"PATH=/bin"}, nil },
		Run: func(_ context.Context, _ string, _ string, _ []string, args ...string) error {
			if reflect.DeepEqual(args, []string{"list", "--json"}) {
				state = after
			}
			return nil
		},
		WithServer: func(_ context.Context, _ string, _ string, _ lineageScenario, _ string, environment []string, action func(int, []string) error) error {
			return action(45123, environment)
		},
		Observe: func(_ context.Context, _ string, _ lineageScenario, _ []string, endpoint doltObservationEndpoint) (family, error) {
			endpoints = append(endpoints, endpoint)
			return state, nil
		},
	})
	defer executor.Close()

	scenario := lineageScenario{Name: rollingServerScenario, Mode: "dolt-server", Start: "v0.49.1", End: "v1.1.2"}
	frontier, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.55.4", Binary: "/historical/source", FamilyID: before.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := executor.Advance(context.Background(), frontier, rollingDoltTarget{Version: "v0.55.5", Binary: "/historical/target"}); err != nil {
		t.Fatal(err)
	}
	want := []doltObservationEndpoint{{host: "127.0.0.1", port: 45123}, {host: "127.0.0.1", port: 45123}, {host: "127.0.0.1", port: 45123}}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("observation endpoints = %#v, want %#v", endpoints, want)
	}
}

func TestRollingDoltExecutorRejectsLegacyServerTransportFailureWithoutObservation(t *testing.T) {
	before := lineageTestFamily(t, "dolt-legacy", "before")
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run:                  func(context.Context, string, string, []string, ...string) error { return nil },
		WithServer: func(context.Context, string, string, lineageScenario, string, []string, func(int, []string) error) error {
			return errors.New("pinned server did not start")
		},
		Observe: func(context.Context, string, lineageScenario, []string, doltObservationEndpoint) (family, error) {
			return before, nil
		},
	})
	defer executor.Close()
	legacy := lineageScenario{Name: rollingLegacyScenario, Mode: "dolt-legacy", Start: "v0.47.2", End: "v1.1.2"}
	frontier, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: legacy, Version: "v0.55.4", Binary: "/source", FamilyID: before.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, observed, err := executor.Advance(context.Background(), frontier, rollingDoltTarget{Version: "v0.56.0", Binary: "/target", Runtime: lineageScenarioMapMust(rollingServerScenario)})
	if err == nil || observed.ID != "" {
		t.Fatalf("transport failure = %v, observed = %#v; want fatal empty observation", err, observed)
	}
}

func TestRollingDoltExecutorClassifiesLegacyServerTargetMutationAfterSession(t *testing.T) {
	before := lineageTestFamily(t, "dolt-legacy", "before")
	partial := lineageTestFamily(t, "dolt-legacy", "partial")
	runs := 0
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run: func(context.Context, string, string, []string, ...string) error {
			runs++
			if runs == 2 {
				return historicalProcessExit(errors.New("historical target failed after mutation"))
			}
			return nil
		},
		WithServer: func(_ context.Context, _ string, _ string, _ lineageScenario, _ string, environment []string, action func(int, []string) error) error {
			return action(45123, environment)
		},
		Observe: func(_ context.Context, _ string, _ lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			if runs < 2 {
				return before, nil
			}
			return partial, nil
		},
	})
	defer executor.Close()
	legacy := lineageScenario{Name: rollingLegacyScenario, Mode: "dolt-legacy", Start: "v0.47.2", End: "v1.1.2"}
	frontier, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: legacy, Version: "v0.55.4", Binary: "/source", FamilyID: before.ID})
	if err != nil {
		t.Fatal(err)
	}
	transition, observed, err := executor.Advance(context.Background(), frontier, rollingDoltTarget{Version: "v0.56.0", Binary: "/target", Runtime: lineageScenarioMapMust(rollingServerScenario)})
	if err == nil || observed.ID != partial.ID || transition.ToFamilyID != partial.ID {
		t.Fatalf("target mutation = transition %#v, observed %#v, error %v", transition, observed, err)
	}
}

// This opt-in regression advances one authentic legacy lineage through every
// consecutive catalog release at the legacy-to-embedded boundary. It stays out
// of the normal suite because it builds the catalog-pinned historical sources.
func TestRollingDoltExecutorAuthenticConsecutiveLegacyLineageToV0630(t *testing.T) {
	if os.Getenv("BEADS_CENSUS_AUTHENTIC") != "1" {
		t.Skip("set BEADS_CENSUS_AUTHENTIC=1 to run the consecutive historical Dolt lineage")
	}
	catalog, _, err := readCatalog("../release-catalog.json", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	cache, err := defaultCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	scenario := lineageScenarioMapMust(rollingLegacyScenario)
	var sourceEntry catalogEntry
	for _, entry := range catalog.Versions {
		if entry.Version == "v0.49.1" {
			sourceEntry = entry
			break
		}
	}
	if sourceEntry.Version == "" {
		t.Fatal("release catalog lacks v0.49.1")
	}
	sourceBinding := authenticatedCensusSourceBinary(t, ctx, sourceEntry, cache)
	ctx = withHistoricalBinaryBinding(ctx, sourceBinding)
	source := sourceBinding.path
	freshWorkspace := t.TempDir()
	if err := initializeCensusRepository(ctx, freshWorkspace); err != nil {
		t.Fatalf("initialize fresh legacy workspace: %v", err)
	}
	environment, err := censusEnvironment(freshWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	args, err := rollingDoltInitArgs(scenario, sourceEntry.Version, "census", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := runRollingDoltCommand(ctx, source, freshWorkspace, environment, args...); err != nil {
		t.Fatalf("initialize fresh legacy family: %v", err)
	}
	freshFamily, err := observeRollingDoltFamily(ctx, freshWorkspace, scenario, environment, doltObservationEndpoint{})
	if err != nil {
		t.Fatalf("observe fresh legacy family: %v", err)
	}
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{Root: filepath.Join(t.TempDir(), "workspaces")})
	defer executor.Close()
	frontier, err := executor.Retain(ctx, rollingDoltSource{
		Scenario: scenario, Version: sourceEntry.Version, Binary: source, FamilyID: freshFamily.ID,
	})
	if err != nil {
		t.Fatalf("retain v0.49.1 legacy frontier: %v", err)
	}
	var boundary lineageTransition
	var boundaryFamily family
	failures := []lineageOutcome{}
	for _, entry := range catalog.Versions {
		if compareReleaseVersions(entry.Version, sourceEntry.Version) <= 0 {
			continue
		}
		if compareReleaseVersions(entry.Version, "v0.63.0") > 0 {
			break
		}
		fromID := frontier.FamilyID
		targetBinding := authenticatedCensusSourceBinary(t, ctx, entry, cache)
		ctx = withHistoricalBinaryBinding(ctx, targetBinding)
		target := targetBinding.path
		runtime, err := rollingDoltTargetRuntime(scenario, entry.Version)
		if err != nil {
			t.Fatal(err)
		}
		transition, observed, advanceErr := executor.Advance(ctx, frontier, rollingDoltTarget{
			Version: entry.Version, Binary: target, Runtime: runtime,
		})
		if entry.Version == "v0.63.0" &&
			(errors.Is(advanceErr, io.EOF) || strings.Contains(strings.ToLower(fmt.Sprint(advanceErr)), "eof")) {
			t.Fatalf("v0.63.0 observation ended at EOF: %v", advanceErr)
		}
		if advanceErr != nil {
			if !isHistoricalProcessExit(advanceErr) || observed.ID == "" {
				t.Fatalf("%s: advance retained frontier: %v", entry.Version, advanceErr)
			}
			outcome := lineageOutcome{
				FromFamilyID: fromID, TargetVersion: entry.Version, Scenario: scenario.Name,
				Mode: scenario.Mode, RuntimeMode: runtime.Mode,
			}
			if transition.ToFamilyID == "" {
				outcome.Outcome = lineageOutcomeUnchangedRefusal
			} else {
				outcome.Outcome = lineageOutcomeMutatingFailure
				outcome.ToFamilyID = observed.ID
				if frontier.FamilyID != observed.ID {
					t.Fatalf("%s: mutating failure did not advance retained frontier", entry.Version)
				}
			}
			failures = append(failures, outcome)
		}
		if entry.Version == "v0.63.0" {
			boundary, boundaryFamily = transition, observed
		}
	}
	if boundary.TargetVersion != "v0.63.0" || boundary.Mode != "dolt-legacy" || boundary.RuntimeMode != "dolt-embedded" {
		t.Fatalf("v0.63.0 transition = %#v, want legacy provenance and embedded runtime", boundary)
	}
	topology, err := recognizeFreshTopology(frontier.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	intrinsicID, err := familyID(topology.Mode, boundaryFamily.Layout)
	if err != nil {
		t.Fatal(err)
	}
	lineageID, err := familyID(boundary.Mode, boundaryFamily.Layout)
	if err != nil {
		t.Fatal(err)
	}
	if boundaryFamily.Mode != topology.Mode || boundaryFamily.ID != intrinsicID || boundaryFamily.ID == lineageID {
		t.Fatalf("v0.63.0 family = %#v, topology = %q, lineage-derived ID = %q", boundaryFamily, topology.Mode, lineageID)
	}
	var layout struct {
		Schema doltFingerprint    `json:"schema"`
		Stores []labeledDoltStore `json:"stores"`
	}
	if err := json.Unmarshal(boundaryFamily.Layout, &layout); err != nil {
		t.Fatal(err)
	}
	if len(layout.Stores) != 2 ||
		layout.Stores[0].Name != "dolt" ||
		layout.Stores[1].Name != "embeddeddolt" ||
		reflect.DeepEqual(layout.Stores[0].Schema, layout.Stores[1].Schema) ||
		!reflect.DeepEqual(layout.Schema, layout.Stores[0].Schema) {
		t.Fatalf("v0.63.0 dual-root layout = %#v", layout)
	}
	t.Logf("recorded %d historical process failures while advancing the retained frontier", len(failures))
}

func authenticatedCensusSourceBinary(t *testing.T, ctx context.Context, entry catalogEntry, cache string) freshBinary {
	t.Helper()
	binary, err := acquireSourceBuild(ctx, entry, cache)
	if err != nil {
		t.Fatalf("%s: build catalog-authenticated source: %v", entry.Version, err)
	}
	acquired, err := recordAcquisition("source-build", entry, binary, "")
	if err != nil {
		t.Fatalf("%s: record authenticated source binary: %v", entry.Version, err)
	}
	binding, err := bindFreshBinary(binary, acquired)
	if err != nil {
		t.Fatalf("%s: bind authenticated source binary: %v", entry.Version, err)
	}
	verified, err := verifyFreshBinary(binding)
	if err != nil {
		t.Fatalf("%s: verify cached source binary: %v", entry.Version, err)
	}
	if verified != binding.path {
		t.Fatalf("%s: verified source path = %q, want %q", entry.Version, verified, binding.path)
	}
	return binding
}

func TestRollingDoltExecutorAdvancesARealTargetCommandAndMovesFrontier(t *testing.T) {
	root := t.TempDir()
	before := lineageTestFamily(t, "dolt-embedded", "before")
	after := lineageTestFamily(t, "dolt-embedded", "after")
	state := before
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 root,
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, binary, _ string, _ []string, args ...string) error {
			if reflect.DeepEqual(args, []string{"init", "--prefix", "census"}) {
				if binary != "/historical/source" {
					t.Fatalf("source initialization binary = %q", binary)
				}
				return nil
			}
			if binary != "/historical/target" || !reflect.DeepEqual(args, []string{"list", "--json"}) {
				t.Fatalf("rolling command = %q %q, want target list --json", binary, args)
			}
			state = after
			return nil
		},
		Observe: func(_ context.Context, _ string, _ lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			return state, nil
		},
	})
	defer executor.Close()

	scenario := lineageScenario{Name: rollingEmbeddedScenario, Mode: "dolt-embedded", Start: "v0.63.0", End: "v1.1.2"}
	frontier, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.63.0", Binary: "/historical/source", FamilyID: before.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	transition, after, err := executor.Advance(context.Background(), frontier, rollingDoltTarget{
		Version: "v0.63.1", Binary: "/historical/target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if transition.FromFamilyID != before.ID || transition.ToFamilyID != after.ID || transition.Scenario != rollingEmbeddedScenario || transition.Mode != "dolt-embedded" {
		t.Fatalf("transition = %#v, family = %#v", transition, after)
	}
	if frontier.FamilyID != after.ID || frontier.Version != "v0.63.1" {
		t.Fatalf("frontier was not moved to the observed target: %#v", frontier)
	}
	if _, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.63.1", Binary: "/historical/target", FamilyID: after.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if got := executor.WorkspaceCount(); got != 1 {
		t.Fatalf("retained workspace count = %d, want 1", got)
	}
}

func TestRollingDoltExecutorObservesAndAdvancesMutatedFrontierAfterFailedTargetCommand(t *testing.T) {
	before := lineageTestFamily(t, "dolt-embedded", "before")
	partial := lineageTestFamily(t, "dolt-embedded", "partial")
	state := before
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, _ string, _ string, _ []string, args ...string) error {
			if reflect.DeepEqual(args, []string{"init", "--prefix", "census"}) {
				return nil
			}
			state = partial
			return historicalProcessExit(errors.New("historical command failed after opening Dolt"))
		},
		Observe: func(_ context.Context, _ string, _ lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			return state, nil
		},
	})
	defer executor.Close()

	scenario := lineageScenario{Name: rollingEmbeddedScenario, Mode: "dolt-embedded", Start: "v0.63.0", End: "v1.1.2"}
	frontier, err := executor.Retain(context.Background(), rollingDoltSource{
		Scenario: scenario, Version: "v0.63.0", Binary: "/historical/source", FamilyID: before.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	transition, observed, err := executor.Advance(context.Background(), frontier, rollingDoltTarget{
		Version: "v0.63.1", Binary: "/historical/target",
	})
	if err == nil {
		t.Fatal("Advance succeeded after the historical command failure")
	}
	if observed.ID != partial.ID || observed.Mode != "dolt-embedded" {
		t.Fatalf("failed target observation = %#v, want partial public SQL family %#v", observed, partial)
	}
	if transition.FromFamilyID != before.ID || transition.ToFamilyID != partial.ID {
		t.Fatalf("failed target transition = %#v, want %s -> %s", transition, before.ID, partial.ID)
	}
	if frontier.FamilyID != partial.ID || frontier.Version != "v0.63.1" {
		t.Fatalf("mutating failed target did not advance frontier: %#v", frontier)
	}
}

func TestRollingDoltExecutorMergesConvergedWorkspaces(t *testing.T) {
	first := lineageTestFamily(t, "dolt-embedded", "first")
	second := lineageTestFamily(t, "dolt-embedded", "second")
	merged := lineageTestFamily(t, "dolt-embedded", "merged")
	initialFamilies := []family{first, second}
	states := make(map[string]family)
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, _ string, workspace string, _ []string, args ...string) error {
			if reflect.DeepEqual(args, []string{"list", "--json"}) {
				states[workspace] = merged
			}
			return nil
		},
		Observe: func(_ context.Context, workspace string, _ lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			if state, ok := states[workspace]; ok {
				return state, nil
			}
			state := initialFamilies[0]
			initialFamilies = initialFamilies[1:]
			states[workspace] = state
			return state, nil
		},
	})
	defer executor.Close()

	scenario := lineageScenario{Name: rollingEmbeddedScenario, Mode: "dolt-embedded", Start: "v0.63.0", End: "v1.1.2"}
	left, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: scenario, Version: "v0.63.0", Binary: "/historical/source", FamilyID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	right, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: scenario, Version: "v0.63.0", Binary: "/historical/source", FamilyID: second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := executor.Advance(context.Background(), left, rollingDoltTarget{Version: "v0.63.1", Binary: "/historical/target"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executor.Advance(context.Background(), right, rollingDoltTarget{Version: "v0.63.1", Binary: "/historical/target"}); err != nil {
		t.Fatalf("converged frontier advance failed: %v", err)
	}
	if got := executor.WorkspaceCount(); got != 1 {
		t.Fatalf("retained workspace count = %d, want 1 after convergence", got)
	}
	if _, err := os.Stat(right.Workspace); !os.IsNotExist(err) {
		t.Fatalf("merged-away workspace exists or stat failed: %v", err)
	}
}

func TestRollingDoltExecutorEndBatchKeepsFirstRetainedFrontierRegardlessOfWorkspaceName(t *testing.T) {
	for _, test := range []struct {
		name           string
		leftWorkspace  string
		rightWorkspace string
	}{
		{name: "first workspace sorts last", leftWorkspace: "z-first", rightWorkspace: "a-second"},
		{name: "first workspace sorts first", leftWorkspace: "a-first", rightWorkspace: "z-second"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := lineageTestFamily(t, "dolt-embedded", "first")
			second := lineageTestFamily(t, "dolt-embedded", "second")
			merged := lineageTestFamily(t, "dolt-embedded", "merged")
			initialFamilies := []family{first, second}
			states := make(map[string]family)
			root := t.TempDir()
			executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
				Root:                 root,
				InitializeRepository: func(context.Context, string) error { return nil },
				Environment:          func(string) ([]string, error) { return nil, nil },
				Run: func(_ context.Context, _ string, workspace string, _ []string, args ...string) error {
					if reflect.DeepEqual(args, []string{"list", "--json"}) {
						states[workspace] = merged
					}
					return nil
				},
				Observe: func(_ context.Context, workspace string, _ lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
					if state, ok := states[workspace]; ok {
						return state, nil
					}
					state := initialFamilies[0]
					initialFamilies = initialFamilies[1:]
					states[workspace] = state
					return state, nil
				},
			})
			defer executor.Close()

			scenario := lineageScenario{Name: rollingEmbeddedScenario, Mode: "dolt-embedded", Start: "v0.63.0", End: "v1.1.2"}
			left, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: scenario, Version: "v0.63.0", Binary: "/historical/source", FamilyID: first.ID})
			if err != nil {
				t.Fatal(err)
			}
			right, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: scenario, Version: "v0.63.0", Binary: "/historical/source", FamilyID: second.ID})
			if err != nil {
				t.Fatal(err)
			}
			for _, frontier := range []*retainedDoltFrontier{left, right} {
				workspace := filepath.Join(root, map[*retainedDoltFrontier]string{left: test.leftWorkspace, right: test.rightWorkspace}[frontier])
				states[workspace] = states[frontier.Workspace]
				delete(states, frontier.Workspace)
				if err := os.Rename(frontier.Workspace, workspace); err != nil {
					t.Fatal(err)
				}
				frontier.Workspace = workspace
			}
			executor.BeginBatch()
			if _, _, err := executor.Advance(context.Background(), left, rollingDoltTarget{Version: "v0.63.1", Binary: "/historical/target"}); err != nil {
				t.Fatal(err)
			}
			if _, _, err := executor.Advance(context.Background(), right, rollingDoltTarget{Version: "v0.63.1", Binary: "/historical/target"}); err != nil {
				t.Fatal(err)
			}
			next := executor.EndBatch()
			if got := next[rollingDoltFrontierKey(scenario.Name, merged.ID)]; got != left {
				t.Fatalf("survivor = %q, want first retained frontier %q", got.Workspace, left.Workspace)
			}
		})
	}
}

func TestRollingDoltExecutorStagesDistinctPostTargetFrontiers(t *testing.T) {
	first := lineageTestFamily(t, "dolt-embedded", "first")
	second := lineageTestFamily(t, "dolt-embedded", "second")
	third := lineageTestFamily(t, "dolt-embedded", "third")
	initialFamilies := []family{first, second}
	states := make(map[string]family)
	initialStates := make(map[string]family)
	targetAttempts := make(map[string]int)
	executor := newRollingDoltLineageExecutor(rollingDoltLineageConfig{
		Root:                 t.TempDir(),
		InitializeRepository: func(context.Context, string) error { return nil },
		Environment:          func(string) ([]string, error) { return nil, nil },
		Run: func(_ context.Context, _ string, workspace string, _ []string, args ...string) error {
			if !reflect.DeepEqual(args, []string{"list", "--json"}) {
				return nil
			}
			targetAttempts[workspace]++
			if targetAttempts[workspace] != 1 {
				return nil
			}
			if initialStates[workspace].ID == first.ID {
				states[workspace] = second
				return historicalProcessExit(errors.New("historical target failed after mutating"))
			}
			states[workspace] = third
			return nil
		},
		Observe: func(_ context.Context, workspace string, _ lineageScenario, _ []string, _ doltObservationEndpoint) (family, error) {
			if state, ok := states[workspace]; ok {
				return state, nil
			}
			state := initialFamilies[0]
			initialFamilies = initialFamilies[1:]
			states[workspace] = state
			initialStates[workspace] = state
			return state, nil
		},
	})
	defer executor.Close()

	scenario := lineageScenario{Name: rollingEmbeddedScenario, Mode: "dolt-embedded", Start: "v0.63.0", End: "v1.1.2"}
	left, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: scenario, Version: "v0.63.0", Binary: "/historical/source", FamilyID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	right, err := executor.Retain(context.Background(), rollingDoltSource{Scenario: scenario, Version: "v0.63.0", Binary: "/historical/source", FamilyID: second.ID})
	if err != nil {
		t.Fatal(err)
	}
	executor.BeginBatch()
	if _, _, err := executor.Advance(context.Background(), left, rollingDoltTarget{Version: "v0.63.1", Binary: "/historical/target"}); err == nil {
		t.Fatal("expected the lexically first target to report its mutating failure")
	}
	if _, _, err := executor.Advance(context.Background(), right, rollingDoltTarget{Version: "v0.63.1", Binary: "/historical/target"}); err != nil {
		t.Fatal(err)
	}
	executor.EndBatch()
	if got := executor.WorkspaceCount(); got != 2 {
		t.Fatalf("retained workspace count = %d, want second and third post-target states", got)
	}
	executor.BeginBatch()
	if _, _, err := executor.Advance(context.Background(), left, rollingDoltTarget{Version: "v0.63.2", Binary: "/historical/target"}); err != nil {
		t.Fatalf("second post-target frontier was discarded: %v", err)
	}
	if _, _, err := executor.Advance(context.Background(), right, rollingDoltTarget{Version: "v0.63.2", Binary: "/historical/target"}); err != nil {
		t.Fatalf("third post-target frontier was discarded: %v", err)
	}
	executor.EndBatch()
}

func TestRollingDoltInitArgsCoverLegacyServerAndEmbedded(t *testing.T) {
	for _, test := range []struct {
		name     string
		scenario lineageScenario
		version  string
		want     []string
	}{
		{"legacy", lineageScenario{Name: rollingLegacyScenario, Mode: "dolt-legacy"}, "v0.50.3", []string{"init", "--backend", "dolt", "--prefix", "census"}},
		{"server", lineageScenario{Name: rollingServerScenario, Mode: "dolt-server"}, "v0.50.0", []string{"init", "--prefix", "census", "--server", "--server-host", "127.0.0.1", "--server-port", "45123", "--server-user", "root"}},
		{"embedded", lineageScenario{Name: rollingEmbeddedScenario, Mode: "dolt-embedded"}, "v0.63.0", []string{"init", "--prefix", "census"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := rollingDoltInitArgs(test.scenario, test.version, "census", 45123)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("args = %q, want %q", got, test.want)
			}
		})
	}
}
