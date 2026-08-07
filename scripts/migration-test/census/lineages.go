package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	lineageSchemaVersion = 1

	rollingSQLiteScenario   = "rolling-sqlite"
	rollingLegacyScenario   = "rolling-dolt-legacy"
	rollingServerScenario   = "rolling-dolt-server"
	rollingEmbeddedScenario = "rolling-dolt-embedded"
)

// lineageSet records schema-family transitions observed by rolling an existing
// historical workspace forward one catalog release at a time. A transition is
// deliberately family-based: release-specific migration adapters belong in the
// runner, not this corpus.
type lineageSet struct {
	SchemaVersion int                 `json:"schema_version"`
	Transitions   []lineageTransition `json:"transitions"`
	Outcomes      []lineageOutcome    `json:"outcomes"`
}

type lineageTransition struct {
	FromFamilyID  string      `json:"from_family_id"`
	TargetVersion string      `json:"target_version"`
	Scenario      string      `json:"scenario"`
	Mode          string      `json:"mode"`
	RuntimeMode   string      `json:"runtime_mode"`
	ToFamilyID    string      `json:"to_family_id"`
	Acquisition   acquisition `json:"acquisition"`
}

const (
	lineageOutcomeUnchangedRefusal = "unchanged-refusal"
	lineageOutcomeMutatingFailure  = "mutating-failure"
)

// lineageOutcome records a real rolling attempt that did not complete as a
// successful migration. It deliberately contains no diagnostic text: command
// failures are volatile, while the retained source or partial target family is
// deterministic and sufficient to reconstruct the next frontier.
type lineageOutcome struct {
	FromFamilyID  string      `json:"from_family_id"`
	TargetVersion string      `json:"target_version"`
	Scenario      string      `json:"scenario"`
	Mode          string      `json:"mode"`
	RuntimeMode   string      `json:"runtime_mode"`
	Outcome       string      `json:"outcome"`
	ToFamilyID    string      `json:"to_family_id,omitempty"`
	Acquisition   acquisition `json:"acquisition"`
}

type lineageScenario struct {
	Name  string
	Mode  string
	Start string // Inclusive; empty means the first catalog release.
	End   string // Inclusive; empty means the final catalog release.
}

// rollingLineageScenarios is the bounded historical release universe. JSONL
// is deliberately absent: it is an export/import route, rather than an
// in-place rolling storage lineage.
var rollingLineageScenarios = []lineageScenario{
	{Name: rollingSQLiteScenario, Mode: "sqlite", End: "v1.1.2"},
	{Name: rollingLegacyScenario, Mode: "dolt-legacy", Start: "v0.47.2", End: "v1.1.2"},
	{Name: rollingServerScenario, Mode: "dolt-server", Start: "v0.49.1", End: "v1.1.2"},
	{Name: rollingEmbeddedScenario, Mode: "dolt-embedded", Start: "v0.63.0", End: "v1.1.2"},
}

func validateTransitionRecords(
	transitions []lineageTransition,
	families map[string]family,
	versions map[string]int,
	scenarios map[string]lineageScenario,
) error {
	seen := make(map[string]bool, len(transitions))
	for index, transition := range transitions {
		if index > 0 && compareLineageTransitions(transitions[index-1], transition) >= 0 {
			return errors.New("lineage transitions are not canonically sorted")
		}
		scenario, ok := scenarios[transition.Scenario]
		if !ok {
			return fmt.Errorf("transition has unknown scenario %q", transition.Scenario)
		}
		if transition.Mode != scenario.Mode {
			return fmt.Errorf("transition %q/%q has mode %q, want %q", transition.FromFamilyID, transition.TargetVersion, transition.Mode, scenario.Mode)
		}
		if _, ok := versions[transition.TargetVersion]; !ok {
			return fmt.Errorf("transition has unknown target version %q", transition.TargetVersion)
		}
		if !scenario.compatible(transition.TargetVersion) {
			return fmt.Errorf("transition target version %q is outside %s", transition.TargetVersion, scenario.Name)
		}
		runtimeMode, err := rollingTargetRuntimeMode(scenario, transition.TargetVersion)
		if err != nil || transition.RuntimeMode != runtimeMode {
			return fmt.Errorf("transition %q/%q has runtime mode %q, want %q", transition.FromFamilyID, transition.TargetVersion, transition.RuntimeMode, runtimeMode)
		}
		from, ok := families[transition.FromFamilyID]
		if !ok {
			return fmt.Errorf("transition has unknown source family %q", transition.FromFamilyID)
		}
		if !rollingLineageFamilyModeAllowed(scenario, from.Mode) {
			return fmt.Errorf("transition has source family mode %q outside %s", from.Mode, scenario.Name)
		}
		to, ok := families[transition.ToFamilyID]
		if !ok {
			return fmt.Errorf("transition has unknown target family %q", transition.ToFamilyID)
		}
		if !rollingLineageFamilyModeAllowed(scenario, to.Mode) {
			return fmt.Errorf("transition has target family mode %q outside %s", to.Mode, scenario.Name)
		}
		key := transition.TargetVersion + "\x00" + transition.Scenario + "\x00" + transition.FromFamilyID
		if seen[key] {
			return fmt.Errorf("duplicate transition for %s", transition.TargetVersion)
		}
		seen[key] = true
	}
	return nil
}

