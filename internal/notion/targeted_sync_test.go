package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// This file covers the #6004 behaviors: incremental pulls filtered
// server-side, targeted single fetches, and push resolution that never
// enumerates the whole data source. The servers here are real HTTP fakes so
// the tests can observe exactly which queries and page reads the tracker
// issues.

// serverPage is one page served by testNotionServer. The parent block is kept
// beside the page (rather than in Page) because the wire form of a parent is
// what membership checks read.
type serverPage struct {
	Page
	parent map[string]interface{}
	member bool // returned by data-source queries; false pages are GET-only
}

// memberParent is the parent block of a page that belongs to dataSourceID.
func memberParent(dataSourceID string) map[string]interface{} {
	return map[string]interface{}{
		"type":           "data_source_id",
		"data_source_id": dataSourceID,
		"database_id":    "db_123",
	}
}

// marshalWithParent encodes page with parent injected as its parent block.
func marshalWithParent(page Page, parent map[string]interface{}) ([]byte, error) {
	data, err := json.Marshal(page)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return data, nil
	}
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	body["parent"] = parent
	return json.Marshal(body)
}

// memberPage builds a live page of the server's data source.
func memberPage(id, beadsID, title string, edited time.Time) serverPage {
	return serverPage{
		Page: Page{
			Object:         "page",
			ID:             id,
			URL:            "https://www.notion.so/" + strings.ReplaceAll(id, "-", ""),
			CreatedTime:    edited.Add(-time.Hour),
			LastEditedTime: edited,
			Properties: map[string]PageProperty{
				PropertyTitle:    {Title: []RichText{{PlainText: title}}},
				PropertyBeadsID:  {RichText: []RichText{{PlainText: beadsID}}},
				PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
				PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
				PropertyType:     {Select: &SelectOption{Name: "Task"}},
			},
		},
		member: true,
	}
}

// testNotionServer is an httptest-backed Notion API. It applies the query
// filters the client actually sends, records every request, serves parents on
// direct reads, and can make unfiltered queries non-terminating — the shape a
// data source past the pagination bound has.
type testNotionServer struct {
	t *testing.T

	dataSourceID string

	mu             sync.Mutex
	pages          []serverPage
	queryFilters   []map[string]interface{} // one per /query request; nil = unfiltered
	gets           []string
	patches        []string
	posts          []string // Beads ID of each created page
	failUnfiltered bool
	failGetFor     map[string]int // page ID -> status served for its GET

	server *httptest.Server
}

func newTestNotionServer(t *testing.T, pages ...serverPage) *testNotionServer {
	t.Helper()
	s := &testNotionServer{
		t:            t,
		dataSourceID: "11111111-1111-1111-1111-111111111111",
		failGetFor:   map[string]int{},
	}
	for _, page := range pages {
		if page.member && page.parent == nil {
			page.parent = memberParent(s.dataSourceID)
		}
		s.pages = append(s.pages, page)
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.route))
	t.Cleanup(s.server.Close)
	return s
}

// tracker returns a Tracker on this server whose client bounds pagination at
// two pages, so an unfiltered query against failUnfiltered fails fast.
func (s *testNotionServer) tracker() *Tracker {
	client := NewClient("secret-token").
		WithBaseURL(s.server.URL).
		WithHTTPClient(s.server.Client()).
		WithMaxQueryPages(2)
	return &Tracker{client: client, config: DefaultMappingConfig(), dataSourceID: s.dataSourceID}
}

