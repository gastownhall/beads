package gitlab

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	tracker.Register("gitlab", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

// issueIIDPattern matches GitLab issue URLs: .../issues/42
var issueIIDPattern = regexp.MustCompile(`/issues/(\d+)`)

// Tracker implements tracker.IssueTracker for GitLab.
type Tracker struct {
	client *Client
	config *MappingConfig
	store  *dolt.DoltStore
}

func (t *Tracker) Name() string         { return "gitlab" }
func (t *Tracker) DisplayName() string  { return "GitLab" }
func (t *Tracker) ConfigPrefix() string { return "gitlab" }

func (t *Tracker) Init(ctx context.Context, store *dolt.DoltStore) error {
	t.store = store

	token, err := t.getConfig(ctx, "gitlab.token", "GITLAB_TOKEN")
	if err != nil || token == "" {
		return fmt.Errorf("GitLab token not configured (set gitlab.token or GITLAB_TOKEN)")
	}

	baseURL, _ := t.getConfig(ctx, "gitlab.url", "GITLAB_URL")
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}

	projectID, err := t.getConfig(ctx, "gitlab.project_id", "GITLAB_PROJECT_ID")
	if err != nil || projectID == "" {
		return fmt.Errorf("GitLab project ID not configured (set gitlab.project_id or GITLAB_PROJECT_ID)")
	}

	t.client = NewClient(token, baseURL, projectID)
	t.config = DefaultMappingConfig()
	return nil
}

func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("GitLab tracker not initialized")
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
	// GitLab uses "opened" not "open"
	if state == "open" {
		state = "opened"
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
	for _, gl := range issues {
		result = append(result, gitlabToTrackerIssue(&gl))
	}
	return result, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*tracker.TrackerIssue, error) {
	iid, err := strconv.Atoi(identifier)
	if err != nil {
		return nil, fmt.Errorf("invalid GitLab IID %q: %w", identifier, err)
	}

	gl, err := t.client.FetchIssueByIID(ctx, iid)
	if err != nil {
		return nil, err
	}
	if gl == nil {
		return nil, nil
	}

	ti := gitlabToTrackerIssue(gl)
	return &ti, nil
}

func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*tracker.TrackerIssue, error) {
	fields := BeadsIssueToGitLabFields(issue, t.config)
	labels, _ := fields["labels"].([]string)

	created, err := t.client.CreateIssue(ctx, issue.Title, issue.Description, labels)
	if err != nil {
		return nil, err
	}

	ti := gitlabToTrackerIssue(created)
	return &ti, nil
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	iid, err := strconv.Atoi(externalID)
	if err != nil {
		return nil, fmt.Errorf("invalid GitLab IID %q: %w", externalID, err)
	}

	updates := BeadsIssueToGitLabFields(issue, t.config)
	updated, err := t.client.UpdateIssue(ctx, iid, updates)
	if err != nil {
		return nil, err
	}

	ti := gitlabToTrackerIssue(updated)
	return &ti, nil
}

func (t *Tracker) FieldMapper() tracker.FieldMapper {
	return &gitlabFieldMapper{config: t.config}
}

func (t *Tracker) IsExternalRef(ref string) bool {
	return strings.Contains(ref, "gitlab") && issueIIDPattern.MatchString(ref)
}

func (t *Tracker) ExtractIdentifier(ref string) string {
	matches := issueIIDPattern.FindStringSubmatch(ref)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func (t *Tracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	if issue.URL != "" {
		return issue.URL
	}
	return fmt.Sprintf("gitlab:%s", issue.Identifier)
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

// gitlabToTrackerIssue converts a gitlab.Issue to a tracker.TrackerIssue.
func gitlabToTrackerIssue(gl *Issue) tracker.TrackerIssue {
	ti := tracker.TrackerIssue{
		ID:          strconv.Itoa(gl.ID),
		Identifier:  strconv.Itoa(gl.IID),
		URL:         gl.WebURL,
		Title:       gl.Title,
		Description: gl.Description,
		Labels:      gl.Labels,
		Raw:         gl,
	}

	if gl.State != "" {
		ti.State = gl.State
	}

	if gl.Assignee != nil {
		ti.Assignee = gl.Assignee.Username
		ti.AssigneeID = strconv.Itoa(gl.Assignee.ID)
	}

	if gl.CreatedAt != nil {
		ti.CreatedAt = *gl.CreatedAt
	}
	if gl.UpdatedAt != nil {
		ti.UpdatedAt = *gl.UpdatedAt
	}
	if gl.ClosedAt != nil {
		ti.CompletedAt = gl.ClosedAt
	}

	return ti
}
