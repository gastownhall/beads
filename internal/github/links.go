package github

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

const (
	githubLinkSubIssue  = "sub_issue"
	githubLinkBlockedBy = "blocked_by"
)

// RefScope resolves beads external refs to issue numbers in exactly one GitHub
// repository. GitHub's sub-issue and issue-dependency endpoints are
// repository-scoped and take bare issue numbers, so a ref pointing at another
// repository — or at another host entirely, e.g. a GitLab or GHES URL — must
// not yield a number here: it would link whichever unrelated issues happen to
// carry those numbers in the configured repository.
type RefScope struct {
	apiHost string // canonical host[:port] of the REST API base URL
	webHost string // canonical host[:port] used by issue HTML URLs
	owner   string
	repo    string
	// issueHost is the host of the repository's own html_url, and names
	// holds every other owner/repo, lower-cased, that GitHub answers with
	// this repository (a renamed or transferred repository keeps answering
	// its old one). Tracker.ResolveTarget learns both.
	issueHost string
	names     map[string]bool
}

// repoNamePattern matches the characters GitHub allows in an owner or
// repository name.
var repoNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// SubIssueLinkFromParentChild is retained for existing callers. Relationship
// sync itself uses the scoped RefScope method below.
func SubIssueLinkFromParentChild(issue *types.Issue, parent *types.IssueWithDependencyMetadata) (DependencyLink, bool) {
	return NewRefScope("https://github.com", "o", "r").SubIssueLinkFromParentChild(issue, parent)
}

// BlockedByLinkFromBeadsDependency is retained for existing callers.
func BlockedByLinkFromBeadsDependency(issue *types.Issue, dep *types.IssueWithDependencyMetadata) (DependencyLink, bool) {
	return NewRefScope("https://github.com", "o", "r").BlockedByLinkFromBeadsDependency(issue, dep)
}

// NewRefScope builds a ref scope for a repository reachable at the given REST
// API base URL.
func NewRefScope(baseURL, owner, repo string) RefScope {
	apiHost := hostFromURL(baseURL)
	webHost := apiHost
	// api.github.com serves the REST API for github.com, while the issue HTML
	// URLs that BuildExternalRef stores use the bare host. GitHub Enterprise
	// serves both from one host, so this trim is a no-op there.
	if trimmed := strings.TrimPrefix(apiHost, "api."); trimmed != "" {
		webHost = trimmed
	}
	return RefScope{
		apiHost: apiHost,
		webHost: webHost,
		owner:   strings.ToLower(strings.TrimSpace(owner)),
		repo:    strings.ToLower(strings.TrimSpace(repo)),
	}
}

func hostFromURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return ""
	}
	return canonicalHost(u)
}

// canonicalHost returns u's host[:port] lower-cased, without a trailing dot
// or the scheme's default port, so respellings of one host compare equal.
func canonicalHost(u *url.URL) string {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	switch port := u.Port(); {
	case port == "", port == "443" && u.Scheme == "https", port == "80" && u.Scheme == "http":
		return host
	default:
		return net.JoinHostPort(host, port)
	}
}

// IssueNumberFromRef extracts a repository-scoped GitHub issue number from a
// beads external ref, but only when the ref points at this scope's repository.
// A full issue URL must match the configured host and owner/repo; the
// repo-less "github:{digits}" shorthand that BuildExternalRef emits when no
// URL is available is read as the configured repository. Everything else —
// another repository, another host, a non-GitHub tracker URL — is rejected.
// Host respellings still match: any letter case, a trailing dot, an explicit
// default port, the web host's www. alias, and a ref with no scheme. So does
// any other name of the repository the scope learned (Tracker.ResolveTarget).
func (s RefScope) IssueNumberFromRef(ref string) (int, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || s.owner == "" || s.repo == "" {
		return 0, false
	}

	if m := ghShorthandPattern.FindStringSubmatch(ref); len(m) >= 2 {
		n, err := strconv.Atoi(m[1])
		return n, err == nil && n > 0
	}

	u, ok := parseRefURL(ref)
	if !ok || !s.servesHost(u) {
		return 0, false
	}

	owner, repo, number, ok := splitIssueURLPath(u.Path)
	if !ok || !s.namesRepository(owner, repo) {
		return 0, false
	}
	return number, true
}

// namesRepository reports whether owner/repo names the scope's repository:
// the configured name, or another one GitHub answers with that repository.
func (s RefScope) namesRepository(owner, repo string) bool {
	if strings.EqualFold(owner, s.owner) && strings.EqualFold(repo, s.repo) {
		return true
	}
	return s.names[strings.ToLower(owner+"/"+repo)]
}

