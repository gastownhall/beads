package notion

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

// fakeAPI is the in-memory notionAPI. It applies the query filters the
// tracker sends (see pageMatchesFilter in targeted_sync_test.go), records
// filters and lookups, and can fail unfiltered queries outright. Methods take
// the mutex because batch push resolves issues from worker goroutines.
type fakeAPI struct {
	mu           sync.Mutex
	user         *User
	dataSource   *DataSource
	pages        []Page
	createdPage  *Page
	updatedPage  *Page
	archivePage  *Page
	queryCalls   int
	queryFilters []map[string]interface{} // one per query; nil = unfiltered
	getCalls     int
	retrievedIDs []string

	failUnfiltered bool

	lastCreateDSID   string
	lastCreateProps  map[string]interface{}
	lastUpdatePageID string
	lastUpdateProps  map[string]interface{}
}

func (f *fakeAPI) GetCurrentUser(context.Context) (*User, error) { return f.user, nil }
func (f *fakeAPI) RetrieveDataSource(context.Context, string) (*DataSource, error) {
	return f.dataSource, nil
}
func (f *fakeAPI) QueryDataSource(_ context.Context, _ string, filter map[string]interface{}) ([]Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queryCalls++
	f.queryFilters = append(f.queryFilters, filter)
	if filter == nil && f.failUnfiltered {
		return nil, fmt.Errorf("query pagination exceeded 2 pages (~200 rows): unfiltered queries fail in this fake")
	}
	matched := make([]Page, 0, len(f.pages))
	for _, page := range f.pages {
		if pageMatchesFilter(page, filter) {
			matched = append(matched, page)
		}
	}
	return matched, nil
}
func (f *fakeAPI) ArchivePage(context.Context, string, bool) (*Page, error) {
	return f.archivePage, nil
}
func (f *fakeAPI) RetrievePage(_ context.Context, pageID string) (*Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	f.retrievedIDs = append(f.retrievedIDs, pageID)
	for i := range f.pages {
		if f.pages[i].ID == pageID {
			clone := f.pages[i]
			return &clone, nil
		}
	}
	return nil, &APIError{Status: http.StatusNotFound, Code: "object_not_found", Message: "Could not find page with ID: " + pageID}
}
func (f *fakeAPI) CreatePage(_ context.Context, dataSourceID string, properties map[string]interface{}) (*Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCreateDSID = dataSourceID
	f.lastCreateProps = properties
	return f.createdPage, nil
}
func (f *fakeAPI) UpdatePage(_ context.Context, pageID string, properties map[string]interface{}) (*Page, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastUpdatePageID = pageID
	f.lastUpdateProps = properties
	return f.updatedPage, nil
}

func strPtr(value string) *string {
	return &value
}

func TestTrackerFetchIssuesFiltersArchivedAndState(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(5 * time.Minute)
	api := &fakeAPI{
		pages: []Page{
			{
				ID:             "01234567-89ab-cdef-0123-456789abcdef",
				URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
				CreatedTime:    createdAt,
				LastEditedTime: updatedAt,
				Properties: map[string]PageProperty{
					PropertyTitle:    {Title: []RichText{{PlainText: "Open task"}}},
					PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
					PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
					PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
					PropertyType:     {Select: &SelectOption{Name: "Task"}},
				},
			},
			{
				ID:             "fedcba98-7654-3210-fedc-ba9876543210",
				URL:            "https://www.notion.so/Closed-fedcba9876543210fedcba9876543210",
				CreatedTime:    createdAt,
				LastEditedTime: updatedAt,
				Properties: map[string]PageProperty{
					PropertyTitle:    {Title: []RichText{{PlainText: "Closed task"}}},
					PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-2"}}},
					PropertyStatus:   {Select: &SelectOption{Name: "Closed"}},
					PropertyPriority: {Select: &SelectOption{Name: "Low"}},
					PropertyType:     {Select: &SelectOption{Name: "Task"}},
				},
			},
			{
				ID:       "11111111-2222-3333-4444-555555555555",
				Archived: true,
				Properties: map[string]PageProperty{
					PropertyTitle:    {Title: []RichText{{PlainText: "Archived"}}},
					PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-3"}}},
					PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
					PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
					PropertyType:     {Select: &SelectOption{Name: "Task"}},
				},
			},
		},
	}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	issues, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{State: "open"})
	if err != nil {
		t.Fatalf("FetchIssues returned error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	if issues[0].ID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("id = %q", issues[0].ID)
	}
}

