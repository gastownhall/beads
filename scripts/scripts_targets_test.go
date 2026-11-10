package scripts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This package's tests are split over several go_test targets in
// scripts/BUILD.bazel, each compiling the files whose tests it runs and
// declaring only the data they read. Their srcs are kept by hand (gazelle
// would put every file back into scripts_test), so nothing else notices a new
// _test.go file in no target (its tests would never run under Bazel) or a file
// in two (its tests would run twice, one copy without the data it reads).

var (
	scriptsGoTestRuleRe = regexp.MustCompile(`(?ms)^go_test\(\n(.*?)^\)$`)
	scriptsRuleNameRe   = regexp.MustCompile(`(?m)^    name = "([^"]+)",$`)
	scriptsRuleSrcsRe   = regexp.MustCompile(`(?ms)^    srcs = \[(.*?)\],`)
	scriptsQuotedRe     = regexp.MustCompile(`"([^"]+)"`)
	scriptsTestFuncRe   = regexp.MustCompile(`(?m)^func (Test\w*)\(\w+ \*testing\.T\) \{`)
)

// scriptsTargetProblems checks build's go_test targets against tests, each
// _test.go file's top-level Test functions. A target whose args hold
// -required-suite is a second, selective run over tests another target runs
// in full (TestBazelRetiredLanesCannotBeNarrowed pins those) and is ignored.
func scriptsTargetProblems(build string, tests map[string][]string) []string {
	var problems []string
	owners := map[string][]string{}
	for _, m := range scriptsGoTestRuleRe.FindAllStringSubmatch(build, -1) {
		rule := m[1]
		if strings.Contains(rule, "-required-suite") {
			continue
		}
		name := scriptsRuleNameRe.FindStringSubmatch(rule)
		srcs := scriptsRuleSrcsRe.FindStringSubmatch(rule)
		if name == nil || srcs == nil {
			problems = append(problems, "a go_test has no parsable name or srcs list:\n"+rule)
			continue
		}
		for _, src := range scriptsQuotedRe.FindAllStringSubmatch(srcs[1], -1) {
			if _, ok := tests[src[1]]; !ok {
				problems = append(problems, name[1]+" lists "+src[1]+", which is not a _test.go file here")
				continue
			}
			owners[src[1]] = append(owners[src[1]], name[1])
		}
	}
	files := make([]string, 0, len(tests))
	for f := range tests {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		switch targets := owners[f]; {
		case len(targets) == 0:
			problems = append(problems, f+" is in no go_test: its tests never run under Bazel; add it to the target that declares what they read")
		case len(targets) > 1 && len(tests[f]) > 0:
			problems = append(problems, f+" defines tests ("+strings.Join(tests[f], ", ")+") and is in "+strings.Join(targets, " and ")+": they would run in each; move shared helpers to a helpers-only file")
		}
	}
	return problems
}

func TestScriptsTestTargetsPartitionTheTests(t *testing.T) {
	fixture := "go_test(\n    name = \"a_test\",\n    srcs = [\n        \"a_test.go\",\n        \"helpers_test.go\",\n    ],\n)\n\n" +
		"go_test(\n    name = \"b_test\",\n    srcs = [\n        \"a_test.go\",\n        \"helpers_test.go\",\n    ],\n)\n\n" +
		"go_test(\n    name = \"v_test\",\n    srcs = [\"c_test.go\"],\n    args = [\"-required-suite=x\"],\n)\n"
	got := scriptsTargetProblems(fixture, map[string][]string{
		"a_test.go":       {"TestA"},
		"c_test.go":       {"TestC"},
		"helpers_test.go": nil,
	})
	if len(got) != 2 || !strings.HasPrefix(got[0], "a_test.go defines tests (TestA) and is in a_test and b_test") || !strings.HasPrefix(got[1], "c_test.go is in no go_test") {
		t.Errorf("fixture problems = %q; want a_test.go in two targets and c_test.go in none (helpers_test.go may be shared)", got)
	}

	root := sourceRepoRoot(t)
	paths, err := filepath.Glob(filepath.Join(root, "scripts", "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 40 {
		t.Fatalf("found only %d scripts/*_test.go files; the listing is broken", len(paths))
	}
	tests := map[string][]string{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, m := range scriptsTestFuncRe.FindAllStringSubmatch(string(data), -1) {
			names = append(names, m[1])
		}
		tests[filepath.Base(p)] = names
	}
	for _, p := range scriptsTargetProblems(readPolicyFile(t, root, "scripts/BUILD.bazel"), tests) {
		t.Errorf("scripts/BUILD.bazel: %s", p)
	}
}
