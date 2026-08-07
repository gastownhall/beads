package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type rollingDoltFrontierExecutor interface {
	Retain(context.Context, rollingDoltSource) (*retainedDoltFrontier, error)
	Advance(context.Context, *retainedDoltFrontier, rollingDoltTarget) (lineageTransition, family, error)
	BeginBatch()
	EndBatch() map[string]*retainedDoltFrontier
	Close() error
}

type rollingDoltRuntime struct {
	acquire     func(context.Context, catalogEntry, string, string, acquisition) (string, error)
	newExecutor func() rollingDoltFrontierExecutor
}

// generateRollingDoltLineage observes real in-place migrations for every
// rolling Dolt mode. It shares a release binary across all active modes and
// retains only one workspace for every currently distinct family.
func generateRollingDoltLineage(ctx context.Context, releases catalog, fresh census, cache string) (lineageSet, []family, error) {
	return generateRollingDoltLineageWithRuntime(ctx, releases, fresh, cache, defaultRollingDoltRuntime())
}

func generateRollingDoltLineageWithRuntime(ctx context.Context, releases catalog, fresh census, cache string, runtime rollingDoltRuntime) (lineages lineageSet, rollingFamilies []family, err error) {
	if runtime.acquire == nil || runtime.newExecutor == nil {
		return lineageSet{}, nil, fmt.Errorf("rolling Dolt runtime is incomplete")
	}
	freshByVersion, acquisitions, allFamilies, err := freshDoltFamiliesByVersion(fresh)
	if err != nil {
		return lineageSet{}, nil, err
	}
	scenarios, err := rollingDoltScenarios()
	if err != nil {
		return lineageSet{}, nil, err
	}
	executor := runtime.newExecutor()
	if executor == nil {
		return lineageSet{}, nil, fmt.Errorf("rolling Dolt runtime has no executor")
	}
	defer func() {
		if closeErr := executor.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close rolling Dolt executor: %w", closeErr))
		}
	}()

	pass := &rollingDoltPass{ctx: ctx, executor: executor, acquire: runtime.acquire, cache: cache, allFamilies: allFamilies, rollingOnly: make(map[string]family)}
	frontiers := make(map[string]map[string]*retainedDoltFrontier, len(scenarios))
	for _, entry := range releases.Versions {
		for _, scenario := range scenarios {
			if !scenario.compatible(entry.Version) {
				continue
			}
			if len(frontiers[scenario.Name]) == 0 && len(freshByVersion[entry.Version][scenario.Name]) == 0 {
				continue
			}
			next, stepErr := pass.stepScenario(scenario, entry, frontiers[scenario.Name], freshByVersion[entry.Version][scenario.Name], acquisitions[entry.Version])
			if stepErr != nil {
				return lineageSet{}, nil, stepErr
			}
			frontiers[scenario.Name] = next
		}
	}
	sortLineageTransitions(pass.transitions)
	sortLineageOutcomes(pass.outcomes)
	families := make([]family, 0, len(pass.rollingOnly))
	for _, candidate := range pass.rollingOnly {
		families = append(families, candidate)
	}
	sort.Slice(families, func(i, j int) bool { return families[i].ID < families[j].ID })
	return lineageSet{SchemaVersion: lineageSchemaVersion, Transitions: pass.transitions, Outcomes: pass.outcomes}, families, nil
}

// rollingDoltPass carries the mutable accumulators for one rolling Dolt lineage
// generation so the release/scenario/family nesting can be split into focused
// steps without changing observation, ordering, or merge semantics.
type rollingDoltPass struct {
	ctx         context.Context
	executor    rollingDoltFrontierExecutor
	acquire     func(context.Context, catalogEntry, string, string, acquisition) (string, error)
	cache       string
	allFamilies map[string]family
	rollingOnly map[string]family
	transitions []lineageTransition
	outcomes    []lineageOutcome
}

// stepScenario advances one rolling scenario across one release version and
// returns the retained frontier for the next version.
func (p *rollingDoltPass) stepScenario(scenario lineageScenario, entry catalogEntry, current map[string]*retainedDoltFrontier, expectedFresh []family, recorded map[string]acquisition) (map[string]*retainedDoltFrontier, error) {
	targetRuntime, err := rollingDoltTargetRuntime(scenario, entry.Version)
	if err != nil {
		return nil, err
	}
	acquired, err := rollingDoltTargetAcquisition(entry.Version, targetRuntime, recorded)
	if err != nil {
		return nil, err
	}
	binary, err := p.acquire(p.ctx, entry, p.cache, targetRuntime.Mode, acquired)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: acquire recorded binary: %w", entry.Version, scenario.Name, err)
	}
	next := make(map[string]*retainedDoltFrontier, len(current))
	p.executor.BeginBatch()
	for _, familyID := range sortedDoltFrontierIDs(current) {
		if err := p.advanceFamily(scenario, entry, targetRuntime, acquired, binary, current[familyID], familyID, next); err != nil {
			p.executor.EndBatch()
			return nil, err
		}
	}
	if canonical := p.executor.EndBatch(); canonical != nil {
		next = make(map[string]*retainedDoltFrontier, len(canonical))
		for _, frontier := range canonical {
			if frontier.Scenario.Name == scenario.Name {
				next[frontier.FamilyID] = frontier
			}
		}
	}
	if err := p.retainFreshFamilies(scenario, entry, binary, expectedFresh, next); err != nil {
		return nil, err
	}
	return next, nil
}