// TestTrackerIncrementalBoundary pins the server-side Since cutoff to the
// same UTC minute floor matchesFetchSince truncates to: a page stamped
// exactly at the floor is fetched, one stamped before it is not, and the
// filter the tracker sends names that floor. State filtering, archived
// exclusion, the limit, and the queried/candidate stats ride along.
func TestTrackerIncrementalBoundary(t *testing.T) {
	t.Parallel()

	// Non-UTC zone with a nonzero second component: the filter must carry the
	// UTC minute floor, not the raw local time.
	since := time.Date(2026, 11, 1, 14, 34, 56, 0, time.FixedZone("test", 2*60*60))
	floor := since.UTC().Truncate(time.Minute) // 2026-11-01T12:34:00Z
	page := func(id, beadsID, title, status string, edited time.Time) Page {
		return Page{
			ID:             id,
			URL:            "https://www.notion.so/x-" + strings.ReplaceAll(id, "-", ""),
			CreatedTime:    edited.Add(-time.Hour),
			LastEditedTime: edited,
			Properties: map[string]PageProperty{
				PropertyTitle:    {Title: []RichText{{PlainText: title}}},
				PropertyBeadsID:  {RichText: []RichText{{PlainText: beadsID}}},
				PropertyStatus:   {Select: &SelectOption{Name: status}},
				PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
				PropertyType:     {Select: &SelectOption{Name: "Task"}},
			},
		}
	}
	archived := page("33333333-0000-0000-0000-000000000004", "bd-arch", "Archived after floor", "Open", floor.Add(3*time.Minute))
	archived.Archived = true
	api := &fakeAPI{pages: []Page{
		page("11111111-0000-0000-0000-000000000001", "bd-before", "Before the floor", "Open", floor.Add(-time.Second)),
		page("22222222-0000-0000-0000-000000000002", "bd-at", "At the floor", "Open", floor),
		page("33333333-0000-0000-0000-000000000003", "bd-after", "After the floor", "Open", floor.Add(time.Minute)),
		page("44444444-0000-0000-0000-000000000005", "bd-closed", "Closed after floor", "Closed", floor.Add(2*time.Minute)),
		archived,
	}}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	issues, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{Since: &since})
	if err != nil {
		t.Fatalf("FetchIssues returned error: %v", err)
	}
	want := []string{"At the floor", "After the floor", "Closed after floor"}
	if len(issues) != len(want) {
		t.Fatalf("issues = %v, want %v", titles(issues), want)
	}
	for i, title := range want {
		if issues[i].Title != title {
			t.Fatalf("issues = %v, want %v", titles(issues), want)
		}
	}

	if len(api.queryFilters) != 1 {
		t.Fatalf("queries = %d, want one", len(api.queryFilters))
	}
	filter := api.queryFilters[0]
	if filter["timestamp"] != "last_edited_time" {
		t.Fatalf("filter timestamp = %v, want last_edited_time", filter["timestamp"])
	}
	condition, ok := filter["last_edited_time"].(map[string]interface{})
	if !ok || condition["on_or_after"] != "2026-11-01T12:34:00Z" {
		t.Fatalf("filter condition = %v, want on_or_after 2026-11-01T12:34:00Z", filter["last_edited_time"])
	}

	// State and limit still select from the live rows the query returned:
	// queried counts them, candidates the survivors.
	_, err = tracker.FetchIssues(context.Background(), itracker.FetchOptions{Since: &since, State: "open", Limit: 1})
	if err != nil {
		t.Fatalf("FetchIssues(state, limit) returned error: %v", err)
	}
	queried, candidates := tracker.LastPullStats()
	if queried != 3 || candidates != 1 {
		t.Fatalf("LastPullStats = (%d, %d), want (3, 1) — three live rows in the window, one open candidate under the limit", queried, candidates)
	}
}

// TestTrackerFetchIssuesIncludesFloorOfLastSyncMinute pins the boundary the
// way operators feel it: a fetch from Since returns pages stamped at Since's
// UTC minute floor — so edits made later in the same minute as last_sync are
// not lost — and the stats name the rows the window query returned.
func TestTrackerFetchIssuesIncludesFloorOfLastSyncMinute(t *testing.T) {
	t.Parallel()

	boundary := time.Date(2026, 3, 22, 9, 54, 51, 0, time.UTC)
	api := &fakeAPI{
		pages: []Page{
			{
				ID:             "01234567-89ab-cdef-0123-456789abcdef",
				URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
				CreatedTime:    boundary.Add(-10 * time.Minute),
				LastEditedTime: boundary.Add(9 * time.Second), // 09:55:00, the floor of since
				Properties: map[string]PageProperty{
					PropertyTitle:    {Title: []RichText{{PlainText: "At the floor"}}},
					PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
					PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
					PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
					PropertyType:     {Select: &SelectOption{Name: "Task"}},
				},
			},
			{
				ID:             "11111111-2222-3333-4444-555555555555",
				URL:            "https://www.notion.so/Task-11111111222233334444555555555555",
				CreatedTime:    boundary.Add(-5 * time.Minute),
				LastEditedTime: boundary.Add(10 * time.Second), // one second past the floor
				Properties: map[string]PageProperty{
					PropertyTitle:    {Title: []RichText{{PlainText: "After the floor"}}},
					PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-2"}}},
					PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
					PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
					PropertyType:     {Select: &SelectOption{Name: "Task"}},
				},
			},
		},
	}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	since := boundary.Add(23 * time.Second) // 09:55:14, floor 09:55:00
	issues, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{Since: &since})
	if err != nil {
		t.Fatalf("FetchIssues returned error: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues = %d, want 2", len(issues))
	}
	if issues[0].Title != "At the floor" || issues[1].Title != "After the floor" {
		t.Fatalf("titles = %q, %q", issues[0].Title, issues[1].Title)
	}
	queried, candidates := tracker.LastPullStats()
	if queried != 2 || candidates != 2 {
		t.Fatalf("LastPullStats = (%d, %d), want (2, 2)", queried, candidates)
	}
}

