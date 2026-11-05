package github

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
	tracker.Register("github", func() tracker.IssueTracker {
		return &Tracker{}
	})
}

// issueNumberPattern matches GitHub issue URLs: .../issues/42
var issueNumberPattern = regexp.MustCompile(`/issues/(\d+)`)

// ghShorthandPattern matches the "github:{digits}" shorthand produced by BuildExternalRef
// when a full URL is unavailable.
var ghShorthandPattern = regexp.MustCompile(`^github:([1-9]\d*)$`)

// Tracker implements tracker.IssueTracker for GitHub.
type Tracker struct {
	client *Client
	config *MappingConfig
	store  tracker.Store
	// currentName is the repository's owner/repo as GitHub reported it at
	// Init, and formerNames are the other names local refs use that GitHub
	// resolved to the same repository (see resolveRepositoryNames). Both are
	// empty when unknown.
	currentName string
	formerNames []string
}

// LinkResolver returns a relationship resolver bound to this tracker's
// repository, or nil when the tracker has not been initialized. Callers get
// the resolver (and, through it, the repository ref scope) rather than the raw
// REST client, so relationship sync cannot reach past the endpoints it needs.
func (t *Tracker) LinkResolver() *LinkResolver {
	if t.client == nil {
		return nil
	}
	r := NewLinkResolver(t.client)
	r.scope = t.RefScope()
	return r
}

func (t *Tracker) Name() string         { return "github" }
func (t *Tracker) DisplayName() string  { return "GitHub" }
func (t *Tracker) ConfigPrefix() string { return "github" }

func (t *Tracker) Init(ctx context.Context, store tracker.Store) error {
	t.store = store

	baseURL := t.getConfig(ctx, "github.url", "GITHUB_API_URL")
	if err := endpointtrust.Check(endpointtrust.WorkspaceKey(), "github", baseURL, endpointtrust.PrivateMatch("github.url", "GITHUB_API_URL", baseURL)); err != nil {
		return err
	}

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
	if baseURL != "" {
		t.client = t.client.WithBaseURL(baseURL)
	}

	t.config = DefaultMappingConfig()
	t.resolveRepositoryNames(ctx)
	return nil
}

// resolveRepositoryNames records the names other than the configured one
// under which the repository's issues may be linked, as GitHub resolves them.
// GitHub serves a renamed or transferred repository under each former name by
// redirect, but every issue it returns names the repository as it is called
// now, and so does every ref BuildExternalRef stores; refs stored before the
// rename keep the name it had then. So a config naming the old name meets
// refs under the current one, and a config moved to the new name (or a
// GITHUB_REPOSITORY that follows the repository) meets refs under the old
// one. Without those names in RefScope the tracker would refuse those refs:
// push and conflict detection would skip their beads without a warning, and a
// pull would import their issues again as new beads.
//
// The current name is the configured repository's full_name. A former name
// can only be recognized by asking GitHub, so each other repository that a
// local ref names on the configured host is read once, and its name counts
// when GitHub answers with the configured repository's ID; another
// repository answers with its own ID, and a deleted or inaccessible one not
// at all. A failed lookup adds no name, and a failed lookup of the configured
// repository leaves the configured name as the only one.
func (t *Tracker) resolveRepositoryNames(ctx context.Context) {
	repo, err := t.client.FetchRepository(ctx)
	if err != nil {
		return
	}
	t.currentName = repo.FullName
	if repo.ID == 0 || t.store == nil {
		return
	}
	for _, name := range t.otherRepositoryNames(ctx) {
		owner, repoName, _ := strings.Cut(name, "/")
		other, err := t.client.fetchRepositoryNamed(ctx, owner, repoName)
		if err == nil && other != nil && other.ID == repo.ID {
			t.formerNames = append(t.formerNames, name)
		}
	}
}

// repositoryNamePattern matches a lowercase "owner/repo" made of the
// characters GitHub allows in owner and repository names.
var repositoryNamePattern = regexp.MustCompile(`^[a-z0-9_.-]+/[a-z0-9_.-]+$`)

