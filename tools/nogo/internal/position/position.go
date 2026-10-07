// Package position converts the token.Position values that some third-party
// checkers return back into token.Pos values an analysis.Pass can report.
package position

import (
	"go/token"

	"golang.org/x/tools/go/analysis"
)

// Index maps the file names of a pass's syntax files to their token.File.
type Index map[string]*token.File

// NewIndex indexes the files of pass by name.
func NewIndex(pass *analysis.Pass) Index {
	idx := make(Index, len(pass.Files))
	for _, f := range pass.Files {
		tf := pass.Fset.File(f.Pos())
		idx[tf.Name()] = tf
	}
	return idx
}

// Pos returns the token.Pos for p, and false when p is not in one of the
// pass's files or lies outside its file.
func (idx Index) Pos(p token.Position) (token.Pos, bool) {
	tf, ok := idx[p.Filename]
	if !ok || p.Line < 1 || p.Line > tf.LineCount() {
		return token.NoPos, false
	}
	pos := tf.LineStart(p.Line)
	if p.Column > 1 {
		pos += token.Pos(p.Column - 1)
	}
	if int(pos)-tf.Base() > tf.Size() {
		return token.NoPos, false
	}
	return pos, true
}