// otherRepository returns the owner/repo that ref names when ref is an issue
// URL on a host the scope serves whose owner/repo is not a name of the
// scope's repository and could name a GitHub repository.
func (s RefScope) otherRepository(ref string) (owner, repo string, ok bool) {
	u, ok := parseRefURL(ref)
	if !ok || !s.servesHost(u) {
		return "", "", false
	}
	owner, repo, _, ok = splitIssueURLPath(u.Path)
	if !ok || s.namesRepository(owner, repo) || !validRepoName(owner) || !validRepoName(repo) {
		return "", "", false
	}
	return owner, repo, true
}

// validRepoName reports whether name could be a GitHub owner or repository
// name, so a request for it stays on the /repos/{owner}/{repo} endpoint.
func validRepoName(name string) bool {
	return name != "." && name != ".." && repoNamePattern.MatchString(name)
}

// onHost reports whether ref is a URL on a host the scope serves, spelled any
// way IssueNumberFromRef accepts, whichever repository it names.
func (s RefScope) onHost(ref string) bool {
	u, ok := parseRefURL(ref)
	return ok && s.servesHost(u)
}

// servesHost reports whether u is on the scope's API host, its web host or
// the host of the repository's html_url, or on either web host's www. alias.
func (s RefScope) servesHost(u *url.URL) bool {
	host := canonicalHost(u)
	return host != "" && (host == s.apiHost || host == s.webHost || host == "www."+s.webHost ||
		(s.issueHost != "" && (host == s.issueHost || host == "www."+s.issueHost)))
}

// parseRefURL parses ref as a URL with a host, reading a ref with no scheme
// (github.com/{owner}/{repo}/issues/42) as https.
func parseRefURL(ref string) (*url.URL, bool) {
	ref = strings.TrimSpace(ref)
	u, err := url.Parse(ref)
	if err == nil && u.Scheme == "" && u.Host == "" {
		u, err = url.Parse("https://" + ref)
	}
	return u, err == nil && u.Host != ""
}

// splitIssueURLPath pulls owner, repo, and issue number out of a GitHub issue
// URL path. It accepts both the HTML form (/{owner}/{repo}/issues/42) and the
// REST form (/repos/{owner}/{repo}/issues/42, optionally behind a GitHub
// Enterprise /api/v3 prefix). It reads the first issues/{number} pair, as
// IsExternalRef's /issues/{digits} match does, so a path below the issue
// (/issues/42/comments) names that issue too.
func splitIssueURLPath(path string) (owner, repo string, number int, ok bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i := 2; i+1 < len(segments); i++ {
		if segments[i] != "issues" {
			continue
		}
		n, err := strconv.Atoi(segments[i+1])
		if err != nil || n <= 0 {
			continue // a repository named "issues": /repos/{owner}/issues/issues/42
		}
		owner, repo = segments[i-2], segments[i-1]
		if owner == "" || repo == "" {
			return "", "", 0, false
		}
		return owner, repo, n, true
	}
	return "", "", 0, false
}

// DependencyLink is a GitHub relationship operation derived from a beads
// parent-child link or "blocks" dependency. FromNumber is the issue the
// relationship is created on (the parent for sub_issue, the blocked issue for
// blocked_by); ToNumber is the other side (the child, or the blocker).
type DependencyLink struct {
	FromNumber  int
	ToNumber    int
	LinkType    string // "sub_issue" or "blocked_by"
	FromBeadsID string
	ToBeadsID   string
}

// PushLinkOptions configures dependency link push behavior.
type PushLinkOptions struct {
	DryRun bool
	OnPlan func(DependencyLink)
}

// PushLinkResult summarizes a PushLinks pass. UnsupportedSkipped counts
// relationships skipped because GitHub answered 404 for the relationship
// endpoint itself — the sub-issue and issue-dependency APIs are absent on
// older GitHub Enterprise Server versions. SourceMissing counts relationships
// skipped because the source issue 404s: deleted, renumbered, or not visible
// to this token. The two are counted apart because a host that cannot do
// relationships at all and a stale local ref call for different operator
// responses; the caller emits one curated line per class instead of a raw
// error per link. Errors holds genuine failures, at most one per source issue.
type PushLinkResult struct {
	Created            int
	UnsupportedSkipped int
	SourceMissing      int
	Errors             []error
}

// LinkResolver handles GitHub relationship convergence (sub-issues and issue
// dependencies) for one repository.
type LinkResolver struct {
	Client *Client
	scope  RefScope
}

// NewLinkResolver creates a GitHub dependency link resolver whose ref scope is
// the client's repository.
func NewLinkResolver(client *Client) *LinkResolver {
	r := &LinkResolver{Client: client}
	if client != nil {
		r.scope = NewRefScope(client.BaseURL, client.Owner, client.Repo)
	}
	return r
}

