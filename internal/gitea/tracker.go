package gitea

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/endpointtrust"
	"github.com/steveyegge/beads/internal/tracker"
	"github.com/steveyegge/beads/internal/types"
)

func init() {
	tracker.Register("gitea", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

var (
	_ tracker.IssueTracker  = (*Tracker)(nil)
	_ tracker.MatchKeyer    = (*Tracker)(nil)
	_ tracker.IdentityKeyer = (*Tracker)(nil)
)

var (
	giteaRepoShorthandPattern = regexp.MustCompile(`^gitea:([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([1-9]\d*)$`)
	giteaShorthandPattern     = regexp.MustCompile(`^gitea:([1-9]\d*)$`)
	sourceSystemPattern       = regexp.MustCompile(`^gitea:([1-9]\d*):([1-9]\d*)$`)
	repositoryNamePattern     = regexp.MustCompile(`^[a-z0-9_.-]+/[a-z0-9_.-]+$`)
)

// Tracker implements tracker.IssueTracker, tracker.MatchKeyer, and
// tracker.IdentityKeyer for Gitea and Forgejo instances.
type Tracker struct {
	client      *Client
	config      *MappingConfig
	store       tracker.Store
	filter      *IssueFilter
	repoID      int64
	currentName string
	formerNames []string
}

func (t *Tracker) Name() string         { return "gitea" }
func (t *Tracker) DisplayName() string  { return "Gitea" }
func (t *Tracker) ConfigPrefix() string { return "gitea" }

// GiteaClient returns the initialized Gitea/Forgejo REST client, or nil if uninitialized.
func (t *Tracker) GiteaClient() *Client { return t.client }

// MappingConfig returns the field mapping configuration.
func (t *Tracker) MappingConfig() *MappingConfig { return t.config }

// SetFilter sets CLI-provided filter overrides for issue fetching.
func (t *Tracker) SetFilter(filter *IssueFilter) {
	t.filter = filter
}

// LinkResolver returns a relationship resolver bound to this tracker's repository.
func (t *Tracker) LinkResolver() *LinkResolver {
	if t.client == nil {
		return nil
	}
	r := NewLinkResolver(t.client)
	r.scope = t.RefScope()
	return r
}

// Init initializes the Gitea/Forgejo tracker from store configuration and environment variables.
func (t *Tracker) Init(ctx context.Context, store tracker.Store) error {
	t.store = store

	// Pass the unstripped raw URL to endpointtrust BEFORE stripping trailing "/" or "/api/v1"
	// so PrivateMatch matches raw GITEA_URL / user config values cleanly.
	rawURL := strings.TrimSpace(t.getConfig(ctx, "gitea.url", "GITEA_URL"))
	if err := endpointtrust.Check(endpointtrust.WorkspaceKey(), "gitea", rawURL, endpointtrust.PrivateMatch("gitea.url", "GITEA_URL", rawURL)); err != nil {
		return err
	}
	if rawURL == "" {
		return fmt.Errorf("Gitea/Forgejo URL not configured (set gitea.url or GITEA_URL)")
	}
	baseURL := NormalizeBaseURL(rawURL)
	if err := ValidateURLScheme(baseURL); err != nil {
		return err
	}

	token := t.getConfig(ctx, "gitea.token", "GITEA_TOKEN")
	if token == "" {
		return fmt.Errorf("Gitea/Forgejo token not configured (set gitea.token or GITEA_TOKEN)")
	}

	owner, repo := t.resolveOwnerRepo(ctx)
	if owner == "" || repo == "" {
		return fmt.Errorf("Gitea/Forgejo repository not configured (set gitea.repo or GITEA_REPO as owner/repo)")
	}

	client := NewClient(token, baseURL, owner, repo)
	if caPath := strings.TrimSpace(t.getConfig(ctx, "gitea.tls_ca", "GITEA_TLS_CA")); caPath != "" {
		var err error
		client, err = client.WithTLSCA(caPath)
		if err != nil {
			return err
		}
	}
	t.client = client

	t.config = DefaultMappingConfig()
	if closedMap := t.readMappingConfigByPrefix(ctx, "gitea.closed_state_map."); len(closedMap) > 0 {
		t.config.ClosedStateMap = closedMap
	}
	t.filter = t.loadFilterConfig(ctx)

	return t.resolveRepositoryNames(ctx)
}

// ResolveOwnerRepo resolves the repository owner and name from gitea.repo,
// gitea.owner, and gitea.project configuration values.
// When gitea.repo is in "owner/repo" form, it takes precedence over gitea.owner.
func ResolveOwnerRepo(repoSpec, ownerConfig, projectConfig string) (owner, repo string) {
	repoSpec = strings.TrimSpace(repoSpec)
	ownerConfig = strings.TrimSpace(ownerConfig)
	projectConfig = strings.TrimSpace(projectConfig)

	if strings.Contains(repoSpec, "/") {
		parts := strings.SplitN(repoSpec, "/", 2)
		specOwner := strings.TrimSpace(parts[0])
		specRepo := strings.Trim(strings.TrimSpace(parts[1]), "/")
		if specOwner != "" && specRepo != "" {
			return specOwner, specRepo
		}
		if specRepo != "" {
			repo = specRepo
		}
		if specOwner != "" {
			owner = specOwner
		}
	} else if repoSpec != "" {
		repo = repoSpec
	}

	if owner == "" {
		owner = ownerConfig
	}
	if repo == "" {
		repo = projectConfig
	}
	return owner, repo
}

func (t *Tracker) resolveOwnerRepo(ctx context.Context) (string, string) {
	return ResolveOwnerRepo(
		t.getConfig(ctx, "gitea.repo", "GITEA_REPO"),
		t.getConfig(ctx, "gitea.owner", "GITEA_OWNER"),
		t.getConfig(ctx, "gitea.project", "GITEA_PROJECT"),
	)
}

func (t *Tracker) readMappingConfigByPrefix(ctx context.Context, prefix string) map[string]string {
	if t.store == nil {
		return nil
	}
	allConfig, err := t.store.GetAllConfig(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]string)
	for k, v := range allConfig {
		if strings.HasPrefix(k, prefix) {
			label := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(k, prefix)))
			val := strings.TrimSpace(v)
			if label != "" && val != "" {
				out[label] = val
			}
		}
	}
	return out
}

