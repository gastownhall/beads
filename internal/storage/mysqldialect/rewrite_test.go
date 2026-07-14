package mysqldialect

import (
	"regexp"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
)

func TestRewriteSelfRefUpdate(t *testing.T) {
	recompute := `UPDATE issues i SET i.is_blocked = 1, i.updated_at = i.updated_at
		WHERE i.id IN (?,?,?)
		  AND i.is_blocked = 0
		  AND i.status <> 'closed' AND i.status <> 'pinned'
		  AND (EXISTS (SELECT 1 FROM dependencies d JOIN issues t ON t.id = d.depends_on_issue_id
		        WHERE d.issue_id = i.id AND d.type = 'blocks' AND t.status <> 'closed'))`

	got := rewriteSelfRefUpdate(recompute)
	if got == recompute {
		t.Fatal("recompute UPDATE was not rewritten")
	}
	// The outer target must no longer be referenced directly in a subquery: the
	// materialized derived table carries the self-reference.
	if !strings.Contains(got, "SELECT id FROM (SELECT _b.id FROM issues _b") {
		t.Fatalf("missing double-nested derived table:\n%s", got)
	}
	// The correlation must be rebound to the derived-table alias.
	if !strings.Contains(got, "d.issue_id = _b.id") {
		t.Fatalf("alias not rebound to _b:\n%s", got)
	}
	// The outer SET still targets the real alias.
	if !strings.HasPrefix(strings.TrimSpace(got), "UPDATE issues i SET i.is_blocked = 1") {
		t.Fatalf("outer UPDATE target changed:\n%s", got)
	}
	// Placeholder count preserved (positional binding).
	if a, b := strings.Count(recompute, "?"), strings.Count(got, "?"); a != b {
		t.Fatalf("placeholder count changed: %d -> %d", a, b)
	}
}

// TestRewriteMatchesRealRecomputeTemplates drives the rewrite from the ACTUAL
// issueops is_blocked recompute templates, not a hand-copied fixture. The rewrite
// regex is coupled to the exact shape those templates emit (UPDATE <tbl> <alias> SET
// … WHERE <alias>.id IN (…) <self-ref EXISTS>); if a template later grows a predicate
// before `id IN`, backticks the table, renames the alias, or changes whitespace, the
// regex would silently stop matching and MySQL 8 would raise error 1093 at runtime on
// a core write path. That failure would otherwise only surface under the env-gated
// MySQL conformance suite — this non-gated test makes template drift fail `go test`.
func TestRewriteMatchesRealRecomputeTemplates(t *testing.T) {
	templates := issueops.SelfReferentialRecomputeTemplates()
	if len(templates) == 0 {
		t.Fatal("issueops.SelfReferentialRecomputeTemplates() returned no templates")
	}
	// Parse the UPDATE target table+alias so the assertions can target the OUTER
	// self-reference specifically (the inner materialized `_b.id IN (?…)` is expected).
	updateHead := regexp.MustCompile(`(?is)^\s*UPDATE\s+(\w+)\s+(\w+)\s+SET\b`)
	for i, tmpl := range templates {
		m := updateHead.FindStringSubmatch(tmpl)
		if m == nil {
			t.Errorf("template[%d] is not an `UPDATE <table> <alias> SET …` shape:\n%s", i, tmpl)
			continue
		}
		table, alias := m[1], m[2]

		got := rewriteSelfRefUpdate(tmpl)
		if got == tmpl {
			t.Errorf("template[%d] was NOT rewritten — the rewrite regex no longer matches an is_blocked "+
				"recompute template; MySQL error 1093 would return at runtime:\n%s", i, tmpl)
			continue
		}
		// The target table must be materialized in a derived table so the outer UPDATE
		// no longer references it directly in a subquery (the 1093 avoidance).
		if want := "SELECT id FROM (SELECT _b.id FROM " + table + " _b"; !strings.Contains(got, want) {
			t.Errorf("template[%d] rewrite missing the double-nested derived table %q:\n%s", i, want, got)
		}
		// The OUTER target alias must now select from the materialized set, not a bare
		// self-referential `<alias>.id IN (?…)`.
		outerMaterialized := regexp.MustCompile(`(?i)\b` + alias + `\.id\s+IN\s+\(\s*SELECT`)
		if !outerMaterialized.MatchString(got) {
			t.Errorf("template[%d] outer %s.id IN (…) was not materialized into a subselect:\n%s", i, alias, got)
		}
		outerRaw := regexp.MustCompile(`(?i)\b` + alias + `\.id\s+IN\s+\(\s*\?`)
		if outerRaw.MatchString(got) {
			t.Errorf("template[%d] outer %s.id IN (?…) is still a raw self-reference after rewrite (MySQL 1093):\n%s", i, alias, got)
		}
		// Positional binding is preserved (placeholders unchanged).
		if a, b := strings.Count(tmpl, "?"), strings.Count(got, "?"); a != b {
			t.Errorf("template[%d] placeholder count changed: %d -> %d", i, a, b)
		}
	}
}

func TestRewritePassThrough(t *testing.T) {
	cases := []string{
		"SELECT * FROM issues WHERE id = ?",
		"UPDATE issues SET title = ? WHERE id = ?",                 // no is_blocked, no IN
		"UPDATE issues i SET i.is_blocked = 1 WHERE i.id IN (?,?)", // batch update, no self-ref conditions
		"INSERT INTO issues (id) VALUES (?)",
		"DELETE FROM issues WHERE id = ?",
	}
	for _, q := range cases {
		if got := rewriteSelfRefUpdate(q); got != q {
			t.Errorf("query was rewritten but should pass through:\n in: %s\nout: %s", q, got)
		}
	}
}

// The rewrite must not leave the outer target table referenced in a subquery in a way
// that regex could double-apply; a second pass is a no-op on already-rewritten SQL.
func TestRewriteIdempotentShape(t *testing.T) {
	q := `UPDATE wisps w SET w.is_blocked = 0, w.updated_at = w.updated_at
		WHERE w.id IN (?) AND w.is_blocked = 1 AND (w.status = 'closed')`
	once := rewriteSelfRefUpdate(q)
	if once == q {
		t.Fatal("expected rewrite")
	}
	// A plain "?,?" derived form should still parse-shape sanely (no target alias left
	// as a bare self-join outside the derived table).
	if regexp.MustCompile(`WHERE w\.id IN \(\?`).MatchString(once) {
		t.Fatalf("outer WHERE still has a raw placeholder IN (self-ref not materialized):\n%s", once)
	}
}
