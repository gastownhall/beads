package issueops

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestDeletedReferenceHierarchyRepro applies the citation rule exactly as
// RewriteDeletedReferencesInTx does: one pass per deleted id, ids in the order
// the request named them. A parent's id must not match inside a descendant's
// dotted id, while a sentence-ending '.' still ends a citation.
func TestDeletedReferenceHierarchyRepro(t *testing.T) {
	rewrite := func(text string, ids ...string) string {
		for _, id := range ids {
			next, _ := types.RewriteDeletedReferences(text, id)
			text = next
		}
		return text
	}
	cases := []struct {
		name string
		text string
		ids  []string
		want string
	}{
		{"parent inside grandchild", "step fx-k0008.1.2 failed", []string{"fx-k0008.1"}, "step fx-k0008.1.2 failed"},
		{"descendant first then ancestor", "step fx-k0008.1.2 failed; parent fx-k0008.1 is gone",
			[]string{"fx-k0008.1.2", "fx-k0008.1"}, "step [deleted:fx-k0008.1.2] failed; parent [deleted:fx-k0008.1] is gone"},
		{"ancestor first then descendant", "step fx-k0008.1.2 failed; parent fx-k0008.1 is gone",
			[]string{"fx-k0008.1", "fx-k0008.1.2"}, "step [deleted:fx-k0008.1.2] failed; parent [deleted:fx-k0008.1] is gone"},
		{"cascade ancestor first", "unblocks fx-k0008.1.2 when done",
			[]string{"fx-k0008", "fx-k0008.1", "fx-k0008.1.2"}, "unblocks [deleted:fx-k0008.1.2] when done"},
		{"sentence-ending dot still a boundary", "see fx-k0008.1.", []string{"fx-k0008.1"}, "see [deleted:fx-k0008.1]."},
		{"adjacent citations share one separator", "fx-k0008.1 fx-k0008.1",
			[]string{"fx-k0008.1"}, "[deleted:fx-k0008.1] [deleted:fx-k0008.1]"},
		{"prior descendant marker stays alone", "done via [deleted:fx-k0008.1.2]",
			[]string{"fx-k0008.1"}, "done via [deleted:fx-k0008.1.2]"},
	}
	for _, tc := range cases {
		if got := rewrite(tc.text, tc.ids...); got != tc.want {
			t.Errorf("%s: rewrite(%q, %v) = %q, want %q", tc.name, tc.text, tc.ids, got, tc.want)
		}
	}
}