func (t *Tracker) loadFilterConfig(ctx context.Context) *IssueFilter {
	labels := t.getConfig(ctx, "gitea.filter_labels", "GITEA_FILTER_LABELS")
	milestone := t.getConfig(ctx, "gitea.filter_milestone", "GITEA_FILTER_MILESTONE")
	assignee := t.getConfig(ctx, "gitea.filter_assignee", "GITEA_FILTER_ASSIGNEE")
	if labels == "" && milestone == "" && assignee == "" {
		return nil
	}
	return &IssueFilter{
		Labels:    labels,
		Milestone: milestone,
		Assignee:  assignee,
	}
}

func (t *Tracker) resolveRepositoryNames(ctx context.Context) error {
	repo, err := t.client.FetchRepository(ctx)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("looking up repository %s/%s on Gitea/Forgejo: %w", t.client.Owner, t.client.Repo, err)
	}
	t.repoID = repo.ID
	t.currentName = repo.FullName
	if repo.ID == 0 || t.store == nil {
		return nil
	}
	names, err := t.otherRepositoryNames(ctx)
	if err != nil {
		return fmt.Errorf("listing local beads to find other names of repository %s/%s: %w", t.client.Owner, t.client.Repo, err)
	}
	for _, name := range names {
		if err := t.resolveOtherName(ctx, name, repo.ID); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tracker) resolveOtherName(ctx context.Context, name string, id int64) error {
	owner, repo, _ := strings.Cut(name, "/")
	other, err := t.client.fetchRepositoryNamed(ctx, owner, repo)
	switch {
	case err == nil:
		if other != nil && other.ID == id {
			t.formerNames = append(t.formerNames, name)
		}
		return nil
	case IsNotFound(err):
		return nil
	}
	configured := t.client.Owner + "/" + t.client.Repo
	return fmt.Errorf("looking up repository %s on Gitea/Forgejo, which local beads link to: %w; "+
		"if %s is another repository and this token lacks access, grant read access or relink those beads to %s",
		name, err, name, configured)
}

