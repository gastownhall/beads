// Package unparam runs mvdan.cc/unparam as golangci-lint's unparam linter
// does, with .golangci.yml's linters.settings.unparam.
package unparam

import (
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/buildssa"
	"golang.org/x/tools/go/packages"
	"mvdan.cc/unparam/check"

	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
)

// Analyzer reports function parameters and results that are always unused
// or always receive the same value.
var Analyzer = golangci.Wrap("unparam", &analysis.Analyzer{
	Name:     "unparam",
	Doc:      "reports unused function parameters and results",
	Requires: []*analysis.Analyzer{buildssa.Analyzer},
	Run:      run,
})

func run(pass *analysis.Pass) (any, error) {
	cfg, err := golangci.Load()
	if err != nil {
		return nil, err
	}
	ssa := pass.ResultOf[buildssa.Analyzer].(*buildssa.SSA)
	pkg := &packages.Package{
		Fset:      pass.Fset,
		Syntax:    pass.Files,
		Types:     pass.Pkg,
		TypesInfo: pass.TypesInfo,
	}
	c := &check.Checker{}
	c.CheckExportedFuncs(cfg.Linters.Settings.Unparam.CheckExported)
	c.Packages([]*packages.Package{pkg})
	c.ProgramSSA(ssa.Pkg.Prog)
	issues, err := c.Check()
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		pass.Report(analysis.Diagnostic{Pos: issue.Pos(), Message: issue.Message()})
	}
	return nil, nil
}
