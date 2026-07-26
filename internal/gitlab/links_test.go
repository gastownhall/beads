package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestIssueLinksToDependencies_NormalizesBlockDirection(t *testing.T) {
	config := DefaultMappingConfig()
	links := []IssueLink{
		{
			SourceIssue: &Issue{IID: 20},
			TargetIssue: &Issue{IID: 10},
			LinkType:    gitLabLinkBlocks,
		},
		{
			SourceIssue: &Issue{IID: 10},
			TargetIssue: &Issue{IID: 30},
			LinkType:    gitLabLinkIsBlockedBy,
		},
	}

	deps := issueLinksToDependencies(10, links, config)

	if len(deps) != 2 {
		t.Fatalf("deps len = %d, want 2", len(deps))
	}
	for i, dep := range deps {
		if dep.FromGitLabIID != 10 || dep.Type != string(types.DepBlocks) {
			t.Fatalf("deps[%d] = %+v, want source issue 10 with blocks type", i, dep)
		}
	}
	if deps[0].ToGitLabIID != 20 {
		t.Fatalf("deps[0].ToGitLabIID = %d, want 20", deps[0].ToGitLabIID)
	}
	if deps[1].ToGitLabIID != 30 {
		t.Fatalf("deps[1].ToGitLabIID = %d, want 30", deps[1].ToGitLabIID)
	}
}

func TestLinkFromBeadsDependencyPushDirection(t *testing.T) {
	issue := gitLabIssue("bd-a", "https://gitlab.example.com/group/project/-/issues/10", types.TypeTask)
	blocker := gitLabDep("bd-b", "https://gitlab.example.com/group/project/-/work_items/20", types.DepBlocks)

	link, ok := LinkFromBeadsDependency(issue, blocker)
	if !ok {
		t.Fatal("LinkFromBeadsDependency returned false")
	}
	if link.SourceIID != 20 || link.TargetIID != 10 || link.LinkType != gitLabLinkBlocks {
		t.Fatalf("link = %+v, want GitLab #20 blocks #10", link)
	}

	related := gitLabDep("bd-c", "gitlab:30", types.DepRelatesTo)
	link, ok = LinkFromBeadsDependency(issue, related)
	if !ok {
		t.Fatal("LinkFromBeadsDependency related returned false")
	}
	if link.SourceIID != 10 || link.TargetIID != 30 || link.LinkType != gitLabLinkRelatesTo {
		t.Fatalf("related link = %+v, want unordered GitLab relates_to #10 #30", link)
	}

	milestone := gitLabDep("bd-epic", "https://gitlab.example.com/group/project/-/milestones/5", types.DepBlocks)
	if _, ok := LinkFromBeadsDependency(issue, milestone); ok {
		t.Fatal("milestone endpoint should not produce an issue link")
	}
}

func TestDeduplicateLinksRelatedReciprocal(t *testing.T) {
	links := []DependencyLink{
		{SourceIID: 10, TargetIID: 20, LinkType: gitLabLinkRelatesTo},
		{SourceIID: 20, TargetIID: 10, LinkType: gitLabLinkRelatesTo},
	}

	got := DeduplicateLinks(links)

	if len(got) != 1 {
		t.Fatalf("deduped links len = %d, want 1", len(got))
	}
	if got[0].SourceIID != 10 || got[0].TargetIID != 20 {
		t.Fatalf("deduped link = %+v, want canonical #10 -> #20", got[0])
	}
}

func TestPushLinksAddsMissing(t *testing.T) {
	var posted bool
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]IssueLink{})
		case http.MethodPost:
			posted = true
			if !strings.Contains(r.URL.Path, "/issues/20/links") {
				t.Fatalf("POST path = %s, want source issue 20 links endpoint", r.URL.Path)
			}
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(IssueLink{
				SourceIssue: &Issue{IID: 20},
				TargetIssue: &Issue{IID: 10},
				LinkType:    gitLabLinkBlocks,
			})
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", server.URL, "123"))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{SourceIID: 20, TargetIID: 10, LinkType: gitLabLinkBlocks},
	}, PushLinkOptions{})
	count, errs := res.Created, res.Errors

	if len(errs) != 0 {
		t.Fatalf("PushLinks errors = %v", errs)
	}
	if count != 1 || !posted {
		t.Fatalf("count = %d, posted = %v, want one created POST", count, posted)
	}
	if capturedBody["link_type"] != gitLabLinkBlocks {
		t.Fatalf("link_type = %v, want blocks", capturedBody["link_type"])
	}
	if int(capturedBody["target_issue_iid"].(float64)) != 10 {
		t.Fatalf("target_issue_iid = %v, want 10", capturedBody["target_issue_iid"])
	}
}

