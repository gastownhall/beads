package notion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type fakeNotionClient struct {
	pullResp   *PullResponse
	pushResp   *PushResponse
	statusResp *StatusResponse
	pullReq    PullRequest
	pushReq    PushRequest
	pullCalls  int
	pushCalls  int
}

func (f *fakeNotionClient) Status(_ context.Context, req StatusRequest) (*StatusResponse, error) {
	return f.statusResp, nil
}

func (f *fakeNotionClient) Pull(_ context.Context, req PullRequest) (*PullResponse, error) {
	f.pullReq = req
	f.pullCalls++
	return f.pullResp, nil
}

func (f *fakeNotionClient) Push(_ context.Context, req PushRequest) (*PushResponse, error) {
	f.pushReq = req
	f.pushCalls++
	return f.pushResp, nil
}

func TestTrackerFetchIssues(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pullResp: &PullResponse{
			Issues: []PulledIssue{
				{
					ID:           "beads-1",
					Title:        "One",
					Description:  "Desc",
					Status:       "open",
					Priority:     "medium",
					IssueType:    "task",
					ExternalRef:  "https://www.notion.so/One-0123456789abcdef0123456789abcdef",
					NotionPageID: "01234567-89ab-cdef-0123-456789abcdef",
					CreatedAt:    "2026-03-19T14:00:00Z",
					UpdatedAt:    "2026-03-19T14:05:00Z",
				},
			},
		},
	}
	tr := NewTracker(WithTrackerClient(client), WithTrackerViewURL("https://example.com/view"))

	issues, err := tr.FetchIssues(context.Background(), itracker.FetchOptions{State: "all"})
	if err != nil {
		t.Fatalf("FetchIssues returned error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	if issues[0].Identifier != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("identifier = %q", issues[0].Identifier)
	}
}

func TestTrackerFetchIssuesPropagatesCacheMaxAge(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pullResp: &PullResponse{Issues: []PulledIssue{}},
	}
	tr := NewTracker(WithTrackerClient(client), WithTrackerCacheMaxAge(5*time.Minute))
	if _, err := tr.FetchIssues(context.Background(), itracker.FetchOptions{State: "all"}); err != nil {
		t.Fatalf("FetchIssues returned error: %v", err)
	}
	if client.pullReq.CacheMaxAge != 5*time.Minute {
		t.Fatalf("cache max age = %v, want 5m", client.pullReq.CacheMaxAge)
	}
}

func TestTrackerFetchIssue(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pullResp: &PullResponse{
			Issues: []PulledIssue{
				{
					ID:           "beads-1",
					Title:        "One",
					Description:  "Desc",
					Status:       "open",
					Priority:     "medium",
					IssueType:    "task",
					ExternalRef:  "https://www.notion.so/One-0123456789abcdef0123456789abcdef",
					NotionPageID: "01234567-89ab-cdef-0123-456789abcdef",
					CreatedAt:    "2026-03-19T14:00:00Z",
					UpdatedAt:    "2026-03-19T14:05:00Z",
				},
			},
		},
	}
	tr := NewTracker(WithTrackerClient(client))

	issue, err := tr.FetchIssue(context.Background(), "notion:01234567-89ab-cdef-0123-456789abcdef")
	if err != nil {
		t.Fatalf("FetchIssue returned error: %v", err)
	}
	if issue == nil {
		t.Fatal("expected issue, got nil")
	}
	if issue.ID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("id = %q", issue.ID)
	}
}

func TestTrackerCreateIssue(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pushResp: &PushResponse{
			Created: []PushResultItem{
				{
					ExternalRef:  "https://www.notion.so/0123456789abcdef0123456789abcdef",
					NotionPageID: "01234567-89ab-cdef-0123-456789abcdef",
				},
			},
		},
	}
	tr := NewTracker(
		WithTrackerClient(client),
		WithTrackerDatabaseID("db_123"),
		WithTrackerViewURL("https://example.com/view"),
	)

	issue := &types.Issue{
		ID:        "beads-1",
		Title:     "Create me",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		CreatedAt: time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 3, 19, 14, 5, 0, 0, time.UTC),
	}

	created, err := tr.CreateIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if created.ID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("id = %q", created.ID)
	}
	if client.pushReq.DatabaseID != "db_123" {
		t.Fatalf("database id = %q", client.pushReq.DatabaseID)
	}
	if client.pushReq.ViewURL != "https://example.com/view" {
		t.Fatalf("view url = %q", client.pushReq.ViewURL)
	}
}

