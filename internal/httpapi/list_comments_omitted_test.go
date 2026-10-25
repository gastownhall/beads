package httpapi

import (
	"net/http"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestListRowsSayWhenCommentTextIsMissing is the HTTP half of be-73x's marker.
//
// `GET /v0/beads/issues` hydrates no comment bodies, so on this surface the
// marker IS the answer: it is the only thing that tells a caller whether an
// absent `comments` key means "this issue has none" or "this response does not
// carry them". The detail route's twin is pinned in get_issue_test.go; nothing
// asserted the list route's wire shape, which is the one route the field was
// added for.
//
// Driven through the server rather than over the epilogue directly, because the
// property at risk here is the SERIALIZATION: the marker rides a *bool with
// omitempty on the shared interchange struct, so a row that was never marked
// and a row marked false are both "absent", and only a response body shows
// which absence a client sees.
func TestListRowsSayWhenCommentTextIsMissing(t *testing.T) {
	ts, rec := newReadServer(t, Config{})
	rec.items = []*types.IssueWithCounts{
		{Issue: &types.Issue{ID: "bd-1", Title: "has comments"}, CommentCount: 2},
		{Issue: &types.Issue{ID: "bd-2", Title: "has none"}, CommentCount: 0},
	}

	resp := ts.get(t, "/v0/beads/issues")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v0/beads/issues: status = %d, want 200", resp.StatusCode)
	}
	body := decodeBody(t, resp)
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v, want the two rows the fixture serves", body["items"])
	}

	withComments, _ := items[0].(map[string]any)
	if got, ok := withComments["comments_omitted"]; !ok || got != true {
		t.Errorf("comments_omitted = %v (present %v) on a row with comment_count 2, want true: an absent key would read as \"no comments\"", got, ok)
	}
	if _, ok := withComments["comments"]; ok {
		t.Errorf("the list route sent `comments`, which it never hydrates: %v", withComments["comments"])
	}

	// The zero-count row is the control, and it is what makes the marker
	// informative: if every row carried it, it would say nothing about any row.
	withoutComments, _ := items[1].(map[string]any)
	if got, ok := withoutComments["comments_omitted"]; ok {
		t.Errorf("comments_omitted = %v on a row with no comments, want the key absent", got)
	}
}