// TestTrackerFetchIssueTargeted covers the resolution rules of a single
// fetch: page references become direct reads, Beads IDs become source-scoped
// equality queries, only live members of the configured source count, and
// none of it narrows a later full pull.
func TestTrackerFetchIssueTargeted(t *testing.T) {
	t.Parallel()

	member := func(id, beadsID, title string) Page {
		return Page{
			Object:         "page",
			ID:             id,
			URL:            "https://www.notion.so/x-" + strings.ReplaceAll(id, "-", ""),
			CreatedTime:    time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC),
			LastEditedTime: time.Date(2026, 3, 19, 14, 5, 0, 0, time.UTC),
			Parent:         &PageParent{Type: "data_source_id", DataSourceID: "ds_123", DatabaseID: "db_1"},
			Properties: map[string]PageProperty{
				PropertyTitle:    {Title: []RichText{{PlainText: title}}},
				PropertyBeadsID:  {RichText: []RichText{{PlainText: beadsID}}},
				PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
				PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
				PropertyType:     {Select: &SelectOption{Name: "Task"}},
			},
		}
	}

	t.Run("page URL and bare ID use a direct GET", func(t *testing.T) {
		t.Parallel()
		api := &fakeAPI{pages: []Page{member("01234567-89ab-cdef-0123-456789abcdef", "bd-1", "Page One")}}
		tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

		for _, ref := range []string{
			"https://www.notion.so/Page-One-0123456789abcdef0123456789abcdef",
			"0123456789abcdef0123456789abcdef",
			"01234567-89ab-cdef-0123-456789abcdef",
		} {
			issue, err := tracker.FetchIssue(context.Background(), ref)
			if err != nil {
				t.Fatalf("FetchIssue(%q) returned error: %v", ref, err)
			}
			if issue == nil || issue.Title != "Page One" {
				t.Fatalf("FetchIssue(%q) = %+v, want Page One", ref, issue)
			}
		}
		if api.getCalls != 3 || api.queryCalls != 0 {
			t.Fatalf("gets = %d, queries = %d, want 3 direct GETs and no query", api.getCalls, api.queryCalls)
		}
	})

	t.Run("Beads ID uses a source-scoped equality query", func(t *testing.T) {
		t.Parallel()
		api := &fakeAPI{pages: []Page{member("01234567-89ab-cdef-0123-456789abcdef", "bd-1", "Page One")}}
		tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

		issue, err := tracker.FetchIssue(context.Background(), "bd-1")
		if err != nil {
			t.Fatalf("FetchIssue(bd-1) returned error: %v", err)
		}
		if issue == nil || issue.Title != "Page One" {
			t.Fatalf("FetchIssue(bd-1) = %+v, want Page One", issue)
		}
		if api.getCalls != 0 || api.queryCalls != 1 {
			t.Fatalf("gets = %d, queries = %d, want one equality query and no GET", api.getCalls, api.queryCalls)
		}
		filter := api.queryFilters[0]
		if filter["property"] != PropertyBeadsID {
			t.Fatalf("filter property = %v, want %q", filter["property"], PropertyBeadsID)
		}
		condition, ok := filter["rich_text"].(map[string]interface{})
		if !ok || condition["equals"] != "bd-1" {
			t.Fatalf("filter condition = %v, want equals bd-1", filter["rich_text"])
		}
	})

	t.Run("blank identifier issues no request", func(t *testing.T) {
		t.Parallel()
		api := &fakeAPI{pages: []Page{member("01234567-89ab-cdef-0123-456789abcdef", "bd-1", "Page One")}}
		tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

		issue, err := tracker.FetchIssue(context.Background(), "   ")
		if err != nil || issue != nil {
			t.Fatalf("FetchIssue(blank) = (%v, %v), want (nil, nil)", issue, err)
		}
		if api.getCalls+api.queryCalls != 0 {
			t.Fatalf("a blank identifier issued %d requests, want none", api.getCalls+api.queryCalls)
		}
	})

	t.Run("missing, archived, trashed, and non-member pages are no result", func(t *testing.T) {
		t.Parallel()
		archived := member("11111111-0000-0000-0000-000000000001", "bd-arch", "Archived")
		archived.Archived = true
		trashed := member("22222222-0000-0000-0000-000000000002", "bd-trash", "Trashed")
		trashed.InTrash = true
		foreign := member("33333333-0000-0000-0000-000000000003", "bd-foreign", "Foreign source")
		foreign.Parent = &PageParent{Type: "data_source_id", DataSourceID: "ds_other", DatabaseID: "db_1"}
		databaseOnly := member("44444444-0000-0000-0000-000000000004", "bd-db", "Database parent")
		databaseOnly.Parent = &PageParent{Type: "database_id", DatabaseID: "db_1"}
		noParent := member("55555555-0000-0000-0000-000000000005", "bd-noparent", "No parent")
		noParent.Parent = nil
		api := &fakeAPI{pages: []Page{archived, trashed, foreign, databaseOnly, noParent}}
		tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

		for _, page := range api.pages {
			issue, err := tracker.FetchIssue(context.Background(), page.ID)
			if err != nil {
				t.Fatalf("FetchIssue(%s) returned error: %v", page.ID, err)
			}
			if issue != nil {
				t.Fatalf("FetchIssue(%s) = %q, want (nil, nil)", page.ID, issue.Title)
			}
		}

		missing, err := tracker.FetchIssue(context.Background(), "99999999-0000-0000-0000-000000000009")
		if err != nil || missing != nil {
			t.Fatalf("FetchIssue(missing) = (%v, %v), want (nil, nil)", missing, err)
		}
	})

	t.Run("data source id spelling is normalized", func(t *testing.T) {
		t.Parallel()
		hyphenated := member("01234567-89ab-cdef-0123-456789abcdef", "bd-1", "Hyphenated")
		hyphenated.Parent = &PageParent{Type: "data_source_id", DataSourceID: "329e5bf9-7fae-8080-bb4a-d94e1387655d", DatabaseID: "db_1"}
		upper := member("fedcba98-7654-3210-fedc-ba9876543210", "bd-2", "Uppercase")
		upper.Parent = &PageParent{Type: "data_source_id", DataSourceID: "329E5BF9-7FAE-8080-BB4A-D94E1387655D", DatabaseID: "db_1"}
		api := &fakeAPI{pages: []Page{hyphenated, upper}}
		tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "329e5bf97fae8080bb4ad94e1387655d"}

		// The configured id is compact while one parent carries hyphens and
		// the other differs only in case; all three spell the same source.
		issue, err := tracker.FetchIssue(context.Background(), "01234567-89ab-cdef-0123-456789abcdef")
		if err != nil || issue == nil || issue.Title != "Hyphenated" {
			t.Fatalf("FetchIssue(hyphenated parent) = (%v, %v), want Hyphenated", issue, err)
		}
		issue, err = tracker.FetchIssue(context.Background(), "fedcba98-7654-3210-fedc-ba9876543210")
		if err != nil || issue == nil || issue.Title != "Uppercase" {
			t.Fatalf("FetchIssue(uppercase parent) = (%v, %v), want Uppercase", issue, err)
		}
	})

	t.Run("duplicate Beads IDs: the last live match wins", func(t *testing.T) {
		t.Parallel()
		archived := member("22222222-0000-0000-0000-000000000002", "bd-1", "Archived twin")
		archived.Archived = true
		api := &fakeAPI{pages: []Page{
			member("11111111-0000-0000-0000-000000000001", "bd-1", "First"),
			archived,
			member("33333333-0000-0000-0000-000000000003", "bd-1", "Last"),
		}}
		tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

		issue, err := tracker.FetchIssue(context.Background(), "bd-1")
		if err != nil {
			t.Fatalf("FetchIssue(bd-1) returned error: %v", err)
		}
		if issue == nil || issue.Title != "Last" {
			t.Fatalf("FetchIssue(bd-1) = %+v, want the last live match", issue)
		}
	})

	t.Run("targeted reads do not restrict a later full pull", func(t *testing.T) {
		t.Parallel()
		api := &fakeAPI{pages: []Page{
			member("01234567-89ab-cdef-0123-456789abcdef", "bd-1", "Page One"),
			member("fedcba98-7654-3210-fedc-ba9876543210", "bd-2", "Page Two"),
		}}
		tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

		if _, err := tracker.FetchIssue(context.Background(), "bd-1"); err != nil {
			t.Fatalf("FetchIssue returned error: %v", err)
		}
		issues, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{})
		if err != nil {
			t.Fatalf("FetchIssues returned error: %v", err)
		}
		if len(issues) != 2 {
			t.Fatalf("full pull after a targeted read returned %d issues, want 2", len(issues))
		}
		if api.queryFilters[len(api.queryFilters)-1] != nil {
			t.Fatalf("full pull sent a filter: %v", api.queryFilters[len(api.queryFilters)-1])
		}
	})
}

