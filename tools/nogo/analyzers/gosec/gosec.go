// Package gosec runs github.com/securego/gosec/v2 as golangci-lint's gosec
// linter does, with .golangci.yml's linters.settings.gosec: every rule and
// SSA analyzer (less G407, which golangci-lint always drops), #nosec honored,
// and findings reported as "<rule>: <what>".
package gosec

import (
	"fmt"
	"go/token"
	"io"
	"log"
	"strconv"
	"strings"

	"github.com/securego/gosec/v2"
	"github.com/securego/gosec/v2/analyzers"
	"github.com/securego/gosec/v2/issue"
	"github.com/securego/gosec/v2/rules"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
	"github.com/steveyegge/beads/tools/nogo/internal/position"
)

// Analyzer reports security problems.
var Analyzer = golangci.Wrap("gosec", &analysis.Analyzer{
	Name: "gosec",
	Doc:  "inspects source code for security problems",
	Run:  run,
})

func run(pass *analysis.Pass) (any, error) {
	cfg, err := golangci.Load()
	if err != nil {
		return nil, err
	}
	settings := cfg.Linters.Settings.Gosec
	severity, err := score(settings.Severity)
	if err != nil {
		return nil, err
	}
	confidence, err := score(settings.Confidence)
	if err != nil {
		return nil, err
	}
	// golangci-lint drops G407 (securego/gosec#1209, #1211) on top of the
	// configured excludes.
	excludes := append([]string{"G407"}, settings.Excludes...)
	var ruleFilters []rules.RuleFilter
	var analyzerFilters []analyzers.AnalyzerFilter
	if len(settings.Includes) > 0 {
		ruleFilters = append(ruleFilters, rules.NewRuleFilter(false, settings.Includes...))
		analyzerFilters = append(analyzerFilters, analyzers.NewAnalyzerFilter(false, settings.Includes...))
	}
	ruleFilters = append(ruleFilters, rules.NewRuleFilter(true, excludes...))
	analyzerFilters = append(analyzerFilters, analyzers.NewAnalyzerFilter(true, excludes...))

	g := gosec.NewAnalyzer(gosec.NewConfig(), true, false, false, 1, log.New(io.Discard, "", 0))
	ruleDefs := rules.Generate(false, ruleFilters...)
	analyzerDefs := analyzers.Generate(false, analyzerFilters...)
	g.LoadRules(ruleDefs.RulesInfo())
	g.LoadAnalyzers(analyzerDefs.AnalyzersInfo())

	pkg := &packages.Package{
		Fset:      pass.Fset,
		Syntax:    pass.Files,
		Types:     pass.Pkg,
		TypesInfo: pass.TypesInfo,
	}
	g.CheckRules(pkg)
	g.CheckAnalyzers(pkg)
	found, _, _ := g.Report()

	idx := position.NewIndex(pass)
	for _, i := range found {
		if i.Severity < severity || i.Confidence < confidence {
			continue
		}
		line, err := strconv.Atoi(i.Line)
		if err != nil {
			// A multi-line finding: "from-to".
			first, _, ok := strings.Cut(i.Line, "-")
			if line, err = strconv.Atoi(first); !ok || err != nil {
				return nil, fmt.Errorf("gosec: line %q of %s in %s", i.Line, i.RuleID, i.File)
			}
		}
		col, err := strconv.Atoi(i.Col)
		if err != nil {
			return nil, fmt.Errorf("gosec: column %q of %s in %s", i.Col, i.RuleID, i.File)
		}
		pos, ok := idx.Pos(token.Position{Filename: i.File, Line: line, Column: col})
		if !ok {
			continue
		}
		pass.Report(analysis.Diagnostic{Pos: pos, Message: fmt.Sprintf("%s: %s", i.RuleID, i.What)})
	}
	return nil, nil
}

// score is golangci-lint's convertToScore: empty means low (everything).
func score(s string) (issue.Score, error) {
	switch strings.ToLower(s) {
	case "", "low":
		return issue.Low, nil
	case "medium":
		return issue.Medium, nil
	case "high":
		return issue.High, nil
	default:
		return issue.Low, fmt.Errorf("gosec: severity/confidence %q: want low, medium or high", s)
	}
}
