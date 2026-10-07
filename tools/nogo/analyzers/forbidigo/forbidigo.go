// Package forbidigo runs github.com/ashanbrown/forbidigo/v2 as golangci-lint's
// forbidigo linter does, with .golangci.yml's linters.settings.forbidigo.
package forbidigo

import (
	"fmt"

	"github.com/ashanbrown/forbidigo/v2/forbidigo"
	"golang.org/x/tools/go/analysis"
	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
)

// Analyzer reports uses of forbidden identifiers.
var Analyzer = golangci.Wrap("forbidigo", &analysis.Analyzer{
	Name: "forbidigo",
	Doc:  "forbids the identifiers .golangci.yml lists",
	Run:  run,
})

// pattern is golangci-lint's ForbidigoPattern as forbidigo.NewLinter reads it.
type pattern struct {
	Pattern string `yaml:"p"`
	Package string `yaml:"pkg,omitempty"`
	Msg     string `yaml:"msg,omitempty"`
}

func run(pass *analysis.Pass) (any, error) {
	cfg, err := golangci.Load()
	if err != nil {
		return nil, err
	}
	settings := cfg.Linters.Settings.Forbidigo
	linter, err := NewLinter(settings)
	if err != nil {
		return nil, err
	}
	for _, file := range pass.Files {
		rc := forbidigo.RunConfig{Fset: pass.Fset}
		if settings.AnalyzeTypes {
			rc.TypesInfo = pass.TypesInfo
		}
		hints, err := linter.RunWithConfig(rc, file)
		if err != nil {
			return nil, fmt.Errorf("forbidigo on %s: %w", pass.Fset.File(file.Pos()).Name(), err)
		}
		for _, hint := range hints {
			pass.Report(analysis.Diagnostic{Pos: hint.Pos(), Message: hint.Details()})
		}
	}
	return nil, nil
}

// NewLinter builds forbidigo's linter from the settings, with golangci-lint's
// options: //permit directives are ignored (only //nolint counts), and godoc
// examples are skipped unless exclude-godoc-examples is false.
func NewLinter(settings golangci.ForbidigoSettings) (*forbidigo.Linter, error) {
	excludeExamples := settings.ExcludeGodocExamples == nil || *settings.ExcludeGodocExamples
	var patterns []string
	for _, p := range settings.Forbid {
		buf, err := yaml.Marshal(pattern{Pattern: p.Pattern, Package: p.Pkg, Msg: p.Msg})
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, string(buf))
	}
	linter, err := forbidigo.NewLinter(patterns,
		forbidigo.OptionExcludeGodocExamples(excludeExamples),
		forbidigo.OptionIgnorePermitDirectives(true),
		forbidigo.OptionAnalyzeTypes(settings.AnalyzeTypes),
	)
	if err != nil {
		return nil, fmt.Errorf("forbidigo: %w", err)
	}
	return linter, nil
}
