package notion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// testDataSourceID is the data source the fake Notion serves as the tracker's
// configured target.
const testDataSourceID = "0f0e0d0c-0b0a-4908-8706-050403020100"

// fakeNotion is an httptest Notion API. It applies the query filters the
// tracker sends, serves direct page reads, scopes queries to the data source
// in the request path, and records every request, so a test can tell a
// targeted lookup from a whole-source enumeration by what reached the server.
type fakeNotion struct {
	t            *testing.T
	server       *httptest.Server
	dataSourceID string

	mu    sync.Mutex
	pages []fakePage
	// hugeSource makes every unfiltered query answer with another page of rows,
	// so enumerating the whole source always runs into the client's bound.
	hugeSource bool
	// chunk is how many matching rows one filtered response carries; zero
	// returns them all in one response.
	chunk int
	// getStatus fails direct reads of a page (keyed by compact page ID) with
	// the given status.
	getStatus map[string]int
	// filteredQueryStatus, when nonzero, fails every filtered query with it.
	filteredQueryStatus int
	nextID              int

	unfilteredQueries int
	filters           []map[string]interface{}
	gets              []string
	created           []string
	updated           []string
}

// fakePage is a page as the fake serves it. Parent is carried here rather than
// on Page so the fixtures do not depend on how the client decodes it.
type fakePage struct {
	Page
	Parent map[string]interface{} `json:"parent,omitempty"`
}

func newFakeNotion(t *testing.T) *fakeNotion {
	t.Helper()
	f := &fakeNotion{t: t, dataSourceID: testDataSourceID}
	f.server = httptest.NewServer(f)
	t.Cleanup(f.server.Close)
	return f
}

// tracker returns a tracker on the fake whose client stops whole-source
// enumeration after three response pages.
func (f *fakeNotion) tracker() *Tracker {
	return f.trackerFor(f.dataSourceID)
}

func (f *fakeNotion) trackerFor(dataSourceID string) *Tracker {
	client := NewClient("secret-token").
		WithBaseURL(f.server.URL).
		WithHTTPClient(f.server.Client()).
		WithMaxQueryPages(3)
	return &Tracker{client: client, config: DefaultMappingConfig(), dataSourceID: dataSourceID}
}

func (f *fakeNotion) addPages(pages ...fakePage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages = append(f.pages, pages...)
}

func (f *fakeNotion) setPage(page fakePage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.pages {
		if compactID(f.pages[i].ID) == compactID(page.ID) {
			f.pages[i] = page
			return
		}
	}
	f.pages = append(f.pages, page)
}

type fakeNotionRequests struct {
	unfilteredQueries int
	filters           []map[string]interface{}
	gets              []string
	created           []string
	updated           []string
}

func (f *fakeNotion) requests() fakeNotionRequests {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeNotionRequests{
		unfilteredQueries: f.unfilteredQueries,
		filters:           append([]map[string]interface{}(nil), f.filters...),
		gets:              append([]string(nil), f.gets...),
		created:           append([]string(nil), f.created...),
		updated:           append([]string(nil), f.updated...),
	}
}

func (f *fakeNotion) resetRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unfilteredQueries = 0
	f.filters = nil
	f.gets = nil
	f.created = nil
	f.updated = nil
}

func compactID(id string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
}

func sourceParent(dataSourceID string) map[string]interface{} {
	return map[string]interface{}{
		"type":           "data_source_id",
		"data_source_id": dataSourceID,
		"database_id":    "d0d0d0d0-d0d0-4d0d-8d0d-d0d0d0d0d0d0",
	}
}

// fakeRow is a live open task in the configured data source.
func fakeRow(n int, beadsID string, edited time.Time) fakePage {
	id := fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", n)
	return fakePage{
		Page: Page{
			Object:         "page",
			ID:             id,
			URL:            "https://www.notion.so/Task-" + compactID(id),
			CreatedTime:    edited.Add(-time.Hour),
			LastEditedTime: edited,
			Properties: map[string]PageProperty{
				PropertyTitle:    {Title: []RichText{{PlainText: "Task " + beadsID}}},
				PropertyBeadsID:  {RichText: []RichText{{PlainText: beadsID}}},
				PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
				PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
				PropertyType:     {Select: &SelectOption{Name: "Task"}},
			},
		},
		Parent: sourceParent(testDataSourceID),
	}
}

func (f *fakeNotion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path, err := url.PathUnescape(r.URL.EscapedPath())
	if err != nil {
		f.t.Errorf("unescape path %q: %v", r.URL.EscapedPath(), err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/data_sources/") && strings.HasSuffix(path, "/query"):
		sourceID := strings.TrimSuffix(strings.TrimPrefix(path, "/data_sources/"), "/query")
		f.serveQuery(w, r, sourceID)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/pages/"):
		f.serveGet(w, strings.TrimPrefix(path, "/pages/"))
	case r.Method == http.MethodPost && path == "/pages":
		f.serveCreate(w, r)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/pages/"):
		f.serveUpdate(w, r, strings.TrimPrefix(path, "/pages/"))
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, path)
		writeFakeError(w, http.StatusNotFound, "invalid_request_url", "unexpected request")
	}
}