func validateOutcomeRecords(
	outcomes []lineageOutcome,
	families map[string]family,
	versions map[string]int,
	scenarios map[string]lineageScenario,
) error {
	seen := make(map[string]bool, len(outcomes))
	for index, outcome := range outcomes {
		if index > 0 && compareLineageOutcomes(outcomes[index-1], outcome) >= 0 {
			return errors.New("lineage outcomes are not canonically sorted")
		}
		scenario, ok := scenarios[outcome.Scenario]
		if !ok || outcome.Mode != scenario.Mode {
			return fmt.Errorf("outcome has invalid scenario/mode %q/%q", outcome.Scenario, outcome.Mode)
		}
		if _, ok := versions[outcome.TargetVersion]; !ok || !scenario.compatible(outcome.TargetVersion) {
			return fmt.Errorf("outcome has invalid target version %q", outcome.TargetVersion)
		}
		runtimeMode, err := rollingTargetRuntimeMode(scenario, outcome.TargetVersion)
		if err != nil || outcome.RuntimeMode != runtimeMode {
			return fmt.Errorf("outcome has invalid runtime mode %q", outcome.RuntimeMode)
		}
		from, ok := families[outcome.FromFamilyID]
		if !ok {
			return fmt.Errorf("outcome has invalid source family %q", outcome.FromFamilyID)
		}
		if !rollingLineageFamilyModeAllowed(scenario, from.Mode) {
			return fmt.Errorf("outcome has source family mode %q outside %s", from.Mode, scenario.Name)
		}
		switch outcome.Outcome {
		case lineageOutcomeUnchangedRefusal:
			if outcome.ToFamilyID != "" {
				return errors.New("unchanged refusal has a target family")
			}
		case lineageOutcomeMutatingFailure:
			to, ok := families[outcome.ToFamilyID]
			if !ok {
				return fmt.Errorf("mutating failure has invalid target family %q", outcome.ToFamilyID)
			}
			if !rollingLineageFamilyModeAllowed(scenario, to.Mode) {
				return fmt.Errorf("mutating failure has target family mode %q outside %s", to.Mode, scenario.Name)
			}
			if outcome.ToFamilyID == outcome.FromFamilyID {
				return errors.New("mutating failure has unchanged target family")
			}
		default:
			return fmt.Errorf("outcome has unknown kind %q", outcome.Outcome)
		}
		key := lineageRecordKey(outcome.TargetVersion, outcome.Scenario, outcome.FromFamilyID)
		if seen[key] {
			return fmt.Errorf("duplicate outcome for %s", outcome.TargetVersion)
		}
		seen[key] = true
	}
	return nil
}

