package issueops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// issueTableWriteRe matches the start of an UPDATE or INSERT to an issue-bearing
// table: literal `issues`/`wisps`, or a `%s`-templated table (the routed
// issues/wisps funnels build the table name with WispTableRouting/pickIssueTable
// and interpolate it as %s). Auxiliary tables reached through the same %s
// templating are filtered out by their distinguishing columns (auxOrExemptMarkers).
var issueTableWriteRe = regexp.MustCompile(`(?i)\b(?:UPDATE|INSERT\s+INTO)\s+(?:issues|wisps|%s)\b`)

// auxOrExemptMarkers identify a matched write that must NOT stamp revision:
//   - is_blocked: the denormalized is_blocked recompute deliberately preserves
//     updated_at (blocked_state.go x4 + dependencies.go markDirect...), so it must
//     not bump revision or it would clobber a concurrent whole-row CAS.
//   - lease-heartbeat: the lease keepalive (HeartbeatIssueInTx) refreshes only the
//     liveness columns (lease_expires_at/heartbeat_at/row_lock, assembled off in the
//     leaseClause), never issue content, so it must not bump revision — a heartbeat
//     every few seconds would otherwise spuriously fail a whole-row CAS on content.
//     (Reclaim and close DO revert/clear leases AND change content, so they bump.)
//   - the remainder are columns unique to AUXILIARY tables reached through the same
//     %s templating (events / dependencies / child_counter / snapshots). Issue rows
//     key on `id` and never carry these, so their presence proves a non-issue table.
//
// If a future write trips this guard: stamp revision = NewRevision() when it is a
// real issues/wisps content write; add the distinguishing column/marker here when
// it is a new auxiliary-table or lease-only write.
var auxOrExemptMarkers = []string{
	"is_blocked",      // is_blocked recompute (exempt by design)
	"lease-heartbeat", // lease keepalive: liveness columns only (exempt by design)
	"issue_id",        // events / dependencies / snapshots FK
	"depends_on",      // dependency edge + rekey writes
	"parent_id",       // child_counter
	"last_child",      // child_counter
	"event_type",      // events / wisp_events
}

// TestAllIssueRowWritesBumpRevision is the load-bearing completeness guard for
// B1.2: it proves, at build time, that EVERY issues/wisps row write across both
// whole-row write stacks (issueops and the proxied internal/storage/domain/db)
// stamps a fresh revision nonce. A forgotten write path is a test failure, not a
// silent hole in the optimistic-concurrency guarantee.
func TestAllIssueRowWritesBumpRevision(t *testing.T) {
	dirs := []string{".", filepath.Join("..", "domain", "db")}
	checked := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			n, violations := scanIssueWriteRevisionStamps(t, path, src)
			checked += n
			for _, v := range violations {
				t.Error(v)
			}
		}
	}
	if checked == 0 {
		t.Fatal("guard verified zero issue-table writes — the scan regex or directory set is wrong")
	}
	t.Logf("verified %d issues/wisps row writes across both stacks stamp revision", checked)
}

// scanIssueWriteRevisionStamps parses one Go source file and returns the number
// of issue-table writes it verified plus a violation message for each such write
// whose enclosing function does not stamp revision. Split out so the guard's
// teeth can be tested against synthetic sources (see TestRevisionGuardHasTeeth).
func scanIssueWriteRevisionStamps(t *testing.T, path string, src []byte) (checked int, violations []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		start := fset.Position(fn.Body.Pos()).Offset
		end := fset.Position(fn.Body.End()).Offset
		body := string(src[start:end])
		// The routed funnels (update.go, domain/db, conditional.go) assemble the
		// SET clause separately from the UPDATE literal, so verify the stamp
		// against the whole function body, not just the write literal.
		stampsRevision := strings.Contains(body, "revision")

		for _, loc := range issueTableWriteRe.FindAllStringIndex(body, -1) {
			// Classify by the tight enclosing SQL literal so an adjacent
			// statement's columns can't bleed into this write's markers.
			stmt := enclosingSQLLiteral(body, loc[0])
			if hasAnyMarker(stmt, auxOrExemptMarkers) {
				continue // auxiliary table or is_blocked recompute: no stamp
			}
			checked++
			if !stampsRevision {
				violations = append(violations, path+": "+funcDisplayName(fn)+
					" performs an issues/wisps row write that does not stamp revision:\n\t"+
					firstSQLLine(stmt)+
					"\nEvery issues/wisps write must set revision = NewRevision() (B1.2).")
			}
		}
		return true
	})
	return checked, violations
}

// TestRevisionGuardHasTeeth proves the guard actually flags an unstamped
// issues/wisps write and passes a correctly-stamped one — so a green
// TestAllIssueRowWritesBumpRevision means something.
func TestRevisionGuardHasTeeth(t *testing.T) {
	stamped := []byte(`package p
func w(tx T, id, s string) {
	tx.ExecContext(ctx, "UPDATE issues SET status = ?, revision = ? WHERE id = ?", s, NewRevision(), id)
}`)
	if n, v := scanIssueWriteRevisionStamps(t, "stamped.go", stamped); len(v) != 0 || n != 1 {
		t.Errorf("stamped write: got checked=%d violations=%v; want checked=1 violations=none", n, v)
	}

	unstamped := []byte(`package p
func w(tx T, id, s string) {
	tx.ExecContext(ctx, "UPDATE issues SET status = ? WHERE id = ?", s, id)
}`)
	if n, v := scanIssueWriteRevisionStamps(t, "unstamped.go", unstamped); n != 1 || len(v) != 1 {
		t.Errorf("unstamped write: got checked=%d violations=%d; want checked=1 violations=1", n, len(v))
	}

	// An auxiliary-table write (has issue_id) must be ignored, stamped or not.
	aux := []byte(`package p
func w(tx T, id, s string) {
	tx.ExecContext(ctx, "INSERT INTO %s (id, issue_id, event_type) VALUES (?, ?, ?)", a, b, c)
}`)
	if n, v := scanIssueWriteRevisionStamps(t, "aux.go", aux); n != 0 || len(v) != 0 {
		t.Errorf("aux write: got checked=%d violations=%v; want checked=0 violations=none", n, v)
	}
}

func hasAnyMarker(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func funcDisplayName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		if t, ok := receiverTypeName(fn.Recv.List[0].Type); ok {
			return "(" + t + ")." + fn.Name.Name
		}
	}
	return fn.Name.Name
}

func receiverTypeName(expr ast.Expr) (string, bool) {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name, true
	}
	return "", false
}

func firstSQLLine(window string) string {
	for _, line := range strings.Split(window, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(window)
}

// enclosingSQLLiteral returns the Go string literal (backtick-raw or
// double-quoted) that contains the byte at matchStart, without its delimiters.
// The SQL write statements here never span more than one literal, and neither
// delimiter appears inside these SQL bodies (which use single quotes), so a
// nearest-delimiter scan bounds the statement exactly — no bleed into adjacent
// statements. Falls back to a fixed window if no delimiter is found.
func enclosingSQLLiteral(body string, matchStart int) string {
	open, delim := -1, byte(0)
	for i := matchStart; i >= 0; i-- {
		if body[i] == '`' || body[i] == '"' {
			open, delim = i, body[i]
			break
		}
	}
	if open < 0 {
		end := matchStart + 400
		if end > len(body) {
			end = len(body)
		}
		return body[matchStart:end]
	}
	for i := open + 1; i < len(body); i++ {
		if body[i] == delim && (delim == '`' || body[i-1] != '\\') {
			return body[open+1 : i]
		}
	}
	return body[open+1:]
}
