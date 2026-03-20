package notion

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	itracker "github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	itracker.Register("notion", func() itracker.IssueTracker {
		return NewTracker()
	})
}

type notionClient interface {
	Status(ctx context.Context, req StatusRequest) (*StatusResponse, error)
	Pull(ctx context.Context, req PullRequest) (*PullResponse, error)
	Push(ctx context.Context, req PushRequest) (*PushResponse, error)
}

// TrackerOption configures a Notion tracker.
type TrackerOption func(*Tracker)

// WithTrackerClient injects a Notion client, primarily for tests.
func WithTrackerClient(client notionClient) TrackerOption {
	return func(t *Tracker) {
		if client != nil {
			t.client = client
		}
	}
}

// WithTrackerConfig injects a mapping config.
func WithTrackerConfig(config *MappingConfig) TrackerOption {
	return func(t *Tracker) {
		if config != nil {
			t.config = config
		}
	}
}

// WithTrackerBinaryPath overrides the ncli binary path.
func WithTrackerBinaryPath(path string) TrackerOption {
	return func(t *Tracker) {
		if strings.TrimSpace(path) != "" {
			t.binaryPath = path
			t.binaryPathExplicit = true
		}
	}
}

// WithTrackerDatabaseID overrides the target database id.
func WithTrackerDatabaseID(databaseID string) TrackerOption {
	return func(t *Tracker) {
		if strings.TrimSpace(databaseID) != "" {
			t.databaseID = databaseID
			t.databaseIDExplicit = true
		}
	}
}

// WithTrackerViewURL overrides the target view url.
func WithTrackerViewURL(viewURL string) TrackerOption {
	return func(t *Tracker) {
		if strings.TrimSpace(viewURL) != "" {
			t.viewURL = viewURL
			t.viewURLExplicit = true
		}
	}
}

// Tracker implements itracker.IssueTracker for Notion via ncli beads commands.
type Tracker struct {
	client             notionClient
	config             *MappingConfig
	store              storage.Storage
	binaryPath         string
	binaryPathExplicit bool
	databaseID         string
	databaseIDExplicit bool
	viewURL            string
	viewURLExplicit    bool
	issueCache         []itracker.TrackerIssue
}

// NewTracker constructs a Notion tracker.
func NewTracker(opts ...TrackerOption) *Tracker {
	tracker := &Tracker{
		config:     DefaultMappingConfig(),
		binaryPath: DefaultBinaryPath,
	}
	for _, opt := range opts {
		opt(tracker)
	}
	return tracker
}

func (t *Tracker) Name() string         { return "notion" }
func (t *Tracker) DisplayName() string  { return "Notion" }
func (t *Tracker) ConfigPrefix() string { return "notion" }

func (t *Tracker) Init(ctx context.Context, store storage.Storage) error {
	t.store = store
	if t.client != nil {
		return nil
	}

	cfg := ResolveRuntimeConfig(ctx, store, RuntimeConfigInput{
		BinaryPath:    t.binaryPath,
		BinaryPathSet: t.binaryPathExplicit,
		DatabaseID:    t.databaseID,
		DatabaseIDSet: t.databaseIDExplicit,
		ViewURL:       t.viewURL,
		ViewURLSet:    t.viewURLExplicit,
	})
	t.binaryPath = cfg.BinaryPath
	t.databaseID = cfg.DatabaseID
	t.viewURL = cfg.ViewURL

	t.client = NewClient(WithBinaryPath(t.binaryPath))
	return nil
}

func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("Notion tracker not initialized")
	}
	return nil
}

func (t *Tracker) Close() error { return nil }