func TestPushLinksIdempotentExistingLink(t *testing.T) {
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]IssueLink{{
				SourceIssue: &Issue{IID: 20},
				TargetIssue: &Issue{IID: 10},
				LinkType:    gitLabLinkBlocks,
			}})
		case http.MethodPost:
			posts++
			t.Fatalf("unexpected POST for existing link")
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", server.URL, "123"))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{SourceIID: 20, TargetIID: 10, LinkType: gitLabLinkBlocks},
	}, PushLinkOptions{})
	count, errs := res.Created, res.Errors

	if len(errs) != 0 {
		t.Fatalf("PushLinks errors = %v", errs)
	}
	if count != 0 || posts != 0 {
		t.Fatalf("count = %d, posts = %d, want no created links", count, posts)
	}
}

func TestPushLinksIdempotentExistingLiveLinkedIssuePayload(t *testing.T) {
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]IssueLink{{
				IID:      33,
				LinkType: gitLabLinkRelatesTo,
			}})
		case http.MethodPost:
			posts++
			t.Fatalf("unexpected POST for existing live GitLab link payload")
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", server.URL, "123"))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{SourceIID: 31, TargetIID: 33, LinkType: gitLabLinkRelatesTo},
	}, PushLinkOptions{})
	count, errs := res.Created, res.Errors

	if len(errs) != 0 {
		t.Fatalf("PushLinks errors = %v", errs)
	}
	if count != 0 || posts != 0 {
		t.Fatalf("count = %d, posts = %d, want no created links", count, posts)
	}
}

func TestPushLinksDryRunDoesNotPost(t *testing.T) {
	var posts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode([]IssueLink{})
		case http.MethodPost:
			posts++
			t.Fatalf("unexpected POST during dry-run")
		}
	}))
	defer server.Close()

	var planned []DependencyLink
	resolver := NewLinkResolver(NewClient("token", server.URL, "123"))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{SourceIID: 20, TargetIID: 10, LinkType: gitLabLinkBlocks},
	}, PushLinkOptions{
		DryRun: true,
		OnPlan: func(link DependencyLink) {
			planned = append(planned, link)
		},
	})
	count, errs := res.Created, res.Errors

	if len(errs) != 0 {
		t.Fatalf("PushLinks errors = %v", errs)
	}
	if count != 1 || posts != 0 || len(planned) != 1 {
		t.Fatalf("count = %d, posts = %d, planned = %d; want one dry-run plan and no POST", count, posts, len(planned))
	}
}

func TestIsGitLabLicenseError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"license 403 body", fmt.Errorf("failed to create issue link: API error: {\"message\":\"Blocked issues not available for current license\"} (status 403)"), true},
		{"license phrase", fmt.Errorf("not available for current license"), true},
		{"plain 403 no license", fmt.Errorf("API error: {\"message\":\"403 Forbidden\"} (status 403)"), false},
		{"server error", fmt.Errorf("API error: {\"message\":\"boom\"} (status 500)"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGitLabLicenseError(tc.err); got != tc.want {
				t.Fatalf("isGitLabLicenseError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestPushLinksLicenseSkippedBlocks verifies that a blocks link rejected for
// lack of a GitLab license is counted as LicenseSkipped (not a genuine error),
// the sync continues, and relates_to links still apply.
func TestPushLinksLicenseSkippedBlocks(t *testing.T) {
	var blocksPosts, relatesPosts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]IssueLink{})
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch body["link_type"] {
		case gitLabLinkBlocks:
			blocksPosts++
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Blocked issues not available for current license"}`))
		case gitLabLinkRelatesTo:
			relatesPosts++
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(IssueLink{
				SourceIssue: &Issue{IID: 31},
				TargetIssue: &Issue{IID: 33},
				LinkType:    gitLabLinkRelatesTo,
			})
		default:
			t.Fatalf("unexpected link_type %v", body["link_type"])
		}
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", server.URL, "123"))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{SourceIID: 20, TargetIID: 10, LinkType: gitLabLinkBlocks},
		{SourceIID: 31, TargetIID: 33, LinkType: gitLabLinkRelatesTo},
	}, PushLinkOptions{})

	if len(res.Errors) != 0 {
		t.Fatalf("Errors = %v, want none (license is not a genuine error)", res.Errors)
	}
	if res.LicenseSkipped != 1 {
		t.Fatalf("LicenseSkipped = %d, want 1", res.LicenseSkipped)
	}
	if res.Created != 1 {
		t.Fatalf("Created = %d, want 1 (relates_to still applied)", res.Created)
	}
	if blocksPosts != 1 || relatesPosts != 1 {
		t.Fatalf("blocksPosts=%d relatesPosts=%d, want 1/1", blocksPosts, relatesPosts)
	}
}

