package linear

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	tracker.Register("linear", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

// Tracker implements tracker.IssueTracker for Linear.
type Tracker struct {
	client    *Client
	config    *MappingConfig
	store     *dolt.DoltStore
	teamID    string
	projectID string
}

func (t *Tracker) Name() string         { return "linear" }
func (t *Tracker) DisplayName() string  { return "Linear" }
func (t *Tracker) ConfigPrefix() string { return "linear" }

func (t *Tracker) Init(ctx context.Context, store *dolt.DoltStore) error {
	t.store = store

	apiKey, err := t.getConfig(ctx, "linear.api_key", "LINEAR_API_KEY")
	if err != nil || apiKey == "" {
		return fmt.Errorf("Linear API key not configured (set linear.api_key or LINEAR_API_KEY)")
	}

	teamID, err := t.getConfig(ctx, "linear.team_id", "LINEAR_TEAM_ID")
	if err != nil || teamID == "" {
		return fmt.Errorf("Linear team ID not configured (set linear.team_id or LINEAR_TEAM_ID)")
	}
	t.teamID = teamID

	client := NewClient(apiKey, teamID)

	if endpoint, _ := store.GetConfig(ctx, "linear.api_endpoint"); endpoint != "" {
		client = client.WithEndpoint(endpoint)
	}
	if projectID, _ := store.GetConfig(ctx, "linear.project_id"); projectID != "" {
		client = client.WithProjectID(projectID)
		t.projectID = projectID
	}

	t.client = client
	t.config = LoadMappingConfig(&configLoaderAdapter{ctx: ctx, store: store})
	return nil
}

func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("Linear tracker not initialized")
	}
	return nil
}

func (t *Tracker) Close() error { return nil }

func (t *Tracker) FetchIssues(ctx context.Context, opts tracker.FetchOptions) ([]tracker.TrackerIssue, error) {
	var issues []Issue
	var err error

	state := opts.State
	if state == "" {
		state = "all"
	}

	if opts.Since != nil {
		issues, err = t.client.FetchIssuesSince(ctx, state, *opts.Since)
	} else {
		issues, err = t.client.FetchIssues(ctx, state)
	}
	if err != nil {
		return nil, err
	}

	result := make([]tracker.TrackerIssue, 0, len(issues))
	for _, li := range issues {
		result = append(result, linearToTrackerIssue(&li))
	}
	return result, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*tracker.TrackerIssue, error) {
	li, err := t.client.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if li == nil {
		return nil, nil
	}
	ti := linearToTrackerIssue(li)
	return &ti, nil
}

func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*tracker.TrackerIssue, error) {
	priority := PriorityToLinear(issue.Priority, t.config)

	stateID, err := t.findStateID(ctx, issue.Status)
	if err != nil {
		return nil, fmt.Errorf("finding state for status %s: %w", issue.Status, err)
	}

	created, err := t.client.CreateIssue(ctx, issue.Title, issue.Description, priority, stateID, nil)
	if err != nil {
		return nil, err
	}

	ti := linearToTrackerIssue(created)
	return &ti, nil
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	mapper := t.FieldMapper()
	updates := mapper.IssueToTracker(issue)

	updated, err := t.client.UpdateIssue(ctx, externalID, updates)
	if err != nil {
		return nil, err
	}

	ti := linearToTrackerIssue(updated)
	return &ti, nil
}

func (t *Tracker) FieldMapper() tracker.FieldMapper {
	return &linearFieldMapper{config: t.config}
}

func (t *Tracker) IsExternalRef(ref string) bool {
	return IsLinearExternalRef(ref)
}

func (t *Tracker) ExtractIdentifier(ref string) string {
	return ExtractLinearIdentifier(ref)
}

func (t *Tracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	if issue.URL != "" {
		if canonical, ok := CanonicalizeLinearExternalRef(issue.URL); ok {
			return canonical
		}
		return issue.URL
	}
	return fmt.Sprintf("https://linear.app/issue/%s", issue.Identifier)
}

// findStateID looks up the Linear workflow state ID for a beads status.
func (t *Tracker) findStateID(ctx context.Context, status types.Status) (string, error) {
	targetType := StatusToLinearStateType(status)

	states, err := t.client.GetTeamStates(ctx)
	if err != nil {
		return "", err
	}

	for _, s := range states {
		if s.Type == targetType {
			return s.ID, nil
		}
	}

	if len(states) > 0 {
		return states[0].ID, nil
	}
	return "", fmt.Errorf("no workflow states found")
}

// getConfig reads a config value from storage, falling back to env var.
func (t *Tracker) getConfig(ctx context.Context, key, envVar string) (string, error) {
	val, err := t.store.GetConfig(ctx, key)
	if err == nil && val != "" {
		return val, nil
	}
	if envVar != "" {
		if envVal := os.Getenv(envVar); envVal != "" {
			return envVal, nil
		}
	}
	return "", nil
}

// linearToTrackerIssue converts a linear.Issue to a tracker.TrackerIssue.
func linearToTrackerIssue(li *Issue) tracker.TrackerIssue {
	ti := tracker.TrackerIssue{
		ID:          li.ID,
		Identifier:  li.Identifier,
		URL:         li.URL,
		Title:       li.Title,
		Description: li.Description,
		Priority:    li.Priority,
		Labels:      make([]string, 0),
		Raw:         li,
	}

	if li.State != nil {
		ti.State = li.State
	}

	if li.Labels != nil {
		for _, l := range li.Labels.Nodes {
			ti.Labels = append(ti.Labels, l.Name)
		}
	}

	if li.Assignee != nil {
		ti.Assignee = li.Assignee.Name
		ti.AssigneeEmail = li.Assignee.Email
		ti.AssigneeID = li.Assignee.ID
	}

	if li.Parent != nil {
		ti.ParentID = li.Parent.Identifier
		ti.ParentInternalID = li.Parent.ID
	}

	if t, err := time.Parse(time.RFC3339, li.CreatedAt); err == nil {
		ti.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339, li.UpdatedAt); err == nil {
		ti.UpdatedAt = t
	}
	if li.CompletedAt != "" {
		if t, err := time.Parse(time.RFC3339, li.CompletedAt); err == nil {
			ti.CompletedAt = &t
		}
	}

	return ti
}

// BuildStateCacheFromTracker builds a StateCache using the tracker's internal client.
// This allows CLI code to set up PushHooks.BuildStateCache without accessing the client directly.
func BuildStateCacheFromTracker(ctx context.Context, t *Tracker) (*StateCache, error) {
	if t.client == nil {
		return nil, fmt.Errorf("Linear tracker not initialized")
	}
	return BuildStateCache(ctx, t.client)
}

// configLoaderAdapter wraps *dolt.DoltStore to implement linear.ConfigLoader.
type configLoaderAdapter struct {
	ctx   context.Context
	store *dolt.DoltStore
}

func (c *configLoaderAdapter) GetAllConfig() (map[string]string, error) {
	return c.store.GetAllConfig(c.ctx)
}
