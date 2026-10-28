package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/config"
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

	// What ResolveTarget learned about the configured repository: the host
	// of its html_url, and its other names (see RefScope).
	issueHost string
	names     map[string]bool
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
// produced by BuildExternalRef when a URL is unavailable. An issue URL is
// GitHub's when it contains github.com, in any letter case, or, once the
// tracker is initialized, sits on the configured host or the host
// ResolveTarget learned, so a GitHub Enterprise host whose name lacks
// "github.com" counts too. Recognition covers any repository's issue URL;
// whether the configured repository can address the ref is
// ExtractIdentifier's call.
func (t *Tracker) IsExternalRef(ref string) bool {
	if ghShorthandPattern.MatchString(ref) {
		return true
	}
	if !issueNumberPattern.MatchString(ref) {
		return false
	}
	return strings.Contains(strings.ToLower(ref), "github.com") || t.RefScope().onHost(ref)
}

// ExtractIdentifier extracts the issue number from a GitHub URL or shorthand
// ref. Refs to other repositories yield "", or their number would match this
// repo's issue (gastownhall/beads#7781): once the tracker is initialized,
// the ref must name an issue of the configured repository, under the
// configured name or another one ResolveTarget learned, resolved through the
// same repository scope relationship sync uses. An uninitialized tracker
// keeps the legacy repo-agnostic parse.
func (t *Tracker) ExtractIdentifier(ref string) string {
	if t.client != nil {
		n, ok := t.RefScope().IssueNumberFromRef(ref)
		if !ok {
			return ""
		}
		return strconv.Itoa(n)
	}
	if m := ghShorthandPattern.FindStringSubmatch(ref); len(m) >= 2 {
		return m[1]
	}
	matches := issueNumberPattern.FindStringSubmatch(ref)
	if len(matches) < 2 {
		return ""
	}
	return matches[1]
}

// RefScope returns the repository scope used to resolve external refs to issue
// numbers for relationship sync. Refs are deliberately not resolved by issue
// number alone: see RefScope.
func (t *Tracker) RefScope() RefScope {
	if t.client == nil {
		return RefScope{}
	}
	scope := NewRefScope(t.client.BaseURL, t.client.Owner, t.client.Repo)
	scope.issueHost, scope.names = t.issueHost, t.names
	return scope
}

// ResolveTarget implements tracker.TargetResolver. A renamed or transferred
// repository keeps answering its old owner/repo, but serves its issues under
// the new one, so a ref can name the configured repository under another
// name: the new one while the configuration still names the old, or the old
// one in a bead linked before the configuration changed. ResolveTarget asks
// GitHub which repository the configured name answers with, then which one
// each other owner/repo that refs name on its hosts does. A name that answers
// with the same repository ID, which GitHub never changes or reuses, is the
// configured repository's, so ExtractIdentifier addresses its refs, while
// another repository's refs still never resolve (gastownhall/beads#7781).
// The host of the repository's html_url, which its issue URLs use, counts as
// the configured host from then on.
func (t *Tracker) ResolveTarget(ctx context.Context, refs []string) error {
	if t.client == nil {
		return nil
	}
	t.issueHost, t.names = "", nil
	configured, err := t.client.FetchRepository(ctx, t.client.Owner, t.client.Repo)
	if IsNotFound(err) {
		return nil // nothing to learn; the sync's own requests report the missing repository
	}
	if err != nil {
		return fmt.Errorf("resolving the configured GitHub repository %s/%s: %w", t.client.Owner, t.client.Repo, err)
	}
	if configured.ID == 0 {
		return nil // no repository ID to tell another name by
	}
	if u, err := url.Parse(configured.HTMLURL); err == nil && u.Host != "" {
		t.issueHost = canonicalHost(u)
	}
	t.names = map[string]bool{strings.ToLower(configured.FullName): true}

	probed := make(map[string]bool)
	for _, ref := range refs {
		owner, repo, ok := t.RefScope().otherRepository(ref)
		name := strings.ToLower(owner + "/" + repo)
		if !ok || probed[name] {
			continue
		}
		probed[name] = true
		other, err := t.client.FetchRepository(ctx, owner, repo)
		if err != nil {
			if answeredWithStatus(err) {
				continue // GitHub refused the name, so it does not answer with the configured repository
			}
			return fmt.Errorf("resolving GitHub repository %s/%s: %w", owner, repo, err)
		}
		if other.ID == configured.ID {
			t.names[name] = true
		}
	}
	return nil
}

// answeredWithStatus reports whether err is GitHub's own answer to a request,
// an HTTP status other than a rate limit, rather than a failure to get one.
func answeredWithStatus(err error) bool {
	var apiErr *APIError
	var authErr *AuthError
	return errors.As(err, &apiErr) || errors.As(err, &authErr)
}

// DescribeTarget implements tracker.TargetDescriber: it names the repository
// ExtractIdentifier addresses.
func (t *Tracker) DescribeTarget() string {
	if t.client == nil {
		return ""
	}
	return "repository " + t.client.Owner + "/" + t.client.Repo
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
