package targetprocess

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientFetchEntities_UsesAccessTokenAndPaging(t *testing.T) {
	t.Parallel()

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		if got := r.URL.Query().Get("access_token"); got != "access-token" {
			t.Fatalf("expected access_token query parameter, got %q", got)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Fatalf("expected format=json, got %q", got)
		}

		skip := r.URL.Query().Get("skip")
		response := collectionResponse[Assignable]{
			Items: []Assignable{
				{ID: 101, Name: "First"},
			},
		}
		if skip == "1" {
			response.Items = []Assignable{}
		}

		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(server.URL, "access-token", "", "", "")
	since := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	items, err := client.FetchEntities(context.Background(), "UserStories", 42, "open", &since, "Owner.Id eq 7", 1)
	if err != nil {
		t.Fatalf("FetchEntities returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != 101 {
		t.Fatalf("unexpected items: %#v", items)
	}
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}

	query := requests[0]
	for _, want := range []string{
		"take=1",
		"skip=0",
		"Project.Id+eq+42",
		"EntityState.IsFinal+eq+%27false%27",
		"ModifyDate+gte+%272026-03-01T12%3A00%3A00%27",
		"Owner.Id+eq+7",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("expected query %q to contain %q", query, want)
		}
	}
}

func TestClientFetchEntitiesV2_UsesV2QueryShape(t *testing.T) {
	t.Parallel()

	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.String())
		if r.URL.Path != "/api/v2/UserStory" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("access_token"); got != "access-token" {
			t.Fatalf("expected access_token query parameter, got %q", got)
		}
		if got := r.URL.Query().Get("select"); got == "" {
			t.Fatal("expected select query parameter")
		}
		if got := r.URL.Query().Get("orderBy"); got != "modifyDate desc" {
			t.Fatalf("expected orderBy query parameter, got %q", got)
		}

		response := v2CollectionResponse[v2Assignable]{
			Items: []v2Assignable{
				{
					ID:          201,
					Name:        "From v2",
					Description: "Pulled through v2",
					EntityType:  &v2EntityRef{Name: "UserStory"},
					EntityState: &v2EntityState{Name: "Open", IsFinal: false},
					Tags:        []string{"api", "sync"},
				},
			},
		}

		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client := NewClient(server.URL, "access-token", "", "", "")
	since := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	items, err := client.FetchEntitiesV2(context.Background(), "UserStory", 42, "open", &since, "owner.id=7", 1)
	if err != nil {
		t.Fatalf("FetchEntitiesV2 returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != 201 || items[0].Tags != "api, sync" {
		t.Fatalf("unexpected items: %#v", items)
	}
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}

	query := requests[0]
	for _, want := range []string{
		"/api/v2/UserStory?",
		"project.id%3D42",
		"entityState.isFinal%3Dfalse",
		"modifyDate%3E%3D%272026-03-01T12%3A00%3A00%27",
		"owner.id%3D7",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("expected query %q to contain %q", query, want)
		}
	}
}

func TestClientCreateIssue_UsesBasicAuth(t *testing.T) {
	t.Parallel()

	var (
		gotAuth string
		gotBody map[string]interface{}
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/Bugs" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}

		_ = json.NewEncoder(w).Encode(Assignable{
			ID:           77,
			Name:         "Created Bug",
			ResourceType: "Bug",
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "", "", "alice", "secret")
	issue, err := client.CreateIssue(context.Background(), "Bug", map[string]interface{}{
		"Name": "Created Bug",
	})
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if issue == nil || issue.ID != 77 {
		t.Fatalf("unexpected issue: %#v", issue)
	}

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	if gotAuth != wantAuth {
		t.Fatalf("expected auth %q, got %q", wantAuth, gotAuth)
	}
	if gotBody["Name"] != "Created Bug" {
		t.Fatalf("unexpected body: %#v", gotBody)
	}
}
