package types

import (
	"fmt"
	"testing"
)

// The citation rows every rewrite assertion shares: input text, the deleted
// id, and the exact bytes the rewrite must produce. Rows are grouped by the
// question they pin — hierarchy negatives, punctuation positives, adjacency —
// and TestCitesIDAgreesWithRewrite replays the same table against CitesID.
var deletedReferenceRows = []struct {
	name string
	text string
	id   string
	want string
}{
	// HIERARCHY NEGATIVES. A child's id is its parent's id plus ".N"
	// (GenerateChildID), so the parent's id occurs inside every descendant's
	// id, and a longer plain id contains a shorter one the same way. None of
	// these are citations; the text must come back byte-for-byte unchanged,
	// because the row that cited them is still live.
	{"child id", "step be-1.2 failed", "be-1", "step be-1.2 failed"},
	{"grandchild id", "see be-1.2.3 done", "be-1", "see be-1.2.3 done"},
	{"child of a child", "step be-1.2.3 failed", "be-1.2", "step be-1.2.3 failed"},
	{"longer id", "see be-12 now", "be-1", "see be-12 now"},
	{"id continues left", "xbe-1", "be-1", "xbe-1"},
	{"hyphen continues the id", "x-be-1", "be-1", "x-be-1"},
	{"hyphen continues right", "be-1-x", "be-1", "be-1-x"},
	{"underscore continues the id", "be-1_x", "be-1", "be-1_x"},
	{"dot plus letter", "be-1.a", "be-1", "be-1.a"},
	{"dot plus underscore", "be-1._x", "be-1", "be-1._x"},
	{"dot plus hyphen", "be-1.-x", "be-1", "be-1.-x"},
	{"dotted continuation on the left", "x.be-1", "be-1", "x.be-1"},
	{"dotted run of two ids", "bd-1.bd-2 and bd-2, bd-1", "bd-1", "bd-1.bd-2 and bd-2, [deleted:bd-1]"},
	{"prior descendant marker", "done via [deleted:be-1.2]", "be-1", "done via [deleted:be-1.2]"},

	// PUNCTUATION POSITIVES. A '.' that no id byte follows still ends a
	// citation, so a sentence-ending id rewrites; so does every other ASCII
	// and non-ASCII separator.
	{"whole text", "be-1", "be-1", "[deleted:be-1]"},
	{"sentence-final dot", "see be-1.", "be-1", "see [deleted:be-1]."},
	{"ellipsis", "be-1...", "be-1", "[deleted:be-1]..."},
	{"double dot before", "..be-1", "be-1", "..[deleted:be-1]"},
	{"leading dot alone", ".be-1", "be-1", ".[deleted:be-1]"},
	{"parentheses", "(be-1)", "be-1", "([deleted:be-1])"},
	{"colon", "note: be-1", "be-1", "note: [deleted:be-1]"},
	{"comma and newline", "be-1,\n", "be-1", "[deleted:be-1],\n"},
	{"trailing newline", "be-1\n", "be-1", "[deleted:be-1]\n"},
	{"non-ASCII punctuation", "«be-1»", "be-1", "«[deleted:be-1]»"},
	{"semicolon after", "see be-1; also be-1.", "be-1", "see [deleted:be-1]; also [deleted:be-1]."},

	// ADJACACENCY. Two citations may share the one separator byte between
	// them: the search resumes at the END of the id, never past the boundary,
	// so both rewrite. A consuming matcher (ReplaceAllString over a boundary
	// group) rewrites only the first of each pair.
	{"space adjacent", "be-1 be-1", "be-1", "[deleted:be-1] [deleted:be-1]"},
	{"comma adjacent", "be-1,be-1", "be-1", "[deleted:be-1],[deleted:be-1]"},
	{"sentence dot then space", "see be-1. be-1", "be-1", "see [deleted:be-1]. [deleted:be-1]"},
	{"sentence dot then newline", "be-1.\nbe-1", "be-1", "[deleted:be-1].\n[deleted:be-1]"},
	{"three in a row", "be-1 be-1 be-1", "be-1", "[deleted:be-1] [deleted:be-1] [deleted:be-1]"},

	// LITERAL HANDLING. The id is matched as bytes, never as a pattern or a
	// replacement template, and a dotted id is a first-class id.
	{"dotted id cited", "x be-1.2 y", "be-1.2", "x [deleted:be-1.2] y"},
	{"dotted id at sentence end", "see be-1.2.", "be-1.2", "see [deleted:be-1.2]."},
	{"replacement template bytes survive", "be-1 $1 ${id} be-1", "be-1", "[deleted:be-1] $1 ${id} [deleted:be-1]"},
	{"regex metacharacters in text", "be-1 (a+b)? [x] be-1", "be-1", "[deleted:be-1] (a+b)? [x] [deleted:be-1]"},
	{"metacharacters in the id", "x a*b y", "a*b", "x [deleted:a*b] y"},
}