func TestTrackerCreateAndUpdateIssue(t *testing.T) {
	t.Parallel()

	createdPage := &Page{
		ID:             "01234567-89ab-cdef-0123-456789abcdef",
		URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
		CreatedTime:    time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC),
		LastEditedTime: time.Date(2026, 3, 19, 14, 5, 0, 0, time.UTC),
		Properties: map[string]PageProperty{
			PropertyTitle:    {Title: []RichText{{PlainText: "Create me"}}},
			PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
			PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
			PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
			PropertyType:     {Select: &SelectOption{Name: "Task"}},
		},
	}
	updatedPage := &Page{
		ID:             "01234567-89ab-cdef-0123-456789abcdef",
		URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
		CreatedTime:    createdPage.CreatedTime,
		LastEditedTime: createdPage.LastEditedTime.Add(10 * time.Minute),
		Properties: map[string]PageProperty{
			PropertyTitle:    {Title: []RichText{{PlainText: "Update me"}}},
			PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
			PropertyStatus:   {Select: &SelectOption{Name: "In Progress"}},
			PropertyPriority: {Select: &SelectOption{Name: "High"}},
			PropertyType:     {Select: &SelectOption{Name: "Feature"}},
		},
	}
	api := &fakeAPI{
		createdPage: createdPage,
		updatedPage: updatedPage,
	}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	created, err := tracker.CreateIssue(context.Background(), &types.Issue{
		ID:        "bd-1",
		Title:     "Create me",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
	})
	if err != nil {
		t.Fatalf("CreateIssue returned error: %v", err)
	}
	if api.lastCreateDSID != "ds_123" {
		t.Fatalf("data source id = %q", api.lastCreateDSID)
	}
	if created.URL != "https://www.notion.so/0123456789abcdef0123456789abcdef" {
		t.Fatalf("created URL = %q", created.URL)
	}

	updated, err := tracker.UpdateIssue(context.Background(), "https://www.notion.so/Task-0123456789abcdef0123456789abcdef", &types.Issue{
		ID:        "bd-1",
		Title:     "Update me",
		Status:    types.StatusInProgress,
		Priority:  1,
		IssueType: types.TypeFeature,
	})
	if err != nil {
		t.Fatalf("UpdateIssue returned error: %v", err)
	}
	if api.lastUpdatePageID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("update page id = %q", api.lastUpdatePageID)
	}
	if updated.Title != "Update me" {
		t.Fatalf("updated title = %q", updated.Title)
	}
}

