// Package errcheck runs github.com/kisielk/errcheck as golangci-lint's errcheck
// linter does, with .golangci.yml's linters.settings.errcheck.
package errcheck

import (
	"cmp"
	"fmt"
	"regexp"
	"strings"

	"github.com/kisielk/errcheck/errcheck"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
	"github.com/steveyegge/beads/tools/nogo/internal/position"
)

// Analyzer reports unchecked errors.
var Analyzer = golangci.Wrap("errcheck", &analysis.Analyzer{
	Name: "errcheck",
	Doc:  "reports unchecked errors",
	Run:  run,
})

func run(pass *analysis.Pass) (any, error) {
	cfg, err := golangci.Load()
	if err != nil {
		return nil, err
	}
	settings := cfg.Linters.Settings.Errcheck
	checker := errcheck.Checker{
		Exclusions: errcheck.Exclusions{
			BlankAssignments:       !settings.CheckAssignToBlank,
			TypeAssertions:         !settings.CheckTypeAssertions,
			SymbolRegexpsByPackage: map[string]*regexp.Regexp{},
		},
	}
	if !settings.DisableDefaultExclusions {
		checker.Exclusions.Symbols = append(checker.Exclusions.Symbols, errcheck.DefaultExcludedSymbols...)
	}
	checker.Exclusions.Symbols = append(checker.Exclusions.Symbols, settings.ExcludeFunctions...)

	pkg := &packages.Package{
		Fset:      pass.Fset,
		Syntax:    pass.Files,
		Types:     pass.Pkg,
		TypesInfo: pass.TypesInfo,
	}
	idx := position.NewIndex(pass)
	for _, u := range checker.CheckPackage(pkg).Unique().UncheckedErrors {
		pos, ok := idx.Pos(u.Pos)
		if !ok {
			continue
		}
		pass.Report(analysis.Diagnostic{Pos: pos, Message: Message(u, settings.Verbose)})
	}
	return nil, nil
}

// Message is golangci-lint's text for an unchecked error, which
// .golangci.yml's exclusion rules match against.
func Message(u errcheck.UncheckedError, verbose bool) string {
	if u.FuncName == "" {
		return "Error return value is not checked"
	}
	code := cmp.Or(u.SelectorName, u.FuncName)
	if verbose {
		code = u.FuncName
	}
	if !strings.Contains(code, "`") {
		code = "`" + code + "`"
	}
	return fmt.Sprintf("Error return value of %s is not checked", code)
}