func (s *testNotionServer) route(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/query"):
		s.handleQuery(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/pages/"):
		s.handleGetPage(w, r, strings.TrimPrefix(r.URL.Path, "/pages/"))
	case r.Method == http.MethodPost && r.URL.Path == "/pages":
		s.handleCreatePage(w, r)
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/pages/"):
		s.handlePatchPage(w, r, strings.TrimPrefix(r.URL.Path, "/pages/"))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"object_not_found","message":"unexpected path `+r.URL.Path+`"}`)
	}
}

func (s *testNotionServer) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filter json.RawMessage `json:"filter"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	var filter map[string]interface{}
	if raw := strings.TrimSpace(string(req.Filter)); raw != "" && raw != "null" {
		_ = json.Unmarshal(req.Filter, &filter)
	}
	s.mu.Lock()
	s.queryFilters = append(s.queryFilters, filter)
	fail := s.failUnfiltered
	s.mu.Unlock()

	if filter == nil && fail {
		// Non-terminating pagination: the client's own bound fails the query.
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[],"has_more":true,"next_cursor":"c"}`)
		return
	}

	results := make([]json.RawMessage, 0)
	s.mu.Lock()
	for _, page := range s.pages {
		if page.member && pageMatchesFilter(page.Page, filter) {
			data, err := marshalWithParent(page.Page, page.parent)
			if err != nil {
				s.mu.Unlock()
				s.t.Fatalf("marshal query result: %v", err)
			}
			results = append(results, data)
		}
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"results": results, "has_more": false})
}

// pageMatchesFilter applies the two filters the tracker sends: the
// last_edited_time on_or_after filter of an incremental pull, and the Beads
// ID rich_text equality filter of a targeted lookup. A nil filter matches
// every page.
func pageMatchesFilter(page Page, filter map[string]interface{}) bool {
	if filter == nil {
		return true
	}
	if ts, _ := filter["timestamp"].(string); ts == "last_edited_time" {
		condition, _ := filter["last_edited_time"].(map[string]interface{})
		cutoffRaw, _ := condition["on_or_after"].(string)
		cutoff, err := time.Parse(time.RFC3339, cutoffRaw)
		if err != nil {
			return false
		}
		if page.LastEditedTime.Before(cutoff) {
			return false
		}
	}
	if property, _ := filter["property"].(string); property == PropertyBeadsID {
		condition, _ := filter["rich_text"].(map[string]interface{})
		want, _ := condition["equals"].(string)
		if DataSourceTitle(page.Properties[PropertyBeadsID].RichText) != want {
			return false
		}
	}
	return true
}

func (s *testNotionServer) handleGetPage(w http.ResponseWriter, r *http.Request, pageID string) {
	s.mu.Lock()
	s.gets = append(s.gets, pageID)
	if status, ok := s.failGetFor[pageID]; ok {
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, `{"code":"restricted_resource","message":"get %d for %s"}`, status, pageID)
		return
	}
	var found *serverPage
	for i := range s.pages {
		if s.pages[i].ID == pageID {
			found = &s.pages[i]
			break
		}
	}
	var data []byte
	var err error
	if found != nil {
		data, err = marshalWithParent(found.Page, found.parent)
	}
	s.mu.Unlock()

	if found == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"object_not_found","message":"Could not find page with ID: `+pageID+`"}`)
		return
	}
	if err != nil {
		s.t.Fatalf("marshal page %s: %v", pageID, err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *testNotionServer) handleCreatePage(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Properties map[string]PageProperty `json:"properties"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	s.mu.Lock()
	id := fmt.Sprintf("eeeeeeee-0000-0000-0000-%012d", len(s.pages)+1)
	now := time.Now().UTC()
	page := Page{
		Object:         "page",
		ID:             id,
		URL:            "https://www.notion.so/New-" + strings.ReplaceAll(id, "-", ""),
		CreatedTime:    now,
		LastEditedTime: now,
		Properties:     req.Properties,
	}
	s.pages = append(s.pages, serverPage{Page: page, parent: memberParent(s.dataSourceID), member: true})
	s.posts = append(s.posts, DataSourceTitle(req.Properties[PropertyBeadsID].RichText))
	data, err := marshalWithParent(page, memberParent(s.dataSourceID))
	s.mu.Unlock()
	if err != nil {
		s.t.Fatalf("marshal created page: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *testNotionServer) handlePatchPage(w http.ResponseWriter, r *http.Request, pageID string) {
	var req struct {
		Properties map[string]PageProperty `json:"properties"`
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &req)

	s.mu.Lock()
	s.patches = append(s.patches, pageID)
	var updated *serverPage
	for i := range s.pages {
		if s.pages[i].ID == pageID {
			s.pages[i].Properties = req.Properties
			s.pages[i].LastEditedTime = time.Now().UTC()
			updated = &s.pages[i]
			break
		}
	}
	var data []byte
	var err error
	if updated != nil {
		data, err = marshalWithParent(updated.Page, updated.parent)
	}
	s.mu.Unlock()

	if updated == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"object_not_found","message":"Could not find page with ID: `+pageID+`"}`)
		return
	}
	if err != nil {
		s.t.Fatalf("marshal patched page: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (s *testNotionServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queryFilters) + len(s.gets) + len(s.posts) + len(s.patches)
}

func (s *testNotionServer) unfilteredQueries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, filter := range s.queryFilters {
		if filter == nil {
			count++
		}
	}
	return count
}

