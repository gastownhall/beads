package notion

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

type notionAPI interface {
	GetCurrentUser(ctx context.Context) (*User, error)
	RetrieveDataSource(ctx context.Context, dataSourceID string) (*DataSource, error)
	QueryDataSource(ctx context.Context, dataSourceID string) ([]Page, error)
	CreatePage(ctx context.Context, dataSourceID string, properties map[string]interface{}) (*Page, error)
	UpdatePage(ctx context.Context, pageID string, properties map[string]interface{}) (*Page, error)
	ArchivePage(ctx context.Context, pageID string, inTrash bool) (*Page, error)
}

var newNotionClient = func(token string) notionAPI {
	return NewClient(token)
}

func init() {
	itracker.Register("notion", func() itracker.IssueTracker {
		return &Tracker{}
	})
}

type Tracker struct {
	client       notionAPI
	store        storage.Storage
	config       *MappingConfig
	dataSourceID string
	viewURL      string
}

func (t *Tracker) Name() string         { return "notion" }
func (t *Tracker) DisplayName() string  { return "Notion" }
func (t *Tracker) ConfigPrefix() string { return "notion" }

func (t *Tracker) Init(ctx context.Context, store storage.Storage) error {
	t.store = store
	t.dataSourceID = t.getConfig(ctx, "notion.data_source_id", "NOTION_DATA_SOURCE_ID")
	t.viewURL = t.getConfig(ctx, "notion.view_url", "NOTION_VIEW_URL")
	token := t.getConfig(ctx, "notion.token", "NOTION_TOKEN")

	if token == "" {
		return fmt.Errorf("Notion token not configured (set notion.token or NOTION_TOKEN)")
	}
	if t.dataSourceID == "" {
		return fmt.Errorf("Notion data source not configured (set notion.data_source_id or NOTION_DATA_SOURCE_ID)")
	}
	if t.client == nil {
		t.client = newNotionClient(token)
	}
	if t.config == nil {
		t.config = DefaultMappingConfig()
	}
	return nil
}

func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("Notion tracker not initialized")
	}
	_, err := t.client.RetrieveDataSource(context.Background(), t.dataSourceID)
	if err != nil {
		return fmt.Errorf("Notion validation failed: %w", err)
	}
	return nil
}

func (t *Tracker) Close() error { return nil }

func (t *Tracker) FetchIssues(ctx context.Context, opts itracker.FetchOptions) ([]itracker.TrackerIssue, error) {
	pages, err := t.client.QueryDataSource(ctx, t.dataSourceID)
	if err != nil {
		return nil, err
	}
	issues := make([]itracker.TrackerIssue, 0, len(pages))
	for _, page := range pages {
		if page.InTrash || page.Archived {
			continue
		}
		pulled := PulledIssueFromPage(page)
		trackerIssue, err := TrackerIssueFromPullIssue(pulled, t.config)
		if err != nil {
			return nil, err
		}
		if !matchesFetchOptions(trackerIssue, opts) {
			continue
		}
		issues = append(issues, *trackerIssue)
		if opts.Limit > 0 && len(issues) >= opts.Limit {
			break
		}
	}
	return issues, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*itracker.TrackerIssue, error) {
	issues, err := t.FetchIssues(ctx, itracker.FetchOptions{State: "all"})
	if err != nil {
		return nil, err
	}
	want := ExtractNotionIdentifier(identifier)
	if want == "" {
		want = strings.TrimSpace(identifier)
	}
	for i := range issues {
		candidate := issues[i]
		if candidate.ID == want || candidate.Identifier == want {
			return &candidate, nil
		}
		if candidate.URL != "" && ExtractNotionIdentifier(candidate.URL) == want {
			return &candidate, nil
		}
	}
	return nil, nil
}

func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*itracker.TrackerIssue, error) {
	pushIssue, err := PushIssueFromIssue(issue, t.config)
	if err != nil {
		return nil, err
	}
	page, err := t.client.CreatePage(ctx, t.dataSourceID, BuildPageProperties(pushIssue))
	if err != nil {
		return nil, err
	}
	return TrackerIssueFromPullIssue(PulledIssueFromPage(*page), t.config)
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*itracker.TrackerIssue, error) {
	pageID := ExtractNotionIdentifier(externalID)
	if pageID == "" && issue != nil && issue.ExternalRef != nil {
		pageID = ExtractNotionIdentifier(*issue.ExternalRef)
	}
	if pageID == "" {
		return nil, fmt.Errorf("invalid Notion page ID %q", externalID)
	}
	pushIssue, err := PushIssueFromIssue(issue, t.config)
	if err != nil {
		return nil, err
	}
	page, err := t.client.UpdatePage(ctx, pageID, BuildPageProperties(pushIssue))
	if err != nil {
		return nil, err
	}
	return TrackerIssueFromPullIssue(PulledIssueFromPage(*page), t.config)
}

func (t *Tracker) FieldMapper() itracker.FieldMapper {
	return NewFieldMapper(t.config)
}

func (t *Tracker) IsExternalRef(ref string) bool {
	return IsNotionExternalRef(ref)
}

func (t *Tracker) ExtractIdentifier(ref string) string {
	return ExtractNotionIdentifier(ref)
}

func (t *Tracker) BuildExternalRef(issue *itracker.TrackerIssue) string {
	if issue == nil {
		return ""
	}
	if canonical, ok := CanonicalizeNotionPageURL(issue.URL); ok {
		return canonical
	}
	if canonical, ok := CanonicalizeNotionPageURL(issue.ID); ok {
		return canonical
	}
	if canonical, ok := CanonicalizeNotionPageURL(issue.Identifier); ok {
		return canonical
	}
	return ""
}

func (t *Tracker) getConfig(ctx context.Context, key, envVar string) string {
	if t.store != nil {
		if value, err := t.store.GetConfig(ctx, key); err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if envVar != "" {
		return strings.TrimSpace(os.Getenv(envVar))
	}
	return ""
}

func matchesFetchOptions(issue *itracker.TrackerIssue, opts itracker.FetchOptions) bool {
	if issue == nil {
		return false
	}
	if opts.Since != nil && !issue.UpdatedAt.IsZero() && issue.UpdatedAt.Before(*opts.Since) {
		return false
	}
	switch strings.TrimSpace(strings.ToLower(opts.State)) {
	case "", "all":
		return true
	case "open":
		status, _ := issue.State.(types.Status)
		return status != types.StatusClosed
	case "closed":
		status, _ := issue.State.(types.Status)
		return status == types.StatusClosed
	default:
		return true
	}
}