func writeFakeError(w http.ResponseWriter, status int, code, message string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "error", "status": status, "code": code, "message": message,
	})
}

func (f *fakeNotion) serveQuery(w http.ResponseWriter, r *http.Request, sourceID string) {
	var req struct {
		StartCursor string                 `json:"start_cursor"`
		Filter      map[string]interface{} `json:"filter"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode query body: %v", err)
	}
	if compactID(sourceID) != compactID(f.dataSourceID) {
		writeFakeError(w, http.StatusNotFound, "object_not_found", "Could not find data source")
		return
	}
	if req.Filter == nil {
		f.unfilteredQueries++
		if f.hugeSource {
			filler := fakeRow(900000+f.unfilteredQueries, fmt.Sprintf("bd-filler-%d", f.unfilteredQueries), time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"results": []fakePage{filler}, "has_more": true, "next_cursor": "more",
			})
			return
		}
	} else {
		f.filters = append(f.filters, req.Filter)
		if f.filteredQueryStatus != 0 {
			writeFakeError(w, f.filteredQueryStatus, "restricted_resource", "query refused")
			return
		}
	}

	var matches []fakePage
	for _, page := range f.pages {
		if compactID(fmt.Sprint(page.Parent["data_source_id"])) != compactID(f.dataSourceID) {
			continue
		}
		if req.Filter != nil && !f.matches(page, req.Filter) {
			continue
		}
		matches = append(matches, page)
	}

	offset := 0
	if req.StartCursor != "" {
		offset, _ = strconv.Atoi(strings.TrimPrefix(req.StartCursor, "o"))
	}
	end := len(matches)
	if f.chunk > 0 && offset+f.chunk < end {
		end = offset + f.chunk
	}
	resp := map[string]interface{}{"results": matches[offset:end], "has_more": end < len(matches)}
	if end < len(matches) {
		resp["next_cursor"] = fmt.Sprintf("o%d", end)
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// matches applies the two filter shapes Notion sync may send. Anything else is
// a test failure, so a filter the fake does not understand cannot pass as a
// match-all.
func (f *fakeNotion) matches(page fakePage, filter map[string]interface{}) bool {
	if timestamp, _ := filter["timestamp"].(string); timestamp != "" {
		cond, _ := filter[timestamp].(map[string]interface{})
		raw, _ := cond["on_or_after"].(string)
		cutoff, err := time.Parse(time.RFC3339, raw)
		if timestamp != "last_edited_time" || len(filter) != 2 || len(cond) != 1 || err != nil {
			f.t.Errorf("unsupported timestamp filter %v", filter)
			return false
		}
		return !page.LastEditedTime.Before(cutoff)
	}
	if property, _ := filter["property"].(string); property != "" {
		cond, _ := filter["rich_text"].(map[string]interface{})
		want, ok := cond["equals"].(string)
		if property != PropertyBeadsID || len(filter) != 2 || len(cond) != 1 || !ok {
			f.t.Errorf("unsupported property filter %v", filter)
			return false
		}
		return DataSourceTitle(page.Properties[PropertyBeadsID].RichText) == want
	}
	f.t.Errorf("unsupported filter %v", filter)
	return false
}

func (f *fakeNotion) serveGet(w http.ResponseWriter, pageID string) {
	f.gets = append(f.gets, compactID(pageID))
	if status := f.getStatus[compactID(pageID)]; status != 0 {
		writeFakeError(w, status, "restricted_resource", "read refused")
		return
	}
	for _, page := range f.pages {
		if compactID(page.ID) == compactID(pageID) {
			_ = json.NewEncoder(w).Encode(page)
			return
		}
	}
	writeFakeError(w, http.StatusNotFound, "object_not_found", "Could not find page with ID: "+pageID)
}

func (f *fakeNotion) serveCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parent     map[string]interface{}  `json:"parent"`
		Properties map[string]PageProperty `json:"properties"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode create body: %v", err)
	}
	if compactID(fmt.Sprint(req.Parent["data_source_id"])) != compactID(f.dataSourceID) {
		f.t.Errorf("create targeted parent %v, want data source %s", req.Parent, f.dataSourceID)
	}
	f.nextID++
	page := fakeRow(500000+f.nextID, "", time.Now().UTC().Truncate(time.Minute))
	page.Properties = req.Properties
	f.pages = append(f.pages, page)
	f.created = append(f.created, DataSourceTitle(req.Properties[PropertyBeadsID].RichText))
	_ = json.NewEncoder(w).Encode(page)
}

