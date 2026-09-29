package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/linear"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// configOnlyTrackerStore exercises front-door validation without constructing
// a local Dolt store. The embedded interface is intentionally unused by these
// tests; any accidental persistence call will panic, making the boundary
// regression visible.
type configOnlyTrackerStore struct {
	tracker.Store
	config map[string]string
}

func (s *configOnlyTrackerStore) GetConfig(_ context.Context, key string) (string, error) {
	return s.config[key], nil
}

func TestValidateLinearConfigForStoreUsesProxiedStore(t *testing.T) {
	old := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = old })
	t.Setenv("LINEAR_OAUTH_CLIENT_ID", "client")
	t.Setenv("LINEAR_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("LINEAR_TEAM_ID", "12345678-1234-1234-1234-123456789abc")

	st := &configOnlyTrackerStore{config: map[string]string{}}
	if err := validateLinearConfigForStore(st, nil); err != nil {
		t.Fatalf("proxied Linear validation failed: %v", err)
	}
	got := getLinearTeamIDsForStore(context.Background(), st, nil)
	if len(got) != 1 || got[0] != "12345678-1234-1234-1234-123456789abc" {
		t.Fatalf("team IDs = %#v", got)
	}
}

func TestValidateJiraConfigForStoreUsesProxiedStore(t *testing.T) {
	old := rootCtx
	rootCtx = context.Background()
	t.Cleanup(func() { rootCtx = old })

	st := &configOnlyTrackerStore{config: map[string]string{
		"jira.url":       "https://example.atlassian.net",
		"jira.project":   "GC",
		"jira.api_token": "token",
	}}
	if err := validateJiraConfigForStore(st); err != nil {
		t.Fatalf("proxied Jira validation failed: %v", err)
	}
}

func TestValidateJiraConfigForStoreRejectsMissingStore(t *testing.T) {
	if err := validateJiraConfigForStore(nil); err == nil {
		t.Fatal("expected missing-store error")
	}
}

type parentLinkStore struct {
	configOnlyTrackerStore
	issues []*types.Issue
	deps   map[string][]*types.IssueWithDependencyMetadata
}

func (s *parentLinkStore) SearchIssues(context.Context, string, types.IssueFilter) ([]*types.Issue, error) {
	return s.issues, nil
}

func (s *parentLinkStore) GetDependenciesWithMetadata(_ context.Context, id string) ([]*types.IssueWithDependencyMetadata, error) {
	return s.deps[id], nil
}

func TestBuildLinearParentLinksForStoreUsesProxiedStore(t *testing.T) {
	parentRef := "https://linear.app/team/issue/ENG-1/parent"
	childRef := "https://linear.app/team/issue/ENG-2/child"
	parent := &types.Issue{ID: "bd-parent", ExternalRef: &parentRef}
	child := &types.Issue{ID: "bd-child", ExternalRef: &childRef}
	st := &parentLinkStore{
		issues: []*types.Issue{parent, child},
		deps: map[string][]*types.IssueWithDependencyMetadata{
			"bd-child": {{Issue: *parent, DependencyType: types.DepParentChild}},
		},
	}
	links, err := buildLinearParentLinksForStore(context.Background(), st, &linear.Tracker{})
	if err != nil {
		t.Fatalf("build links: %v", err)
	}
	if len(links) != 1 || links[0].ChildIdentifier != "ENG-2" || links[0].ParentIdentifier != "ENG-1" {
		t.Fatalf("links = %#v", links)
	}
}

func TestBuildLinearClientAPIOnlyDoesNotProbeLocalStore(t *testing.T) {
	oldPath, oldStore := dbPath, store
	dbPath, store = "/definitely/missing/local/beads.db", nil
	t.Cleanup(func() { dbPath, store = oldPath, oldStore })
	t.Setenv("LINEAR_API_KEY", "api-key")
	client, err := buildLinearClientAPIOnly(context.Background(), "", nil)
	if err != nil || client == nil {
		t.Fatalf("API-only client: client=%v err=%v", client, err)
	}
}

type teamsRoundTripper struct{ called bool }

func (rt *teamsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.called = true
	if req.Method != http.MethodPost || req.URL.String() != "https://api.linear.app/graphql" {
		return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
	}
	body := `{"data":{"teams":{"nodes":[{"id":"team-1","name":"Engineering","key":"ENG"}]}}}`
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

func TestRunLinearTeamsAPIOnlyDoesNotOpenLocalStore(t *testing.T) {
	oldPath, oldStore, oldJSON, oldCtx := dbPath, store, jsonOutput, rootCtx
	oldTransport := http.DefaultTransport
	t.Cleanup(func() {
		dbPath, store, jsonOutput, rootCtx, http.DefaultTransport = oldPath, oldStore, oldJSON, oldCtx, oldTransport
	})
	dbPath, store, jsonOutput, rootCtx = "/definitely/missing/local/beads.db", nil, true, context.Background()
	t.Setenv("LINEAR_API_KEY", "api-key")
	rt := &teamsRoundTripper{}
	http.DefaultTransport = rt
	if err := runLinearTeams(nil, nil); err != nil {
		t.Fatalf("runLinearTeams: %v", err)
	}
	if !rt.called {
		t.Fatal("expected Linear teams API request")
	}
}

func TestBuildLinearClientAPIOnlyUsesProxiedEndpointConfig(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "api-key")
	st := &configOnlyTrackerStore{config: map[string]string{"linear.api_endpoint": "https://proxy.example/graphql"}}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://proxy.example/graphql" {
			return nil, fmt.Errorf("unexpected endpoint %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"teams":{"nodes":[]}}}`)), Header: make(http.Header), Request: req}, nil
	})
	client, err := buildLinearClientAPIOnly(context.Background(), "", st)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	if _, err := client.FetchTeams(context.Background()); err != nil {
		t.Fatalf("fetch teams: %v", err)
	}
}

// TestBuildLinearClientAPIOnlyUsesProxiedEndpointConfigOnOAuth is the OAuth
// mirror of the test above. The helper must apply linear.api_endpoint on BOTH
// auth branches, as Tracker.Init does; an early return on the OAuth branch
// silently ignores a configured custom endpoint whenever OAuth credentials are
// present, which is the whole set of CI environments.
func TestBuildLinearClientAPIOnlyUsesProxiedEndpointConfigOnOAuth(t *testing.T) {
	t.Setenv("LINEAR_OAUTH_CLIENT_ID", "client")
	t.Setenv("LINEAR_OAUTH_CLIENT_SECRET", "secret")
	st := &configOnlyTrackerStore{config: map[string]string{"linear.api_endpoint": "https://proxy.example/graphql"}}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })

	// The OAuth branch makes two requests, and only the second is the one under
	// test: the token exchange goes to the OAuth token endpoint, which
	// linear.api_endpoint does not and should not redirect. Serve that one a
	// token and record where the GraphQL call actually lands.
	var graphQLURL string
	http.DefaultTransport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == linear.DefaultOAuthTokenURL {
			body := `{"access_token":"tok","token_type":"Bearer","expires_in":3600}`
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
		}
		graphQLURL = req.URL.String()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"teams":{"nodes":[]}}}`)), Header: make(http.Header), Request: req}, nil
	})

	client, err := buildLinearClientAPIOnly(context.Background(), "", st)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	if _, err := client.FetchTeams(context.Background()); err != nil {
		t.Fatalf("fetch teams: %v", err)
	}
	if graphQLURL != "https://proxy.example/graphql" {
		t.Fatalf("OAuth branch sent GraphQL to %q, want the configured linear.api_endpoint", graphQLURL)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
