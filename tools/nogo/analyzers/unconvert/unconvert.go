// Package unconvert runs golangci-lint's fork of mdempsky/unconvert, which
// exposes Run(pass) rather than an analysis.Analyzer.
package unconvert

import (
	"sync"

	"github.com/golangci/unconvert"
	"golang.org/x/tools/go/analysis"

	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
	"github.com/steveyegge/beads/tools/nogo/internal/position"
)

// Analyzer reports type conversions whose operand already has the target type.
var Analyzer = golangci.Wrap("unconvert", &analysis.Analyzer{
	Name: "unconvert",
	Doc:  "reports unnecessary type conversions",
	Run:  run,
})

var configure sync.Once

func run(pass *analysis.Pass) (any, error) {
	cfg, err := golangci.Load()
	if err != nil {
		return nil, err
	}
	configure.Do(func() {
		unconvert.SetFastMath(cfg.Linters.Settings.Unconvert.FastMath)
		unconvert.SetSafe(cfg.Linters.Settings.Unconvert.Safe)
	})
	idx := position.NewIndex(pass)
	for _, p := range unconvert.Run(pass) {
		pos, ok := idx.Pos(p)
		if !ok {
			continue
		}
		pass.Report(analysis.Diagnostic{Pos: pos, Message: "unnecessary conversion"})
	}
	return nil, nil
}
