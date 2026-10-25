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

func TestSubIssueLinkFromParentChild(t *testing.T) {
	child := githubIssue("bd-child", "https://github.com/o/r/issues/10", types.TypeTask)
	parent := githubDep("bd-epic", "https://github.com/o/r/issues/5", types.DepParentChild)

	link, ok := SubIssueLinkFromParentChild(child, parent)
	if !ok {
		t.Fatal("SubIssueLinkFromParentChild returned false")
	}
	if link.FromNumber != 5 || link.ToNumber != 10 || link.LinkType != githubLinkSubIssue {
		t.Fatalf("link = %+v, want parent #5 sub_issue #10", link)
	}

	// Same issue number should not produce a self-referential link.
	same := githubDep("bd-epic", "https://github.com/o/r/issues/10", types.DepParentChild)
	if _, ok := SubIssueLinkFromParentChild(child, same); ok {
		t.Fatal("self-referential parent link should not be produced")
	}
}

func TestBlockedByLinkFromBeadsDependency(t *testing.T) {
	issue := githubIssue("bd-a", "https://github.com/o/r/issues/10", types.TypeTask)
	blocker := githubDep("bd-b", "https://github.com/o/r/issues/20", types.DepBlocks)

	link, ok := BlockedByLinkFromBeadsDependency(issue, blocker)
	if !ok {
		t.Fatal("BlockedByLinkFromBeadsDependency returned false")
	}
	if link.FromNumber != 10 || link.ToNumber != 20 || link.LinkType != githubLinkBlockedBy {
		t.Fatalf("link = %+v, want #10 blocked_by #20", link)
	}

	related := githubDep("bd-c", "https://github.com/o/r/issues/30", types.DepRelatesTo)
	if _, ok := BlockedByLinkFromBeadsDependency(issue, related); ok {
		t.Fatal("non-blocks dependency type should not produce a blocked_by link")
	}
}

func TestDeduplicateLinks(t *testing.T) {
	links := []DependencyLink{
		{FromNumber: 10, ToNumber: 20, LinkType: githubLinkBlockedBy},
		{FromNumber: 10, ToNumber: 20, LinkType: githubLinkBlockedBy},
		{FromNumber: 5, ToNumber: 10, LinkType: githubLinkSubIssue},
	}

	got := DeduplicateLinks(links)

	if len(got) != 2 {
		t.Fatalf("deduped links len = %d, want 2", len(got))
	}
}

func TestPushLinksAddsMissing(t *testing.T) {
	var posted bool
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/dependencies/blocked_by"):
			_ = json.NewEncoder(w).Encode([]Issue{})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/20"):
			_ = json.NewEncoder(w).Encode(Issue{ID: 2000, Number: 20})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/issues/10/dependencies/blocked_by"):
			posted = true
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Issue{ID: 2000, Number: 20})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", "o", "r").WithBaseURL(server.URL))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 10, ToNumber: 20, LinkType: githubLinkBlockedBy},
	}, PushLinkOptions{})

	if len(res.Errors) != 0 {
		t.Fatalf("PushLinks errors = %v", res.Errors)
	}
	if res.Created != 1 || !posted {
		t.Fatalf("Created = %d, posted = %v, want one created POST", res.Created, posted)
	}
	if int(capturedBody["issue_id"].(float64)) != 2000 {
		t.Fatalf("issue_id = %v, want 2000", capturedBody["issue_id"])
	}
}

func TestPushLinksIdempotentExistingLink(t *testing.T) {
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/sub_issues"):
			_ = json.NewEncoder(w).Encode([]Issue{{ID: 2000, Number: 20}})
		case r.Method == http.MethodPost:
			posts++
			t.Fatalf("unexpected POST for existing link")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", "o", "r").WithBaseURL(server.URL))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 5, ToNumber: 20, LinkType: githubLinkSubIssue},
	}, PushLinkOptions{})

	if len(res.Errors) != 0 {
		t.Fatalf("PushLinks errors = %v", res.Errors)
	}
	if res.Created != 0 || posts != 0 {
		t.Fatalf("Created = %d, posts = %d, want no created links", res.Created, posts)
	}
}

