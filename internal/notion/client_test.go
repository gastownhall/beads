package notion

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientRetrieveDataSourceSetsHeaders(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/data_sources/ds_123" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := r.Header.Get("Notion-Version"); got != DefaultNotionVersion {
			t.Fatalf("notion version = %q", got)
		}
		_, _ = io.WriteString(w, `{"id":"ds_123","url":"https://www.notion.so/source","title":[{"plain_text":"Tasks"}],"properties":{"Name":{"type":"title"}}}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	ds, err := client.RetrieveDataSource(context.Background(), "ds_123")
	if err != nil {
		t.Fatalf("RetrieveDataSource returned error: %v", err)
	}
	if ds.ID != "ds_123" {
		t.Fatalf("id = %q", ds.ID)
	}
	if DataSourceTitle(ds.Title) != "Tasks" {
		t.Fatalf("title = %q", DataSourceTitle(ds.Title))
	}
}

func TestClientQueryDataSourcePaginates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		switch r.Header.Get("X-Test-Step") {
		default:
		}
		if r.URL.Path != "/data_sources/ds_123/query" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			t.Fatalf("content type = %q", r.Header.Get("Content-Type"))
		}
		if !strings.Contains(r.URL.RawQuery, "") {
		}
		if strings.Contains(r.Header.Get("X-Page"), "2") {
		}
	}))
	defer server.Close()

	call := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/data_sources/ds_123/query" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if call == 1 {
			if !strings.Contains(string(body), `"page_size":100`) {
				t.Fatalf("request body = %s", body)
			}
			_, _ = io.WriteString(w, `{"results":[{"id":"page-1"},{"id":"page-2"}],"has_more":true,"next_cursor":"cursor-2"}`)
			return
		}
		if !strings.Contains(string(body), `"start_cursor":"cursor-2"`) {
			t.Fatalf("request body = %s", body)
		}
		_, _ = io.WriteString(w, `{"results":[{"id":"page-3"}],"has_more":false}`)
	})

	client := NewClient("secret-token").WithBaseURL(server.URL)
	pages, err := client.QueryDataSource(context.Background(), "ds_123")
	if err != nil {
		t.Fatalf("QueryDataSource returned error: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("pages = %d, want 3", len(pages))
	}
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"code":"unauthorized","message":"token is invalid"}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	_, err := client.GetCurrentUser(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "token is invalid") {
		t.Fatalf("error = %q", err)
	}
}

func TestClientCreateDatabaseSendsInitialDataSource(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/databases" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		for _, want := range []string{
			`"page_id":"329e5bf9-7fae-8080-bb4a-d94e1387655d"`,
			`"initial_data_source"`,
			`"Beads ID"`,
			`"Status"`,
			`"Type"`,
		} {
			if !strings.Contains(string(body), want) {
				t.Fatalf("request body missing %q\n%s", want, body)
			}
		}
		_, _ = io.WriteString(w, `{"id":"db_123","url":"https://www.notion.so/db123","data_sources":[{"id":"ds_123","name":"Beads Issues"}]}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	db, err := client.CreateDatabase(context.Background(), "329e5bf9-7fae-8080-bb4a-d94e1387655d", DefaultDatabaseTitle)
	if err != nil {
		t.Fatalf("CreateDatabase returned error: %v", err)
	}
	if db.ID != "db_123" {
		t.Fatalf("id = %q", db.ID)
	}
	if len(db.DataSources) != 1 || db.DataSources[0].ID != "ds_123" {
		t.Fatalf("data_sources = %+v", db.DataSources)
	}
}

func TestClientRetrieveDatabase(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/databases/db_123" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"db_123","url":"https://www.notion.so/db123","data_sources":[{"id":"ds_123","name":"Beads Issues"}]}`)
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	db, err := client.RetrieveDatabase(context.Background(), "db_123")
	if err != nil {
		t.Fatalf("RetrieveDatabase returned error: %v", err)
	}
	if db.ID != "db_123" {
		t.Fatalf("id = %q", db.ID)
	}
	if len(db.DataSources) != 1 || db.DataSources[0].ID != "ds_123" {
		t.Fatalf("data_sources = %+v", db.DataSources)
	}
}

func TestResolveDataSourceReferencePrefersDataSource(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data_sources/329e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"329e5bf9-7fae-8080-bb4a-d94e1387655d","properties":{"Name":{"type":"title"}}}`)
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	resolved, err := ResolveDataSourceReference(context.Background(), client, "https://www.notion.so/workspace/329e5bf97fae8080bb4ad94e1387655d")
	if err != nil {
		t.Fatalf("ResolveDataSourceReference returned error: %v", err)
	}
	if resolved.DataSourceID != "329e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("data_source_id = %q", resolved.DataSourceID)
	}
}

func TestResolveDataSourceReferenceFallsBackToDatabase(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/data_sources/429e5bf9-7fae-8080-bb4a-d94e1387655d":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"object_not_found","message":"not found"}`)
		case "/databases/429e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"429e5bf9-7fae-8080-bb4a-d94e1387655d","data_sources":[{"id":"529e5bf9-7fae-8080-bb4a-d94e1387655d","name":"Beads Issues"}]}`)
		case "/data_sources/529e5bf9-7fae-8080-bb4a-d94e1387655d":
			_, _ = io.WriteString(w, `{"id":"529e5bf9-7fae-8080-bb4a-d94e1387655d","properties":{"Name":{"type":"title"}}}`)
		default:
			t.Fatalf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL)
	resolved, err := ResolveDataSourceReference(context.Background(), client, "https://www.notion.so/workspace/429e5bf97fae8080bb4ad94e1387655d")
	if err != nil {
		t.Fatalf("ResolveDataSourceReference returned error: %v", err)
	}
	if resolved.DataSourceID != "529e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("data_source_id = %q", resolved.DataSourceID)
	}
	if resolved.Database == nil || resolved.Database.ID != "429e5bf9-7fae-8080-bb4a-d94e1387655d" {
		t.Fatalf("database = %+v", resolved.Database)
	}
}
