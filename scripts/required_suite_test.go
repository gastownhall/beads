package scripts_test

import (
	"flag"
	"fmt"
	"go/token"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"
)

const (
	docFreshnessRequiredSuite = "doc-freshness"
	requiredSuiteContractTest = "TestRequiredSuiteContract"
)

var requiredSuite = flag.String(
	"required-suite",
	"",
	"require the named CI test suite to have its exact compiled inventory selected",
)

type requiredSuiteDefinition struct {
	memberPrefix string
	tests        []string
}

var requiredSuiteDefinitions = map[string]requiredSuiteDefinition{
	docFreshnessRequiredSuite: {
		memberPrefix: "TestDocFreshness",
		tests: []string{
			"TestDocFreshnessDoesNotRequirePython",
			"TestDocFreshnessValidatesMaxAge",
			"TestDocFreshnessBashProbeIgnoresStartupEnvironment",
			"TestDocFreshnessPreservesDateDiagnostics",
			"TestDocFreshnessPreservesISODateBoundaries",
			"TestDocFreshnessUsesProlepticGregorianLeapRules",
			"TestDocFreshnessUsesNativeDefaultTodayProvider",
			"TestDocFreshnessReportsUnavailableTodayProvider",
			"TestDocFreshnessReportsInvalidTodayProviderOutput",
			"TestDocFreshnessReportsInvalidTodayOverride",
		},
	},
}

type requiredSuiteFlags struct {
	run, skip, list, count, cpu string
	short                       bool
}