// otherRepositoryNames returns, sorted, the lowercase "owner/repo" of each
// repository other than this one that a local issue's external_ref names on
// the configured host.
func (t *Tracker) otherRepositoryNames(ctx context.Context) []string {
	issues, err := t.store.SearchIssues(ctx, "", types.IssueFilter{})
	if err != nil {
		return nil
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
	return names
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

	// GitHub's POST /issues cannot set state, so a closed bead is created
	// open. Close it with a follow-up update so the state carries
	// (gastownhall/beads#5486). On failure we keep the created issue and its
	// external_ref (returning an error here would strand the issue and
	// re-create a duplicate on the next push) and report the failed close as
	// a warning instead; the engine then leaves the push hash uncached, so the
	// next push fetches the issue and retries the close.
	var warnings []string
	if issue.Status == types.StatusClosed {
		if closed, err := t.client.UpdateIssue(ctx, created.Number, map[string]interface{}{
			"state": "closed",
		}); err == nil {
			created = closed
		} else {
			warnings = append(warnings, fmt.Sprintf("created GitHub issue #%d but failed to close it (left open): %v", created.Number, err))
		}
	}

	ti := githubToTrackerIssue(created)
	ti.Warnings = warnings
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

// MappingConfig exposes the tracker's field-mapping configuration so callers
// (e.g. push-hook content comparison) can mirror push field semantics.
func (t *Tracker) MappingConfig() *MappingConfig {
	return t.config
}

// PushTargetScope returns the canonical repository endpoint under which issue
// identifiers are resolved. It lets the sync engine distinguish repo-less refs
// such as github:42 when the configured host, owner, or repository changes.
func (t *Tracker) PushTargetScope() string {
	if t.client == nil {
		return ""
	}
	return fmt.Sprintf("%s/repos/%s/%s",
		canonicalGitHubBaseURL(t.client.BaseURL),
		strings.ToLower(strings.TrimSpace(t.client.Owner)),
		strings.ToLower(strings.TrimSpace(t.client.Repo)),
	)
}

// canonicalGitHubBaseURL normalizes the URL components that are
// case-insensitive while preserving case-sensitive path semantics. Malformed
// and relative custom values fall back to deterministic whitespace/slash
// trimming instead of being rejected here (Tracker.Init/client validation owns
// their usability).
func canonicalGitHubBaseURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return trimmed
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	if u.RawPath != "" {
		u.RawPath = strings.TrimRight(u.RawPath, "/")
	}
	return u.String()
}

// IsExternalRef checks if a ref belongs to this GitHub tracker.
// It recognizes both full GitHub URLs and the "github:{id}" shorthand format
// produced by BuildExternalRef when a URL is unavailable.
func (t *Tracker) IsExternalRef(ref string) bool {
	if ghShorthandPattern.MatchString(ref) {
		return true
	}
	return strings.Contains(ref, "github.com") && issueNumberPattern.MatchString(ref)
}

// ExtractIdentifier extracts the issue number from a GitHub URL or shorthand ref.
// Once the tracker is initialized, a URL yields a number only when it points
// at the configured repository, by its configured name or another name GitHub
// resolves to it (see RefScope): issue numbers are repository-scoped,
// so another repository's URL must not address, match or overwrite the
// configured repository's issue with the same number
// (gastownhall/beads#7781). The repo-less shorthand is read as the configured
// repository, as before.
func (t *Tracker) ExtractIdentifier(ref string) string {
	if m := ghShorthandPattern.FindStringSubmatch(ref); len(m) >= 2 {
		return m[1]
	}
	if t.client != nil {
		number, ok := t.RefScope().IssueNumberFromRef(ref)
		if !ok {
			return ""
		}
		return strconv.Itoa(number)
	}
	matches := issueNumberPattern.FindStringSubmatch(ref)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// RefScope returns the repository scope used to resolve external refs to issue
// numbers for ExtractIdentifier and relationship sync. Refs are deliberately
// not resolved by issue number alone: see RefScope.
func (t *Tracker) RefScope() RefScope {
	if t.client == nil {
		return RefScope{}
	}
	return NewRefScope(t.client.BaseURL, t.client.Owner, t.client.Repo).
		withNames(append([]string{t.currentName}, t.formerNames...)...)
}

func (t *Tracker) BuildExternalRef(issue *tracker.TrackerIssue) string {
	if issue.URL != "" {
		return issue.URL
	}
	return fmt.Sprintf("github:%s", issue.Identifier)
}

// getConfig reads a config value from storage, falling back to env var.
// For yaml-only keys (e.g. github.token), reads from config.yaml first
// to match the behavior of cmd/bd/github.go:getGitHubConfigValue().
func (t *Tracker) getConfig(ctx context.Context, key, envVar string) string {
	// Secret keys are stored in config.yaml, not the Dolt database,
	// to avoid leaking secrets when pushing to remotes.
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
