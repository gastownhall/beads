package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ListIssueComments must follow Link-header pagination and parse the comment
// fields the pull path relies on (stable numeric ID, author login, body,
// created timestamp).
func TestListIssueComments_Paginates(t *testing.T) {
	page1 := []IssueComment{
		{ID: 101, User: &User{Login: "alice"}, Body: "first", CreatedAt: ptrTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))},
	}
	page2 := []IssueComment{
		{ID: 102, User: &User{Login: "bob"}, Body: "second", CreatedAt: ptrTime(time.Date(2026, 1, 3, 3, 4, 5, 0, time.UTC))},
	}

	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/issues/7/comments" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/owner/repo/issues/7/comments?per_page=%d&page=2>; rel="next"`, srvURL(r), MaxPerPage))
			_ = json.NewEncoder(w).Encode(page1)
		default:
			_ = json.NewEncoder(w).Encode(page2)
		}
	}))
	defer srv.Close()

	c := newRateLimitTestClient(srv.URL)
	got, err := c.ListIssueComments(context.Background(), 7)
	if err != nil {
		t.Fatalf("ListIssueComments() error: %v", err)
	}

	if len(pages) != 2 {
		t.Fatalf("fetched %d pages (%v), want 2", len(pages), pages)
	}
	if len(got) != 2 {
		t.Fatalf("got %d comments, want 2", len(got))
	}
	if got[0].ID != 101 || got[0].User.Login != "alice" || got[0].Body != "first" {
		t.Errorf("comment[0] = %+v, want id 101/alice/first", got[0])
	}
	if got[1].ID != 102 || got[1].User.Login != "bob" || got[1].Body != "second" {
		t.Errorf("comment[1] = %+v, want id 102/bob/second", got[1])
	}
	if got[0].CreatedAt == nil || got[0].CreatedAt.Year() != 2026 {
		t.Errorf("comment[0].CreatedAt = %v, want parsed timestamp", got[0].CreatedAt)
	}
}

func TestListIssueComments_Empty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := newRateLimitTestClient(srv.URL)
	got, err := c.ListIssueComments(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListIssueComments() error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d comments, want 0", len(got))
	}
}

// The issues-list payload's comment count must survive unmarshalling so the
// tracker knows which issues need a follow-up thread fetch.
func TestIssueUnmarshal_CommentCount(t *testing.T) {
	raw := []byte(`{"id":1,"number":2,"title":"t","body":"b","state":"open","comments":3}`)
	var issue Issue
	if err := json.Unmarshal(raw, &issue); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if issue.Comments != 3 {
		t.Errorf("Comments = %d, want 3", issue.Comments)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func srvURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
