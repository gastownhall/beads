package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	freshSQLiteScenario       = "fresh-sqlite"
	freshDoltLegacyScenario   = "fresh-dolt-legacy"
	freshDoltServerScenario   = "fresh-dolt-server"
	freshDoltEmbeddedScenario = "fresh-dolt-embedded"
)

// freshDefaultJSONLVersions records the two release-default observations that
// remain outside the authenticated-source capability matrix.
var freshDefaultJSONLVersions = []string{"v0.50.2", "v0.50.3"}

// freshScenarioRules are the authenticated-source storage capabilities. They
// deliberately describe capability intervals, rather than individual releases.
var freshScenarioRules = []scenarioSpec{
	{Name: freshSQLiteScenario, Mode: "sqlite", First: "v0.9.1", Last: "v0.50.3"},
	{Name: freshDoltLegacyScenario, Mode: "dolt-legacy", First: "v0.47.2", Last: "v0.55.4", Excluded: []string{"v0.49.5"}},
	{Name: freshDoltServerScenario, Mode: "dolt-server", First: "v0.49.1", Last: "v1.1.2"},
	{Name: freshDoltEmbeddedScenario, Mode: "dolt-embedded", First: "v0.63.0", Last: "v1.1.2"},
}

type scenarioSpec struct {
	Name  string
	Mode  string
	First string
	Last  string
	// Excluded records shipped capability holes inside an otherwise stable
	// interval. v0.49.5 was server-only; v0.49.6 restored local Dolt.
	Excluded []string
}

func freshScenarioByName(name string) (scenarioSpec, bool) {
	for _, scenario := range freshScenarioRules {
		if scenario.Name == name {
			return scenario, true
		}
	}
	return scenarioSpec{}, false
}

func expectedFreshScenarioKeys(c catalog) (map[string]scenarioSpec, error) {
	expected := make(map[string]scenarioSpec)
	for _, entry := range c.Versions {
		scenarios, err := freshScenariosForVersion(entry.Version)
		if err != nil {
			return nil, err
		}
		for _, scenario := range scenarios {
			key := scenarioKey(entry.Version, scenario.Name)
			if _, exists := expected[key]; exists {
				return nil, fmt.Errorf("duplicate expected scenario %q/%q", entry.Version, scenario.Name)
			}
			expected[key] = scenario
		}
	}
	return expected, nil
}

func expectedFreshDefaultJSONLVersions(c catalog) map[string]bool {
	present := make(map[string]bool, len(c.Versions))
	for _, entry := range c.Versions {
		present[entry.Version] = true
	}
	expected := make(map[string]bool, len(freshDefaultJSONLVersions))
	for _, version := range freshDefaultJSONLVersions {
		if present[version] {
			expected[version] = true
		}
	}
	return expected
}

func isFreshDefaultJSONLVersion(version string) bool {
	for _, allowed := range freshDefaultJSONLVersions {
		if version == allowed {
			return true
		}
	}
	return false
}

func freshScenariosForVersion(version string) ([]scenarioSpec, error) {
	result := make([]scenarioSpec, 0, len(freshScenarioRules))
	for _, scenario := range freshScenarioRules {
		matches, err := scenario.includes(version)
		if err != nil {
			return nil, fmt.Errorf("%s: evaluate %s: %w", version, scenario.Name, err)
		}
		if matches {
			result = append(result, scenario)
		}
	}
	return result, nil
}

// validateFreshScenarioCoverage checks the required source-mode matrix only.
// A release-default observation is deliberately outside this matrix: release
// assets may expose an additional usable layout (such as JSONL) that is not a
// source-mode capability and must be retained as its own observed family.
func validateFreshScenarioCoverage(observations []observation, c catalog) error {
	expected, err := expectedFreshScenarioKeys(c)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(expected))
	for _, observed := range observations {
		if _, isFreshScenario := freshScenarioByName(observed.Scenario); !isFreshScenario {
			continue
		}
		key := scenarioKey(observed.Version, observed.Scenario)
		if _, required := expected[key]; !required {
			return fmt.Errorf("unexpected fresh scenario %q/%q", observed.Version, observed.Scenario)
		}
		if seen[key] {
			return fmt.Errorf("duplicate fresh scenario %q/%q", observed.Version, observed.Scenario)
		}
		seen[key] = true
	}
	for key := range expected {
		if !seen[key] {
			return fmt.Errorf("missing required fresh scenario %q", key)
		}
	}
	return nil
}

func validateFreshDefaultJSONLCoverage(observations []observation, c catalog) error {
	expected := expectedFreshDefaultJSONLVersions(c)
	observed := make(map[string]string, len(expected))
	for _, observation := range observations {
		if observation.Scenario != freshScenario {
			continue
		}
		if !expected[observation.Version] {
			return fmt.Errorf("unexpected JSONL fresh-default observation for %q", observation.Version)
		}
		if _, exists := observed[observation.Version]; exists {
			return fmt.Errorf("duplicate JSONL fresh-default observation for %q", observation.Version)
		}
		observed[observation.Version] = observation.FamilyID
	}
	var familyID string
	for _, version := range freshDefaultJSONLVersions {
		if !expected[version] {
			continue
		}
		observedID, ok := observed[version]
		if !ok {
			return fmt.Errorf("missing required JSONL fresh-default observation for %q", version)
		}
		if familyID == "" {
			familyID = observedID
		} else if observedID != familyID {
			return errors.New("JSONL fresh-default observations do not share one family")
		}
	}
	return nil
}