func (t *Tracker) FetchIssues(ctx context.Context, opts itracker.FetchOptions) ([]itracker.TrackerIssue, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if t.canUseIssueCache(opts) && t.issueCache != nil {
		return cloneTrackerIssues(t.issueCache), nil
	}

	resp, err := t.client.Pull(ctx, PullRequest{})
	if err != nil {
		return nil, err
	}

	result := make([]itracker.TrackerIssue, 0, len(resp.Issues))
	for _, issue := range resp.Issues {
		trackerIssue, err := TrackerIssueFromPullIssue(issue, t.config)
		if err != nil {
			return nil, err
		}
		if !matchesFetchOptions(trackerIssue, opts) {
			continue
		}
		result = append(result, *trackerIssue)
		if opts.Limit > 0 && len(result) >= opts.Limit {
			break
		}
	}
	if t.canUseIssueCache(opts) {
		t.issueCache = cloneTrackerIssues(result)
	}

	return result, nil
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
	payload, err := PushPayloadFromIssue(issue, t.config)
	if err != nil {
		return nil, err
	}
	body, err := MarshalPushPayload(payload)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.Push(ctx, PushRequest{
		DatabaseID: t.databaseID,
		ViewURL:    t.viewURL,
		Payload:    body,
	})
	if err != nil {
		return nil, err
	}

	created, err := t.issueFromPushResponse(resp, issue, "")
	if err != nil {
		return nil, err
	}
	if created != nil && strings.TrimSpace(t.BuildExternalRef(created)) != "" {
		t.upsertIssueCache(created)
		return created, nil
	}
	return t.fetchCreatedIssue(ctx, issue.ID, created)
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*itracker.TrackerIssue, error) {
	if issue == nil {
		return nil, fmt.Errorf("issue is nil")
	}

	clone := *issue
	if strings.TrimSpace(externalID) != "" {
		externalRef := "notion:" + externalID
		clone.ExternalRef = &externalRef
	}

	payload, err := PushPayloadFromIssue(&clone, t.config)
	if err != nil {
		return nil, err
	}
	body, err := MarshalPushPayload(payload)
	if err != nil {
		return nil, err
	}

	resp, err := t.client.Push(ctx, PushRequest{
		DatabaseID: t.databaseID,
		ViewURL:    t.viewURL,
		Payload:    body,
	})
	if err != nil {
		return nil, err
	}

	updated, err := t.issueFromPushResponse(resp, &clone, externalID)
	if err != nil {
		return nil, err
	}
	t.upsertIssueCache(updated)
	return updated, nil
}

func (t *Tracker) fetchCreatedIssue(ctx context.Context, issueID string, fallback *itracker.TrackerIssue) (*itracker.TrackerIssue, error) {
	issues, err := t.FetchIssues(ctx, itracker.FetchOptions{State: "all"})
	if err != nil {
		if fallback != nil {
			return fallback, nil
		}
		return nil, err
	}

	for i := range issues {
		if issues[i].Raw == nil {
			continue
		}
		pulled, ok := issues[i].Raw.(*PulledIssue)
		if !ok || pulled == nil {
			continue
		}
		if strings.TrimSpace(pulled.ID) == strings.TrimSpace(issueID) {
			return &issues[i], nil
		}
	}

	if fallback != nil {
		t.upsertIssueCache(fallback)
		return fallback, nil
	}
	return nil, nil
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
	if issue.ID != "" {
		if canonical, ok := CanonicalizeNotionPageURL(issue.ID); ok {
			return canonical
		}
	}
	if issue.Identifier != "" {
		if canonical, ok := CanonicalizeNotionPageURL(issue.Identifier); ok {
			return canonical
		}
	}
	return ""
}

