package encode

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// `bd list --title-contains X` folds onto the listing's `title` parameter: the
// store compiles TitleContains and TitleSearch to one predicate, so the fold
// is exact. A pair that differs other than in letter case is two predicates,
// so it is left unfolded and sends both `title` and `title_contains` (the
// latter behind issues.list.filters): dropping either half would widen the
// answer.
func TestFoldTitleContainsRidesTheTitleParameter(t *testing.T) {
	for _, tc := range []struct {
		name              string
		req               issueops.ListRequest
		wantTitle         []string
		wantTitleContains []string
	}{
		{name: "title-contains alone", req: issueops.ListRequest{TitleContains: "Routed"}, wantTitle: []string{"Routed"}},
		{name: "equal pair sends one title", req: issueops.ListRequest{TitleSearch: "Routed", TitleContains: "Routed"}, wantTitle: []string{"Routed"}},
		// The store lowers both values with strings.ToLower, so a pair that
		// differs only in letter case is one predicate too.
		{name: "pair differing only in case sends one title", req: issueops.ListRequest{TitleSearch: "Routed", TitleContains: "rOUTED"}, wantTitle: []string{"Routed"}},
		{name: "title alone is untouched", req: issueops.ListRequest{TitleSearch: "Routed"}, wantTitle: []string{"Routed"}},
		{name: "neither sends no title", req: issueops.ListRequest{}},
		{name: "differing pair sends both", req: issueops.ListRequest{TitleSearch: "Routed", TitleContains: "task"}, wantTitle: []string{"Routed"}, wantTitleContains: []string{"task"}},
		// strings.EqualFold matches "ſ" (long s) with "s", but the store lowers
		// them to different patterns, so this pair is two predicates.
		{name: "pair equal only under case folding sends both", req: issueops.ListRequest{TitleSearch: "ſtep", TitleContains: "step"}, wantTitle: []string{"ſtep"}, wantTitleContains: []string{"step"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			folded := FoldTitleContains(tc.req)
			params, err := ListParams(folded)
			if err != nil {
				t.Fatalf("ListParams(%+v): %v", folded, err)
			}
			if got := params["title"]; !reflect.DeepEqual(got, tc.wantTitle) {
				t.Errorf("title = %q, want %q", got, tc.wantTitle)
			}
			if got := params["title_contains"]; !reflect.DeepEqual(got, tc.wantTitleContains) {
				t.Errorf("title_contains = %q, want %q", got, tc.wantTitleContains)
			}
		})
	}
}
