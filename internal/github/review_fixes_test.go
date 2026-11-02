package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
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
		"https://gitlab.com/owner/repo/-/issues/42",
		"https://gitlab.com/owner/repo/issues/42",
		"https://github.com/other/repo/issues/42",
		"https://github.com/owner/other/issues/42",
		"https://github.com/owner/repo/-/issues/42",
		"https://github.com/evil/owner/repo/issues/42",
		"https://github.com/owner/repo/pull/42",
		"https://ghe.example.com/owner/repo/issues/42",
		"ftp://github.com/owner/repo/issues/42",
		"gitlab:42",
		"42",
		"",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); ok {
			t.Errorf("IssueNumberFromRef(%q) = %d, true; want rejected", ref, number)
		}
	}
	for _, ref := range []string{
		"https://github.com/owner/repo/issues/42",
		"https://github.com/Owner/Repo/issues/42",
		"https://api.github.com/repos/owner/repo/issues/42",
		"github:42",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); !ok || number != 42 {
			t.Errorf("IssueNumberFromRef(%q) = %d, %v; want 42, true", ref, number, ok)
		}
	}
}

func TestRefScopeGitHubEnterprise(t *testing.T) {
	scope := NewRefScope("https://ghe.example.com/api/v3", "owner", "repo")
	for _, ref := range []string{
		"https://ghe.example.com/owner/repo/issues/42",
		"https://ghe.example.com/api/v3/repos/owner/repo/issues/42",
		"github:42",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); !ok || number != 42 {
			t.Errorf("IssueNumberFromRef(%q) = %d, %v; want 42, true", ref, number, ok)
		}
	}
	for _, ref := range []string{
		"https://github.com/owner/repo/issues/42",
		"https://gitlab.example.com/owner/repo/-/issues/42",
	} {
		if number, ok := scope.IssueNumberFromRef(ref); ok {
			t.Errorf("IssueNumberFromRef(%q) = %d, true; want rejected", ref, number)
		}
	}
}

func TestRefScopeLinksSkipForeignRefs(t *testing.T) {
	scope := NewRefScope("https://api.github.com", "owner", "repo")
	child := githubIssue("bd-child", "https://github.com/owner/repo/issues/10", types.TypeTask)
	for _, ref := range []string{
		"https://gitlab.com/owner/repo/-/issues/42",
		"https://github.com/other/repo/issues/42",
	} {
		parent := githubDep("bd-parent", ref, types.DepParentChild)
		if link, ok := scope.SubIssueLinkFromParentChild(child, parent); ok {
			t.Errorf("sub-issue link from %q = %+v; want skipped", ref, link)
		}
		blocker := githubDep("bd-blocker", ref, types.DepBlocks)
		if link, ok := scope.BlockedByLinkFromBeadsDependency(child, blocker); ok {
			t.Errorf("blocked_by link from %q = %+v; want skipped", ref, link)
		}
	}
}

// TestPushLinksSkipsUnsupportedLinkTypeAfterFirst404 pins nit 8 of the #5971
// review: GHES (or a repo with the feature off) answers 404 on the
// relationship endpoints. The first 404 disables that link type for the rest
// of the pass, with no further calls and no per-issue errors.
func TestPushLinksSkipsUnsupportedLinkTypeAfterFirst404(t *testing.T) {
	var blockedByCalls, subIssueLists, subIssuePosts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/dependencies/blocked_by"):
			// Unsupported on list.
			blockedByCalls++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case strings.HasSuffix(r.URL.Path, "/sub_issues") && r.Method == http.MethodGet:
			subIssueLists++
			_ = json.NewEncoder(w).Encode([]Issue{})
		case strings.HasSuffix(r.URL.Path, "/sub_issues") && r.Method == http.MethodPost:
			// Listing works but creating is unsupported.
			subIssuePosts++
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(Issue{ID: 1000, Number: 1})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	res := newTestTracker(server.URL).PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 10, ToNumber: 1, LinkType: githubLinkBlockedBy},
		{FromNumber: 11, ToNumber: 1, LinkType: githubLinkBlockedBy},
		{FromNumber: 12, ToNumber: 1, LinkType: githubLinkBlockedBy},
		{FromNumber: 20, ToNumber: 2, LinkType: githubLinkSubIssue},
		{FromNumber: 21, ToNumber: 2, LinkType: githubLinkSubIssue},
	}, PushLinkOptions{})

	if len(res.Errors) != 0 {
		t.Fatalf("Errors = %v, want 404s degraded, not reported per issue", res.Errors)
	}
	if blockedByCalls != 1 || subIssueLists != 1 || subIssuePosts != 1 {
		t.Fatalf("calls: blocked_by=%d sub_issue lists=%d posts=%d; want one each", blockedByCalls, subIssueLists, subIssuePosts)
	}
	if res.Created != 0 || res.UnsupportedSkipped != 5 {
		t.Fatalf("Created = %d, UnsupportedSkipped = %d; want 0 and 5", res.Created, res.UnsupportedSkipped)
	}
	if len(res.Unsupported) != 2 || res.Unsupported[0] != githubLinkBlockedBy || res.Unsupported[1] != githubLinkSubIssue {
		t.Fatalf("Unsupported = %v, want [blocked_by sub_issue]", res.Unsupported)
	}
}

