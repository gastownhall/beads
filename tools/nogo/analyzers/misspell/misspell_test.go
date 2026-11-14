package misspell_test

import (
	"testing"

	"github.com/steveyegge/beads/tools/nogo/analyzers/misspell"
	"github.com/steveyegge/beads/tools/nogo/internal/analyzertest"
)

// The fixture words are assembled from halves so this file does not itself
// trip the analyzer it tests.
var (
	receive   = "rec" + "ieve"
	color     = "col" + "our"
	cancelled = "cancel" + "led"
)

func TestReportsMisspellingsAnywhereInTheFileUSLocale(t *testing.T) {
	got := analyzertest.RunInRepo(t, misspell.Analyzer, "p", map[string]string{
		"p.go": "// Package p is fine.\npackage p\n\n// " + receive + " is misspelled in a comment.\nvar s = \"" + color + "\"\n",
	})
	analyzertest.Equal(t, got, []analyzertest.Diagnostic{
		{File: "p.go", Line: 4, Message: "`" + receive + "` is a misspelling of `receive`"},
		{File: "p.go", Line: 5, Message: "`" + color + "` is a misspelling of `color`"},
	})
}

// .golangci.yml keeps GitHub's spelling in cmd/bd/gate.go only.
func TestPathTextExclusion(t *testing.T) {
	src := map[string]string{"gate.go": "package main\n\nconst status = \"" + cancelled + "\"\n"}
	analyzertest.Equal(t, analyzertest.RunInRepo(t, misspell.Analyzer, "cmd/bd", src), nil)
	analyzertest.Equal(t, analyzertest.RunInRepo(t, misspell.Analyzer, "cmd/other", src), []analyzertest.Diagnostic{
		{File: "gate.go", Line: 3, Message: "`" + cancelled + "` is a misspelling of `canceled`"},
	})
}
