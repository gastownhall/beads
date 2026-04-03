package gitlab

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	tracker.Register("gitlab", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

// issueIIDPattern matches GitLab issue URLs: .../issues/42 or .../work_items/42
var issueIIDPattern = regexp.MustCompile(`/(?:issues|work_items)/(\d+)`)

// glShorthandPattern matches the "gitlab:{digits}" shorthand produced by BuildExternalRef
// when a full URL is unavailable.
var glShorthandPattern = regexp.MustCompile(`^gitlab:([1-9]\d*)$`)

// milestoneIDPattern matches GitLab milestone URLs: .../-/milestones/5
var milestoneIDPattern = regexp.MustCompile(`/-/milestones/(\d+)`)

// Tracker implements tracker.IssueTracker for GitLab.
type Tracker struct {
	client      *Client
	config      *MappingConfig
	store       storage.Storage
	filter      *IssueFilter // Optional filters for issue fetching
	projectPath string       // GitLab project path (e.g., "socwave/socwave") for GraphQL
}

func (t *Tracker) Name() string         { return "gitlab" }
func (t *Tracker) DisplayName() string  { return "GitLab" }
func (t *Tracker) ConfigPrefix() string { return "gitlab" }

func (t *Tracker) Init(ctx context.Context, store storage.Storage) error {
	t.store = store

	token, err := t.getConfig(ctx, "gitlab.token", "GITLAB_TOKEN")
	if err != nil || token == "" {
		return fmt.Errorf("GitLab token not configured (set gitlab.token or GITLAB_TOKEN)")
	}

	baseURL, _ := t.getConfig(ctx, "gitlab.url", "GITLAB_URL")
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}

	projectID, _ := t.getConfig(ctx, "gitlab.project_id", "GITLAB_PROJECT_ID")
	groupID, _ := t.getConfig(ctx, "gitlab.group_id", "GITLAB_GROUP_ID")
	defaultProjectID, _ := t.getConfig(ctx, "gitlab.default_project_id", "GITLAB_DEFAULT_PROJECT_ID")

	// When group_id is set, default_project_id is used for creating issues.
	// When group_id is not set, project_id is required.
	if groupID == "" && projectID == "" {
		return fmt.Errorf("GitLab project ID not configured (set gitlab.project_id or GITLAB_PROJECT_ID)")
	}

	// For group mode, use default_project_id as the project for creating issues.
	// If default_project_id is not set, fall back to project_id.
	if groupID != "" && projectID == "" {
		if defaultProjectID != "" {
			projectID = defaultProjectID
		}
	}

	t.client = NewClient(token, baseURL, projectID)
	if groupID != "" {
		t.client = t.client.WithGroupID(groupID)
	}
	t.config = DefaultMappingConfig()

	// Load project path for GraphQL (e.g., "socwave/socwave")
	t.projectPath, _ = t.getConfig(ctx, "gitlab.project_path", "GITLAB_PROJECT_PATH")

	// Load optional filter config
	t.filter = t.loadFilterConfig(ctx)

	return nil
}

// loadFilterConfig reads filter configuration from store/env.
// Returns nil if no filters are configured.
func (t *Tracker) loadFilterConfig(ctx context.Context) *IssueFilter {
	labels, _ := t.getConfig(ctx, "gitlab.filter_labels", "GITLAB_FILTER_LABELS")
	projectStr, _ := t.getConfig(ctx, "gitlab.filter_project", "GITLAB_FILTER_PROJECT")
	milestone, _ := t.getConfig(ctx, "gitlab.filter_milestone", "GITLAB_FILTER_MILESTONE")
	assignee, _ := t.getConfig(ctx, "gitlab.filter_assignee", "GITLAB_FILTER_ASSIGNEE")

	if labels == "" && projectStr == "" && milestone == "" && assignee == "" {
		return nil
	}

	filter := &IssueFilter{
		Labels:    labels,
		Milestone: milestone,
		Assignee:  assignee,
	}
	if projectStr != "" {
		if pid, err := strconv.Atoi(projectStr); err == nil {
			filter.ProjectID = pid
		}
	}
	return filter
}

