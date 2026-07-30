package issueops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// rowLockAssigned reports whether sql assigns row_lock in its SET clause, and
// not merely mentions it. Two near-misses have to fail: a WHERE-only reference
// (`WHERE id = ? AND row_lock = ?` reads the cell, it does not rewrite it, so
// it does nothing to force the conflict the fence bump needs) and a self-
// assignment (`row_lock = row_lock` writes the same value, which is exactly
// the silent cell-merge shape the invariant exists to prevent). The SET clause
// is everything ahead of the first WHERE — true of every ownership UPDATE we
// write, and of the ON DUPLICATE KEY UPDATE upsert form, which has no WHERE at
// all.
func rowLockAssigned(sql string) bool {
	setClause := sql
	if loc := whereKeyword.FindStringIndex(sql); loc != nil {
		setClause = sql[:loc[0]]
	}
	if rowLockNoOp.MatchString(setClause) {
		return false
	}
	return rowLockAssign.MatchString(setClause)
}

var (
	whereKeyword  = regexp.MustCompile(`(?i)(^|\W)where(\W|$)`)
	rowLockAssign = regexp.MustCompile(`row_lock\s*=`)
	rowLockNoOp   = regexp.MustCompile(`row_lock\s*=\s*row_lock\b`)
)

// TestRowLockAssignedRejectsNonAssignments self-tests the guard above: the
// pairing check is only worth anything if the near-misses fail it.
func TestRowLockAssignedRejectsNonAssignments(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		want bool
	}{
		{"set assignment", "UPDATE issues SET claim_fence = claim_fence + 1, row_lock = ? WHERE id = ?", true},
		{"set assignment multiline", "UPDATE issues\n\tSET claim_fence = claim_fence + 1,\n\t    row_lock = ?\n\tWHERE id = ?", true},
		{"upsert assignment", "INSERT ... ON DUPLICATE KEY UPDATE claim_fence = claim_fence + 1, row_lock = VALUES(row_lock)", true},
		{"where-only mention", "UPDATE issues SET claim_fence = claim_fence + 1 WHERE id = ? AND row_lock = ?", false},
		{"self-assignment", "UPDATE issues SET claim_fence = claim_fence + 1, row_lock = row_lock WHERE id = ?", false},
		{"column list only", "SELECT claim_fence, row_lock FROM issues WHERE id = ?", false},
	} {
		if got := rowLockAssigned(tc.sql); got != tc.want {
			t.Errorf("%s: rowLockAssigned = %v, want %v for:\n%s", tc.name, got, tc.want, tc.sql)
		}
	}
}

// TestFenceBumpAlwaysPairsRowLock enforces the claim_fence pairing invariant
// at the source level for LITERAL-form bumps: every SQL string literal that
// bumps claim_fence must ASSIGN row_lock in the same literal's SET clause — a
// WHERE-clause mention reads the cell without rewriting it and does not
// satisfy the invariant (see rowLockAssigned). A monotonic
// cell is exactly the write pattern Dolt cell-merges silently — two
// concurrent N→N+1 bumps produce identical cell values and no conflict — so
// the random row_lock rewrite is what forces racing ownership transitions to
// serialize (1213/1205 → withRetryTx replay). See freshRowLock in lease.go.
//
// Scope and honest limits: the scan covers this package AND the proxied
// domain/db repository (the two dispatch layers that hand-write ownership
// SQL). It cannot see fragment-composed statements — claim.go pairs
// fenceBumpExpr with RowLockClause, updateIssueInTx with its unconditional
// row_lock append, the upsert paths with their row_lock assignment — those
// pairings are asserted behaviorally by the fence tests in
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
			// shared upsert fragment builder; the fragment's pairing is owned
			// by the statement that composes it.
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
				if !rowLockAssigned(val) {
					t.Errorf("%s: SQL literal bumps claim_fence without ASSIGNING row_lock in the same statement's SET clause (a WHERE-clause mention or a row_lock = row_lock self-assignment does not count):\n%s",
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

	// The proxied-server repository is the second dispatch layer: it hand-writes
	// its own claim/update SQL and composes the bump as a fragment, which makes
	// it INVISIBLE to the literal scan in TestFenceBumpAlwaysPairsRowLock (the
	// literal there reads `%s` where the bump goes). Require the shared
	// FenceBumpExpr by name so deleting the proxied bump — or open-coding a
	// divergent one — fails here rather than only under a live proxied server.
	proxied, err := os.ReadFile(filepath.Clean("../domain/db/issue.go"))
	if err != nil {
		t.Fatalf("read ../domain/db/issue.go: %v", err)
	}
	if !strings.Contains(string(proxied), "issueops.FenceBumpExpr") {
		t.Error("../domain/db/issue.go performs ownership transitions on the proxied path and must compose issueops.FenceBumpExpr (its fragment-composed SQL is invisible to the literal scan)")
	}
}
