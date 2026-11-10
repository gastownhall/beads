package errcheck_test

import (
	"testing"

	"github.com/steveyegge/beads/tools/nogo/analyzers/errcheck"
	"github.com/steveyegge/beads/tools/nogo/internal/analyzertest"
)

// The expected messages are golangci-lint v2.10.1's for the same package.
func TestReportsUncheckedErrorsAsGolangciLintDoes(t *testing.T) {
	got := analyzertest.RunInRepo(t, errcheck.Analyzer, "p", map[string]string{
		"p.go": `package p

type closer struct{}

func (closer) Close() error { return nil }

func f() error { return nil }

func g() {
	f()
	_ = f()
	closer{}.Close()
	if err := f(); err != nil {
		return
	}
}
`,
	})
	analyzertest.Equal(t, got, []analyzertest.Diagnostic{
		{File: "p.go", Line: 10, Message: "Error return value is not checked"},
		{File: "p.go", Line: 12, Message: "Error return value of `(example.com/repo/p.closer).Close` is not checked"},
	})
}
