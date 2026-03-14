package github

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/gastownhall/beads/internal/storage"
	"github.com/gastownhall/beads/internal/tracker"
	"github.com/gastownhall/beads/internal/types"
)

func init() {
	tracker.Register("github", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

// issueNumberPattern matches GitHub issue URLs: .../issues/42
var issueNumberPattern = regexp.MustCompile(`/issues/(\d+)`)

// Tracker implements tracker.IssueTracker for GitHub.
type Tracker struct {
	client *Client
	config *MappingConfig
	store  storage.Storage
}

func (t *Tracker) Name() string         { return "github" }
func (t *Tracker) DisplayName() string  { return "GitHub" }
func (t *Tracker) ConfigPrefix() string { return "github" }

func (t *Tracker) Init(ctx context.Context, store storage.Storage) error {
	t.store = store

	token := t.getConfig(ctx, "github.token", "GITHUB_TOKEN")
	if token == "" {
		return fmt.Errorf("GitHub token not configured (set github.token or GITHUB_TOKEN)")
	}

	owner := t.getConfig(ctx, "github.owner", "GITHUB_OWNER")
	repo := t.getConfig(ctx, "github.repo", "GITHUB_REPO")

	// Try combined owner/repo format: "owner/repo"
	if owner == "" || repo == "" {
		ownerRepo := t.getConfig(ctx, "github.repository", "GITHUB_REPOSITORY")
		if ownerRepo != "" {
			parts := strings.SplitN(ownerRepo, "/", 2)
			if len(parts) == 2 {
				owner = parts[0]
				repo = parts[1]
			}
		}
	}

	if owner == "" {
		return fmt.Errorf("GitHub owner not configured (set github.owner or GITHUB_OWNER)")
	}
	if repo == "" {
		return fmt.Errorf("GitHub repo not configured (set github.repo or GITHUB_REPO)")
	}

	t.client = NewClient(token, owner, repo)

	// Allow custom base URL for GitHub Enterprise
	baseURL := t.getConfig(ctx, "github.url", "GITHUB_API_URL")
	if baseURL != "" {
		t.client = t.client.WithBaseURL(baseURL)
	}

	t.config = DefaultMappingConfig()
	return nil
}

func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("GitHub tracker not initialized")
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
	for _, gh := range issues {
		result = append(result, githubToTrackerIssue(&gh))
	}
	return result, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*tracker.TrackerIssue, error) {
	number, err := strconv.Atoi(identifier)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub issue number %q: %w", identifier, err)
	}

	gh, err := t.client.FetchIssueByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	if gh == nil {
		return nil, nil
	}

	ti := githubToTrackerIssue(gh)
	return &ti, nil
}

func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*tracker.TrackerIssue, error) {
	fields := BeadsIssueToGitHubFields(issue, t.config)
	labels, _ := fields["labels"].([]string)

	created, err := t.client.CreateIssue(ctx, issue.Title, issue.Description, labels)
	if err != nil {
		return nil, err
	}

	ti := githubToTrackerIssue(created)
	return &ti, nil
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	number, err := strconv.Atoi(externalID)
	if err != nil {
		return nil, fmt.Errorf("invalid GitHub issue number %q: %w", externalID, err)
	}

	updates := BeadsIssueToGitHubFields(issue, t.config)
	updated, err := t.client.UpdateIssue(ctx, number, updates)
	if err != nil {
		return nil, err
	}

	ti := githubToTrackerIssue(updated)
	return &ti, nil
}

func (t *Tracker) FieldMapper() tracker.FieldMapper {
	return &githubFieldMapper{config: t.config}
}

func (t *Tracker) IsExternalRef(ref string) bool {
	return strings.Contains(ref, "github.com") && issueNumberPattern.MatchString(ref)
}

func (t *Tracker) ExtractIdentifier(ref string) string {
	matches := issueNumberPattern.FindStringSubmatch(ref)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

func (t *Tracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	if issue.URL != "" {
		return issue.URL
	}
	return fmt.Sprintf("github:%s", issue.Identifier)
}

// getConfig reads a config value from storage, falling back to env var.
func (t *Tracker) getConfig(ctx context.Context, key, envVar string) string {
	val, err := t.store.GetConfig(ctx, key)
	if err == nil && val != "" {
		return val
	}
	if envVar != "" {
		if envVal := os.Getenv(envVar); envVal != "" {
			return envVal
		}
	}
	return ""
}

// githubToTrackerIssue converts a github.Issue to a tracker.TrackerIssue.
func githubToTrackerIssue(gh *Issue) tracker.TrackerIssue {
	ti := tracker.TrackerIssue{
		ID:          strconv.Itoa(gh.ID),
		Identifier:  strconv.Itoa(gh.Number),
		URL:         gh.HTMLURL,
		Title:       gh.Title,
		Description: gh.Body,
		Labels:      gh.LabelNames(),
		Raw:         gh,
	}

	if gh.State != "" {
		ti.State = gh.State
	}

	if gh.Assignee != nil {
		ti.Assignee = gh.Assignee.Login
		ti.AssigneeID = strconv.Itoa(gh.Assignee.ID)
	}

	if gh.CreatedAt != nil {
		ti.CreatedAt = *gh.CreatedAt
	}
	if gh.UpdatedAt != nil {
		ti.UpdatedAt = *gh.UpdatedAt
	}
	if gh.ClosedAt != nil {
		ti.CompletedAt = gh.ClosedAt
	}

	return ti
}
