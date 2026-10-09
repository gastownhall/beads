// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/divergence_citation_gate_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/httpclient/encode"
)

// The invariant this gate holds is "nothing skips without a ledger row": every
// served-tier park calls skipKnownDivergence(t, <row>, ...) naming the
// divergence-ledger id (encode/ledger.go) that says WHAT the wire cannot carry.
//
// Until now that binding was a STRING and nothing more. The row a park cites
// was checkable only by a reader who went and looked, so a row renamed or
// deleted in encode/ledger.go left its parks citing a coordinate that resolves
// to nothing — the exact drift the ledger's own RowByID panic and bijection
// gate exist to make impossible on the ENCODER side, but which the park side
// had no counterpart for. A park that cites a dead row still skips, still reads
// as "known divergence", and no test notices.
//
// This gate closes that gap by machine-binding each citation to a LIVE row.
// It parses the served test sources, collects every skipKnownDivergence park's
// cited id, and resolves each through the encoder's own lookup — the same
// RowByID a refusal raises through — failing on any id no live row carries. A
// park resolves here iff a refusal citing the same id would; the two can no
// longer disagree.
//
// It binds the row's KIND as well as its existence, because existence alone
// leaves the more likely rot: a row is far more often RETIRED than deleted, and
// a park citing a retired row resolves cleanly while skipping a case whose
// divergence upstream already closed. Retirement is the moment those cases must
// come back, so it is the moment this fails.
//
// It is deliberately UNTAGGED, for the reason divergence_test.go states about
// the helper it guards: the parks live behind //go:build cgo, but a gate that
// reads them as TEXT needs no build tag to do so, and one that carried the cgo
// tag would go blind in exactly the no-cgo build where a citation could rot
// unobserved. The encode package it resolves against is pure Go.
const parkHelperName = "skipKnownDivergence"

// parkCitation is one skipKnownDivergence call site: the ledger row it cites and
// where it cites it, so a failure names the park rather than just the id.
type parkCitation struct {
	row string
	pos string
}

func TestDivergenceParkCitationsResolveToLiveLedgerRows(t *testing.T) {
	cites := collectParkCitations(t, ".")
	if len(cites) == 0 {
		// Finding nothing is itself a failure: the helper was renamed or moved
		// out of this package and the gate now guards an empty set.
		t.Fatalf("found no %s parks to bind; the helper was renamed or moved and this gate now guards nothing", parkHelperName)
	}
	for _, c := range cites {
		row, ok := resolveLedgerRow(c.row)
		if !ok {
			t.Errorf("%s: %s parks on ledger row %q, which no live encoder-ledger row resolves (encode/ledger.go) — the row was renamed or deleted and this park now cites nothing",
				c.pos, parkHelperName, c.row)
			continue
		}
		// RESOLVING IS NOT ENOUGH, and the gap between the two is a park that
		// keeps skipping forever. A row's KIND is what says whether the wire
		// still cannot carry the thing: refuse and degrade are live divergences,
		// and KindRetired means the divergence is GONE — upstream published the
		// member, or the client started sending it. A case parked on a retired
		// row is a case whose reason expired, and because the park is a
		// t.Skipf it goes on reporting itself as a known divergence rather than
		// as coverage that should have come back. Retiring a row is exactly when
		// its parks must be re-run, so it is exactly when this has to fail.
		if row.Kind == encode.KindRetired {
			t.Errorf("%s: %s parks on ledger row %q, which is RETIRED — the divergence it cites no longer exists, so this case is skipping for a reason that expired. Unpark it (and delete the citation), or move the park onto the row that describes what still diverges",
				c.pos, parkHelperName, c.row)
		}
	}
}

// resolveLedgerRow returns the row an id names through the encoder's own lookup —
// the one every refusal raises through.
//
// RowByID panics on an unknown id, which encode/ledger.go documents as the
// correct severity for a refusal. The gate recovers it so a dead citation reads
// as a located test failure instead of a raw stack trace, and so one dead park
// does not hide the next.
func resolveLedgerRow(id string) (row encode.Row, ok bool) {
	defer func() {
		if recover() != nil {
			row, ok = encode.Row{}, false
		}
	}()
	return encode.RowByID(id), true
}

// collectParkCitations reads the row every skipKnownDivergence park cites out of
// the sources rather than from a hand-kept list, for the reason the ledger's own
// gates are derived rather than restated: a second copy is a second thing that
// can be wrong. It scans this package's test files only, because the helper is
// package-private and a park elsewhere could not call it by bare name.
func collectParkCitations(t *testing.T, dir string) []parkCitation {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var cites []parkCitation
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != parkHelperName {
				return true
			}
			pos := fset.Position(call.Pos()).String()
			// The signature is skipKnownDivergence(t, row, beadID, reason): the
			// receiver comes first and the cited row is the second argument.
			if len(call.Args) < 2 {
				t.Errorf("%s: %s(...) has too few arguments to carry a ledger-row citation", pos, parkHelperName)
				return true
			}
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				// A citation the gate cannot read statically defeats the whole
				// invariant: the row a park names has to be a fact this test can
				// resolve, not one computed at runtime.
				t.Errorf("%s: %s cites a non-literal ledger row (%T); the citation must be a string literal so it can be bound to a live row",
					pos, parkHelperName, call.Args[1])
				return true
			}
			row, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Errorf("%s: cannot read ledger-row citation %s: %v", pos, lit.Value, err)
				return true
			}
			cites = append(cites, parkCitation{row: row, pos: pos})
			return true
		})
	}
	return cites
}