// advanceFamily advances one retained family frontier by one release version,
// recording either a lineage transition or a mutation/refusal outcome and
// merging the observed family into next.
func (p *rollingDoltPass) advanceFamily(scenario lineageScenario, entry catalogEntry, targetRuntime lineageScenario, acquired acquisition, binary string, frontier *retainedDoltFrontier, familyID string, next map[string]*retainedDoltFrontier) error {
	transition, observed, err := p.executor.Advance(p.ctx, frontier, rollingDoltTarget{Version: entry.Version, Binary: binary, Runtime: targetRuntime})
	if err != nil {
		if observed.ID == "" || !validMode(observed.Mode) {
			return fmt.Errorf("%s: advance %s family %s: %w", entry.Version, scenario.Name, familyID, err)
		}
		p.recordFailureOutcome(scenario, entry, targetRuntime, acquired, familyID, observed)
		p.mergeObserved(next, observed.ID, frontier)
		return nil
	}
	transition.Acquisition = acquired
	transition.RuntimeMode = targetRuntime.Mode
	p.transitions = append(p.transitions, transition)
	p.noteRollingOnly(observed)
	p.mergeObserved(next, observed.ID, frontier)
	return nil
}

// recordFailureOutcome appends the unchanged-refusal or mutating-failure outcome
// for a family whose advance returned an error but a valid observed family.
func (p *rollingDoltPass) recordFailureOutcome(scenario lineageScenario, entry catalogEntry, targetRuntime lineageScenario, acquired acquisition, familyID string, observed family) {
	outcome := lineageOutcome{
		FromFamilyID: familyID, TargetVersion: entry.Version, Scenario: scenario.Name,
		Mode: scenario.Mode, RuntimeMode: targetRuntime.Mode, Acquisition: acquired,
	}
	if observed.ID == familyID {
		outcome.Outcome = lineageOutcomeUnchangedRefusal
	} else {
		outcome.Outcome = lineageOutcomeMutatingFailure
		outcome.ToFamilyID = observed.ID
		p.noteRollingOnly(observed)
	}
	p.outcomes = append(p.outcomes, outcome)
}

// retainFreshFamilies retains a workspace for every fresh family of this
// scenario/version that no advance already produced.
func (p *rollingDoltPass) retainFreshFamilies(scenario lineageScenario, entry catalogEntry, binary string, expected []family, next map[string]*retainedDoltFrontier) error {
	for _, candidate := range expected {
		if _, exists := next[candidate.ID]; exists {
			continue
		}
		frontier, err := p.executor.Retain(p.ctx, rollingDoltSource{Scenario: scenario, Version: entry.Version, Binary: binary, FamilyID: candidate.ID})
		if err != nil {
			return fmt.Errorf("%s: retain fresh %s family %s: %w", entry.Version, scenario.Name, candidate.ID, err)
		}
		next[candidate.ID] = frontier
	}
	return nil
}

// noteRollingOnly records a family first observed during rolling migration so it
// is emitted alongside the fresh families.
func (p *rollingDoltPass) noteRollingOnly(observed family) {
	if _, known := p.allFamilies[observed.ID]; !known {
		p.rollingOnly[observed.ID] = observed
	}
}

// mergeObserved keeps the first frontier retained for an observed family within
// the batch, matching the original left-to-right merge.
func (p *rollingDoltPass) mergeObserved(next map[string]*retainedDoltFrontier, observedID string, frontier *retainedDoltFrontier) {
	if _, merged := next[observedID]; !merged {
		next[observedID] = frontier
	}
}

// rollingDoltTargetAcquisition chooses the authenticated executable for one
// target attempt. v0.49.5 deliberately lacks a fresh legacy observation, but
// its rolling legacy frontier must still run the source-built target recorded
// by its supported Dolt scenario.
func rollingDoltTargetRuntime(scenario lineageScenario, version string) (lineageScenario, error) {
	if scenario.Name != rollingLegacyScenario {
		return scenario, nil
	}
	if version == "v0.49.5" {
		return lineageScenarioMapMust(rollingServerScenario), nil
	}
	if before, err := versionBefore(version, "v0.56.0"); err != nil {
		return lineageScenario{}, err
	} else if before {
		return scenario, nil
	}
	if before, err := versionBefore(version, "v0.63.0"); err != nil {
		return lineageScenario{}, err
	} else if before {
		return lineageScenarioMapMust(rollingServerScenario), nil
	}
	return lineageScenarioMapMust(rollingEmbeddedScenario), nil
}

