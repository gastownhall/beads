package unconvert_test

import (
	"testing"

	"github.com/steveyegge/beads/tools/nogo/analyzers/unconvert"
	"github.com/steveyegge/beads/tools/nogo/internal/analyzertest"
)

func TestReportsUnnecessaryConversions(t *testing.T) {
	got := analyzertest.RunInRepo(t, unconvert.Analyzer, "p", map[string]string{
		"p.go": "package p\n\nfunc f(x int) int { return int(x) }\n\nfunc g(x int32) int64 { return int64(x) }\n",
	})
	analyzertest.Equal(t, got, []analyzertest.Diagnostic{{File: "p.go", Line: 3, Message: "unnecessary conversion"}})
}
