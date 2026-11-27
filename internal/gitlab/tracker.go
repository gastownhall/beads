package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/endpointtrust"
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

const gitLabMilestoneIdentifierPrefix = "milestone:"

// Tracker implements tracker.IssueTracker for GitLab.
type Tracker struct {
	client      *Client
	config      *MappingConfig
	store       tracker.Store
	filter      *IssueFilter // Optional filters for issue fetching
	projectPath string       // GitLab project path (e.g., "socwave/socwave") for GraphQL
	// webURL is gitlab.web_url: the address GitLab puts in the web_url of
	// what it returns, when that is not the gitlab.url the API is reached at.
	// It is only compared with refs and never contacted.
	webURL string
	// projectAt maps a project path, keyed as MatchKey keys it without the
	// IID, to the project ID GitLab reported for an issue or link fetched at
	// that path. LocalIdentityKey reads it.
	projectAt map[string]int
}

func (t *Tracker) Name() string         { return "gitlab" }
func (t *Tracker) DisplayName() string  { return "GitLab" }
func (t *Tracker) ConfigPrefix() string { return "gitlab" }

// GitLabClient returns the underlying GitLab API client.
func (t *Tracker) GitLabClient() *Client { return t.client }

func (t *Tracker) Init(ctx context.Context, store tracker.Store) error {
	t.store = store

	baseURL, _ := t.getConfig(ctx, "gitlab.url", "GITLAB_URL")
	if err := endpointtrust.Check(endpointtrust.WorkspaceKey(), "gitlab", baseURL, endpointtrust.PrivateMatch("gitlab.url", "GITLAB_URL", baseURL)); err != nil {
		return err
	}

	token, err := t.getConfig(ctx, "gitlab.token", "GITLAB_TOKEN")
	if err != nil || token == "" {
		return fmt.Errorf("GitLab token not configured (set gitlab.token or GITLAB_TOKEN)")
	}

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

	// Load the instance's web address, for a GitLab whose external_url is not
	// the address its API is reached at. A value that names no host would be
	// ignored by every comparison and bring the duplicates back, so refuse it.
	t.webURL, _ = t.getConfig(ctx, "gitlab.web_url", "GITLAB_WEB_URL")
	if t.webURL != "" {
		if _, _, ok := canonicalGitLabURL(t.webURL); !ok {
			return fmt.Errorf("gitlab.web_url %q is not a URL with a host (set it to the address GitLab shows in issue links, e.g. https://git.example.org)", t.webURL)
		}
	}

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

	// Enrich each issue with its links (dependencies), listed in the issue's
	// own project: a group pull spans projects whose IIDs repeat.
	for i := range issues {
		t.noteProject(issues[i].WebURL, issues[i].ProjectID)
		project := t.client.ProjectID
		if issues[i].ProjectID > 0 {
			project = strconv.Itoa(issues[i].ProjectID)
		}
		links, err := t.client.GetProjectIssueLinks(ctx, project, issues[i].IID)
		if err != nil {
			// Non-fatal: the token may lack permission to read the issue's links.
			continue
		}
		for _, link := range links {
			t.noteProject(link.WebURL, link.ProjectID)
		}
		issues[i].IssueLinksData = links
	}

	result := make([]tracker.TrackerIssue, 0, len(issues))
	for _, gl := range issues {
		result = append(result, gitlabToTrackerIssue(&gl))
	}
	return result, nil
}

