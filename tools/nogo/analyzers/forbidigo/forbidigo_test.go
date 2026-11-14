package forbidigo_test

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/tools/nogo/analyzers/forbidigo"
	"github.com/steveyegge/beads/tools/nogo/internal/analyzertest"
)

const labelUse = `package main

type uow struct{}

func (uow) LabelUseCase() int { return 0 }

func f(u uow) int { return u.LabelUseCase() }
`

// The LabelUseCase rule applies across cmd/bd; .golangci.yml's path-except
// rule keeps forbidigo off the rest of the tree.
func TestRulesApplyOnlyWhereTheExclusionsLeaveThem(t *testing.T) {
	got := analyzertest.RunInRepo(t, forbidigo.Analyzer, "cmd/bd", map[string]string{"x.go": labelUse})
	if len(got) != 1 || got[0].Line != 7 || !strings.HasPrefix(got[0].Message, "use of `u.LabelUseCase` forbidden because \"reach the label plane") {
		t.Fatalf("diagnostics = %+v, want the LabelUseCase rule", got)
	}
	analyzertest.Equal(t, analyzertest.RunInRepo(t, forbidigo.Analyzer, "internal/other", map[string]string{"x.go": labelUse}), nil)
}

// run.tests: false: a test compile unit is not analyzed at all.
func TestTestUnitsAreSkipped(t *testing.T) {
	got := analyzertest.RunInRepo(t, forbidigo.Analyzer, "cmd/bd", map[string]string{
		"x.go":      labelUse,
		"x_test.go": "package main\n",
	})
	analyzertest.Equal(t, got, nil)
}