// Scope returns the repository that external refs must point at to take part
// in relationship sync.
func (r *LinkResolver) Scope() RefScope {
	if r == nil {
		return RefScope{}
	}
	return r.scope
}

// SubIssueLinkFromParentChild converts one beads parent-child dependency into
// a GitHub sub-issue link. parent must be the issue that issue's
// GetDependenciesWithMetadata resolved via a DepParentChild edge. Both refs
// must resolve inside this scope's repository.
func (s RefScope) SubIssueLinkFromParentChild(issue *types.Issue, parent *types.IssueWithDependencyMetadata) (DependencyLink, bool) {
	if issue == nil || parent == nil || issue.ExternalRef == nil || parent.ExternalRef == nil {
		return DependencyLink{}, false
	}
	childNumber, ok := s.IssueNumberFromRef(*issue.ExternalRef)
	if !ok {
		return DependencyLink{}, false
	}
	parentNumber, ok := s.IssueNumberFromRef(*parent.ExternalRef)
	if !ok || parentNumber == childNumber {
		return DependencyLink{}, false
	}
	return DependencyLink{
		FromNumber:  parentNumber,
		ToNumber:    childNumber,
		LinkType:    githubLinkSubIssue,
		FromBeadsID: parent.ID,
		ToBeadsID:   issue.ID,
	}, true
}

// BlockedByLinkFromBeadsDependency converts one beads "blocks" dependency into
// a GitHub blocked_by link. For a beads blocks edge issue -> dep (issue
// depends on / is blocked by dep), GitHub records: issue is blocked_by dep.
// Both refs must resolve inside this scope's repository.
func (s RefScope) BlockedByLinkFromBeadsDependency(issue *types.Issue, dep *types.IssueWithDependencyMetadata) (DependencyLink, bool) {
	if issue == nil || dep == nil || issue.ExternalRef == nil || dep.ExternalRef == nil {
		return DependencyLink{}, false
	}
	if dep.DependencyType != types.DepBlocks {
		return DependencyLink{}, false
	}
	issueNumber, ok := s.IssueNumberFromRef(*issue.ExternalRef)
	if !ok {
		return DependencyLink{}, false
	}
	depNumber, ok := s.IssueNumberFromRef(*dep.ExternalRef)
	if !ok || depNumber == issueNumber {
		return DependencyLink{}, false
	}
	return DependencyLink{
		FromNumber:  issueNumber,
		ToNumber:    depNumber,
		LinkType:    githubLinkBlockedBy,
		FromBeadsID: issue.ID,
		ToBeadsID:   dep.ID,
	}, true
}

type githubLinkKey struct {
	FromNumber int
	ToNumber   int
	LinkType   string
}

func (l DependencyLink) key() githubLinkKey {
	return githubLinkKey{FromNumber: l.FromNumber, ToNumber: l.ToNumber, LinkType: l.LinkType}
}

type githubLinkSourceKey struct {
	Number   int
	LinkType string
}

// DeduplicateLinks removes duplicate desired GitHub links and returns them in
// a deterministic order.
func DeduplicateLinks(links []DependencyLink) []DependencyLink {
	if len(links) == 0 {
		return nil
	}
	result := make([]DependencyLink, 0, len(links))
	seen := make(map[githubLinkKey]struct{}, len(links))
	for _, link := range links {
		key := link.key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, link)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].FromNumber != result[j].FromNumber {
			return result[i].FromNumber < result[j].FromNumber
		}
		if result[i].ToNumber != result[j].ToNumber {
			return result[i].ToNumber < result[j].ToNumber
		}
		return result[i].LinkType < result[j].LinkType
	})
	return result
}

