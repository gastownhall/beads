package main

import (
	"strings"
	"testing"
)

func TestExpectedFreshScenarioKeysCoverCatalog(t *testing.T) {
	catalog, _, err := readCatalog("../release-catalog.json", false)
	if err != nil {
		t.Fatal(err)
	}

	expected, err := expectedFreshScenarioKeys(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(expected), 167; got != want {
		t.Fatalf("scenario key count = %d, want %d", got, want)
	}
	wantCounts := map[string]int{
		freshSQLiteScenario:       91,
		freshDoltLegacyScenario:   22,
		freshDoltServerScenario:   41,
		freshDoltEmbeddedScenario: 13,
	}
	gotCounts := make(map[string]int)
	for _, scenario := range expected {
		gotCounts[scenario.Name]++
	}
	for name, want := range wantCounts {
		if got := gotCounts[name]; got != want {
			t.Errorf("%s count = %d, want %d", name, got, want)
		}
	}
	for _, test := range []struct {
		version string
		name    string
		want    bool
	}{
		{"v0.9.1", freshSQLiteScenario, true},
		{"v0.50.3", freshSQLiteScenario, true},
		{"v0.51.0", freshSQLiteScenario, false},
		{"v0.47.2", freshDoltLegacyScenario, true},
		{"v0.49.5", freshDoltLegacyScenario, false},
		{"v0.49.6", freshDoltLegacyScenario, true},
		{"v0.55.4", freshDoltLegacyScenario, true},
		{"v0.56.0", freshDoltLegacyScenario, false},
		{"v0.49.1", freshDoltServerScenario, true},
		{"v1.1.2", freshDoltServerScenario, true},
		{"v0.49.0", freshDoltServerScenario, false},
		{"v0.63.0", freshDoltEmbeddedScenario, true},
		{"v0.62.0", freshDoltEmbeddedScenario, false},
	} {
		_, got := expected[scenarioKey(test.version, test.name)]
		if got != test.want {
			t.Errorf("expected[%s, %s] exists = %t, want %t", test.version, test.name, got, test.want)
		}
	}
}

func TestExpectedFreshDefaultJSONLVersionsCoverPinnedCatalog(t *testing.T) {
	catalog, _, err := readCatalog("../release-catalog.json", false)
	if err != nil {
		t.Fatal(err)
	}

	got := expectedFreshDefaultJSONLVersions(catalog)
	if len(got) != 2 {
		t.Fatalf("fresh-default JSONL version count = %d, want 2 (%v)", len(got), freshDefaultJSONLVersions)
	}
	for _, version := range freshDefaultJSONLVersions {
		if !got[version] {
			t.Errorf("fresh-default JSONL versions missing %s", version)
		}
	}
}

func TestValidateFreshScenarioCoverage(t *testing.T) {
	catalog := catalog{Versions: []catalogEntry{
		{Version: "v0.9.1"},
		{Version: "v0.47.2"},
		{Version: "v0.49.1"},
		{Version: "v0.63.0"},
		{Version: "v1.1.2"},
	}}
	expected, err := expectedFreshScenarioKeys(catalog)
	if err != nil {
		t.Fatal(err)
	}
	observations := make([]observation, 0, len(expected))
	for key, scenario := range expected {
		version, _, ok := strings.Cut(key, "\x00")
		if !ok {
			t.Fatalf("invalid test key %q", key)
		}
		observations = append(observations, observation{Version: version, Scenario: scenario.Name})
	}
	if err := validateFreshScenarioCoverage(observations, catalog); err != nil {
		t.Fatalf("valid coverage: %v", err)
	}

	if err := validateFreshScenarioCoverage(observations[1:], catalog); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing scenario error = %v", err)
	}
	duplicated := append(append([]observation(nil), observations...), observations[0])
	if err := validateFreshScenarioCoverage(duplicated, catalog); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate scenario error = %v", err)
	}
	outside := append(append([]observation(nil), observations...), observation{Version: "v0.9.1", Scenario: freshDoltEmbeddedScenario})
	if err := validateFreshScenarioCoverage(outside, catalog); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unexpected scenario error = %v", err)
	}
}

func TestFreshScenarioInitArgsAndCapabilities(t *testing.T) {
	for _, test := range []struct {
		name       string
		version    string
		port       int
		wantArgs   []string
		capability string
	}{
		{freshSQLiteScenario, "v0.47.1", 0, []string{"init", "--prefix", "census"}, ""},
		{freshSQLiteScenario, "v0.47.2", 0, []string{"init", "--backend", "sqlite", "--prefix", "census"}, "--backend"},
		{freshDoltLegacyScenario, "v0.50.3", 0, []string{"init", "--backend", "dolt", "--prefix", "census"}, "--backend"},
		{freshDoltLegacyScenario, "v0.51.0", 0, []string{"init", "--prefix", "census"}, ""},
		{freshDoltServerScenario, "v0.49.1", 45123, []string{"init", "--backend", "dolt", "--prefix", "census", "--server", "--server-host", "127.0.0.1", "--server-port", "45123", "--server-user", "root"}, "--backend --server --server-host --server-port --server-user"},
		{freshDoltServerScenario, "v0.50.0", 45123, []string{"init", "--prefix", "census", "--server", "--server-host", "127.0.0.1", "--server-port", "45123", "--server-user", "root"}, "--server --server-host --server-port --server-user"},
		{freshDoltEmbeddedScenario, "v0.63.0", 0, []string{"init", "--prefix", "census"}, ""},
	} {
		t.Run(test.name+"/"+test.version, func(t *testing.T) {
			scenario, ok := freshScenarioByName(test.name)
			if !ok {
				t.Fatal("scenario not found")
			}
			got, err := scenario.initArgs(test.version, "census", test.port)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "\x00") != strings.Join(test.wantArgs, "\x00") {
				t.Errorf("init args = %q, want %q", got, test.wantArgs)
			}
			if !scenario.supportedByInitHelp(test.version, []byte("init options "+test.capability)) {
				t.Errorf("capability helper rejected %q", test.capability)
			}
		})
	}
	server, _ := freshScenarioByName(freshDoltServerScenario)
	if _, err := server.initArgs("v0.49.1", "census", 0); err == nil {
		t.Error("server scenario accepted invalid port")
	}
	for version, want := range map[string]int{
		"v0.49.4": 0,
		"v0.49.5": 3307,
		"v0.49.6": 0,
	} {
		got, err := server.bootstrapServerPort(version)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("bootstrap server port for %s = %d, want %d", version, got, want)
		}
	}
}