func TestRewriteDeletedReferences(t *testing.T) {
	for _, tc := range deletedReferenceRows {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := RewriteDeletedReferences(tc.text, tc.id)
			if got != tc.want {
				t.Errorf("RewriteDeletedReferences(%q, %q) = %q, want %q", tc.text, tc.id, got, tc.want)
			}
			wantChanged := got != tc.text
			if changed != wantChanged {
				t.Errorf("RewriteDeletedReferences(%q, %q) changed = %v, want %v", tc.text, tc.id, changed, wantChanged)
			}
		})
	}

	t.Run("empty text", func(t *testing.T) {
		if got, changed := RewriteDeletedReferences("", "be-1"); got != "" || changed {
			t.Errorf("RewriteDeletedReferences(\"\", id) = (%q, %v), want (\"\", false)", got, changed)
		}
	})
	t.Run("empty id returns the text unchanged", func(t *testing.T) {
		const text = "be-1 and be-2"
		if got, changed := RewriteDeletedReferences(text, ""); got != text || changed {
			t.Errorf("RewriteDeletedReferences(text, \"\") = (%q, %v), want the text and false", got, changed)
		}
	})
}

func TestCitesID(t *testing.T) {
	for _, tc := range deletedReferenceRows {
		t.Run(tc.name, func(t *testing.T) {
			// A row whose expected output differs from its input is a row the
			// rewrite rewrites, which is exactly the set CitesID must report.
			// (A marker PRESENT IN THE EXPECTED OUTPUT would make a
			// contains-the-marker check lie, as the prior-descendant-marker
			// row shows.)
			want := tc.want != tc.text
			if got := CitesID(tc.text, tc.id); got != want {
				t.Errorf("CitesID(%q, %q) = %v, want %v", tc.text, tc.id, got, want)
			}
		})
	}
	t.Run("empty id never cites", func(t *testing.T) {
		if CitesID("be-1 be-1", "") {
			t.Error("CitesID(text, \"\") = true, want false")
		}
	})
}

// TestCitesIDAgreesWithRewrite pins the two entry points to ONE decision: the
// preview (CitesID) names exactly the rows the deletion would rewrite, so a
// divergence between them is a preview that lies, in whichever direction.
func TestCitesIDAgreesWithRewrite(t *testing.T) {
	texts := []string{
		"be-1", "be-1 be-1", "see be-1.", "step be-1.2 failed", "x.be-1", "be-1._x",
		"be-1,be-1 be-1.2", "..be-1 ... be-1.2.3", "no citations here", "", "be-1.2.3",
		"[deleted:be-1.2] cites be-1? no: be-1.2 is live",
	}
	ids := []string{"be-1", "be-1.2", "be-2", "be"}
	for _, text := range texts {
		for _, id := range ids {
			_, changed := RewriteDeletedReferences(text, id)
			if got := CitesID(text, id); got != changed {
				t.Errorf("CitesID(%q, %q) = %v but the rewrite changed the text: %v — the two must agree",
					text, id, got, changed)
			}
		}
	}
}

// TestRewriteDeletedReferencesIsOrderIndependent pins the property the
// per-id passes depend on: a deletion names several ids and the rewrite runs
// once per id in request order, so for ordinary bd ids the outcome must not
// depend on that order. A rule that only tightened the TRAILING boundary
// would leave `bd-1.bd-2` order-dependent — rewriting bd-1 first would expose
// bd-2 as a citation, and the reverse order would not — which is why the
// boundary rule is symmetric.
func TestRewriteDeletedReferencesIsOrderIndependent(t *testing.T) {
	permutations := func(ids ...string) (orders [][]string) {
		var walk func(prefix, rest []string)
		walk = func(prefix, rest []string) {
			if len(rest) == 0 {
				orders = append(orders, append([]string(nil), prefix...))
				return
			}
			for i, id := range rest {
				others := append([]string(nil), rest[:i]...)
				others = append(others, rest[i+1:]...)
				walk(append(prefix, id), others)
			}
		}
		walk(nil, ids)
		return orders
	}
	groups := []struct {
		name  string
		ids   []string
		cases []struct{ text, want string }
	}{
		{
			name: "root child grandchild",
			ids:  []string{"bd-1", "bd-1.2", "bd-1.2.3"},
			cases: []struct{ text, want string }{
				{"bd-1, bd-1.2 and bd-1.2.3 end", "[deleted:bd-1], [deleted:bd-1.2] and [deleted:bd-1.2.3] end"},
				{"root bd-1; child bd-1.2; grandchild bd-1.2.3.", "root [deleted:bd-1]; child [deleted:bd-1.2]; grandchild [deleted:bd-1.2.3]."},
			},
		},
		{
			name: "two roots in a dotted run",
			ids:  []string{"bd-1", "bd-2"},
			cases: []struct{ text, want string }{
				{"bd-1.bd-2 and bd-2, bd-1", "bd-1.bd-2 and [deleted:bd-2], [deleted:bd-1]"},
			},
		},
	}
	for _, group := range groups {
		for i, order := range permutations(group.ids...) {
			t.Run(fmt.Sprintf("%s order %d %v", group.name, i, order), func(t *testing.T) {
				for _, tc := range group.cases {
					text := tc.text
					for _, id := range order {
						next, _ := RewriteDeletedReferences(text, id)
						text = next
					}
					if text != tc.want {
						t.Errorf("ids %v over %q = %q, want %q", order, tc.text, text, tc.want)
					}
				}
			})
		}
	}
}