func scenarioKey(version, name string) string {
	return version + "\x00" + name
}

func (scenario scenarioSpec) includes(version string) (bool, error) {
	for _, excluded := range scenario.Excluded {
		if version == excluded {
			return false, nil
		}
	}
	got, err := parseStableVersion(version)
	if err != nil {
		return false, err
	}
	first, err := parseStableVersion(scenario.First)
	if err != nil {
		return false, fmt.Errorf("invalid first boundary %q: %w", scenario.First, err)
	}
	last, err := parseStableVersion(scenario.Last)
	if err != nil {
		return false, fmt.Errorf("invalid last boundary %q: %w", scenario.Last, err)
	}
	return first.compare(got) <= 0 && got.compare(last) <= 0, nil
}

// initArgs selects the smallest historical invocation that produces the
// requested mode. Beads exposed --backend only from v0.47.2 through v0.50.3,
// and the first server releases required --backend dolt alongside --server.
func (scenario scenarioSpec) initArgs(version, prefix string, serverPort int) ([]string, error) {
	if prefix == "" {
		return nil, fmt.Errorf("scenario %s requires a prefix", scenario.Name)
	}
	included, err := scenario.includes(version)
	if err != nil {
		return nil, err
	}
	if !included {
		return nil, fmt.Errorf("scenario %s does not include %s", scenario.Name, version)
	}
	args := []string{"init"}
	switch scenario.Name {
	case freshSQLiteScenario:
		if before, err := versionBefore(version, "v0.47.2"); err != nil {
			return nil, err
		} else if !before {
			args = append(args, "--backend", "sqlite")
		}
	case freshDoltLegacyScenario:
		if after, err := versionAfter(version, "v0.50.3"); err != nil {
			return nil, err
		} else if !after {
			args = append(args, "--backend", "dolt")
		}
	case freshDoltServerScenario:
		if serverPort < 1 || serverPort > 65535 {
			return nil, fmt.Errorf("invalid external Dolt server port %d", serverPort)
		}
		if before, err := versionBefore(version, "v0.50.0"); err != nil {
			return nil, err
		} else if before {
			args = append(args, "--backend", "dolt")
		}
	case freshDoltEmbeddedScenario:
	default:
		return nil, fmt.Errorf("unknown fresh scenario %q", scenario.Name)
	}
	args = append(args, "--prefix", prefix)
	if scenario.Name == freshDoltServerScenario {
		args = append(args,
			"--server",
			"--server-host", "127.0.0.1",
			"--server-port", strconv.Itoa(serverPort),
			"--server-user", "root",
		)
	}
	return args, nil
}

// supportedByInitHelp checks only the flags required by the selected
// historical cohort; default-mode cohorts require no mode-specific flag.
func (scenario scenarioSpec) supportedByInitHelp(version string, help []byte) bool {
	text := string(help)
	args, err := scenario.initArgs(version, "census", 1)
	if err != nil {
		return false
	}
	required := make(map[string]bool)
	for _, arg := range args {
		if strings.HasPrefix(arg, "--") && arg != "--prefix" {
			required[arg] = true
		}
	}
	for flag := range required {
		if !strings.Contains(text, flag) {
			return false
		}
	}
	return true
}

// bootstrapServerPort captures the v0.49.5 server-only regression: that
// release ignored its init endpoint flags and always connected to port 3307.
func (scenario scenarioSpec) bootstrapServerPort(version string) (int, error) {
	included, err := scenario.includes(version)
	if err != nil {
		return 0, err
	}
	if scenario.Name != freshDoltServerScenario || !included {
		return 0, fmt.Errorf("scenario %s cannot bootstrap a server for %s", scenario.Name, version)
	}
	if version == "v0.49.5" {
		return 3307, nil
	}
	return 0, nil
}

func versionBefore(version, boundary string) (bool, error) {
	got, err := parseStableVersion(version)
	if err != nil {
		return false, err
	}
	limit, err := parseStableVersion(boundary)
	if err != nil {
		return false, err
	}
	return got.compare(limit) < 0, nil
}

func versionAfter(version, boundary string) (bool, error) {
	got, err := parseStableVersion(version)
	if err != nil {
		return false, err
	}
	limit, err := parseStableVersion(boundary)
	if err != nil {
		return false, err
	}
	return got.compare(limit) > 0, nil
}

type stableVersion struct {
	major int
	minor int
	patch int
}

func parseStableVersion(raw string) (stableVersion, error) {
	if !strings.HasPrefix(raw, "v") {
		return stableVersion{}, fmt.Errorf("version must start with v")
	}
	parts := strings.Split(strings.TrimPrefix(raw, "v"), ".")
	if len(parts) != 3 {
		return stableVersion{}, fmt.Errorf("version must have major.minor.patch")
	}
	values := [3]int{}
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || strconv.Itoa(value) != part {
			return stableVersion{}, fmt.Errorf("invalid version component %q", part)
		}
		values[i] = value
	}
	return stableVersion{major: values[0], minor: values[1], patch: values[2]}, nil
}

func (left stableVersion) compare(right stableVersion) int {
	for _, values := range [][2]int{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if values[0] < values[1] {
			return -1
		}
		if values[0] > values[1] {
			return 1
		}
	}
	return 0
}