// Required mode owns only compiled top-level inventory and selection. m.Run
// remains authoritative for runtime failures, skips, and subtest dispositions.
func TestMain(m *testing.M) {
	if !flag.Parsed() {
		flag.Parse()
	}
	if *requiredSuite == "" {
		os.Exit(m.Run())
	}
	tests, err := listCompiledTopLevelTests()
	if err == nil {
		err = validateRequiredSuite(*requiredSuite, tests, currentRequiredSuiteFlags())
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "required test suite contract failed: %v\n", err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestRequiredSuiteContract(t *testing.T) {
	suite := requiredSuiteDefinitions[docFreshnessRequiredSuite]
	compiled := append([]string{requiredSuiteContractTest, "TestUnrelated"}, suite.tests...)
	valid := requiredSuiteFlags{run: "^(TestDocFreshness.*|TestRequiredSuiteContract)$", count: "1"}

	withoutLast := append([]string(nil), compiled[:len(compiled)-1]...)
	withExtra := append(append([]string(nil), compiled...), "TestDocFreshnessNewBehavior")
	withDuplicate := append(append([]string(nil), compiled...), suite.tests[0])
	for _, test := range []struct {
		name     string
		active   string
		compiled []string
		flags    requiredSuiteFlags
		wantErr  bool
	}{
		{name: "inactive focused use", compiled: withExtra, flags: requiredSuiteFlags{run: "[", skip: ".", count: "0", short: true}},
		{name: "exact inventory and selection", active: docFreshnessRequiredSuite, compiled: compiled, flags: valid},
		{name: "unknown suite", active: "unknown", compiled: compiled, flags: valid, wantErr: true},
		{name: "zero selected", active: docFreshnessRequiredSuite, compiled: compiled, flags: withRun(valid, "^$"), wantErr: true},
		{name: "partial selection", active: docFreshnessRequiredSuite, compiled: compiled, flags: withRun(valid, "^TestDocFreshnessDoesNotRequirePython$"), wantErr: true},
		{name: "authority omitted", active: docFreshnessRequiredSuite, compiled: compiled, flags: withRun(valid, "^TestDocFreshness"), wantErr: true},
		{name: "unrelated selected", active: docFreshnessRequiredSuite, compiled: compiled, flags: withRun(valid, "^Test"), wantErr: true},
		{name: "missing inventory", active: docFreshnessRequiredSuite, compiled: withoutLast, flags: valid, wantErr: true},
		{name: "extra inventory", active: docFreshnessRequiredSuite, compiled: withExtra, flags: valid, wantErr: true},
		{name: "duplicate inventory", active: docFreshnessRequiredSuite, compiled: withDuplicate, flags: valid, wantErr: true},
		{name: "skip", active: docFreshnessRequiredSuite, compiled: compiled, flags: withFlag(valid, "skip"), wantErr: true},
		{name: "count", active: docFreshnessRequiredSuite, compiled: compiled, flags: withFlag(valid, "count"), wantErr: true},
		{name: "cpu", active: docFreshnessRequiredSuite, compiled: compiled, flags: withFlag(valid, "cpu"), wantErr: true},
		{name: "list", active: docFreshnessRequiredSuite, compiled: compiled, flags: withFlag(valid, "list"), wantErr: true},
		{name: "short", active: docFreshnessRequiredSuite, compiled: compiled, flags: withFlag(valid, "short"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRequiredSuite(test.active, test.compiled, test.flags)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateRequiredSuite() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}

	t.Run("inactive TestMain preserves zero-selection success", func(t *testing.T) {
		output, exitCode := runSuiteTestProcess(t, "^$", "")
		if exitCode != 0 {
			t.Fatalf("inactive focused child exit = %d, want 0:\n%s", exitCode, output)
		}
	})

	t.Run("compiled suite", func(t *testing.T) {
		actual, err := listCompiledTopLevelTests()
		if err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(actual, func(name string) bool {
			return strings.HasPrefix(name, suite.memberPrefix)
		}) {
			t.Skip("doc-freshness tests are not compiled without the integration build tag")
		}
		if err := validateRequiredSuiteInventory(suite, actual); err != nil {
			t.Fatal(err)
		}
		for _, run := range []string{"^$", "^TestDocFreshnessDoesNotRequirePython$", "^TestDocFreshness"} {
			t.Run("TestMain rejects "+run, func(t *testing.T) {
				output, exitCode := runSuiteTestProcess(t, run, docFreshnessRequiredSuite)
				if exitCode == 0 || !strings.Contains(output, "required test suite contract failed:") {
					t.Fatalf("exit = %d, want nonzero required-suite diagnostic:\n%s", exitCode, output)
				}
			})
		}
	})

	t.Run("Unicode inventory is retained", func(t *testing.T) {
		parsed := parseCompiledTopLevelTests("TestDocFreshnessΔ\nok\texample/scripts\t0.1s\n")
		if len(parsed) != 1 || parsed[0] != "TestDocFreshnessΔ" ||
			validateRequiredSuiteInventory(suite, append(compiled, parsed...)) == nil {
			t.Fatalf("Unicode test was dropped or accepted: parsed=%v", parsed)
		}
	})

	for _, test := range []struct {
		name string
		want bool
	}{
		{name: "TestDocFreshnessΔ", want: true},
		{name: "TestdocFreshness"},
		{name: "Testδ"},
		{name: "Test", want: true},
		{name: "Benchmark", want: true},
		{name: "Example", want: true},
		{name: "Fuzz", want: true},
		{name: "Example_doc", want: true},
		{name: "Exampledoc"},
	} {
		if got := isTopLevelListCandidate(test.name); got != test.want {
			t.Errorf("isTopLevelListCandidate(%q) = %t, want %t", test.name, got, test.want)
		}
	}
}

func withRun(flags requiredSuiteFlags, run string) requiredSuiteFlags {
	flags.run = run
	return flags
}

func withFlag(flags requiredSuiteFlags, name string) requiredSuiteFlags {
	switch name {
	case "skip":
		flags.skip = "."
	case "count":
		flags.count = "2"
	case "cpu":
		flags.cpu = "1,2"
	case "list":
		flags.list = "."
	case "short":
		flags.short = true
	}
	return flags
}

func validateRequiredSuite(name string, compiled []string, flags requiredSuiteFlags) error {
	if name == "" {
		return nil
	}
	suite, ok := requiredSuiteDefinitions[name]
	if !ok {
		return fmt.Errorf("unknown suite %q", name)
	}
	switch {
	case flags.run == "":
		return fmt.Errorf("suite %q requires an explicit -test.run selection", name)
	case strings.Contains(flags.run, "/"):
		return fmt.Errorf("suite %q requires top-level-only -test.run=%q", name, flags.run)
	case flags.skip != "":
		return fmt.Errorf("suite %q forbids -test.skip=%q", name, flags.skip)
	case flags.count != "1":
		return fmt.Errorf("suite %q requires -test.count=1, got %q", name, flags.count)
	case flags.cpu != "":
		return fmt.Errorf("suite %q forbids -test.cpu=%q", name, flags.cpu)
	case flags.list != "":
		return fmt.Errorf("suite %q forbids -test.list=%q", name, flags.list)
	case flags.short:
		return fmt.Errorf("suite %q forbids -test.short", name)
	}
	if err := validateRequiredSuiteInventory(suite, compiled); err != nil {
		return fmt.Errorf("suite %q inventory: %w", name, err)
	}
	pattern, err := regexp.Compile(flags.run)
	if err != nil {
		return fmt.Errorf("suite %q has invalid -test.run=%q: %w", name, flags.run, err)
	}
	var selected []string
	for _, testName := range compiled {
		if isTopLevelRunCandidate(testName) && pattern.MatchString(testName) {
			selected = append(selected, testName)
		}
	}
	want := append(append([]string(nil), suite.tests...), requiredSuiteContractTest)
	return exactInventory("-test.run="+flags.run, want, selected)
}

func validateRequiredSuiteInventory(suite requiredSuiteDefinition, compiled []string) error {
	var members []string
	for _, name := range compiled {
		if strings.HasPrefix(name, suite.memberPrefix) {
			members = append(members, name)
		}
	}
	return exactInventory("compiled", suite.tests, members)
}

func exactInventory(label string, want, got []string) error {
	wantCounts, gotCounts := make(map[string]int), make(map[string]int)
	for _, name := range want {
		wantCounts[name]++
	}
	for _, name := range got {
		gotCounts[name]++
	}
	var missing, extra, duplicate []string
	for name, count := range wantCounts {
		if gotCounts[name] == 0 {
			missing = append(missing, name)
		}
		if count > 1 {
			duplicate = append(duplicate, name)
		}
	}
	for name, count := range gotCounts {
		if wantCounts[name] == 0 {
			extra = append(extra, name)
		}
		if count > 1 {
			duplicate = append(duplicate, name)
		}
	}
	if len(missing)+len(extra)+len(duplicate) == 0 {
		return nil
	}
	for _, names := range [][]string{missing, extra, duplicate} {
		sort.Strings(names)
	}
	return fmt.Errorf("%s inventory missing=%v extra=%v duplicate=%v", label, missing, extra, duplicate)
}

func isTopLevelRunCandidate(name string) bool {
	return isGoTestName(name, "Test") || isGoTestName(name, "Example") || isGoTestName(name, "Fuzz")
}

func listCompiledTopLevelTests() ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate current test executable: %w", err)
	}
	output, err := exec.Command(executable, "-test.list=.").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list compiled top-level tests: %w: %s", err, strings.TrimSpace(string(output)))
	}
	tests := parseCompiledTopLevelTests(string(output))
	if len(tests) == 0 {
		return nil, fmt.Errorf("test executable reported no compiled top-level tests")
	}
	return tests, nil
}

func parseCompiledTopLevelTests(output string) []string {
	var tests []string
	for _, line := range strings.Split(output, "\n") {
		name := strings.TrimSpace(line)
		if token.IsIdentifier(name) && isTopLevelListCandidate(name) {
			tests = append(tests, name)
		}
	}
	return tests
}

func isTopLevelListCandidate(name string) bool {
	return isTopLevelRunCandidate(name) || isGoTestName(name, "Benchmark")
}

func isGoTestName(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(next)
}

func runSuiteTestProcess(t *testing.T, run, suite string) (string, int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.count=1", "-test.run=" + run}
	if suite != "" {
		args = append(args, "-required-suite="+suite)
	}
	output, runErr := exec.Command(executable, args...).CombinedOutput()
	if runErr == nil {
		return string(output), 0
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		return string(output), exitErr.ExitCode()
	}
	t.Fatal(runErr)
	return "", -1
}

func currentRequiredSuiteFlags() requiredSuiteFlags {
	return requiredSuiteFlags{
		run: testFlagValue("test.run"), skip: testFlagValue("test.skip"),
		list: testFlagValue("test.list"), count: testFlagValue("test.count"),
		cpu: testFlagValue("test.cpu"), short: testFlagValue("test.short") == "true",
	}
}

func testFlagValue(name string) string {
	if testFlag := flag.Lookup(name); testFlag != nil {
		return testFlag.Value.String()
	}
	return ""
}