// validateRollingLineageCoverage reconstructs every family frontier from the
// fresh observations and requires one recorded result for each later compatible
// release. An unchanged refusal retains the source family; a mutating failure
// advances to the fingerprinted partial family without masquerading as a
// successful migration.
func validateRollingLineageCoverage(result census, releases catalog, families map[string]family) error {
	if err := validateReleaseOrder(releases); err != nil {
		return err
	}
	scenarios, err := lineageScenarioMap()
	if err != nil {
		return err
	}
	versions := make(map[string]int, len(releases.Versions))
	for index, entry := range releases.Versions {
		versions[entry.Version] = index
	}
	if err := validateTransitionRecords(result.Transitions, families, versions, scenarios); err != nil {
		return err
	}
	if err := validateOutcomeRecords(result.Outcomes, families, versions, scenarios); err != nil {
		return err
	}
	transitions := make(map[string]lineageTransition, len(result.Transitions))
	for _, transition := range result.Transitions {
		transitions[lineageRecordKey(transition.TargetVersion, transition.Scenario, transition.FromFamilyID)] = transition
	}
	outcomes := make(map[string]lineageOutcome, len(result.Outcomes))
	for _, outcome := range result.Outcomes {
		key := lineageRecordKey(outcome.TargetVersion, outcome.Scenario, outcome.FromFamilyID)
		if _, duplicate := transitions[key]; duplicate {
			return fmt.Errorf("transition and outcome both record %s", outcome.TargetVersion)
		}
		outcomes[key] = outcome
	}
	fresh := make(map[string]map[string]map[string]struct{})
	for _, observed := range result.Observations {
		if !strings.HasPrefix(observed.Scenario, "fresh-") {
			continue
		}
		scenarioName, ok := rollingScenarioForFreshScenario(observed.Scenario)
		if !ok || !scenarios[scenarioName].compatible(observed.Version) {
			continue
		}
		if fresh[observed.Version] == nil {
			fresh[observed.Version] = make(map[string]map[string]struct{})
		}
		if fresh[observed.Version][scenarioName] == nil {
			fresh[observed.Version][scenarioName] = make(map[string]struct{})
		}
		fresh[observed.Version][scenarioName][observed.FamilyID] = struct{}{}
	}
	frontiers := make(map[string]map[string]struct{}, len(scenarios))
	used := make(map[string]bool, len(transitions)+len(outcomes))
	for _, entry := range releases.Versions {
		next := make(map[string]map[string]struct{}, len(scenarios))
		for name, scenario := range scenarios {
			if !scenario.compatible(entry.Version) {
				continue
			}
			targets := make(map[string]struct{})
			for from := range frontiers[name] {
				key := lineageRecordKey(entry.Version, name, from)
				if transition, ok := transitions[key]; ok {
					targets[transition.ToFamilyID] = struct{}{}
					used[key] = true
					continue
				}
				outcome, ok := outcomes[key]
				if !ok {
					return fmt.Errorf("missing rolling result for %s from %s in %s", entry.Version, from, name)
				}
				if outcome.Outcome == lineageOutcomeUnchangedRefusal {
					targets[from] = struct{}{}
				} else {
					targets[outcome.ToFamilyID] = struct{}{}
				}
				used[key] = true
			}
			for familyID := range fresh[entry.Version][name] {
				targets[familyID] = struct{}{}
			}
			next[name] = targets
		}
		frontiers = next
	}
	for key := range transitions {
		if !used[key] {
			return errors.New("orphan rolling transition")
		}
	}
	for key := range outcomes {
		if !used[key] {
			return errors.New("orphan rolling outcome")
		}
	}
	return nil
}

