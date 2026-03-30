package ado

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// setupTestServer creates an httptest server and a Client pointed at it.
func setupTestServer(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	client, err := NewClient(NewSecretString("test-pat"), "testorg", "testproject").
		WithBaseURL(ts.URL)
	if err != nil {
		t.Fatalf("WithBaseURL(%s) error: %v", ts.URL, err)
	}
	client = client.WithHTTPClient(ts.Client())
	return client, ts
}

func TestClient_doRequest_Auth(t *testing.T) {
	var gotAuth string
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	_, err := client.doRequest(context.Background(), http.MethodGet, client.apiBase()+"/test", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte(":test-pat"))
	if gotAuth != expected {
		t.Errorf("auth header = %q, want %q", gotAuth, expected)
	}
}

func TestClient_doRequest_RetryOn429(t *testing.T) {
	var attempts int32
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	body, err := client.doRequest(context.Background(), http.MethodGet, client.apiBase()+"/test", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&attempts) < 2 {
		t.Error("expected at least 2 attempts for 429 retry")
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestClient_doRequest_NoRetryOn401(t *testing.T) {
	var attempts int32
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	})

	_, err := client.doRequest(context.Background(), http.MethodGet, client.apiBase()+"/test", "", nil)
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected exactly 1 attempt for 401, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestClient_apiBase(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		org     string
		project string
		want    string
	}{
		{
			name:    "cloud default",
			baseURL: "",
			org:     "myorg",
			project: "myproject",
			want:    "https://dev.azure.com/myorg/myproject/_apis",
		},
		{
			name:    "on-prem custom URL",
			baseURL: "https://tfs.example.com/collection",
			org:     "ignored",
			project: "myproject",
			want:    "https://tfs.example.com/collection/myproject/_apis",
		},
		{
			name:    "trailing slash stripped",
			baseURL: "https://tfs.example.com/collection/",
			org:     "ignored",
			project: "myproject",
			want:    "https://tfs.example.com/collection/myproject/_apis",
		},
		{
			name:    "special characters in project",
			baseURL: "",
			org:     "myorg",
			project: "my project",
			want:    "https://dev.azure.com/myorg/my%20project/_apis",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(NewSecretString("pat"), tt.org, tt.project)
			if tt.baseURL != "" {
				var err error
				c, err = c.WithBaseURL(tt.baseURL)
				if err != nil {
					t.Fatalf("WithBaseURL(%q) error: %v", tt.baseURL, err)
				}
			}
			got := c.apiBase()
			if got != tt.want {
				t.Errorf("apiBase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClient_FetchWorkItems(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/wit/workitems") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		ids := r.URL.Query().Get("ids")
		if ids != "1,2,3" {
			t.Errorf("unexpected ids param: %s", ids)
		}
		expand := r.URL.Query().Get("$expand")
		if expand != "All" {
			t.Errorf("unexpected expand param: %s", expand)
		}
		resp := `{"count":2,"value":[{"id":1,"rev":1,"fields":{"System.Title":"Item 1"},"url":"https://example.com/1"},{"id":2,"rev":2,"fields":{"System.Title":"Item 2"},"url":"https://example.com/2"}]}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	items, err := client.FetchWorkItems(context.Background(), []int{1, 2, 3})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].ID != 1 || items[0].GetStringField("System.Title") != "Item 1" {
		t.Errorf("unexpected first item: %+v", items[0])
	}
}

func TestClient_FetchWorkItems_Batching(t *testing.T) {
	var requestCount int32
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		ids := r.URL.Query().Get("ids")
		idList := strings.Split(ids, ",")
		var items []string
		for _, id := range idList {
			items = append(items, fmt.Sprintf(`{"id":%s,"rev":1,"fields":{},"url":"https://example.com/%s"}`, id, id))
		}
		resp := fmt.Sprintf(`{"count":%d,"value":[%s]}`, len(items), strings.Join(items, ","))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	// Create 250 IDs to force 2 batches (MaxBatchSize=200).
	ids := make([]int, 250)
	for i := range ids {
		ids[i] = i + 1
	}

	items, err := client.FetchWorkItems(context.Background(), ids)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 250 {
		t.Errorf("expected 250 items, got %d", len(items))
	}
	if atomic.LoadInt32(&requestCount) != 2 {
		t.Errorf("expected 2 batch requests, got %d", atomic.LoadInt32(&requestCount))
	}
}

func TestClient_FetchWorkItems_EmptyIDs(t *testing.T) {
	requestMade := false
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requestMade = true
		w.WriteHeader(http.StatusOK)
	})

	items, err := client.FetchWorkItems(context.Background(), []int{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Errorf("expected nil, got %v", items)
	}
	if requestMade {
		t.Error("no request should be made for empty IDs")
	}
}

func TestClient_FetchWorkItemsSince(t *testing.T) {
	step := 0
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/wit/wiql"):
			// WIQL query step.
			if r.Method != http.MethodPost {
				t.Errorf("WIQL: expected POST, got %s", r.Method)
			}
			body, _ := io.ReadAll(r.Body)
			var req WIQLRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("failed to parse WIQL body: %v", err)
			}
			if !strings.Contains(req.Query, "System.ChangedDate") {
				t.Error("WIQL query should reference ChangedDate")
			}
			if !strings.Contains(req.Query, "testproject") {
				t.Error("WIQL query should reference project name")
			}
			resp := `{"workItems":[{"id":10,"url":"https://example.com/10"},{"id":20,"url":"https://example.com/20"}]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
			step++

		case strings.Contains(r.URL.Path, "/wit/workitems"):
			// Batch GET step.
			if step == 0 {
				t.Error("batch GET called before WIQL")
			}
			resp := `{"count":2,"value":[{"id":10,"rev":1,"fields":{"System.Title":"Item 10"}},{"id":20,"rev":1,"fields":{"System.Title":"Item 20"}}]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))

		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	items, err := client.FetchWorkItemsSince(context.Background(), since, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestClient_CreateWorkItem(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/wit/workitems/$Task") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		ct := r.Header.Get("Content-Type")
		if ct != "application/json-patch+json" {
			t.Errorf("content-type = %q, want application/json-patch+json", ct)
		}

		body, _ := io.ReadAll(r.Body)
		var ops []PatchOperation
		if err := json.Unmarshal(body, &ops); err != nil {
			t.Fatalf("failed to parse patch ops: %v", err)
		}
		if len(ops) != 2 {
			t.Errorf("expected 2 ops, got %d", len(ops))
		}
		// Ops should be sorted by path.
		for _, op := range ops {
			if op.Op != "add" {
				t.Errorf("expected op 'add', got %q", op.Op)
			}
		}

		resp := `{"id":42,"rev":1,"fields":{"System.Title":"New Task","System.State":"New"},"url":"https://example.com/42"}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	fields := map[string]interface{}{
		FieldTitle: "New Task",
		FieldState: "New",
	}
	item, err := client.CreateWorkItem(context.Background(), "Task", fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.ID != 42 {
		t.Errorf("expected ID 42, got %d", item.ID)
	}
	if item.GetStringField(FieldTitle) != "New Task" {
		t.Errorf("unexpected title: %s", item.GetStringField(FieldTitle))
	}
}

func TestClient_UpdateWorkItem(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/wit/workitems/42") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		ct := r.Header.Get("Content-Type")
		if ct != "application/json-patch+json" {
			t.Errorf("content-type = %q, want application/json-patch+json", ct)
		}

		body, _ := io.ReadAll(r.Body)
		var ops []PatchOperation
		if err := json.Unmarshal(body, &ops); err != nil {
			t.Fatalf("failed to parse patch ops: %v", err)
		}
		if len(ops) != 1 {
			t.Errorf("expected 1 op, got %d", len(ops))
		}

		resp := `{"id":42,"rev":2,"fields":{"System.Title":"Updated Task","System.State":"Active"},"url":"https://example.com/42"}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	fields := map[string]interface{}{
		FieldState: "Active",
	}
	item, err := client.UpdateWorkItem(context.Background(), 42, fields)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item.Rev != 2 {
		t.Errorf("expected rev 2, got %d", item.Rev)
	}
}

func TestClient_ListProjects(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		// ListProjects is org-level — path should NOT include testproject.
		if strings.Contains(r.URL.Path, "testproject") {
			t.Errorf("ListProjects should be org-level, got path: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.Path, "/projects") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := `{"count":2,"value":[{"id":"p1","name":"Project Alpha","description":"First","url":"https://example.com/p1","state":"wellFormed"},{"id":"p2","name":"Project Beta","description":"Second","url":"https://example.com/p2","state":"wellFormed"}]}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	projects, err := client.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if projects[0].Name != "Project Alpha" {
		t.Errorf("unexpected name: %s", projects[0].Name)
	}
}

func TestClient_GetWorkItemTypes(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/wit/workitemtypes") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := `{"count":2,"value":[{"name":"Bug","description":"A bug"},{"name":"Task","description":"A task"}]}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	types, err := client.GetWorkItemTypes(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("expected 2 types, got %d", len(types))
	}
	if types[0].Name != "Bug" {
		t.Errorf("unexpected type name: %s", types[0].Name)
	}
}

func TestClient_GetWorkItemStates(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/wit/workitemtypes/Bug/states") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := `{"count":3,"value":[{"name":"New","color":"b2b2b2","category":"Proposed"},{"name":"Active","color":"007acc","category":"InProgress"},{"name":"Closed","color":"339933","category":"Completed"}]}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	states, err := client.GetWorkItemStates(context.Background(), "Bug")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(states) != 3 {
		t.Fatalf("expected 3 states, got %d", len(states))
	}
	if states[0].Name != "New" || states[0].Category != "Proposed" {
		t.Errorf("unexpected first state: %+v", states[0])
	}
}

func TestClient_escapeWIQL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "plain string", input: "hello world", want: "hello world"},
		{name: "single quotes", input: "it's a test", want: "it''s a test"},
		{name: "backslashes", input: `path\to\thing`, want: `path\\to\\thing`},
		{name: "both", input: `it's a\path`, want: `it''s a\\path`},
		{name: "empty", input: "", want: ""},
		{name: "multiple quotes", input: "a'b'c", want: "a''b''c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeWIQL(tt.input)
			if got != tt.want {
				t.Errorf("escapeWIQL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatWIQLDate(t *testing.T) {
	tests := []struct {
		name string
		time time.Time
		want string
	}{
		{
			name: "UTC time",
			time: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
			want: "2024-06-01T00:00:00Z",
		},
		{
			name: "UTC time with seconds",
			time: time.Date(2024, 6, 15, 14, 30, 45, 0, time.UTC),
			want: "2024-06-15T14:30:45Z",
		},
		{
			name: "non-UTC positive offset converts to UTC",
			time: time.Date(2024, 6, 1, 12, 0, 0, 0, time.FixedZone("IST", 5*3600+30*60)),
			want: "2024-06-01T06:30:00Z",
		},
		{
			name: "non-UTC negative offset converts to UTC",
			time: time.Date(2024, 6, 1, 10, 0, 0, 0, time.FixedZone("EST", -5*3600)),
			want: "2024-06-01T15:00:00Z",
		},
		{
			name: "nanoseconds truncated to seconds",
			time: time.Date(2024, 6, 1, 10, 30, 45, 123456789, time.UTC),
			want: "2024-06-01T10:30:45Z",
		},
		{
			name: "midnight boundary from non-UTC",
			time: time.Date(2024, 6, 2, 2, 0, 0, 0, time.FixedZone("CEST", 2*3600)),
			want: "2024-06-02T00:00:00Z",
		},
		{
			name: "date rollback across day boundary",
			time: time.Date(2024, 6, 1, 1, 0, 0, 0, time.FixedZone("JST", 9*3600)),
			want: "2024-05-31T16:00:00Z",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatWIQLDate(tt.time)
			if got != tt.want {
				t.Errorf("formatWIQLDate() = %q, want %q", got, tt.want)
			}
			// Verify output always ends with Z (UTC indicator)
			if !strings.HasSuffix(got, "Z") {
				t.Errorf("formatWIQLDate() output %q must end with Z for WIQL compatibility", got)
			}
		})
	}
}

func TestClient_AddWorkItemLink(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/wit/workitems/10") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		ct := r.Header.Get("Content-Type")
		if ct != "application/json-patch+json" {
			t.Errorf("content-type = %q, want application/json-patch+json", ct)
		}

		body, _ := io.ReadAll(r.Body)
		var ops []PatchOperation
		if err := json.Unmarshal(body, &ops); err != nil {
			t.Fatalf("failed to parse patch ops: %v", err)
		}
		if len(ops) != 1 {
			t.Fatalf("expected 1 op, got %d", len(ops))
		}
		if ops[0].Op != "add" {
			t.Errorf("expected op 'add', got %q", ops[0].Op)
		}
		if ops[0].Path != "/relations/-" {
			t.Errorf("expected path '/relations/-', got %q", ops[0].Path)
		}

		// Verify the value contains the right relation info.
		valMap, ok := ops[0].Value.(map[string]interface{})
		if !ok {
			// Value may have been re-serialized; unmarshal again.
			valBytes, _ := json.Marshal(ops[0].Value)
			valMap = make(map[string]interface{})
			_ = json.Unmarshal(valBytes, &valMap)
		}
		if valMap["rel"] != RelChild {
			t.Errorf("expected rel %q, got %v", RelChild, valMap["rel"])
		}
		if valMap["url"] != "https://example.com/workitems/20" {
			t.Errorf("unexpected url: %v", valMap["url"])
		}

		// Return updated work item.
		resp := `{"id":10,"rev":3,"fields":{},"url":"https://example.com/10"}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	err := client.AddWorkItemLink(context.Background(), 10, "https://example.com/workitems/20", RelChild, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPullFilters_Validate(t *testing.T) {
	tests := []struct {
		name    string
		filters PullFilters
		wantErr bool
	}{
		{
			name:    "empty filters",
			filters: PullFilters{},
			wantErr: false,
		},
		{
			name:    "valid area path with backslash",
			filters: PullFilters{AreaPath: `MyProject\Backend`},
			wantErr: false,
		},
		{
			name:    "valid area path with spaces",
			filters: PullFilters{AreaPath: "My Project/Sub Area"},
			wantErr: false,
		},
		{
			name:    "invalid area path with semicolon",
			filters: PullFilters{AreaPath: "My;Path"},
			wantErr: true,
		},
		{
			name:    "valid state",
			filters: PullFilters{States: []string{"Active"}},
			wantErr: false,
		},
		{
			name:    "invalid state with special chars",
			filters: PullFilters{States: []string{"Active; DROP TABLE"}},
			wantErr: true,
		},
		{
			name:    "valid work item types",
			filters: PullFilters{WorkItemTypes: []string{"Bug", "Task"}},
			wantErr: false,
		},
		{
			name: "all filters set and valid",
			filters: PullFilters{
				AreaPath:      `MyProject\Backend`,
				IterationPath: `MyProject\Sprint 1`,
				WorkItemTypes: []string{"Bug", "User Story"},
				States:        []string{"Active", "New"},
			},
			wantErr: false,
		},
		{
			name:    "invalid iteration path",
			filters: PullFilters{IterationPath: "Bad;Path"},
			wantErr: true,
		},
		{
			name:    "invalid work item type",
			filters: PullFilters{WorkItemTypes: []string{"Bug", "Bad;Type"}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.filters.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateOrg(t *testing.T) {
	tests := []struct {
		name    string
		org     string
		wantErr bool
	}{
		{name: "simple org", org: "myorg", wantErr: false},
		{name: "org with hyphens and dots", org: "my-org.name", wantErr: false},
		{name: "org with spaces", org: "my org", wantErr: true},
		{name: "empty org", org: "", wantErr: true},
		{name: "org with semicolon", org: "my;org", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateOrg(tt.org)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateOrg(%q) error = %v, wantErr %v", tt.org, err, tt.wantErr)
			}
		})
	}
}

func TestValidateProject(t *testing.T) {
	tests := []struct {
		name    string
		project string
		wantErr bool
	}{
		{name: "simple project", project: "MyProject", wantErr: false},
		{name: "project with spaces", project: "My Project", wantErr: false},
		{name: "project with apostrophe", project: "Project's Name", wantErr: false},
		{name: "empty project", project: "", wantErr: true},
		{name: "project with semicolon", project: "My;Project", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProject(tt.project)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProject(%q) error = %v, wantErr %v", tt.project, err, tt.wantErr)
			}
		})
	}
}

func TestBuildPullWIQL(t *testing.T) {
	c := NewClient(NewSecretString("pat"), "myorg", "testproject")
	since := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	sinceNonUTC := time.Date(2024, 6, 1, 12, 0, 0, 0, time.FixedZone("IST", 5*3600+30*60))

	tests := []struct {
		name     string
		since    *time.Time
		filters  *PullFilters
		contains []string
		absent   []string
	}{
		{
			name:    "no filters no since",
			since:   nil,
			filters: nil,
			contains: []string{
				"[System.TeamProject] = 'testproject'",
				"[System.IsDeleted] = false",
			},
			absent: []string{"ChangedDate >=", "AreaPath", "WorkItemType", "State"},
		},
		{
			name:    "with since",
			since:   &since,
			filters: nil,
			contains: []string{
				"[System.ChangedDate] >= '2024-06-01T00:00:00Z'",
			},
		},
		{
			name:    "with since non-UTC converts to UTC",
			since:   &sinceNonUTC,
			filters: nil,
			contains: []string{
				"[System.ChangedDate] >= '2024-06-01T06:30:00Z'",
			},
		},
		{
			name:    "with area path",
			since:   nil,
			filters: &PullFilters{AreaPath: `MyProject\Backend`},
			contains: []string{
				`[System.AreaPath] UNDER 'MyProject\\Backend'`,
			},
		},
		{
			name:    "with work item types",
			since:   nil,
			filters: &PullFilters{WorkItemTypes: []string{"Bug", "Task"}},
			contains: []string{
				"[System.WorkItemType] IN ('Bug', 'Task')",
			},
		},
		{
			name:    "with states",
			since:   nil,
			filters: &PullFilters{States: []string{"Active", "New"}},
			contains: []string{
				"[System.State] IN ('Active', 'New')",
			},
		},
		{
			name:  "all filters",
			since: &since,
			filters: &PullFilters{
				AreaPath:      `MyProject\Backend`,
				IterationPath: `MyProject\Sprint 1`,
				WorkItemTypes: []string{"Bug"},
				States:        []string{"Active"},
			},
			contains: []string{
				"[System.TeamProject] = 'testproject'",
				"[System.IsDeleted] = false",
				"[System.ChangedDate] >= '2024-06-01T00:00:00Z'",
				`[System.AreaPath] UNDER 'MyProject\\Backend'`,
				`[System.IterationPath] UNDER 'MyProject\\Sprint 1'`,
				"[System.WorkItemType] IN ('Bug')",
				"[System.State] IN ('Active')",
			},
		},
		{
			name:    "WIQL injection escaped",
			since:   nil,
			filters: &PullFilters{AreaPath: "Path' OR 1=1--"},
			contains: []string{
				"UNDER 'Path'' OR 1=1--'",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.buildPullWIQL(tt.since, tt.filters)
			for _, s := range tt.contains {
				if !strings.Contains(got, s) {
					t.Errorf("buildPullWIQL() missing %q in:\n%s", s, got)
				}
			}
			for _, s := range tt.absent {
				if strings.Contains(got, s) {
					t.Errorf("buildPullWIQL() should not contain %q in:\n%s", s, got)
				}
			}
		})
	}
}

func TestFetchWorkItemsSince_WithFilters(t *testing.T) {
	step := 0
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/wit/wiql"):
			if r.Method != http.MethodPost {
				t.Errorf("WIQL: expected POST, got %s", r.Method)
			}
			body, _ := io.ReadAll(r.Body)
			var req WIQLRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("failed to parse WIQL body: %v", err)
			}
			if !strings.Contains(req.Query, "[System.AreaPath] UNDER") {
				t.Error("WIQL query should contain area path filter")
			}
			if !strings.Contains(req.Query, "[System.WorkItemType] IN") {
				t.Error("WIQL query should contain work item type filter")
			}
			if !strings.Contains(req.Query, "[System.State] IN") {
				t.Error("WIQL query should contain state filter")
			}
			resp := `{"workItems":[{"id":5,"url":"https://example.com/5"}]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
			step++

		case strings.Contains(r.URL.Path, "/wit/workitems"):
			if step == 0 {
				t.Error("batch GET called before WIQL")
			}
			resp := `{"count":1,"value":[{"id":5,"rev":1,"fields":{"System.Title":"Filtered Item"}}]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))

		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	filters := &PullFilters{
		AreaPath:      `MyProject\Backend`,
		WorkItemTypes: []string{"Bug", "Task"},
		States:        []string{"Active"},
	}
	items, err := client.FetchWorkItemsSince(context.Background(), since, filters)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].GetStringField("System.Title") != "Filtered Item" {
		t.Errorf("unexpected title: %s", items[0].GetStringField("System.Title"))
	}
}

func TestFetchWorkItemsSince_InvalidFilters(t *testing.T) {
	requestMade := false
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requestMade = true
		w.WriteHeader(http.StatusOK)
	})

	since := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	filters := &PullFilters{
		AreaPath: "Bad;Path",
	}
	_, err := client.FetchWorkItemsSince(context.Background(), since, filters)
	if err == nil {
		t.Fatal("expected error for invalid filters")
	}
	if !strings.Contains(err.Error(), "invalid pull filters") {
		t.Errorf("error should mention invalid pull filters: %v", err)
	}
	if requestMade {
		t.Error("no HTTP request should be made for invalid filters")
	}
}

func TestFetchAllWorkItems(t *testing.T) {
	step := 0
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/wit/wiql"):
			body, _ := io.ReadAll(r.Body)
			var req WIQLRequest
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("failed to parse WIQL body: %v", err)
			}
			if strings.Contains(req.Query, "ChangedDate >=") {
				t.Error("FetchAllWorkItems should not include ChangedDate filter")
			}
			if !strings.Contains(req.Query, "[System.IsDeleted] = false") {
				t.Error("WIQL query should include IsDeleted filter")
			}
			resp := `{"workItems":[{"id":1,"url":"u"},{"id":2,"url":"u"}]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
			step++

		case strings.Contains(r.URL.Path, "/wit/workitems"):
			resp := `{"count":2,"value":[{"id":1,"rev":1,"fields":{}},{"id":2,"rev":1,"fields":{}}]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	items, err := client.FetchAllWorkItems(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}

func TestFetchAllWorkItems_InvalidFilters(t *testing.T) {
	requestMade := false
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		requestMade = true
		w.WriteHeader(http.StatusOK)
	})

	_, err := client.FetchAllWorkItems(context.Background(), &PullFilters{States: []string{"Bad;State"}})
	if err == nil {
		t.Fatal("expected error for invalid filters")
	}
	if requestMade {
		t.Error("no HTTP request should be made for invalid filters")
	}
}

func TestClient_RemoveWorkItemLink(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/wit/workitems/10") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		body, _ := io.ReadAll(r.Body)
		var ops []PatchOperation
		if err := json.Unmarshal(body, &ops); err != nil {
			t.Fatalf("failed to parse patch ops: %v", err)
		}
		if len(ops) != 1 {
			t.Fatalf("expected 1 op, got %d", len(ops))
		}
		if ops[0].Op != "remove" {
			t.Errorf("expected op 'remove', got %q", ops[0].Op)
		}
		if ops[0].Path != "/relations/2" {
			t.Errorf("expected path '/relations/2', got %q", ops[0].Path)
		}

		resp := `{"id":10,"rev":4,"fields":{},"url":"https://example.com/10"}`
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(resp))
	})

	err := client.RemoveWorkItemLink(context.Background(), 10, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_RemoveWorkItemLink_Error(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"relation index out of range"}`))
	})

	err := client.RemoveWorkItemLink(context.Background(), 10, 99)
	if err == nil {
		t.Fatal("expected error for bad request")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400: %v", err)
	}
	if !strings.Contains(err.Error(), "relation index out of range") {
		t.Errorf("error should include response body: %v", err)
	}
}

func TestClient_doRequest_RetryOn503(t *testing.T) {
	var attempts int32
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"service unavailable"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	body, err := client.doRequest(context.Background(), http.MethodGet, client.apiBase()+"/test", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if atomic.LoadInt32(&attempts) < 3 {
		t.Errorf("expected at least 3 attempts for 503 retry, got %d", atomic.LoadInt32(&attempts))
	}
	if !strings.Contains(string(body), "ok") {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestClient_doRequest_NoRetryOn400(t *testing.T) {
	var attempts int32
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad request"}`))
	})

	_, err := client.doRequest(context.Background(), http.MethodGet, client.apiBase()+"/test", "", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400: %v", err)
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Errorf("error should include response body: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected exactly 1 attempt for 400, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestClient_doRequest_NoRetryOn404(t *testing.T) {
	var attempts int32
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found"}`))
	})

	_, err := client.doRequest(context.Background(), http.MethodGet, client.apiBase()+"/test", "", nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected exactly 1 attempt for 404, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestClient_doRequest_NoRetryOn403(t *testing.T) {
	var attempts int32
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"forbidden"}`))
	})

	_, err := client.doRequest(context.Background(), http.MethodGet, client.apiBase()+"/test", "", nil)
	if err == nil {
		t.Fatal("expected error for 403")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should mention 403: %v", err)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("expected exactly 1 attempt for 403, got %d", atomic.LoadInt32(&attempts))
	}
}

func TestClient_doRequest_ContextCancellation(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"unavailable"}`))
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := client.doRequest(ctx, http.MethodGet, client.apiBase()+"/test", "", nil)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestClient_doRequest_PostBody(t *testing.T) {
	var gotBody string
	var gotContentType string
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})

	payload := map[string]string{"key": "value"}
	_, err := client.doRequest(context.Background(), http.MethodPost, client.apiBase()+"/test", "application/json", payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
	if !strings.Contains(gotBody, `"key":"value"`) {
		t.Errorf("unexpected body: %s", gotBody)
	}
}

func TestClient_orgBase(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		org     string
		want    string
	}{
		{
			name:    "cloud default",
			baseURL: "",
			org:     "myorg",
			want:    "https://dev.azure.com/myorg/_apis",
		},
		{
			name:    "custom base URL (on-prem)",
			baseURL: "https://tfs.example.com/collection",
			org:     "ignored",
			want:    "https://tfs.example.com/collection/_apis",
		},
		{
			name:    "trailing slash stripped",
			baseURL: "https://tfs.example.com/collection/",
			org:     "ignored",
			want:    "https://tfs.example.com/collection/_apis",
		},
		{
			name:    "legacy visualstudio.com URL",
			baseURL: "https://myorg.visualstudio.com",
			org:     "myorg",
			want:    "https://myorg.visualstudio.com/_apis",
		},
		{
			name:    "special characters in org",
			baseURL: "",
			org:     "my org",
			want:    "https://dev.azure.com/my%20org/_apis",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(NewSecretString("pat"), tt.org, "proj")
			if tt.baseURL != "" {
				var err error
				c, err = c.WithBaseURL(tt.baseURL)
				if err != nil {
					t.Fatalf("WithBaseURL(%q) error: %v", tt.baseURL, err)
				}
			}
			got := c.orgBase()
			if got != tt.want {
				t.Errorf("orgBase() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClient_CreateWorkItem_Error400(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Field 'System.Title' is required"}`))
	})

	fields := map[string]interface{}{
		FieldState: "New",
	}
	_, err := client.CreateWorkItem(context.Background(), "Task", fields)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400: %v", err)
	}
	if !strings.Contains(err.Error(), "System.Title") {
		t.Errorf("error should include response body with field name: %v", err)
	}
}

func TestClient_CreateWorkItem_InvalidJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})

	fields := map[string]interface{}{FieldTitle: "Test"}
	_, err := client.CreateWorkItem(context.Background(), "Task", fields)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "failed to parse create response") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_UpdateWorkItem_Error400(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Invalid field value"}`))
	})

	fields := map[string]interface{}{
		FieldState: "InvalidState",
	}
	_, err := client.UpdateWorkItem(context.Background(), 42, fields)
	if err == nil {
		t.Fatal("expected error for 400")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400: %v", err)
	}
	if !strings.Contains(err.Error(), "Invalid field value") {
		t.Errorf("error should include response body: %v", err)
	}
}

