// Package sloglint runs go-simpler.org/sloglint with .golangci.yml's
// linters.settings.sloglint and golangci-lint's defaults.
package sloglint

import (
	"strings"
	"sync"

	"go-simpler.org/sloglint"
	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"

	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
)

// Analyzer checks log/slog calls for a consistent style.
var Analyzer = golangci.Wrap("sloglint", &analysis.Analyzer{
	Name:     "sloglint",
	Doc:      "ensures a consistent code style when using log/slog",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
})

var (
	once  sync.Once
	inner *analysis.Analyzer
)

func run(pass *analysis.Pass) (any, error) {
	cfg, err := golangci.Load()
	if err != nil {
		return nil, err
	}
	once.Do(func() { inner = sloglint.New(Options(cfg.Linters.Settings.Sloglint)) })
	return inner.Run(WithModule(pass))
}

// WithModule returns pass with Module set when nogo left it nil (rules_go does
// not set it; sloglint reads Module.GoVersion). The language version is the
// package's, which rules_go sets from go.mod's go directive, as golangci-lint
// reads the module's.
func WithModule(pass *analysis.Pass) *analysis.Pass {
	if pass.Module != nil {
		return pass
	}
	p := *pass
	p.Module = &analysis.Module{GoVersion: strings.TrimPrefix(pass.Pkg.GoVersion(), "go")}
	return &p
}

// Options maps the settings onto sloglint's options; no-mixed-args defaults
// to true, as in golangci-lint.
func Options(s golangci.SloglintSettings) *sloglint.Options {
	return &sloglint.Options{
		NoMixedArgs:    s.NoMixedArgs == nil || *s.NoMixedArgs,
		KVOnly:         s.KVOnly,
		AttrOnly:       s.AttrOnly,
		NoGlobal:       s.NoGlobal,
		ContextOnly:    s.Context,
		StaticMsg:      s.StaticMsg,
		MsgStyle:       s.MsgStyle,
		NoRawKeys:      s.NoRawKeys,
		KeyNamingCase:  s.KeyNamingCase,
		ForbiddenKeys:  s.ForbiddenKeys,
		ArgsOnSepLines: s.ArgsOnSepLines,
	}
}