func TestTrackerCreateIssueFallsBackToPullForExternalRef(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pushResp: &PushResponse{
			Created: []PushResultItem{
				{
					ID:    "beads-1",
					Title: "Create me",
				},
			},
		},
		pullResp: &PullResponse{
			Issues: []PulledIssue{
				{
					ID:           "beads-1",
					Title:        "Create me",
					Description:  "Created through fallback",
					Status:       "open",
					Priority:     "medium",
					IssueType:    "task",
					ExternalRef:  "https://www.notion.so/0123456789abcdef0123456789abcdef",
					NotionPageID: "01234567-89ab-cdef-0123-456789abcdef",
					CreatedAt:    "2026-03-19T14:00:00Z",
					UpdatedAt:    "2026-03-19T14:05:00Z",
				},
			},
		},
	}
	tr := NewTracker(WithTrackerClient(client), WithTrackerViewURL("view://example"))

	issue := &types.Issue{
		ID:        "beads-1",
		Title:     "Create me",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}

	created, err := tr.CreateIssue(context.Background(), issue)
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if created == nil {
		t.Fatal("expected created issue, got nil")
	}
	if created.URL != "https://www.notion.so/0123456789abcdef0123456789abcdef" {
		t.Fatalf("url = %q", created.URL)
	}
}

func TestTrackerUpdateIssue(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pushResp: &PushResponse{
			Updated: []PushResultItem{
				{
					ExternalRef:  "https://www.notion.so/0123456789abcdef0123456789abcdef",
					NotionPageID: "01234567-89ab-cdef-0123-456789abcdef",
				},
			},
		},
	}
	tr := NewTracker(WithTrackerClient(client))

	issue := &types.Issue{
		ID:        "beads-1",
		Title:     "Update me",
		Status:    types.StatusInProgress,
		Priority:  1,
		IssueType: types.TypeFeature,
	}

	updated, err := tr.UpdateIssue(context.Background(), "01234567-89ab-cdef-0123-456789abcdef", issue)
	if err != nil {
		t.Fatalf("UpdateIssue returned error: %v", err)
	}
	if updated.ID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("id = %q", updated.ID)
	}
	if got := tr.ExtractIdentifier("https://www.notion.so/0123456789abcdef0123456789abcdef"); got != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("identifier = %q", got)
	}
}

func TestTrackerCreateIssueReturnsErrorOnPushErrors(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pushResp: &PushResponse{
			Errors: []PushResultError{
				{
					ID:      "beads-1",
					Stage:   "create",
					Message: "schema mismatch",
				},
			},
		},
	}
	tr := NewTracker(WithTrackerClient(client))

	_, err := tr.CreateIssue(context.Background(), &types.Issue{
		ID:        "beads-1",
		Title:     "Create me",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "schema mismatch") {
		t.Fatalf("error = %q, want schema mismatch", got)
	}
}

func TestTrackerUpdateIssueReturnsErrorWithoutResultItem(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pushResp: &PushResponse{},
	}
	tr := NewTracker(WithTrackerClient(client))

	_, err := tr.UpdateIssue(context.Background(), "01234567-89ab-cdef-0123-456789abcdef", &types.Issue{
		ID:        "beads-1",
		Title:     "Update me",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); !strings.Contains(got, "did not include a created or updated result") {
		t.Fatalf("error = %q", got)
	}
}

