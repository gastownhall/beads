package github

import (
	"context"
	"fmt"
	"net/url"
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
	return strings.ToLower(u.Host)
}

// String names the repository refs must point at, for warnings.
func (s RefScope) String() string {
	return s.webHost + "/" + s.owner + "/" + s.repo
}

// IssueNumberFromRef extracts a repository-scoped GitHub issue number from a
// beads external ref, but only when the ref points at this scope's repository.
// A full issue URL must match the configured host and owner/repo; the
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

	u, err := url.Parse(ref)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return 0, false
	}
	if host := strings.ToLower(u.Host); host != s.apiHost && host != s.webHost {
		return 0, false
	}

	owner, repo, number, ok := splitIssueURLPath(u.Path)
	if !ok {
		return 0, false
	}
	if !strings.EqualFold(owner, s.owner) || !strings.EqualFold(repo, s.repo) {
		return 0, false
	}
	return number, true
}

// splitIssueURLPath pulls owner, repo, and issue number out of a GitHub issue
// URL path. It accepts exactly the HTML form (/{owner}/{repo}/issues/42) and
// the REST form (/repos/{owner}/{repo}/issues/42, optionally behind a GitHub
// Enterprise /api/v3 prefix). Anything else, such as GitLab's
// /{group}/{project}/-/issues/42 or a deeper path, is rejected.
func splitIssueURLPath(path string) (owner, repo string, number int, ok bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case len(segments) == 4:
	case len(segments) == 5 && segments[0] == "repos":
		segments = segments[1:]
	case len(segments) == 7 && segments[0] == "api" && segments[1] == "v3" && segments[2] == "repos":
		segments = segments[3:]
	default:
		return "", "", 0, false
	}
	if segments[2] != "issues" {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(segments[3])
	if err != nil || n <= 0 {
		return "", "", 0, false
	}
	owner, repo = segments[0], segments[1]
	if owner == "" || repo == "" {
		return "", "", 0, false
	}
	return owner, repo, n, true
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

// PushLinkResult summarizes a PushLinks pass. A 404 from a relationship
// endpoint means one of two things, which PushLinks tells apart by fetching
// the source issue itself:
//
//   - The source issue is gone (deleted or transferred). Only that issue's
//     links are skipped; its number is listed once in MissingSources.
//   - The issue exists, so the endpoint itself is absent (older GitHub
//     Enterprise Server, or the feature is off). That link type is disabled
//     for the rest of the pass: Unsupported lists the disabled types in the
//     order they were hit, and UnsupportedSkipped counts the links dropped
//     because of it, so the caller can warn once per type.
//
// Errors holds genuine failures, at most one per source issue.
type PushLinkResult struct {
	Created            int
	UnsupportedSkipped int
	Unsupported        []string
	MissingSources     []int
	Errors             []error
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
func (t *Tracker) PushLinks(ctx context.Context, desired []DependencyLink, opts PushLinkOptions) PushLinkResult {
	if t == nil || t.client == nil {
		return PushLinkResult{Errors: []error{fmt.Errorf("GitHub tracker not initialized")}}
	}

	desired = DeduplicateLinks(desired)
	if len(desired) == 0 {
		return PushLinkResult{}
	}

	sources := make(map[githubLinkSourceKey]*githubLinkSourceState)
	idByNumber := make(map[int]int)
	unsupported := make(map[string]bool)
	var result PushLinkResult
	markUnsupported := func(linkType string) {
		if !unsupported[linkType] {
			unsupported[linkType] = true
			result.Unsupported = append(result.Unsupported, linkType)
		}
		result.UnsupportedSkipped++
	}
	// sourceGone caches whether a source issue answered 404 on a direct
	// fetch, so a missing issue is probed once however many links hang off it.
	sourceGone := make(map[int]bool)
	// handleNotFound classifies a relationship-endpoint 404 for link and
	// reports whether the link's source issue must be skipped for the rest of
	// the pass. A missing source only skips that issue; an existing one means
	// the endpoint is unsupported, which disables the whole link type.
	handleNotFound := func(link DependencyLink) (skipSource bool) {
		gone, probed := sourceGone[link.FromNumber]
		if !probed {
			_, err := t.client.FetchIssueByNumber(ctx, link.FromNumber)
			switch {
			case IsNotFound(err):
				gone = true
				result.MissingSources = append(result.MissingSources, link.FromNumber)
			case err != nil:
				result.Errors = append(result.Errors, fmt.Errorf("check GitHub issue #%d after %s 404: %w", link.FromNumber, link.LinkType, err))
				return true
			}
			sourceGone[link.FromNumber] = gone
		}
		if gone {
			return true
		}
		markUnsupported(link.LinkType)
		return false
	}

	for _, link := range desired {
		if unsupported[link.LinkType] {
			result.UnsupportedSkipped++
			continue
		}
		srcKey := githubLinkSourceKey{Number: link.FromNumber, LinkType: link.LinkType}
		state, ok := sources[srcKey]
		if !ok {
			// One list call per (source issue, link type), and one error per
			// failed source rather than one per link hanging off it.
			targets, err := t.fetchCurrentTargets(ctx, link.FromNumber, link.LinkType)
			state = &githubLinkSourceState{targets: targets}
			switch {
			case IsNotFound(err):
				state.failed = true
				if !handleNotFound(link) {
					// Unsupported type: later links of this type stop at the
					// check at the top of the loop.
					continue
				}
			case err != nil:
				state.failed = true
				result.Errors = append(result.Errors, fmt.Errorf("fetch GitHub %s for #%d: %w", link.LinkType, link.FromNumber, err))
			}
			sources[srcKey] = state
		}
		if state.failed {
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
			issue, err := t.client.FetchIssueByNumber(ctx, link.ToNumber)
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
			err = t.client.AddSubIssue(ctx, link.FromNumber, targetID)
		case githubLinkBlockedBy:
			err = t.client.AddBlockedBy(ctx, link.FromNumber, targetID)
		default:
			continue
		}
		if err != nil {
			if IsNotFound(err) {
				// Same classification as a 404 on the list call. The target's
				// ID was just resolved, so the target itself exists.
				if handleNotFound(link) {
					state.failed = true
				}
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
	targets map[int]struct{}
	failed  bool
}

func (t *Tracker) fetchCurrentTargets(ctx context.Context, number int, linkType string) (map[int]struct{}, error) {
	var issues []Issue
	var err error
	switch linkType {
	case githubLinkSubIssue:
		issues, err = t.client.ListSubIssues(ctx, number)
	case githubLinkBlockedBy:
		issues, err = t.client.ListBlockedBy(ctx, number)
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
