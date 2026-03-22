package notion

import (
	"context"
	"strings"
	"testing"
	"time"

	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type fakeAPI struct {
	user             *User
	dataSource       *DataSource
	pages            []Page
	createdPage      *Page
	updatedPage      *Page
	archivePage      *Page
	queryCalls       int
	lastCreateDSID   string
	lastCreateProps  map[string]interface{}
	lastUpdatePageID string
	lastUpdateProps  map[string]interface{}
}

func (f *fakeAPI) GetCurrentUser(context.Context) (*User, error) { return f.user, nil }
func (f *fakeAPI) RetrieveDataSource(context.Context, string) (*DataSource, error) {
	return f.dataSource, nil
}
func (f *fakeAPI) QueryDataSource(context.Context, string) ([]Page, error) {
	f.queryCalls++
	return append([]Page(nil), f.pages...), nil
}
func (f *fakeAPI) ArchivePage(context.Context, string, bool) (*Page, error) {
	return f.archivePage, nil
}
func (f *fakeAPI) CreatePage(_ context.Context, dataSourceID string, properties map[string]interface{}) (*Page, error) {
	f.lastCreateDSID = dataSourceID
	f.lastCreateProps = properties
	return f.createdPage, nil
}
func (f *fakeAPI) UpdatePage(_ context.Context, pageID string, properties map[string]interface{}) (*Page, error) {
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

func TestTrackerFetchIssuesExcludesEqualLastSyncBoundary(t *testing.T) {
	t.Parallel()

	boundary := time.Date(2026, 3, 22, 9, 54, 51, 0, time.UTC)
	api := &fakeAPI{
		pages: []Page{
			{
				ID:             "01234567-89ab-cdef-0123-456789abcdef",
				URL:            "https://www.notion.so/Task-0123456789abcdef0123456789abcdef",
				CreatedTime:    boundary.Add(-10 * time.Minute),
				LastEditedTime: boundary,
				Properties: map[string]PageProperty{
					PropertyTitle:    {Title: []RichText{{PlainText: "Equal boundary"}}},
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
				LastEditedTime: boundary.Add(time.Second),
				Properties: map[string]PageProperty{
					PropertyTitle:    {Title: []RichText{{PlainText: "After boundary"}}},
					PropertyBeadsID:  {RichText: []RichText{{PlainText: "bd-2"}}},
					PropertyStatus:   {Select: &SelectOption{Name: "Open"}},
					PropertyPriority: {Select: &SelectOption{Name: "Medium"}},
					PropertyType:     {Select: &SelectOption{Name: "Task"}},
				},
			},
		},
	}
	tracker := &Tracker{client: api, config: DefaultMappingConfig(), dataSourceID: "ds_123"}

	issues, err := tracker.FetchIssues(context.Background(), itracker.FetchOptions{Since: &boundary})
	if err != nil {
		t.Fatalf("FetchIssues returned error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %d, want 1", len(issues))
	}
	if issues[0].Title != "After boundary" {
		t.Fatalf("title = %q", issues[0].Title)
	}
	queried, candidates := tracker.LastPullStats()
	if queried != 2 || candidates != 1 {
		t.Fatalf("LastPullStats = (%d, %d), want (2, 1)", queried, candidates)
	}
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