func TestTrackerBuildExternalRefFallbacks(t *testing.T) {
	t.Parallel()

	tracker := &Tracker{}
	if got := tracker.BuildExternalRef(&itracker.TrackerIssue{ID: "01234567-89ab-cdef-0123-456789abcdef"}); got != "https://www.notion.so/0123456789abcdef0123456789abcdef" {
		t.Fatalf("got = %q", got)
	}
}

func TestTrackerBatchPushReusesRemoteInventory(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC)
	remotePage := Page{
		ID:             "01234567-89ab-cdef-0123-456789abcdef",
		URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
		CreatedTime:    createdAt,
		LastEditedTime: createdAt.Add(5 * time.Minute),
		Properties: map[string]PageProperty{
			PropertyTitle:    {Title: []RichText{{PlainText: "Existing remote"}}},
			PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
			PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
			PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
			PropertyType:     {Select: &SelectOption{Name: "Task"}},
		},
	}
	api := &fakeAPI{
		pages: []Page{remotePage},
		createdPage: &Page{
			ID:             "11111111-2222-3333-4444-555555555555",
			URL:            "https://www.notion.so/New-11111111222233334444555555555555",
			CreatedTime:    createdAt,
			LastEditedTime: createdAt.Add(10 * time.Minute),
			Properties: map[string]PageProperty{
				PropertyTitle:    {Title: []RichText{{PlainText: "Create me"}}},
				PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-2"}}},
				PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
				PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
				PropertyType:     {Select: &SelectOption{Name: "Task"}},
			},
		},
		updatedPage: &Page{
			ID:             remotePage.ID,
			URL:            remotePage.URL,
			CreatedTime:    remotePage.CreatedTime,
			LastEditedTime: remotePage.LastEditedTime.Add(10 * time.Minute),
			Properties: map[string]PageProperty{
				PropertyTitle:    {Title: []RichText{{PlainText: "Updated local"}}},
				PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
				PropertyStatus:   {Select: &SelectOption{Name: "In Progress"}},
				PropertyPriority: {Select: &SelectOption{Name: "High"}},
				PropertyType:     {Select: &SelectOption{Name: "Feature"}},
			},
		},
	}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	result, err := tracker.BatchPush(context.Background(), []*types.Issue{
		{
			ID:          "bd-1",
			Title:       "Updated local",
			Status:      types.StatusInProgress,
			Priority:    1,
			IssueType:   types.TypeFeature,
			ExternalRef: strPtr("https://www.notion.so/Task-0123456789abcdef0123456789abcdef"),
			UpdatedAt:   remotePage.LastEditedTime.Add(30 * time.Minute),
		},
		{
			ID:        "bd-2",
			Title:     "Create me",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			UpdatedAt: remotePage.LastEditedTime.Add(30 * time.Minute),
		},
	}, map[string]bool{})
	if err != nil {
		t.Fatalf("BatchPush returned error: %v", err)
	}
	if api.queryCalls != 1 {
		t.Fatalf("queryCalls = %d, want 1", api.queryCalls)
	}
	if len(result.Updated) != 1 || len(result.Created) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if api.lastUpdatePageID != "01234567-89ab-cdef-0123-456789abcdef" {
		t.Fatalf("lastUpdatePageID = %q", api.lastUpdatePageID)
	}
	if api.lastCreateDSID != "ds_123" {
		t.Fatalf("lastCreateDSID = %q", api.lastCreateDSID)
	}
}

