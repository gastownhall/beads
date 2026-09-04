package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListRelationshipPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "1" {
			w.Header().Set("Link", "<"+"http://"+r.Host+r.URL.Path+"?page=2&per_page=100>; rel=\"next\"")
		}
		if page == "2" {
			_ = json.NewEncoder(w).Encode([]Issue{{Number: 2}})
			return
		}
		_ = json.NewEncoder(w).Encode([]Issue{{Number: 1}})
	}))
	defer server.Close()

	client := NewClient("token", "owner", "repo").WithBaseURL(server.URL)
	for _, get := range []struct {
		name string
		fn   func(context.Context) ([]Issue, error)
	}{
		{"sub-issues", func(ctx context.Context) ([]Issue, error) { return client.ListSubIssues(ctx, 7) }},
		{"blocked-by", func(ctx context.Context) ([]Issue, error) { return client.ListBlockedBy(ctx, 7) }},
	} {
		t.Run(get.name, func(t *testing.T) {
			issues, err := get.fn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(issues) != 2 || issues[0].Number != 1 || issues[1].Number != 2 {
				t.Fatalf("issues = %+v, want both pages", issues)
			}
		})
	}
}

func TestRefScopeRejectsForeignHostAndRepository(t *testing.T) {
	scope := NewRefScope("https://api.github.com", "owner", "repo")
	for _, ref := range []string{
		"https://gitlab.com/owner/repo/issues/12",
		"https://github.com/other/repo/issues/12",
		"https://github.com/owner/other/issues/12",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); ok {
			t.Fatalf("IssueNumberFromRef(%q) = %d, true; want rejected", ref, number)
		}
	}
	if number, ok := scope.IssueNumberFromRef("https://github.com/owner/repo/issues/12"); !ok || number != 12 {
		t.Fatalf("configured ref = %d, %v; want 12, true", number, ok)
	}
}
