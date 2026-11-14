package gosec_test

import (
	"testing"

	"github.com/steveyegge/beads/tools/nogo/analyzers/gosec"
	"github.com/steveyegge/beads/tools/nogo/internal/analyzertest"
)

// golangci-lint v2.10.1 reports the same finding for the same package.
func TestReportsRuleIDAndText(t *testing.T) {
	got := analyzertest.RunInRepo(t, gosec.Analyzer, "p", map[string]string{
		"p.go": "package p\n\nconst apiKey = \"AKIAIOSFODNN7EXAMPLE\"\n\n// G115 is excluded tree-wide by .golangci.yml.\nfunc narrow(x int64) int32 { return int32(x) }\n",
	})
	analyzertest.Equal(t, got, []analyzertest.Diagnostic{
		{File: "p.go", Line: 3, Message: "G101: Potential hardcoded credentials"},
	})
}

func TestNosecIsHonored(t *testing.T) {
	got := analyzertest.RunInRepo(t, gosec.Analyzer, "p", map[string]string{
		"p.go": "package p\n\nconst apiKey = \"AKIAIOSFODNN7EXAMPLE\" // #nosec G101 -- fixture\n",
	})
	analyzertest.Equal(t, got, nil)
}
