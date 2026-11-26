package gitea

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

const giteaLinkBlockedBy = "blocked_by"

// canonicalURLHostAndPath normalizes a Gitea/Forgejo URL into its canonical
// lowercased host (stripping default :80/:443 ports and bracketing IPv6) and
// lowercased path without repeated or trailing slashes.
func canonicalURLHostAndPath(raw string) (host, path string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return "", "", false
	}
	scheme := strings.ToLower(u.Scheme)
	host = strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" {
		isDefaultHTTP := (scheme == "http" || scheme == "") && port == "80"
		isDefaultHTTPS := (scheme == "https" || scheme == "") && port == "443"
		if !isDefaultHTTP && !isDefaultHTTPS {
			host += ":" + port
		}
	}
	path = strings.ToLower(u.Path)
	for strings.Contains(path, "//") {
		path = strings.ReplaceAll(path, "//", "/")
	}
	return host, strings.TrimSuffix(path, "/"), true
}

// RefScope resolves beads external refs to issue numbers in exactly one
// Gitea/Forgejo repository.
type RefScope struct {
	baseHost string
	basePath string
	owner    string
	repo     string
	aliases  []string
}

// NewRefScope builds a ref scope for a repository reachable at baseURL.
func NewRefScope(baseURL, owner, repo string) RefScope {
	host, basePath, _ := canonicalURLHostAndPath(NormalizeBaseURL(baseURL))
	return RefScope{
		baseHost: host,
		basePath: basePath,
		owner:    strings.ToLower(strings.TrimSpace(owner)),
		repo:     strings.ToLower(strings.TrimSpace(repo)),
	}
}

func (s RefScope) withNames(fullNames ...string) RefScope {
	for _, fullName := range fullNames {
		owner, repo, ok := strings.Cut(strings.TrimSpace(fullName), "/")
		if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") || s.namesRepository(owner, repo) {
			continue
		}
		s.aliases = append(slices.Clip(s.aliases), strings.ToLower(owner+"/"+repo))
	}
	return s
}

func (s RefScope) namesRepository(owner, repo string) bool {
	if strings.EqualFold(owner, s.owner) && strings.EqualFold(repo, s.repo) {
		return true
	}
	return len(s.aliases) > 0 && slices.Contains(s.aliases, strings.ToLower(owner+"/"+repo))
}

func (s RefScope) matchesBaseHostAndPath(refHost, refPath string) (relPath string, ok bool) {
	if s.baseHost == "" || refHost != s.baseHost {
		return "", false
	}
	if s.basePath == "" {
		return refPath, true
	}
	if refPath == s.basePath {
		return "", true
	}
	if strings.HasPrefix(refPath, s.basePath+"/") {
		return strings.TrimPrefix(refPath, s.basePath), true
	}
	return "", false
}

func (s RefScope) issueURL(ref string) (owner, repo string, number int, ok bool) {
	refHost, refPath, parsed := canonicalURLHostAndPath(ref)
	if !parsed {
		return "", "", 0, false
	}
	relPath, matched := s.matchesBaseHostAndPath(refHost, refPath)
	if !matched {
		return "", "", 0, false
	}
	return splitIssueURLPath(relPath)
}