func (t *Tracker) otherRepositoryNames(ctx context.Context) ([]string, error) {
	issues, err := t.store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil, err
	}
	scope := t.RefScope()
	seen := make(map[string]bool)
	var names []string
	for _, issue := range issues {
		if issue == nil || issue.ExternalRef == nil {
			continue
		}
		name, ok := scope.otherRepository(*issue.ExternalRef)
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		owner, repo, _ := strings.Cut(name, "/")
		if !repositoryNamePattern.MatchString(name) || owner == "." || owner == ".." || repo == "." || repo == ".." {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// Validate checks that the tracker is initialized.
func (t *Tracker) Validate() error {
	if t.client == nil {
		return fmt.Errorf("Gitea/Forgejo tracker not initialized")
	}
	return nil
}

// Close releases tracker resources.
func (t *Tracker) Close() error { return nil }

// FetchIssues retrieves issues from Gitea/Forgejo, skipping PRs and best-effort
// hydrating blocking dependencies.
func (t *Tracker) FetchIssues(ctx context.Context, opts tracker.FetchOptions) ([]tracker.TrackerIssue, error) {
	state := opts.State
	if state == "" {
		state = "all"
	}
	var issues []Issue
	var err error
	if opts.Since != nil {
		issues, err = t.client.FetchIssuesSince(ctx, state, *opts.Since, t.filter)
	} else {
		issues, err = t.client.FetchIssues(ctx, state, t.filter)
	}
	if err != nil {
		return nil, err
	}
	if opts.Limit > 0 && len(issues) > opts.Limit {
		issues = issues[:opts.Limit]
	}

	result := make([]tracker.TrackerIssue, 0, len(issues))
	for i := range issues {
		gt := issues[i]
		t.attachRepoFallback(&gt)
		deps, depErr := t.client.ListBlockedBy(ctx, gt.Number)
		if depErr == nil {
			for j := range deps {
				t.attachRepoFallback(&deps[j])
			}
			gt.BlockedBy = deps
		} else if !IsForbidden(depErr) && !IsNotFound(depErr) {
			// Best-effort dependency hydration; only unexpected non-403/404 errors are surfaced?
			// Actually return unexpected transport/5xx errors so transient failures aren't silently swallowed.
			return nil, fmt.Errorf("fetch dependencies for issue #%d: %w", gt.Number, depErr)
		}
		result = append(result, giteaToTrackerIssue(&gt))
	}
	return result, nil
}

func (t *Tracker) attachRepoFallback(gt *Issue) {
	if gt == nil || t.client == nil {
		return
	}
	if gt.Repository == nil && (t.repoID > 0 || t.client.Owner != "" && t.client.Repo != "") {
		gt.Repository = &RepositoryMeta{
			ID:       t.repoID,
			Owner:    t.client.Owner,
			Name:     t.client.Repo,
			FullName: t.client.Owner + "/" + t.client.Repo,
		}
	} else if gt.Repository != nil && gt.Repository.ID == 0 && t.repoID > 0 {
		gt.Repository.ID = t.repoID
	}
}

// FetchIssue retrieves a single issue by numeric index or repository-scoped
// external reference (such as a full issue URL, gitea:N, or gitea:owner/repo#N).
// Returns nil, nil if not found or if it is a PR.
func (t *Tracker) FetchIssue(ctx context.Context, identifier string) (*tracker.TrackerIssue, error) {
	if t.client == nil {
		return nil, fmt.Errorf("Gitea/Forgejo tracker not initialized")
	}
	trimmed := strings.TrimSpace(identifier)
	number, err := strconv.Atoi(trimmed)
	if err != nil || number <= 0 {
		if !t.IsExternalRef(trimmed) {
			return nil, fmt.Errorf("invalid Gitea/Forgejo issue number or reference %q", identifier)
		}
		extID := t.ExtractIdentifier(trimmed)
		if extID == "" {
			return nil, fmt.Errorf("Gitea/Forgejo reference %q does not match configured repository %s/%s", identifier, t.client.Owner, t.client.Repo)
		}
		parsed, parseErr := strconv.Atoi(extID)
		if parseErr != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid Gitea/Forgejo issue number %q", identifier)
		}
		number = parsed
	}
	gt, err := t.client.FetchIssueByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	if gt == nil || gt.IsPullRequest() {
		return nil, nil
	}
	t.attachRepoFallback(gt)
	if deps, depErr := t.client.ListBlockedBy(ctx, gt.Number); depErr == nil {
		for j := range deps {
			t.attachRepoFallback(&deps[j])
		}
		gt.BlockedBy = deps
	} else if !IsForbidden(depErr) && !IsNotFound(depErr) {
		return nil, fmt.Errorf("fetch dependencies for issue #%d: %w", gt.Number, depErr)
	}
	ti := giteaToTrackerIssue(gt)
	return &ti, nil
}

// CreateIssue creates a new issue in Gitea/Forgejo, resolving label IDs and closing it
// in a follow-up PATCH if the local bead is closed.
func (t *Tracker) CreateIssue(ctx context.Context, issue *types.Issue) (*tracker.TrackerIssue, error) {
	fields := BeadsIssueToGiteaFields(issue, t.config)
	labelNames, _ := fields["labels"].([]string)
	labelIDs, err := t.client.ResolveLabelIDs(ctx, labelNames)
	if err != nil {
		return nil, err
	}

	created, err := t.client.CreateIssue(ctx, issue.Title, issue.Description, labelIDs)
	if err != nil {
		return nil, err
	}

	var warnings []string
	if issue.Status == types.StatusClosed {
		if closed, closeErr := t.client.UpdateIssue(ctx, created.Number, map[string]interface{}{
			"state": "closed",
		}); closeErr == nil {
			created = closed
		} else {
			warnings = append(warnings, fmt.Sprintf("created Gitea/Forgejo issue #%d but failed to close it (left open): %v", created.Number, closeErr))
		}
	}
	t.attachRepoFallback(created)
	ti := giteaToTrackerIssue(created)
	ti.Warnings = warnings
	return &ti, nil
}

// UpdateIssue updates an existing issue and replaces its labels in Gitea/Forgejo.
func (t *Tracker) UpdateIssue(ctx context.Context, externalID string, issue *types.Issue) (*tracker.TrackerIssue, error) {
	number, err := strconv.Atoi(strings.TrimSpace(externalID))
	if err != nil || number <= 0 {
		return nil, fmt.Errorf("invalid Gitea/Forgejo issue number %q", externalID)
	}

	fields := BeadsIssueToGiteaFields(issue, t.config)
	labelNames, _ := fields["labels"].([]string)
	labelIDs, err := t.client.ResolveLabelIDs(ctx, labelNames)
	if err != nil {
		return nil, err
	}

	patchFields := map[string]interface{}{
		"title": fields["title"],
		"body":  fields["body"],
		"state": fields["state"],
	}
	updated, err := t.client.UpdateIssue(ctx, number, patchFields)
	if err != nil {
		return nil, err
	}
	if err := t.client.ReplaceIssueLabels(ctx, number, labelIDs); err != nil {
		return nil, err
	}
	if refreshed, refreshErr := t.client.FetchIssueByNumber(ctx, number); refreshErr == nil && refreshed != nil {
		updated = refreshed
	}
	t.attachRepoFallback(updated)
	ti := giteaToTrackerIssue(updated)
	return &ti, nil
}

// FieldMapper returns the field mapper for Gitea/Forgejo.
func (t *Tracker) FieldMapper() tracker.FieldMapper {
	return &giteaFieldMapper{config: t.config}
}

// PushTargetScope returns a canonical identifier for the configured repository endpoint.
func (t *Tracker) PushTargetScope() string {
	if t.client == nil {
		return ""
	}
	base := canonicalGiteaBaseURL(t.client.BaseURL)
	return fmt.Sprintf("%s/repos/%s/%s",
		base,
		strings.ToLower(strings.TrimSpace(t.client.Owner)),
		strings.ToLower(strings.TrimSpace(t.client.Repo)),
	)
}

func canonicalGiteaBaseURL(raw string) string {
	normalized := NormalizeBaseURL(raw)
	u, err := url.Parse(normalized)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return normalized
	}
	host, path, ok := canonicalURLHostAndPath(normalized)
	if !ok {
		return normalized
	}
	return strings.ToLower(u.Scheme) + "://" + host + path
}