func TestPushLinksDryRunDoesNotPost(t *testing.T) {
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/sub_issues"):
			_ = json.NewEncoder(w).Encode([]Issue{})
		case r.Method == http.MethodPost:
			posts++
			t.Fatalf("unexpected POST during dry-run")
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var planned []DependencyLink
	resolver := NewLinkResolver(NewClient("token", "o", "r").WithBaseURL(server.URL))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 5, ToNumber: 20, LinkType: githubLinkSubIssue},
	}, PushLinkOptions{
		DryRun: true,
		OnPlan: func(link DependencyLink) {
			planned = append(planned, link)
		},
	})

	if len(res.Errors) != 0 {
		t.Fatalf("PushLinks errors = %v", res.Errors)
	}
	if res.Created != 1 || posts != 0 || len(planned) != 1 {
		t.Fatalf("Created = %d, posts = %d, planned = %d; want one dry-run plan and no POST", res.Created, posts, len(planned))
	}
}

func githubIssue(id, ref string, issueType types.IssueType) *types.Issue {
	return &types.Issue{
		ID:          id,
		ExternalRef: &ref,
		IssueType:   issueType,
		Status:      types.StatusOpen,
	}
}

func githubDep(id, ref string, depType types.DependencyType) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{
		Issue:          *githubIssue(id, ref, types.TypeTask),
		DependencyType: depType,
	}
}

// The 404 degradation path: a relationship endpoint that is absent on this
// host and a source issue that is gone both surface as 404 on the list call,
// but they are different operator problems and must not be counted together.

func TestPushLinksUnavailableEndpointCountsUnsupported(t *testing.T) {
	var probes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dependencies/blocked_by"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/10"):
			// The source issue is readable, so the 404 above is the endpoint.
			probes++
			_ = json.NewEncoder(w).Encode(Issue{ID: 1000, Number: 10})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", "o", "r").WithBaseURL(server.URL))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 10, ToNumber: 20, LinkType: githubLinkBlockedBy},
	}, PushLinkOptions{})

	if len(res.Errors) != 0 {
		t.Fatalf("PushLinks errors = %v, want none (degradation is counted, not raised)", res.Errors)
	}
	if res.UnsupportedSkipped != 1 {
		t.Fatalf("UnsupportedSkipped = %d, want 1", res.UnsupportedSkipped)
	}
	if res.SourceMissing != 0 {
		t.Fatalf("SourceMissing = %d, want 0 - the source issue was readable", res.SourceMissing)
	}
	if res.Created != 0 {
		t.Fatalf("Created = %d, want 0", res.Created)
	}
	if probes != 1 {
		t.Fatalf("source issue probed %d times, want 1", probes)
	}
}

func TestPushLinksMissingSourceIssueIsNotUnsupported(t *testing.T) {
	var probes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dependencies/blocked_by"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sub_issues"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/10"):
			// The source issue itself is gone: deleted, renumbered, or hidden.
			probes++
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", "o", "r").WithBaseURL(server.URL))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 10, ToNumber: 20, LinkType: githubLinkBlockedBy},
		{FromNumber: 10, ToNumber: 30, LinkType: githubLinkSubIssue},
	}, PushLinkOptions{})

	if len(res.Errors) != 0 {
		t.Fatalf("PushLinks errors = %v, want none", res.Errors)
	}
	if res.SourceMissing != 2 {
		t.Fatalf("SourceMissing = %d, want 2", res.SourceMissing)
	}
	if res.UnsupportedSkipped != 0 {
		t.Fatalf("UnsupportedSkipped = %d, want 0 - the endpoint was never shown to be absent", res.UnsupportedSkipped)
	}
	// Both link types hang off issue #10, and the classification is a property
	// of the issue, so one probe answers for both.
	if probes != 1 {
		t.Fatalf("source issue probed %d times, want 1", probes)
	}
}

func TestPushLinksCreateNotFoundCountsUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/dependencies/blocked_by"):
			_ = json.NewEncoder(w).Encode([]Issue{})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/20"):
			_ = json.NewEncoder(w).Encode(Issue{ID: 2000, Number: 20})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/dependencies/blocked_by"):
			// Listing worked and the target resolved, so a 404 here can only
			// be the create endpoint.
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", "o", "r").WithBaseURL(server.URL))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{FromNumber: 10, ToNumber: 20, LinkType: githubLinkBlockedBy},
	}, PushLinkOptions{})

	if len(res.Errors) != 0 {
		t.Fatalf("PushLinks errors = %v, want none", res.Errors)
	}
	if res.UnsupportedSkipped != 1 {
		t.Fatalf("UnsupportedSkipped = %d, want 1", res.UnsupportedSkipped)
	}
	if res.SourceMissing != 0 {
		t.Fatalf("SourceMissing = %d, want 0", res.SourceMissing)
	}
	if res.Created != 0 {
		t.Fatalf("Created = %d, want 0", res.Created)
	}
}