func TestClient_UpdateWorkItem_InvalidJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})

	fields := map[string]interface{}{FieldState: "Active"}
	_, err := client.UpdateWorkItem(context.Background(), 42, fields)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "failed to parse update response") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_ListProjects_Error401(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid PAT"}`))
	})

	_, err := client.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid PAT") {
		t.Errorf("error should include response body: %v", err)
	}
}

func TestClient_ListProjects_InvalidJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})

	_, err := client.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "failed to parse projects response") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_ListProjects_InvalidValueJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count":1,"value":"not an array"}`))
	})

	_, err := client.ListProjects(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid value JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse projects value") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_GetWorkItemTypes_Error(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	})

	_, err := client.GetWorkItemTypes(context.Background())
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401: %v", err)
	}
}

func TestClient_GetWorkItemTypes_InvalidJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})

	_, err := client.GetWorkItemTypes(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse work item types response") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_GetWorkItemTypes_InvalidValueJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count":1,"value":"not an array"}`))
	})

	_, err := client.GetWorkItemTypes(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid value JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse work item types value") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_GetWorkItemStates_Error(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	})

	_, err := client.GetWorkItemStates(context.Background(), "Bug")
	if err == nil {
		t.Fatal("expected error for 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401: %v", err)
	}
}

func TestClient_GetWorkItemStates_InvalidJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})

	_, err := client.GetWorkItemStates(context.Background(), "Bug")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse work item states response") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_GetWorkItemStates_InvalidValueJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count":1,"value":"not an array"}`))
	})

	_, err := client.GetWorkItemStates(context.Background(), "Bug")
	if err == nil {
		t.Fatal("expected error for invalid value JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse work item states value") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_fetchWorkItemsByWIQL_EmptyResults(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/wit/wiql") {
			resp := `{"workItems":[]}`
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(resp))
			return
		}
		t.Errorf("unexpected request to: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})

	items, err := client.FetchAllWorkItems(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Errorf("expected nil for empty results, got %v", items)
	}
}