// TestPushLinksMissingSourceDoesNotDisableLinkType: a deleted or transferred
// source issue also answers 404 on the relationship endpoints. That must skip
// only the missing issue, not turn the whole link type off.
func TestPushLinksMissingSourceDoesNotDisableLinkType(t *testing.T) {
	notFound := func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	}
	var probes = map[string]int{}
	var posts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/repos/o/r/issues/")
		switch {
		// #10 is gone: every endpoint under it 404s.
		case strings.HasPrefix(path, "10"):
			if path == "10" {
				probes[path]++
			}
			notFound(w)
		// #12 exists and lists fine, but was transferred before the create.
		case path == "12":
			probes[path]++
			notFound(w)
		case r.Method == http.MethodPost && path == "12/dependencies/blocked_by":
			notFound(w)
		case r.Method == http.MethodPost:
			posts = append(posts, path)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Issue{ID: 1001, Number: 1})
		case strings.HasSuffix(path, "/dependencies/blocked_by"), strings.HasSuffix(path, "/sub_issues"):
			_ = json.NewEncoder(w).Encode([]Issue{})
		default:
			_ = json.NewEncoder(w).Encode(Issue{ID: 1001, Number: 1})
		}
	}))
	defer server.Close()

	res := newTestTracker(server.URL).PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 10, ToNumber: 1, LinkType: githubLinkBlockedBy},
		{FromNumber: 10, ToNumber: 2, LinkType: githubLinkBlockedBy},
		{FromNumber: 10, ToNumber: 3, LinkType: githubLinkSubIssue},
		{FromNumber: 11, ToNumber: 1, LinkType: githubLinkBlockedBy},
		{FromNumber: 12, ToNumber: 1, LinkType: githubLinkBlockedBy},
		{FromNumber: 12, ToNumber: 2, LinkType: githubLinkBlockedBy},
		{FromNumber: 13, ToNumber: 1, LinkType: githubLinkSubIssue},
	}, PushLinkOptions{})

	if len(res.Errors) != 0 {
		t.Fatalf("Errors = %v", res.Errors)
	}
	if len(res.Unsupported) != 0 || res.UnsupportedSkipped != 0 {
		t.Fatalf("Unsupported = %v (%d skipped); a missing issue must not disable a link type", res.Unsupported, res.UnsupportedSkipped)
	}
	if len(res.MissingSources) != 2 || res.MissingSources[0] != 10 || res.MissingSources[1] != 12 {
		t.Fatalf("MissingSources = %v, want [10 12]", res.MissingSources)
	}
	if probes["10"] != 1 || probes["12"] != 1 {
		t.Fatalf("source probes = %v, want one per missing issue", probes)
	}
	if res.Created != 2 || len(posts) != 2 || posts[0] != "11/dependencies/blocked_by" || posts[1] != "13/sub_issues" {
		t.Fatalf("Created = %d, posts = %v; want links from #11 and #13 still created", res.Created, posts)
	}
}