// PushLinks creates missing GitHub relationships (sub-issues, issue
// dependencies) for the desired link set. It is additive and idempotent:
// each source issue's current relationships of the relevant type are fetched
// once and consulted before any create call, so re-running a sync does not
// re-POST relationships that already exist. Stale remote relationships are
// left untouched.
func (r *LinkResolver) PushLinks(ctx context.Context, desired []DependencyLink, opts PushLinkOptions) PushLinkResult {
	if r == nil || r.Client == nil {
		return PushLinkResult{Errors: []error{fmt.Errorf("GitHub link resolver has no client")}}
	}

	desired = DeduplicateLinks(desired)
	if len(desired) == 0 {
		return PushLinkResult{}
	}

	sources := make(map[githubLinkSourceKey]*githubLinkSourceState)
	idByNumber := make(map[int]int)
	// One source issue can carry both link types, so the 404 classification
	// probe below is cached by issue number rather than by source key.
	missingByNumber := make(map[int]bool)
	var result PushLinkResult

	for _, link := range desired {
		srcKey := githubLinkSourceKey{Number: link.FromNumber, LinkType: link.LinkType}
		state, ok := sources[srcKey]
		if !ok {
			// One list call per (source issue, link type), and one error per
			// failed source rather than one per link hanging off it.
			targets, err := r.fetchCurrentTargets(ctx, link.FromNumber, link.LinkType)
			state = &githubLinkSourceState{targets: targets}
			if err != nil {
				state.failed = true
				if IsNotFound(err) {
					// A 404 here has two causes that need different reporting:
					// the relationship API is absent on this host, or the
					// source issue itself is gone — deleted, renumbered, or
					// hidden from this token. Reading the issue separates
					// them. Only a clean 404 from that read proves the issue
					// is the missing half; anything else (including a probe
					// that succeeds or fails for another reason) leaves the
					// skip classified as an unavailable endpoint.
					state.missing = r.sourceIssueMissing(ctx, link.FromNumber, missingByNumber)
					state.notFound = !state.missing
				} else {
					result.Errors = append(result.Errors, fmt.Errorf("fetch GitHub %s for #%d: %w", link.LinkType, link.FromNumber, err))
				}
			}
			sources[srcKey] = state
		}
		if state.failed {
			switch {
			case state.missing:
				result.SourceMissing++
			case state.notFound:
				result.UnsupportedSkipped++
			}
			continue
		}
		current := state.targets

		// current is keyed by issue number (present on the list response), so
		// the existence check needs no extra API call. The target's internal
		// numeric ID is only resolved when we're about to actually create the
		// link.
		if _, exists := current[link.ToNumber]; exists {
			continue
		}

		if opts.DryRun {
			if opts.OnPlan != nil {
				opts.OnPlan(link)
			}
			result.Created++
			continue
		}

		targetID, ok := idByNumber[link.ToNumber]
		if !ok {
			issue, err := r.Client.FetchIssueByNumber(ctx, link.ToNumber)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Errorf("resolve GitHub issue #%d: %w", link.ToNumber, err))
				continue
			}
			targetID = issue.ID
			idByNumber[link.ToNumber] = targetID
		}

		var err error
		switch link.LinkType {
		case githubLinkSubIssue:
			err = r.Client.AddSubIssue(ctx, link.FromNumber, targetID)
		case githubLinkBlockedBy:
			err = r.Client.AddBlockedBy(ctx, link.FromNumber, targetID)
		default:
			continue
		}
		if err != nil {
			if IsNotFound(err) {
				// Unambiguously the endpoint here, unlike the list call above:
				// this source issue's relationships listed successfully and
				// the target was just fetched by number, so neither issue can
				// be the missing half. Counted, not error-spammed.
				result.UnsupportedSkipped++
				continue
			}
			result.Errors = append(result.Errors, fmt.Errorf("create GitHub %s link #%d -> #%d: %w", link.LinkType, link.FromNumber, link.ToNumber, err))
			continue
		}
		current[link.ToNumber] = struct{}{}
		result.Created++
	}

	return result
}

// githubLinkSourceState caches one source issue's existing relationships of a
// single link type, or the fact that listing them failed. On a 404, notFound
// and missing record which of the two causes the probe settled on.
type githubLinkSourceState struct {
	targets  map[int]struct{}
	failed   bool
	notFound bool
	missing  bool
}

// sourceIssueMissing reports whether issue number itself 404s, which is what
// separates a stale local ref from a host whose relationship endpoints are
// absent. Results are memoized in cache so a source issue carrying both link
// types is probed once.
func (r *LinkResolver) sourceIssueMissing(ctx context.Context, number int, cache map[int]bool) bool {
	if missing, ok := cache[number]; ok {
		return missing
	}
	_, err := r.Client.FetchIssueByNumber(ctx, number)
	missing := err != nil && IsNotFound(err)
	cache[number] = missing
	return missing
}

func (r *LinkResolver) fetchCurrentTargets(ctx context.Context, number int, linkType string) (map[int]struct{}, error) {
	var issues []Issue
	var err error
	switch linkType {
	case githubLinkSubIssue:
		issues, err = r.Client.ListSubIssues(ctx, number)
	case githubLinkBlockedBy:
		issues, err = r.Client.ListBlockedBy(ctx, number)
	default:
		return nil, fmt.Errorf("unknown GitHub link type %q", linkType)
	}
	if err != nil {
		return nil, err
	}
	result := make(map[int]struct{}, len(issues))
	for _, iss := range issues {
		result[iss.Number] = struct{}{}
	}
	return result, nil
}