func TestTrackerBatchPushMatchesCurrentTargetByLocalID(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC)
	remotePage := Page{
		ID:             "01234567-89ab-cdef-0123-456789abcdef",
		URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
		CreatedTime:    createdAt,
		LastEditedTime: createdAt.Add(5 * time.Minute),
		Properties: map[string]PageProperty{
			PropertyTitle:    {Title: []RichText{{PlainText: "Existing remote"}}},
			PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
			PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
			PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
			PropertyType:     {Select: &SelectOption{Name: "Task"}},
		},
	}
	api := &fakeAPI{
		pages: []Page{remotePage},
		updatedPage: &Page{
			ID:             remotePage.ID,
			URL:            remotePage.URL,
			CreatedTime:    remotePage.CreatedTime,
			LastEditedTime: remotePage.LastEditedTime.Add(10 * time.Minute),
			Properties: map[string]PageProperty{
				PropertyTitle:    {Title: []RichText{{PlainText: "Updated local"}}},
				PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
				PropertyStatus:   {Select: &SelectOption{Name: "In Progress"}},
				PropertyPriority: {Select: &SelectOption{Name: "High"}},
				PropertyType:     {Select: &SelectOption{Name: "Feature"}},
			},
		},
	}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}
	foreignRef := "https://www.notion.so/foreign-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	result, err := tracker.BatchPush(context.Background(), []*types.Issue{
		{
			ID:          "bd-1",
			Title:       "Updated local",
			Status:      types.StatusInProgress,
			Priority:    1,
			IssueType:   types.TypeFeature,
			ExternalRef: &foreignRef,
			UpdatedAt:   remotePage.LastEditedTime.Add(30 * time.Minute),
		},
	}, map[string]bool{})
	if err != nil {
		t.Fatalf("BatchPush returned error: %v", err)
	}
	if len(result.Updated) != 1 || len(result.Created) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if api.lastUpdatePageID != remotePage.ID {
		t.Fatalf("lastUpdatePageID = %q, want %q", api.lastUpdatePageID, remotePage.ID)
	}
	if api.lastCreateDSID != "" {
		t.Fatalf("lastCreateDSID = %q, want empty", api.lastCreateDSID)
	}
}

func TestTrackerBatchPushSkipsStaleExternalRefOutsideCurrentTarget(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}
	foreignRef := "https://www.notion.so/foreign-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	result, err := tracker.BatchPush(context.Background(), []*types.Issue{
		{
			ID:          "bd-1",
			Title:       "Stale ref",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			ExternalRef: &foreignRef,
			UpdatedAt:   time.Now().UTC(),
		},
	}, map[string]bool{})
	if err != nil {
		t.Fatalf("BatchPush returned error: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "bd-1" {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "outside the current target") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	if api.lastCreateDSID != "" || api.lastUpdatePageID != "" {
		t.Fatalf("unexpected mutation create=%q update=%q", api.lastCreateDSID, api.lastUpdatePageID)
	}
}

func TestTrackerBatchPushDryRunSkipsStaleExternalRefOutsideCurrentTarget(t *testing.T) {
	t.Parallel()

	api := &fakeAPI{}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}
	foreignRef := "https://www.notion.so/foreign-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	result, err := tracker.BatchPushDryRun(context.Background(), []*types.Issue{
		{
			ID:          "bd-1",
			Title:       "Stale ref",
			Status:      types.StatusOpen,
			Priority:    2,
			IssueType:   types.TypeTask,
			ExternalRef: &foreignRef,
			UpdatedAt:   time.Now().UTC(),
		},
	}, map[string]bool{})
	if err != nil {
		t.Fatalf("BatchPushDryRun returned error: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "bd-1" {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
	if len(result.Updated) != 0 || len(result.Created) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "outside the current target") {
		t.Fatalf("warnings = %+v", result.Warnings)
	}
	if api.lastCreateDSID != "" || api.lastUpdatePageID != "" {
		t.Fatalf("unexpected mutation create=%q update=%q", api.lastCreateDSID, api.lastUpdatePageID)
	}
}