func (t *Tracker) onConfiguredHost(ref string) bool {
	if t.client == nil || t.client.BaseURL == "" {
		return false
	}
	baseHost, basePath, ok := canonicalURLHostAndPath(t.client.BaseURL)
	if !ok {
		return false
	}
	refHost, refPath, refOK := canonicalURLHostAndPath(ref)
	if !refOK || refHost != baseHost {
		return false
	}
	return basePath == "" || refPath == basePath || strings.HasPrefix(refPath, basePath+"/")
}

// IsExternalRef reports whether ref belongs to this Gitea/Forgejo tracker.
func (t *Tracker) IsExternalRef(ref string) bool {
	trimmed := strings.TrimSpace(ref)
	if giteaRepoShorthandPattern.MatchString(trimmed) || giteaShorthandPattern.MatchString(trimmed) {
		return true
	}
	if t.client == nil || !t.onConfiguredHost(trimmed) {
		return false
	}
	_, _, _, ok := t.RefScope().issueURL(trimmed)
	return ok
}

// ExtractIdentifier extracts the repository-scoped issue number from ref.
func (t *Tracker) ExtractIdentifier(ref string) string {
	trimmed := strings.TrimSpace(ref)
	if t.client != nil {
		number, ok := t.RefScope().IssueNumberFromRef(trimmed)
		if !ok {
			return ""
		}
		return strconv.Itoa(number)
	}
	if m := giteaRepoShorthandPattern.FindStringSubmatch(trimmed); len(m) == 4 {
		return m[3]
	}
	if m := giteaShorthandPattern.FindStringSubmatch(trimmed); len(m) >= 2 {
		return m[1]
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	_, _, number, ok := splitIssueURLPath(strings.ToLower(u.Path))
	if !ok {
		return ""
	}
	return strconv.Itoa(number)
}

// RefScope returns the repository scope for resolving external refs.
func (t *Tracker) RefScope() RefScope {
	if t.client == nil {
		return RefScope{}
	}
	return NewRefScope(t.client.BaseURL, t.client.Owner, t.client.Repo).
		withNames(append([]string{t.currentName}, t.formerNames...)...)
}

// BuildExternalRef constructs the canonical external_ref for a pulled or created issue.
func (t *Tracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	if issue.URL != "" {
		return issue.URL
	}
	if t.client != nil && t.client.Owner != "" && t.client.Repo != "" {
		return fmt.Sprintf("gitea:%s/%s#%s", t.client.Owner, t.client.Repo, issue.Identifier)
	}
	return fmt.Sprintf("gitea:%s", issue.Identifier)
}

// MatchKey implements tracker.MatchKeyer.
func (t *Tracker) MatchKey(ref string) string {
	var baseURL, owner, repo string
	if t.client != nil {
		baseURL = t.client.BaseURL
		owner = t.client.Owner
		repo = t.client.Repo
	}
	return matchKeyFromRef(baseURL, owner, repo, ref)
}

// LocalIdentityKey implements tracker.IdentityKeyer.
func (t *Tracker) LocalIdentityKey(issue *types.Issue) string {
	if issue == nil || issue.ExternalRef == nil {
		return ""
	}
	m := sourceSystemPattern.FindStringSubmatch(issue.SourceSystem)
	if m == nil {
		return ""
	}
	ref := strings.TrimSpace(*issue.ExternalRef)
	if !giteaRepoShorthandPattern.MatchString(ref) && !giteaShorthandPattern.MatchString(ref) && !t.onConfiguredHost(ref) {
		return ""
	}
	if t.ExtractIdentifier(ref) != m[2] {
		return ""
	}
	if t.repoID > 0 && m[1] != strconv.FormatInt(t.repoID, 10) {
		return ""
	}
	return issue.SourceSystem
}

// RemoteIdentityKey implements tracker.IdentityKeyer.
func (t *Tracker) RemoteIdentityKey(issue *tracker.TrackerIssue) string {
	gt, ok := issue.Raw.(*Issue)
	if !ok || gt == nil {
		return ""
	}
	var repoID int64
	if gt.Repository != nil && gt.Repository.ID > 0 {
		repoID = gt.Repository.ID
	} else if t.repoID > 0 {
		repoID = t.repoID
	}
	if repoID <= 0 || gt.Number <= 0 {
		return ""
	}
	return fmt.Sprintf("gitea:%d:%d", repoID, gt.Number)
}

func (t *Tracker) getConfig(ctx context.Context, key, envVar string) string {
	if config.IsYamlOnlyKey(key) {
		if val := config.GetString(key); val != "" {
			return val
		}
		if envVar != "" {
			if envVal := os.Getenv(envVar); envVal != "" {
				return envVal
			}
		}
		return ""
	}

	if t.store != nil {
		val, err := t.store.GetConfig(ctx, key)
		if err == nil && val != "" {
			return val
		}
	}
	if envVar != "" {
		if envVal := os.Getenv(envVar); envVal != "" {
			return envVal
		}
	}
	return ""
}

func giteaToTrackerIssue(gt *Issue) tracker.TrackerIssue {
	ti := tracker.TrackerIssue{
		ID:          strconv.FormatInt(gt.ID, 10),
		Identifier:  strconv.Itoa(gt.Number),
		URL:         gt.HTMLURL,
		Title:       gt.Title,
		Description: gt.Body,
		Labels:      gt.LabelNames(),
		Raw:         gt,
	}
	if gt.State != "" {
		ti.State = gt.State
	}
	if gt.Assignee != nil {
		ti.Assignee = gt.Assignee.Handle()
		ti.AssigneeID = strconv.FormatInt(gt.Assignee.ID, 10)
	}
	if gt.CreatedAt != nil {
		ti.CreatedAt = *gt.CreatedAt
	}
	if gt.UpdatedAt != nil {
		ti.UpdatedAt = *gt.UpdatedAt
	}
	if gt.ClosedAt != nil {
		ti.CompletedAt = gt.ClosedAt
	}
	return ti
}