func TestClient_fetchWorkItemsByWIQL_Error(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"invalid WIQL query"}`))
	})

	_, err := client.FetchAllWorkItems(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for WIQL failure")
	}
	if !strings.Contains(err.Error(), "failed to execute WIQL query") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_fetchWorkItemsByWIQL_InvalidJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})

	_, err := client.FetchAllWorkItems(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse WIQL response") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_FetchWorkItems_Error(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"work items not found"}`))
	})

	_, err := client.FetchWorkItems(context.Background(), []int{999})
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "failed to fetch work items") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_FetchWorkItems_InvalidJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})

	_, err := client.FetchWorkItems(context.Background(), []int{1})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse work items response") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClient_FetchWorkItems_InvalidValueJSON(t *testing.T) {
	client, _ := setupTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count":1,"value":"not an array"}`))
	})

	_, err := client.FetchWorkItems(context.Background(), []int{1})
	if err == nil {
		t.Fatal("expected error for invalid value JSON")
	}
	if !strings.Contains(err.Error(), "failed to parse work items value") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateURLScheme(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "https allowed", url: "https://dev.azure.com/org", wantErr: false},
		{name: "https on-prem", url: "https://tfs.example.com/collection", wantErr: false},
		{name: "http localhost", url: "http://localhost:8080/api", wantErr: false},
		{name: "http 127.0.0.1", url: "http://127.0.0.1:9090", wantErr: false},
		{name: "http IPv6 localhost", url: "http://[::1]:8080/api", wantErr: false},
		{name: "http remote rejected", url: "http://ado.example.com/org", wantErr: true},
		{name: "http IP rejected", url: "http://10.0.0.1:8080/api", wantErr: true},
		{name: "unparseable URL", url: "://bad", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateURLScheme(tt.url)
			if tt.wantErr && err == nil {
				t.Errorf("validateURLScheme(%q) = nil, want error", tt.url)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("validateURLScheme(%q) = %v, want nil", tt.url, err)
			}
		})
	}
}