func TestTrackerBatchPushDryRunTreatsLabelOnlyChangeAsUpdate(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC)
	remotePage := Page{
		ID:             "01234567-89ab-cdef-0123-456789abcdef",
		URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
		CreatedTime:    createdAt,
		LastEditedTime: createdAt.Add(5 * time.Minute),
		Properties: map[string]PageProperty{
			PropertyTitle:    {Title: []RichText{{PlainText: "Existing remote"}}},
			PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
			PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
			PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
			PropertyType:     {Select: &SelectOption{Name: "Task"}},
			PropertyLabels:   {MultiSelect: []SelectOption{{Name: "matrix-a"}, {Name: "matrix-b"}}},
		},
	}
	api := &fakeAPI{pages: []Page{remotePage}}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	result, err := tracker.BatchPushDryRun(context.Background(), []*types.Issue{
		{
			ID:        "bd-1",
			Title:     "Existing remote",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Labels:    []string{"matrix-a", "matrix-c"},
			UpdatedAt: remotePage.LastEditedTime.Add(30 * time.Minute),
		},
	}, map[string]bool{})
	if err != nil {
		t.Fatalf("BatchPushDryRun returned error: %v", err)
	}
	if len(result.Updated) != 1 || result.Updated[0].LocalID != "bd-1" {
		t.Fatalf("updated = %+v", result.Updated)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
	if api.lastCreateDSID != "" || api.lastUpdatePageID != "" {
		t.Fatalf("unexpected mutation create=%q update=%q", api.lastCreateDSID, api.lastUpdatePageID)
	}
}

func TestTrackerBatchPushDryRunSkipsLabelOrderOnlyDifference(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 3, 19, 14, 0, 0, 0, time.UTC)
	remotePage := Page{
		ID:             "01234567-89ab-cdef-0123-456789abcdef",
		URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
		CreatedTime:    createdAt,
		LastEditedTime: createdAt.Add(5 * time.Minute),
		Properties: map[string]PageProperty{
			PropertyTitle:    {Title: []RichText{{PlainText: "Existing remote"}}},
			PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-1"}}},
			PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
			PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
			PropertyType:     {Select: &SelectOption{Name: "Task"}},
			PropertyLabels:   {MultiSelect: []SelectOption{{Name: "matrix-b"}, {Name: "matrix-a"}}},
		},
	}
	api := &fakeAPI{pages: []Page{remotePage}}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	result, err := tracker.BatchPushDryRun(context.Background(), []*types.Issue{
		{
			ID:        "bd-1",
			Title:     "Existing remote",
			Status:    types.StatusOpen,
			Priority:  2,
			IssueType: types.TypeTask,
			Labels:    []string{"matrix-a", "matrix-b"},
			UpdatedAt: remotePage.LastEditedTime.Add(30 * time.Minute),
		},
	}, map[string]bool{})
	if err != nil {
		t.Fatalf("BatchPushDryRun returned error: %v", err)
	}
	if len(result.Skipped) != 1 || result.Skipped[0] != "bd-1" {
		t.Fatalf("skipped = %+v", result.Skipped)
	}
	if len(result.Updated) != 0 || len(result.Created) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if api.lastCreateDSID != "" || api.lastUpdatePageID != "" {
		t.Fatalf("unexpected mutation create=%q update=%q", api.lastCreateDSID, api.lastUpdatePageID)
	}
}

// TestGetConfig_YamlOnlyKeyBypassesStore verifies that yaml-only keys
// bypass the Dolt store entirely, falling through to config.yaml and env var.
// notion.token is resolved by ResolveAuth (see auth_test.go); this covers the
// generic getConfig branch.
func TestGetConfig_YamlOnlyKeyBypassesStore(t *testing.T) {
	ctx := context.Background()
	tr := &Tracker{store: nil}

	// Use a key that's yaml-only via prefix match (dolt.* keys are yaml-only).
	// This exercises the yaml-only branch without needing a Notion-specific secret.
	t.Run("yaml-only key falls back to env var", func(t *testing.T) {
		t.Setenv("TEST_YAML_ONLY", "env-value")
		got := tr.getConfig(ctx, "dolt.test-key", "TEST_YAML_ONLY")
		if got != "env-value" {
			t.Errorf("getConfig(dolt.test-key) = %q, want %q", got, "env-value")
		}
	})

	t.Run("yaml-only key returns empty when no value is set", func(t *testing.T) {
		t.Setenv("TEST_YAML_ONLY", "")
		got := tr.getConfig(ctx, "dolt.test-key", "TEST_YAML_ONLY")
		if got != "" {
			t.Errorf("getConfig(dolt.test-key) = %q, want empty", got)
		}
	})

	t.Run("non-yaml key with nil store returns env var", func(t *testing.T) {
		t.Setenv("NOTION_DATA_SOURCE_ID", "env-ds-id")
		got := tr.getConfig(ctx, "notion.data_source_id", "NOTION_DATA_SOURCE_ID")
		if got != "env-ds-id" {
			t.Errorf("getConfig(notion.data_source_id) = %q, want %q", got, "env-ds-id")
		}
	})
}

