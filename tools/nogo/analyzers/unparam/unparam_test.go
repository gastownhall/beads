package unparam_test

import (
	"testing"

	"github.com/steveyegge/beads/tools/nogo/analyzers/unparam"
	"github.com/steveyegge/beads/tools/nogo/internal/analyzertest"
)

const lib = `package p

func render(n int, unused string) int {
	if n > 1 {
		return n * 2
	}
	return n
}

// Use is exported, so unparam does not check it (check-exported is off).
func Use(x int) int { return render(1, "a") + render(2, "b") }
`

// golangci-lint v2.10.1 reports the same finding for the same package (a
// body this small but no smaller: unparam skips trivial functions).
func TestReportsUnusedParameters(t *testing.T) {
	analyzertest.Equal(t, analyzertest.RunInRepo(t, unparam.Analyzer, "p", map[string]string{"p.go": lib}),
		[]analyzertest.Diagnostic{{File: "p.go", Line: 3, Message: "render - unused is unused"}})
}

// run.tests: false: golangci-lint judged the package without its tests,
// which is Bazel's library unit; the test unit is not analyzed.
func TestTestUnitIsSkipped(t *testing.T) {
	got := analyzertest.RunInRepo(t, unparam.Analyzer, "p", map[string]string{
		"p.go":      lib,
		"p_test.go": "package p\n\nvar _ = render(1, \"c\")\n",
	})
	analyzertest.Equal(t, got, nil)
}

// .golangci.yml drops unparam's getConfig findings in the tracker adapters.
func TestPathTextExclusion(t *testing.T) {
	src := map[string]string{"tracker.go": "package jira\n\nfunc getConfig(k string, unused int) string {\n\tif k == \"\" {\n\t\treturn \"none\"\n\t}\n\treturn k\n}\n\n// Use is exported.\nfunc Use() string { return getConfig(\"a\", 1) + getConfig(\"b\", 2) }\n"}
	analyzertest.Equal(t, analyzertest.RunInRepo(t, unparam.Analyzer, "internal/jira", src), nil)
	analyzertest.Equal(t, analyzertest.RunInRepo(t, unparam.Analyzer, "internal/other", src),
		[]analyzertest.Diagnostic{{File: "tracker.go", Line: 3, Message: "getConfig - unused is unused"}})
}