// SetFilter overrides the tracker's issue filter.
// CLI flags use this to override config-based defaults.
func (t *Tracker) SetFilter(filter *IssueFilter) {
	t.filter = filter
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
		issues, err = t.client.FetchIssuesSince(ctx, state, *opts.Since, t.filter)
	} else {
		issues, err = t.client.FetchIssues(ctx, state, t.filter)
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
	// Epic → milestone
	if issue.IssueType == types.TypeEpic {
		return t.createMilestone(ctx, issue)
	}

	// Task with a story/feature parent → GitLab Task work item (child of parent Issue)
	if issue.IssueType == types.TypeTask && t.projectPath != "" && t.store != nil {
		if parentGID := t.findParentStoryGID(ctx, issue.ID); parentGID != "" {
			return t.createTaskWorkItem(ctx, issue, parentGID)
		}
	}

	fields := BeadsIssueToGitLabFields(issue, t.config)
	labels, _ := fields["labels"].([]string)

	created, err := t.client.CreateIssue(ctx, issue.Title, issue.Description, labels)
	if err != nil {
		return nil, err
	}

	// Assign milestone from parent epic if one exists
	if t.store != nil {
		if milestoneID := t.findParentEpicMilestone(ctx, issue.ID); milestoneID > 0 {
			_, _ = t.client.UpdateIssue(ctx, created.IID, map[string]interface{}{
				"milestone_id": milestoneID,
			})
		}
	}

	ti := gitlabToTrackerIssue(created)
	return &ti, nil
}

func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	// Epic → milestone
	if issue.IssueType == types.TypeEpic {
		return t.updateMilestone(ctx, externalID, issue)
	}

	iid, err := strconv.Atoi(externalID)
	if err != nil {
		return nil, fmt.Errorf("invalid GitLab IID %q: %w", externalID, err)
	}

	updates := BeadsIssueToGitLabFields(issue, t.config)

	// Assign milestone from parent epic if one exists
	if t.store != nil {
		if milestoneID := t.findParentEpicMilestone(ctx, issue.ID); milestoneID > 0 {
			updates["milestone_id"] = milestoneID
		}
	}

	updated, err := t.client.UpdateIssue(ctx, iid, updates)
	if err != nil {
		return nil, err
	}

	ti := gitlabToTrackerIssue(updated)
	return &ti, nil
}

// createMilestone creates a GitLab milestone for an epic bead.
func (t *Tracker) createMilestone(ctx context.Context, issue *types.Issue) (*tracker.TrackerIssue, error) {
	ms, err := t.client.CreateMilestone(ctx, issue.Title, issue.Description)
	if err != nil {
		return nil, fmt.Errorf("creating milestone for epic: %w", err)
	}
	return milestoneToTrackerIssue(ms), nil
}

// updateMilestone updates a GitLab milestone for an epic bead.
func (t *Tracker) updateMilestone(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	mid, err := strconv.Atoi(externalID)
	if err != nil {
		return nil, fmt.Errorf("invalid milestone ID %q: %w", externalID, err)
	}

	updates := map[string]interface{}{
		"title":       issue.Title,
		"description": issue.Description,
	}
	if issue.Status == types.StatusClosed {
		updates["state_event"] = "close"
	} else {
		updates["state_event"] = "activate"
	}

	ms, err := t.client.UpdateMilestone(ctx, mid, updates)
	if err != nil {
		return nil, fmt.Errorf("updating milestone for epic: %w", err)
	}
	return milestoneToTrackerIssue(ms), nil
}

// findParentEpicMilestone walks up the parent-child chain from an issue
// to find an ancestor epic, then returns its GitLab milestone API ID.
// Returns 0 if no epic ancestor or no milestone found.
func (t *Tracker) findParentEpicMilestone(ctx context.Context, issueID string) int {
	// Walk up parent chain (max 5 levels to prevent loops)
	currentID := issueID
	for i := 0; i < 5; i++ {
		deps, err := t.store.GetDependenciesWithMetadata(ctx, currentID)
		if err != nil {
			return 0
		}
		foundParent := false
		for _, dep := range deps {
			if dep.DependencyType != types.DepParentChild {
				continue
			}
			if dep.Issue.IssueType == types.TypeEpic {
				// Found the epic — extract milestone IID from its external ref
				ref := ""
				if dep.Issue.ExternalRef != nil {
					ref = *dep.Issue.ExternalRef
				}
				if ref == "" {
					return 0
				}
				matches := milestoneIDPattern.FindStringSubmatch(ref)
				if len(matches) < 2 {
					return 0
				}
				milestoneIID, err := strconv.Atoi(matches[1])
				if err != nil {
					return 0
				}
				// Look up API ID by IID (single API call instead of fetching all milestones)
				ms, err := t.client.FetchMilestoneByIID(ctx, milestoneIID)
				if err != nil || ms == nil {
					return 0
				}
				return ms.ID
			}
			// Not an epic — keep walking up
			currentID = dep.Issue.ID
			foundParent = true
			break
		}
		if !foundParent {
			return 0
		}
	}
	return 0
}