// Archive/delete sync remains unsupported until the ncli/live MCP surface exposes a real archive operation.
func (t *Tracker) issueFromPushResponse(resp *PushResponse, issue *types.Issue, externalID string) (*itracker.TrackerIssue, error) {
	if resp == nil {
		return nil, fmt.Errorf("push response is nil")
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("push response reported %d error(s): %s", len(resp.Errors), summarizePushErrors(resp.Errors))
	}
	if len(resp.Updated) == 0 && len(resp.Created) == 0 {
		return nil, fmt.Errorf("push response did not include a created or updated result")
	}

	var item *PushResultItem
	if len(resp.Updated) > 0 {
		item = &resp.Updated[0]
	} else if len(resp.Created) > 0 {
		item = &resp.Created[0]
	}

	result := &itracker.TrackerIssue{
		Title:       issue.Title,
		Description: issue.Description,
		Priority:    issue.Priority,
		State:       issue.Status,
		Type:        issue.IssueType,
		Labels:      append([]string(nil), issue.Labels...),
		Assignee:    issue.Assignee,
		CreatedAt:   issue.CreatedAt,
		UpdatedAt:   issue.UpdatedAt,
		Raw:         issue,
	}

	if item != nil {
		result.ID = firstNonEmpty(item.NotionPageID, externalID)
		result.Identifier = ExtractNotionIdentifier(firstNonEmpty(item.ExternalRef, result.ID))
		result.URL = t.BuildExternalRef(&itracker.TrackerIssue{
			ID:         result.ID,
			Identifier: result.Identifier,
			URL:        item.ExternalRef,
		})
		if result.URL == "" {
			result.URL = item.ExternalRef
		}
		return result, nil
	}

	result.ID = externalID
	result.Identifier = ExtractNotionIdentifier(firstNonEmpty(externalID, issueExternalRef(issue)))
	result.URL = issueExternalRef(issue)
	return result, nil
}

func summarizePushErrors(errors []PushResultError) string {
	parts := make([]string, 0, len(errors))
	for _, item := range errors {
		part := strings.TrimSpace(item.Message)
		if item.Stage != "" {
			part = strings.TrimSpace(item.Stage + ": " + part)
		}
		if item.ID != "" {
			part = strings.TrimSpace(item.ID + " " + part)
		}
		if part == "" {
			part = "unknown push error"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

func (t *Tracker) canUseIssueCache(opts itracker.FetchOptions) bool {
	return opts.State == "all" && opts.Since == nil && opts.Limit == 0
}

func (t *Tracker) upsertIssueCache(issue *itracker.TrackerIssue) {
	if issue == nil || t.issueCache == nil {
		return
	}
	for i := range t.issueCache {
		if sameTrackerIssue(t.issueCache[i], *issue) {
			t.issueCache[i] = cloneTrackerIssue(*issue)
			return
		}
	}
	t.issueCache = append(t.issueCache, cloneTrackerIssue(*issue))
}

func sameTrackerIssue(left, right itracker.TrackerIssue) bool {
	leftIDs := []string{
		ExtractNotionIdentifier(left.ID),
		ExtractNotionIdentifier(left.Identifier),
		ExtractNotionIdentifier(left.URL),
	}
	rightIDs := []string{
		ExtractNotionIdentifier(right.ID),
		ExtractNotionIdentifier(right.Identifier),
		ExtractNotionIdentifier(right.URL),
	}
	for _, leftID := range leftIDs {
		if leftID == "" {
			continue
		}
		for _, rightID := range rightIDs {
			if rightID != "" && leftID == rightID {
				return true
			}
		}
	}
	return false
}

func cloneTrackerIssues(issues []itracker.TrackerIssue) []itracker.TrackerIssue {
	if issues == nil {
		return nil
	}
	cloned := make([]itracker.TrackerIssue, len(issues))
	for i := range issues {
		cloned[i] = cloneTrackerIssue(issues[i])
	}
	return cloned
}

func cloneTrackerIssue(issue itracker.TrackerIssue) itracker.TrackerIssue {
	cloned := issue
	if issue.Labels != nil {
		cloned.Labels = append([]string(nil), issue.Labels...)
	}
	return cloned
}

func matchesFetchOptions(issue *itracker.TrackerIssue, opts itracker.FetchOptions) bool {
	if issue == nil {
		return false
	}
	if opts.State != "" && opts.State != "all" {
		status, ok := issue.State.(types.Status)
		if !ok {
			return false
		}
		if opts.State == "open" && status == types.StatusClosed {
			return false
		}
		if opts.State == "closed" && status != types.StatusClosed {
			return false
		}
	}
	if opts.Since != nil && !issue.UpdatedAt.IsZero() && issue.UpdatedAt.Before(*opts.Since) {
		return false
	}
	return true
}

func issueExternalRef(issue *types.Issue) string {
	if issue == nil || issue.ExternalRef == nil {
		return ""
	}
	return *issue.ExternalRef
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