// TestPushLinksNonLicenseErrorIsGenuine verifies that a non-license failure
// (e.g. a permissions 403 without the license message) is reported as a genuine
// error, not silently treated as the expected license degradation.
func TestPushLinksNonLicenseErrorIsGenuine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]IssueLink{})
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"403 Forbidden - insufficient_scope"}`))
	}))
	defer server.Close()

	resolver := NewLinkResolver(NewClient("token", server.URL, "123"))
	res := resolver.PushLinks(context.Background(), []DependencyLink{
		{SourceIID: 20, TargetIID: 10, LinkType: gitLabLinkBlocks},
	}, PushLinkOptions{})

	if res.LicenseSkipped != 0 {
		t.Fatalf("LicenseSkipped = %d, want 0 (not a license error)", res.LicenseSkipped)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Errors = %v, want exactly 1 genuine error", res.Errors)
	}
	if res.Created != 0 {
		t.Fatalf("Created = %d, want 0", res.Created)
	}
}

func TestPushEpicMilestonesDependencyOnlySync(t *testing.T) {
	epic := gitLabIssue("bd-epic", "https://gitlab.example.com/group/project/-/milestones/5", types.TypeEpic)
	child := gitLabIssue("bd-child", "https://gitlab.example.com/group/project/-/issues/10", types.TypeTask)
	fakeStore := &gitLabLinkFakeStore{
		deps: map[string][]*types.IssueWithDependencyMetadata{
			"bd-child": {{Issue: *epic, DependencyType: types.DepParentChild}},
		},
	}

	var updates int
	var capturedBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/milestones"):
			if r.URL.Query().Get("iids[]") != "5" {
				t.Fatalf("milestone query = %s, want iids[]=5", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]Milestone{{ID: 500, IID: 5, Title: "Epic milestone"}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/10"):
			_ = json.NewEncoder(w).Encode(Issue{ID: 100, IID: 10, Title: "Child"})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/issues/10"):
			updates++
			_ = json.NewDecoder(r.Body).Decode(&capturedBody)
			_ = json.NewEncoder(w).Encode(Issue{ID: 100, IID: 10, Title: "Child", Milestone: &Milestone{ID: 500}})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	tr := &Tracker{
		client: NewClient("token", server.URL, "123"),
		store:  fakeStore,
	}
	count, errs := tr.PushEpicMilestones(context.Background(), []*types.Issue{child}, EpicMilestoneOptions{})

	if len(errs) != 0 {
		t.Fatalf("PushEpicMilestones errors = %v", errs)
	}
	if count != 1 || updates != 1 {
		t.Fatalf("count = %d, updates = %d, want one milestone update", count, updates)
	}
	if int(capturedBody["milestone_id"].(float64)) != 500 {
		t.Fatalf("milestone_id = %v, want 500", capturedBody["milestone_id"])
	}
}

func TestUpdateMilestoneResolvesIIDToAPIID(t *testing.T) {
	epic := gitLabIssue("bd-epic", "https://gitlab.example.com/group/project/-/milestones/4", types.TypeEpic)

	var updatedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/milestones"):
			if r.URL.Query().Get("iids[]") != "4" {
				t.Fatalf("milestone query = %s, want iids[]=4", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]Milestone{{ID: 35, IID: 4, Title: "Existing milestone"}})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/milestones/35"):
			updatedPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(Milestone{ID: 35, IID: 4, Title: epic.Title, WebURL: *epic.ExternalRef})
		case r.Method == http.MethodPut:
			t.Fatalf("PUT path = %s, want milestone API ID 35", r.URL.Path)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	tr := &Tracker{client: NewClient("token", server.URL, "123")}
	got, err := tr.updateMilestone(context.Background(), "milestone:4", epic)
	if err != nil {
		t.Fatalf("updateMilestone: %v", err)
	}
	if updatedPath == "" {
		t.Fatal("UpdateMilestone was not called")
	}
	if got.ID != "35" {
		t.Fatalf("TrackerIssue ID = %s, want 35", got.ID)
	}
}

func TestFetchIssueMilestoneIdentifier(t *testing.T) {
	updatedAt := time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/milestones") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("iids[]") != "4" {
			t.Fatalf("milestone query = %s, want iids[]=4", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]Milestone{{
			ID:          35,
			IID:         4,
			Title:       "Existing milestone",
			Description: "remote milestone",
			UpdatedAt:   &updatedAt,
			WebURL:      "https://gitlab.example.com/group/project/-/milestones/4",
		}})
	}))
	defer server.Close()

	tr := &Tracker{client: NewClient("token", server.URL, "123")}
	got, err := tr.FetchIssue(context.Background(), "milestone:4")
	if err != nil {
		t.Fatalf("FetchIssue milestone: %v", err)
	}
	if got == nil {
		t.Fatal("FetchIssue milestone returned nil")
	}
	if got.Title != "Existing milestone" || got.Description != "remote milestone" {
		t.Fatalf("TrackerIssue = %+v", got)
	}
	if !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("UpdatedAt = %s, want %s", got.UpdatedAt, updatedAt)
	}
}

type gitLabLinkFakeStore struct {
	storage.Storage
	deps map[string][]*types.IssueWithDependencyMetadata
}

func (s *gitLabLinkFakeStore) GetDependenciesWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps[issueID], nil
}

func gitLabIssue(id, ref string, issueType types.IssueType) *types.Issue {
	return &types.Issue{
		ID:          id,
		ExternalRef: &ref,
		IssueType:   issueType,
		Status:      types.StatusOpen,
	}
}

func gitLabDep(id, ref string, depType types.DependencyType) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{
		Issue:          *gitLabIssue(id, ref, types.TypeTask),
		DependencyType: depType,
	}
}