// splitIssueURLPath extracts owner, repo, and issue number from either an HTML
// path (/{owner}/{repo}/issues/{number}) or a REST path
// (/api/v1/repos/{owner}/{repo}/issues/{number}).
func splitIssueURLPath(path string) (owner, repo string, number int, ok bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 4 {
		return "", "", 0, false
	}
	last := len(segments) - 1
	if segments[last-1] != "issues" {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(segments[last])
	if err != nil || n <= 0 {
		return "", "", 0, false
	}
	owner, repo = segments[last-3], segments[last-2]
	if owner == "" || repo == "" {
		return "", "", 0, false
	}
	// Accept /{owner}/{repo}/issues/{n} or /api/v1/repos/{owner}/{repo}/issues/{n}
	if len(segments) == 4 {
		return owner, repo, n, true
	}
	if len(segments) == 7 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "repos" {
		return owner, repo, n, true
	}
	return "", "", 0, false
}

// IssueNumberFromRef extracts a repository-scoped issue number from ref when
// it addresses this scope's repository.
func (s RefScope) IssueNumberFromRef(ref string) (int, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || s.owner == "" || s.repo == "" {
		return 0, false
	}
	if m := giteaRepoShorthandPattern.FindStringSubmatch(ref); len(m) == 4 {
		if !s.namesRepository(m[1], m[2]) {
			return 0, false
		}
		n, err := strconv.Atoi(m[3])
		return n, err == nil && n > 0
	}
	if m := giteaShorthandPattern.FindStringSubmatch(ref); len(m) >= 2 {
		n, err := strconv.Atoi(m[1])
		return n, err == nil && n > 0
	}
	owner, repo, number, ok := s.issueURL(ref)
	if !ok || !s.namesRepository(owner, repo) {
		return 0, false
	}
	return number, true
}

func (s RefScope) otherRepository(ref string) (string, bool) {
	if s.owner == "" || s.repo == "" {
		return "", false
	}
	owner, repo, _, ok := s.issueURL(ref)
	if !ok || s.namesRepository(owner, repo) {
		return "", false
	}
	return strings.ToLower(owner + "/" + repo), true
}

// DependencyLink represents a Gitea/Forgejo blocked_by relationship derived from a beads blocks dependency.
type DependencyLink struct {
	FromNumber  int
	ToNumber    int
	LinkType    string
	FromBeadsID string
	ToBeadsID   string
}

// PushLinkOptions configures dependency link push behavior.
type PushLinkOptions struct {
	DryRun bool
	OnPlan func(DependencyLink)
}

// PushLinkResult summarizes a PushLinks pass.
type PushLinkResult struct {
	Created                     int
	DependenciesDisabledSkipped int
	SourceMissing               int
	Errors                      []error
}

// LinkResolver handles Gitea/Forgejo issue dependency convergence for one repository.
type LinkResolver struct {
	Client *Client
	scope  RefScope
}

// NewLinkResolver creates a dependency link resolver bound to client's repository.
func NewLinkResolver(client *Client) *LinkResolver {
	r := &LinkResolver{Client: client}
	if client != nil {
		r.scope = NewRefScope(client.BaseURL, client.Owner, client.Repo)
	}
	return r
}

// Scope returns the RefScope used by this resolver.
func (r *LinkResolver) Scope() RefScope {
	if r == nil {
		return RefScope{}
	}
	return r.scope
}

// BlockedByLinkFromBeadsDependency converts one beads "blocks" dependency into a
// Gitea/Forgejo blocked_by link when both endpoints resolve within s.
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
		LinkType:    giteaLinkBlockedBy,
		FromBeadsID: issue.ID,
		ToBeadsID:   dep.ID,
	}, true
}

type giteaLinkKey struct {
	FromNumber int
	ToNumber   int
	LinkType   string
}