func rollingScenarioForFreshScenario(name string) (string, bool) {
	switch name {
	case freshSQLiteScenario:
		return rollingSQLiteScenario, true
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

func rollingTargetRuntimeMode(scenario lineageScenario, version string) (string, error) {
	if isRollingDoltMode(scenario.Mode) {
		runtime, err := rollingDoltTargetRuntime(scenario, version)
		if err != nil {
			return "", err
		}
		return runtime.Mode, nil
	}
	return scenario.Mode, nil
}

func rollingLineageFamilyModeAllowed(scenario lineageScenario, mode string) bool {
	if scenario.Mode == "sqlite" {
		return mode == "sqlite"
	}
	if isRollingDoltMode(scenario.Mode) {
		return mode == "dolt-legacy" || mode == "dolt-server" || mode == "dolt-embedded"
	}
	return false
}

func lineageRecordKey(version, scenario, from string) string {
	return version + "\x00" + scenario + "\x00" + from
}

func validateReleaseOrder(releases catalog) error {
	for index, entry := range releases.Versions {
		if _, err := parseReleaseVersion(entry.Version); err != nil {
			return err
		}
		if index > 0 && compareReleaseVersions(releases.Versions[index-1].Version, entry.Version) >= 0 {
			return errors.New("catalog releases are not in ascending semantic-version order")
		}
	}
	return nil
}

func lineageScenarioMap() (map[string]lineageScenario, error) {
	result := make(map[string]lineageScenario, len(rollingLineageScenarios))
	for _, scenario := range rollingLineageScenarios {
		if scenario.Name == "" || !validMode(scenario.Mode) || scenario.Mode == "jsonl" {
			return nil, errors.New("rolling lineage scenario is invalid")
		}
		if _, exists := result[scenario.Name]; exists {
			return nil, fmt.Errorf("duplicate rolling lineage scenario %q", scenario.Name)
		}
		if _, err := parseReleaseVersion(scenario.Start); err != nil && scenario.Start != "" {
			return nil, err
		}
		if _, err := parseReleaseVersion(scenario.End); err != nil && scenario.End != "" {
			return nil, err
		}
		if scenario.Start != "" && scenario.End != "" && compareReleaseVersions(scenario.Start, scenario.End) > 0 {
			return nil, fmt.Errorf("rolling lineage scenario %q has inverted interval", scenario.Name)
		}
		result[scenario.Name] = scenario
	}
	return result, nil
}

func (scenario lineageScenario) compatible(version string) bool {
	if scenario.Start != "" && compareReleaseVersions(version, scenario.Start) < 0 {
		return false
	}
	return scenario.End == "" || compareReleaseVersions(version, scenario.End) <= 0
}

type releaseVersion [3]int

func parseReleaseVersion(value string) (releaseVersion, error) {
	if value == "" {
		return releaseVersion{}, nil
	}
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if !strings.HasPrefix(value, "v") || len(parts) != 3 {
		return releaseVersion{}, fmt.Errorf("invalid stable release version %q", value)
	}
	var result releaseVersion
	for index, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 || strconv.Itoa(parsed) != part {
			return releaseVersion{}, fmt.Errorf("invalid stable release version %q", value)
		}
		result[index] = parsed
	}
	return result, nil
}

func compareReleaseVersions(left, right string) int {
	leftVersion, leftErr := parseReleaseVersion(left)
	rightVersion, rightErr := parseReleaseVersion(right)
	if leftErr != nil || rightErr != nil {
		panic("compareReleaseVersions requires valid stable release versions")
	}
	for index := range leftVersion {
		if leftVersion[index] < rightVersion[index] { //nolint:gosec // both operands are the same fixed-length releaseVersion array.
			return -1
		}
		if leftVersion[index] > rightVersion[index] { //nolint:gosec // both operands are the same fixed-length releaseVersion array.
			return 1
		}
	}
	return 0
}

func compareLineageTransitions(left, right lineageTransition) int {
	for _, pair := range [][2]string{
		{left.FromFamilyID, right.FromFamilyID},
		{left.TargetVersion, right.TargetVersion},
		{left.Scenario, right.Scenario},
		{left.Mode, right.Mode},
		{left.RuntimeMode, right.RuntimeMode},
		{left.ToFamilyID, right.ToFamilyID},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func sortLineageTransitions(transitions []lineageTransition) {
	sort.Slice(transitions, func(i, j int) bool {
		return compareLineageTransitions(transitions[i], transitions[j]) < 0
	})
}

func compareLineageOutcomes(left, right lineageOutcome) int {
	for _, pair := range [][2]string{
		{left.FromFamilyID, right.FromFamilyID},
		{left.TargetVersion, right.TargetVersion},
		{left.Scenario, right.Scenario},
		{left.Mode, right.Mode},
		{left.RuntimeMode, right.RuntimeMode},
		{left.Outcome, right.Outcome},
		{left.ToFamilyID, right.ToFamilyID},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

func sortLineageOutcomes(outcomes []lineageOutcome) {
	sort.Slice(outcomes, func(i, j int) bool {
		return compareLineageOutcomes(outcomes[i], outcomes[j]) < 0
	})
}
