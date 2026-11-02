package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
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

// TestRefScopeWithNames: a scope that knows the repository's other names (its
// current name, and a former name GitHub still resolves to it) accepts refs
// under each of them as well as under the configured name. The names do not
// mix (owner from one, repository from another), and a fullName that is not
// exactly owner/repo, or that the scope already accepts, adds nothing.
func TestRefScopeWithNames(t *testing.T) {
	scope := NewRefScope("https://api.github.com", "old-owner", "old-repo").
		withNames("New-Owner/new-repo", "mid-owner/mid-repo")
	for ref, want := range map[string]int{
		"https://github.com/old-owner/old-repo/issues/12":           12,
		"https://github.com/new-owner/new-repo/issues/12":           12,
		"https://github.com/NEW-OWNER/New-Repo/issues/12":           12,
		"https://api.github.com/repos/new-owner/new-repo/issues/12": 12,
		"https://github.com/mid-owner/mid-repo/issues/12":           12,
		"github:12": 12,
		"https://github.com/old-owner/new-repo/issues/12":      0,
		"https://github.com/new-owner/old-repo/issues/12":      0,
		"https://github.com/mid-owner/new-repo/issues/12":      0,
		"https://github.com/other/new-repo/issues/12":          0,
		"https://ghe.example.com/new-owner/new-repo/issues/12": 0,
	} {
		number, ok := scope.IssueNumberFromRef(ref)
		if number != want || ok != (want != 0) {
			t.Errorf("IssueNumberFromRef(%q) = %d, %v; want %d", ref, number, ok, want)
		}
	}

	base := NewRefScope("https://api.github.com", "old-owner", "old-repo")
	for _, fullName := range []string{"", " ", "new-owner", "/new-repo", "new-owner/", "a/b/c", "Old-Owner/OLD-REPO"} {
		if got := base.withNames(fullName); !reflect.DeepEqual(got, base) {
			t.Errorf("withNames(%q) = %+v, want the scope unchanged", fullName, got)
		}
	}
	if got := base.withNames("new-owner/new-repo", "NEW-owner/new-REPO").aliases; !slices.Equal(got, []string{"new-owner/new-repo"}) {
		t.Errorf("withNames with a repeated name recorded aliases %q, want it once", got)
	}

	// Copies extended from one scope do not share names: appending to one
	// never writes into the spare capacity another one sees.
	shared := base.withNames("a/one", "b/two", "c/three")
	first := shared.withNames("d/four")
	second := shared.withNames("e/five")
	if want := []string{"a/one", "b/two", "c/three", "d/four"}; !slices.Equal(first.aliases, want) {
		t.Errorf("first copy's aliases = %q, want %q", first.aliases, want)
	}
	if want := []string{"a/one", "b/two", "c/three", "e/five"}; !slices.Equal(second.aliases, want) {
		t.Errorf("second copy's aliases = %q, want %q", second.aliases, want)
	}
	if want := []string{"a/one", "b/two", "c/three"}; !slices.Equal(shared.aliases, want) {
		t.Errorf("original's aliases = %q, want %q", shared.aliases, want)
	}
}

// TestRefScopeOtherRepository: only an issue URL on the scope's host that
// names a repository the scope does not accept is another repository's.
func TestRefScopeOtherRepository(t *testing.T) {
	scope := NewRefScope("https://api.github.com", "owner-b", "repo-b").withNames("owner-b/new-b")
	for ref, want := range map[string]string{
		"https://github.com/Owner-A/Repo-A/issues/12":           "owner-a/repo-a",
		"https://api.github.com/repos/owner-a/repo-a/issues/12": "owner-a/repo-a",
		"https://github.com/owner-b/repo-b/issues/12":           "",
		"https://github.com/owner-b/new-b/issues/12":            "",
		"github:12": "",
		"https://ghe.example.com/owner-a/repo-a/issues/12": "",
		"https://github.com/owner-a/repo-a/pull/12":        "",
		"https://github.com/owner-a/repo-a":                "",
		"not a url":                                        "",
	} {
		got, ok := scope.otherRepository(ref)
		if got != want || ok != (want != "") {
			t.Errorf("otherRepository(%q) = %q, %v; want %q", ref, got, ok, want)
		}
	}
	if got, ok := (RefScope{}).otherRepository("https://github.com/owner-a/repo-a/issues/12"); ok {
		t.Errorf("zero scope otherRepository = %q, want none", got)
	}
}
