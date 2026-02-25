package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func TestRegistered(t *testing.T) {
	factory := tracker.Get("jira")
	if factory == nil {
		t.Fatal("jira tracker not registered")
	}
	tr := factory()
	if tr.Name() != "jira" {
		t.Errorf("Name() = %q, want %q", tr.Name(), "jira")
	}
	if tr.DisplayName() != "Jira" {
		t.Errorf("DisplayName() = %q, want %q", tr.DisplayName(), "Jira")
	}
	if tr.ConfigPrefix() != "jira" {
		t.Errorf("ConfigPrefix() = %q, want %q", tr.ConfigPrefix(), "jira")
	}
}

func TestIsExternalRef(t *testing.T) {
	tr := &Tracker{jiraURL: "https://company.atlassian.net"}
	tests := []struct {
		ref  string
		want bool
	}{
		{"https://company.atlassian.net/browse/PROJ-123", true},
		{"https://company.atlassian.net/browse/TEAM-1", true},
		{"https://other.atlassian.net/browse/PROJ-123", false},
		{"https://linear.app/team/issue/PROJ-123", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tr.IsExternalRef(tt.ref); got != tt.want {
			t.Errorf("IsExternalRef(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

func TestExtractIdentifier(t *testing.T) {
	tr := &Tracker{}
	tests := []struct {
		ref  string
		want string
	}{
		{"https://company.atlassian.net/browse/PROJ-123", "PROJ-123"},
		{"https://company.atlassian.net/browse/TEAM-1", "TEAM-1"},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		if got := tr.ExtractIdentifier(tt.ref); got != tt.want {
			t.Errorf("ExtractIdentifier(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestBuildExternalRef(t *testing.T) {
	tr := &Tracker{jiraURL: "https://company.atlassian.net"}
	ti := &tracker.TrackerIssue{Identifier: "PROJ-123"}
	ref := tr.BuildExternalRef(ti)
	want := "https://company.atlassian.net/browse/PROJ-123"
	if ref != want {
		t.Errorf("BuildExternalRef() = %q, want %q", ref, want)
	}
}

func TestJiraToTrackerIssue(t *testing.T) {
	ji := &Issue{
		ID:   "10001",
		Key:  "PROJ-42",
		Self: "https://company.atlassian.net/rest/api/3/issue/10001",
		Fields: IssueFields{
			Summary:     "Fix login bug",
			Description: json.RawMessage(`"A plain text description"`),
			Status:      &StatusField{ID: "1", Name: "In Progress"},
			Priority:    &PriorityField{ID: "2", Name: "High"},
			IssueType:   &IssueTypeField{ID: "10001", Name: "Bug"},
			Project:     &ProjectField{ID: "10000", Key: "PROJ"},
			Assignee:    &UserField{AccountID: "abc123", DisplayName: "Alice", EmailAddress: "alice@example.com"},
			Labels:      []string{"backend", "urgent"},
			Created:     "2025-01-15T10:30:00.000+0000",
			Updated:     "2025-01-16T14:20:00.000+0000",
		},
	}

	ti := jiraToTrackerIssue(ji)

	if ti.ID != "10001" {
		t.Errorf("ID = %q, want %q", ti.ID, "10001")
	}
	if ti.Identifier != "PROJ-42" {
		t.Errorf("Identifier = %q, want %q", ti.Identifier, "PROJ-42")
	}
	if ti.Title != "Fix login bug" {
		t.Errorf("Title = %q, want %q", ti.Title, "Fix login bug")
	}
	if ti.Description != "A plain text description" {
		t.Errorf("Description = %q, want %q", ti.Description, "A plain text description")
	}
	if ti.Assignee != "Alice" {
		t.Errorf("Assignee = %q, want %q", ti.Assignee, "Alice")
	}
	if ti.AssigneeEmail != "alice@example.com" {
		t.Errorf("AssigneeEmail = %q, want %q", ti.AssigneeEmail, "alice@example.com")
	}
	if ti.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if ti.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
	if len(ti.Labels) != 2 {
		t.Errorf("Labels length = %d, want 2", len(ti.Labels))
	}
}

func TestDescriptionToPlainText(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"null", json.RawMessage(`null`), ""},
		{"empty", json.RawMessage(``), ""},
		{"plain string", json.RawMessage(`"hello world"`), "hello world"},
		{"ADF document", json.RawMessage(`{
			"type": "doc",
			"content": [
				{
					"type": "paragraph",
					"content": [
						{"type": "text", "text": "First paragraph"}
					]
				},
				{
					"type": "paragraph",
					"content": [
						{"type": "text", "text": "Second paragraph"}
					]
				}
			]
		}`), "First paragraph\nSecond paragraph"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DescriptionToPlainText(tt.raw)
			if got != tt.want {
				t.Errorf("DescriptionToPlainText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPlainTextToADF(t *testing.T) {
	adf := PlainTextToADF("Hello\nWorld")
	if adf == nil {
		t.Fatal("PlainTextToADF returned nil")
	}

	var doc struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
		Content []struct {
			Type    string `json:"type"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(adf, &doc); err != nil {
		t.Fatalf("Failed to parse ADF: %v", err)
	}
	if doc.Type != "doc" {
		t.Errorf("doc type = %q, want %q", doc.Type, "doc")
	}
	if len(doc.Content) != 2 {
		t.Fatalf("content length = %d, want 2", len(doc.Content))
	}
	if doc.Content[0].Content[0].Text != "Hello" {
		t.Errorf("first paragraph text = %q, want %q", doc.Content[0].Content[0].Text, "Hello")
	}
}

func TestFieldMapperIssueToBeads(t *testing.T) {
	ji := &Issue{
		ID:   "10001",
		Key:  "PROJ-42",
		Self: "https://company.atlassian.net/rest/api/3/issue/10001",
		Fields: IssueFields{
			Summary:     "Test issue",
			Description: json.RawMessage(`"Description text"`),
			Status:      &StatusField{Name: "In Progress"},
			Priority:    &PriorityField{Name: "High"},
			IssueType:   &IssueTypeField{Name: "Bug"},
			Assignee:    &UserField{DisplayName: "Bob"},
			Labels:      []string{"frontend"},
			Created:     time.Now().Format(time.RFC3339),
			Updated:     time.Now().Format(time.RFC3339),
		},
	}

	ti := jiraToTrackerIssue(ji)
	mapper := &jiraFieldMapper{}
	conv := mapper.IssueToBeads(&ti)

	if conv == nil {
		t.Fatal("IssueToBeads returned nil")
	}
	if conv.Issue.Title != "Test issue" {
		t.Errorf("Title = %q, want %q", conv.Issue.Title, "Test issue")
	}
	if conv.Issue.Description != "Description text" {
		t.Errorf("Description = %q, want %q", conv.Issue.Description, "Description text")
	}
	if conv.Issue.Priority != 1 {
		t.Errorf("Priority = %d, want 1 (High)", conv.Issue.Priority)
	}
	if conv.Issue.Owner != "Bob" {
		t.Errorf("Owner = %q, want %q", conv.Issue.Owner, "Bob")
	}
}

func TestFieldMapperIssueToTracker(t *testing.T) {
	mapper := &jiraFieldMapper{}

	issue := &types.Issue{
		Title:       "New feature",
		Description: "Feature description",
		Priority:    0,
		IssueType:   types.TypeBug,
		Labels:      []string{"critical"},
	}

	fields := mapper.IssueToTracker(issue)

	if fields["summary"] != "New feature" {
		t.Errorf("summary = %v, want %q", fields["summary"], "New feature")
	}
	if fields["description"] == nil {
		t.Error("description should not be nil")
	}
	issueType, ok := fields["issuetype"].(map[string]string)
	if !ok || issueType["name"] != "Bug" {
		t.Errorf("issuetype = %v, want Bug", fields["issuetype"])
	}
	priority, ok := fields["priority"].(map[string]string)
	if !ok || priority["name"] != "Highest" {
		t.Errorf("priority = %v, want Highest", fields["priority"])
	}
}

func TestFieldMapperDescriptionV3UsesADF(t *testing.T) {
	mapper := &jiraFieldMapper{apiVersion: "3"}
	issue := &types.Issue{Title: "T", Description: "Hello world"}
	fields := mapper.IssueToTracker(issue)

	// v3: description must be ADF JSON (json.RawMessage), not a plain string.
	raw, ok := fields["description"].(json.RawMessage)
	if !ok {
		t.Fatalf("v3 description type = %T, want json.RawMessage", fields["description"])
	}
	var doc struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("v3 description is not valid JSON: %v", err)
	}
	if doc.Type != "doc" {
		t.Errorf("v3 description ADF type = %q, want %q", doc.Type, "doc")
	}
}

func TestFieldMapperDescriptionV2UsesPlainString(t *testing.T) {
	mapper := &jiraFieldMapper{apiVersion: "2"}
	issue := &types.Issue{Title: "T", Description: "Hello world"}
	fields := mapper.IssueToTracker(issue)

	// v2: description must be a plain string.
	desc, ok := fields["description"].(string)
	if !ok {
		t.Fatalf("v2 description type = %T, want string", fields["description"])
	}
	if desc != "Hello world" {
		t.Errorf("v2 description = %q, want %q", desc, "Hello world")
	}
}

func TestFieldMapperDescriptionEmptyAPIVersionDefaultsToADF(t *testing.T) {
	// Empty apiVersion should behave like v3 (ADF).
	mapper := &jiraFieldMapper{apiVersion: ""}
	issue := &types.Issue{Title: "T", Description: "text"}
	fields := mapper.IssueToTracker(issue)

	if _, ok := fields["description"].(json.RawMessage); !ok {
		t.Errorf("empty apiVersion description type = %T, want json.RawMessage (ADF)", fields["description"])
	}
}

func TestTrackerFieldMapperPropagatesVersion(t *testing.T) {
	tr := &Tracker{apiVersion: "2"}
	mapper := tr.FieldMapper().(*jiraFieldMapper)
	if mapper.apiVersion != "2" {
		t.Errorf("FieldMapper apiVersion = %q, want %q", mapper.apiVersion, "2")
	}
}

func TestTrackerFieldMapperDefaultVersion(t *testing.T) {
	// A tracker with no apiVersion set should produce a mapper that uses ADF (v3 behavior).
	tr := &Tracker{}
	issue := &types.Issue{Title: "T", Description: "desc"}
	fields := tr.FieldMapper().IssueToTracker(issue)
	if _, ok := fields["description"].(json.RawMessage); !ok {
		t.Errorf("default tracker description type = %T, want json.RawMessage (ADF)", fields["description"])
	}
}

// newTrackerWithServer creates a Tracker backed by a test HTTP server.
func newTrackerWithServer(srvURL, version string) *Tracker {
	return &Tracker{
		client:     newTestClient(srvURL, version),
		jiraURL:    srvURL,
		apiVersion: version,
	}
}

// issueResponse returns a Jira Issue JSON response with the given status name.
func issueResponse(key, statusName string) Issue {
	return Issue{
		ID:  "10001",
		Key: key,
		Fields: IssueFields{
			Status: &StatusField{Name: statusName},
		},
	}
}

func TestUpdateIssueAppliesTransitionWhenStatusChanges(t *testing.T) {
	const key = "PROJ-1"
	issuePath := "/rest/api/3/issue/" + key
	transitionsPath := issuePath + "/transitions"

	var transitionPostedID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == issuePath:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == issuePath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(issueResponse(key, "To Do"))
		case r.Method == http.MethodGet && r.URL.Path == transitionsPath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(TransitionsResult{
				Transitions: []Transition{
					{ID: "11", Name: "Start Progress", To: StatusField{Name: "In Progress"}},
					{ID: "31", Name: "Resolve", To: StatusField{Name: "Done"}},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == transitionsPath:
			body, _ := io.ReadAll(r.Body)
			var payload struct {
				Transition map[string]string `json:"transition"`
			}
			_ = json.Unmarshal(body, &payload)
			transitionPostedID = payload.Transition["id"]
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	tr := newTrackerWithServer(srv.URL, "3")
	_, err := tr.UpdateIssue(context.Background(), key, &types.Issue{
		Title:  "Test",
		Status: types.StatusInProgress,
	})
	if err != nil {
		t.Fatalf("UpdateIssue error: %v", err)
	}
	if transitionPostedID != "11" {
		t.Errorf("transition ID posted = %q, want %q", transitionPostedID, "11")
	}
}

func TestUpdateIssueSkipsTransitionWhenStatusUnchanged(t *testing.T) {
	const key = "PROJ-1"
	issuePath := "/rest/api/3/issue/" + key

	var transitionCalled bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == issuePath:
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == issuePath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(issueResponse(key, "In Progress"))
		case strings.Contains(r.URL.Path, "/transitions"):
			transitionCalled = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	tr := newTrackerWithServer(srv.URL, "3")
	_, err := tr.UpdateIssue(context.Background(), key, &types.Issue{
		Title:  "Updated title",
		Status: types.StatusInProgress, // matches current Jira status
	})
	if err != nil {
		t.Fatalf("UpdateIssue error: %v", err)
	}
	if transitionCalled {
		t.Error("transitions endpoint called unexpectedly when status was already correct")
	}
}

func TestStatusToTrackerUsesCustomMap(t *testing.T) {
	mapper := &jiraFieldMapper{
		statusMap: map[string]string{
			"open":        "Backlog",
			"in_progress": "Active Sprint",
			"closed":      "Released",
			"review":      "Code Review", // custom non-standard beads status
		},
	}

	tests := []struct {
		status types.Status
		want   string
	}{
		{types.StatusOpen, "Backlog"},
		{types.StatusInProgress, "Active Sprint"},
		{types.StatusClosed, "Released"},
		{types.Status("review"), "Code Review"},
		{types.StatusBlocked, "Blocked"}, // not in custom map → falls back to default
	}
	for _, tt := range tests {
		got, _ := mapper.StatusToTracker(tt.status).(string)
		if got != tt.want {
			t.Errorf("StatusToTracker(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestStatusToBeadsUsesCustomMap(t *testing.T) {
	mapper := &jiraFieldMapper{
		statusMap: map[string]string{
			"open":        "Backlog",
			"in_progress": "Active Sprint",
			"closed":      "Released",
			"review":      "Code Review", // custom non-standard beads status
		},
	}

	tests := []struct {
		jiraStatus string
		want       types.Status
	}{
		{"Backlog", types.StatusOpen},
		{"Active Sprint", types.StatusInProgress},
		{"Released", types.StatusClosed},
		{"Code Review", types.Status("review")},
		{"Done", types.StatusClosed},            // not in custom map → falls back to default
		{"To Do", types.StatusOpen},             // not in custom map → falls back to default
		{"In Progress", types.StatusInProgress}, // not in custom map → falls back to default
	}
	for _, tt := range tests {
		got := mapper.StatusToBeads(tt.jiraStatus)
		if got != tt.want {
			t.Errorf("StatusToBeads(%q) = %q, want %q", tt.jiraStatus, got, tt.want)
		}
	}
}

func TestStatusMapCaseInsensitiveMatch(t *testing.T) {
	mapper := &jiraFieldMapper{
		statusMap: map[string]string{"in_progress": "Active Sprint"},
	}

	// Custom map match should be case-insensitive.
	got := mapper.StatusToBeads("active sprint")
	if got != types.StatusInProgress {
		t.Errorf("StatusToBeads(\"active sprint\") = %q, want %q", got, types.StatusInProgress)
	}
}

// configStore is a minimal storage.Storage stub for testing Init() config loading.
// Only GetConfig and GetAllConfig are implemented; all other methods are no-ops.
type configStore struct {
	data map[string]string
}

func (s *configStore) GetConfig(_ context.Context, key string) (string, error) {
	return s.data[key], nil
}
func (s *configStore) GetAllConfig(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.data))
	for k, v := range s.data {
		out[k] = v
	}
	return out, nil
}

// Storage interface stubs — not exercised by Init().
func (s *configStore) SetConfig(_ context.Context, _, _ string) error { return nil }
func (s *configStore) CreateIssue(_ context.Context, _ *types.Issue, _ string) error {
	return nil
}
func (s *configStore) CreateIssues(_ context.Context, _ []*types.Issue, _ string) error {
	return nil
}
func (s *configStore) GetIssue(_ context.Context, _ string) (*types.Issue, error) { return nil, nil }
func (s *configStore) GetIssueByExternalRef(_ context.Context, _ string) (*types.Issue, error) {
	return nil, nil
}
func (s *configStore) GetIssuesByIDs(_ context.Context, _ []string) ([]*types.Issue, error) {
	return nil, nil
}
func (s *configStore) UpdateIssue(_ context.Context, _ string, _ map[string]interface{}, _ string) error {
	return nil
}
func (s *configStore) CloseIssue(_ context.Context, _, _, _, _ string) error { return nil }
func (s *configStore) DeleteIssue(_ context.Context, _ string) error          { return nil }
func (s *configStore) SearchIssues(_ context.Context, _ string, _ types.IssueFilter) ([]*types.Issue, error) {
	return nil, nil
}
func (s *configStore) AddDependency(_ context.Context, _ *types.Dependency, _ string) error {
	return nil
}
func (s *configStore) RemoveDependency(_ context.Context, _, _, _ string) error { return nil }
func (s *configStore) GetDependencies(_ context.Context, _ string) ([]*types.Issue, error) {
	return nil, nil
}
func (s *configStore) GetDependents(_ context.Context, _ string) ([]*types.Issue, error) {
	return nil, nil
}
func (s *configStore) GetDependenciesWithMetadata(_ context.Context, _ string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *configStore) GetDependentsWithMetadata(_ context.Context, _ string) ([]*types.IssueWithDependencyMetadata, error) {
	return nil, nil
}
func (s *configStore) GetDependencyTree(_ context.Context, _ string, _ int, _, _ bool) ([]*types.TreeNode, error) {
	return nil, nil
}
func (s *configStore) AddLabel(_ context.Context, _, _, _ string) error    { return nil }
func (s *configStore) RemoveLabel(_ context.Context, _, _, _ string) error { return nil }
func (s *configStore) GetLabels(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (s *configStore) GetIssuesByLabel(_ context.Context, _ string) ([]*types.Issue, error) {
	return nil, nil
}
func (s *configStore) GetReadyWork(_ context.Context, _ types.WorkFilter) ([]*types.Issue, error) {
	return nil, nil
}
func (s *configStore) GetBlockedIssues(_ context.Context, _ types.WorkFilter) ([]*types.BlockedIssue, error) {
	return nil, nil
}
func (s *configStore) GetEpicsEligibleForClosure(_ context.Context) ([]*types.EpicStatus, error) {
	return nil, nil
}
func (s *configStore) AddIssueComment(_ context.Context, _, _, _ string) (*types.Comment, error) {
	return nil, nil
}
func (s *configStore) GetIssueComments(_ context.Context, _ string) ([]*types.Comment, error) {
	return nil, nil
}
func (s *configStore) GetEvents(_ context.Context, _ string, _ int) ([]*types.Event, error) {
	return nil, nil
}
func (s *configStore) GetAllEventsSince(_ context.Context, _ int64) ([]*types.Event, error) {
	return nil, nil
}
func (s *configStore) GetStatistics(_ context.Context) (*types.Statistics, error) { return nil, nil }
func (s *configStore) RunInTransaction(_ context.Context, _ string, _ func(tx storage.Transaction) error) error {
	return nil
}
func (s *configStore) Close() error { return nil }

func TestInitLoadsCustomStatusMapFromAllConfig(t *testing.T) {
	store := &configStore{
		data: map[string]string{
			"jira.url":                  "https://example.atlassian.net",
			"jira.project":              "PROJ",
			"jira.api_token":            "token123",
			"jira.status_map.open":      "Backlog",
			"jira.status_map.in_progress": "Active Sprint",
			"jira.status_map.review":    "Code Review", // custom non-standard beads status
		},
	}

	tr := &Tracker{}
	if err := tr.Init(context.Background(), store); err != nil {
		t.Fatalf("Init error: %v", err)
	}

	mapper := tr.FieldMapper()

	tests := []struct {
		status types.Status
		want   string
	}{
		{types.StatusOpen, "Backlog"},
		{types.StatusInProgress, "Active Sprint"},
		{types.Status("review"), "Code Review"},
		{types.StatusClosed, "Done"},    // not in store → falls back to default
		{types.StatusBlocked, "Blocked"}, // not in store → falls back to default
	}
	for _, tt := range tests {
		got, _ := mapper.StatusToTracker(tt.status).(string)
		if got != tt.want {
			t.Errorf("StatusToTracker(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}
