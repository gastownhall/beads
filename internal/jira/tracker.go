package jira

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	tracker.Register("jira", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

// Tracker implements tracker.IssueTracker for Jira.
type Tracker struct {
	client     *Client
	store      *dolt.DoltStore
	jiraURL    string
	projectKey string
}

func (t *Tracker) Name() string         { return "jira" }
func (t *Tracker) DisplayName() string  { return "Jira" }
func (t *Tracker) ConfigPrefix() string { return "jira" }

func (t *Tracker) Init(ctx context.Context, store *dolt.DoltStore) error {
	t.store = store

	jiraURL, err := t.getConfig(ctx, "jira.url", "JIRA_URL")
	if err != nil || jiraURL == "" {
		return fmt.Errorf("Jira URL not configured (set jira.url or JIRA_URL)")
	}
	t.jiraURL = jiraURL

	projectKey, err := t.getConfig(ctx, "jira.project", "JIRA_PROJECT")
	if err != nil || projectKey == "" {
		return fmt.Errorf("Jira project not configured (set jira.project or JIRA_PROJECT)")
	}
	t.projectKey = projectKey

	username, _ := t.getConfig(ctx, "jira.username", "JIRA_USERNAME")
	apiToken, err := t.getConfig(ctx, "jira.api_token", "JIRA_API_TOKEN")
	if err != nil || apiToken == "" {
		return fmt.Errorf("Jira API token not configured (set jira.api_token or JIRA_API_TOKEN)")
	}

	t.client = NewClient(jiraURL, username, apiToken)
	return nil
}

func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("Jira tracker not initialized")
	}
	return nil
}

func (t *Tracker) Close() error { return nil }

func (t *Tracker) FetchIssues(ctx context.Context, opts tracker.FetchOptions) ([]tracker.TrackerIssue, error) {
	// Build JQL query
	jql := fmt.Sprintf("project = %q", t.projectKey)

	// State filter
	switch opts.State {
	case "open":
		jql += " AND statusCategory != Done"
	case "closed":
		jql += " AND statusCategory = Done"
	}

	// Incremental sync
	if opts.Since != nil {
		jql += fmt.Sprintf(" AND updated >= %q", opts.Since.Format("2006-01-02 15:04"))
	}

	jql += " ORDER BY updated DESC"

	issues, err := t.client.SearchIssues(ctx, jql)
	if err != nil {
		return nil, err
	}

	result := make([]tracker.TrackerIssue, 0, len(issues))
	for i := range issues {
		result = append(result, jiraToTrackerIssue(&issues[i]))
	}
	return result, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*tracker.TrackerIssue, error) {
	issue, err := t.client.GetIssue(ctx, identifier)
	if err != nil {
		return nil, err
	}
	if issue == nil {
		return nil, nil
	}
	ti := jiraToTrackerIssue(issue)
	return &ti, nil
}

func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*tracker.TrackerIssue, error) {
	mapper := t.FieldMapper()
	fields := mapper.IssueToTracker(issue)

	// Set project
	fields["project"] = map[string]string{"key": t.projectKey}

	created, err := t.client.CreateIssue(ctx, fields)
	if err != nil {
		return nil, err
	}

	ti := jiraToTrackerIssue(created)
	return &ti, nil
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	mapper := t.FieldMapper()
	fields := mapper.IssueToTracker(issue)

	if err := t.client.UpdateIssue(ctx, externalID, fields); err != nil {
		return nil, err
	}

	// Fetch the updated issue to return current state
	updated, err := t.client.GetIssue(ctx, externalID)
	if err != nil {
		return nil, err
	}
	ti := jiraToTrackerIssue(updated)
	return &ti, nil
}

func (t *Tracker) FieldMapper() tracker.FieldMapper {
	return &jiraFieldMapper{}
}

func (t *Tracker) IsExternalRef(ref string) bool {
	return IsJiraExternalRef(ref, t.jiraURL)
}

func (t *Tracker) ExtractIdentifier(ref string) string {
	return ExtractJiraKey(ref)
}

func (t *Tracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	return fmt.Sprintf("%s/browse/%s", t.jiraURL, issue.Identifier)
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

// jiraToTrackerIssue converts a Jira API Issue to the generic TrackerIssue format.
func jiraToTrackerIssue(ji *Issue) tracker.TrackerIssue {
	ti := tracker.TrackerIssue{
		ID:         ji.ID,
		Identifier: ji.Key,
		URL:        ji.Self,
		Title:      ji.Fields.Summary,
		Labels:     ji.Fields.Labels,
		Raw:        ji,
	}

	// Description: convert ADF to plain text
	ti.Description = DescriptionToPlainText(ji.Fields.Description)

	// Priority
	if ji.Fields.Priority != nil {
		ti.Priority = jiraPriorityToNumeric(ji.Fields.Priority.Name)
	}

	// State
	if ji.Fields.Status != nil {
		ti.State = ji.Fields.Status.Name
	}

	// Type
	if ji.Fields.IssueType != nil {
		ti.Type = ji.Fields.IssueType.Name
	}

	// Assignee
	if ji.Fields.Assignee != nil {
		ti.Assignee = ji.Fields.Assignee.DisplayName
		ti.AssigneeEmail = ji.Fields.Assignee.EmailAddress
		ti.AssigneeID = ji.Fields.Assignee.AccountID
	}

	// Timestamps
	if t, err := ParseTimestamp(ji.Fields.Created); err == nil {
		ti.CreatedAt = t
	}
	if t, err := ParseTimestamp(ji.Fields.Updated); err == nil {
		ti.UpdatedAt = t
	}

	// Store Jira-specific metadata
	ti.Metadata = map[string]interface{}{
		"source_system": fmt.Sprintf("jira:%s:%s", projectKeyFromIssue(ji), ji.Key),
	}
	if ji.Fields.IssueType != nil {
		ti.Metadata["jira_type"] = ji.Fields.IssueType.Name
	}

	return ti
}

// jiraPriorityToNumeric converts a Jira priority name to a numeric value (0=highest, 4=lowest).
func jiraPriorityToNumeric(name string) int {
	switch strings.ToLower(name) {
	case "highest":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "low":
		return 3
	case "lowest":
		return 4
	default:
		return 2
	}
}

// projectKeyFromIssue extracts the project key from a Jira issue.
func projectKeyFromIssue(ji *Issue) string {
	if ji.Fields.Project != nil {
		return ji.Fields.Project.Key
	}
	// Fall back to extracting from issue key (e.g., "PROJ-123" → "PROJ")
	if idx := strings.LastIndex(ji.Key, "-"); idx > 0 {
		return ji.Key[:idx]
	}
	return ""
}
