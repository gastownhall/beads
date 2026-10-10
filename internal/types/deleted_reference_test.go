package types

import (
	"strings"
	"testing"
)

// rewriteAsTheRoleDoes applies the citation rule the way both delete bodies
// apply it to one field: one pass per deleted id, in the order given, each pass
// over the previous pass's result.
func rewriteAsTheRoleDoes(text string, deletedIDs ...string) string {
	for _, id := range deletedIDs {
		text = RewriteDeletedReferences(DeletedReferencePattern(id), text, id)
	}
	return text
}

// A hierarchical child's id is its parent's id plus `.<n>`, so a parent's id
// is a prefix of every descendant's. Deleting the parent must not rewrite a
// citation of a descendant that is still there: "step fx-k0008.1.2 failed"
// turning into "step [deleted:fx-k0008.1].2 failed" claims a row is gone that
// is not, and loses the id of the one that was cited.
func TestDeletedReferencePatternDoesNotMatchInsideDescendantID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		deleted string
		text    string
	}{
		{name: "grandchild cited mid-sentence", deleted: "fx-k0008.1", text: "step fx-k0008.1.2 failed"},
		{name: "grandchild is the whole field", deleted: "fx-k0008.1", text: "fx-k0008.1.2"},
		{name: "child of a top-level id", deleted: "fx-k0008", text: "see fx-k0008.1 for the plan"},
		{name: "descendant cited at the end of a sentence", deleted: "fx-k0008.1", text: "blocked on fx-k0008.1.2."},
		{name: "descendant in parentheses", deleted: "fx-k0008", text: "(fx-k0008.3)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// MatchString is what `bd delete`'s preview asks (cmd/bd/delete.go),
			// so a false match here lists a neighbor the deletion should not touch.
			if DeletedReferencePattern(tc.deleted).MatchString(tc.text) {
				t.Errorf("DeletedReferencePattern(%q) matches %q, which cites only a descendant", tc.deleted, tc.text)
			}
			if got := rewriteAsTheRoleDoes(tc.text, tc.deleted); got != tc.text {
				t.Errorf("deleting %q rewrote %q to %q; want it unchanged", tc.deleted, tc.text, got)
			}
		})
	}
}

// The edge the descendant rule must not break: a `.` that ends a sentence is
// not part of the id it follows, and neither is any other punctuation. Every
// citation here is rewritten, including a second one that shares its boundary
// character with the first.
func TestDeletedReferencePatternRewritesCitationBeforePunctuation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		deleted string
		text    string
		want    string
	}{
		{name: "end of sentence", deleted: "fx-k0008.1", text: "see fx-k0008.1.", want: "see [deleted:fx-k0008.1]."},
		{name: "comma", deleted: "fx-k0008.1", text: "fx-k0008.1, then the rest", want: "[deleted:fx-k0008.1], then the rest"},
		{name: "parentheses then period", deleted: "be-1", text: "see (be-1).", want: "see ([deleted:be-1])."},
		{name: "whole field", deleted: "fx-k0008.1", text: "fx-k0008.1", want: "[deleted:fx-k0008.1]"},
		{name: "period then newline", deleted: "fx-k0008.1", text: "done: fx-k0008.1.\nnext", want: "done: [deleted:fx-k0008.1].\nnext"},
		{name: "ellipsis", deleted: "fx-k0008.1", text: "waiting on fx-k0008.1...", want: "waiting on [deleted:fx-k0008.1]..."},
		{name: "colon", deleted: "fx-k0008.1", text: "fx-k0008.1: gone", want: "[deleted:fx-k0008.1]: gone"},
		{
			name:    "same id again after a sentence break",
			deleted: "fx-k0008.1",
			text:    "see fx-k0008.1. fx-k0008.1 is the parent",
			want:    "see [deleted:fx-k0008.1]. [deleted:fx-k0008.1] is the parent",
		},
		// One boundary character between two citations: a rewrite that resumed
		// after the whole match swallowed it and left the second verbatim.
		{name: "same id twice, one space apart", deleted: "be-1", text: "be-1 be-1", want: "[deleted:be-1] [deleted:be-1]"},
		{name: "same id twice, one comma apart", deleted: "be-1", text: "(be-1,be-1)", want: "([deleted:be-1],[deleted:be-1])"},
		// The word-boundary half the existing rule already gets right, kept so
		// the fix cannot trade one for the other.
		{name: "longer id sharing the prefix", deleted: "be-1", text: "be-12 and xbe-1", want: "be-12 and xbe-1"},
		{name: "hyphenated suffix", deleted: "be-1", text: "be-1-old", want: "be-1-old"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := rewriteAsTheRoleDoes(tc.text, tc.deleted); got != tc.want {
				t.Errorf("deleting %q rewrote %q to %q; want %q", tc.deleted, tc.text, got, tc.want)
			}
		})
	}
}

// One request can delete an ancestor and its descendant together (a cascade
// always does, and a `--from-file` list may name both in any order). Each id
// cited must come out as exactly its own marker, whichever id is rewritten
// first, and a marker an earlier delete left must not be rewritten into.
func TestDeletedReferenceRewriteLeavesOneMarkerPerID(t *testing.T) {
	t.Parallel()

	const (
		text = "step fx-k0008.1.2 failed; parent fx-k0008.1 is gone"
		want = "step [deleted:fx-k0008.1.2] failed; parent [deleted:fx-k0008.1] is gone"
	)
	tests := []struct {
		name    string
		deleted []string
		text    string
		want    string
	}{
		// The order a cascade hands the rewrite: SortedDeleteIDs, ancestor first.
		{name: "ancestor first", deleted: []string{"fx-k0008.1", "fx-k0008.1.2"}, text: text, want: want},
		{name: "descendant first", deleted: []string{"fx-k0008.1.2", "fx-k0008.1"}, text: text, want: want},
		{
			name:    "marker from an earlier delete",
			deleted: []string{"fx-k0008.1"},
			text:    "see [deleted:fx-k0008.1.2]",
			want:    "see [deleted:fx-k0008.1.2]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := rewriteAsTheRoleDoes(tc.text, tc.deleted...)
			if strings.Contains(got, "[deleted:[deleted:") {
				t.Errorf("deleting %q nested a marker inside a marker: %q", tc.deleted, got)
			}
			if got != tc.want {
				t.Errorf("deleting %q rewrote %q to %q; want %q", tc.deleted, tc.text, got, tc.want)
			}
		})
	}
}
