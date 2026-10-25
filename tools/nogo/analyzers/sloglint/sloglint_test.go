package sloglint_test

import (
	"testing"

	"github.com/steveyegge/beads/tools/nogo/analyzers/sloglint"
	"github.com/steveyegge/beads/tools/nogo/internal/analyzertest"
	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
)

func TestOptionsDefaultLikeGolangciLint(t *testing.T) {
	opts := sloglint.Options(golangci.SloglintSettings{})
	if !opts.NoMixedArgs || opts.KVOnly || opts.AttrOnly || opts.StaticMsg {
		t.Errorf("default options = %+v, want golangci-lint's (no-mixed-args only)", opts)
	}
	off := false
	if sloglint.Options(golangci.SloglintSettings{NoMixedArgs: &off}).NoMixedArgs {
		t.Error("no-mixed-args: false was not honored")
	}
}

// nogo passes carry no Module; sloglint reads its Go version.
func TestRunsWithoutAModule(t *testing.T) {
	got := analyzertest.RunInRepo(t, sloglint.Analyzer, "p", map[string]string{"p.go": "package p\n\nfunc f() {}\n"})
	analyzertest.Equal(t, got, nil)
}