func TestTrackerBatchPushBuildsSinglePayloadAndNormalizesResponse(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pushResp: &PushResponse{
			Created: []PushResultItem{
				{ID: "beads-1", NotionPageID: "01234567-89ab-cdef-0123-456789abcdef"},
			},
			Updated: []PushResultItem{
				{ID: "beads-2", ExternalRef: "https://www.notion.so/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			},
			Skipped: []PushResultItem{
				{ID: "beads-3"},
			},
			Errors: []PushResultError{
				{ID: "beads-4", Stage: "state_fetch", Message: "missing page"},
			},
			Warnings: []string{"Skipped unsupported Notion issue types: pm=1"},
		},
	}
	tr := NewTracker(WithTrackerClient(client), WithTrackerDatabaseID("db_123"), WithTrackerViewURL("view://example"))

	issues := []*types.Issue{
		{ID: "beads-1", Title: "Create me", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		{ID: "beads-2", Title: "Update me", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeFeature},
	}

	result, err := tr.BatchPush(context.Background(), issues)
	if err != nil {
		t.Fatalf("BatchPush returned error: %v", err)
	}
	if client.pushCalls != 1 {
		t.Fatalf("pushCalls = %d, want 1", client.pushCalls)
	}
	if len(result.Created) != 1 || result.Created[0].LocalID != "beads-1" {
		t.Fatalf("created = %#v", result.Created)
	}
	if result.Created[0].ExternalRef != "https://www.notion.so/0123456789abcdef0123456789abcdef" {
		t.Fatalf("created external_ref = %q", result.Created[0].ExternalRef)
	}
	if len(result.Updated) != 1 || result.Updated[0].LocalID != "beads-2" {
		t.Fatalf("updated = %#v", result.Updated)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "beads-3" {
		t.Fatalf("skipped = %#v", result.Skipped)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Message, "missing page") {
		t.Fatalf("errors = %#v", result.Errors)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "pm=1") {
		t.Fatalf("warnings = %#v", result.Warnings)
	}
	if !strings.Contains(string(client.pushReq.Payload), "\"issues\"") {
		t.Fatalf("payload = %s", client.pushReq.Payload)
	}
}

func TestTrackerBatchPushIncludesExistingIssuesFromPullSnapshot(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pullResp: &PullResponse{
			Issues: []PulledIssue{
				{
					ID:           "beads-2",
					Title:        "Update me",
					Description:  "Desc",
					Status:       "in_progress",
					Priority:     "high",
					IssueType:    "feature",
					Assignee:     "osamu",
					Labels:       []string{"alpha"},
					ExternalRef:  "https://www.notion.so/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					NotionPageID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
					CreatedAt:    "2026-03-19T14:00:00Z",
					UpdatedAt:    "2026-03-19T14:05:00Z",
				},
			},
		},
		pushResp: &PushResponse{
			Skipped: []PushResultItem{{ID: "beads-2"}},
		},
	}
	tr := NewTracker(WithTrackerClient(client), WithTrackerDatabaseID("db_123"), WithTrackerViewURL("view://example"))

	if _, err := tr.FetchIssues(context.Background(), itracker.FetchOptions{State: "all"}); err != nil {
		t.Fatalf("FetchIssues returned error: %v", err)
	}

	issues := []*types.Issue{
		{ID: "beads-2", Title: "Update me", Description: "Desc", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeFeature, Assignee: "osamu", Labels: []string{"alpha"}},
	}
	if _, err := tr.BatchPush(context.Background(), issues); err != nil {
		t.Fatalf("BatchPush returned error: %v", err)
	}

	var payload PushPayload
	if err := json.Unmarshal(client.pushReq.Payload, &payload); err != nil {
		t.Fatalf("json.Unmarshal(payload): %v", err)
	}
	if len(payload.ExistingIssues) != 1 {
		t.Fatalf("existing issues = %#v", payload.ExistingIssues)
	}
	if payload.ExistingIssues[0].ID != "beads-2" || payload.ExistingIssues[0].NotionPageID != "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("existing issue = %#v", payload.ExistingIssues[0])
	}
	if payload.ExistingIssues[0].ExternalRef != "https://www.notion.so/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("external ref = %q", payload.ExistingIssues[0].ExternalRef)
	}
}

func TestTrackerBatchPushPropagatesCacheMaxAge(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pushResp: &PushResponse{Skipped: []PushResultItem{{ID: "beads-2"}}},
	}
	tr := NewTracker(WithTrackerClient(client), WithTrackerCacheMaxAge(5*time.Minute))
	issues := []*types.Issue{
		{ID: "beads-2", Title: "Update me", Status: types.StatusInProgress, Priority: 1, IssueType: types.TypeFeature},
	}
	if _, err := tr.BatchPush(context.Background(), issues); err != nil {
		t.Fatalf("BatchPush returned error: %v", err)
	}
	if client.pushReq.CacheMaxAge != 5*time.Minute {
		t.Fatalf("cache max age = %v, want 5m", client.pushReq.CacheMaxAge)
	}
}

func TestTrackerBuildExternalRefFallback(t *testing.T) {
	t.Parallel()

	tr := NewTracker()
	got := tr.BuildExternalRef(&itracker.TrackerIssue{
		ID: "01234567-89ab-cdef-0123-456789abcdef",
	})
	want := "https://www.notion.so/0123456789abcdef0123456789abcdef"
	if got != want {
		t.Fatalf("got = %q, want %q", got, want)
	}
}

func TestTrackerFetchIssueCachesFullPull(t *testing.T) {
	t.Parallel()

	client := &fakeNotionClient{
		pullResp: &PullResponse{
			Issues: []PulledIssue{
				{
					ID:           "beads-1",
					Title:        "One",
					Description:  "Desc",
					Status:       "open",
					Priority:     "medium",
					IssueType:    "task",
					ExternalRef:  "https://www.notion.so/0123456789abcdef0123456789abcdef",
					NotionPageID: "01234567-89ab-cdef-0123-456789abcdef",
					CreatedAt:    "2026-03-19T14:00:00Z",
					UpdatedAt:    "2026-03-19T14:05:00Z",
				},
			},
		},
	}
	tr := NewTracker(WithTrackerClient(client))

	for i := 0; i < 2; i++ {
		issue, err := tr.FetchIssue(context.Background(), "01234567-89ab-cdef-0123-456789abcdef")
		if err != nil {
			t.Fatalf("FetchIssue returned error: %v", err)
		}
		if issue == nil {
			t.Fatal("expected issue, got nil")
		}
	}

	if client.pullCalls != 1 {
		t.Fatalf("pullCalls = %d, want 1", client.pullCalls)
	}
}