func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*tracker.TrackerIssue, error) {
	if milestoneIID, ok, err := parseMilestoneIdentifier(identifier); ok || err != nil {
		if err != nil {
			return nil, err
		}
		ms, err := t.client.FetchMilestoneByIID(ctx, milestoneIID)
		if err != nil || ms == nil {
			return nil, err
		}
		ti := milestoneToTrackerIssue(ms)
		return ti, nil
	}

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
	t.noteProject(gl.WebURL, gl.ProjectID)

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

	// GitLab's POST /issues cannot set state, so a closed bead is created
	// "opened". Close it with a follow-up update so the state carries. On
	// failure we keep the created issue and its external_ref (returning an error
	// here would strand the issue and re-create a duplicate on the next push).
	// The trade-off: the push skip-guard treats the just-created remote as
	// up-to-date, so a failed close is not retried until the bead is next
	// modified — hence the warning. A closed-state push is the common bulk case
	// and this call rarely fails.
	var warnings []string
	if issue.Status == types.StatusClosed {
		if closed, err := t.client.UpdateIssue(ctx, created.IID, map[string]interface{}{
			"state_event": "close",
		}); err == nil {
			created = closed
		} else {
			warnings = append(warnings, fmt.Sprintf("created GitLab issue %d but failed to close it (left open): %v", created.IID, err))
		}
	}

	ti := gitlabToTrackerIssue(created)
	warnings = append(warnings, t.unrecognizedRefWarnings(ti.URL)...)
	ti.Warnings = warnings
	return &ti, nil
}

// unrecognizedRefWarnings returns a warning when the web URL GitLab reported
// for an issue it just created is one IsExternalRef refuses. That URL becomes
// the bead's external_ref, and the engine creates an issue for a bead whose
// ref the tracker refuses, so without the warning every later push would
// silently add one more copy of the issue.
func (t *Tracker) unrecognizedRefWarnings(webURL string) []string {
	if webURL == "" || t.IsExternalRef(webURL) {
		return nil
	}
	where := "not under gitlab.url"
	if t.webURL != "" {
		where = fmt.Sprintf("under neither gitlab.url nor gitlab.web_url (%s)", t.webURL)
	}
	hint := ""
	if address := t.webAddressOf(webURL); address != "" {
		hint = fmt.Sprintf(" (e.g. %s)", address)
	}
	return []string{fmt.Sprintf("GitLab reports the created issue at %s, which is %s, so the next push will not recognize the link and will create the issue again; set gitlab.web_url to this GitLab's web address%s", webURL, where, hint)}
}

// webAddressOf returns the gitlab.web_url that claims webURL, the web URL of
// an issue GitLab created in the configured project. That is webURL's scheme
// and host, followed by whatever path comes before the project's path: the
// relative URL root of a GitLab served from a sub-path, which the project
// scope and the "gitlab:N" shorthand key on. With the project known only by
// numeric ID nothing keys on the root, and the host alone claims the ref.
func (t *Tracker) webAddressOf(webURL string) string {
	u, err := url.Parse(webURL)
	if err != nil || u.Host == "" {
		return ""
	}
	address := u.Scheme + "://" + u.Host
	project := t.matchKeyProjectPath()
	_, path, ok := canonicalGitLabURL(webURL)
	if project == "" || !ok {
		return address
	}
	if m := matchKeyIssuePathPattern.FindStringSubmatch(path); m != nil && strings.HasSuffix(m[1], "/"+project) {
		address += "/" + strings.TrimSuffix(m[1], "/"+project)
	}
	return address
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

	// Milestones are created active; close it with a follow-up update if the
	// epic is closed so its state carries. Best effort with the same failure
	// trade-off as CreateIssue (a failed close is not retried until the epic is
	// next modified).
	var warnings []string
	if issue.Status == types.StatusClosed {
		if closed, err := t.client.UpdateMilestone(ctx, ms.ID, map[string]interface{}{
			"state_event": "close",
		}); err == nil {
			ms = closed
		} else {
			warnings = append(warnings, fmt.Sprintf("created GitLab milestone %d but failed to close it (left active): %v", ms.ID, err))
		}
	}
	ti := milestoneToTrackerIssue(ms)
	ti.Warnings = warnings
	return ti, nil
}

