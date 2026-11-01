package issueops_test

import (
	"reflect"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

// TestSearchListRequestIsSearchsScope pins the library's search scope: every
// status, every kind of row, the store's default order.
func TestSearchListRequestIsSearchsScope(t *testing.T) {
	got := issueops.SearchListRequest("needle")
	want := issueops.ListRequest{
		Query: "needle", AllFlag: true,
		IncludeTemplates: true, IncludeGates: true, IncludeInfra: true, IncludeEphemeral: true,
		SortBy: "priority",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchListRequest = %+v, want %+v", got, want)
	}
}