func TestWithBaseURL_RejectsHTTP(t *testing.T) {
	c := NewClient(NewSecretString("pat"), "org", "proj")
	_, err := c.WithBaseURL("http://ado.example.com/collection")
	if err == nil {
		t.Fatal("WithBaseURL should reject http:// for non-localhost")
	}
	if !strings.Contains(err.Error(), "HTTPS required") {
		t.Errorf("error = %q, want mention of HTTPS required", err.Error())
	}
}

func TestWithBaseURL_AllowsHTTPS(t *testing.T) {
	c := NewClient(NewSecretString("pat"), "org", "proj")
	got, err := c.WithBaseURL("https://tfs.example.com/collection")
	if err != nil {
		t.Fatalf("WithBaseURL should allow https://: %v", err)
	}
	if got.BaseURL != "https://tfs.example.com/collection" {
		t.Errorf("BaseURL = %q, want %q", got.BaseURL, "https://tfs.example.com/collection")
	}
}

func TestWithBaseURL_AllowsLocalhost(t *testing.T) {
	c := NewClient(NewSecretString("pat"), "org", "proj")
	got, err := c.WithBaseURL("http://localhost:8080")
	if err != nil {
		t.Fatalf("WithBaseURL should allow http://localhost: %v", err)
	}
	if got.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want %q", got.BaseURL, "http://localhost:8080")
	}
}
