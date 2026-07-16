package issueops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestFenceBumpAlwaysPairsRowLock enforces the claim_fence pairing invariant
// at the source level for LITERAL-form bumps: every SQL string literal that
// bumps claim_fence must rewrite row_lock in the same literal. A monotonic
// cell is exactly the write pattern Dolt cell-merges silently — two
// concurrent N→N+1 bumps produce identical cell values and no conflict — so
// the random row_lock rewrite is what forces racing ownership transitions to
// serialize (1213/1205 → withRetryTx replay). See freshRowLock in lease.go.
//
// Scope and honest limits: the scan covers this package AND the proxied
// domain/db repository (the two dispatch layers that hand-write ownership
// SQL). It cannot see fragment-composed statements — claim.go pairs
// fenceBumpExpr with the lease clause, updateIssueInTx with its unconditional
// row_lock append, and the domain/db Update with an adjacent "row_lock = ?" —
// those pairings are asserted behaviorally by the fence tests in
// internal/storage/dolt (readFenceState checks row_lock movement on every
// bump).
func TestFenceBumpAlwaysPairsRowLock(t *testing.T) {
	dirs := []string{".", "../domain/db"}
	found := 0
	for _, dir := range dirs {
		fset := token.NewFileSet()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			// fence.go holds the bump expression's own definition and the
			// shared upsert fragment builder; the fragment carries its pairing
			// inside one literal, and bare fenceBumpExpr callers own theirs.
			if dir == "." && name == "fence.go" {
				continue
			}
			path := filepath.Join(dir, name)
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if !strings.Contains(val, "claim_fence = claim_fence +") {
					return true
				}
				found++
				pos := fset.Position(lit.Pos())
				if !strings.Contains(val, "row_lock") {
					t.Errorf("%s: SQL literal bumps claim_fence without rewriting row_lock in the same statement:\n%s",
						pos, val)
				}
				return true
			})
		}
	}

	if found == 0 {
		t.Fatal("no claim_fence bump literals found — the fence bump discipline moved or was removed; update this guard")
	}
}

// TestFenceTransitionCoverage enforces that every ownership-transition entry
// point in this package carries a fence bump. Transitions are enumerated
// semantically: any statement that writes assignee, or moves status across
// the closed boundary, changes the ownership context.
func TestFenceTransitionCoverage(t *testing.T) {
	// Files whose non-test SQL performs ownership transitions and therefore
	// must contain at least one fence bump. update.go builds its SET
	// dynamically; its bump is asserted via the fenceBumpExpr reference scan
	// below.
	mustBump := []string{"claim.go", "unclaim.go", "lease.go", "reopen.go"}
	for _, name := range mustBump {
		data, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(data), "claim_fence = claim_fence + 1") &&
			!strings.Contains(string(data), "fenceBumpExpr") {
			t.Errorf("%s performs ownership transitions but contains no claim_fence bump", name)
		}
	}

	// update.go: the dynamic SET builder must reference the shared bump
	// expression for its assignee-change / reopen branches.
	data, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatalf("read update.go: %v", err)
	}
	if !strings.Contains(string(data), "fenceBumpExpr") {
		t.Error("update.go must apply fenceBumpExpr on assignee-change and closed→open transitions")
	}
}