func TestTrackerMaxQueryPages(t *testing.T) {
	ctx := context.Background()

	// Unset means "use the client's own default": a data source under the
	// default ceiling needs no configuration at all.
	t.Run("unset falls back to the client default", func(t *testing.T) {
		t.Setenv("NOTION_MAX_QUERY_PAGES", "")
		tr := &Tracker{}
		got, err := tr.maxQueryPages(ctx)
		if err != nil {
			t.Fatalf("maxQueryPages returned error: %v", err)
		}
		if got != 0 {
			t.Fatalf("maxQueryPages = %d, want 0 (unset)", got)
		}
	})

	t.Run("a positive value is read", func(t *testing.T) {
		t.Setenv("NOTION_MAX_QUERY_PAGES", "120")
		tr := &Tracker{}
		got, err := tr.maxQueryPages(ctx)
		if err != nil {
			t.Fatalf("maxQueryPages returned error: %v", err)
		}
		if got != 120 {
			t.Fatalf("maxQueryPages = %d, want 120", got)
		}
	})

	// Set-but-unusable is an error, not a silent fallback. The operator sets
	// this key because sync is already failing on the bound; ignoring a bad
	// value would reproduce the identical failure with nothing to explain it.
	for _, bad := range []string{"lots", "0", "-5", "12.5"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			t.Setenv("NOTION_MAX_QUERY_PAGES", bad)
			tr := &Tracker{}
			if _, err := tr.maxQueryPages(ctx); err == nil {
				t.Fatalf("maxQueryPages(%q) succeeded, want an error", bad)
			}
		})
	}
}

// The bound is useless if it stops at the config read, which is exactly what
// this PR was first sent without. This pins the whole path: env var through
// Init to the field the client paginates on.
func TestTrackerInitAppliesMaxQueryPagesToClient(t *testing.T) {
	t.Setenv("NOTION_TOKEN", "secret-token")
	t.Setenv("NOTION_DATA_SOURCE_ID", "ds_123")
	t.Setenv("NOTION_MAX_QUERY_PAGES", "120")

	original := newNotionClient
	defer func() { newNotionClient = original }()

	var applied int
	newNotionClient = func(token string, maxQueryPages int) notionAPI {
		applied = maxQueryPages
		return original(token, maxQueryPages)
	}

	tr := &Tracker{}
	if err := tr.Init(context.Background(), nil); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if applied != 120 {
		t.Fatalf("bound passed to the client = %d, want 120", applied)
	}
	client, ok := tr.client.(*Client)
	if !ok {
		t.Fatalf("client type = %T, want *Client", tr.client)
	}
	if client.maxQueryPages() != 120 {
		t.Fatalf("client paginates at %d, want 120", client.maxQueryPages())
	}
}

func TestTrackerInitRejectsUnusableMaxQueryPages(t *testing.T) {
	t.Setenv("NOTION_TOKEN", "secret-token")
	t.Setenv("NOTION_DATA_SOURCE_ID", "ds_123")
	t.Setenv("NOTION_MAX_QUERY_PAGES", "lots")

	tr := &Tracker{}
	err := tr.Init(context.Background(), nil)
	if err == nil {
		t.Fatal("Init succeeded, want an error naming the bad value")
	}
	if !strings.Contains(err.Error(), "notion.max_query_pages") {
		t.Fatalf("error should name the config key, got: %v", err)
	}
	// The bad value came from the environment here, so naming only the config key
	// points the operator at a lever they did not pull.
	if !strings.Contains(err.Error(), "NOTION_MAX_QUERY_PAGES") {
		t.Fatalf("error should name the env var the value came from, got: %v", err)
	}
}

// An unset bound must leave the client exactly where it was before this option
// existed — 50 pages, hardcoded here so lowering the default fails the test.
func TestNewNotionClientUnsetBoundKeepsDefault(t *testing.T) {
	t.Parallel()

	client, ok := newNotionClient("secret-token", 0).(*Client)
	if !ok {
		t.Fatalf("client type = %T, want *Client", client)
	}
	if client.maxQueryPages() != 50 {
		t.Fatalf("client paginates at %d, want the unchanged default 50", client.maxQueryPages())
	}
}

// TestMatchesFetchSinceUsesSinceGranularity pins SinceGranularity to the
// fetch filter: a fetch from Since returns a page stamped at Since truncated
// to SinceGranularity, and none stamped before that.
func TestMatchesFetchSinceUsesSinceGranularity(t *testing.T) {
	since := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	floor := since.Truncate((&Tracker{}).SinceGranularity())
	if !matchesFetchSince(&itracker.TrackerIssue{UpdatedAt: floor}, &since) {
		t.Fatalf("a page stamped %s is not fetched from %s", floor, since)
	}
	if matchesFetchSince(&itracker.TrackerIssue{UpdatedAt: floor.Add(-time.Nanosecond)}, &since) {
		t.Fatalf("a page stamped just before %s is fetched from %s", floor, since)
	}
}