func lineageScenarioMapMust(name string) lineageScenario {
	scenarios, err := lineageScenarioMap()
	if err != nil {
		panic(err)
	}
	return scenarios[name]
}

func rollingDoltTargetAcquisition(version string, targetRuntime lineageScenario, recorded map[string]acquisition) (acquisition, error) {
	if acquired, ok := recorded[targetRuntime.Name]; ok {
		return acquired, nil
	}
	return acquisition{}, fmt.Errorf("%s/%s: no authenticated target acquisition", version, targetRuntime.Name)
}

func requireRollingDoltTargetAcquisition(recorded map[string]map[string]acquisition, version, scenarioName string, candidate acquisition) error {
	scenarios, err := lineageScenarioMap()
	if err != nil {
		return err
	}
	scenario, ok := scenarios[scenarioName]
	if !ok {
		return fmt.Errorf("%s: unknown rolling scenario %q", version, scenarioName)
	}
	targetRuntime, err := rollingDoltTargetRuntime(scenario, version)
	if err != nil {
		return err
	}
	expected, err := rollingDoltTargetAcquisition(version, targetRuntime, recorded[version])
	if err != nil {
		return err
	}
	if expected != candidate {
		return fmt.Errorf("%s/%s: rolling acquisition differs from authenticated target acquisition", version, scenarioName)
	}
	return nil
}

func defaultRollingDoltRuntime() rollingDoltRuntime {
	return rollingDoltRuntime{acquire: acquireRecordedBinary, newExecutor: func() rollingDoltFrontierExecutor {
		return newRollingDoltLineageExecutor(rollingDoltLineageConfig{})
	}}
}

func rollingDoltScenarios() ([]lineageScenario, error) {
	all, err := lineageScenarioMap()
	if err != nil {
		return nil, err
	}
	result := make([]lineageScenario, 0, 3)
	for _, name := range []string{rollingLegacyScenario, rollingServerScenario, rollingEmbeddedScenario} {
		scenario, ok := all[name]
		if !ok {
			return nil, fmt.Errorf("Dolt rolling lineage scenario %q is unavailable", name)
		}
		result = append(result, scenario)
	}
	return result, nil
}

func freshDoltFamiliesByVersion(result census) (map[string]map[string][]family, map[string]map[string]acquisition, map[string]family, error) {
	families := make(map[string]family, len(result.Families))
	for _, candidate := range result.Families {
		families[candidate.ID] = candidate
	}
	byVersion := make(map[string]map[string][]family)
	acquisitions := make(map[string]map[string]acquisition)
	seen := make(map[string]bool)
	for _, observed := range result.Observations {
		if !strings.HasPrefix(observed.Scenario, "fresh-") {
			continue
		}
		candidate, ok := families[observed.FamilyID]
		if !ok {
			return nil, nil, nil, fmt.Errorf("%s/%s: fresh observation has no family", observed.Version, observed.Scenario)
		}
		rollingScenario, ok := rollingDoltScenarioForFreshScenario(observed.Scenario)
		if !ok {
			continue
		}
		key := observed.Version + "\x00" + rollingScenario + "\x00" + candidate.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		if byVersion[observed.Version] == nil {
			byVersion[observed.Version] = make(map[string][]family)
			acquisitions[observed.Version] = make(map[string]acquisition)
		}
		if prior, exists := acquisitions[observed.Version][rollingScenario]; exists && prior != observed.Acquisition {
			return nil, nil, nil, fmt.Errorf("%s/%s: fresh Dolt acquisition differs by observation", observed.Version, rollingScenario)
		}
		acquisitions[observed.Version][rollingScenario] = observed.Acquisition
		byVersion[observed.Version][rollingScenario] = append(byVersion[observed.Version][rollingScenario], candidate)
	}
	for _, scenarios := range byVersion {
		for scenario := range scenarios {
			sort.Slice(scenarios[scenario], func(i, j int) bool { return scenarios[scenario][i].ID < scenarios[scenario][j].ID })
		}
	}
	return byVersion, acquisitions, families, nil
}

func rollingDoltScenarioForFreshScenario(name string) (string, bool) {
	switch name {
	case freshDoltLegacyScenario:
		return rollingLegacyScenario, true
	case freshDoltServerScenario:
		return rollingServerScenario, true
	case freshDoltEmbeddedScenario:
		return rollingEmbeddedScenario, true
	default:
		return "", false
	}
}

func isRollingDoltMode(mode string) bool {
	switch mode {
	case "dolt-legacy", "dolt-server", "dolt-embedded":
		return true
	default:
		return false
	}
}

func sortedDoltFrontierIDs(frontier map[string]*retainedDoltFrontier) []string {
	ids := make([]string, 0, len(frontier))
	for familyID := range frontier {
		ids = append(ids, familyID)
	}
	sort.Strings(ids)
	return ids
}
