// Package depguard runs github.com/OpenPeeDeeP/depguard/v2 with the rules in
// .golangci.yml's linters.settings.depguard, as golangci-lint does.
package depguard

import (
	"go/token"
	"strings"

	"github.com/OpenPeeDeeP/depguard/v2"
	"golang.org/x/tools/go/analysis"

	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
)

// Analyzer reports imports the configured depguard rules deny.
var Analyzer = golangci.Wrap("depguard", &analysis.Analyzer{
	Name: "depguard",
	Doc:  "checks package imports against the depguard rules in .golangci.yml",
	Run:  run,
})

const goStd = "$gostd"

func run(pass *analysis.Pass) (any, error) {
	cfg, err := golangci.Load()
	if err != nil {
		return nil, err
	}
	std := stdPrefixes(pass)
	settings := depguard.LinterSettings{}
	for name, rule := range cfg.Linters.Settings.Depguard.Rules {
		deny := map[string]string{}
		for _, d := range rule.Deny {
			if d.Pkg == goStd {
				for _, p := range std {
					deny[p] = d.Desc
				}
				continue
			}
			deny[d.Pkg] = d.Desc
		}
		settings[name] = &depguard.List{
			ListMode: rule.ListMode,
			Files:    rule.Files,
			Allow:    expandGoStd(rule.Allow, std),
			Deny:     deny,
		}
	}
	a, err := depguard.NewAnalyzer(&settings)
	if err != nil {
		return nil, err
	}
	rooted := *pass
	rooted.Fset = Rooted(pass.Fset)
	return a.Run(&rooted)
}

// stdPrefixes stands in for depguard's $gostd, which lists $GOROOT/src's
// top-level directories (a nogo action has no GOROOT source tree). It returns
// the first path element of every standard-library import in the pass, by
// cmd/go's own rule: a standard import path has no dot in its first element.
// "C" (cgo) is not a GOROOT directory. "unsafe" keeps an allow list non-empty
// for a package with no standard imports; it is a GOROOT directory too.
func stdPrefixes(pass *analysis.Pass) []string {
	seen := map[string]bool{"unsafe": true}
	out := []string{"unsafe"}
	for _, f := range pass.Files {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, "\"`")
			first, _, _ := strings.Cut(path, "/")
			if first == "C" || strings.Contains(first, ".") || seen[first] {
				continue
			}
			seen[first] = true
			out = append(out, first)
		}
	}
	return out
}

func expandGoStd(list, std []string) []string {
	var out []string
	for _, p := range list {
		if p == goStd {
			out = append(out, std...)
			continue
		}
		out = append(out, p)
	}
	return out
}

// Rooted returns a copy of fset whose file names are rooted at "/": the
// absolute paths golangci-lint hands depguard, so that a files pattern such
// as "**/internal/storage/**" or "$all" ("**/*.go") also matches a file at
// the repository root, where nogo's execroot-relative name has no leading
// directory. Every token.Pos keeps its meaning.
func Rooted(fset *token.FileSet) *token.FileSet {
	out := token.NewFileSet()
	fset.Iterate(func(f *token.File) bool {
		name := f.Name()
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		nf := out.AddFile(name, f.Base(), f.Size())
		nf.SetLines(f.Lines())
		return true
	})
	return out
}
