package github

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// RefScope resolves beads external refs to issue numbers in exactly one GitHub
// repository. GitHub issue numbers are repository-scoped, so a ref pointing at
// another repository — or at another host entirely, e.g. a GitLab or GHES URL —
// must not yield a number here: it would address, match or overwrite whichever
// unrelated issue happens to carry that number in the configured repository
// (gastownhall/beads#7781).
type RefScope struct {
	apiHost string // canonical host[:port] of the REST API base URL
	webHost string // canonical host[:port] used by issue HTML URLs
	owner   string
	repo    string
	// aliases holds the repository's other names (see withNames), each a
	// lowercase "owner/repo"; empty when unknown.
	aliases []string
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

// withNames returns a copy of s that also accepts refs naming the scope's
// repository by any of fullNames ("owner/repo"): the name GitHub reports for
// it, and former names GitHub still resolves to it. After a rename or
// transfer GitHub serves the repository under each former name by redirect,
// but the issues it returns, and so the refs BuildExternalRef stores from
// them, carry the name the repository had when they were fetched. A fullName
// that is not exactly owner/repo, or that s already accepts, adds nothing.
func (s RefScope) withNames(fullNames ...string) RefScope {
	for _, fullName := range fullNames {
		owner, repo, ok := strings.Cut(strings.TrimSpace(fullName), "/")
		if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") || s.namesRepository(owner, repo) {
			continue
		}
		// Clip so the copy never appends into the receiver's backing array.
		s.aliases = append(slices.Clip(s.aliases), strings.ToLower(owner+"/"+repo))
	}
	return s
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
	return strings.ToLower(u.Host)
}

// IssueNumberFromRef extracts a repository-scoped GitHub issue number from a
// beads external ref, but only when the ref points at this scope's repository.
// A full issue URL must match the configured host and owner/repo, or one of
// the repository's other names when the scope knows them (see withNames); the
// repo-less "github:{digits}" shorthand that BuildExternalRef emits when no
// URL is available is read as the configured repository. Everything else —
// another repository, another host, a non-GitHub tracker URL — is rejected.
func (s RefScope) IssueNumberFromRef(ref string) (int, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || s.owner == "" || s.repo == "" {
		return 0, false
	}

	if m := ghShorthandPattern.FindStringSubmatch(ref); len(m) >= 2 {
		n, err := strconv.Atoi(m[1])
		return n, err == nil && n > 0
	}

	owner, repo, number, ok := s.issueURL(ref)
	if !ok || !s.namesRepository(owner, repo) {
		return 0, false
	}
	return number, true
}

// otherRepository returns the lowercase "owner/repo" that ref names when ref
// is an issue URL on the scope's host whose repository is not one the scope
// accepts. It is false for the scope's own refs, the shorthand, other hosts
// and anything that is not an issue URL.
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

// issueURL parses ref as an issue URL on the scope's host and returns the
// owner and repository its path names, whichever repository that is, and the
// issue number.
func (s RefScope) issueURL(ref string) (owner, repo string, number int, ok bool) {
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil || u.Host == "" {
		return "", "", 0, false
	}
	if host := strings.ToLower(u.Host); host != s.apiHost && host != s.webHost {
		return "", "", 0, false
	}
	return splitIssueURLPath(u.Path)
}

// namesRepository reports whether owner/repo is the scope's repository, by
// its configured name or by one of its other names.
func (s RefScope) namesRepository(owner, repo string) bool {
	if strings.EqualFold(owner, s.owner) && strings.EqualFold(repo, s.repo) {
		return true
	}
	return len(s.aliases) > 0 && slices.Contains(s.aliases, strings.ToLower(owner+"/"+repo))
}

// splitIssueURLPath pulls owner, repo, and issue number out of a GitHub issue
// URL path. It accepts both the HTML form (/{owner}/{repo}/issues/42) and the
// REST form (/repos/{owner}/{repo}/issues/42, optionally behind a GitHub
// Enterprise /api/v3 prefix), and requires the number to be the last segment.
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
	return owner, repo, n, true
}