// DeduplicateLinks removes duplicate desired links and returns them sorted deterministically.
func DeduplicateLinks(links []DependencyLink) []DependencyLink {
	if len(links) == 0 {
		return nil
	}
	result := make([]DependencyLink, 0, len(links))
	seen := make(map[giteaLinkKey]struct{}, len(links))
	for _, link := range links {
		k := giteaLinkKey{FromNumber: link.FromNumber, ToNumber: link.ToNumber, LinkType: link.LinkType}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
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

type giteaLinkSourceState struct {
	targets  map[int]struct{}
	failed   bool
	disabled bool
	missing  bool
}

// PushLinks creates missing blocked_by relationships in Gitea/Forgejo.
// 403/404 responses caused by disabled issue dependencies are counted in
// DependenciesDisabledSkipped rather than failing the sync.
func (r *LinkResolver) PushLinks(ctx context.Context, desired []DependencyLink, opts PushLinkOptions) PushLinkResult {
	if r == nil || r.Client == nil {
		return PushLinkResult{Errors: []error{fmt.Errorf("Gitea link resolver has no client")}}
	}
	desired = DeduplicateLinks(desired)
	if len(desired) == 0 {
		return PushLinkResult{}
	}

	sources := make(map[int]*giteaLinkSourceState)
	missingByNumber := make(map[int]bool)
	var result PushLinkResult

	for _, link := range desired {
		if link.LinkType != "" && link.LinkType != giteaLinkBlockedBy {
			continue
		}
		state, ok := sources[link.FromNumber]
		if !ok {
			issues, err := r.Client.ListBlockedBy(ctx, link.FromNumber)
			state = &giteaLinkSourceState{}
			if err != nil {
				state.failed = true
				switch {
				case IsForbidden(err):
					state.disabled = true
				case IsNotFound(err):
					state.missing = r.sourceIssueMissing(ctx, link.FromNumber, missingByNumber)
					state.disabled = !state.missing
				default:
					result.Errors = append(result.Errors, fmt.Errorf("fetch Gitea dependencies for #%d: %w", link.FromNumber, err))
				}
			} else {
				state.targets = make(map[int]struct{}, len(issues))
				for _, iss := range issues {
					state.targets[iss.Number] = struct{}{}
				}
			}
			sources[link.FromNumber] = state
		}

		if state.failed {
			switch {
			case state.missing:
				result.SourceMissing++
			case state.disabled:
				result.DependenciesDisabledSkipped++
			}
			continue
		}

		if _, exists := state.targets[link.ToNumber]; exists {
			continue
		}

		if opts.DryRun {
			if opts.OnPlan != nil {
				opts.OnPlan(link)
			}
			result.Created++
			continue
		}

		err := r.Client.AddBlockedBy(ctx, link.FromNumber, r.Client.Owner, r.Client.Repo, link.ToNumber)
		if err != nil {
			if IsForbidden(err) || IsNotFound(err) {
				result.DependenciesDisabledSkipped++
				continue
			}
			result.Errors = append(result.Errors, fmt.Errorf("create Gitea blocked_by link #%d -> #%d: %w", link.FromNumber, link.ToNumber, err))
			continue
		}
		state.targets[link.ToNumber] = struct{}{}
		result.Created++
	}

	return result
}

func (r *LinkResolver) sourceIssueMissing(ctx context.Context, number int, cache map[int]bool) bool {
	if missing, ok := cache[number]; ok {
		return missing
	}
	iss, err := r.Client.FetchIssueByNumber(ctx, number)
	missing := (err == nil && iss == nil) || (err != nil && IsNotFound(err))
	cache[number] = missing
	return missing
}

func matchKeyFromRef(baseURL, defaultOwner, defaultRepo, ref string) string {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return ""
	}
	host, basePath, hasBase := canonicalURLHostAndPath(NormalizeBaseURL(baseURL))
	if m := giteaRepoShorthandPattern.FindStringSubmatch(trimmed); len(m) == 4 {
		if hasBase {
			return fmt.Sprintf("gt://%s%s/%s/%s#%s", host, basePath, strings.ToLower(m[1]), strings.ToLower(m[2]), m[3])
		}
		return fmt.Sprintf("gitea:%s/%s#%s", strings.ToLower(m[1]), strings.ToLower(m[2]), m[3])
	}
	if m := giteaShorthandPattern.FindStringSubmatch(trimmed); len(m) >= 2 {
		if hasBase && defaultOwner != "" && defaultRepo != "" {
			return fmt.Sprintf("gt://%s%s/%s/%s#%s", host, basePath, strings.ToLower(defaultOwner), strings.ToLower(defaultRepo), m[1])
		}
		return trimmed
	}
	refHost, refPath, ok := canonicalURLHostAndPath(trimmed)
	if !ok {
		return ""
	}
	relPath := refPath
	prefix := ""
	if hasBase && refHost == host && basePath != "" && (refPath == basePath || strings.HasPrefix(refPath, basePath+"/")) {
		relPath = strings.TrimPrefix(refPath, basePath)
		prefix = basePath
	}
	owner, repo, number, ok := splitIssueURLPath(relPath)
	if !ok {
		return ""
	}
	return fmt.Sprintf("gt://%s%s/%s/%s#%d", refHost, prefix, owner, repo, number)
}
