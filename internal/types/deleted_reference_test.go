package types

import (
	"strings"
	"testing"
)

// deletedReferenceCase is one input to the citation rule and the exact bytes
// RewriteDeletedReferences must return.
type deletedReferenceCase struct {
	name string
	text string
	id   string
	want string
}

// deletedReferenceCases is shared by the rewrite test and the CitesID
// agreement test, so the two cannot drift apart.
var deletedReferenceCases = []deletedReferenceCase{
	// A parent's id inside a descendant's id is not a citation of the parent.
	{"parent inside child", "step be-1.2 failed", "be-1", "step be-1.2 failed"},
	{"parent inside grandchild", "step fx-k0008.1.2 failed", "fx-k0008.1", "step fx-k0008.1.2 failed"},
	{"top-level inside grandchild", "step fx-k0008.1.2 failed", "fx-k0008", "step fx-k0008.1.2 failed"},
	{"child inside grandchild", "fx-k0008.1.2", "fx-k0008.1", "fx-k0008.1.2"},
	{"grandchild itself", "step fx-k0008.1.2 failed", "fx-k0008.1.2", "step [deleted:fx-k0008.1.2] failed"},

	// Longer ids that merely contain the deleted one.
	{"leading id byte", "xbe-1", "be-1", "xbe-1"},
	{"trailing digit", "be-12", "be-1", "be-12"},
	{"leading hyphen", "x-be-1", "be-1", "x-be-1"},
	{"trailing hyphen", "be-1-x", "be-1", "be-1-x"},
	{"trailing underscore", "be-1_x", "be-1", "be-1_x"},
	{"dot then letter", "be-1.a", "be-1", "be-1.a"},
	{"dot then underscore", "be-1._x", "be-1", "be-1._x"},
	{"dot then hyphen", "be-1.-x", "be-1", "be-1.-x"},
	{"id byte then dot before", "x.be-1", "be-1", "x.be-1"},

	// Punctuation around a complete citation is a boundary and survives.
	{"whole text", "be-1", "be-1", "[deleted:be-1]"},
	{"start of text", "be-1 closes", "be-1", "[deleted:be-1] closes"},
	{"end of text", "see be-1", "be-1", "see [deleted:be-1]"},
	{"sentence-ending dot", "see fx-k0008.1.", "fx-k0008.1", "see [deleted:fx-k0008.1]."},
	{"parentheses", "(be-1)", "be-1", "([deleted:be-1])"},
	{"colon", "be-1: done", "be-1", "[deleted:be-1]: done"},
	{"comma", "be-1, be-2", "be-1", "[deleted:be-1], be-2"},
	{"ellipsis", "be-1...", "be-1", "[deleted:be-1]..."},
	{"newline", "see\nbe-1\nnext", "be-1", "see\n[deleted:be-1]\nnext"},
	{"leading double dot", "..be-1", "be-1", "..[deleted:be-1]"},
	{"leading lone dot", ".be-1", "be-1", ".[deleted:be-1]"},
	{"dot then space after", "be-1. next", "be-1", "[deleted:be-1]. next"},
	{"non-ASCII punctuation", "«be-1»—be-1…", "be-1", "«[deleted:be-1]»—[deleted:be-1]…"},
	{"non-ASCII letter next to it", "ébe-1", "be-1", "é[deleted:be-1]"},

	// Degenerate inputs.
	{"empty text", "", "be-1", ""},
	{"empty id", "be-1 text", "", "be-1 text"},
	{"empty both", "", "", ""},
	{"no occurrence", "nothing here", "be-1", "nothing here"},
	{"regex metacharacters are literal", "see a+b(1) and aab(1)", "a+b(1)", "see [deleted:a+b(1)] and aab(1)"},
	{"replacement template is literal", "see $1x", "$1x", "see [deleted:$1x]"},
	{"metacharacter id does not match as a pattern", "see bdx1", "bd.1", "see bdx1"},
}

func TestRewriteDeletedReferences(t *testing.T) {
	for _, tc := range deletedReferenceCases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := RewriteDeletedReferences(tc.text, tc.id)
			if got != tc.want {
				t.Errorf("RewriteDeletedReferences(%q, %q) = %q, want %q", tc.text, tc.id, got, tc.want)
			}
			if changed != (tc.want != tc.text) {
				t.Errorf("RewriteDeletedReferences(%q, %q) reported changed=%v, want %v",
					tc.text, tc.id, changed, tc.want != tc.text)
			}
		})
	}
}

func TestCitesIDAgreesWithRewrite(t *testing.T) {
	for _, tc := range deletedReferenceCases {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := CitesID(tc.text, tc.id), tc.want != tc.text; got != want {
				t.Errorf("CitesID(%q, %q) = %v, want %v (whether the rewrite changes the text)", tc.text, tc.id, got, want)
			}
		})
	}
}

// TestRewriteDeletedReferencesIsOrderIndependent runs the rewrite once per
// deleted id, the way both delete bodies do, in every order of the ids. A
// cascade hands the ids ancestor first and a request may name them in any
// order, so the result must not depend on it.
func TestRewriteDeletedReferencesIsOrderIndependent(t *testing.T) {
	cases := []struct {
		name string
		text string
		ids  []string
		want string
	}{
		{
			name: "root, child and grandchild all cited",
			text: "root fx-k0008, child fx-k0008.1, grandchild fx-k0008.1.2.",
			ids:  []string{"fx-k0008", "fx-k0008.1", "fx-k0008.1.2"},
			want: "root [deleted:fx-k0008], child [deleted:fx-k0008.1], grandchild [deleted:fx-k0008.1.2].",
		},
		{
			name: "only the grandchild cited",
			text: "unblocks fx-k0008.1.2 when done",
			ids:  []string{"fx-k0008", "fx-k0008.1", "fx-k0008.1.2"},
			want: "unblocks [deleted:fx-k0008.1.2] when done",
		},
		{
			name: "child and grandchild",
			text: "step fx-k0008.1.2 failed; parent fx-k0008.1 is gone",
			ids:  []string{"fx-k0008.1", "fx-k0008.1.2"},
			want: "step [deleted:fx-k0008.1.2] failed; parent [deleted:fx-k0008.1] is gone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, order := range permutations(tc.ids) {
				text := tc.text
				for _, id := range order {
					text, _ = RewriteDeletedReferences(text, id)
				}
				if text != tc.want {
					t.Errorf("rewrite in order [%s] = %q, want %q", strings.Join(order, " "), text, tc.want)
				}
			}
		})
	}
}

// permutations returns every ordering of ids.
func permutations(ids []string) [][]string {
	if len(ids) <= 1 {
		return [][]string{append([]string(nil), ids...)}
	}
	var out [][]string
	for i := range ids {
		rest := make([]string, 0, len(ids)-1)
		rest = append(rest, ids[:i]...)
		rest = append(rest, ids[i+1:]...)
		for _, tail := range permutations(rest) {
			out = append(out, append([]string{ids[i]}, tail...))
		}
	}
	return out
}