// createTaskWorkItem creates a GitLab Task work item as a child of a parent Issue.
func (t *Tracker) createTaskWorkItem(ctx context.Context, issue *types.Issue, parentGID string) (*tracker.TrackerIssue, error) {
	wi, err := t.client.CreateTaskWorkItem(ctx, t.projectPath, issue.Title, issue.Description, parentGID)
	if err != nil {
		return nil, fmt.Errorf("creating task work item: %w", err)
	}

	// Build URL from project path and IID
	webURL := fmt.Sprintf("%s/%s/-/work_items/%s", t.client.BaseURL, t.projectPath, wi.IID)

	// Also set milestone if there's a grandparent epic
	if milestoneID := t.findParentEpicMilestone(ctx, issue.ID); milestoneID > 0 {
		iid, _ := strconv.Atoi(wi.IID)
		if iid > 0 {
			_, _ = t.client.UpdateIssue(ctx, iid, map[string]interface{}{
				"milestone_id": milestoneID,
			})
		}
	}

	return &tracker.TrackerIssue{
		ID:         wi.ID,
		Identifier: wi.IID,
		URL:        webURL,
		Title:      wi.Title,
	}, nil
}

// findParentStoryGID finds the GitLab global ID (gid://...) of a parent story/feature.
// Returns empty string if no story/feature parent or if the parent isn't synced to GitLab.
func (t *Tracker) findParentStoryGID(ctx context.Context, issueID string) string {
	deps, err := t.store.GetDependenciesWithMetadata(ctx, issueID)
	if err != nil {
		return ""
	}
	for _, dep := range deps {
		if dep.DependencyType != types.DepParentChild {
			continue
		}
		// Only match story/feature parents (not epics — those become milestones)
		if dep.Issue.IssueType == types.TypeEpic {
			continue
		}
		ref := ""
		if dep.Issue.ExternalRef != nil {
			ref = *dep.Issue.ExternalRef
		}
		if ref == "" {
			continue
		}
		// Extract IID from the issue URL and look up the GID
		matches := issueIIDPattern.FindStringSubmatch(ref)
		if len(matches) < 2 {
			continue
		}
		iid, err := strconv.Atoi(matches[1])
		if err != nil {
			continue
		}
		gid, err := t.client.GetWorkItemGID(ctx, t.projectPath, iid)
		if err != nil {
			continue
		}
		return gid
	}
	return ""
}

// milestoneToTrackerIssue converts a GitLab Milestone to a TrackerIssue.
func milestoneToTrackerIssue(ms *Milestone) *tracker.TrackerIssue {
	return &tracker.TrackerIssue{
		ID:         strconv.Itoa(ms.ID),
		Identifier: strconv.Itoa(ms.ID),
		URL:        ms.WebURL,
		Title:      ms.Title,
	}
}

func (t *Tracker) FieldMapper() tracker.FieldMapper {
	return &gitlabFieldMapper{config: t.config}
}

// IsExternalRef checks if a ref belongs to this GitLab tracker.
// It recognizes both full GitLab URLs and the "gitlab:{id}" shorthand format
// produced by BuildExternalRef when a URL is unavailable.
func (t *Tracker) IsExternalRef(ref string) bool {
	if glShorthandPattern.MatchString(ref) {
		return true
	}
	if !strings.Contains(ref, "gitlab") && !strings.Contains(ref, "milestones") {
		return false
	}
	return issueIIDPattern.MatchString(ref) || milestoneIDPattern.MatchString(ref)
}

// ExtractIdentifier extracts the issue IID from a GitLab URL or shorthand ref.
func (t *Tracker) ExtractIdentifier(ref string) string {
	if m := glShorthandPattern.FindStringSubmatch(ref); len(m) >= 2 {
		return m[1]
	}
	// Try milestone pattern first (more specific path)
	if matches := milestoneIDPattern.FindStringSubmatch(ref); len(matches) >= 2 {
		return matches[1]
	}
	// Fall back to issue pattern
	if matches := issueIIDPattern.FindStringSubmatch(ref); len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// IsMilestoneRef checks if an external_ref points to a milestone (not an issue).
func (t *Tracker) IsMilestoneRef(ref string) bool {
	return milestoneIDPattern.MatchString(ref)
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
