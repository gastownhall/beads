// Package misspell runs github.com/golangci/misspell as golangci-lint's
// misspell linter does, with .golangci.yml's linters.settings.misspell.
// misspell is a library with no analysis.Analyzer.
package misspell

import (
	"fmt"
	"go/token"
	"os"
	"strings"
	"sync"
	"unicode"

	"github.com/golangci/misspell"
	"golang.org/x/tools/go/analysis"

	"github.com/steveyegge/beads/tools/nogo/internal/golangci"
)

// Analyzer reports commonly misspelled English words.
var Analyzer = golangci.Wrap("misspell", &analysis.Analyzer{
	Name: "misspell",
	Doc:  "reports commonly misspelled English words",
	Run:  run,
})

var (
	once        sync.Once
	replacer    *misspell.Replacer
	replacerErr error
)

func run(pass *analysis.Pass) (any, error) {
	cfg, err := golangci.Load()
	if err != nil {
		return nil, err
	}
	settings := cfg.Linters.Settings.Misspell
	once.Do(func() { replacer, replacerErr = NewReplacer(settings) })
	if replacerErr != nil {
		return nil, replacerErr
	}
	// The default mode checks the whole file text; "restricted" only
	// comments.
	replace := replacer.Replace
	if strings.EqualFold(settings.Mode, "restricted") {
		replace = replacer.ReplaceGo
	}
	for _, file := range pass.Files {
		tf := pass.Fset.File(file.Pos())
		if !strings.HasSuffix(tf.Name(), ".go") {
			continue
		}
		content, err := os.ReadFile(tf.Name())
		if err != nil {
			return nil, fmt.Errorf("misspell: reading %s: %w", tf.Name(), err)
		}
		_, diffs := replace(string(content))
		for _, diff := range diffs {
			if diff.Line < 1 || diff.Line > tf.LineCount() {
				continue
			}
			start := tf.LineStart(diff.Line) + token.Pos(diff.Column)
			end := start + token.Pos(len(diff.Original))
			pass.Report(analysis.Diagnostic{
				Pos:     start,
				End:     end,
				Message: fmt.Sprintf("`%s` is a misspelling of `%s`", diff.Original, diff.Corrected),
				SuggestedFixes: []analysis.SuggestedFix{{
					Message:   "fix spelling",
					TextEdits: []analysis.TextEdit{{Pos: start, End: end, NewText: []byte(diff.Corrected)}},
				}},
			})
		}
	}
	return nil, nil
}

// NewReplacer builds golangci-lint's misspell replacer for the settings.
func NewReplacer(s golangci.MisspellSettings) (*misspell.Replacer, error) {
	r := &misspell.Replacer{Replacements: misspell.DictMain}
	switch strings.ToUpper(s.Locale) {
	case "":
	case "US":
		r.AddRuleList(misspell.DictAmerican)
	case "UK", "GB":
		r.AddRuleList(misspell.DictBritish)
	default:
		return nil, fmt.Errorf("misspell: unknown locale %q", s.Locale)
	}
	if len(s.ExtraWords) > 0 {
		extra := make([]string, 0, len(s.ExtraWords)*2)
		for _, w := range s.ExtraWords {
			if w.Typo == "" || w.Correction == "" {
				return nil, fmt.Errorf("misspell: extra word typo %q / correction %q must not be empty", w.Typo, w.Correction)
			}
			notLetter := func(r rune) bool { return !unicode.IsLetter(r) }
			if strings.ContainsFunc(w.Typo, notLetter) || strings.ContainsFunc(w.Correction, notLetter) {
				return nil, fmt.Errorf("misspell: extra word %q -> %q must contain only letters", w.Typo, w.Correction)
			}
			extra = append(extra, strings.ToLower(w.Typo), strings.ToLower(w.Correction))
		}
		r.AddRuleList(extra)
	}
	if len(s.IgnoreRules) > 0 {
		r.RemoveRule(s.IgnoreRules)
	}
	r.Compile()
	return r, nil
}