func (f *fakeNotion) serveUpdate(w http.ResponseWriter, r *http.Request, pageID string) {
	var req struct {
		Properties map[string]PageProperty `json:"properties"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decode update body: %v", err)
	}
	f.updated = append(f.updated, compactID(pageID))
	for i := range f.pages {
		if compactID(f.pages[i].ID) == compactID(pageID) {
			f.pages[i].Properties = req.Properties
			f.pages[i].LastEditedTime = time.Now().UTC().Truncate(time.Minute)
			_ = json.NewEncoder(w).Encode(f.pages[i])
			return
		}
	}
	writeFakeError(w, http.StatusNotFound, "object_not_found", "Could not find page with ID: "+pageID)
}

func beadsIDs(issues []itracker.TrackerIssue) []string {
	ids := make([]string, 0, len(issues))
	for _, issue := range issues {
		raw, _ := issue.Raw.(*PulledIssue)
		if raw == nil {
			ids = append(ids, "<no raw>")
			continue
		}
		ids = append(ids, raw.ID)
	}
	sort.Strings(ids)
	return ids
}

// An incremental pull narrows the query by last_edited_time, so it no longer
// rediscovers an old page that carries a Beads ID bd has never seen. A
// cursor-less pull still enumerates it, and an explicit fetch reaches it at any
// age; those are the recovery paths.
func TestTrackerIncrementalPullDoesNotDiscoverOldUnlinkedPages(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 11, 1, 12, 0, 30, 0, time.UTC)
	f := newFakeNotion(t)
	old := fakeRow(1, "bd-old", since.Add(-48*time.Hour))
	recent := fakeRow(2, "bd-recent", since.Add(time.Hour))
	f.addPages(old, recent)
	ctx := context.Background()

	incremental, err := f.tracker().FetchIssues(ctx, itracker.FetchOptions{Since: &since})
	if err != nil {
		t.Fatalf("incremental FetchIssues: %v", err)
	}
	if got := strings.Join(beadsIDs(incremental), ","); got != "bd-recent" {
		t.Fatalf("incremental pull returned %q, want only bd-recent", got)
	}

	full, err := f.tracker().FetchIssues(ctx, itracker.FetchOptions{})
	if err != nil {
		t.Fatalf("full FetchIssues: %v", err)
	}
	if got := strings.Join(beadsIDs(full), ","); got != "bd-old,bd-recent" {
		t.Fatalf("full pull returned %q, want bd-old,bd-recent", got)
	}

	fetched, err := f.tracker().FetchIssue(ctx, old.URL)
	if err != nil {
		t.Fatalf("FetchIssue(%s): %v", old.URL, err)
	}
	if fetched == nil || fetched.ID != old.ID {
		t.Fatalf("FetchIssue(%s) = %+v, want page %s", old.URL, fetched, old.ID)
	}
}

// A cursor-less pull always runs its own unfiltered query. A targeted read, an
// incremental pull or a write on the same tracker must not leave behind a
// partial result that a later full pull reports as the whole source.
func TestTrackerFullFetchAfterTargetedOperations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	edited := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	t.Run("returns every live row", func(t *testing.T) {
		t.Parallel()
		f := newFakeNotion(t)
		open := fakeRow(1, "bd-1", edited)
		closed := fakeRow(2, "bd-2", edited)
		closed.Properties[PropertyStatus] = PageProperty{Select: &SelectOption{Name: "Closed"}}
		archived := fakeRow(3, "bd-3", edited)
		archived.Archived = true
		f.addPages(open, closed, archived)
		tr := f.tracker()

		created, err := tr.CreateIssue(ctx, &types.Issue{ID: "bd-new", Title: "New", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask})
		if err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		if _, err := tr.UpdateIssue(ctx, open.URL, &types.Issue{ID: "bd-1", Title: "Edited", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}); err != nil {
			t.Fatalf("UpdateIssue: %v", err)
		}
		if _, err := tr.FetchIssue(ctx, created.URL); err != nil {
			t.Fatalf("FetchIssue: %v", err)
		}
		since := time.Now().UTC().Add(-time.Hour)
		if _, err := tr.FetchIssues(ctx, itracker.FetchOptions{Since: &since}); err != nil {
			t.Fatalf("incremental FetchIssues: %v", err)
		}

		f.resetRequests()
		all, err := tr.FetchIssues(ctx, itracker.FetchOptions{})
		if err != nil {
			t.Fatalf("full FetchIssues: %v", err)
		}
		if got := strings.Join(beadsIDs(all), ","); got != "bd-1,bd-2,bd-new" {
			t.Fatalf("full pull returned %q, want bd-1,bd-2,bd-new", got)
		}
		if reqs := f.requests(); reqs.unfilteredQueries == 0 || len(reqs.filters) != 0 {
			t.Fatalf("full pull sent %d unfiltered and %d filtered queries, want an unfiltered query only",
				reqs.unfilteredQueries, len(reqs.filters))
		}
		if queried, candidates := tr.LastPullStats(); queried != 3 || candidates != 3 {
			t.Fatalf("LastPullStats = (%d, %d), want (3, 3)", queried, candidates)
		}

		openOnly, err := tr.FetchIssues(ctx, itracker.FetchOptions{State: "open", Limit: 1})
		if err != nil {
			t.Fatalf("full open FetchIssues: %v", err)
		}
		if len(openOnly) != 1 || beadsIDs(openOnly)[0] == "bd-2" {
			t.Fatalf("open pull with limit 1 returned %v", beadsIDs(openOnly))
		}
		if queried, candidates := tr.LastPullStats(); queried != 3 || candidates != 1 {
			t.Fatalf("LastPullStats = (%d, %d), want (3, 1)", queried, candidates)
		}
	})

	t.Run("fails beyond the bound without partial rows", func(t *testing.T) {
		t.Parallel()
		f := newFakeNotion(t)
		f.hugeSource = true
		tr := f.tracker()

		if _, err := tr.CreateIssue(ctx, &types.Issue{ID: "bd-new", Title: "New", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}); err != nil {
			t.Fatalf("CreateIssue: %v", err)
		}
		issues, err := tr.FetchIssues(ctx, itracker.FetchOptions{})
		if err == nil {
			t.Fatalf("full FetchIssues beyond the bound returned %v, want the bound error", beadsIDs(issues))
		}
		if issues != nil {
			t.Fatalf("full FetchIssues returned partial rows %v with its error", beadsIDs(issues))
		}
		if !strings.Contains(err.Error(), "notion.max_query_pages") || !strings.Contains(err.Error(), "NOTION_MAX_QUERY_PAGES") {
			t.Fatalf("bound error should name both levers, got: %v", err)
		}
	})
}

func wantSinceFilter(cutoff string) string {
	return `{"last_edited_time":{"on_or_after":"` + cutoff + `"},"timestamp":"last_edited_time"}`
}

func filterJSON(t *testing.T, filter map[string]interface{}) string {
	t.Helper()
	encoded, err := json.Marshal(filter)
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	return string(encoded)
}

// #6004: a source too large to enumerate within the bound still pulls
// incrementally, because the edited window is filtered server-side on every
// page of the query rather than after enumerating everything.
func TestTrackerIncrementalBeyondQueryBound(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f := newFakeNotion(t)
	f.hugeSource = true
	f.chunk = 1
	f.addPages(fakeRow(1, "bd-r1", since.Add(time.Minute)), fakeRow(2, "bd-r2", since.Add(2*time.Minute)))

	issues, err := f.tracker().FetchIssues(context.Background(), itracker.FetchOptions{Since: &since})
	if err != nil {
		t.Fatalf("incremental FetchIssues on a source beyond the bound: %v", err)
	}
	if got := strings.Join(beadsIDs(issues), ","); got != "bd-r1,bd-r2" {
		t.Fatalf("incremental pull returned %q, want bd-r1,bd-r2", got)
	}
	reqs := f.requests()
	if reqs.unfilteredQueries != 0 {
		t.Fatalf("incremental pull sent %d unfiltered queries, want none", reqs.unfilteredQueries)
	}
	if len(reqs.filters) != 2 {
		t.Fatalf("filtered requests = %d, want 2 (one per response page)", len(reqs.filters))
	}
	for i, filter := range reqs.filters {
		if got, want := filterJSON(t, filter), wantSinceFilter("2026-11-01T12:00:00Z"); got != want {
			t.Fatalf("request %d filter = %s, want %s", i, got, want)
		}
	}
}

// The server-side cutoff is Since in UTC truncated to the minute, the same
// boundary matchesFetchSince applies, and state, archived/trashed exclusion and
// limit still select from the rows that query returned.
func TestTrackerIncrementalBoundary(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 11, 1, 7, 4, 37, 0, time.FixedZone("UTC-5", -5*60*60))
	cutoff := time.Date(2026, 11, 1, 12, 4, 0, 0, time.UTC)
	f := newFakeNotion(t)
	f.hugeSource = true
	closed := fakeRow(4, "bd-closed", cutoff.Add(2*time.Minute))
	closed.Properties[PropertyStatus] = PageProperty{Select: &SelectOption{Name: "Closed"}}
	archived := fakeRow(5, "bd-archived", cutoff.Add(2*time.Minute))
	archived.Archived = true
	trashed := fakeRow(6, "bd-trashed", cutoff.Add(2*time.Minute))
	trashed.InTrash = true
	f.addPages(
		fakeRow(1, "bd-before", cutoff.Add(-time.Second)),
		fakeRow(2, "bd-at", cutoff),
		fakeRow(3, "bd-after", cutoff.Add(time.Minute)),
		closed, archived, trashed,
	)
	tr := f.tracker()
	ctx := context.Background()

	all, err := tr.FetchIssues(ctx, itracker.FetchOptions{Since: &since})
	if err != nil {
		t.Fatalf("incremental FetchIssues: %v", err)
	}
	if got := strings.Join(beadsIDs(all), ","); got != "bd-after,bd-at,bd-closed" {
		t.Fatalf("incremental pull returned %q, want bd-after,bd-at,bd-closed", got)
	}
	if queried, candidates := tr.LastPullStats(); queried != 3 || candidates != 3 {
		t.Fatalf("LastPullStats = (%d, %d), want (3, 3)", queried, candidates)
	}
	reqs := f.requests()
	if len(reqs.filters) != 1 || reqs.unfilteredQueries != 0 {
		t.Fatalf("requests: %d filtered, %d unfiltered, want 1 filtered only", len(reqs.filters), reqs.unfilteredQueries)
	}
	if got, want := filterJSON(t, reqs.filters[0]), wantSinceFilter("2026-11-01T12:04:00Z"); got != want {
		t.Fatalf("filter = %s, want %s", got, want)
	}

	open, err := tr.FetchIssues(ctx, itracker.FetchOptions{Since: &since, State: "open"})
	if err != nil {
		t.Fatalf("incremental open FetchIssues: %v", err)
	}
	if got := strings.Join(beadsIDs(open), ","); got != "bd-after,bd-at" {
		t.Fatalf("incremental open pull returned %q, want bd-after,bd-at", got)
	}
	if queried, candidates := tr.LastPullStats(); queried != 3 || candidates != 2 {
		t.Fatalf("LastPullStats = (%d, %d), want (3, 2)", queried, candidates)
	}

	limited, err := tr.FetchIssues(ctx, itracker.FetchOptions{Since: &since, State: "open", Limit: 1})
	if err != nil {
		t.Fatalf("incremental limited FetchIssues: %v", err)
	}
	if got := strings.Join(beadsIDs(limited), ","); got != "bd-at" {
		t.Fatalf("incremental pull with limit 1 returned %q, want the first open row bd-at", got)
	}
	if queried, candidates := tr.LastPullStats(); queried != 3 || candidates != 1 {
		t.Fatalf("LastPullStats = (%d, %d), want (3, 1)", queried, candidates)
	}
}

// A single fetch resolves a page reference with one direct read and a Beads ID
// with one equality query scoped to the configured source, so it works on a
// source that cannot be enumerated within the bound. A direct read reaches any
// page shared with the integration, so only a live row whose data_source_id
// parent names the configured source counts.
func TestTrackerFetchIssueTargeted(t *testing.T) {
	t.Parallel()

	edited := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f := newFakeNotion(t)
	f.hugeSource = true
	member := fakeRow(1, "bd-1", edited)
	archived := fakeRow(2, "bd-archived", edited)
	archived.Archived = true
	trashed := fakeRow(3, "bd-trashed", edited)
	trashed.InTrash = true
	foreign := fakeRow(4, "bd-foreign", edited)
	foreign.Parent = sourceParent("11111111-2222-4333-8444-555555555555")
	noParent := fakeRow(5, "bd-no-parent", edited)
	noParent.Parent = nil
	databaseOnly := fakeRow(6, "bd-database-only", edited)
	databaseOnly.Parent = map[string]interface{}{"type": "database_id", "database_id": testDataSourceID}
	dupFirst := fakeRow(7, "bd-dup", edited)
	dupLast := fakeRow(8, "bd-dup", edited)
	dupArchived := fakeRow(9, "bd-dup", edited)
	dupArchived.Archived = true
	f.addPages(member, archived, trashed, foreign, noParent, databaseOnly, dupFirst, dupLast, dupArchived)
	missingID := "bbbbbbbb-0000-4000-8000-000000000001"
	ctx := context.Background()

	for _, ref := range []string{member.URL, member.ID, compactID(member.ID), strings.ToUpper(compactID(member.ID))} {
		f.resetRequests()
		got, err := f.tracker().FetchIssue(ctx, ref)
		if err != nil {
			t.Fatalf("FetchIssue(%q): %v", ref, err)
		}
		if got == nil || got.ID != member.ID {
			t.Fatalf("FetchIssue(%q) = %+v, want page %s", ref, got, member.ID)
		}
		reqs := f.requests()
		if len(reqs.gets) != 1 || reqs.gets[0] != compactID(member.ID) || reqs.unfilteredQueries != 0 || len(reqs.filters) != 0 {
			t.Fatalf("FetchIssue(%q) requests = %+v, want one direct read of %s", ref, reqs, member.ID)
		}
	}

	f.resetRequests()
	got, err := f.tracker().FetchIssue(ctx, "bd-1")
	if err != nil {
		t.Fatalf("FetchIssue(bd-1): %v", err)
	}
	if got == nil || got.ID != member.ID {
		t.Fatalf("FetchIssue(bd-1) = %+v, want page %s", got, member.ID)
	}
	reqs := f.requests()
	if reqs.unfilteredQueries != 0 || len(reqs.gets) != 0 || len(reqs.filters) != 1 {
		t.Fatalf("FetchIssue(bd-1) requests = %+v, want one filtered query", reqs)
	}
	if got, want := filterJSON(t, reqs.filters[0]), `{"property":"Beads ID","rich_text":{"equals":"bd-1"}}`; got != want {
		t.Fatalf("Beads ID filter = %s, want %s", got, want)
	}

	got, err = f.tracker().FetchIssue(ctx, "bd-dup")
	if err != nil {
		t.Fatalf("FetchIssue(bd-dup): %v", err)
	}
	if got == nil || got.ID != dupLast.ID {
		t.Fatalf("FetchIssue(bd-dup) = %+v, want the last live match %s", got, dupLast.ID)
	}

	f.resetRequests()
	for _, empty := range []string{"", "   "} {
		if got, err := f.tracker().FetchIssue(ctx, empty); got != nil || err != nil {
			t.Fatalf("FetchIssue(%q) = (%+v, %v), want (nil, nil)", empty, got, err)
		}
	}
	if reqs := f.requests(); reqs.unfilteredQueries != 0 || len(reqs.filters) != 0 || len(reqs.gets) != 0 {
		t.Fatalf("empty identifiers sent requests %+v", reqs)
	}

	for name, ref := range map[string]string{
		"missing":       missingID,
		"archived":      archived.URL,
		"trashed":       trashed.URL,
		"foreign":       foreign.URL,
		"no parent":     noParent.URL,
		"database only": databaseOnly.URL,
	} {
		got, err := f.tracker().FetchIssue(ctx, ref)
		if err != nil || got != nil {
			t.Fatalf("FetchIssue(%s page) = (%+v, %v), want (nil, nil)", name, got, err)
		}
	}
	for _, beadsID := range []string{"bd-archived", "bd-foreign", "bd-missing"} {
		got, err := f.tracker().FetchIssue(ctx, beadsID)
		if err != nil || got != nil {
			t.Fatalf("FetchIssue(%s) = (%+v, %v), want (nil, nil)", beadsID, got, err)
		}
	}

	variant := f.trackerFor(strings.ToUpper(compactID(testDataSourceID)))
	if got, err := variant.FetchIssue(ctx, member.URL); err != nil || got == nil || got.ID != member.ID {
		t.Fatalf("FetchIssue via a case/hyphen variant of the source ID = (%+v, %v), want page %s", got, err, member.ID)
	}
	if got, err := variant.FetchIssue(ctx, "bd-1"); err != nil || got == nil || got.ID != member.ID {
		t.Fatalf("FetchIssue(bd-1) via a case/hyphen variant of the source ID = (%+v, %v), want page %s", got, err, member.ID)
	}

	if reqs := f.requests(); reqs.unfilteredQueries != 0 {
		t.Fatalf("single fetches sent %d unfiltered queries, want none", reqs.unfilteredQueries)
	}
}

// localTask is the local side of fakeRow: equal to the row when title is
// "Task "+beadsID.
func localTask(beadsID, title string, updated time.Time, ref string) *types.Issue {
	issue := &types.Issue{ID: beadsID, Title: title, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask, UpdatedAt: updated}
	if ref != "" {
		issue.ExternalRef = &ref
	}
	return issue
}

func pushedIDs(items []itracker.BatchPushItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.LocalID)
	}
	sort.Strings(ids)
	return ids
}

// Push resolves each issue with a direct read of its linked page or a Beads ID
// query, never by listing the data source, so it works for every push size on
// a data source too large to enumerate.
func TestTrackerPushBeyondQueryBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	edited := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	newer := edited.Add(30 * time.Minute)

	// Each fixture is: bd-1 linked and changed (update), bd-2 unlinked but in
	// the source by Beads ID and changed (update), bd-3 unchanged (skip), bd-4
	// linked with the remote edited later (skip unless forced), bd-new nowhere
	// (create).
	setup := func(t *testing.T) (*fakeNotion, []fakePage, []*types.Issue) {
		f := newFakeNotion(t)
		f.hugeSource = true
		rows := []fakePage{fakeRow(1, "bd-1", edited), fakeRow(2, "bd-2", edited), fakeRow(3, "bd-3", edited), fakeRow(4, "bd-4", edited)}
		f.addPages(rows...)
		issues := []*types.Issue{
			localTask("bd-1", "Changed 1", newer, rows[0].URL),
			localTask("bd-2", "Changed 2", newer, ""),
			localTask("bd-3", "Task bd-3", newer, rows[2].URL),
			localTask("bd-4", "Changed 4", edited.Add(-time.Hour), rows[3].URL),
			localTask("bd-new", "New", newer, ""),
		}
		return f, rows, issues
	}

	t.Run("one issue per push", func(t *testing.T) {
		t.Parallel()
		f, rows, issues := setup(t)
		tr := f.tracker()
		want := []struct{ created, updated, skipped string }{
			{updated: "bd-1"}, {updated: "bd-2"}, {skipped: "bd-3"}, {skipped: "bd-4"}, {created: "bd-new"},
		}
		for i, issue := range issues {
			result, err := tr.BatchPush(ctx, []*types.Issue{issue}, nil)
			if err != nil {
				t.Fatalf("BatchPush(%s): %v", issue.ID, err)
			}
			got := struct{ created, updated, skipped string }{
				strings.Join(pushedIDs(result.Created), ","), strings.Join(pushedIDs(result.Updated), ","), strings.Join(result.Skipped, ","),
			}
			if got != want[i] || len(result.Errors) != 0 || len(result.Warnings) != 0 {
				t.Fatalf("BatchPush(%s) = %+v, want %+v", issue.ID, result, want[i])
			}
		}
		reqs := f.requests()
		if got, want := strings.Join(reqs.updated, ","), compactID(rows[0].ID)+","+compactID(rows[1].ID); got != want {
			t.Fatalf("updated pages = %s, want %s", got, want)
		}
		if got := strings.Join(reqs.created, ","); got != "bd-new" {
			t.Fatalf("created = %s, want bd-new", got)
		}
		// A linked issue costs one read; an unlinked one costs one query.
		if len(reqs.gets) != 3 || len(reqs.filters) != 2 || reqs.unfilteredQueries != 0 {
			t.Fatalf("requests = %+v, want 3 direct reads, 2 filtered queries and no enumeration", reqs)
		}

		f.resetRequests()
		result, err := tr.BatchPush(ctx, []*types.Issue{issues[3]}, map[string]bool{"bd-4": true})
		if err != nil || len(result.Updated) != 1 || result.Updated[0].LocalID != "bd-4" {
			t.Fatalf("forced BatchPush(bd-4) = (%+v, %v), want bd-4 updated", result, err)
		}
		if reqs := f.requests(); len(reqs.updated) != 1 || reqs.updated[0] != compactID(rows[3].ID) || reqs.unfilteredQueries != 0 {
			t.Fatalf("forced push requests = %+v, want one update of %s", reqs, rows[3].ID)
		}
	})

	t.Run("32 issues in one push", func(t *testing.T) {
		t.Parallel()
		f, rows, issues := setup(t)
		wantCreated := []string{"bd-new"}
		for i := len(issues); i < 32; i++ {
			id := fmt.Sprintf("bd-extra-%02d", i)
			issues = append(issues, localTask(id, "Extra", newer, ""))
			wantCreated = append(wantCreated, id)
		}
		sort.Strings(wantCreated)

		result, err := f.tracker().BatchPush(ctx, issues, nil)
		if err != nil {
			t.Fatalf("BatchPush: %v", err)
		}
		sort.Strings(result.Skipped)
		if got := strings.Join(pushedIDs(result.Updated), ","); got != "bd-1,bd-2" {
			t.Fatalf("updated = %s, want bd-1,bd-2", got)
		}
		if got := strings.Join(result.Skipped, ","); got != "bd-3,bd-4" {
			t.Fatalf("skipped = %s, want bd-3,bd-4", got)
		}
		if got, want := strings.Join(pushedIDs(result.Created), ","), strings.Join(wantCreated, ","); got != want {
			t.Fatalf("created = %s, want %s", got, want)
		}
		if len(result.Errors) != 0 || len(result.Warnings) != 0 {
			t.Fatalf("errors = %+v, warnings = %v", result.Errors, result.Warnings)
		}
		reqs := f.requests()
		sort.Strings(reqs.updated)
		if got, want := strings.Join(reqs.updated, ","), compactID(rows[0].ID)+","+compactID(rows[1].ID); got != want {
			t.Fatalf("updated pages = %s, want %s", got, want)
		}
		if reqs.unfilteredQueries != 0 || len(reqs.created) != len(wantCreated) {
			t.Fatalf("requests = %+v, want %d creates and no enumeration", reqs, len(wantCreated))
		}
	})

	t.Run("dry run writes nothing", func(t *testing.T) {
		t.Parallel()
		f, _, issues := setup(t)
		result, err := f.tracker().BatchPushDryRun(ctx, issues, map[string]bool{"bd-4": true})
		if err != nil {
			t.Fatalf("BatchPushDryRun: %v", err)
		}
		if got := strings.Join(pushedIDs(result.Updated), ","); got != "bd-1,bd-2,bd-4" {
			t.Fatalf("dry-run updated = %s, want bd-1,bd-2,bd-4", got)
		}
		if got := strings.Join(pushedIDs(result.Created), ","); got != "bd-new" {
			t.Fatalf("dry-run created = %s, want bd-new", got)
		}
		if reqs := f.requests(); len(reqs.created) != 0 || len(reqs.updated) != 0 || reqs.unfilteredQueries != 0 {
			t.Fatalf("dry run requests = %+v, want no writes and no enumeration", reqs)
		}
	})

	t.Run("empty push sends nothing", func(t *testing.T) {
		t.Parallel()
		f, _, _ := setup(t)
		result, err := f.tracker().BatchPush(ctx, nil, nil)
		if err != nil || result == nil {
			t.Fatalf("BatchPush(nil) = (%+v, %v), want an empty result", result, err)
		}
		if reqs := f.requests(); reqs.unfilteredQueries != 0 || len(reqs.filters) != 0 || len(reqs.gets) != 0 {
			t.Fatalf("empty push requests = %+v, want none", reqs)
		}
	})
}

// An external_ref whose page is missing, archived, trashed or in another data
// source falls back to the row carrying the issue's Beads ID. With no such row
// the push is skipped with the stale-ref warning: nothing is created, and the
// stale page is never written.
func TestTrackerPushStaleReferenceFallsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	edited := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	newer := edited.Add(30 * time.Minute)

	stale := map[string]func(n int, beadsID string) (ref string, page *fakePage){
		"missing": func(n int, _ string) (string, *fakePage) {
			return fmt.Sprintf("bbbbbbbb-0000-4000-8000-%012d", n), nil
		},
		"archived": func(n int, beadsID string) (string, *fakePage) {
			page := fakeRow(n, beadsID, edited)
			page.Archived = true
			return page.URL, &page
		},
		"trashed": func(n int, beadsID string) (string, *fakePage) {
			page := fakeRow(n, beadsID, edited)
			page.InTrash = true
			return page.URL, &page
		},
		"foreign": func(n int, beadsID string) (string, *fakePage) {
			page := fakeRow(n, beadsID, edited)
			page.Parent = sourceParent("11111111-2222-4333-8444-555555555555")
			return page.URL, &page
		},
	}

	for name, makeStale := range stale {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeNotion(t)
			f.hugeSource = true

			// bd-update and bd-skip have a live row; bd-orphan has none.
			var issues []*types.Issue
			var staleIDs []string
			for i, c := range []struct{ beadsID, title string }{
				{"bd-update", "Changed"}, {"bd-skip", "Task bd-skip"}, {"bd-orphan", "Changed"},
			} {
				ref, page := makeStale(10+i, c.beadsID)
				if page != nil {
					f.addPages(*page)
				}
				staleIDs = append(staleIDs, compactID(ExtractNotionIdentifier(ref)))
				issues = append(issues, localTask(c.beadsID, c.title, newer, ref))
			}
			live := fakeRow(20, "bd-update", edited)
			f.addPages(live, fakeRow(21, "bd-skip", edited))

			result, err := f.tracker().BatchPush(ctx, issues, nil)
			if err != nil {
				t.Fatalf("BatchPush: %v", err)
			}
			sort.Strings(result.Skipped)
			if got := strings.Join(pushedIDs(result.Updated), ","); got != "bd-update" {
				t.Fatalf("updated = %q, want bd-update", got)
			}
			if got := strings.Join(result.Skipped, ","); got != "bd-orphan,bd-skip" {
				t.Fatalf("skipped = %q, want bd-orphan,bd-skip", got)
			}
			if len(result.Created) != 0 || len(result.Errors) != 0 {
				t.Fatalf("created = %+v, errors = %+v, want none", result.Created, result.Errors)
			}
			if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "Skipped bd-orphan: Notion external_ref points outside the current target") {
				t.Fatalf("warnings = %q, want only the stale-ref warning for bd-orphan", result.Warnings)
			}
			if got := result.Updated[0].ExternalRef; compactID(ExtractNotionIdentifier(got)) != compactID(live.ID) {
				t.Fatalf("bd-update external_ref = %q, want the live row %s", got, live.ID)
			}

			reqs := f.requests()
			if len(reqs.created) != 0 || len(reqs.updated) != 1 || reqs.updated[0] != compactID(live.ID) {
				t.Fatalf("writes = created %v, updated %v; want only an update of %s", reqs.created, reqs.updated, live.ID)
			}
			for _, id := range staleIDs {
				for _, updated := range reqs.updated {
					if updated == id {
						t.Fatalf("stale page %s was written", id)
					}
				}
			}
			if reqs.unfilteredQueries != 0 {
				t.Fatalf("push sent %d unfiltered queries, want none", reqs.unfilteredQueries)
			}
		})
	}
}