// updateMilestone updates a GitLab milestone for an epic bead.
func (t *Tracker) updateMilestone(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	mid, ok, err := parseMilestoneIdentifier(externalID)
	if err != nil {
		return nil, err
	}
	if !ok {
		mid, err = strconv.Atoi(externalID)
		if err != nil {
			return nil, fmt.Errorf("invalid milestone ID %q: %w", externalID, err)
		}
	}
	apiID := mid
	msByIID, err := t.client.FetchMilestoneByIID(ctx, mid)
	if err != nil {
		return nil, fmt.Errorf("resolving milestone IID %d: %w", mid, err)
	}
	if msByIID != nil {
		apiID = msByIID.ID
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

	ms, err := t.client.UpdateMilestone(ctx, apiID, updates)
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

	// Build the ref from the project path and IID under webBase, the address
	// GitLab's own links are on once gitlab.web_url is set, so a pull matches
	// it. It is not taken from the webUrl GitLab reports: where no configured
	// address claims that, the next push would create the task again.
	webURL := fmt.Sprintf("%s/%s/-/work_items/%s", strings.TrimRight(t.webBase(), "/"), t.projectPath, wi.IID)

	iid, _ := strconv.Atoi(wi.IID)

	// Also set milestone if there's a grandparent epic
	if milestoneID := t.findParentEpicMilestone(ctx, issue.ID); milestoneID > 0 {
		if iid > 0 {
			_, _ = t.client.UpdateIssue(ctx, iid, map[string]interface{}{
				"milestone_id": milestoneID,
			})
		}
	}

	// Work items are created open; close via the issues API (work items share
	// the project IID space, as the milestone assignment above relies on) if the
	// bead is closed. Best effort with the same failure trade-off as CreateIssue.
	var warnings []string
	if issue.Status == types.StatusClosed && iid > 0 {
		if _, err := t.client.UpdateIssue(ctx, iid, map[string]interface{}{
			"state_event": "close",
		}); err != nil {
			warnings = append(warnings, fmt.Sprintf("created GitLab work item %s but failed to close it (left open): %v", wi.IID, err))
		}
	}

	return &tracker.TrackerIssue{
		ID:         wi.ID,
		Identifier: wi.IID,
		URL:        webURL,
		Title:      wi.Title,
		Warnings:   warnings,
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
	ti := &tracker.TrackerIssue{
		ID:          strconv.Itoa(ms.ID),
		Identifier:  strconv.Itoa(ms.ID),
		URL:         ms.WebURL,
		Title:       ms.Title,
		Description: ms.Description,
		State:       ms.State,
	}
	if ms.CreatedAt != nil {
		ti.CreatedAt = *ms.CreatedAt
	}
	if ms.UpdatedAt != nil {
		ti.UpdatedAt = *ms.UpdatedAt
	}
	return ti
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
	if !strings.Contains(ref, "gitlab") && !strings.Contains(ref, "milestones") && !t.onConfiguredHost(ref) {
		return false
	}
	return issueIIDPattern.MatchString(ref) || milestoneIDPattern.MatchString(ref)
}

// onConfiguredHost reports whether ref is a URL on the same host (and base
// path, for GitLab served from a sub-path) as the configured gitlab.url, or as
// gitlab.web_url when that is set. Self-hosted instances need not have
// "gitlab" in their hostname.
//
// Matching is host-scoped, not project-scoped: with GitLab at the host root
// this claims every issue/work-item URL on the host, other projects' included.
// That is what the "gitlab" substring arm already does for gitlab.com, and
// keying on the project would silently stop recognizing self-hosted refs,
// since gitlab.project_path is optional and Init discards its load error.
//
// Hosts are compared without case and without the default port of each URL's
// own scheme, so "https://host:443" and "http://host:80" are "host", while any
// other port, "https://host:80" included, must be in both. Scheme is not
// otherwise compared, so an http:// ref on an https:// instance is still
// claimed — leniency only, since the IID patterns below still decide what is
// ultimately recognized.
//
// GitLab writes its own external_url into every web_url, whatever address the
// API was reached at. An instance reached at another address (an internal
// name, an IP, a proxy) therefore returns refs that gitlab.url alone cannot
// claim, and gitlab.web_url names that second address.
// Recognition stays host-scoped; pull fallback identity is project-qualified
// by MatchKey.
func (t *Tracker) onConfiguredHost(ref string) bool {
	if t.client != nil && urlUnderBase(ref, t.client.BaseURL) {
		return true
	}
	return urlUnderBase(ref, t.webURL)
}

// urlUnderBase reports whether ref is a URL on base's host and under base's
// path. An empty or host-less base matches nothing.
func urlUnderBase(ref, base string) bool {
	baseHost, basePath, ok := endpointOf(base)
	if !ok {
		return false
	}
	host, path, ok := endpointOf(ref)
	if !ok || host != baseHost {
		return false
	}
	return basePath == "" || path == basePath || strings.HasPrefix(path, basePath+"/")
}

// endpointOf returns raw's host and path as canonicalGitLabURL spells them,
// except that a port 80 or 443 stays on the host unless it is the default port
// of raw's own scheme: https://host:80 is another endpoint than https://host.
func endpointOf(raw string) (host, path string, ok bool) {
	host, path, ok = canonicalGitLabURL(raw)
	if !ok {
		return "", "", false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", false
	}
	if port := u.Port(); (port == "80" && u.Scheme != "http") || (port == "443" && u.Scheme != "https") {
		host += ":" + port
	}
	return host, path, true
}

// webBase returns the address GitLab's web URLs are under: gitlab.web_url
// when it is set, gitlab.url otherwise.
func (t *Tracker) webBase() string {
	if t.webURL != "" {
		return t.webURL
	}
	if t.client == nil {
		return ""
	}
	return t.client.BaseURL
}

// ExtractIdentifier extracts the issue IID from a GitLab URL or shorthand ref.
func (t *Tracker) ExtractIdentifier(ref string) string {
	if m := glShorthandPattern.FindStringSubmatch(ref); len(m) >= 2 {
		return m[1]
	}
	// Try milestone pattern first (more specific path)
	if matches := milestoneIDPattern.FindStringSubmatch(ref); len(matches) >= 2 {
		return gitLabMilestoneIdentifierPrefix + matches[1]
	}
	// Fall back to issue pattern
	if matches := issueIIDPattern.FindStringSubmatch(ref); len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

// MatchKey implements tracker.MatchKeyer. ExtractIdentifier returns the bare
// IID that FetchIssue addresses in the configured project, but IIDs repeat
// across projects, so pull fallback matching keys refs by host, project path
// and IID parsed from the ref itself.
//
// The "gitlab:{iid}" shorthand names no project. When the configured project
// is known by path (gitlab.project_path, or a path-form gitlab.project_id) it
// is keyed as that project's issue under webBase, so it still matches the
// issue's web_url. Otherwise it keeps the "gitlab:{iid}" key,
// which no URL key can equal: such a bead is never matched to some project's
// issue URL, at worst duplicated by the next pull.
func (t *Tracker) MatchKey(ref string) string {
	if m := glShorthandPattern.FindStringSubmatch(ref); len(m) >= 2 {
		project := t.configuredMatchKeyProject()
		if project == "" {
			return ref
		}
		return project + "#" + m[1]
	}
	return matchKeyFromURL(ref)
}

// configuredMatchKeyProject returns the configured project keyed as MatchKey
// keys a ref's project, "gl://{host}/{path}" on the host GitLab's web URLs
// are on (webBase), or "" when the project is only known by numeric ID.
func (t *Tracker) configuredMatchKeyProject() string {
	project := t.matchKeyProjectPath()
	if project == "" || t.client == nil {
		return ""
	}
	return projectKeyUnder(t.webBase(), project)
}

// configuredProjectKeys returns the configured project keyed as MatchKey keys
// a ref's project under each address its refs are on: gitlab.url, and
// gitlab.web_url when that is set. Beads linked before gitlab.web_url was set
// keep refs under gitlab.url, and so does a task work item created then. It
// returns nil when the project is only known by numeric ID.
func (t *Tracker) configuredProjectKeys() []string {
	project := t.matchKeyProjectPath()
	if project == "" || t.client == nil {
		return nil
	}
	var keys []string
	for _, base := range []string{t.client.BaseURL, t.webURL} {
		if key := projectKeyUnder(base, project); key != "" && !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// projectKeyUnder keys project, a canonical project path, as MatchKey keys a
// ref's project on the instance at base: "gl://{host}{base path}/{project}",
// or "" when base names no host.
func projectKeyUnder(base, project string) string {
	host, basePath, ok := canonicalGitLabURL(base)
	if !ok {
		return ""
	}
	return "gl://" + host + basePath + "/" + project
}

// matchKeyProjectPath returns the configured project's canonical path, or ""
// when it is only known by numeric ID.
func (t *Tracker) matchKeyProjectPath() string {
	project := t.projectPath
	if project == "" && t.client != nil && strings.Contains(t.client.ProjectID, "/") {
		project = t.client.ProjectID
	}
	project = strings.Trim(strings.ToLower(strings.TrimSpace(project)), "/")
	if !validMatchKeyProject(project) {
		return ""
	}
	return project
}

// LocalIdentityKey implements tracker.IdentityKeyer. A pulled issue's bead
// records the issue's project ID and IID as its source_system, and a project
// rename or transfer changes neither, while it changes the project path that
// MatchKey reads from the URL. The source_system identifies the bead only
// while its external_ref still names that IID on the configured instance, in
// a project path no issue or link fetched in this sync showed to be another
// project's. A pull rewrites external_ref but never source_system, so a bead
// relinked to another issue (by hand, or by an older pull that matched another
// project's issue with the same IID) or linked to another GitLab instance
// (whose project IDs are its own) keeps its MatchKey fallback.
func (t *Tracker) LocalIdentityKey(issue *types.Issue) string {
	if issue == nil || issue.ExternalRef == nil {
		return ""
	}
	m := sourceSystemPattern.FindStringSubmatch(issue.SourceSystem)
	if m == nil {
		return ""
	}
	ref := strings.TrimSpace(*issue.ExternalRef)
	if !glShorthandPattern.MatchString(ref) && !t.onConfiguredHost(ref) {
		return ""
	}
	if t.ExtractIdentifier(ref) != m[2] || t.refInOtherProject(ref, m[1]) {
		return ""
	}
	return issue.SourceSystem
}

// RemoteIdentityKey implements tracker.IdentityKeyer: the source_system a pull
// records for the issue, "" when GitLab sent no project ID.
func (t *Tracker) RemoteIdentityKey(issue *tracker.TrackerIssue) string {
	gl, ok := issue.Raw.(*Issue)
	if !ok || gl == nil {
		return ""
	}
	return gitLabIdentityKey(gl.ProjectID, gl.IID)
}

// noteProject records that GitLab reported projectID for the issue at webURL,
// whose path is that project's current path.
func (t *Tracker) noteProject(webURL string, projectID int) {
	project, _, ok := strings.Cut(matchKeyFromURL(webURL), "#")
	if !ok || projectID <= 0 {
		return
	}
	if t.projectAt == nil {
		t.projectAt = make(map[string]int)
	}
	t.projectAt[project] = projectID
}

// refInOtherProject reports whether an issue or link fetched in this sync
// showed ref's project path to be the path of a project other than projectID.
func (t *Tracker) refInOtherProject(ref, projectID string) bool {
	project, _, ok := strings.Cut(t.MatchKey(ref), "#")
	if !ok {
		return false
	}
	seen, ok := t.projectAt[project]
	return ok && strconv.Itoa(seen) != projectID
}

func parseMilestoneIdentifier(identifier string) (int, bool, error) {
	if !strings.HasPrefix(identifier, gitLabMilestoneIdentifierPrefix) {
		return 0, false, nil
	}
	raw := strings.TrimPrefix(identifier, gitLabMilestoneIdentifierPrefix)
	iid, err := strconv.Atoi(raw)
	if err != nil {
		return 0, true, fmt.Errorf("invalid GitLab milestone identifier %q: %w", identifier, err)
	}
	return iid, true, nil
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
// For yaml-only keys (e.g. gitlab.token), reads from config.yaml first
// to avoid leaking secrets when pushing the Dolt database to remotes.
func (t *Tracker) getConfig(ctx context.Context, key, envVar string) (string, error) {
	// Secret keys are stored in config.yaml, not the Dolt database,
	// to avoid leaking secrets when pushing to remotes.
	if config.IsYamlOnlyKey(key) {
		if val := config.GetString(key); val != "" {
			return val, nil
		}
		if envVar != "" {
			if envVal := os.Getenv(envVar); envVal != "" {
				return envVal, nil
			}
		}
		return "", nil
	}

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