// TestTrackerIncrementalFetchDoesNotEnumerateWholeDataSource reproduces #6004:
// an incremental pull (Since set) must narrow the Notion query server-side with a
// last_edited_time filter, so a data source larger than the default pagination
// bound (50 pages x 100 rows) still syncs when only a few rows changed.
func TestTrackerIncrementalFetchDoesNotEnumerateWholeDataSource(t *testing.T) {
	t.Setenv("NOTION_MAX_QUERY_PAGES", "")
	since := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	recentPage := func(n int) Page {
		return Page{
			Object:         "page",
			ID:             fmt.Sprintf("aaaaaaaa-0000-0000-0000-%012d", n),
			URL:            fmt.Sprintf("https://www.notion.so/Task-aaaaaaaa00000000000%013d", n),
			CreatedTime:    since.Add(-time.Hour),
			LastEditedTime: since.Add(time.Duration(n) * time.Minute),
			Properties: map[string]PageProperty{
				PropertyTitle:    {Title: []RichText{{PlainText: fmt.Sprintf("Recent %d", n)}}},
				PropertyBeadsID:  {RichText: []RichText{{PlainText: fmt.Sprintf("bd-r%d", n)}}},
				PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
				PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
				PropertyType:     {Select: &SelectOption{Name: "Task"}},
			},
		}
	}

	var unfiltered, filtered int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			StartCursor string                 `json:"start_cursor"`
			Filter      map[string]interface{} `json:"filter"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if ts, _ := req.Filter["timestamp"].(string); ts == "last_edited_time" {
			filtered++
			_ = json.NewEncoder(w).Encode(QueryDataSourceResponse{Results: []Page{recentPage(1), recentPage(2)}})
			return
		}
		// Unfiltered: a 6000-row data source, 100 old rows per page.
		unfiltered++
		page := 0
		_, _ = fmt.Sscanf(req.StartCursor, "c%d", &page)
		results := make([]Page, 0, 100)
		for i := 0; i < 100; i++ {
			n := page*100 + i
			old := recentPage(100000 + n)
			old.LastEditedTime = since.Add(-time.Duration(n+1) * time.Hour)
			results = append(results, old)
		}
		_ = json.NewEncoder(w).Encode(QueryDataSourceResponse{
			Results:    results,
			HasMore:    page+1 < 60,
			NextCursor: fmt.Sprintf("c%d", page+1),
		})
	}))
	defer server.Close()

	client := NewClient("secret-token").WithBaseURL(server.URL).WithHTTPClient(server.Client())
	tracker := &Tracker{client: client, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	issues, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{Since: &since})
	if err != nil {
		t.Fatalf("incremental FetchIssues on a 6000-row data source failed (unfiltered queries=%d, filtered queries=%d): %v",
			unfiltered, filtered, err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %d, want the 2 rows edited since %s", len(issues), since)
	}
	if filtered == 0 {
		t.Fatalf("no query carried a last_edited_time filter; unfiltered queries = %d", unfiltered)
	}
	if unfiltered != 0 {
		t.Fatalf("incremental fetch issued %d unfiltered queries; the window query must be the only one", unfiltered)
	}
}

// TestTrackerFullFetchAfterTargetedOperations pins AC-2's first clause:
// after targeted writes and an incremental pull, a cursor-less fetch must
// still enumerate the whole data source. The whole-source index it used to
// consult was exactly what a single write could leave partial.
func TestTrackerFullFetchAfterTargetedOperations(t *testing.T) {
	edited := time.Now().UTC().Add(-2 * time.Hour)
	server := newTestNotionServer(t,
		memberPage("aaaaaaaa-0000-0000-0000-000000000001", "bd-a", "Page A", edited),
		memberPage("aaaaaaaa-0000-0000-0000-000000000002", "bd-b", "Page B", edited),
		memberPage("aaaaaaaa-0000-0000-0000-000000000003", "bd-c", "Page C", edited),
	)
	tracker := server.tracker()

	if _, err := tracker.CreateIssue(context.Background(), &types.Issue{
		ID:        "bd-new",
		Title:     "Created",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	}); err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if _, err := tracker.UpdateIssue(context.Background(),
		"https://www.notion.so/aaaaaaaa000000000000000000000001",
		&types.Issue{ID: "bd-a", Title: "Page A, edited", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
	); err != nil {
		t.Fatalf("UpdateIssue returned error: %v", err)
	}
	since := time.Now().UTC().Add(-5 * time.Minute)
	if _, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{Since: &since}); err != nil {
		t.Fatalf("incremental FetchIssues returned error: %v", err)
	}

	issues, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{})
	if err != nil {
		t.Fatalf("full FetchIssues returned error: %v", err)
	}
	got := map[string]bool{}
	for _, issue := range issues {
		got[issue.Title] = true
	}
	for _, want := range []string{"Page A, edited", "Page B", "Page C", "Created"} {
		if !got[want] {
			t.Fatalf("full fetch after targeted writes missed %q; got %v", want, got)
		}
	}
	queried, candidates := tracker.LastPullStats()
	if queried != 4 || candidates != 4 {
		t.Fatalf("LastPullStats = (%d, %d), want (4, 4) — the live rows the query returned", queried, candidates)
	}
}

// TestTrackerFullFetchBeyondBoundReturnsErrorNotPartialRows pins AC-2's
// second clause: a cursor-less fetch over a source larger than the configured
// bound fails outright; partial rows are not a success.
func TestTrackerFullFetchBeyondBoundReturnsErrorNotPartialRows(t *testing.T) {
	edited := time.Now().UTC().Add(-2 * time.Hour)
	server := newTestNotionServer(t,
		memberPage("aaaaaaaa-0000-0000-0000-000000000001", "bd-a", "Page A", edited),
		memberPage("aaaaaaaa-0000-0000-0000-000000000002", "bd-b", "Page B", edited),
	)
	server.failUnfiltered = true
	tracker := server.tracker()

	issues, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{})
	if err == nil {
		t.Fatalf("full fetch beyond the bound succeeded with %d issues, want the pagination-bound error", len(issues))
	}
	if issues != nil {
		t.Fatalf("full fetch beyond the bound returned %d rows alongside its error; partial success is not a success", len(issues))
	}
}

// TestTrackerFetchIssueTargetedBeyondQueryBound pins AC-3: a single fetch
// resolves page references by direct reads and Beads IDs by a source-scoped
// equality query, without enumerating a data source larger than the bound.
func TestTrackerFetchIssueTargetedBeyondQueryBound(t *testing.T) {
	edited := time.Now().UTC().Add(-2 * time.Hour)
	server := newTestNotionServer(t,
		memberPage("aaaaaaaa-0000-0000-0000-000000000001", "bd-a", "Page A", edited),
		memberPage("aaaaaaaa-0000-0000-0000-000000000002", "bd-b", "Page B", edited),
	)
	server.failUnfiltered = true
	tracker := server.tracker()

	for _, ref := range []string{
		"https://www.notion.so/Page-A-aaaaaaaa000000000000000000000001",
		"aaaaaaaa000000000000000000000001",
		"aaaaaaaa-0000-0000-0000-000000000001",
	} {
		issue, err := tracker.FetchIssue(context.Background(), ref)
		if err != nil {
			t.Fatalf("FetchIssue(%q) returned error: %v", ref, err)
		}
		if issue == nil || issue.Title != "Page A" {
			t.Fatalf("FetchIssue(%q) = %+v, want Page A", ref, issue)
		}
	}

	issue, err := tracker.FetchIssue(context.Background(), "bd-b")
	if err != nil {
		t.Fatalf("FetchIssue(bd-b) returned error: %v", err)
	}
	if issue == nil || issue.Title != "Page B" {
		t.Fatalf("FetchIssue(bd-b) = %+v, want Page B", issue)
	}

	missing, err := tracker.FetchIssue(context.Background(), "bbbbbbbb-0000-0000-0000-000000000009")
	if err != nil {
		t.Fatalf("FetchIssue(missing page) returned error: %v", err)
	}
	if missing != nil {
		t.Fatalf("FetchIssue(missing page) = %+v, want (nil, nil)", missing)
	}

	before := server.requestCount()
	issue, err = tracker.FetchIssue(context.Background(), "   ")
	if err != nil || issue != nil {
		t.Fatalf("FetchIssue(blank) = (%v, %v), want (nil, nil)", issue, err)
	}
	if after := server.requestCount(); after != before {
		t.Fatalf("a blank identifier issued %d requests, want none", after-before)
	}
	if server.unfilteredQueries() != 0 {
		t.Fatalf("FetchIssue issued %d unfiltered queries, want none", server.unfilteredQueries())
	}
}

func titles(issues []itracker.TrackerIssue) []string {
	result := make([]string, 0, len(issues))
	for _, issue := range issues {
		result = append(result, issue.Title)
	}
	return result
}
